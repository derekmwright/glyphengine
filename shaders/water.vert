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
    vec4 sunDir;    // xyz = direction toward sun
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

// Four waves at spread angles and decreasing wavelength. Directions that are
// not parallel keep the sum from collapsing into visible one-dimensional
// corduroy; the wavelength ratios are deliberately not integer multiples so
// the pattern takes a long time to visibly repeat.
const vec2 WAVE_DIR[4] = vec2[4](
    vec2( 1.00,  0.00),
    vec2( 0.62,  0.78),
    vec2(-0.44,  0.90),
    vec2( 0.85, -0.53)
);
const float WAVE_LEN[4]   = float[4](1.00, 0.61, 0.37, 0.23);
const float WAVE_AMP[4]   = float[4](1.00, 0.52, 0.28, 0.14);
const float WAVE_SPEED[4] = float[4](1.00, 1.27, 1.63, 2.11);

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

void main() {
    float time      = pc.tint.x;
    float amplitude = pc.tint.y;
    float baseLen   = max(pc.tint.z, 0.001);

    vec3 pos = inPosition;

    // Shallow water flattens: a wave cannot be taller than the water it sits
    // in, and this also hides the seam where the surface mesh meets the shore,
    // since the amplitude reaches zero exactly as the depth does.
    float shoal = clamp(inUV.x * 0.7, 0.0, 1.0);
    float spacing = inUV.y;

    // Resolve each component's amplitude first, then how steep the sum is
    // allowed to be. Both bounds depend on the whole sum rather than on any one
    // wave, so neither can be applied inside the displacement loop.
    float amps[4];
    float kaSum = 0.0;
    for (int i = 0; i < 4; i++) {
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

    for (int i = 0; i < 4; i++) {
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
