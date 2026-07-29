#version 450
#extension GL_GOOGLE_include_directive : require

// Material shader: the lit path plus normal, metallic-roughness, and occlusion
// maps. Shares lit.vert and the lit shadow set (set 1); only the material set
// (set 0) differs, the same way terrain.frag does.
//
// Without this, lighting is Blinn-Phong with one uniform material per object,
// which is why adding an albedo texture alone disappoints: colour varies per
// pixel while the surface still lights as one perfectly smooth material.

layout(location = 0) in vec3 fragColor;
layout(location = 1) in vec3 fragWorldPos;
layout(location = 2) in vec3 fragWorldNormal;
layout(location = 3) in vec2 fragUV;
layout(location = 4) in vec3 fragShadowPos;

// set 0: the material. Every slot is always bound -- unsupplied maps get a
// neutral 1x1 -- because a descriptor a shader statically references has to be
// written. The flags below are what actually decides whether one is read.
layout(set = 0, binding = 0) uniform sampler2D albedoTex;
layout(set = 0, binding = 1) uniform sampler2D normalTex;
layout(set = 0, binding = 2) uniform sampler2D metalRoughTex;
layout(set = 0, binding = 3) uniform sampler2D occlusionTex;

layout(set = 0, binding = 4) uniform MaterialBlock {
    vec4 maps;  // xyz = has normal / metallic-roughness / occlusion, w = occlusion strength
    vec4 scale; // xy = tangent-space normal scale, y negative flips green
} matl;

// Shadow at set 1 — identical to lit.frag so the shared shadow descriptor binds.
layout(set = 1, binding = 0) uniform ShadowData {
    mat4 cascadeVP[2];
} shadow;
layout(set = 1, binding = 1) uniform sampler2DArrayShadow shadowMap;
layout(set = 1, binding = 2) uniform samplerCube pointShadowMap;

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
    vec4 sunColor;  // rgb, w = the real sun's elevation
    vec4 pointPos;  // xyz = position, w = range
    vec4 pointColor;// rgb, w = roughness
    vec4 ambient;   // rgb, w = metallic
    vec4 cameraPos; // xyz = eye position
    vec4 fog;       // x = height falloff, y = base height, zw = real sun horizontal
} pc;

layout(location = 0) out vec4 outColor;

#include "lighting.inc"

// perturbNormal applies a tangent-space normal map using a frame derived from
// screen-space derivatives of world position and UV.
//
// The alternative is a real tangent vertex attribute, which is higher quality on
// heavily distorted UVs but touches every mesh builder, every vertex buffer, and
// every pipeline's vertex input state. This costs four derivatives and reads the
// frame out of data the rasterizer is already interpolating, so normal maps land
// without the vertex format changing at all.
//
// Standard cotangent-frame construction: solve for the tangent and bitangent
// that carry the UV gradients, with N held fixed so the map perturbs the
// interpolated normal rather than replacing it.
vec3 perturbNormal(vec3 N, vec3 worldPos, vec2 uv, vec3 mapSample, vec2 mapScale) {
    vec3 dp1 = dFdx(worldPos);
    vec3 dp2 = dFdy(worldPos);
    vec2 duv1 = dFdx(uv);
    vec2 duv2 = dFdy(uv);

    vec3 dp2perp = cross(dp2, N);
    vec3 dp1perp = cross(N, dp1);
    vec3 T = dp2perp * duv1.x + dp1perp * duv2.x;
    vec3 B = dp2perp * duv1.y + dp1perp * duv2.y;

    // A face with zero UV area leaves T and B at zero, and normalizing that
    // produces NaN -- which propagates through the lighting and paints black or
    // white garbage rather than failing visibly. Fall back to the geometric
    // normal, which is what an unmapped surface would have used anyway.
    float maxLen = max(dot(T, T), dot(B, B));
    if (maxLen < 1e-16) {
        return N;
    }

    float invMax = inversesqrt(maxLen);
    mat3 TBN = mat3(T * invMax, B * invMax, N);

    vec3 n = mapSample * 2.0 - 1.0;
    n.xy *= mapScale;
    return normalize(TBN * n);
}

void main() {
    vec4 texSample = texture(albedoTex, fragUV);
    if (texSample.a < 0.5) discard; // alpha test for foliage cutout
    vec3 baseColor = fragColor * texSample.rgb;

    // Emissive early-out: tint.w > 0 bypasses lighting (used for moon disc etc.)
    if (pc.tint.w > 0.0) {
        outColor = vec4(baseColor, 1.0);
        return;
    }

    // The per-object values stay factors that the maps multiply, which is both
    // glTF's rule and what keeps MeshRef.Metallic/.Roughness meaningful on a
    // material that supplies no metallic-roughness map.
    float metallic = pc.ambient.w;
    float roughness = pc.pointColor.w;
    if (matl.maps.y > 0.5) {
        vec4 mr = texture(metalRoughTex, fragUV);
        roughness *= mr.g;
        metallic *= mr.b;
    }
    roughness = clamp(roughness, 0.04, 1.0);
    float shininess = clamp(2.0 / (roughness * roughness) - 2.0, 1.0, 2048.0);

    vec3 diffuseColor = baseColor * (1.0 - metallic);
    vec3 F0 = mix(vec3(0.04), baseColor, metallic);

    vec3 V = normalize(pc.cameraPos.xyz - fragWorldPos);
    vec3 N = normalize(fragWorldNormal);
    if (matl.maps.x > 0.5) {
        N = perturbNormal(N, fragWorldPos, fragUV, texture(normalTex, fragUV).rgb, matl.scale.xy);
    }

    float ao = 1.0;
    if (matl.maps.z > 0.5) {
        ao = mix(1.0, texture(occlusionTex, fragUV).r, matl.maps.w);
    }

    vec3 lit = evalLightingAO(diffuseColor, F0, shininess, N, V, fragWorldPos, fragShadowPos, ao);
    outColor = vec4(applyFog(lit, fragWorldPos), 1.0);
}
