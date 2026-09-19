#version 450
#extension GL_GOOGLE_include_directive : require

// Water surface shading: refraction through the surface, a Fresnel-weighted
// sky reflection over it, and a glint from the sun and from every clustered
// lamp that reaches the fragment.
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
} shadow;
layout(set = 1, binding = 1) uniform sampler2DArrayShadow shadowMap;
layout(set = 1, binding = 2) uniform samplerCube pointShadowMap;

// Clustered light data (points + spots) at set 1, bindings 3-5; see lights.inc.
#define LIGHT_SET 1

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
    vec4 fog;       // x = height falloff, y = base height, zw = real sun horizontal
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

// glintGain scales every specular lobe on this surface, the sun's and the
// lamps' alike. One constant rather than one per light source: a lamp that
// delivers as much irradiance to a patch of water as the sun does should
// glint as brightly off it, and giving lamps a gain of their own would be a
// second look to keep in step with this one.
const float glintGain = 1.4;

// A lamp contributes NO diffuse body term, and that is a measurement rather
// than an omission.
//
// The obvious thing to do is what the sun does below: add the lamp's
// irradiance into the parenthesis that multiplies `albedo`, so light enters
// the water, scatters, and comes back out. Tried, at shares of the sun's
// weight, on `09-water -lamps 9 -spots 2 -time 0.02` differenced against the
// same scene under `-lampsoff`. What the lamps ADD to a patch of water
// carrying a reflection streak, in 8-bit channels:
//
//	share 0.00   R +21.4  G +18.0  B +14.9   warm: the lamp's own colour
//	share 0.10   R +24.9  G +26.1  B +24.6   green above red; the hue is gone
//	share 0.25   R +35.1  G +38.9  B +36.1   and the whole lake reads teal
//
// The body term is multiplied by the water's albedo, which is cyan (the
// defaults are 0.30/0.52/0.52 shallow, 0.02/0.10/0.17 deep), so every unit of
// it repaints the lamp in the lake's colour. A tenth of the sun's weight
// already puts green above red in what the lamp adds -- which is the failure
// `task nightlight` exists to catch on the ground, arriving on the water by a
// different route. At 0.25 the open lake BETWEEN the streaks lifts by
// +13/+18/+18 with no reflection on it at all: the pool of paint.
//
// Calm-to-rippled water is overwhelmingly specular, so this is the physical
// answer as well as the better-looking one. The light a lamp does put into the
// body is not lost, it is computed elsewhere: where the water is shallow
// enough for the body to matter, the bed shows through the refraction, and the
// opaque pass has already lit that bed with this same lamp.

// waterGlint is the specular lobe, factored out of the sun's inline version so
// a lamp's reflection breaks up across exactly the ripples the sun's does --
// same N, so the same wave crests and the same per-fragment ripples decide
// where it lands. That is the whole reason a lamp on a lake reads as a broken
// streak rather than a disc.
//
// The exponent is the sun's 220 and stays there for lamps. It is what turns
// one light into a streak over the wavelets, and a second exponent would give
// a lamp a differently shaped reflection from the moon hanging beside it in
// the same frame.
//
// A lamp is the wider source of the two and could argue for a broader lobe --
// a 0.22 m bulb at 15 m subtends 0.84 degrees against the sun's 0.53 -- but
// the surface spreads the reflection an order of magnitude further than that
// on its own. rippleNormal alone tilts by up to atan(1.85/6) = 17 degrees
// before it is mixed in at 0.35, so about 6, and the Gerstner normal moves
// under it. Matching the lobe to the source size would be tuning the smaller
// of the two terms.
float waterGlint(vec3 L, vec3 V, vec3 N) {
    vec3 H = normalize(L + V);
    return pow(max(dot(N, H), 0.0), 220.0);
}

void main() {
    float time         = pc.tint.x;
    float refractScale = pc.tint.w;
    float absorption   = max(pc.pointColor.w, 0.01);

    // Reverse-Z leaves gl_FragCoord.w equal to 1/viewDepth; see the same note
    // in evalLightingAO, which is where the cluster lookup's convention lives.
    float viewDepth = 1.0 / gl_FragCoord.w;

    // The froxel heatmap replaces the surface outright, exactly as
    // evalLightingAO does it for every other lit shader. Water was the one
    // hole in that readout: it shaded itself, so the lake showed a refracted,
    // absorbed, Fresnel-mixed picture of the BED's heatmap, in colours that
    // read as a smaller count than the cells there actually hold.
    //
    // Alpha 1.0 rather than the surface's own fade, because this is a number
    // in false colour and not something the air or the water is in front of:
    // blending it over the bed's heatmap mixes two cells' counts into one
    // pixel and produces a colour that means neither. applyFog would hand it
    // straight back (lightDebugReadout), so it is not called at all.
    if ((lb.flags.x & 2u) != 0u) {
        outColor = vec4(lightHeatmap(cellLightCount(gl_FragCoord.xy, viewDepth)), 1.0);
        return;
    }

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

    // ── clustered lamps (points + spots) ──
    //
    // Through resolveLightRange/lightAt/lightIrradiance rather than a loop of
    // this shader's own, so the cluster grid, the brute-force reference path
    // and the spot cone all behave here exactly as they do on terrain beside
    // the same lake.
    //
    // One sum, the surface reflection, for the reason recorded on waterGlint's
    // neighbour above: what a lamp does to a lake is a broken streak of its own
    // colour across the wavelets, and the body term measurably repaints that
    // streak in the water's cyan. The lobe uses the N the waves and the
    // per-fragment ripples already built, so a lamp's reflection breaks up
    // exactly where the sun's does.
    vec3 lampGlint = vec3(0.0);
    LightRange lr = resolveLightRange(gl_FragCoord.xy, viewDepth);
    for (uint i = 0u; i < lr.count; i++) {
        GpuLight light = lightAt(lr, i);
        vec3 uL;
        vec3 irradiance = lightIrradiance(light, fragWorldPos, N, uL);
        // The same skip lighting.inc's two loops take, for the same reason: a
        // light that does not reach this fragment adds exactly +0.0 to the
        // sum, so skipping it is bit-for-bit what going round it was -- which
        // is what lets `task lights` require clustered and brute force to
        // match exactly on water -- and it saves the pow() in waterGlint on
        // every light that contributes nothing.
        if (irradiance == vec3(0.0)) {
            continue;
        }
        lampGlint += irradiance * waterGlint(uL, V, N);
    }

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
    float spec = waterGlint(L, V, N);
    color += pc.sunColor.rgb * spec * sunShadow * glintGain;

    // ── lamp glint ──
    // Unshadowed, like every clustered light everywhere else in the engine.
    color += lampGlint * glintGain;

    // ── the night grade's local share ──
    //
    // Water shades itself, so it has to set lighting.inc's two accumulators by
    // hand; applyFog reads them a line below. Without this the share is
    // structurally zero and a lamplit harbour takes the same full scotopic
    // grade as open water under the moon, which is the half of issue #39 that
    // is not a missing highlight.
    //
    // The local side is the glint exactly as it was added, and the sky side is
    // the rest of the finished colour, so the two sum to what actually left
    // the fragment and the share cannot exceed one. Summed from the finished
    // terms rather than accumulated in the loop, for the reason evalLightingAO
    // gives: it leaves every expression that builds `color` untouched, so no
    // scene's pixels can move through a regrouped float add and the clustered
    // and brute-force paths cannot disagree about a summation order neither
    // takes.
    //
    // APPROXIMATE, in two places, both on the sky side:
    //
    //   - The refracted scene inside `color` is a pixel of the opaque pass,
    //     already lit, already fogged and already graded, and nothing here can
    //     say how much of it was lamplight. It is counted wholly as sky. So
    //     water over a lamplit bed -- a jetty light in the shallows -- keeps
    //     more of the night grade than the bed beside it does. The error is
    //     bounded by how much of the bed survives absorption and goes to
    //     nothing as the water deepens.
    //
    //   - The sky reflection is counted as sky even where what it reflects is
    //     a lit shore, because fogColor() is the atmosphere's horizon gradient
    //     and knows nothing about lamps. Scattered skylight is what it is
    //     nearly all of the time.
    //
    // Both err the same way, toward calling light "sky", so the surface comes
    // out slightly cooler than the shore rather than warmer. On this scene a
    // warm surface is the claim being made, so erring against it is the safe
    // direction.
    lightLocalLum = lightLum(lampGlint * glintGain);
    lightSkyLum = max(lightLum(color) - lightLocalLum, 0.0);

    outColor = vec4(applyFog(color, fragWorldPos), alpha);
}
