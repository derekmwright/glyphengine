#version 450
layout(set=2, binding=0) uniform sampler2D scene; // Reads[0], resolved HDR before bloom
layout(set=2, binding=1) uniform sampler2D depth; // Reads[1], reverse-Z scene depth
layout(push_constant) uniform AppPush {
    layout(offset=128) mat4 inverseVP;
    vec4 lightColor;
} pc;
layout(location=0) out vec4 scatter;
void main() {
    // Derive the actual low-resolution extent, including odd window sizes.
    vec2 uv = gl_FragCoord.xy / floor(vec2(textureSize(depth, 0)) * 0.5);
    float z = texture(depth, uv).r;
    vec4 nearPoint = pc.inverseVP * vec4(uv * 2 - 1, 1, 1);
    vec4 farPoint = pc.inverseVP * vec4(uv * 2 - 1, z, 1);
    vec3 ray = farPoint.xyz / farPoint.w - nearPoint.xyz / nearPoint.w;
    float distanceFog = smoothstep(35.0, 95.0, length(ray));
    // A shallow bank extends just above the horizon; the upper sky stays clear.
    float bank = 1.0 - smoothstep(0.015, 0.085, normalize(ray).y);
    vec3 incident = pc.lightColor.rgb * 0.32 + texture(scene, uv).rgb * 0.06;
    scatter = vec4(incident * distanceFog * bank, z); // A keeps depth for upsampling
}
