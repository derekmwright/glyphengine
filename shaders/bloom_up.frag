#version 450
#extension GL_GOOGLE_include_directive : require

// Bloom step 3: walk back up the chain, adding each level into the one below it.
//
// The pipeline blends additively rather than this shader reading the destination,
// so the accumulation happens in the blend unit. Each level therefore contributes
// its own width of glow, and the sum across levels is what gives the halo a
// natural falloff instead of one Gaussian's shoulder.

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
    outColor = vec4(upsampleTent(srcTex, fragUV, pc.tint.zw, pc.tint.x), 1.0);
}
