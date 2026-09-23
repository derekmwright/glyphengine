#version 450
layout(set=0,binding=0) uniform sampler2D sceneDepth;
layout(location=0) out float depth;
void main() { depth = texelFetch(sceneDepth, ivec2(gl_FragCoord.xy), 0).r; }
