#version 450

// Grass impostor: a camera-facing quad standing in for a blade mesh at
// distance, textured from the baked atlas.
//
// Pairs with grass.frag unchanged. That shader samples set 0 binding 0 with
// fragUV, cuts out at 0.5 and biases its normal toward vertical -- all of which
// suit a billboard as well as they suit a blade, so the impostor needs a new
// vertex stage and nothing else.
//
// No per-vertex buffer: the quad's six corners come from gl_VertexIndex, and
// binding 1 supplies the same per-instance data the mesh path uses. That is the
// whole saving -- about 340 triangles per blade become two.

// Per-instance (binding 1): xyz = world position, w = Y rotation.
layout(location = 4) in vec4 inInstance;

// The billboard's own numbers ride in the .w slots grass leaves unread: it is
// diffuse-only, so roughness and metallic reach no code, and nothing in the
// grass path looks at tint.w. Everything else here is live lighting that
// grass.frag reads, and writing any of it would light the billboards
// differently from the meshes they stand in for.
layout(push_constant) uniform PushConstants {
    mat4 vp;
    mat4 model;
    vec4 tint;       // rgb = tint, w = atlas cell count * 64 + cell index
    vec4 sunDir;     // xyz = sun direction, w = time (for wind)
    vec4 sunColor;
    vec4 pointPos;   // x = LOD max distance, y = fade start (as grass.vert)
    vec4 pointColor; // w = billboard width in world units
    vec4 ambient;    // w = billboard height in world units
    vec4 cameraPos;
} pc;

layout(location = 0) out vec3 fragColor;
layout(location = 1) out vec3 fragWorldPos;
layout(location = 2) out vec3 fragWorldNormal;
layout(location = 3) out vec2 fragUV;
layout(location = 4) out vec3 fragShadowPos;
layout(location = 5) out float fragFade;

// Two triangles. x is across the blade in [-0.5, 0.5], y is up it in [0, 1],
// which is exactly how the atlas cell is framed: base on the bottom edge.
const vec2 CORNERS[6] = vec2[6](
    vec2(-0.5, 0.0), vec2(0.5, 0.0), vec2(0.5, 1.0),
    vec2(-0.5, 0.0), vec2(0.5, 1.0), vec2(-0.5, 1.0)
);

void main() {
    vec3 base = inInstance.xyz;

    float lodMax  = pc.pointPos.x > 0.0 ? pc.pointPos.x : 80.0;
    float lodFade = pc.pointPos.y > 0.0 ? pc.pointPos.y : 50.0;

    float distToCam = distance(base, pc.cameraPos.xyz);
    if (distToCam > lodMax) {
        gl_Position = vec4(0.0);
        fragFade = 0.0;
        return;
    }

    // The same fade curve the mesh path uses, so a blade crossing the impostor
    // boundary keeps the height and coverage it already had.
    float fadeFactor = smoothstep(lodMax, lodFade, distToCam);
    fragFade = fadeFactor;

    vec2 corner = CORNERS[gl_VertexIndex];
    float width  = pc.pointColor.w;
    float height = pc.ambient.w * fadeFactor;

    // Face the camera about the vertical axis only. A fully camera-facing quad
    // would tilt as the camera pitches, and grass that leans back when you look
    // up reads as broken far more loudly than a billboard ever reads as flat.
    vec3 toCam = pc.cameraPos.xyz - base;
    toCam.y = 0.0;
    float len = length(toCam);
    vec3 right = len > 1e-4
        ? normalize(vec3(-toCam.z, 0.0, toCam.x))
        : vec3(1.0, 0.0, 0.0);

    vec3 offset = right * (corner.x * width) + vec3(0.0, corner.y * height, 0.0);

    // Wind, identical to grass.vert: the phase is per-instance and the strength
    // is proportional to height up the blade. That is what lets a billboard sway
    // like the mesh it replaces -- a quad has both a per-instance position and a
    // height, which is everything the model needs. The mesh bends as a curve
    // across its vertices where this shears, and at impostor range the
    // difference is well under a pixel.
    float time = pc.sunDir.w;
    float windStrength = (corner.y * height) * 0.06;
    float windPhase = time * 2.0 + base.x * 1.3 + base.z * 0.9;
    offset.x += sin(windPhase) * windStrength;
    offset.z += cos(windPhase * 0.7) * windStrength * 0.5;

    vec3 worldPos = base + offset;
    gl_Position = pc.vp * vec4(worldPos, 1.0);

    // Into this variant's atlas cell. v is flipped because the bake put the
    // blade's tip at the top of the cell, which is v = 0.
    // Cell index and cell count share one slot. Both are small non-negative
    // integers and a float32 holds integers exactly to 2^24, so this is lossless
    // -- and it is what the block can afford: the only alternative slots hold
    // live lighting, and the count is capped at 64 where it is packed.
    float cells = max(floor(pc.tint.w / 64.0), 1.0);
    float cell  = pc.tint.w - cells * 64.0;
    fragUV = vec2((cell + corner.x + 0.5) / cells, 1.0 - corner.y);

    fragColor = pc.tint.rgb;
    fragWorldPos = worldPos;

    // Mostly up, leaning toward the viewer. grass.frag biases whatever it gets
    // 70 percent toward vertical anyway, so this only has to be stable: a quad's
    // geometric normal would swing with the camera and make a distant field
    // change brightness as the player turns.
    fragWorldNormal = normalize(vec3(0.0, 1.0, 0.0) + (len > 1e-4 ? normalize(toCam) : vec3(0.0)) * 0.35);

    vec3 N = normalize(fragWorldNormal);
    float NdotL = dot(N, normalize(pc.sunDir.xyz));
    float sinAngle = sqrt(max(0.0, 1.0 - NdotL * NdotL));
    fragShadowPos = worldPos + N * (0.08 * sinAngle + 0.02);
}
