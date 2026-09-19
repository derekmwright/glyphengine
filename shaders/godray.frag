#version 450

// Light shafts, as a screen-space radial blur toward the sun.
//
// The trick is old and cheap: smear the bright parts of the frame outward from
// the sun's screen position. Where geometry stands between the eye and the sun
// it is dark, so it contributes nothing and leaves a gap in the smear — and a
// gap in a radial smear reads as a shadow cast through the air. Nothing here
// knows about volumes, scattering, or the depth buffer.
//
// EVERY CONSTANT BELOW WAS RE-DERIVED IN #50, because the ones this file
// shipped with described an effect that had never produced a pixel: the
// pipeline was built on 2026-07-29 and nothing bound it until this issue. The
// numbers next to each one are measurements; where an old claim turned out to
// be wrong the correction says so.
//
// What it cannot do follows from having no depth buffer. It cannot tell the air
// in front of a distant hill from the ground two metres from the eye, so left
// alone it lights both. The lobe is what keeps it off the foreground, and it is
// an approximation rather than a fix — see shaftLobeRadius in
// renderer/commands.go for what it costs and what the alternative looked like.
//
// It also only works while the sun is on screen: the effect is built from
// pixels, so a sun outside the frame has none to build from. The fade that
// keeps that from popping lives on the CPU side (shaftEdgeFade, app.go),
// because it depends only on where the sun is and the renderer needs the answer
// before it decides whether to run this pass at all.
//
// set 0 binding 0 is the scene as it stood before the water pass — opaque
// geometry and the sky, which is where the sun disc lives.

layout(location = 0) in vec2 fragUV;

layout(set = 0, binding = 0) uniform sampler2D sceneColor;

// The engine's shared 256-byte push block. This pass fills it by hand rather
// than through packLightingPC (see recordLightShafts), so only the fields named
// below hold anything; everything else arrives zeroed.
layout(push_constant) uniform PushConstants {
    mat4 mvp;
    mat4 model;
    vec4 tint;      // xy = sun position in UV space, z = strength, w = decay
    vec4 sunDir;    // xy = 1 / lobe radius, per axis, in UV space
                    // zw = the brightness window, low and high, linear luminance
    vec4 sunColor;
    vec4 pointPos;
    vec4 pointColor;
    vec4 ambient;
    vec4 cameraPos;
    vec4 fog;
} pc;

layout(location = 0) out vec4 outColor;

// SAMPLES sets how finely the ray between the fragment and the sun is walked.
// It is the whole cost of the effect: every one of these is a texture fetch,
// and nothing else in here is expensive.
//
// Measured on a Radeon RX 7900 XTX at 1280x720, MSAA 4x, as PassShafts alone
// over five interleaved 200-frame runs of `09-water -time 0.72 -yaw 1.771
// -pitch -0.185 -pillars`, which puts the sun in the middle of the frame and so
// is close to the worst case for this pass:
//
//	48 taps   0.163 ms   (0.144 – 0.169)
//	24 taps   0.102 ms   (0.097 – 0.104)
//
// Halving them saves 0.06 ms and costs most of what the jitter below buys: the
// banding metric on the lit hillside goes from 0.28 mean / 1.03 max at 48 to
// 0.43 / 1.94 at 24, against 0.70 / 3.31 with no jitter at all. Not a trade
// worth making for a pass that is already under 5 % of this frame.
const int SAMPLES = 48;

// bright keeps only what is plausibly the sky or the sun.
//
// Without a threshold the blur smears the whole image and reads as a lens
// smudge rather than as light. The cutoff is what makes geometry act as an
// occluder: terrain is darker than the sky it is silhouetted against, so it
// simply contributes nothing.
//
// These are LINEAR values and the window sits in a measured gap. Read off
// 09-water captures — the tonemap is identity and the swapchain is sRGB, so a
// capture byte decoded from sRGB is the scene value, clamped at 1:
//
//	terrain, and a pillar in silhouette      0.055 – 0.09
//	plain sky at noon, far from the sun      0.345
//	plain sky at noon, 60 px from the sun    0.566
//	sky at dusk, away from the sun           0.35 – 0.45
//	cloud                                    0.85 – above 1
//	the sunset glow around the disc          0.85 – above 1
//	the disc itself                          5.0 (SunDiscColor's boost)
//
// So 0.62 to 0.88 -- the default, renderer.LightShaftShape.Threshold, which
// arrives in pc.sunDir.zw -- admits cloud, the sunset glow and the disc, and
// rejects every plain sky measured and everything on the ground. Those are
// measurements of the DEFAULT sky palette; a game that changes the palette
// (#12) is changing the numbers in the table above, which is why the window
// is data and not a constant here. Dropping the lower edge to
// 0.5 would admit the whole midday sky — 85 % of the frame in `09-water -time
// 0.5` sits at 0.45 to 0.50 — and smear the image into itself.
//
// Two claims that used to be here are gone. "The sunset disc reads as luminance
// 0.71, and an 0.80 threshold rejected it entirely, leaving the effect
// contributing exactly nothing" and "the disc reaches 1.0" are both wrong:
// DayNight.SunDiscColor multiplies the disc by 5, so it clears this window by a
// factor of five and always did. The observation they were invented to explain
// — that the effect contributed nothing — had one cause, which was that nothing
// ever drew it.
//
// What does still hold is the ceiling. l can exceed 1 once anything emits above
// it, and then this window admits everything rather than selecting the sun.
// Whoever raises a light past 1 has to revisit these two constants.
vec3 bright(vec3 c) {
    float l = dot(c, vec3(0.2126, 0.7152, 0.0722));
    float m = smoothstep(pc.sunDir.z, pc.sunDir.w, l);
    return c * m;
}

// Interleaved gradient noise, from a pixel's own coordinates and nothing else.
//
// It offsets where each pixel's march starts, which is what turns the sample
// grid into noise instead of into rings. It has to be a function of the pixel
// and only the pixel: anything animated would shimmer, since there is no
// temporal filter here to average it out, and it would break renders under
// GLYPHENGINE_FIXED_FRAME_TIME, which have to repeat byte for byte.
//
// Measured on the hillside the shafts fall on in `09-water -time 0.72 -yaw
// 1.771 -pitch -0.185 -pillars -shafts 0.35`, box 670,480,170x160, as the
// Laplacian of the added light after an 8x8 box blur — the blur tells a step
// apart from dither: with the jitter 0.28 mean / 1.03 max, without it 0.70 /
// 3.31. The picture is plainer than the number: amplified 4x, the unjittered
// difference is a set of concentric arcs and the jittered one is a smooth
// gradient with a fine grain. A per-column profile sees neither, because the
// bands are arcs that cross a column and average out inside it.
float startJitter(vec2 px) {
    return fract(52.9829189 * fract(dot(px, vec2(0.06711056, 0.00583715))));
}

void main() {
    vec2 sunUV = pc.tint.xy;
    float strength = pc.tint.z;
    float decay = pc.tint.w;

    if (strength <= 0.0) {
        outColor = vec4(0.0);
        return;
    }

    // The scattering lobe: how much of this pixel's march survives, by how far
    // the pixel is from the sun. pc.sunDir.xy is 1/radius per axis, so r comes
    // out in units of the lobe's radius with the aspect ratio already in it.
    //
    // Squared rather than plain, so the falloff is fast near the sun and gentle
    // at the rim; plain smoothstep leaves the edge of the lobe as a visible
    // disc boundary on flat ground.
    //
    // The early-out is not only tidiness. Outside the lobe this pass does no
    // texture fetches at all, which is most of the screen whenever the sun is
    // near an edge.
    float r = length((sunUV - fragUV) * pc.sunDir.xy);
    float lobe = 1.0 - smoothstep(0.0, 1.0, r);
    lobe *= lobe;
    if (lobe <= 0.0) {
        outColor = vec4(0.0);
        return;
    }

    // Step from this pixel toward the sun, the whole way. Every pixel inside
    // the lobe therefore samples the occluders between it and the sun as well
    // as the sun itself, which is what the streaks are made of; the lobe, not
    // the length of the march, is what confines the result.
    //
    // There used to be a `density` factor here, with a comment explaining that
    // 0.85 made the effect vanish because the march then stopped short of the
    // disc. That was true, and the factor still had no job: shortening every
    // ray by the same fraction is a worse version of what the lobe does,
    // because it shortens the rays at the sun as much as the ones far from it.
    vec2 delta = (fragUV - sunUV) / float(SAMPLES);

    // Start a fraction of a step in, per pixel. Without this the 48 sample
    // positions line up across neighbouring pixels and the result steps.
    vec2 uv = fragUV - delta * startJitter(gl_FragCoord.xy);
    vec3 accum = vec3(0.0);
    float illum = 1.0;
    float wsum = 0.0;

    for (int i = 0; i < SAMPLES; i++) {
        uv -= delta;
        accum += bright(texture(sceneColor, clamp(uv, 0.0, 1.0)).rgb) * illum;
        wsum += illum;
        illum *= decay;
    }

    // Normalise by the weights actually used rather than by the sample count.
    // Dividing by SAMPLES makes the result depend on decay, so changing how
    // fast the shafts fall off would also change how bright they are, and the
    // strength setting would stop meaning anything on its own.
    accum /= max(wsum, 1e-4);

    // No tint. The old code multiplied by normalize(sunColor) * sqrt(3), to
    // "warm the shafts up at sunset rather than staying white all day" — but
    // accum is built out of the sunset sky's own pixels, which are already
    // orange, so that counted the warmth twice. It is what made the spike's
    // haze pink: over a pillar it took the added light to linear RGB 0.208 /
    // 0.133 / 0.104, red nearly twice green. Without it the same pillar reads
    // 0.157 / 0.123 / 0.111, the colour of the sky the light came from.
    outColor = vec4(accum * strength * lobe, 1.0);
}
