// Single scattering of refracted sunlight through the water, integrated along
// the view ray at reduced resolution. RGB is the light to add; alpha is the
// scene depth this sample was taken at, which is what the composite weighs its
// neighbours by.
#version 450
#extension GL_GOOGLE_include_directive : require

layout(set = 2, binding = 0) uniform sampler2D sceneDepth;

layout(push_constant) uniform Push {
    layout(offset = 128) mat4 inverseVP;
    vec4 waterSun; // xyz unit vector TOWARD the refracted sun, w 1/cos to the surface
    vec4 light;    // rgb sunColor * ScatterColor * daylight, w surface world Y
    vec4 medium;   // rgb absorption per world unit, w phase asymmetry
    vec4 march;    // x span, y sample count, zw this target's extent in pixels
} pc;

#include "water-path.glsl"

layout(location = 0) out vec4 outColor;

// The same interleaved-gradient jitter the engine's volumetric.inc uses, for
// the same two reasons: neighbouring pixels must not line their samples up, and
// nothing here may depend on time, because there is no temporal filter to
// average a flicker out and because captures under GLYPHENGINE_FIXED_FRAME_TIME
// have to repeat byte for byte.
//
// It is copied rather than included. volStartJitter lives inside
// volumetric.inc, which cannot be included without declaring the clustered
// light buffers, the shadow UBO and a push block with cameraPos -- a light set
// this shader never reads. Filed as a seam issue against the engine rather than
// patched around; see water.md.
float waterJitter(vec2 px) {
    return fract(52.9829189 * fract(dot(px, vec2(0.06711056, 0.00583715))));
}

void main() {
    // The scattering target is smaller than the scene, so map this pixel back
    // to the full-resolution depth texel that will be reconstructed from it.
    // The extent comes from the CPU rather than from the scale, because the
    // renderer rounds a relative target's size and a reconstruction that is one
    // texel out shows up as a seam along every silhouette.
    vec2 uv = gl_FragCoord.xy / pc.march.zw;
    ivec2 full = textureSize(sceneDepth, 0);
    ivec2 source = clamp(ivec2(uv * vec2(full)), ivec2(0), full - 1);
    float depth = texelFetch(sceneDepth, source, 0).r;

    vec3 eye = waterEye(pc.inverseVP);
    vec3 scene = waterUnproject(pc.inverseVP, uv * 2.0 - 1.0, depth);
    vec3 dir = normalize(scene - eye);
    float travel = waterPath(eye, dir, length(scene - eye), pc.light.w);

    // Switched off by the CPU past the depth where there is no sunlight left,
    // and at night. The pass still runs and still clears, so the composite
    // never reconstructs last frame's shafts.
    if (dot(pc.light.rgb, vec3(1.0)) <= 0.0) {
        outColor = vec4(0.0, 0.0, 0.0, depth);
        return;
    }

    float eyeDepth = pc.light.w - eye.y;
    float span = min(travel, pc.march.x);
    int samples = int(pc.march.y);
    float jitter = waterJitter(gl_FragCoord.xy);
    vec3 sum = vec3(0.0);
    // Stratified rather than fixed-midpoint: midpoints align into visible
    // bands, and the per-pixel jitter trades those bands for fine grain that
    // the depth-aware reconstruction then smooths.
    //
    // Every sample's focusing factor is exactly 1 here. The refracted sunlight
    // field that would vary it is a separate system this package does not own
    // (see water.md), and 1 is the value the source returns wherever its own
    // field is inactive, so this is its uncaustic path rather than a new one.
    for (int i = 0; i < samples; i++) {
        float t = span * (float(i) + jitter) / float(samples);
        // How deep this sample is, and so how far the sunlight that reaches it
        // travelled through water on the way down: depth over the cosine of the
        // refracted sun's descent, which the CPU already inverted and clamped.
        float sampleDepth = max(eyeDepth - dir.y * t, 0.0);
        sum += exp(-pc.medium.rgb * (t + sampleDepth * pc.waterSun.w));
    }

    float g = pc.medium.w;
    float cosine = dot(dir, pc.waterSun.xyz);
    float d = max(1.0 + g * g - 2.0 * g * cosine, 1e-3);
    // Henyey-Greenstein, unnormalised, exactly as the source writes it. The
    // floor on d is the engine's volPhase convention and costs nothing: at the
    // default asymmetry d never falls below 0.12.
    float phase = (1.0 - g * g) / (d * sqrt(d));
    outColor = vec4(pc.light.rgb * phase * sum * (span / float(samples)), depth);
}
