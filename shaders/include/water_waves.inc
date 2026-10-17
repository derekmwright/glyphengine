// The engine water surface's wave function: the Gerstner sum and the drifting
// noise water.vert displaces its grid by, the analytic derivatives its normal
// comes from, and the Jacobian water_foam.comp marks whitecaps from.
//
// Three things have to agree on it: the geometry, the shading and the foam. A
// foam pass computing the Jacobian from its own copy of this arithmetic would
// put foam on a surface nobody draws, and nothing would say so -- it would sit
// beside the crests instead of on them. So the sum is written once, in
// water_wave_sum.inc, and both stages include it. glyphengine.WaterOptions.
// SurfaceAt is the Go mirror the checks predict from; `task waterfx` compares
// the two through a real render.
//
// This file needs nothing declared before it: no bindings and no push block.

// Five waves at spread angles and decreasing wavelength.
//
// Every Gerstner component is a plane wave, which means its crests are perfectly
// straight and infinitely long. A surface is only saved from looking that way by
// having no single component loud enough to be seen on its own -- so the
// amplitudes are deliberately close together rather than falling off steeply.
// The previous table ran 1.00, 0.52, 0.28, 0.14, and the first wave duly showed
// through as one long unbroken crest sweeping the whole lake, which reads as a
// boat wake rather than as water.
//
// Directions are spread over about 110 degrees and none is axis-aligned. An
// axis-aligned dominant wave is worse than an oblique one, because its crest
// lines up with the vertex grid and with how scenes tend to be laid out.
//
// Wavelength ratios are deliberately not integer multiples, so the sum takes a
// long time to visibly repeat.
//
// WAVE_AMP sums to one, which is what makes WaveAmplitude mean the surface's
// maximum crest height rather than just the first component's. Keep it summing
// to one if the table changes, or WaveAmplitude silently changes meaning.
const int WAVE_COUNT = 5;
const vec2 WAVE_DIR[5] = vec2[5](
    vec2( 0.92,  0.39),
    vec2( 0.64,  0.77),
    vec2( 0.99, -0.14),
    vec2( 0.29,  0.96),
    vec2( 0.78, -0.63)
);
const float WAVE_LEN[5]   = float[5](1.000, 0.730, 0.520, 0.370, 0.260);
const float WAVE_AMP[5]   = float[5](0.315, 0.252, 0.196, 0.142, 0.095);
const float WAVE_SPEED[5] = float[5](1.00, 1.17, 1.39, 1.64, 1.96);

// Steepness: how far the horizontal term pulls vertices toward each crest.
const float STEEPNESS = 0.55;

// FOLD_LIMIT bounds sum(Q*k*A) over the wave sum.
//
// Gerstner's horizontal map x -> x + Q*A*dir*cos(phase) has Jacobian
// 1 - sum(Q*k*A*sin(phase)), so it stops being invertible once sum(Q*k*A)
// reaches one: adjacent vertices swap order and the surface passes through
// itself. It shows up as hard flat shards tearing out of the wave field, with
// shading that does not match the water around them -- because the same sum
// appears as `1.0 - fold` in the normal below, and goes negative there too,
// turning those facets inside out.
//
// STEEPNESS alone does not bound this. The sum also carries k and A, which come
// from the caller's WaveLength and WaveAmplitude, so a steepness "well under
// 1.0" folds anyway once the waves are tall enough relative to their length.
// With the engine's defaults the sum reaches 0.16 and there was no problem to
// see; at WaveAmplitude 0.6 it reaches 0.95 and the surface is visibly torn.
const float FOLD_LIMIT = 0.85;

// MIN_SAMPLES is how many vertices a wavelength needs before it is a wave on
// this mesh rather than noise on it.
//
// Below two samples per wavelength a sinusoid is not representable at all --
// the grid reconstructs a lower-frequency beat pattern instead, which is a shape
// no wave in the sum has. Fading such a component out is the geometric
// equivalent of picking a coarser mip: better to omit detail than to render
// something the sampling invented.
const float MIN_SAMPLES = 1.2;
const float FULL_SAMPLES = 2.0;

// ---- Fractal noise, for breaking up the wave sum's periodicity ----
//
// A sum of sinusoids is exactly periodic, so at a glancing angle the surface
// visibly tiles: the same crest pattern marching away to the horizon. That is
// most obvious on a coarse grid, where the finest component is faded out and
// fewer remain to disguise it.
//
// The noise is added to the height, not to the phases, so the normal stays
// exactly consistent with the geometry -- its gradient is differenced from the
// same function below rather than approximated.
float hashNoise(vec2 p) {
    p = fract(p * vec2(443.897, 441.423));
    p += dot(p, p.yx + 19.19);
    return fract(p.x * p.y);
}

// Value noise in [-1, 1], smoothstep-interpolated so its gradient is continuous
// across lattice cells. A gradient that jumped at the cell boundary would show
// up as a grid of shading creases, which is the artifact this whole file has
// been trying to get rid of.
float valueNoise(vec2 p) {
    vec2 i = floor(p);
    vec2 f = fract(p);
    f = f * f * (3.0 - 2.0 * f);

    float a = hashNoise(i);
    float b = hashNoise(i + vec2(1.0, 0.0));
    float c = hashNoise(i + vec2(0.0, 1.0));
    float d = hashNoise(i + vec2(1.0, 1.0));

    return mix(mix(a, b, f.x), mix(c, d, f.x), f.y) * 2.0 - 1.0;
}

const int NOISE_OCTAVES = 3;

// swell is the non-periodic part of the height field, in units of amplitude.
//
// Feature sizes are multiples of the base wavelength rather than of the world,
// so the noise scales with the sea state instead of having to be retuned
// whenever WaveLength changes.
//
// The field drifts rather than oscillating in place. Real swell varies from one
// patch of water to the next and that variation travels with the wind, which is
// a translation; a standing pattern reads as the lake having a texture painted
// on it.
//
// Octaves stop where the grid does, for exactly the reason the wave components
// do -- see MIN_SAMPLES. Noise finer than a couple of vertices apart is not
// detail, it is the same aliasing wearing a different hat.
float swell(vec2 xz, float time, float baseLen, float spacing) {
    float total = 0.0;
    float amp = 0.62;
    float feature = baseLen * 1.7;

    for (int i = 0; i < NOISE_OCTAVES; i++) {
        float carry = smoothstep(MIN_SAMPLES, FULL_SAMPLES, feature / max(spacing, 1e-4));
        if (carry > 0.0) {
            vec2 drift = vec2(0.021, -0.013) * time * baseLen;
            total += amp * carry * valueNoise((xz + drift) / feature);
        }
        feature *= 0.5;
        amp *= 0.55;
    }
    return total;
}

// waterShoal is how much of the sea state reaches water this deep. Shallow
// water flattens: a wave cannot be taller than the water it sits in, and this
// also hides the seam where the surface mesh meets the shore, since the
// amplitude reaches zero exactly as the depth does.
float waterShoal(float depth) {
    return clamp(depth * 0.7, 0.0, 1.0);
}

// waterJacobian is the determinant of the horizontal map (x, z) -> (x', z')
// from the three partials water_wave_sum.inc accumulates: one on undisturbed
// water, falling toward zero where the horizontal term pulls neighbouring
// points together under a crest, and negative only where the surface would fold
// through itself, which FOLD_LIMIT prevents. A crest that is breaking is one
// being squeezed, so this is where foam goes. The noise layer is vertical only
// and does not enter it.
float waterJacobian(float jxx, float jzz, float jxz) {
    return (1.0 - jxx) * (1.0 - jzz) - jxz * jxz;
}

// waterFoamFresh is how much new foam a Jacobian makes: none at or above the
// threshold, rising to full over FOAM_RAMP below it. A ramp rather than a step,
// so a crest's foam has an edge the sampler can interpolate rather than a
// one-texel stair, and so a Jacobian a hair either side of the threshold on the
// GPU and on the CPU differs by a hair of coverage rather than by all of it.
const float FOAM_RAMP = 0.1;

float waterFoamFresh(float jacobian, float threshold) {
    return clamp((threshold - jacobian) / FOAM_RAMP, 0.0, 1.0);
}
