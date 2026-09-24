#version 450
layout(set=2, binding=0) uniform sampler2D fog;   // Reads[0], RGB scatter / A depth
layout(set=2, binding=1) uniform sampler2D depth; // Reads[1], full-resolution reverse-Z
layout(location=0) out vec4 color;
void main() {
    vec2 uv = gl_FragCoord.xy / vec2(textureSize(depth, 0));
    float z = texelFetch(depth, ivec2(gl_FragCoord.xy), 0).r;
    vec2 low = uv * vec2(textureSize(fog, 0)) - 0.5;
    ivec2 base = ivec2(floor(low));
    vec2 f = fract(low);
    vec3 sum = vec3(0);
    float weight = 0;
    for (int y=0; y<2; y++) for (int x=0; x<2; x++) {
        ivec2 p = clamp(base + ivec2(x,y), ivec2(0), textureSize(fog,0)-1);
        vec4 sampleFog = texelFetch(fog, p, 0);
        vec2 bilinear = mix(1.0-f, f, vec2(x,y));
        float similarity = exp(-abs(sampleFog.a-z) / max(z*0.08, 0.00002));
        float w = bilinear.x * bilinear.y * similarity;
        sum += sampleFog.rgb * w;
        weight += w;
    }
    color = vec4(sum / max(weight, 0.00001), 0); // additive RGB; retain HDR alpha
}
