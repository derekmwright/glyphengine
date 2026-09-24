#version 450
layout(location=0) in vec2 texcoord;
layout(location=0) out float field; // R16F attachment, summed by BlendAdditive
void main() {
    vec2 edge = smoothstep(vec2(0), vec2(0.16), texcoord)
              * smoothstep(vec2(0), vec2(0.16), 1.0 - texcoord);
    field = 0.6 * edge.x * edge.y; // soft translucent rectangles, not opaque stamps
}
