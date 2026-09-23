#version 450
layout(set=0,binding=0) uniform sampler2DMS sceneDepth;
layout(location=0) out float depth;
void main() {
    depth = 0.0;
    for (int i=0; i<textureSamples(sceneDepth); ++i)
        depth = max(depth, texelFetch(sceneDepth, ivec2(gl_FragCoord.xy), i).r);
}
