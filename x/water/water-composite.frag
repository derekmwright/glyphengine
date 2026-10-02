// Absorb the scene by its water path length, fill in the water's own radiance,
// and add the reconstructed scattering. Writes the package's own full-resolution
// target; the present pass copies it over the HDR scene.
#version 450
#extension GL_GOOGLE_include_directive : require

layout(set = 2, binding = 0) uniform sampler2D sceneColor;
layout(set = 2, binding = 1) uniform sampler2D sceneDepth;
layout(set = 2, binding = 2) uniform sampler2D scattering;

layout(push_constant) uniform Push {
    layout(offset = 128) mat4 inverseVP;
    vec4 medium; // rgb absorption per world unit, w surface world Y
    vec4 body;   // rgb the water's own radiance this frame, w depth tolerance
} pc;

#include "water-path.glsl"

layout(location = 0) out vec4 outColor;

// Fixed four-sample bilateral reconstruction of the reduced-resolution
// scattering, weighted by how closely each neighbour's scene depth matches this
// pixel's. The footprint is written out rather than looped so the compiler has
// no nested per-pixel loop to keep, and the tolerance is relative because
// reverse-Z depth is not linear in distance: a fixed one would be far too tight
// near the camera and meaningless away from it.
vec3 reconstruct(vec2 uv, float depth) {
    ivec2 size = textureSize(scattering, 0);
    vec2 low = uv * vec2(size) - 0.5;
    ivec2 base = ivec2(floor(low));
    vec2 f = fract(low);
    vec4 a = texelFetch(scattering, clamp(base, ivec2(0), size - 1), 0);
    vec4 b = texelFetch(scattering, clamp(base + ivec2(1, 0), ivec2(0), size - 1), 0);
    vec4 c = texelFetch(scattering, clamp(base + ivec2(0, 1), ivec2(0), size - 1), 0);
    vec4 d = texelFetch(scattering, clamp(base + ivec2(1, 1), ivec2(0), size - 1), 0);
    vec4 delta = abs(vec4(a.a, b.a, c.a, d.a) - depth);
    // The absolute floor is here and not in the options: it exists so a
    // background pixel, whose reverse-Z depth is exactly zero, does not divide
    // by nothing. That is not a number a game tunes.
    vec4 weights = vec4(1.0 - f.x, f.x, 1.0 - f.x, f.x) * vec4(1.0 - f.y, 1.0 - f.y, f.y, f.y)
        * exp(-delta / max(depth * pc.body.w, 0.000002));
    float weight = dot(weights, vec4(1.0));
    if (weight > 0.00001) {
        return (a.rgb * weights.x + b.rgb * weights.y + c.rgb * weights.z + d.rgb * weights.w) / weight;
    }
    // Every neighbour disagrees about depth: a thin silhouette falling between
    // low-resolution samples. Take the nearest in depth rather than a blend of
    // four wrong ones, which is what would draw a halo around it.
    vec3 nearest = a.rgb;
    float closest = delta.x;
    if (delta.y < closest) { closest = delta.y; nearest = b.rgb; }
    if (delta.z < closest) { closest = delta.z; nearest = c.rgb; }
    if (delta.w < closest) { nearest = d.rgb; }
    return nearest;
}

void main() {
    ivec2 px = ivec2(gl_FragCoord.xy);
    ivec2 full = textureSize(sceneDepth, 0);
    vec2 uv = gl_FragCoord.xy / vec2(full);
    float depth = texelFetch(sceneDepth, px, 0).r;
    vec3 color = texelFetch(sceneColor, px, 0).rgb;

    vec3 eye = waterEye(pc.inverseVP);
    vec3 scene = waterUnproject(pc.inverseVP, uv * 2.0 - 1.0, depth);
    vec3 dir = normalize(scene - eye);
    float travel = waterPath(eye, dir, length(scene - eye), pc.medium.w);

    // Beer-Lambert over the water the view ray crossed, with the water's own
    // radiance filling in exactly what the scene lost. Red is absorbed first,
    // which is the whole reason the three coefficients are so far apart.
    vec3 transmission = exp(-pc.medium.rgb * travel);
    outColor = vec4(color * transmission + pc.body.rgb * (1.0 - transmission)
        + reconstruct(uv, depth), 1.0);
}
