#version 450

// UI layer resolve: read the screen-space UI's own HDR layer and composite it
// onto the swapchain, in place of drawing the UI quads there directly.
//
// This is the second half of giving the UI somewhere to be brighter than 1.
// The scene's tonemap cannot do this job -- running the HUD through it is the
// bug #6 fixed, in a nicer suit: with an exposure that tracks the time of day
// a white label is a different white at midday than at dusk, which is not what
// a UI colour means. So the layer gets its own resolve, and that resolve is
// IDENTITY for everything at or below 1. A colour a game wrote as 0.05 still
// reaches the display as 0.05, exactly as it does on the direct path; what is
// new is only what sits above 1, which is where the bloom chain over this
// layer got its light from.
//
// The layer holds PREMULTIPLIED colour: ui.frag and msdf.frag scale by their
// own coverage before writing, and the layer blends One / OneMinusSrcAlpha. So
// the composite is a straight "over" with the same factors, and the alpha this
// writes is the layer's accumulated coverage rather than anything computed
// here.
//
// The glow is added to the colour and NOT to the alpha, which is the whole
// point of it: outside an element the layer's alpha is zero, so the composite
// leaves the scene where it is and adds the glow on top of it. That is light
// falling on what is behind the HUD rather than a translucent rectangle over
// it, and it is what makes the glow cross onto a neighbouring panel.

layout(location = 0) in vec2 fragUV;

layout(set = 0, binding = 0) uniform sampler2D uiLayer;
layout(set = 0, binding = 1) uniform sampler2D uiBloom;

// The same 256-byte block every other pipeline declares; only tint is read.
// Offsets match the lit shaders' so there is one convention rather than a
// per-shader packing to get wrong.
layout(push_constant) uniform PushConstants {
    mat4 invVP;
    mat4 model;
    vec4 tint; // x = UI exposure, w = glow strength
} pc;

layout(location = 0) out vec4 outColor;

void main() {
    vec4 ui = texture(uiLayer, fragUV);
    vec3 c = ui.rgb;

    // Skipped entirely when the glow is off. The branch is uniform across the
    // draw so it costs nothing, and it keeps a bloom chain nothing has written
    // this frame from contributing whatever its memory happened to hold.
    float strength = pc.tint.w;
    if (strength > 0.0) {
        c += texture(uiBloom, fragUV).rgb * strength;
    }

    // Fixed, and deliberately not the scene's. SetTonemap moves with the
    // day/night cycle; this does not move at all unless a game changes it, and
    // the default of 1 makes this line exact.
    float exposure = pc.tint.x;
    if (exposure > 0.0) {
        c *= exposure;
    }

    // Clamped for the same reason tonemap.frag clamps: the swapchain is 8-bit
    // and the hardware would anyway, and doing it here keeps the two resolves
    // reading the same. Alpha is the layer's own coverage and is already in
    // range.
    outColor = vec4(clamp(c, 0.0, 1.0), ui.a);
}
