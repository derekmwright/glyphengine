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

// volStartJitter: the engine's own interleaved-gradient jitter, through the
// exported include set rather than copied. It is in volumetric_common.inc
// precisely so a pass like this one can have it -- the file binds to nothing, so
// none of the clustered light buffers volumetric.inc needs are declared here.
// The reasoning that matters lives with the function: nothing in a jitter may
// depend on time, because there is no temporal filter to average a flicker out
// and because captures under GLYPHENGINE_FIXED_FRAME_TIME have to repeat byte
// for byte.
#include "volumetric_common.inc"
#include "water-path.glsl"

layout(location = 0) out vec4 outColor;

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
    float jitter = volStartJitter(gl_FragCoord.xy);
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
    // Henyey-Greenstein, unnormalised, the same curve as the engine's volPhase
    // -- but written out here rather than called, because of the floor on d.
    //
    // volPhase has no floor, and says why: packLitUBO clamps the engine's |g| to
    // 0.99, so the smallest d it can see is 1e-4. ScatterPhase is not clamped --
    // Options passes it through -- so g == 1.0 is a value a game can set, and at
    // g == 1 looking straight down the sun's refracted direction d is exactly 0.
    // The floor is what stands between that and a NaN in the scattering target,
    // and it costs nothing elsewhere: at the default asymmetry of 0.65 the
    // smallest d is 0.1225, 122 times it.
    float phase = (1.0 - g * g) / (d * sqrt(d));
    outColor = vec4(pc.light.rgb * phase * sum * (span / float(samples)), depth);
}
