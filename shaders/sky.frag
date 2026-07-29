#version 450
#extension GL_GOOGLE_include_directive : require

layout(location = 0) in vec2 fragUV;

layout(push_constant) uniform PushConstants {
    mat4 invVP;    // inverse view-projection
    mat4 model;    // [0].xyz = camera position
    vec4 tint;     // x = time, y = nightFactor
    vec4 sunDir;
    vec4 sunColor;
    vec4 pointPos;
    vec4 pointColor;
    vec4 ambient;
} pc;

layout(location = 0) out vec4 outColor;

#include "atmosphere.inc"

// ----- Hash / Noise -----
float hash2D(vec2 p) {
    p = fract(p * vec2(443.897, 441.423));
    p += dot(p, p.yx + 19.19);
    return fract(p.x * p.y);
}

float valueNoise(vec2 p) {
    vec2 i = floor(p);
    vec2 f = fract(p);
    f = f * f * (3.0 - 2.0 * f); // smoothstep

    float a = hash2D(i);
    float b = hash2D(i + vec2(1.0, 0.0));
    float c = hash2D(i + vec2(0.0, 1.0));
    float d = hash2D(i + vec2(1.0, 1.0));

    return mix(mix(a, b, f.x), mix(c, d, f.x), f.y);
}

float fbm(vec2 p) {
    float value = 0.0;
    float amplitude = 0.5;
    for (int i = 0; i < 4; i++) {
        value += amplitude * valueNoise(p);
        p *= 2.0;
        amplitude *= 0.5;
    }
    return value;
}

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

    vec3 zenith, horizon;
    atmSkyPalette(sunElevation, zenith, horizon);

    // Rayleigh-ish falloff rather than a linear ramp: most of the colour
    // change happens in the first part of the climb from the horizon, which is
    // what gives the sky depth instead of a flat wash.
    float t = pow(smoothstep(-0.08, 0.75, elevation), 0.65);
    vec3 skyColor = mix(horizon, zenith, t);

    skyColor += atmSunGlow(dir, sunDir, sunCol, sunElevation);

    // Below-horizon: darken toward ground
    if (elevation < 0.0) {
        float belowFade = smoothstep(0.0, -0.3, elevation);
        vec3 groundColor = mix(horizon, vec3(0.15, 0.18, 0.12), belowFade) * mix(0.3, 1.0, atmDaylight(sunElevation));
        skyColor = mix(skyColor, groundColor, belowFade);
    }

    // ----- Cloud layer -----
    float cloudAlt = 800.0;
    float relAlt = cloudAlt - camPos.y;

    if (dir.y > 0.01 && relAlt > 0.0) {
        float tHit = relAlt / dir.y;
        vec2 cloudUV = (camPos.xz + dir.xz * tHit) * 0.0004;

        // Wind animation
        vec2 wind = vec2(time * 0.008, time * 0.003);
        cloudUV += wind;

        // FBM cloud density
        float density = fbm(cloudUV * 3.0);

        // Coverage threshold — fewer clouds at night
        float coverage = mix(0.55, 0.42, atmDaylight(sunElevation));
        density = smoothstep(coverage, coverage + 0.25, density);

        // Fade clouds near horizon to avoid harsh cutoff
        float horizonFade = smoothstep(0.01, 0.15, dir.y);
        density *= horizonFade;

        // Fade clouds at night
        density *= mix(0.3, 1.0, atmDaylight(sunElevation));

        // Beer's law light attenuation
        float absorb = 2.5;
        float beer = exp(-density * absorb);

        // Henyey-Greenstein-lite: brighten clouds facing the sun
        float HG = pow(max(dot(dir, sunDir), 0.0), 3.0) * 0.4;

        // Cloud color: shadow to sun-lit
        vec3 cloudShadow = mix(vec3(0.06, 0.07, 0.11), vec3(0.45, 0.48, 0.55), atmDaylight(sunElevation));
        vec3 cloudLit = mix(vec3(0.13, 0.13, 0.19), vec3(1.0, 0.98, 0.95), atmDaylight(sunElevation));
        // Add sun color tinting for sunset/sunrise
        // Undersides catch the low sun long after the ground has lost it,
        // which is most of what makes a sunset sky worth looking at.
        cloudLit = mix(cloudLit, vec3(1.0, 0.55, 0.28), atmTwilight(sunElevation) * 0.75);

        vec3 cloudColor = mix(cloudShadow, cloudLit, beer + HG);

        skyColor = mix(skyColor, cloudColor, density);
    }

    outColor = vec4(skyColor, 1.0);
}
