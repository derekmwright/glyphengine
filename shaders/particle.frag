#version 450

layout(location = 0) in vec2 fragUV;
layout(location = 1) in vec4 fragColor;

layout(location = 0) out vec4 outColor;

void main() {
    // Distance from quad center (UV 0..1, center at 0.5)
    vec2 centered = fragUV - vec2(0.5);
    float dist = length(centered) * 2.0; // 0 at center, 1 at edge

    // Soft radial falloff: bright center, soft edges
    float falloff = smoothstep(1.0, 0.0, dist);

    outColor = vec4(fragColor.rgb, fragColor.a * falloff);
}
