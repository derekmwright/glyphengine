#version 450
#extension GL_GOOGLE_include_directive : require
layout(location=3) in vec2 fragUV;
layout(location=0) out vec4 outColor;
#include "params.inc"
void main() { outColor = parameterColor(); }
