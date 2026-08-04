#version 450
#extension GL_GOOGLE_include_directive : require

layout(location = 0) in vec2 fragUV;

// Offsets match the lit shaders' block exactly. pointPos through cameraPos are
// unused here and declared only so fog lands where every other shader reads it
// from -- one packing convention beats a per-shader one nobody can keep in step.
layout(push_constant) uniform PushConstants {
    mat4 invVP;    // inverse view-projection
    mat4 model;    // [0].xyz = camera position
    vec4 tint;     // x = time, y = nightFactor, z = cloud raymarch steps
    vec4 sunDir;   // xyz = direction toward the body lighting the scene
    vec4 sunColor; // rgb, w = the real sun's elevation
    vec4 pointPos;
    vec4 pointColor;
    vec4 ambient;
    vec4 cameraPos;
    vec4 fog;      // zw = the real sun's horizontal direction
} pc;

// The half-resolution cloud target: rgb = in-scattered radiance, a =
// transmittance. Always bound; a scene with clouds disabled gets a cleared
// target that composites to a no-op rather than a special case here.
layout(set = 0, binding = 0) uniform sampler2D cloudTex;

layout(location = 0) out vec4 outColor;

#include "atmosphere.inc"

// The noise and the raymarch moved to clouds.frag, which runs at half
// resolution. What is left here is the dome, which measured 0.008 ms against
// the march's 1.146 -- the sky was never the expensive part of the sky.

void main() {
    float time = pc.tint.x;
    float nightFactor = pc.tint.y;
    vec3 camPos = pc.model[0].xyz;
    vec3 sunDir = normalize(pc.sunDir.xyz);
    vec3 sunCol = pc.sunColor.xyz;

    // Reconstruct world-space ray direction from screen UV
    vec2 ndc = fragUV * 2.0 - 1.0;
    vec4 world = pc.invVP * vec4(ndc, 0.0, 1.0);
    vec3 dir = normalize(world.xyz / world.w - camPos);

    // ----- Sky gradient -----
    float elevation = dir.y;
    // The real sun's elevation, not the current light's: at night pc.sunDir is
    // the moon, which is high when the sky should be darkest.
    float sunElevation = pc.sunColor.w;

    // And the real sun's *direction*, for the same reason. sunDir above stays
    // the lighting body, which is what the clouds below want -- they are lit by
    // the moon at night and their palette already accounts for that. Only the
    // scattering halo has to follow the sun itself.
    vec3 realSunDir = atmSunDirFrom(sunElevation, pc.fog.zw);

    vec3 zenith, horizon;
    atmSkyPalette(sunElevation, zenith, horizon);

    // Rayleigh-ish falloff rather than a linear ramp: most of the colour
    // change happens in the first part of the climb from the horizon, which is
    // what gives the sky depth instead of a flat wash.
    float t = pow(smoothstep(-0.08, 0.75, elevation), 0.65);
    vec3 skyColor = mix(horizon, zenith, t);

    skyColor += atmSunGlow(dir, realSunDir, sunCol, sunElevation);

    // Below-horizon: darken toward ground
    if (elevation < 0.0) {
        float belowFade = smoothstep(0.0, -0.3, elevation);
        vec3 groundColor = mix(horizon, vec3(0.15, 0.18, 0.12), belowFade) * mix(0.3, 1.0, atmDaylight(sunElevation));
        skyColor = mix(skyColor, groundColor, belowFade);
    }

    // ----- Volumetric clouds -----
    //
    // A slab of 3D noise, raymarched. The previous version sampled 2D noise on
    // a single plane, which looks convincing straight up and falls apart at a
    // shallow angle: a plane has no thickness, so clouds near the horizon are
    // as thin as clouds overhead, when they should be the longest sightline in
    // the sky. Marching a volume gets that for free, along with self-shadowing
    // and edges that light up from behind.
    //
    // This is only affordable because the sky now draws last and depth-tested,
    // so nothing here runs for a pixel the terrain covers.
    // Composite the half-resolution cloud target over the dome.
    //
    // rgb is in-scattered radiance and alpha is transmittance, so this is a
    // premultiplied over: what gets through the layer, plus what the layer
    // itself sends toward the eye. Sampling bilinearly upscales it, which is
    // acceptable precisely because clouds are soft -- there is no edge here for
    // the interpolation to blur that was not already soft.
    vec4 clouds = texture(cloudTex, fragUV);
    float cloudTransmit = clouds.a;
    skyColor = skyColor * cloudTransmit + clouds.rgb;

    outColor = vec4(skyColor, cloudTransmit);
}
