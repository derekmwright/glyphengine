#version 450
layout(set=2,binding=0) uniform sampler2D chart;
layout(push_constant) uniform Push { layout(offset=128) vec4 extent; } pc;
layout(location=0) out vec4 color;
void main() {
    ivec2 p = ivec2(gl_FragCoord.xy / pc.extent.xy * vec2(12,4));
    vec3 v = texelFetch(chart, p, 0).rgb;
    // Undo the swapchain's sRGB encoding so each captured byte measures the
    // sampled field directly, without losing precision to a tone curve.
    vec3 linear = mix(v / 12.92, pow((v + .055) / 1.055, vec3(2.4)), greaterThan(v, vec3(.04045)));
    color = vec4(linear, 1);
}
