#version 450
layout(set=2,binding=0) uniform sampler2D nearestClamp;
layout(set=2,binding=1) uniform sampler2D nearestRepeat;
layout(set=2,binding=2) uniform sampler2D linearClamp;
layout(set=2,binding=3) uniform sampler2D linearRepeat;
layout(set=1,binding=7) uniform sampler2D globalField;
layout(location=0) out vec4 color;
const vec2 probes[12] = vec2[](
    vec2(.25,.25), vec2(.75,.25), vec2(.25,.75), vec2(.75,.75),
    vec2(.375,.375), vec2(.625,.625), vec2(.5,.25), vec2(.25,.5),
    vec2(1.25,.25), vec2(.25,-.25), vec2(1,.25), vec2(.25,0));
void main() {
    vec2 uv = probes[int(gl_FragCoord.x)];
    int row = int(gl_FragCoord.y);
    float v;
    if (row == 0) v = texture(nearestClamp, uv).r;
    else if (row == 1) v = texture(nearestRepeat, uv).r;
    else if (row == 2) v = texture(linearClamp, uv).r;
    else v = texture(linearRepeat, uv).r;
    color = vec4(v, texture(globalField, uv).r, v, 1);
}
