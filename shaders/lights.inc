// Clustered forward lighting: the storage-buffer light list, the cluster
// grid lookup, and the per-light evaluation shared by both lighting.inc
// loops (PBR and diffuse-only).
//
// The includer must #define LIGHT_SET before including this file -- static
// pipelines share set 1 with the shadow data, skinned pipelines share set 2
// because set 1 there is the joint matrix UBO. Binding numbers are fixed
// (3, 4, 5) because they sit on the descriptor set the shadow data owns; see
// renderer/shadow.go.
//
// This replaces a LightBlock UBO struct and block that used to be
// copy-pasted into each of seven fragment shaders (lit, lit_material,
// skinned_lit, skinned_lit_material, terrain, grass, water) with one
// declaration, and replaces their fixed 32-light cap with a runtime light
// count -- see MaxLights in renderer/lights.go.

struct GpuLight {            // 48 bytes, std430
    vec4 posRange;            // xyz world position, w range (<=0 = disabled)
    vec4 color;               // rgb linear colour*intensity, a = cos(inner half-angle)
    vec4 dirCone;             // xyz unit direction the light points, w = cos(outer half-angle)
};                            // dirCone.xyz == 0 => point light, cone ignored

layout(std430, set = LIGHT_SET, binding = 3) readonly buffer LightBuffer {
    uvec4 grid;      // x,y,z cluster counts; w = number of lights in lights[]
    vec4  zParams;   // x = slice scale, y = slice bias, z = per-cell light cap, w reserved
    vec4  screen;    // x,y framebuffer px; z,w = grid.xy / framebuffer px
    uvec4 flags;     // x bit0 = brute force, bit1 = debug heatmap; y,z,w reserved
    GpuLight lights[];
} lb;

struct Cell { uint offset; uint count; };
layout(std430, set = LIGHT_SET, binding = 4) readonly buffer ClusterGrid  { Cell cells[]; } cg;
layout(std430, set = LIGHT_SET, binding = 5) readonly buffer LightIndices { uint indices[]; } li;

// clusterIndex maps a fragment to a cell index, matching the CPU binner
// exactly (including the Vulkan Y-flip baked into the projection, which the
// binner has to replicate since gl_FragCoord is already past it here).
uint clusterIndex(vec2 fragCoordXY, float viewDepth) {
    uvec2 xy = uvec2(fragCoordXY * lb.screen.zw);
    xy = min(xy, lb.grid.xy - uvec2(1u));
    float s = floor(log(max(viewDepth, 1e-4)) * lb.zParams.x + lb.zParams.y);
    uint z = uint(clamp(s, 0.0, float(lb.grid.z - 1u)));
    return xy.x + xy.y * lb.grid.x + z * lb.grid.x * lb.grid.y;
}

// cellLightCount always does the clustered lookup, independent of flags.x --
// the debug heatmap exists to show what the grid holds, which brute force
// mode (flags.x bit0) deliberately ignores for actual shading.
uint cellLightCount(vec2 fragCoordXY, float viewDepth) {
    return cg.cells[clusterIndex(fragCoordXY, viewDepth)].count;
}

// lightHeatmap is a plain blue -> green -> red ramp of a cell's light count.
// It exists to answer "which cells are hot", not to look good, so it stays
// this simple rather than chasing a perceptually uniform ramp.
//
// Full scale is zParams.z, the per-cell cap: red means the cell is at the
// point where it starts dropping lights, and that is the only reading worth
// colouring by. A fixed full scale of 16 looked right against an empty grid
// and is useless against a filled one -- a floor of 400 short-range lamps
// peaks at 67 lights in a cell and a ring of 1024 long-range ones at the cap
// itself, so everything but the sky came back saturated red.
vec3 lightHeatmap(uint count) {
    float t = clamp(float(count) / max(lb.zParams.z, 1.0), 0.0, 1.0);
    vec3 lo = vec3(0.0, 0.0, 1.0);
    vec3 mid = vec3(0.0, 1.0, 0.0);
    vec3 hi = vec3(1.0, 0.0, 0.0);
    return t < 0.5 ? mix(lo, mid, t * 2.0) : mix(mid, hi, (t - 0.5) * 2.0);
}

// LightRange abstracts over brute-force vs. clustered iteration so both
// lighting.inc loops share one iteration order instead of each re-deciding
// how to walk the light list.
struct LightRange { uint offset; uint count; bool brute; };

LightRange resolveLightRange(vec2 fragCoordXY, float viewDepth) {
    LightRange r;
    if ((lb.flags.x & 1u) != 0u) {
        // Brute force: the reference path. Loops every uploaded light,
        // ignoring cg/li entirely, so a scene lights correctly even before
        // anything fills the grid, and so a clustered result can be checked
        // against a path whose correctness never depends on binning being
        // right.
        r.offset = 0u;
        r.count = lb.grid.w;
        r.brute = true;
        return r;
    }
    uint idx = clusterIndex(fragCoordXY, viewDepth);
    r.offset = cg.cells[idx].offset;
    r.count = cg.cells[idx].count;
    r.brute = false;
    return r;
}

GpuLight lightAt(LightRange r, uint i) {
    if (r.brute) {
        return lb.lights[i];
    }
    return lb.lights[li.indices[r.offset + i]];
}

// lightSpotFactor is the smooth cone falloff for a spot light. dirCone.xyz
// == 0 means omnidirectional -- the zero value of the new fields keeps
// meaning what it always meant for a plain point light, so this returns 1.0
// (an explicit no-op factor) rather than skipping the multiply below: a
// multiply by exactly 1.0 introduces no rounding of its own, which is what
// keeps a plain point light's attenuation as close as possible to what it
// was before spot lights existed.
float lightSpotFactor(GpuLight light, vec3 L) {
    vec3 dir = light.dirCone.xyz;
    if (dir == vec3(0.0)) {
        return 1.0;
    }
    float cosOuter = light.dirCone.w;
    float cosInner = light.color.a;
    float cosAngle = dot(dir, -L);
    return smoothstep(cosOuter, cosInner, cosAngle);
}

// lightIrradiance is the one routine both lighting.inc loops call per light:
// the same point-light falloff math the old inline loops used, times the
// spot cone factor. L is written with the unit direction toward the light,
// for callers that need it for specular; it is left at the zero vector when
// the light contributes nothing, matching the "unnormalized" state the old
// inline loops left unused variables in.
vec3 lightIrradiance(GpuLight light, vec3 worldPos, vec3 N, out vec3 L) {
    vec3 toLight = light.posRange.xyz - worldPos;
    float dist = length(toLight);
    float range = light.posRange.w;
    if (range <= 0.0 || dist >= range) {
        L = vec3(0.0);
        return vec3(0.0);
    }
    float ratio = dist / range;
    float atten = (1.0 - ratio * ratio);
    atten = atten * atten;
    L = normalize(toLight);
    float NdotL = clamp(dot(N, L), 0.0, 1.0);
    float spot = lightSpotFactor(light, L);
    return NdotL * atten * spot * light.color.rgb;
}
