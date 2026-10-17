#version 450
#extension GL_GOOGLE_include_directive : require

// The graphics arm: a fullscreen pass into an 11x3 chart, one probe per texel.
#include "probe.inc"

layout(location = 0) out vec4 color;

void main() {
    color = probe(int(gl_FragCoord.x), int(gl_FragCoord.y));
}
