#version 450

// Per-vertex attributes (binding 0, same Vertex struct as lit.vert)
layout(location = 0) in vec3 inPosition;
layout(location = 1) in vec3 inColor;
layout(location = 2) in vec3 inNormal;
layout(location = 3) in vec2 inUV;

// Per-instance attribute (binding 1): xyz = world position, w = Y rotation
layout(location = 4) in vec4 inInstance;

layout(push_constant) uniform PushConstants {
    mat4 vp;       // camera view-projection (no per-object model)
    mat4 model;    // identity for grass (used by lit.frag for worldNormal)
    vec4 tint;
    vec4 sunDir;   // xyz = sun direction, w = time (for wind)
    vec4 sunColor;
    vec4 pointPos;
    vec4 pointColor;
    vec4 ambient;
    vec4 cameraPos;
} pc;

layout(location = 0) out vec3 fragColor;
layout(location = 1) out vec3 fragWorldPos;
layout(location = 2) out vec3 fragWorldNormal;
layout(location = 3) out vec2 fragUV;
layout(location = 4) out vec3 fragShadowPos;
layout(location = 5) out float fragFade;

const float grassScale = 0.35;

void main() {
    // Distance fade: shrink grass blades toward zero height at max distance.
    float distToCam = distance(inInstance.xyz, pc.cameraPos.xyz);
    if (distToCam > 80.0) {
        gl_Position = vec4(0.0);
        fragFade = 0.0;
        return;
    }
    float fadeFactor = smoothstep(80.0, 50.0, distToCam);
    // Alpha-to-coverage fade: distant blades dissolve via MSAA coverage
    // instead of shrinking into shimmering sub-pixel slivers.
    fragFade = fadeFactor;

    // Scale mesh to appropriate world size; fade height with distance
    vec3 scaledPos = inPosition * grassScale;
    scaledPos.y *= fadeFactor;

    float rot = inInstance.w;
    float cosR = cos(rot);
    float sinR = sin(rot);

    // Rotate scaled vertex position around Y axis
    vec3 rotated;
    rotated.x = scaledPos.x * cosR - scaledPos.z * sinR;
    rotated.y = scaledPos.y;
    rotated.z = scaledPos.x * sinR + scaledPos.z * cosR;

    // Wind displacement: proportional to scaled height
    float time = pc.sunDir.w;
    float windStrength = scaledPos.y * 0.06;
    vec3 worldBase = inInstance.xyz;
    float windPhase = time * 2.0 + worldBase.x * 1.3 + worldBase.z * 0.9;
    rotated.x += sin(windPhase) * windStrength;
    rotated.z += cos(windPhase * 0.7) * windStrength * 0.5;

    // Translate to world position
    vec3 worldPos = rotated + inInstance.xyz;

    gl_Position = pc.vp * vec4(worldPos, 1.0);
    fragColor = inColor * pc.tint.rgb;
    fragWorldPos = worldPos;

    // Rotate normal the same way (normals don't scale)
    vec3 rotNormal;
    rotNormal.x = inNormal.x * cosR - inNormal.z * sinR;
    rotNormal.y = inNormal.y;
    rotNormal.z = inNormal.x * sinR + inNormal.z * cosR;
    fragWorldNormal = rotNormal;
    fragUV = inUV;

    // Normal offset bias for shadow receiving; cascade selection happens in
    // the fragment shader.
    vec3 N = normalize(rotNormal);
    float NdotL = dot(N, normalize(pc.sunDir.xyz));
    float sinAngle = sqrt(1.0 - NdotL * NdotL);
    float offsetScale = 0.08 * sinAngle + 0.02;
    fragShadowPos = worldPos + N * offsetScale;
}
