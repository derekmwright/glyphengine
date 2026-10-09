#version 450
// A flat colour from the pass's own push constants, and nothing else.
//
// Nothing else on purpose: the mode this shader exists for counts fragment
// invocations against covered pixels read back out of the frame, so every
// covered pixel has to be unmistakably this pass's and no pixel may be reached
// by anything but its own geometry. No texture read, no derivative, no discard
// -- a discard would still have run the invocation and would make the two
// numbers disagree for a reason that is not a bug.
layout(location=0) in vec2 texcoord;
layout(location=0) out vec4 color;
layout(push_constant) uniform AppPush { mat4 vp; mat4 model; vec4 data[8]; } pc;
void main() { color = vec4(pc.data[0].rgb, 1); }
