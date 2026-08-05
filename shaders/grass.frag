#version 450
#extension GL_GOOGLE_include_directive : require

// Grass-specific variant of lit.frag: outputs the distance fade as alpha so
// alpha-to-coverage dissolves distant blades through MSAA coverage instead of
// leaving shimmering sub-pixel slivers ("TV static").

// centroid, and it matters more than it looks. The flora vertex colour is a
// base-to-tip gradient authored 0..1; extrapolated at MSAA sample positions
// outside a sub-pixel blade it leaves that range, and a negative albedo shades
// to a black pixel. That was thousands of dark specks along the horizon --
// 1105 in the horizon band at morning, 3516 at night, against 8 and 272 with
// this. It also removes the bright specks at the other end of the gradient,
// which a 0.90 ceiling was previously papering over at a cost of RMS 0.014 to
// midday tip brightness.
layout(location = 0) centroid in vec3 fragColor;
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

    // The per-vertex blade normal, as authored.
    //
    // This used to be biased 70% toward vertical, credited with stopping a
    // "TV static" speckle. It stops nothing: swept across six times of day the
    // bias changed the speck count by zero and the image by RMS 0.0003 to
    // 0.005. It also named a cause this shader does not have -- the derivative
    // normals it blamed are never computed here. The speckle is the vertex
    // colour gradient extrapolating past its range; see the centroid note above.
    vec3 N = normalize(fragWorldNormal);

    // Diffuse-only, and not as an artifact fix either: the grass draw pins
    // roughness to 1.0, so the specular lobe is already flat and restoring it
    // moves the image by RMS 0.00003 at dusk. It stays because skipping it
    // measures 0.017 ms cheaper on the grass pass.
    vec3 lit = evalLightingDiffuse(baseColor, N, fragWorldPos, fragShadowPos);

    // Alpha drives MSAA coverage (alpha-to-coverage), dissolving distant blades.
    outColor = vec4(applyFog(lit, fragWorldPos), fragFade);
}
