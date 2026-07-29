#version 450
#extension GL_GOOGLE_include_directive : require

// Water surface shading: refraction through the surface, a Fresnel-weighted
// sky reflection over it, and a sun glint on top.
//
// set 0 binding 0 is the opaque scene, copied to a texture after the first
// render pass. Sampling it with an offset is what bends the lake bed: the
// offset is the surface normal, so wherever a wave tilts, the view through it
// shifts. That is the whole of the refraction, and it needs the scene as a
// texture, which is why water draws in a second pass.

layout(location = 0) in vec3 fragDeepColor;
layout(location = 1) in vec3 fragShallowColor;
layout(location = 2) in vec3 fragWorldPos;
layout(location = 3) in vec3 fragNormal;
layout(location = 4) in float fragDepth;
layout(location = 5) in vec3 fragShadowPos;

layout(set = 0, binding = 0) uniform sampler2D texSampler; // opaque scene colour

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
    vec4 tint;      // x = time, y = amplitude, z = wavelength, w = refraction strength
    vec4 sunDir;
    vec4 sunColor;  // rgb, w = night factor
    vec4 pointPos;
    vec4 pointColor;// w = absorption depth
    vec4 ambient;
    vec4 cameraPos; // xyz = eye, w = fog density
    vec4 fog;       // x = height falloff, y = base height
} pc;

layout(location = 0) out vec4 outColor;

#include "lighting.inc"

// rippleNormal adds detail the vertex waves cannot carry.
//
// The Gerstner sum runs at vertex rate, so its finest wavelength is limited by
// the grid spacing. These are evaluated per fragment instead, which costs
// nothing in geometry and gives the surface something small enough to catch
// the sun as separate glints rather than one broad sheet.
vec3 rippleNormal(vec2 p, float time) {
    vec2 d = vec2(0.0);
    float amp = 1.0;
    float freq = 1.0;
    for (int i = 0; i < 3; i++) {
        vec2 dir = normalize(vec2(cos(float(i) * 2.4), sin(float(i) * 2.4)));
        float phase = dot(dir, p) * freq + time * (0.9 + 0.4 * float(i));
        d += dir * cos(phase) * amp;
        amp *= 0.55;
        freq *= 2.1;
    }
    return normalize(vec3(-d.x, 6.0, -d.y));
}

void main() {
    float time         = pc.tint.x;
    float refractScale = pc.tint.w;
    float absorption   = max(pc.pointColor.w, 0.01);

    vec3 V = normalize(pc.cameraPos.xyz - fragWorldPos);

    // Detail ripples fade out in shallow water, along with the waves that
    // carry them, so the surface settles as it reaches the shore.
    float shoal = clamp(fragDepth * 0.7, 0.0, 1.0);
    vec3 rn = rippleNormal(fragWorldPos.xz * 1.6, time);
    vec3 N = normalize(mix(vec3(0.0, 1.0, 0.0), normalize(fragNormal + vec3(rn.x, 0.0, rn.z) * 0.35), shoal));

    // Backface guard: at a grazing view the perturbed normal can tip away from
    // the eye, which flips the Fresnel term and produces black speckle.
    if (dot(N, V) < 0.0) {
        N = reflect(N, V);
    }

    // How much of the water column the view passes through. Looking straight
    // down crosses `depth`; looking along the surface crosses far more, which
    // is why a lake is clear at your feet and opaque at the far shore.
    float NdotV = clamp(dot(N, V), 0.02, 1.0);
    float travel = fragDepth / NdotV;
    float absorbed = 1.0 - exp(-travel / absorption);

    vec3 albedo = mix(fragShallowColor, fragDeepColor, clamp(absorbed, 0.0, 1.0));

    // The body colour is albedo, not emission: it is light that entered the
    // water, scattered, and came back out. Lighting it is the difference
    // between a lake and a light source -- emitted directly it keeps its full
    // daytime colour at midnight, and the water glows teal against black land.
    //
    // The refracted scene is not lit here. It arrives already shaded from the
    // opaque pass, and lighting it a second time would double every lamp.
    vec3 L = normalize(pc.sunDir.xyz);
    float NdotL = clamp(dot(N, L), 0.0, 1.0);
    float sunShadow = calcShadow(fragShadowPos, NdotL);
    vec3 bodyColor = albedo * (pc.ambient.rgb + pc.sunColor.rgb * NdotL * sunShadow);

    // ── refraction ──
    vec3 throughWater;
    float alpha;
    if (refractScale > 0.0) {
        vec2 texel = 1.0 / vec2(textureSize(texSampler, 0));
        vec2 screenUV = gl_FragCoord.xy * texel;

        // Offsetting by the normal's horizontal component tilts the view the
        // way the surface does. Scaling by depth keeps the shallows honest:
        // with no water to bend through there is nothing to displace, and it
        // also stops the shoreline smearing into the lake, which is the usual
        // giveaway of screen-space refraction.
        vec2 offset = N.xz * refractScale * 0.02 * shoal;
        vec3 refracted = texture(texSampler, clamp(screenUV + offset, vec2(0.0), vec2(1.0))).rgb;

        // The scene is composited here rather than by the blender, so the
        // surface is opaque wherever there is water at all. It still has to
        // fade in over the first few centimetres of depth, or the edge of the
        // surface mesh shows up as a hard line across the shore -- the geometry
        // ends somewhere, and without this that somewhere is visible.
        throughWater = mix(refracted, bodyColor, clamp(absorbed, 0.0, 1.0));
        alpha = clamp(fragDepth / 0.35, 0.0, 1.0);
    } else {
        // No scene texture: fall back to ordinary alpha blending and let the
        // blender show the lake bed through.
        throughWater = bodyColor;
        alpha = clamp(absorbed, 0.0, 1.0);
    }

    // ── reflection ──
    // fogColor is sky.frag's horizon gradient, so reflecting the view vector
    // through it gives the sky the water is actually under -- including the
    // sunset glow -- without a reflection pass.
    vec3 R = reflect(-V, N);
    R.y = abs(R.y); // a downward reflection would sample below the horizon
    vec3 skyColor = fogColor(R);

    // Schlick, with water's F0 of about 0.02. This is the term that makes a
    // lake transparent at your feet and mirror-like across the far side.
    float fresnel = 0.02 + 0.98 * pow(1.0 - NdotV, 5.0);

    vec3 color = mix(throughWater, skyColor, fresnel);

    // ── sun glint ──
    vec3 H = normalize(L + V);
    float spec = pow(max(dot(N, H), 0.0), 220.0);
    color += pc.sunColor.rgb * spec * sunShadow * 1.4;

    outColor = vec4(applyFog(color, fragWorldPos), alpha);
}
