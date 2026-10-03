// Write this pass's own uniform block out, one float per column, so a check
// outside the process can read every byte of it back from a capture. The block
// is the subject: nothing else in this shader varies.
#version 450

layout(set = 2, binding = 12, std140) uniform Params { vec4 value[4]; } params;

layout(location = 0) out vec4 color;

void main() {
    int i = int(gl_FragCoord.x);
    float v = params.value[i / 4][i % 4];
    color = vec4(v, v, v, 1.0);
}
