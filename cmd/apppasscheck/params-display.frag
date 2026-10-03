// Stretch the 16x1 probe chart over the frame so a screenshot carries it, and
// undo the swapchain's sRGB encoding first so each captured byte measures the
// block's float directly rather than through a tone curve. Same trick as
// target-display.frag, over a different chart shape.
#version 450

layout(set = 2, binding = 0) uniform sampler2D chart;
layout(push_constant) uniform Push { layout(offset = 128) vec4 extent; } pc;

layout(location = 0) out vec4 color;

void main() {
    int col = int(gl_FragCoord.x / pc.extent.x * 16.0);
    vec3 v = texelFetch(chart, ivec2(clamp(col, 0, 15), 0), 0).rgb;
    vec3 linear = mix(v / 12.92, pow((v + .055) / 1.055, vec3(2.4)), greaterThan(v, vec3(.04045)));
    color = vec4(linear, 1.0);
}
