#version 450

// Water surface: a flat grid displaced by a sum of Gerstner waves.
//
// Gerstner rather than plain sine because the horizontal term is what makes
// water look like water. A sine height field gives symmetric humps; Gerstner
// pulls vertices toward each crest as well as up, which sharpens crests and
// flattens troughs the way a real wave profile does.
//
// Vertex attributes carry water-specific data rather than their usual meaning,
// because the surface has no use for the originals and this avoids a second
// vertex format for one mesh type:
//
//   inColor   deep water colour
//   inNormal  shallow water colour -- the surface normal comes from the waves,
//             so the attribute is free (see WaterOptions in water.go)
//   inUV.x    still-water depth at this vertex, in world units, baked from the
//             heightmap at build time
//   inUV.y    the surface grid's vertex spacing in world units, also baked at
//             build time. The shader cannot otherwise know how far apart its
//             own vertices are, and that is what decides which wave components
//             this mesh is fine enough to carry.

layout(location = 0) in vec3 inPosition;
layout(location = 1) in vec3 inColor;
layout(location = 2) in vec3 inNormal;
layout(location = 3) in vec2 inUV;

layout(push_constant) uniform PushConstants {
    mat4 mvp;
    mat4 model;
    vec4 tint;      // x = time, y = amplitude, z = wavelength, w = refraction strength
    vec4 sunDir;    // xyz = direction toward sun, w = wave noise fraction
    vec4 sunColor;  // rgb, w = night factor
    vec4 pointPos;
    vec4 pointColor;
    vec4 ambient;
    vec4 cameraPos; // xyz = eye, w = fog density
} pc;

layout(location = 0) out vec3 fragDeepColor;
layout(location = 1) out vec3 fragShallowColor;
layout(location = 2) out vec3 fragWorldPos;
layout(location = 3) out vec3 fragNormal;
layout(location = 4) out float fragDepth;
layout(location = 5) out vec3 fragShadowPos;

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

void main() {
    float time      = pc.tint.x;
    float amplitude = pc.tint.y;
    float baseLen   = max(pc.tint.z, 0.001);
    float noiseFrac = pc.sunDir.w;

    vec3 pos = inPosition;

    // Shallow water flattens: a wave cannot be taller than the water it sits
    // in, and this also hides the seam where the surface mesh meets the shore,
    // since the amplitude reaches zero exactly as the depth does.
    float shoal = clamp(inUV.x * 0.7, 0.0, 1.0);
    float spacing = inUV.y;

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

    for (int i = 0; i < WAVE_COUNT; i++) {
        vec2  dir = normalize(WAVE_DIR[i]);
        float len = baseLen * WAVE_LEN[i];
        float amp = amps[i];
        float k   = 6.2831853 / len;

        // Deep-water dispersion: long waves travel faster than short ones, so
        // speed goes as sqrt(wavelength). Without it every component moves in
        // lockstep and the surface slides rather than undulates.
        float speed = sqrt(9.81 / k) * WAVE_SPEED[i];
        float phase = k * dot(dir, inPosition.xz) - speed * k * time * 0.15;

        float s = sin(phase);
        float c = cos(phase);

        pos.y   += amp * s;
        pos.xz  += steep * amp * dir * c;

        dhdx += dir.x * k * amp * c;
        dhdz += dir.y * k * amp * c;
        fold += steep * k * amp * s;
    }

    // The non-periodic layer, vertical only. Giving it a horizontal term too
    // would spend fold budget for no visible gain, since what it is here to do
    // is vary the crests rather than sharpen them.
    float noiseAmp = amplitude * noiseFrac * shoal;
    if (noiseAmp > 0.0) {
        float eps = max(spacing, 0.01);
        float s0 = swell(inPosition.xz, time, baseLen, spacing);
        float sx = swell(inPosition.xz + vec2(eps, 0.0), time, baseLen, spacing);
        float sz = swell(inPosition.xz + vec2(0.0, eps), time, baseLen, spacing);

        pos.y += noiseAmp * s0;

        // Differenced over one vertex step, so the normal describes the slope
        // the mesh actually has rather than the slope of the underlying field --
        // which the mesh only samples. One-sided rather than central: three
        // evaluations instead of five, and shading cannot tell the difference.
        dhdx += noiseAmp * (sx - s0) / eps;
        dhdz += noiseAmp * (sz - s0) / eps;
    }

    fragNormal = normalize(vec3(-dhdx, 1.0 - fold, -dhdz));

    gl_Position      = pc.mvp * vec4(pos, 1.0);
    fragWorldPos     = (pc.model * vec4(pos, 1.0)).xyz;
    fragDeepColor    = inColor;
    fragShallowColor = inNormal;
    fragDepth        = inUV.x;

    // Match lit.vert's normal-offset shadow bias so a shadow falling across
    // the water lines up with the same shadow on the shore beside it.
    float NdotL = dot(fragNormal, normalize(pc.sunDir.xyz));
    float sinAngle = sqrt(max(1.0 - NdotL * NdotL, 0.0));
    fragShadowPos = fragWorldPos + fragNormal * (0.08 * sinAngle + 0.02);
}
