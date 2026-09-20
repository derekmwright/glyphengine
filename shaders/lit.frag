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
    // Environment values the fragment shaders grade with, appended
    // because the push constant block is full at 256 bytes. rgb is the
    // scotopic tint, w the strength (0 disables). Nine shaders declare this
    // block -- the seven lit ones, plus sky.frag and clouds.frag, which reach
    // the same buffer through the cloud descriptor set -- and all nine must
    // agree with renderer/shadow.go's litUBOSize.
    vec4 nightGrade;
    // The sky gradient's six palette endpoints; see atmSkyPalette in
    // atmosphere.inc for the order. They live here rather than as constants
    // in that file so the horizon colour applyFog fades geometry into and the
    // dome sky.frag draws come out of ONE buffer and cannot drift.
    vec4 skyPalette[6];
    // The in-scattering march's medium and sampling: x = the
    // Henyey-Greenstein anisotropy, y = the step count, zw spare. See
    // volInscatter in volumetric.inc, and Volumetrics in renderer/shadow.go for
    // why these are data rather than constants in this file.
    vec4 volumetric;
} shadow;
layout(set = 1, binding = 1) uniform sampler2DArrayShadow shadowMap;
layout(set = 1, binding = 2) uniform samplerCube pointShadowMap;

// Clustered light data (points + spots) at set 1, bindings 3-5; see lights.inc.
#define LIGHT_SET 1

layout(push_constant) uniform PushConstants {
    mat4 mvp;
    mat4 model;
    vec4 tint;
    vec4 sunDir;    // xyz = direction toward sun, w = alpha (0 = opaque)
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

    // Per-object opacity for the translucent pipeline. It lives in sunDir.w
    // because tint.w is already a mode selector -- positive is emissive,
    // negative is flat-shaded foliage -- and the push-constant block is full at
    // 256 bytes with nowhere to put a fifth vec4. sunDir.w is padding on this
    // path: grass, the grass impostors and water each borrow it, and none of
    // them is drawn by lit.frag.
    //
    // Zero means opaque, so every draw that does not set it is unaffected and
    // the opaque pipelines -- which do not blend anyway -- keep writing 1.
    float alpha = pc.sunDir.w > 0.0 ? pc.sunDir.w : 1.0;

    // Emissive early-out: tint.w > 0 bypasses lighting entirely.
    //
    // NOT the moon disc, whatever this comment used to say: the celestial
    // pipeline is built from mesh.vert/mesh.frag (see createCelestialPipeline),
    // as are the overlays. What reaches here is a world-space mesh tagged
    // Emissive -- a bulb, a fixture marker -- and a full-bright translucent
    // object, which is the placement ghost the translucent pass exists for, so
    // alpha still applies and the two compose rather than one winning.
    //
    // Bypassing applyFog means bypassing the night shift too, which is the
    // same answer material_shading.inc reaches by counting emission as local
    // light: a glowing surface keeps its colour at night. It also means no
    // fog, which is the one place the two paths still differ -- the material
    // path fogs its emission on the argument that air scatters emitted light
    // like any other, and that argument holds here too. Measured before
    // leaving it alone: routing this through applyFog moves 256 pixels by one
    // 8-bit step in 21-streetlights, the only example with both fog and
    // emissive meshes, because the camera sits well above the fog base and
    // `amount` comes out at 0.18. Worth revisiting for a game with thick fog
    // at eye level; not worth a behaviour change for one LSB here.
    if (pc.tint.w > 0.0) {
        outColor = vec4(baseColor, alpha);
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
    outColor = vec4(applyFog(lit, fragWorldPos), alpha);
}
