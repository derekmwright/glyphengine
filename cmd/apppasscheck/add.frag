#version 450
layout(set=2,binding=0) uniform sampler2D filtered;
layout(location=0) out vec4 color;
void main() { color=texture(filtered,gl_FragCoord.xy/(2*vec2(textureSize(filtered,0)))); }
