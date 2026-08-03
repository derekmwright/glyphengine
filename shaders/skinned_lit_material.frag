#version 450
#extension GL_GOOGLE_include_directive : require

// Material shader for skinned geometry: skinned_lit.vert plus the same material
// maps the static path gets.
//
// The comment this replaces claimed a skinned material needed a fourth
// descriptor set. It does not. A Material replaces the plain texture at set 0,
// which is where skinned_lit.frag already binds its sampler, so the set layout
// is the one the skinned pipeline has always used: 0 = material, 1 = joints
// (vertex stage only, so this shader never names it), 2 = shadow.
//
// The shading itself is in material_shading.inc, shared with the static
// variant. Only the shadow set number differs between them.

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
layout(set = 0, binding = 4) uniform sampler2D emissiveTex;

layout(set = 0, binding = 5) uniform MaterialBlock {
    vec4 maps;     // xyz = has normal / metallic-roughness / occlusion, w = occlusion strength
    vec4 scale;    // xy = tangent-space normal scale, y negative flips green
    vec4 emissive; // rgb = emitted radiance with strength folded in, w = has emissive map
} matl;

// Shadow at set 2, because set 1 is the joint matrix UBO the vertex stage
// reads. Same contents as the static variant's set 1.
layout(set = 2, binding = 0) uniform ShadowData {
    mat4 cascadeVP[2];
} shadow;
layout(set = 2, binding = 1) uniform sampler2DArrayShadow shadowMap;
layout(set = 2, binding = 2) uniform samplerCube pointShadowMap;

struct UPointLight { vec4 posRange; vec4 color; };
layout(set = 2, binding = 3) uniform LightBlock {
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

#include "material_shading.inc"
