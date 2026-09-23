#version 450
layout(location=0) in vec3 position;
layout(location=3) in vec2 uv;
layout(location=0) out vec2 texcoord;
layout(push_constant) uniform AppPush { mat4 viewProjection; mat4 model; vec4 data[8]; } pc;
void main() { gl_Position=pc.viewProjection*pc.model*vec4(position,1); texcoord=uv; }
