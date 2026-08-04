#version 450

// Impostor bake: render a grass mesh flat into an atlas cell.
//
// Orthographic and axis-aligned, supplied whole as pc.mvp by the host, because
// the projection has to frame the mesh's own bounds exactly -- a perspective
// view would bake in foreshortening that then fights the billboard's own
// perspective at run time.

layout(location = 0) in vec3 inPosition;
layout(location = 1) in vec3 inColor;
layout(location = 2) in vec3 inNormal;
layout(location = 3) in vec2 inUV;

layout(push_constant) uniform PushConstants {
    mat4 mvp;
    mat4 model;
    vec4 tint;
} pc;

layout(location = 0) out vec3 fragColor;
layout(location = 1) out vec2 fragUV;

void main() {
    gl_Position = pc.mvp * vec4(inPosition, 1.0);
    fragColor = inColor * pc.tint.rgb;
    fragUV = inUV;
}
