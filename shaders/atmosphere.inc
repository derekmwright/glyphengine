// Shared atmosphere model.
//
// Everything here is a function of the sun's elevation — pc.sunDir.y — and
// nothing is a function of the clock. That is the point: the sky dome, the fog
// that distant geometry fades into, and the water's reflection all need the
// same answer, and deriving it from a value they already have is the only way
// to guarantee they agree. There is no uniform to forget to update.
//
// The old model had a single "night factor" that ramped linearly over five
// percent of the cycle and drove the entire palette, with the sunset glow
// multiplied by (1 - it). Night arriving therefore erased the sunset, at
// exactly the moment there should have been most of it, and dusk had no shape
// at all beyond a straight crossfade between a day palette and a night one.

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
float atmTwilight(float sunY) {
    float y = sunY / 0.20;
    return exp(-y * y);
}

// atmNight lags the sky's darkening, so stars arrive during the blue hour
// rather than the instant the sun touches the horizon.
float atmNight(float sunY) {
    return 1.0 - smoothstep(-0.22, -0.04, sunY);
}

// atmSkyPalette returns the zenith and horizon colours for a sun elevation.
//
// Three palettes rather than two. The blue hour is the one that was missing:
// overhead goes deep indigo while the horizon keeps a last band of warmth, and
// that separation is what makes dusk read as dusk instead of as the lights
// being turned down.
void atmSkyPalette(float sunY, out vec3 zenith, out vec3 horizon) {
    // The horizon is pale because that is what looking through more atmosphere
    // does, but not white: distant geometry fades into this colour, so a
    // washed-out horizon washes out the whole landscape with it.
    const vec3 zenithDay    = vec3(0.13, 0.30, 0.78);
    const vec3 horizonDay   = vec3(0.52, 0.70, 0.93);

    const vec3 zenithTwi    = vec3(0.055, 0.085, 0.26);
    const vec3 horizonTwi   = vec3(0.88, 0.42, 0.22);

    const vec3 zenithNight  = vec3(0.008, 0.010, 0.035);
    const vec3 horizonNight = vec3(0.020, 0.030, 0.075);

    float day = atmDaylight(sunY);
    float twi = atmTwilight(sunY);

    zenith  = mix(zenithNight, zenithDay, day);
    horizon = mix(horizonNight, horizonDay, day);

    zenith  = mix(zenith, zenithTwi, twi);
    horizon = mix(horizon, horizonTwi, twi);
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
vec3 atmNightShift(vec3 color, float sunY) {
    float night = 1.0 - atmDaylight(sunY);
    if (night <= 0.0) {
        return color;
    }
    float l = dot(color, vec3(0.2126, 0.7152, 0.0722));
    vec3 scotopic = vec3(l) * vec3(0.72, 0.86, 1.30);
    return mix(color, scotopic, night * 0.8);
}
