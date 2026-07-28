#version 450

layout(location = 0) in vec3 inPosition;
layout(location = 1) in vec3 inColor;
layout(location = 2) in vec3 inNormal;
layout(location = 3) in vec2 inUV;

layout(push_constant) uniform PushConstants {
    mat4 mvp;
    mat4 model;
    vec4 tint; // rgb = multiplier (usually 1,1,1), w = screenPxRange
} pc;

layout(location = 0) out vec2 fragUV;
layout(location = 1) out vec4 fragTint;
layout(location = 2) out float fragAlpha;
layout(location = 3) out float fragBoldBias;

void main() {
    gl_Position = pc.mvp * vec4(inPosition, 1.0);
    fragUV = inUV;
    fragTint = vec4(inColor * pc.tint.rgb, inNormal.y); // .w = per-vertex screenPxRange
    fragAlpha = inNormal.x;
    fragBoldBias = inNormal.z;
}
