#version 450

layout(location = 0) in vec2 fragUV;
layout(location = 1) in vec4 fragColor;

layout(push_constant) uniform PushConstants {
    mat4 mvp;
    mat4 model;
    vec4 tint;
    vec4 params; // x = textureMode (0=panel, 1=straight texture)
} pc;

layout(set = 0, binding = 0) uniform sampler2D uiTexture;
layout(location = 0) out vec4 outColor;

void main() {
    vec4 texel = texture(uiTexture, fragUV);
    if (pc.params.x > 0.5) {
        // Straight texture mode: texture * vertex color, respecting texture alpha.
        outColor = texel * fragColor;
    } else {
        // Panel 9-slice mode.
        // Border (texel.a=1): decorated border at full tint opacity.
        // Center (texel.a=0): dark semi-transparent fill.
        vec3 fillColor = fragColor.rgb * 0.2;
        vec3 borderColor = fragColor.rgb * texel.rgb;
        float fillAlpha = fragColor.a * 0.7;
        outColor = vec4(
            mix(fillColor, borderColor, texel.a),
            mix(fillAlpha, fragColor.a, texel.a)
        );
    }
}
