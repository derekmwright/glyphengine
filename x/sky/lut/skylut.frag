#version 450
#extension GL_GOOGLE_include_directive : require

layout(location = 0) in vec2 fragUV;

// The same block x/sky's sky.frag declares, member for member, and for the same
// reason: the renderer pushes one layout for every shader in the frame, so
// pointPos through cameraPos are declared here purely to land sunColor.w and
// fog.zw at the offsets the recorder writes them to. One packing convention
// beats a per-shader one nobody can keep in step.
layout(push_constant) uniform PushConstants {
    mat4 invVP;    // inverse view-projection
    mat4 model;    // [0].xyz = camera position
    vec4 tint;     // x = time, y = nightFactor, z = cloud raymarch steps
    vec4 sunDir;   // xyz = direction toward the body lighting the scene
    vec4 sunColor; // rgb, w = the real sun's elevation
    vec4 pointPos;
    vec4 pointColor;
    vec4 ambient;
    vec4 cameraPos;
    vec4 fog;      // zw = the real sun's horizontal direction
} pc;

// The baked sky, at light-set binding 7 + lut.ShaderTextureSlot. Set 1 is the
// shadow/light set, which the sky pipeline binds for every dome; the four
// application image samplers at bindings 7..10 are what Renderer.SetShaderTexture
// fills, and this is the last of them.
//
// Three axes in two dimensions: sun proximity across within a sun-elevation
// slice, the slices across after it, view elevation down. See bake.go for the
// axes and the sizes, which are the same four numbers as below.
layout(set = 1, binding = 10) uniform sampler2D skyLUT;

layout(location = 0) out vec4 outColor;

// For atmSunDirFrom, and for nothing else.
//
// The one line it is could have been written out here. Including the engine's
// own file instead means the dome, the fog distant geometry fades into and the
// water's reflection reconstruct the sun from the push block the same way rather
// than three ways -- and it makes spirv_test.go a compile gate on the include
// set whose curves bake.go copies into Go, so a reworded signature there fails
// at build rather than at the next time someone looks at a sunset.
#include "atmosphere.inc"

// The table's shape, which has to match bake.go. TestShaderAndBakeAgreeOnTheGrid
// reads these three lines out of this file and compares them with the constants
// in Go, because two copies of a grid are two things that can disagree about a
// texel.
//
// There used to be a fourth, LUT_RANGE = 3.0, because the table was RGBA8
// holding sqrt(radiance/3) and this shader squared it back. The table is
// R16G16B16A16_SFLOAT now and holds radiance, so there is no transfer here and
// no range for a palette to exceed. One consequence worth knowing: every
// interpolation below -- the sampler's within a texel pair, and the mix across
// two sun slices -- now happens in LINEAR radiance rather than in an encoded
// domain that bowed each blend toward its darker neighbour.
const float LUT_VIEW = 64.0;
const float LUT_SUN  = 32.0;
const float LUT_PROX = 32.0;

void main() {
    vec3 camPos = pc.model[0].xyz;

    // Reconstruct the world-space ray direction from the screen UV, exactly as
    // every other far-plane pass does.
    vec2 ndc = fragUV * 2.0 - 1.0;
    vec4 world = pc.invVP * vec4(ndc, 0.0, 1.0);
    vec3 dir = normalize(world.xyz / world.w - camPos);

    // The REAL sun's elevation and direction, not the current light's. pc.sunDir
    // is whichever body lights the scene; this sky has no moon, so the two are
    // the same vector today -- reading the sun's own anyway is what keeps that
    // true if one is ever added.
    float sunElevation = pc.sunColor.w;
    vec3 realSunDir = atmSunDirFrom(sunElevation, pc.fog.zw);

    // ── the three axes, each the inverse of bake.go's ──

    // View elevation, signed-square so the texels sit near the horizon.
    float vn = 0.5 + 0.5 * sign(dir.y) * sqrt(abs(dir.y));
    float v = (0.5 + vn * (LUT_VIEW - 1.0)) / LUT_VIEW;

    // Proximity to the sun, on the chord rather than the cosine so the texels
    // sit near the sun.
    float prox = clamp(dot(dir, realSunDir), -1.0, 1.0);
    float pn = 1.0 - sqrt(0.5 - 0.5 * prox);
    // Half a texel in from each end of the slice. The slices are laid side by
    // side along u with no padding between them, so this inset is the whole of
    // what stops the sampler's filter at one slice's edge from reaching into the
    // next one's -- which would show up as a thin wrong-coloured ring around the
    // sun at certain hours and nowhere else.
    float px = 0.5 + pn * (LUT_PROX - 1.0);

    // Sun elevation, the same signed-square axis, and the one axis the shader
    // filters itself. It cannot be left to the sampler: u already carries
    // proximity, and interpolating along u between slices would blend the far
    // side of one sky with the near side of the next.
    float sn = 0.5 + 0.5 * sign(sunElevation) * sqrt(abs(sunElevation));
    float sf = sn * (LUT_SUN - 1.0);
    // Clamped to the second-last slice so the +1 below stays inside the texture.
    // At the top of the range that leaves sfrac at 1, which picks the last slice
    // exactly.
    float s0 = min(floor(sf), LUT_SUN - 2.0);
    float sfrac = sf - s0;

    float w = LUT_SUN * LUT_PROX;
    // textureLod at level 0, not texture(), because level 0 is the only level a
    // lookup has any use for and this says so without depending on a derivative
    // or on a mip chain.
    //
    // It is NOT here to fix an artifact, and the comment that used to say it was
    // has been measured and was wrong. The worry was that the elevation axis is a
    // square root, so dv/dy grows without bound as dir.y approaches zero, and an
    // implicit-derivative fetch would therefore pick a high mip along the horizon
    // -- where a high mip of this layout is an average across slices. Measured
    // 2026-10-03 on an RX 7900 XTX: with texture() in place of both fetches below,
    // the frame is PIXEL-IDENTICAL, both with the horizon out of shot and with it
    // across the middle of a 640x480 frame (lutskycheck -pitch 0, compared with
    // cmd/pngsame).
    //
    // The reason is the axis itself. It is scaled so that near the horizon one
    // screen pixel is about one texel: at 480 pixels over a 60-degree vertical
    // field of view, the first pixel above the horizon moves v by 1.05 texels,
    // which is an LOD of 0.07. The chain was reachable at the time of that
    // measurement -- CreateDataTexture built 11 levels for this 1024x64 image and
    // set MaxLod to the count -- and simply never reached. The table is uploaded
    // through CreateTextureRGBA16F now and asks for no chain at all, so there is
    // no longer a level above 0 to pick; the measurement is what says that losing
    // it changed nothing.
    //
    // So this stays because it is free and unconditional, not because it is
    // load-bearing today: a coarser axis or a smaller table would be relying on
    // that measurement instead of on this call.
    vec3 a = textureLod(skyLUT, vec2((s0 * LUT_PROX + px) / w, v), 0.0).rgb;
    vec3 b = textureLod(skyLUT, vec2(((s0 + 1.0) * LUT_PROX + px) / w, v), 0.0).rgb;

    // The fetch is the radiance. Alpha is the transmittance the stars and the
    // discs would blend against; this sky draws neither, and the dome is opaque.
    outColor = vec4(mix(a, b, sfrac), 1.0);
}
