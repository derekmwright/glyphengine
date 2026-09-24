#version 450
layout(location=0) in vec3 position; // renderer.Vertex position
layout(location=3) in vec2 uv;       // renderer.Vertex UV
layout(location=0) out vec2 texcoord;
// The engine supplies VP and Model; this pass uses its own world-XZ projection.
layout(push_constant) uniform AppPush { mat4 vp; mat4 model; vec4 data[8]; } pc;
void main() {
    vec2 worldXZ = (pc.model * vec4(position, 1)).xy;
    gl_Position = vec4(worldXZ / 20.0, 0.5, 1); // positive reverse-Z depth
    texcoord = uv;
}
