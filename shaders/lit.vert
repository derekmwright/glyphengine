#version 450

layout(location = 0) in vec3 inPosition;
layout(location = 1) in vec3 inColor;
layout(location = 2) in vec3 inNormal;
layout(location = 3) in vec2 inUV;

layout(push_constant) uniform PushConstants {
    mat4 mvp;
    mat4 model;
    vec4 tint;
    vec4 sunDir;    // xyz = direction toward sun
} pc;

layout(location = 0) out vec3 fragColor;
layout(location = 1) out vec3 fragWorldPos;
layout(location = 2) out vec3 fragWorldNormal;
layout(location = 3) out vec2 fragUV;
layout(location = 4) out vec3 fragShadowPos;

void main() {
    gl_Position = pc.mvp * vec4(inPosition, 1.0);
    fragColor = inColor * pc.tint.rgb;
    vec4 worldPos = pc.model * vec4(inPosition, 1.0);
    fragWorldPos = worldPos.xyz;
    vec3 worldNormal = mat3(pc.model) * inNormal;
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
