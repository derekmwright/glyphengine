#version 450

// Stretches the 7x4 chart over the frame so an 8-bit capture reads every
// probe back at a cell centre. The swapchain's sRGB encoding is undone first,
// as cmd/tex3dcheck's display does, so each captured byte is the probed value.
layout(set = 2, binding = 0) uniform sampler2D probes;
layout(push_constant) uniform Push { layout(offset = 128) vec4 extent; } pc;
layout(location = 0) out vec4 color;

void main() {
    vec2 f = gl_FragCoord.xy / pc.extent.xy;
    vec3 v = texelFetch(probes, ivec2(int(f.x * 7.0), int(f.y * 4.0)), 0).rgb;
    vec3 linear = mix(v / 12.92, pow((v + 0.055) / 1.055, vec3(2.4)), greaterThan(v, vec3(0.04045)));
    color = vec4(linear, 1.0);
}
