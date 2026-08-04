#version 450

// Impostor bake: albedo and coverage, unlit.
//
// Deliberately unlit. Baking the lighting in would freeze the sun at whatever
// time of day the bake ran, and the whole scene moves through a day cycle -- a
// billboard lit for noon sitting in a field at dusk is worse than no billboard.
// The quad is lit at run time instead, from the same directional light the
// blades use.
//
// Alpha carries coverage rather than the texture's own alpha channel being
// passed through: what matters downstream is "is there a blade here", which the
// cutout already decides. Anything that survives the cutout is fully opaque.

layout(location = 0) in vec3 fragColor;
layout(location = 1) in vec2 fragUV;

layout(set = 0, binding = 0) uniform sampler2D albedoTex;

layout(location = 0) out vec4 outColor;

void main() {
    vec4 t = texture(albedoTex, fragUV);
    // The same 0.5 cutout the grass pipeline uses, so the impostor's silhouette
    // is the silhouette the blades actually had.
    if (t.a < 0.5) {
        discard;
    }
    outColor = vec4(fragColor * t.rgb, 1.0);
}
