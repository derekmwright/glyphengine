#version 450
#extension GL_GOOGLE_include_directive : require

// Grass-specific variant of lit.frag: outputs the distance fade as alpha so
// alpha-to-coverage dissolves distant blades through MSAA coverage instead of
// leaving shimmering sub-pixel slivers ("TV static").

layout(location = 0) in vec3 fragColor;
layout(location = 1) in vec3 fragWorldPos;
layout(location = 2) in vec3 fragWorldNormal;
layout(location = 3) in vec2 fragUV;
layout(location = 4) in vec3 fragShadowPos;
layout(location = 5) in float fragFade;

layout(set = 0, binding = 0) uniform sampler2D texSampler;

// Shadow at set 1 for static lit pipeline: cascade VPs + 2-layer array map
layout(set = 1, binding = 0) uniform ShadowData {
    mat4 cascadeVP[2];
} shadow;
layout(set = 1, binding = 1) uniform sampler2DArrayShadow shadowMap;
layout(set = 1, binding = 2) uniform samplerCube pointShadowMap;

// Unshadowed point lights UBO at set 1, binding 3
struct UPointLight { vec4 posRange; vec4 color; };
layout(set = 1, binding = 3) uniform LightBlock {
    int numLights;
    UPointLight lights[32];
} lb;

layout(push_constant) uniform PushConstants {
    mat4 mvp;
    mat4 model;
    vec4 tint;
    vec4 sunDir;    // xyz = direction toward sun
    vec4 sunColor;  // rgb
    vec4 pointPos;  // xyz = position, w = range
    vec4 pointColor;// rgb, w = roughness
    vec4 ambient;   // rgb, w = metallic
    vec4 cameraPos; // xyz = eye position
    vec4 fog;       // x = height falloff, y = base height, zw = real sun horizontal
} pc;

layout(location = 0) out vec4 outColor;

#include "lighting.inc"

void main() {
    vec4 texSample = texture(texSampler, fragUV);
    if (texSample.a < 0.5) discard; // alpha test for foliage cutout
    vec3 baseColor = fragColor * texSample.rgb;

    // Stable analytic normal, biased heavily toward vertical. Derivative-based
    // normals (dFdx/dFdy) are per-quad noise on sub-pixel blades, and combined
    // with grazing-angle Fresnel they produce bright speckle at dawn/dusk —
    // the "TV static" artifact. The per-vertex blade normal is smooth, and the
    // up-bias gives near-uniform lighting across a patch of blades.
    vec3 N = normalize(mix(normalize(fragWorldNormal), vec3(0.0, 1.0, 0.0), 0.7));

    // Diffuse-only: grass gets no specular/Fresnel at all.
    vec3 lit = evalLightingDiffuse(baseColor, N, fragWorldPos, fragShadowPos);

    // Alpha drives MSAA coverage (alpha-to-coverage), dissolving distant blades.
    outColor = vec4(applyFog(lit, fragWorldPos), fragFade);
}
