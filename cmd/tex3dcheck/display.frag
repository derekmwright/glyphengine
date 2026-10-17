#version 450

// Stretches the two charts over the frame -- the graphics arm's on top, the
// compute arm's below -- so an 8-bit capture can read every probe back at a
// cell centre. The swapchain's sRGB encoding is undone first, as
// cmd/apppasscheck's target-display.frag does, so each captured byte is the
// sampled value and not a curve of it.
layout(set = 2, binding = 0) uniform sampler2D graphicsChart;
layout(set = 2, binding = 1) uniform sampler2D computeChart;
layout(push_constant) uniform Push { layout(offset = 128) vec4 extent; } pc;
layout(location = 0) out vec4 color;

void main() {
    vec2 f = gl_FragCoord.xy / pc.extent.xy;
    int col = int(f.x * 11.0);
    int row = int(fract(f.y * 2.0) * 3.0);
    vec3 v = f.y < 0.5 ? texelFetch(graphicsChart, ivec2(col, row), 0).rgb : texelFetch(computeChart, ivec2(col, row), 0).rgb;
    vec3 linear = mix(v / 12.92, pow((v + 0.055) / 1.055, vec3(2.4)), greaterThan(v, vec3(0.04045)));
    color = vec4(linear, 1.0);
}
