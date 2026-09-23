#version 450
layout(location=0) in vec2 texcoord;
layout(location=0) out float pattern;
layout(set=2,binding=0) uniform sampler2D previous;
layout(set=0,binding=0) uniform sampler2D drawTexture;
layout(set=2,binding=1) uniform sampler2D modulation;
layout(push_constant) uniform AppPush {mat4 vp;mat4 model;vec4 data[8];} pc;
void main() {
 pattern=0.3+0.6*step(0.5,fract(texcoord.x*8));
 pattern*=texture(drawTexture,texcoord).r*texture(modulation,texcoord).r;
 if (pc.data[0].x>0) pattern=mix(pattern,texture(previous,texcoord).r,0.2);
}
