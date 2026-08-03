#version 450
#extension GL_GOOGLE_include_directive : require

// Bloom step 2: halve the image, repeatedly.
//
// Each level is a wider blur than the last for the same tap count, which is the
// trick that makes a wide glow affordable: a halo spanning a fifth of the screen
// costs thirteen taps at 1/32 resolution rather than thousands at full.

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
    outColor = vec4(downsample13(srcTex, fragUV, pc.tint.zw), 1.0);
}
