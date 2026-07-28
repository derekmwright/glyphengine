#version 450

layout(location = 0) out vec2 fragUV;

void main() {
    // Fullscreen triangle from vertex index — no vertex buffer needed
    vec2 positions[3] = vec2[](vec2(-1, -1), vec2(3, -1), vec2(-1, 3));
    vec2 pos = positions[gl_VertexIndex];
    gl_Position = vec4(pos, 0.0, 1.0);
    fragUV = pos * 0.5 + 0.5;
}
