#version 450
layout(set=1,binding=7) uniform sampler2D applicationPattern;
layout(location=0) out vec4 color;
void main() {
    vec2 uv=gl_FragCoord.xy/vec2(textureSize(applicationPattern,0));
    color=vec4(vec3(0.16,0.34,0.08)*texture(applicationPattern,uv).r,1);
}
