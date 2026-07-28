#version 450

// Per-vertex (binding 0): standard Vertex layout (only pos and uv used)
layout(location = 0) in vec3 inPos;     // quad corner (-0.5..0.5)
layout(location = 1) in vec3 inColor;   // unused (Vertex.Color)
layout(location = 2) in vec3 inNormal;  // unused (Vertex.Normal)
layout(location = 3) in vec2 inUV;      // quad UV

// Per-instance (binding 1): particle data
layout(location = 4) in vec4 inPosSize;  // xyz = world pos, w = size
layout(location = 5) in vec4 inInstColor; // rgba

layout(push_constant) uniform PushConstants {
    mat4 vp;         // [0..15]  view-projection
    mat4 model;      // [16..31] model[0..2] = cameraRight, model[4..6] = cameraUp
    vec4 tint;       // [32..35]
    vec4 sunDir;     // [36..39]
    vec4 sunColor;   // [40..43]
    vec4 pointPos;   // [44..47]
    vec4 pointColor; // [48..51]
    vec4 ambient;    // [52..55]
    vec4 cameraPos;  // [56..59]
} pc;

layout(location = 0) out vec2 fragUV;
layout(location = 1) out vec4 fragColor;

void main() {
    vec3 right = vec3(pc.model[0][0], pc.model[0][1], pc.model[0][2]);
    vec3 up    = vec3(pc.model[1][0], pc.model[1][1], pc.model[1][2]);

    vec3 worldPos = inPosSize.xyz
        + (inPos.x * right + inPos.y * up) * inPosSize.w;

    gl_Position = pc.vp * vec4(worldPos, 1.0);
    fragUV = inUV;
    fragColor = inInstColor;
}
