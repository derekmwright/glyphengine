// Copy the composite over the HDR scene.
//
// This pass exists because the composite reads scene colour, and a pass may not
// sample the destination it writes. One fullscreen triangle is the price of
// replacing an image you had to read first.
#version 450

layout(set = 2, binding = 0) uniform sampler2D composed;

layout(location = 0) out vec4 outColor;

void main() {
    outColor = texelFetch(composed, ivec2(gl_FragCoord.xy), 0);
}
