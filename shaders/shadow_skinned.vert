#version 450

layout(location = 0) in vec3 inPosition;
layout(location = 1) in vec3 inColor;
layout(location = 2) in vec3 inNormal;
layout(location = 3) in vec2 inUV;
layout(location = 4) in uvec4 inJoints;
layout(location = 5) in vec4 inWeights;

layout(push_constant) uniform PushConstants {
    mat4 mvp;
    mat4 model;
} pc;

layout(set = 0, binding = 0) uniform JointMatrices {
    mat4 joints[128];
};

void main() {
    mat4 skin = inWeights.x * joints[inJoints.x]
              + inWeights.y * joints[inJoints.y]
              + inWeights.z * joints[inJoints.z]
              + inWeights.w * joints[inJoints.w];

    vec4 skinnedPos = skin * vec4(inPosition, 1.0);
    gl_Position = pc.mvp * skinnedPos;
}
