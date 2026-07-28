#version 450
#extension GL_GOOGLE_include_directive : require

layout(location = 0) in vec3 fragColor;
layout(location = 1) in vec3 fragWorldPos;
layout(location = 2) in vec3 fragWorldNormal;
layout(location = 3) in vec2 fragUV;
layout(location = 4) in vec3 fragShadowPos;

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
} pc;

layout(location = 0) out vec4 outColor;

#include "lighting.inc"

void main() {
    vec4 texSample = texture(texSampler, fragUV);
    if (texSample.a < 0.5) discard; // alpha test for foliage cutout
    vec3 baseColor = fragColor * texSample.rgb;

    // Emissive early-out: tint.w > 0 bypasses lighting (used for moon disc etc.)
    if (pc.tint.w > 0.0) {
        outColor = vec4(baseColor, 1.0);
        return;
    }

    float metallic = pc.ambient.w;
    float roughness = clamp(pc.pointColor.w, 0.04, 1.0);
    float shininess = 2.0 / (roughness * roughness) - 2.0;
    shininess = clamp(shininess, 1.0, 2048.0);

    vec3 diffuseColor = baseColor * (1.0 - metallic);
    vec3 F0 = mix(vec3(0.04), baseColor, metallic);

    vec3 V = normalize(pc.cameraPos.xyz - fragWorldPos);

    vec3 N;
    if (pc.tint.w < 0.0) {
        // Flat shading for double-sided foliage: compute geometric face normal
        // from screen-space derivatives. Eliminates smooth-normal gradients on
        // stylized leaf planes where each face should have uniform lighting.
        // Orient toward camera using view direction (avoids gl_FrontFacing
        // sign issues with Vulkan Y-flip projection).
        N = normalize(cross(dFdx(fragWorldPos), dFdy(fragWorldPos)));
        if (dot(N, V) < 0.0) N = -N;
    } else {
        N = normalize(fragWorldNormal);
        // For very rough materials (grass blades), bias normal toward vertical.
        // Thin geometry has rapidly varying per-face normals that cause noisy
        // lighting ("TV static"). Biasing toward up gives smooth, uniform lighting.
        float upBias = smoothstep(0.9, 1.0, roughness);
        N = normalize(mix(N, vec3(0.0, 1.0, 0.0), upBias * 0.7));
    }

    vec3 lit = evalLighting(diffuseColor, F0, shininess, N, V, fragWorldPos, fragShadowPos);
    outColor = vec4(applyFog(lit, fragWorldPos), 1.0);
}
