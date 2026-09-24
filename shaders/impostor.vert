#version 450

layout(location = 4) in mat4 inModel;
layout(location = 8) in vec4 inTint;
layout(push_constant) uniform PushConstants {
    mat4 vp;
    mat4 atlas; // column 0 = local bound centre xyz and radius
    vec4 tint;
    vec4 sunDir;
    vec4 sunColor;
    vec4 pointPos;
    vec4 pointColor;
    vec4 ambient;
    vec4 cameraPos;
} pc;
layout(location = 0) centroid out vec3 fragColor;
layout(location = 1) out vec3 fragWorldPos;
layout(location = 2) out vec3 fragWorldNormal;
layout(location = 3) out vec2 fragUV;
layout(location = 4) out vec3 fragShadowPos;
layout(location = 5) out float fragFade;

const vec2 corners[6] = vec2[6](vec2(-1,-1),vec2(1,-1),vec2(1,1),vec2(-1,-1),vec2(1,1),vec2(-1,1));
void main() {
    vec3 centre = (inModel * vec4(pc.atlas[0].xyz,1)).xyz;
    vec3 toEye = pc.cameraPos.xyz-centre;
    vec3 forward = length(toEye)>0.0001 ? normalize(toEye) : vec3(0,0,1);
    vec3 right = cross(vec3(0,1,0),forward);
    right = length(right)>0.0001 ? normalize(right) : vec3(1,0,0);
    vec3 up = normalize(cross(forward,right));
    vec3 localEye = inverse(mat3(inModel))*toEye;
    float angle = atan(localEye.x,localEye.z);
    float cell = mod(floor(angle/(3.14159265359/4.0)+0.5)+8.0,8.0);
    float scale = max(length(inModel[0].xyz),max(length(inModel[1].xyz),length(inModel[2].xyz)));
    vec2 corner = corners[gl_VertexIndex];
    vec3 worldPos = centre+(right*corner.x+up*corner.y)*pc.atlas[0].w*scale;
    gl_Position = pc.vp*vec4(worldPos,1);
    // Each tile has a transparent margin; clamp the sample to its half texel.
    float inset = pc.atlas[1].x;
    vec2 uv = clamp(vec2(corner.x*0.5+0.5,0.5-corner.y*0.5),vec2(inset),vec2(1-inset));
    fragUV = vec2((cell+uv.x)/8.0,uv.y);
    fragColor = pc.tint.rgb*inTint.rgb;
    fragWorldPos = worldPos;
    fragWorldNormal = normalize(vec3(0,1,0)+forward*0.35);
    float ndl = dot(fragWorldNormal,normalize(pc.sunDir.xyz));
    fragShadowPos = worldPos+fragWorldNormal*(0.08*sqrt(max(0,1-ndl*ndl))+0.02);
    fragFade = inTint.w;
}
