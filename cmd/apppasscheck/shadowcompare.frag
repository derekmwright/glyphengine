#version 450

// Reports both probes and their difference as bytes a screenshot can be read
// for. Undoing the swapchain's sRGB encoding is what makes the captured byte
// measure the probe directly instead of a tone curve; see target-display.frag.
layout(set = 2, binding = 0) uniform sampler2D computeShadow;
layout(set = 2, binding = 1) uniform sampler2D fragmentShadow;
layout(set = 2, binding = 2) uniform sampler2D shape;
layout(push_constant) uniform ApplicationPush {
    layout(offset = 128) vec4 extent; // xy = probe extent, zw = frame extent
} pc;
layout(location = 0) out vec4 color;

vec3 encode(vec3 v) {
    return mix(v / 12.92, pow((v + .055) / 1.055, vec3(2.4)), greaterThan(v, vec3(.04045)));
}

void main() {
    ivec2 p = ivec2(min(gl_FragCoord.xy * pc.extent.xy / pc.extent.zw, pc.extent.xy - 1.0));
    float a = texelFetch(computeShadow, p, 0).r;
    float b = texelFetch(fragmentShadow, p, 0).r;
    // The top band carries the LUT-times-uniform product so the dispatch's other
    // two bindings are measured too; the rest is the comparison.
    if (gl_FragCoord.y < 8.0) {
        color = vec4(encode(vec3(texelFetch(shape, p, 0).r)), 1);
        return;
    }
    // Blue is the difference at 255x, so one captured byte of blue is 1/65025 of
    // disagreement. A byte-level comparison of red against green alone cannot
    // tell exact agreement from agreement to within half a byte.
    color = vec4(encode(vec3(a, b, clamp(abs(a - b) * 255.0, 0.0, 1.0))), 1);
}
