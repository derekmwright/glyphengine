// Shared atmosphere model.
//
// Everything here is a function of the sun's elevation — pc.sunDir.y — and
// nothing is a function of the clock. That is the point: the sky dome, the fog
// that distant geometry fades into, and the water's reflection all need the
// same answer, and deriving it from a value they already have is the only way
// to guarantee they agree. The curves have no uniform to forget to update.
//
// The COLOURS those curves blend between are the one exception: they are data
// (see atmSkyPalette), and the three agree because there is one buffer and
// every caller is handed it, not because there is nothing to hand. `task
// skypalette` is what notices a caller that stops taking it.
//
// The old model had a single "night factor" that ramped linearly over five
// percent of the cycle and drove the entire palette, with the sunset glow
// multiplied by (1 - it). Night arriving therefore erased the sunset, at
// exactly the moment there should have been most of it, and dusk had no shape
// at all beyond a straight crossfade between a day palette and a night one.

// atmSunDirFrom rebuilds the real sun's direction from the two places the push
// constant block has room for it: its elevation, which is the y component and
// rides in sunColor.w, and its horizontal pair in fog.zw.
//
// It exists because pc.sunDir is NOT the sun. It is whichever body currently
// lights the scene, which from dusk to dawn is the moon. Handing that to
// atmSunGlow hangs a warm sunset halo on the moon at midnight — the same
// mistake as driving the palette from sunDir.y, one step further along.
//
// The result is already unit length when the caller's sun direction was, since
// both components come from the same vector.
vec3 atmSunDirFrom(float elevation, vec2 horizontal) {
    return vec3(horizontal.x, elevation, horizontal.y);
}

// atmDaylight is the day/night blend. The lower edge sits below the horizon
// because the sky stays lit for a while after the sun has gone.
float atmDaylight(float sunY) {
    return smoothstep(-0.18, 0.10, sunY);
}

// atmTwilight peaks with the sun on the horizon and falls off either side.
//
// It is independent of atmDaylight on purpose. The warm scattering has to
// survive sunset — it is strongest just after — so it cannot be gated on how
// much daylight is left.
//
// Asymmetric, because the two sides are not the same event. Above the horizon
// the warmth builds while there is still a sun to light the air; below it there
// is progressively less air left in sunlight, and the glow should go with it.
// A symmetric curve wide enough for the approach still held the horizon 37
// percent warm at an elevation of -0.20, with the stars already 71 percent out
// — the sky holding a sunset well into the night.
//
// DayNight.Twilight in Go is the same curve; change both together.
float atmTwilight(float sunY) {
    float width = sunY > 0.0 ? 0.20 : 0.115;
    float y = sunY / width;
    return exp(-y * y);
}

// There is deliberately no star-fade curve here. The stars are drawn from Go's
// DayNight.StarVisibility, which arrives as a push constant, and the copy that
// used to sit at this spot had no callers and had already drifted to different
// constants — a trap for anyone who came here to change the star fade.

// The six palette endpoints, in the order renderer/shadow.go packs them.
// Only .rgb is read; .a is std140 padding, since an array of vec3 has a
// 16-byte stride anyway.
#define ATM_ZENITH_DAY    0
#define ATM_HORIZON_DAY   1
#define ATM_ZENITH_TWI    2
#define ATM_HORIZON_TWI   3
#define ATM_ZENITH_NIGHT  4
#define ATM_HORIZON_NIGHT 5

// atmSkyPalette returns the zenith and horizon colours for a sun elevation.
//
// Three palettes rather than two. The blue hour is the one that was missing:
// overhead goes deep indigo while the horizon keeps a last band of warmth, and
// that separation is what makes dusk read as dusk instead of as the lights
// being turned down.
//
// `pal` is the six endpoints, and it is a uniform rather than the constants
// that used to sit here: what colour a planet's sky is is a property of the
// planet, and baking it into a file the dome, the fog distant geometry fades
// into and the water's reflection all include meant a game had to vendor the
// whole lighting chain -- 430 lines with no version handshake -- to change six
// vec3s. See renderer.SkyPalette for the defaults, which are exactly the
// constants this replaced.
//
// It arrives as a PARAMETER rather than being read from a uniform inside this
// function, the same way atmNightShift takes its grade. This file is included
// by sky.frag and clouds.frag as well as by lighting.inc, and those two reach
// the block through a different descriptor set from the lit family's -- so a
// uniform declared here would have to be declared at an address that suits all
// three, which there is none of. Passing it keeps atmosphere.inc free of
// bindings while every caller still hands it the same buffer.
//
// Reading the endpoints instead of compiling them in costs a handful of last-
// bit pixels, and it is worth knowing that it is the arithmetic and not a
// wrong value. The bytes the UBO carries for the default palette are the same
// bytes glslc put in the old constant table -- checked against spirv-dis of
// the previous sky.frag.spv, all eighteen -- but the driver folds a mix() of
// two compile-time constants differently from a mix() of two uniform loads,
// and the result can land on the other side of the tonemap's rounding. Over
// the whole `screenshots` set plus 09-water at five hours, dusk and midnight
// in 15-kitchen-sink, 12-particles and 16-materials -- 30 captures under a
// fixed clock -- eight move, by 1 to 4 pixels each, every one of them by
// exactly 1 of 255 in one channel.
//
// It is not the way this is written. mix() as below, the explicit
// `a + t*(b-a)` and the literal `a*(1-t) + b*t` were each captured over those
// eight scenes: the first two give the identical 1-4 pixels, the third is
// worse at 1-8. Nor can it be avoided while the palette is data -- the only
// construction with no drift at all is a constant fast path, which puts the
// six numbers back in the shader as well as in Go and is the duplication this
// change exists to delete.
void atmSkyPalette(float sunY, vec4 pal[6], out vec3 zenith, out vec3 horizon) {
    float day = atmDaylight(sunY);
    float twi = atmTwilight(sunY);

    zenith  = mix(pal[ATM_ZENITH_NIGHT].rgb,  pal[ATM_ZENITH_DAY].rgb,  day);
    horizon = mix(pal[ATM_HORIZON_NIGHT].rgb, pal[ATM_HORIZON_DAY].rgb, day);

    zenith  = mix(zenith,  pal[ATM_ZENITH_TWI].rgb,  twi);
    horizon = mix(horizon, pal[ATM_HORIZON_TWI].rgb, twi);
}

// atmSunGlow is the scattering around the sun's direction: a tight halo on top
// of a broad wash, both strongest at the horizon.
//
// Two lobes rather than one because they do different jobs. The tight lobe is
// the sun's own halo and stays near it; the broad one is what spreads colour
// across half the sky at sunset and is the difference between a warm dot and a
// warm evening.
vec3 atmSunGlow(vec3 dir, vec3 sunDir, vec3 sunCol, float sunY) {
    float twi = atmTwilight(sunY);
    float day = atmDaylight(sunY);

    float prox = max(dot(dir, sunDir), 0.0);

    // The sun's own colour drives the tint, but it fades out with the sun, so
    // it is blended toward a fixed ember so the glow keeps its warmth after
    // the directional light has gone.
    vec3 tint = mix(vec3(1.0, 0.45, 0.16), sunCol, clamp(day, 0.0, 1.0) * 0.6);

    float halo = pow(prox, 8.0) * (0.35 + 0.9 * twi);
    float wash = pow(prox, 1.6) * twi * 0.55;


    // Concentrate the wash near the horizon, where the light path through the
    // atmosphere is longest.
    float horizonBand = exp(-3.5 * abs(dir.y));

    return tint * (halo + wash * horizonBand);
}

// atmNightShift desaturates and blue-shifts a surface colour as daylight goes.
//
// Colour vision fails in the dark -- the cones stop responding and the rods,
// which are blue-biased and see no colour at all, take over. Without this a
// night scene is just a dimmed day scene, and every albedo keeps announcing
// itself: grass stays green, sand stays yellow, and the ground ends up reading
// brighter and more colourful than the sky above it, which is backwards.
//
// The shift is deliberately partial. Going fully monochrome is accurate for
// starlight and looks dead in a game.
//
// `local` is the share, 0 to 1, of the light reaching the eye from this
// fragment that came from a lamp rather than from the moon, the sky or the air
// -- lighting.inc's nightLocalShare. It backs the blend off by exactly that
// much, because rods taking over is a statement about how much light reaches
// the eye from a surface, not about what hour it is. A doorstep under a lamp
// is not dark, and keying the blend on sun altitude alone shifted it anyway.
//
// That was not only wrong in principle, it was one-sided: with `night` at its
// full 1, expanding the blend gives
//
//	g - b = 0.2*(c.g - c.b) - 0.352*luminance(c)
//
// and for every colour with no negative channel the right-hand term wins, so
// the shifted blue always came out above the shifted green. No lamp of any
// colour or intensity could put an amber pool on the ground -- measured on
// 21-streetlights' doorway, which read R 151 G 126 B 143 through the shift and
// R 228 G 103 B 27 with it ablated, off the same light.
// `grade` is the look, and it is a uniform rather than the constants that used
// to sit here: how far night goes and what colour it goes is an artistic
// decision, and baking it into a file every lit surface includes meant a game
// had to vendor the whole lighting chain to change two numbers. rgb is the
// tint a fully shifted surface's luminance is multiplied by, w the strength.
// It arrives in the per-frame ShadowData UBO; see renderer.NightGrade for the
// defaults, which are exactly the constants this replaced.
vec3 atmNightShift(vec3 color, float sunY, float local, vec4 grade) {
    // Three factors, all of which mean "do less of this": the sun is up, the
    // fragment is lamplit, the game asked for a weaker grade. Folding them
    // into one term keeps a single early-out covering all three, and keeps the
    // arithmetic in the order it was before any of them existed -- with the
    // default grade and no lamp, `night` is the same float to the bit.
    float night = (1.0 - atmDaylight(sunY)) * (1.0 - local) * grade.w;
    if (night <= 0.0) {
        return color;
    }
    float l = dot(color, vec3(0.2126, 0.7152, 0.0722));
    vec3 scotopic = vec3(l) * grade.rgb;
    return mix(color, scotopic, night);
}
