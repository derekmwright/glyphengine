// The directional cascade lookup both halves of -shadowcompute perform, in one
// file so the compute and the fragment probe cannot drift apart. Agreement
// between them is the whole point of the check, and two copies of this
// arithmetic would make a disagreement ambiguous -- a copy that fell behind
// looks exactly like a synchronization or descriptor fault.
//
// Set 1 is the engine's shared shadow/light set (set 2 for skinned lit
// shaders). Only the two bindings a compute dispatch declares with
// AppComputeDesc.ReadsShadows are declared here; see shaders/lit.frag for the
// rest of the block.

layout(set = 1, binding = 0, std140) uniform ShadowData {
    mat4 cascadeVP[2];
    vec4 nightGrade;
    vec4 skyPalette[6];
    vec4 volumetric;
} shadow;
layout(set = 1, binding = 1) uniform sampler2DArrayShadow shadowMap;

layout(push_constant) uniform ApplicationPush {
    layout(offset = 128) vec4 data[8]; // data[0].xy = the probe image extent
} pc;

const int NumCascades = 2;

// probeWorld maps a probe texel to a known world position: a flat patch of
// ground at y = 0 spanning ProbeHalfExtent either side of the origin. Both
// halves must land on the same position for the same texel, which is why the
// extent arrives as data rather than as each shader's own idea of its size --
// the compute shader's imageSize and a half-resolution fragment pass's
// gl_FragCoord agree today and would stop agreeing the moment either target's
// scale changed.
const float ProbeHalfExtent = 12.0;

vec3 probeWorld(vec2 pixel, vec2 size) {
    vec2 uv = pixel / size;
    return vec3(mix(-ProbeHalfExtent, ProbeHalfExtent, uv.x), 0.0,
                mix(-ProbeHalfExtent, ProbeHalfExtent, uv.y));
}

// probeShadow is one hardware comparison against the first cascade whose
// frustum contains the position: 1.0 lit, 0.0 fully occluded, and the bilinear
// blend of the two on a cascade texel boundary. One tap rather than the engine's
// four, because what is being compared is the comparison itself and a wider
// kernel only spreads any disagreement over more texels.
//
// The bias is a constant rather than lighting.inc's normal-and-range-scaled
// expression: the probe surface is a single flat plane with a fixed normal, so
// the scaling has nothing to vary with, and an identical constant on both sides
// cannot be the reason the two disagree.
//
// With shadows disabled every cascadeVP is zero, so the projected position is
// the cascade centre at depth 0, the cascade pass has still cleared its layer to
// depth 1.0, and CompareOpLessOrEqual answers 1.0 -- the documented unshadowed
// value, reached without a dummy image or a second code path.
float probeShadow(vec2 pixel, vec2 size) {
    vec3 world = probeWorld(pixel, size);
    for (int c = 0; c < NumCascades; c++) {
        vec4 ls = shadow.cascadeVP[c] * vec4(world, 1.0);
        vec3 proj = ls.xyz; // orthographic: w == 1
        proj.xy = proj.xy * 0.5 + 0.5;
        if (proj.x < 0.01 || proj.x > 0.99 || proj.y < 0.01 || proj.y > 0.99 || proj.z < 0.0 || proj.z > 1.0) {
            continue;
        }
        return texture(shadowMap, vec4(proj.xy, float(c), proj.z - 0.0015));
    }
    return 1.0; // outside every cascade is lit, as it is in lighting.inc
}
