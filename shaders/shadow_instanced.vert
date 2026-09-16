#version 450

// Depth-only stage for instanced static meshes.
//
// The instanced path needs its own shadow vertex stage for the same reason it
// needs its own lit one: the model matrix is a vertex attribute rather than a
// push constant. Without this, instanced geometry would silently stop casting
// shadows -- the draw would still record, and every instance would land on top
// of the first one.

layout(location = 0) in vec3 inPosition;
layout(location = 1) in vec3 inColor;
layout(location = 2) in vec3 inNormal;
layout(location = 3) in vec2 inUV;

layout(location = 4) in mat4 inModel;
layout(location = 8) in vec4 inTint;

layout(push_constant) uniform PushConstants {
    mat4 vp;      // the cascade's view-projection
    mat4 unused;
} pc;

void main() {
    gl_Position = pc.vp * inModel * vec4(inPosition, 1.0);
}
