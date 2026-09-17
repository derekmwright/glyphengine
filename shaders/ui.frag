#version 450

layout(location = 0) in vec2 fragUV;
layout(location = 1) in vec4 fragColor;

layout(push_constant) uniform PushConstants {
    mat4 mvp;
    mat4 model;
    vec4 tint;
    vec4 params; // x = textureMode (0=panel, 1=straight texture)
    vec4 fill;   // rgb = panel interior colour, w = its opacity; w < 0 = derive
} pc;

layout(set = 0, binding = 0) uniform sampler2D uiTexture;
layout(location = 0) out vec4 outColor;

#include "srgb.inc"

void main() {
    vec4 texel = texture(uiTexture, fragUV);

    // The game's colour is sRGB; the texel is not, because a colour texture is
    // created as R8G8B8A8_SRGB and the sampler has already decoded it.
    vec3 color = srgbToLinear(fragColor.rgb);

    if (pc.params.x > 0.5) {
        // Straight texture mode: texture * colour, respecting texture alpha.
        outColor = vec4(texel.rgb * color, texel.a * fragColor.a);
    } else {
        // Panel 9-slice mode.
        // Border (texel.a=1): decorated border at full tint opacity.
        // Center (texel.a=0): the interior fill.
        vec3 fillColor;
        float fillAlpha;
        if (pc.fill.w < 0.0) {
            // Derived: the look this shader had before the interior was
            // configurable, kept so a panel that asks for nothing does not move.
            fillColor = color * 0.2;
            fillAlpha = fragColor.a * 0.7;
        } else {
            fillColor = srgbToLinear(pc.fill.rgb);
            fillAlpha = pc.fill.w;
        }

        vec3 borderColor = color * texel.rgb;
        outColor = vec4(
            mix(fillColor, borderColor, texel.a),
            mix(fillAlpha, fragColor.a, texel.a)
        );
    }
}
