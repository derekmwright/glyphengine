#version 450

layout(location = 0) in vec2 fragUV;
layout(location = 1) in vec4 fragTint;
layout(location = 2) in float fragAlpha;
layout(location = 3) in float fragBoldBias;
layout(location = 4) in float fragGlow;

// Only glow.y is read here, and the four members above it exist solely to land
// it at offset 176 -- the same offset ui.frag reads it from. sky.frag declares
// the members it ignores for the same reason: one packing convention for the
// 256-byte block beats a per-shader one nobody can keep in step. The emission
// multiplier itself arrives per vertex (see msdf.vert), because one draw
// carries every line of text an overlay holds.
layout(push_constant) uniform PushConstants {
    mat4 mvp;
    mat4 model;
    vec4 tint;
    vec4 params;
    vec4 fill;
    vec4 glow; // y = premultiply alpha before writing
} pc;

layout(set = 0, binding = 0) uniform sampler2D msdfAtlas;
layout(location = 0) out vec4 outColor;

#include "srgb.inc"

float median(float r, float g, float b) {
    return max(min(r, g), min(max(r, g), b));
}

void main() {
    vec3 msd = texture(msdfAtlas, fragUV).rgb;
    float sd = median(msd.r, msd.g, msd.b);
    float screenPxDistance = fragTint.w * (sd - 0.5 + fragBoldBias);
    float opacity = clamp(screenPxDistance + 0.5, 0.0, 1.0);
    // The tint is a colour the game chose, so it is sRGB; the atlas is not a
    // colour at all and is sampled above as the distance field it is.
    //
    // The emission multiplier lands after the decode, not before: it is linear
    // light added to a colour, and folding it into the sRGB value would push
    // the argument of srgbToLinear past 1 where that curve means nothing. Zero
    // is no glow, and multiplying by exactly 1.0 is exact, so a line that asks
    // for none writes the bits it wrote before this existed.
    outColor = vec4(srgbToLinear(fragTint.rgb) * (1.0 + fragGlow), opacity * fragAlpha);

    // Premultiply, for the UI layer only. See ui.frag for the whole argument;
    // the two pipelines draw into the same layer and have to agree about what
    // is in it.
    if (pc.glow.y > 0.5) {
        outColor.rgb *= outColor.a;
    }
}
