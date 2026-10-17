#version 450
#extension GL_GOOGLE_include_directive : require

// Water surface: a flat grid displaced by a sum of Gerstner waves.
//
// Gerstner rather than plain sine because the horizontal term is what makes
// water look like water. A sine height field gives symmetric humps; Gerstner
// pulls vertices toward each crest as well as up, which sharpens crests and
// flattens troughs the way a real wave profile does.
//
// Vertex attributes carry water-specific data rather than their usual meaning,
// because the surface has no use for the originals and this avoids a second
// vertex format for one mesh type:
//
//   inColor   deep water colour
//   inNormal  shallow water colour -- the surface normal comes from the waves,
//             so the attribute is free (see WaterOptions in water.go)
//   inUV.x    still-water depth at this vertex, in world units, baked from the
//             heightmap at build time
//   inUV.y    the surface grid's vertex spacing in world units, also baked at
//             build time. The shader cannot otherwise know how far apart its
//             own vertices are, and that is what decides which wave components
//             this mesh is fine enough to carry.

layout(location = 0) in vec3 inPosition;
layout(location = 1) in vec3 inColor;
layout(location = 2) in vec3 inNormal;
layout(location = 3) in vec2 inUV;

layout(push_constant) uniform PushConstants {
    mat4 mvp;
    mat4 model;
    vec4 tint;      // x = time, y = amplitude, z = wavelength, w = refraction strength
    vec4 sunDir;    // xyz = direction toward sun, w = wave noise fraction
    vec4 sunColor;  // rgb, w = night factor
    vec4 pointPos;
    vec4 pointColor;
    vec4 ambient;
    vec4 cameraPos; // xyz = eye, w = fog density
} pc;

layout(location = 0) out vec3 fragDeepColor;
layout(location = 1) out vec3 fragShallowColor;
layout(location = 2) out vec3 fragWorldPos;
layout(location = 3) out vec3 fragNormal;
layout(location = 4) out float fragDepth;
layout(location = 5) out vec3 fragShadowPos;
// How far this vertex rose above still water, as a fraction of WaveAmplitude:
// the subsurface term is proportional to it. Normalised so the glow follows the
// shape of the sea rather than its size, and a game that scales its sea state
// up or down keeps the same look. Divided here because the displacement is
// computed here.
layout(location = 6) out float fragCrest;
// The foam layer's inputs: xy the rest position, which is where the persistent
// foam target is indexed (foam stays where the crest left it on the grid, not
// where the vertex happens to have been pushed this frame), and z this
// vertex's Jacobian, for the foam made this frame at full vertex resolution.
// Only the foam pipeline's fragment stage reads it.
layout(location = 7) out vec3 fragFoam;

#include "water_waves.inc"

void main() {
    float time      = pc.tint.x;
    float amplitude = pc.tint.y;
    float baseLen   = max(pc.tint.z, 0.001);
    float noiseFrac = pc.sunDir.w;

    vec3 rest = inPosition;
    vec3 pos = rest;

    // See waterShoal.
    float shoal = waterShoal(inUV.x);
    float spacing = inUV.y;

    #include "water_wave_sum.inc"

    fragNormal = normalize(vec3(-dhdx, 1.0 - fold, -dhdz));

    gl_Position      = pc.mvp * vec4(pos, 1.0);
    fragWorldPos     = (pc.model * vec4(pos, 1.0)).xyz;
    fragDeepColor    = inColor;
    fragShallowColor = inNormal;
    fragDepth        = inUV.x;
    fragCrest        = amplitude > 0.0 ? (pos.y - inPosition.y) / amplitude : 0.0;
    fragFoam         = vec3(inPosition.xz, waterJacobian(jxx, jzz, jxz));

    // Match lit.vert's normal-offset shadow bias so a shadow falling across
    // the water lines up with the same shadow on the shore beside it.
    float NdotL = dot(fragNormal, normalize(pc.sunDir.xyz));
    float sinAngle = sqrt(max(1.0 - NdotL * NdotL, 0.0));
    fragShadowPos = fragWorldPos + fragNormal * (0.08 * sinAngle + 0.02);
}
