#version 450
#extension GL_GOOGLE_include_directive : require

layout(location = 0) in vec2 fragUV;

layout(push_constant) uniform PushConstants {
    mat4 invVP;    // inverse view-projection
    mat4 model;    // [0].xyz = camera position
    vec4 tint;     // x = time, y = nightFactor, z = cloud raymarch steps
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

// ---- 3D value noise, for the cloud volume ----
float hash3D(vec3 p) {
    p = fract(p * vec3(443.897, 441.423, 437.195));
    p += dot(p, p.yzx + 19.19);
    return fract((p.x + p.y) * p.z);
}

float valueNoise3D(vec3 p) {
    vec3 i = floor(p);
    vec3 f = fract(p);
    f = f * f * (3.0 - 2.0 * f);

    float n000 = hash3D(i + vec3(0, 0, 0));
    float n100 = hash3D(i + vec3(1, 0, 0));
    float n010 = hash3D(i + vec3(0, 1, 0));
    float n110 = hash3D(i + vec3(1, 1, 0));
    float n001 = hash3D(i + vec3(0, 0, 1));
    float n101 = hash3D(i + vec3(1, 0, 1));
    float n011 = hash3D(i + vec3(0, 1, 1));
    float n111 = hash3D(i + vec3(1, 1, 1));

    return mix(
        mix(mix(n000, n100, f.x), mix(n010, n110, f.x), f.y),
        mix(mix(n001, n101, f.x), mix(n011, n111, f.x), f.y),
        f.z);
}

float fbm3D(vec3 p) {
    float v = 0.0;
    float a = 0.5;
    for (int i = 0; i < 4; i++) {
        v += a * valueNoise3D(p);
        p = p * 2.03 + vec3(17.1, 9.7, 23.3);
        a *= 0.5;
    }
    return v;
}

// fbm3DLow is the two-octave version used for the light march.
//
// Shadowing inside a cloud does not need the detail the shape does: the fine
// octaves only add high-frequency variation that reads as speckle once it is
// sampled four times per step. Dropping them is both cheaper and smoother,
// which is a rare direction for that trade to go.
float fbm3DLow(vec3 p) {
    float v = 0.5 * valueNoise3D(p);
    p = p * 2.03 + vec3(17.1, 9.7, 23.3);
    v += 0.25 * valueNoise3D(p);
    return v;
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
    int cloudSteps = int(pc.tint.z);
    if (dir.y > 0.015 && cloudSteps > 0) {
        const float CLOUD_BOTTOM = 620.0;
        const float CLOUD_TOP    = 1500.0;
        const int   LIGHT_STEPS  = 4;
        int STEPS = cloudSteps;

        float t0 = (CLOUD_BOTTOM - camPos.y) / dir.y;
        float t1 = (CLOUD_TOP - camPos.y) / dir.y;
        t0 = max(t0, 0.0);

        // Cap the march. Near the horizon the slab crossing runs to the
        // horizon itself, and past a few tens of kilometres the samples are so
        // far apart that they alias into noise rather than resolving cloud.
        const float MAX_DIST = 34000.0;
        t1 = min(t1, MAX_DIST);

        if (t1 > t0) {
            float day = atmDaylight(sunElevation);
            float twi = atmTwilight(sunElevation);

            // Coverage: a little heavier at night so the sky is not empty.
            float coverage = mix(0.52, 0.44, day);

            vec2 wind = vec2(time * 0.0016, time * 0.0007);

            float stepLen = (t1 - t0) / float(STEPS);
            float transmittance = 1.0;
            vec3 scattered = vec3(0.0);

            // Forward-scattering: the sun's disc bleeds through thin cloud,
            // which is what makes edges glow when it is behind them.
            float cosT = dot(dir, sunDir);
            float g = 0.55;
            float g2 = g * g;
            float phase = (1.0 - g2) / (12.566 * pow(1.0 + g2 - 2.0 * g * cosT, 1.5));
            phase = 0.35 + 2.2 * phase;

            vec3 sunLight = mix(vec3(0.55, 0.60, 0.75), vec3(1.0, 0.97, 0.92), day);
            sunLight = mix(sunLight, vec3(1.0, 0.62, 0.34), twi * 0.8);
            vec3 skyFill = mix(zenith, horizon, 0.5) * 1.6;

            // Jitter the first sample so the step boundaries do not band.
            float jitter = hash2D(fragUV * 1024.0);
            float t = t0 + stepLen * jitter;

            for (int i = 0; i < STEPS; i++) {
                vec3 p = camPos + dir * t;

                float h = clamp((p.y - CLOUD_BOTTOM) / (CLOUD_TOP - CLOUD_BOTTOM), 0.0, 1.0);
                vec3 q = vec3(p.xz * 0.00035 + wind, p.y * 0.0011);
                float n = fbm3D(q * 3.0);

                // Flatten toward both faces of the slab so clouds have bases
                // and tops rather than being cut off by the boundary.
                float profile = smoothstep(0.0, 0.22, h) * smoothstep(1.0, 0.55, h);
                float density = smoothstep(coverage, coverage + 0.30, n) * profile;

                if (density > 0.002) {
                    // Light march: how much sun reaches this sample.
                    float shadow = 0.0;
                    float ls = 90.0;
                    for (int j = 1; j <= LIGHT_STEPS; j++) {
                        vec3 lp = p + sunDir * (ls * float(j));
                        float lh = clamp((lp.y - CLOUD_BOTTOM) / (CLOUD_TOP - CLOUD_BOTTOM), 0.0, 1.0);
                        vec3 lq = vec3(lp.xz * 0.00035 + wind, lp.y * 0.0011);
                        float ln = fbm3DLow(lq * 3.0);
                        float lprofile = smoothstep(0.0, 0.22, lh) * smoothstep(1.0, 0.55, lh);
                        shadow += smoothstep(coverage - 0.04, coverage + 0.30, ln) * lprofile;
                    }
                    float lightT = exp(-shadow * 1.5);

                    vec3 lit = sunLight * lightT * phase + skyFill * 0.30;

                    float dt = density * stepLen * 0.0075;
                    // Integrate analytically over the step rather than
                    // point-sampling it, which keeps the result stable as the
                    // step length changes with view angle.
                    float absorbed = 1.0 - exp(-dt);
                    scattered += lit * absorbed * transmittance;
                    transmittance *= exp(-dt);

                    if (transmittance < 0.02) {
                        break;
                    }
                }
                t += stepLen;
            }

            // Fade the whole layer out at the horizon, where the march is
            // longest and least accurate, and where real cloud is lost to haze.
            float horizonFade = smoothstep(0.015, 0.16, dir.y);
            float cover = (1.0 - transmittance) * horizonFade;
            skyColor = skyColor * (1.0 - cover) + scattered * horizonFade;
        }
    }

    outColor = vec4(skyColor, 1.0);
}
