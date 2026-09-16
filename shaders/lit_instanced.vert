#version 450

// The lit vertex stage for instanced static meshes.
//
// Identical to lit.vert except for where the model matrix and the tint come
// from. On the ordinary path both arrive in push constants, one object per
// draw; here they are per-instance vertex attributes, so one draw covers the
// whole set. pc.mvp therefore carries the view-projection alone rather than
// view-projection-model, and this shader does the multiply.
//
// The fragment stage is unchanged: lit.frag reads fragWorldPos and
// fragWorldNormal rather than pc.model, so it never had to know.

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
    vec4 tint;
    vec4 sunDir;    // xyz = direction toward sun
} pc;

layout(location = 0) out vec3 fragColor;
layout(location = 1) out vec3 fragWorldPos;
layout(location = 2) out vec3 fragWorldNormal;
layout(location = 3) out vec2 fragUV;
layout(location = 4) out vec3 fragShadowPos;

void main() {
    vec4 worldPos = inModel * vec4(inPosition, 1.0);
    gl_Position = pc.vp * worldPos;

    // Per-instance tint multiplies the per-set one, so a set can be dimmed as a
    // whole and an instance still picked out of it.
    fragColor = inColor * inTint.rgb * pc.tint.rgb;
    fragWorldPos = worldPos.xyz;

    // mat3 of the instance model. Non-uniform scale would need the inverse
    // transpose; uniform scale and rotation, which is what a placed prop has,
    // survives this intact.
    vec3 worldNormal = mat3(inModel) * inNormal;
    fragWorldNormal = worldNormal;
    fragUV = inUV;

    // Same normal-offset bias as lit.vert; see the comment there.
    vec3 N = normalize(worldNormal);
    float NdotL = dot(N, normalize(pc.sunDir.xyz));
    float sinAngle = sqrt(1.0 - NdotL * NdotL);
    float offsetScale = 0.08 * sinAngle + 0.02;
    fragShadowPos = worldPos.xyz + N * offsetScale;
}
