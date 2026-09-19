#version 450

layout(location = 0) in vec3 inPosition;
layout(location = 1) in vec3 inColor;
layout(location = 2) in vec3 inNormal;
layout(location = 3) in vec2 inUV;

layout(push_constant) uniform PushConstants {
    mat4 mvp;
    mat4 model;
    vec4 tint; // rgb = multiplier (usually 1,1,1), w = screenPxRange
} pc;

layout(location = 0) out vec2 fragUV;
layout(location = 1) out vec4 fragTint;
layout(location = 2) out float fragAlpha;
layout(location = 3) out float fragBoldBias;
layout(location = 4) out float fragGlow;

void main() {
    // Z is dropped rather than transformed, because inPosition.z carries the
    // per-line emission multiplier rather than a position.
    //
    // Text is one draw for every line an overlay holds -- MSDFText builds one
    // mesh from all of them -- so a per-LINE value cannot ride in a push
    // constant, and every other per-vertex channel is taken: inNormal is alpha,
    // screenPxRange and the bold bias, and inColor and inUV are the colour and
    // the atlas lookup. Z is the one float that was always zero here, because
    // this pipeline is ortho, depth-tested against nothing, and writes no
    // depth.
    //
    // It has to be dropped, not merely ignored: the overlay projection is
    // Ortho(-1, 1), so a z of 2 would put the glyph at clip z = -2 and Vulkan
    // would clip the whole quad away. Glyphs would vanish exactly when a game
    // asked them to glow, which reads as a text bug rather than a clip-space
    // one. With z zero -- every glyph built before this line existed -- adding
    // the third column contributed exactly the zero vector, so dropping it
    // changes no existing pixel.
    gl_Position = pc.mvp * vec4(inPosition.xy, 0.0, 1.0);
    fragUV = inUV;
    fragTint = vec4(inColor * pc.tint.rgb, inNormal.y); // .w = per-vertex screenPxRange
    fragAlpha = inNormal.x;
    fragBoldBias = inNormal.z;
    fragGlow = inPosition.z;
}
