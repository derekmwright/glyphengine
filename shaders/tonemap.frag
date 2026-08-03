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

layout(push_constant) uniform PushConstants {
    mat4 invVP;
    mat4 model;
    vec4 tint;      // x = exposure, y = curve select
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
// 1/(1+1). Kept here unused by default as the obvious first curve to reach for.
vec3 reinhard(vec3 c, float whitePoint) {
    float w2 = whitePoint * whitePoint;
    return c * (1.0 + c / w2) / (1.0 + c);
}

void main() {
    vec3 hdr = texture(hdrScene, fragUV).rgb;

    float exposure = pc.tint.x;
    if (exposure > 0.0) {
        hdr *= exposure;
    }

    // Curve select: 0 = identity, 1 = extended Reinhard. Identity still clamps,
    // because the swapchain is 8-bit and the hardware would anyway; doing it
    // explicitly keeps the two paths reading the same.
    if (pc.tint.y > 0.5) {
        hdr = reinhard(hdr, max(pc.tint.z, 1.0));
    }

    outColor = vec4(clamp(hdr, 0.0, 1.0), 1.0);
}
