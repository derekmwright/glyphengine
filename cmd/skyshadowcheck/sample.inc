// One world-space receiver queried by both passes, to prove shared resources.
layout(set=1,binding=0) uniform ShadowData { mat4 cascadeVP[2]; } shadow;
layout(set=1,binding=1) uniform sampler2DArrayShadow shadowMap;
vec4 probeShadow() {
    vec3 p = (shadow.cascadeVP[0] * vec4(0,0,0,1)).xyz;
    float visibility = texture(shadowMap, vec4(p.xy * 0.5 + 0.5, 0, p.z - 0.001));
    return vec4(vec3(0.1 + 0.7 * visibility), 1);
}
