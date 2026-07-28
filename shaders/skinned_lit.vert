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
    vec4 tint;
    vec4 sunDir;    // xyz = direction toward sun
} pc;

layout(set = 1, binding = 0) uniform JointMatrices {
    mat4 joints[128];
};

layout(location = 0) out vec3 fragColor;
layout(location = 1) out vec3 fragWorldPos;
layout(location = 2) out vec3 fragWorldNormal;
layout(location = 3) out vec2 fragUV;
layout(location = 4) out vec3 fragShadowPos;

void main() {
    mat4 skin = inWeights.x * joints[inJoints.x]
              + inWeights.y * joints[inJoints.y]
              + inWeights.z * joints[inJoints.z]
              + inWeights.w * joints[inJoints.w];

    vec4 skinnedPos = skin * vec4(inPosition, 1.0);
    vec3 skinnedNormal = mat3(skin) * inNormal;

    gl_Position = pc.mvp * skinnedPos;
    fragColor = inColor * pc.tint.rgb;
    vec4 worldPos = pc.model * skinnedPos;
    fragWorldPos = worldPos.xyz;
    vec3 worldNormal = mat3(pc.model) * skinnedNormal;
    fragWorldNormal = worldNormal;
    fragUV = inUV;

    // Normal offset bias: push shadow lookup along surface normal to prevent
    // self-shadowing bands at grazing sun angles. Cascade selection and the
    // light-space transform happen in the fragment shader.
    vec3 N = normalize(worldNormal);
    float NdotL = dot(N, normalize(pc.sunDir.xyz));
    float sinAngle = sqrt(1.0 - NdotL * NdotL);
    float offsetScale = 0.08 * sinAngle + 0.02; // larger offset at grazing angles, small baseline
    fragShadowPos = worldPos.xyz + N * offsetScale;
}
