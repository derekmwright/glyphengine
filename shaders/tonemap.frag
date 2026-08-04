#version 450

// Tonemap resolve: read the HDR scene and write the swapchain.
//
// This pass exists so there is somewhere for a tonemap curve to live. The scene
// now renders into a half-float target, so highlights survive above 1 instead of
// clipping at the moment they are written, which is the thing that made every
// previous attempt at tonemapping pointless -- ACES was tried against an 8-bit
// target and reverted, because a curve built to compress values above 1 has
// nothing to do when nothing can exceed it.
//
// The default curve is deliberately identity. Adding a target and adding a look
// are separate changes, and bundling them makes it impossible to tell which one
// moved the image. Exposure and the curve are wired and reachable; they are just
// set to do nothing until someone chooses a look on purpose.
//
// Output goes to an sRGB swapchain image, so the hardware does the linear to
// sRGB encode on write. Writing an already-encoded value here would gamma it
// twice.

layout(location = 0) in vec2 fragUV;

layout(set = 0, binding = 0) uniform sampler2D hdrScene;
layout(set = 0, binding = 1) uniform sampler2D bloomTex;

layout(push_constant) uniform PushConstants {
    mat4 invVP;
    mat4 model;
    vec4 tint;      // x = exposure, y = curve select, z = white point, w = bloom intensity
    vec4 sunDir;
    vec4 sunColor;
    vec4 pointPos;
    vec4 pointColor;
    vec4 ambient;
    vec4 cameraPos;
    vec4 fog;
} pc;

layout(location = 0) out vec4 outColor;

// Reinhard, extended so that pure white maps to pure white rather than to
// 1/(1+1). The obvious first curve to reach for, and a poor default: it
// compresses midtones as hard as highlights, so reinhard(0.5, 6) is 0.34 and
// every lit surface in the scene goes flat to buy headroom it does not need.
vec3 reinhard(vec3 c, float whitePoint) {
    float w2 = whitePoint * whitePoint;
    return c * (1.0 + c / w2) / (1.0 + c);
}

// Narkowicz's fit of the ACES filmic tone curve.
//
// The engine's notes used to say not to reach for a film curve, on the grounds
// that nothing here emitted above 1 and a curve built to compress highlights had
// nothing to compress. That stopped being true: the sun disc emits 5, emissive
// materials emit 6, and at sunset 63 percent of the sky was measured sitting at
// or within a whisker of the top of the 8-bit range.
//
// That is photographic blowout, and it is what makes a sunset read wrong. The
// sun's core is supposed to clip -- a real one is thousands of times the sky and
// no display can show it -- but when the sky clips alongside it there is nothing
// left to tell them apart. This rolls the highlights off so the sky keeps its
// gradient while the disc stays above it, and unlike Reinhard it holds the
// midtones: aces(0.5) is 0.62 against Reinhard's 0.34.
vec3 aces(vec3 x) {
    const float a = 2.51;
    const float b = 0.03;
    const float c = 2.43;
    const float d = 0.59;
    const float e = 0.14;
    return clamp((x * (a * x + b)) / (x * (c * x + d) + e), 0.0, 1.0);
}

void main() {
    vec3 hdr = texture(hdrScene, fragUV).rgb;

    // Bloom is added before exposure and the curve, not after. It is light that
    // reached the sensor, so it has to go through the same response the rest of
    // the frame does -- add it afterwards and a glow stays linear while
    // everything around it is compressed, which reads as a decal rather than as
    // light.
    //
    // Skipped entirely when off. The branch is uniform across the draw, so it
    // costs nothing, and it keeps an untouched bloom target from contributing
    // whatever its memory happened to contain.
    float bloomIntensity = pc.tint.w;
    if (bloomIntensity > 0.0) {
        hdr += texture(bloomTex, fragUV).rgb * bloomIntensity;
    }

    float exposure = pc.tint.x;
    if (exposure > 0.0) {
        hdr *= exposure;
    }

    // Curve select: 0 = identity, 1 = extended Reinhard, 2 = ACES filmic.
    // Identity still clamps, because the swapchain is 8-bit and the hardware
    // would anyway; doing it explicitly keeps the paths reading the same.
    if (pc.tint.y > 1.5) {
        hdr = aces(hdr);
    } else if (pc.tint.y > 0.5) {
        hdr = reinhard(hdr, max(pc.tint.z, 1.0));
    }

    outColor = vec4(clamp(hdr, 0.0, 1.0), 1.0);
}
