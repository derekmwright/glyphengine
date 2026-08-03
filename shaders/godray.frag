#version 450

// Light shafts, as a screen-space radial blur toward the sun.
//
// The trick is old and cheap: smear the bright parts of the frame outward from
// the sun's screen position. Where geometry stands between the eye and the sun
// it is dark, so it contributes nothing and leaves a gap in the smear — and a
// gap in a radial smear reads as a shadow cast through the air. Nothing here
// knows about volumes, scattering, or the depth buffer.
//
// What it cannot do follows from the same fact. It only works while the sun is
// on screen: the effect is built from pixels, so a sun just outside the frame
// has no pixels to build from. That is why real engines fade it out toward the
// screen edge rather than letting it pop, and why this does too.
//
// set 0 binding 0 is the scene as it stood before the water pass — opaque
// geometry and the sky, which is where the sun disc lives.

layout(location = 0) in vec2 fragUV;

layout(set = 0, binding = 0) uniform sampler2D sceneColor;

layout(push_constant) uniform PushConstants {
    mat4 mvp;
    mat4 model;
    vec4 tint;      // xy = sun position in UV space, z = intensity, w = decay
    vec4 sunDir;
    vec4 sunColor;  // rgb = sun colour
    vec4 pointPos;
    vec4 pointColor;
    vec4 ambient;
    vec4 cameraPos;
    vec4 fog;
} pc;

layout(location = 0) out vec4 outColor;

// SAMPLES sets how far the smear can reach before it starts to band. It is the
// whole cost of the effect.
const int SAMPLES = 48;

// bright keeps only what is plausibly the sky or the sun.
//
// Without a threshold the blur smears the whole image and reads as a lens
// smudge rather than as light. The cutoff is what makes geometry act as an
// occluder: terrain is darker than the sky it is silhouetted against, so it
// simply contributes nothing.
vec3 bright(vec3 c) {
    float l = dot(c, vec3(0.2126, 0.7152, 0.0722));
    // These are LINEAR values, and a cutoff picked by eye from sRGB numbers
    // lands far too high: the sunset disc displays near white but reads as
    // luminance 0.71 here, and an 0.80 threshold rejected it entirely, leaving
    // the effect contributing exactly nothing.
    //
    // Daytime sky is around 0.68 and the disc reaches 1.0, so the window sits
    // between them. Below it the whole sky smears and the frame washes out.
    //
    // The scene now arrives from a half-float target rather than an sRGB one.
    // The numbers did not move -- sampling an sRGB image decoded to linear
    // too -- but the ceiling did: l can exceed 1 once anything emits above it,
    // and then this window admits everything rather than selecting the sun.
    // Whoever raises a light past 1 has to revisit these two constants.
    float m = smoothstep(0.62, 0.88, l);
    return c * m;
}

void main() {
    vec2 sunUV = pc.tint.xy;
    float intensity = pc.tint.z;
    float decay = pc.tint.w;

    if (intensity <= 0.0) {
        outColor = vec4(0.0);
        return;
    }

    // Fade out as the sun approaches and leaves the frame. A hard cutoff at
    // the screen edge makes the shafts vanish in a single frame as the camera
    // turns, which is far more noticeable than their absence.
    vec2 d = abs(sunUV - 0.5) * 2.0;
    float onScreen = (1.0 - smoothstep(0.75, 1.35, max(d.x, d.y)));
    if (onScreen <= 0.0) {
        outColor = vec4(0.0);
        return;
    }

    // Step from this pixel toward the sun. density < 1 keeps the march inside
    // the region between the fragment and the sun, so shafts stay anchored to
    // the sun rather than sliding across the frame.
    // 1.0 so the march actually reaches the sun. At 0.85 it stops short, and
    // since the disc is small and its halo sits near the threshold, most
    // fragments then sample nothing bright at all and the effect vanishes.
    const float density = 1.0;
    vec2 delta = (fragUV - sunUV) * (density / float(SAMPLES));

    vec2 uv = fragUV;
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
    // fast the shafts fall off also changes how bright they are, and the
    // intensity setting stops meaning anything on its own.
    accum /= max(wsum, 1e-4);

    // Tint by the sun's own colour so the shafts warm up with it at sunset
    // rather than staying white all day.
    vec3 tint = normalize(max(pc.sunColor.rgb, vec3(0.05))) * 1.732;

    outColor = vec4(accum * tint * intensity * onScreen, 1.0);
}
