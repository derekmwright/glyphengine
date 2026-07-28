#version 450

layout(location = 0) in vec2 fragUV;
layout(location = 1) in vec4 fragTint;
layout(location = 2) in float fragAlpha;
layout(location = 3) in float fragBoldBias;

layout(set = 0, binding = 0) uniform sampler2D msdfAtlas;
layout(location = 0) out vec4 outColor;

float median(float r, float g, float b) {
    return max(min(r, g), min(max(r, g), b));
}

void main() {
    vec3 msd = texture(msdfAtlas, fragUV).rgb;
    float sd = median(msd.r, msd.g, msd.b);
    float screenPxDistance = fragTint.w * (sd - 0.5 + fragBoldBias);
    float opacity = clamp(screenPxDistance + 0.5, 0.0, 1.0);
    outColor = vec4(fragTint.rgb, opacity * fragAlpha);
}
