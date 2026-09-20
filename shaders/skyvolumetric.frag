#version 450
#extension GL_GOOGLE_include_directive : require

// In-scattering from the local lights over the pixels nothing else covered:
// the beam of a lamp aimed at the night sky.
//
// Every lit surface gets this inside applyFog, integrated to its own depth.
// The sky cannot: it is a fullscreen triangle at the far plane, it does not
// call applyFog, and gl_FragCoord.w is 1.0 for every pixel of it. So the air
// in front of the dome needs a pass, and without one a beam aimed upward
// stops dead at the skyline -- which is what the first capture of this
// feature showed, a column crossing the dark hills and vanishing at the
// horizon as if it had hit something.
//
// WHY THIS IS A SEPARATE DRAW rather than four lines at the end of sky.frag,
// which is where it started and which is simpler to read: those four lines
// cost three pixels.
//
// Measured, because it is not the kind of claim to make from reasoning.
// `09-water -time 0.72 -yaw 1.771 -pitch -0.185 -pillars` at a fixed clock,
// against the same capture from origin/main: with the march in sky.frag, 3 of
// 921600 pixels differed by one 8-bit step (4 with the light shafts on, which
// amplify through their luminance threshold) in a scene with no volumetric
// light in it at all. The march never ran -- it takes a uniform early-out --
// and the pixels moved anyway, because the compiler schedules sky.frag's
// EXISTING arithmetic differently once the march is in the same function.
//
// Isolated rather than guessed at: with lights.inc and volumetric.inc
// included in sky.frag but the volInscatter call removed, the capture is
// byte-identical to origin/main; adding the call back reproduces the three
// pixels. Guarding the call behind the same uniform branch does not help, and
// neither does avoiding gl_FragCoord (the one input the march newly
// introduced). It is the presence of the code, not what it reads or whether
// it runs.
//
// Three pixels of a million is not a look anyone would notice. It is a
// promise that was worth keeping anyway: a scene that asks for no volumetrics
// renders the bytes it always did, and "byte-identical" is a claim a gate can
// check while "visually indistinguishable" is not. As a draw of its own the
// dome is untouched, and the cost is skipped entirely rather than branched
// over when no light scatters.

layout(location = 0) in vec2 fragUV;

// Offsets match the lit shaders' block exactly, as sky.frag's does: one
// packing convention beats a per-shader one nobody can keep in step. The
// members before cameraPos are declared only to land the ones after it where
// every other shader reads them from.
layout(push_constant) uniform PushConstants {
    mat4 invVP;    // inverse view-projection
    mat4 model;
    vec4 tint;
    vec4 sunDir;
    vec4 sunColor;
    vec4 pointPos;
    vec4 pointColor;
    vec4 ambient;
    vec4 cameraPos; // xyz = eye, w = fog density
    vec4 fog;       // x = height falloff, y = base height, zw unused here
} pc;

// The per-frame lit block, read here at binding 0 of the shadow/light set --
// the set this pass binds anyway for the light buffers, so unlike sky.frag
// (which reaches the same buffer through the cloud set at binding 1) this can
// read it where it actually lives.
layout(set = 1, binding = 0) uniform ShadowData {
    mat4 cascadeVP[2];
    vec4 nightGrade;
    vec4 skyPalette[6];
    // x = the march's Henyey-Greenstein anisotropy, y = its step count.
    vec4 volumetric;
} shadow;

// Clustered light data at set 1, bindings 3-5; see lights.inc.
#define LIGHT_SET 1

layout(location = 0) out vec4 outColor;

#include "lights.inc"
#include "volumetric.inc"

void main() {
    vec3 camPos = pc.cameraPos.xyz;

    // The world-space ray for this pixel, reconstructed exactly as sky.frag
    // reconstructs its own: the two have to agree about which direction a
    // pixel looks, or the beam lands beside the dome it is drawn over.
    vec2 ndc = fragUV * 2.0 - 1.0;
    vec4 world = pc.invVP * vec4(ndc, 0.0, 1.0);
    vec3 dir = normalize(world.xyz / world.w - camPos);

    // Two numbers a lit fragment has for free have to be built here, because
    // this is a fullscreen triangle and knows neither how far away it is nor
    // what its view depth is:
    //
    //   - how far to integrate is the froxel grid's own far plane, recovered
    //     from the binner's slice parameters (volGridFar) rather than sent
    //     again. There are no froxels past it, so there is nothing out there
    //     that could light the air;
    //   - view depth per metre along the ray is the cosine to the camera
    //     axis, and the axis is the ray through NDC (0,0) -- the same
    //     reconstruction above, at the centre of the screen. Exact for the
    //     symmetric perspective this engine builds, which is the same
    //     assumption lightcluster's screen-bound test already makes.
    vec4 axisWorld = pc.invVP * vec4(0.0, 0.0, 0.0, 1.0);
    vec3 axis = normalize(axisWorld.xyz / axisWorld.w - camPos);
    float depthPerDist = max(dot(dir, axis), 1e-4);

    vec3 scatter = volInscatter(gl_FragCoord.xy, dir, volGridFar() / depthPerDist, depthPerDist);

    // Additive, with alpha left alone by the blend state: the dome writes
    // cloud transmittance there and this has no opinion about it. Premultiplied
    // is not a question -- there is no coverage here, only light added to what
    // is already in the target.
    outColor = vec4(scatter, 0.0);
}
