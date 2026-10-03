#version 450

// Depth-only stage for the optional depth prepass (see WithDepthPrepass).
//
// It exists rather than reusing shadow.vert because the depth it writes has to
// come out BIT-IDENTICAL to what lit.vert writes for the same draw: the main
// pass then re-tests those fragments with a compare of EQUAL, and a last-bit
// difference is not a dimmer pixel, it is a surface that vanishes. Pairing this
// file with lit.vert -- same push-constant block prefix, same expression, same
// order of operations -- is what makes that a property of the source rather than
// a coincidence of two shaders that happen to agree today.
//
// Keep the gl_Position line below character for character the same as lit.vert's.

layout(location = 0) in vec3 inPosition;
layout(location = 1) in vec3 inColor;
layout(location = 2) in vec3 inNormal;
layout(location = 3) in vec2 inUV;

layout(push_constant) uniform PushConstants {
    mat4 mvp;
    mat4 model;
} pc;

void main() {
    gl_Position = pc.mvp * vec4(inPosition, 1.0);
}
