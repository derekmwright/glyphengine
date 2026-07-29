#version 450
#extension GL_GOOGLE_include_directive : require

// Terrain splat shader: blends three tiled detail textures (grass / path / rock)
// by per-pixel weights read from a top-down splat map. RGB channels of the splat
// map select grass / path / rock respectively. Shares lit.vert and the lit
// shadow set (set 1); only the material set (set 0) differs.

layout(location = 0) in vec3 fragColor;
layout(location = 1) in vec3 fragWorldPos;
layout(location = 2) in vec3 fragWorldNormal;
layout(location = 3) in vec2 fragUV;
layout(location = 4) in vec3 fragShadowPos;

// set 0: detail textures (tiled by fragUV) + splat weight map (top-down 0..1)
layout(set = 0, binding = 0) uniform sampler2D grassTex;
layout(set = 0, binding = 1) uniform sampler2D pathTex;
layout(set = 0, binding = 2) uniform sampler2D rockTex;
layout(set = 0, binding = 3) uniform sampler2D splatTex;

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
    vec4 sunDir;
    vec4 sunColor;
    vec4 pointPos;
    vec4 pointColor; // w = roughness
    vec4 ambient;    // w = metallic
    vec4 cameraPos;
    vec4 fog;       // x = height falloff, y = base height, zw = real sun horizontal
} pc;

layout(location = 0) out vec4 outColor;

#include "lighting.inc"

// Must match uvTileScale in cmd/tools/genterrain/main.go. The detail UV tiles
// 0..SPLAT_TILE across the terrain, so dividing recovers the 0..1 top-down
// coordinate used to sample the splat weight map.
const float SPLAT_TILE = 10.0;

void main() {
    vec2 splatUV = fragUV / SPLAT_TILE;
    vec3 w = texture(splatTex, splatUV).rgb;

    // Normalize blend weights; an empty (black) splat falls back to grass so
    // an unpainted terrain looks exactly like the old single-texture ground.
    float sum = w.r + w.g + w.b;
    w = (sum > 0.001) ? w / sum : vec3(1.0, 0.0, 0.0);

    vec3 grass = texture(grassTex, fragUV).rgb;
    vec3 path  = texture(pathTex, fragUV).rgb;
    vec3 rock  = texture(rockTex, fragUV).rgb;
    vec3 detail = grass * w.r + path * w.g + rock * w.b;

    vec3 baseColor = fragColor * detail;

    float metallic = pc.ambient.w;
    float roughness = clamp(pc.pointColor.w, 0.04, 1.0);
    float shininess = clamp(2.0 / (roughness * roughness) - 2.0, 1.0, 2048.0);

    vec3 diffuseColor = baseColor * (1.0 - metallic);
    vec3 F0 = mix(vec3(0.04), baseColor, metallic);
    vec3 V = normalize(pc.cameraPos.xyz - fragWorldPos);
    vec3 N = normalize(fragWorldNormal);

    vec3 lit = evalLighting(diffuseColor, F0, shininess, N, V, fragWorldPos, fragShadowPos);
    outColor = vec4(applyFog(lit, fragWorldPos), 1.0);
}
