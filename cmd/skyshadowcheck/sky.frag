#version 450
#extension GL_GOOGLE_include_directive : require
layout(location=0) in vec2 fragUV;
layout(location=0) out vec4 outColor;
#include "sample.inc"
void main() { outColor = probeShadow(); }
