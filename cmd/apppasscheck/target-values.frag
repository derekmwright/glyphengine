#version 450
layout(location=0) out float value;
void main() {
    ivec2 p = ivec2(gl_FragCoord.xy);
    value = 0.125 + 0.25 * p.x + 0.5 * p.y;
}
