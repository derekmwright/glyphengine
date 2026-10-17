// The water wave sum as statements, spliced into the stage that includes it
// rather than called as a function.
//
// That is measured, not stylistic. The same arithmetic moved into a function
// returning a struct renders `09-water -frames 150` with 239 pixels one step
// different under the fixed clock: the driver's compiler groups the float
// operations differently once they cross a call, and the surface every
// committed capture of the lake was taken from is the inline one. Spliced, the
// stage compiles to the floats it always did.
//
// In scope before it: vec3 rest (the grid vertex), vec3 pos (= rest, displaced
// in place), float time, amplitude, baseLen, noiseFrac (0 skips the noise),
// shoal (see waterShoal) and spacing. It declares dhdx, dhdz and fold for the
// normal, and jxx, jzz and jxz for waterJacobian.

    // Resolve each component's amplitude first, then how steep the sum is
    // allowed to be. Both bounds depend on the whole sum rather than on any one
    // wave, so neither can be applied inside the displacement loop.
    float amps[WAVE_COUNT];
    float kaSum = 0.0;
    for (int i = 0; i < WAVE_COUNT; i++) {
        float len = baseLen * WAVE_LEN[i];
        float amp = amplitude * WAVE_AMP[i] * shoal;

        // Drop what this grid is too coarse to carry. See MIN_SAMPLES.
        if (spacing > 0.0) {
            amp *= smoothstep(MIN_SAMPLES, FULL_SAMPLES, len / spacing);
        }

        amps[i] = amp;
        kaSum += (6.2831853 / len) * amp;
    }

    // See FOLD_LIMIT: the horizontal term is only invertible while
    // STEEPNESS * kaSum stays below one, and that depends on the caller's
    // amplitude and wavelength rather than on STEEPNESS alone.
    float steep = STEEPNESS;
    if (kaSum > 0.0) {
        steep = min(STEEPNESS, FOLD_LIMIT / kaSum);
    }

    // Accumulate the partial derivatives alongside the displacement; the
    // normal falls out of them analytically, so it costs no extra sampling and
    // is exact for the surface actually being drawn.
    float dhdx = 0.0;
    float dhdz = 0.0;
    float fold = 0.0;
    float jxx = 0.0;
    float jzz = 0.0;
    float jxz = 0.0;

    for (int i = 0; i < WAVE_COUNT; i++) {
        vec2  dir = normalize(WAVE_DIR[i]);
        float len = baseLen * WAVE_LEN[i];
        float amp = amps[i];
        float k   = 6.2831853 / len;

        // Deep-water dispersion: long waves travel faster than short ones, so
        // speed goes as sqrt(wavelength). Without it every component moves in
        // lockstep and the surface slides rather than undulates.
        float speed = sqrt(9.81 / k) * WAVE_SPEED[i];
        float phase = k * dot(dir, rest.xz) - speed * k * time * 0.15;

        float s = sin(phase);
        float c = cos(phase);

        pos.y   += amp * s;
        pos.xz  += steep * amp * dir * c;

        dhdx += dir.x * k * amp * c;
        dhdz += dir.y * k * amp * c;
        fold += steep * k * amp * s;
        jxx += steep * k * amp * s * dir.x * dir.x;
        jzz += steep * k * amp * s * dir.y * dir.y;
        jxz += steep * k * amp * s * dir.x * dir.y;
    }

    // The non-periodic layer, vertical only. Giving it a horizontal term too
    // would spend fold budget for no visible gain, since what it is here to do
    // is vary the crests rather than sharpen them.
    float noiseAmp = amplitude * noiseFrac * shoal;
    if (noiseAmp > 0.0) {
        float eps = max(spacing, 0.01);
        float s0 = swell(rest.xz, time, baseLen, spacing);
        float sx = swell(rest.xz + vec2(eps, 0.0), time, baseLen, spacing);
        float sz = swell(rest.xz + vec2(0.0, eps), time, baseLen, spacing);

        pos.y += noiseAmp * s0;

        // Differenced over one vertex step, so the normal describes the slope
        // the mesh actually has rather than the slope of the underlying field --
        // which the mesh only samples. One-sided rather than central: three
        // evaluations instead of five, and shading cannot tell the difference.
        dhdx += noiseAmp * (sx - s0) / eps;
        dhdz += noiseAmp * (sz - s0) / eps;
    }

