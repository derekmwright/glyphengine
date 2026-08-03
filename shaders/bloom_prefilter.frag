#version 450
#extension GL_GOOGLE_include_directive : require

// Bloom step 1: pull out what is bright enough to glow, at half resolution.
//
// The threshold is the whole reason this reads as light rather than as a smeared
// lens. Without one the blur takes the entire image and the frame goes milky --
// the same failure godray.frag documents, and the same fix.
//
// Thresholding is only meaningful because the scene arrives from a half-float
// target and can exceed 1. On the old 8-bit path everything bright was pinned at
// exactly 1, so a threshold at 1 selected either nothing or every white pixel in
// the frame, with nothing in between. See docs/agents/hdr-tonemap.md.

layout(location = 0) in vec2 fragUV;

layout(set = 0, binding = 0) uniform sampler2D srcTex;

layout(push_constant) uniform PushConstants {
    mat4 invVP;
    mat4 model;
    vec4 tint;
    vec4 sunDir;
    vec4 sunColor;
    vec4 pointPos;
    vec4 pointColor;
    vec4 ambient;
    vec4 cameraPos;
    vec4 fog;
} pc;

layout(location = 0) out vec4 outColor;

#include "bloom.inc"

void main() {
    vec3 c = downsample13(srcTex, fragUV, pc.tint.zw);

    float threshold = pc.tint.x;
    float knee = pc.tint.y;

    // Soft knee: a hard cutoff makes a moving highlight pop in and out as it
    // crosses the threshold, because a pixel is either fully in the bloom or
    // fully out. The quadratic ramp over [threshold-knee, threshold+knee] gives
    // it somewhere to fade.
    float bright = max(c.r, max(c.g, c.b));
    float soft = clamp(bright - threshold + knee, 0.0, 2.0 * knee);
    soft = soft * soft / (4.0 * knee + 1e-5);
    float weight = max(soft, bright - threshold) / max(bright, 1e-5);

    outColor = vec4(c * weight, 1.0);
}
