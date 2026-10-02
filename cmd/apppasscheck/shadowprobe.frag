#version 450
#extension GL_GOOGLE_include_directive : require
#include "shadowprobe.inc"

// The fragment half of -shadowcompute: the same lookup at the same positions,
// through the path that has always had this descriptor. gl_FragCoord is the
// half-resolution target's pixel centre, which is pixel + 0.5 -- the offset the
// dispatch adds to its integer invocation id.
layout(location = 0) out float value;

void main() {
    value = probeShadow(gl_FragCoord.xy, pc.data[0].xy);
}
