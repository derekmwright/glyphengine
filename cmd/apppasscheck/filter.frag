#version 450
layout(set=2,binding=0) uniform sampler2D scene;
layout(set=2,binding=1) uniform sampler2D depth;
layout(location=0) out vec4 color;
void main() {
    vec2 uv=gl_FragCoord.xy*2/vec2(textureSize(scene,0));
    color=vec4(texture(scene,uv).rgb*texture(depth,uv).r*0.3,0);
}
