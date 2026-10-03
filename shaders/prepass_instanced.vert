#version 450

// Depth-only stage for instanced static meshes in the optional depth prepass.
//
// The twin of lit_instanced.vert, for the reason prepass.vert is lit.vert's: the
// main pass re-tests these fragments with a compare of EQUAL, so the depth has
// to match to the last bit.
//
// That is why this is a separate file from shadow_instanced.vert rather than a
// reuse of it. shadow_instanced.vert writes `pc.vp * inModel * vec4(...)`, which
// GLSL left-associates into a matrix-matrix product followed by a
// matrix-vector one; lit_instanced.vert writes `pc.vp * (inModel * vec4(...))`,
// which is two matrix-vector products. The two are not the same arithmetic and
// do not agree in the low bits, and the surfaces that disagree would simply not
// be drawn.
//
// Keep the two lines below character for character the same as
// lit_instanced.vert's.

layout(location = 0) in vec3 inPosition;
layout(location = 1) in vec3 inColor;
layout(location = 2) in vec3 inNormal;
layout(location = 3) in vec2 inUV;

// Per-instance, binding 1. A mat4 attribute occupies four consecutive
// locations, so the tint lands at 8.
layout(location = 4) in mat4 inModel;
layout(location = 8) in vec4 inTint;

layout(push_constant) uniform PushConstants {
    mat4 vp;        // view-projection; the model comes from the instance
    mat4 unused;    // the ordinary path's model slot, left alone here
} pc;

void main() {
    vec4 worldPos = inModel * vec4(inPosition, 1.0);
    gl_Position = pc.vp * worldPos;
}
