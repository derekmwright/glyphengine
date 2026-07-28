#version 450

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
    float t = smoothstep(-0.1, 0.6, elevation);

    // Daytime colors
    vec3 zenithDay = vec3(0.18, 0.35, 0.78);
    vec3 horizonDay = vec3(0.65, 0.78, 0.95);

    // Night colors
    vec3 zenithNight = vec3(0.01, 0.01, 0.04);
    vec3 horizonNight = vec3(0.03, 0.04, 0.08);

    vec3 zenith = mix(zenithDay, zenithNight, nightFactor);
    vec3 horizon = mix(horizonDay, horizonNight, nightFactor);

    vec3 skyColor = mix(horizon, zenith, t);

    // Sunrise/sunset tint near horizon when sun is low
    float sunElevation = sunDir.y;
    float sunsetStrength = smoothstep(0.3, 0.0, sunElevation) * (1.0 - nightFactor);
    float horizonMask = exp(-3.0 * abs(elevation));
    // Tint toward sun direction
    float sunProximity = max(dot(dir, sunDir), 0.0);
    float sunGlow = pow(sunProximity, 4.0) * sunsetStrength;
    vec3 sunsetTint = mix(sunCol, vec3(1.0, 0.4, 0.1), 0.5);
    skyColor += sunsetTint * sunGlow * 0.6;
    skyColor += sunsetTint * horizonMask * sunsetStrength * 0.25;

    // Below-horizon: darken toward ground
    if (elevation < 0.0) {
        float belowFade = smoothstep(0.0, -0.3, elevation);
        vec3 groundColor = mix(horizon, vec3(0.15, 0.18, 0.12), belowFade) * (1.0 - nightFactor * 0.7);
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
        float coverage = mix(0.42, 0.55, nightFactor);
        density = smoothstep(coverage, coverage + 0.25, density);

        // Fade clouds near horizon to avoid harsh cutoff
        float horizonFade = smoothstep(0.01, 0.15, dir.y);
        density *= horizonFade;

        // Fade clouds at night
        density *= mix(1.0, 0.3, nightFactor);

        // Beer's law light attenuation
        float absorb = 2.5;
        float beer = exp(-density * absorb);

        // Henyey-Greenstein-lite: brighten clouds facing the sun
        float HG = pow(max(dot(dir, sunDir), 0.0), 3.0) * 0.4;

        // Cloud color: shadow to sun-lit
        vec3 cloudShadow = mix(vec3(0.45, 0.48, 0.55), vec3(0.08, 0.08, 0.12), nightFactor);
        vec3 cloudLit = mix(vec3(1.0, 0.98, 0.95), vec3(0.15, 0.15, 0.2), nightFactor);
        // Add sun color tinting for sunset/sunrise
        cloudLit = mix(cloudLit, sunsetTint * 1.1, sunsetStrength * 0.5);

        vec3 cloudColor = mix(cloudShadow, cloudLit, beer + HG);

        skyColor = mix(skyColor, cloudColor, density);
    }

    outColor = vec4(skyColor, 1.0);
}
