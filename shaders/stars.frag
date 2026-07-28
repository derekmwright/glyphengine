#version 450

layout(location = 0) in vec2 fragUV;

layout(push_constant) uniform PushConstants {
    mat4 invVP;    // inverse view-projection
    mat4 model;    // [0..2] = camera position (reuses model slot)
    vec4 tint;     // x = time, y = nightFactor (reuses tint slot)
    vec4 sunDir;
    vec4 sunColor;
    vec4 pointPos;
    vec4 pointColor;
    vec4 ambient;
} pc;

layout(location = 0) out vec4 outColor;

// Hash function for pseudo-random star placement
float hash(vec3 p) {
    p = fract(p * vec3(443.897, 441.423, 437.195));
    p += dot(p, p.yzx + 19.19);
    return fract((p.x + p.y) * p.z);
}

// Generate stars for one grid scale
float starLayer(vec3 dir, float scale, float threshold, float brightness, float time) {
    vec3 cell = floor(dir * scale);
    vec3 local = fract(dir * scale) - 0.5;

    float h = hash(cell);

    // Only some cells contain a star
    if (h > threshold) return 0.0;

    // Star position within cell
    vec3 starPos = vec3(
        hash(cell + vec3(1.0, 0.0, 0.0)) - 0.5,
        hash(cell + vec3(0.0, 1.0, 0.0)) - 0.5,
        hash(cell + vec3(0.0, 0.0, 1.0)) - 0.5
    );

    float dist = length(local - starPos * 0.4);

    // Sharp star point
    float star = smoothstep(0.05, 0.0, dist);

    // Twinkling
    float twinkle = 0.7 + 0.3 * sin(h * 6283.185 + time * 2.0);

    return star * brightness * twinkle;
}

void main() {
    float nightFactor = pc.tint.y;
    if (nightFactor <= 0.0) {
        outColor = vec4(0.0);
        return;
    }

    float time = pc.tint.x;
    vec3 camPos = pc.model[0].xyz;

    // Reconstruct world-space ray direction from screen UV
    vec2 ndc = fragUV * 2.0 - 1.0;
    vec4 world = pc.invVP * vec4(ndc, 0.0, 1.0);
    vec3 dir = normalize(world.xyz / world.w - camPos);

    // Stars only above horizon
    if (dir.y <= 0.0) {
        outColor = vec4(0.0);
        return;
    }

    // Fade near horizon to avoid harsh cutoff
    float horizonFade = smoothstep(0.0, 0.08, dir.y);

    // Two star layers: dense dim + sparse bright
    float stars = 0.0;
    stars += starLayer(dir, 80.0, 0.15, 0.5, time);  // dense, dim
    stars += starLayer(dir, 30.0, 0.10, 1.0, time);   // sparse, bright

    stars *= nightFactor * horizonFade;

    outColor = vec4(vec3(stars), 1.0);
}
