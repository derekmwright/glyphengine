#version 450
#extension GL_GOOGLE_include_directive : require

// Volumetric clouds, raymarched at half resolution into their own target.
//
// Split out of sky.frag because that is where the frame's time was. Measured on
// 09-water, the sky pass costs 1.146 ms with this march and 0.008 ms without it:
// over 99 percent of "the sky is the top GPU pass" is these steps. The dome it
// used to share a shader with is free by comparison, so the way to make the sky
// cheaper is to march fewer pixels, not to make the dome smarter.
//
// Output is premultiplied: rgb is in-scattered radiance along the ray, and alpha
// is transmittance -- how much of what is behind the layer still gets through.
// Keeping them separate is what lets the full-resolution sky composite this over
// a dome it computes at its own resolution, rather than the march having to know
// what is behind it.

layout(location = 0) in vec2 fragUV;

layout(push_constant) uniform PushConstants {
    mat4 invVP;    // inverse view-projection
    mat4 model;    // [0].xyz = camera position
    vec4 tint;     // x = time, y = nightFactor, z = cloud raymarch steps
    vec4 sunDir;   // xyz = direction toward the body lighting the scene
    vec4 sunColor; // rgb, w = the real sun's elevation
    // The previous frame's view-projection, occupying the four vec4s the march
    // has never read. Push constants are full at 256 bytes, and reusing dead
    // slots beats adding a uniform buffer for one matrix -- but it does mean
    // this block deliberately disagrees with every other shader's packing from
    // here to fog, which is why it is spelled out rather than left implicit.
    mat4 prevVP;   // was pointPos, pointColor, ambient, cameraPos
    vec4 fog;      // zw = the real sun's horizontal direction
} pc;

// The previous frame's cloud target. Always bound; on the first frame it holds
// the clear value and the reprojection is rejected anyway.
layout(set = 0, binding = 0) uniform sampler2D historyTex;

layout(location = 0) out vec4 outColor;

#include "atmosphere.inc"

// ----- Hash / Noise -----
float hash2D(vec2 p) {
    p = fract(p * vec2(443.897, 441.423));
    p += dot(p, p.yx + 19.19);
    return fract(p.x * p.y);
}

// ---- 3D value noise, for the cloud volume ----
float hash3D(vec3 p) {
    p = fract(p * vec3(443.897, 441.423, 437.195));
    p += dot(p, p.yzx + 19.19);
    return fract((p.x + p.y) * p.z);
}

float valueNoise3D(vec3 p) {
    vec3 i = floor(p);
    vec3 f = fract(p);
    f = f * f * (3.0 - 2.0 * f);

    float n000 = hash3D(i + vec3(0, 0, 0));
    float n100 = hash3D(i + vec3(1, 0, 0));
    float n010 = hash3D(i + vec3(0, 1, 0));
    float n110 = hash3D(i + vec3(1, 1, 0));
    float n001 = hash3D(i + vec3(0, 0, 1));
    float n101 = hash3D(i + vec3(1, 0, 1));
    float n011 = hash3D(i + vec3(0, 1, 1));
    float n111 = hash3D(i + vec3(1, 1, 1));

    return mix(
        mix(mix(n000, n100, f.x), mix(n010, n110, f.x), f.y),
        mix(mix(n001, n101, f.x), mix(n011, n111, f.x), f.y),
        f.z);
}

float fbm3D(vec3 p) {
    float v = 0.0;
    float a = 0.5;
    for (int i = 0; i < 4; i++) {
        v += a * valueNoise3D(p);
        p = p * 2.03 + vec3(17.1, 9.7, 23.3);
        a *= 0.5;
    }
    return v;
}

// fbm3DDetail is fbm3D with the octaves the raymarch cannot resolve faded out.
//
// This is the fix for clouds that look like a Photoshop noise filter, worst
// while the camera turns. The finest octave has features about 113 world units
// across, and a step is 30 to 170 units at ordinary elevations and hundreds near
// the horizon -- so that octave sits at or under the sample spacing and cannot
// be integrated, only aliased. The per-pixel jitter then re-rolls the aliasing
// for every pixel, and because the jitter is keyed to screen position rather
// than to the world, rotating the camera drags the clouds through a stationary
// noise field. That is the boiling.
//
// detail is how many octaves are worth sampling, from Nyquist: an octave is
// resolvable while its wavelength stays above twice the step length. Partial
// values fade the last octave in rather than popping it, which matters because
// step length varies smoothly across the frame and a hard cutoff would draw a
// visible arc across the sky.
//
// The result is renormalised by the amplitude actually used, so dropping octaves
// does not shift the mean and quietly change how much of the sky is cloud --
// the coverage threshold outside is tuned against the full-octave range.
float fbm3DDetail(vec3 p, float detail) {
    float v = 0.0;
    float used = 0.0;
    float a = 0.5;
    for (int i = 0; i < 4; i++) {
        float w = clamp(detail - float(i), 0.0, 1.0);
        if (w <= 0.0) {
            break;
        }
        v += a * w * valueNoise3D(p);
        used += a * w;
        p = p * 2.03 + vec3(17.1, 9.7, 23.3);
        a *= 0.5;
    }
    // 0.9375 is the four-octave amplitude sum, so a reduced-octave sample lands
    // on the same scale the coverage threshold expects.
    return v * (0.9375 / max(used, 1e-4));
}

// fbm3DLow is the two-octave version used for the light march.
//
// Shadowing inside a cloud does not need the detail the shape does: the fine
// octaves only add high-frequency variation that reads as speckle once it is
// sampled four times per step. Dropping them is both cheaper and smoother,
// which is a rare direction for that trade to go.
float fbm3DLow(vec3 p) {
    float v = 0.5 * valueNoise3D(p);
    p = p * 2.03 + vec3(17.1, 9.7, 23.3);
    v += 0.25 * valueNoise3D(p);
    return v;
}


void main() {
    float time = pc.tint.x;
    float sunElevation = pc.sunColor.w;
    vec3 realSunDir = atmSunDirFrom(sunElevation, pc.fog.zw);
    vec3 sunDir = normalize(pc.sunDir.xyz);

    vec3 camPos = pc.model[0].xyz;

    // Reconstruct the view ray. The half-resolution target covers exactly the
    // same frustum as the screen, so the same inverse view-projection applies
    // and no separate matrix is needed.
    vec4 world = pc.invVP * vec4(fragUV * 2.0 - 1.0, 1.0, 1.0);
    vec3 dir = normalize(world.xyz / world.w - camPos);

    // The dome's palette, which the march uses for its ambient fill.
    vec3 zenith, horizon;
    atmSkyPalette(sunElevation, zenith, horizon);

    vec3 cloudScatter = vec3(0.0);
    float cloudTransmit = 1.0;

    // Where to reproject from. Zero means "no history", which is what a ray
    // that never entered the slab should get.
    float reprojectDist = 0.0;

    int cloudSteps = int(pc.tint.z);
    if (dir.y > 0.015 && cloudSteps > 0) {
        const float CLOUD_BOTTOM = 620.0;
        const float CLOUD_TOP    = 1500.0;
        const int   LIGHT_STEPS  = 4;
        int STEPS = cloudSteps;

        float t0 = (CLOUD_BOTTOM - camPos.y) / dir.y;
        float t1 = (CLOUD_TOP - camPos.y) / dir.y;
        t0 = max(t0, 0.0);

        // Cap the march. Near the horizon the slab crossing runs to the
        // horizon itself, and past a few tens of kilometres the samples are so
        // far apart that they alias into noise rather than resolving cloud.
        const float MAX_DIST = 34000.0;
        t1 = min(t1, MAX_DIST);

        if (t1 > t0) {
            // The middle of the marched span stands in for where the cloud is.
            // It has to come from the geometry rather than a host constant,
            // because a grazing ray crosses the slab tens of kilometres out
            // while an overhead one crosses it in hundreds of units.
            reprojectDist = (t0 + t1) * 0.5;
            float day = atmDaylight(sunElevation);
            float twi = atmTwilight(sunElevation);

            // Coverage: a little heavier at night so the sky is not empty.
            float coverage = mix(0.52, 0.44, day);

            vec2 wind = vec2(time * 0.0016, time * 0.0007);

            float stepLen = (t1 - t0) / float(STEPS);
            float transmittance = 1.0;
            vec3 scattered = vec3(0.0);

            // Forward-scattering: the sun's disc bleeds through thin cloud,
            // which is what makes edges glow when it is behind them.
            float cosT = dot(dir, sunDir);
            float g = 0.55;
            float g2 = g * g;
            float phase = (1.0 - g2) / (12.566 * pow(1.0 + g2 - 2.0 * g * cosT, 1.5));
            phase = 0.35 + 2.2 * phase;

            vec3 sunLight = mix(vec3(0.55, 0.60, 0.75), vec3(1.0, 0.97, 0.92), day);
            sunLight = mix(sunLight, vec3(1.0, 0.62, 0.34), twi * 0.8);
            vec3 skyFill = mix(zenith, horizon, 0.5) * 1.6;

            // How much of the noise the step spacing can actually resolve.
            //
            // The base octave is about 950 world units across after the 0.00035
            // and 3.0 scalings below; each further octave is 2.03 times finer.
            // An octave survives while its wavelength stays above twice the
            // step, so the count is log(475/stepLen) in base 2.03. Floored at 1
            // because a cloud layer with no octaves at all is a flat sheet.
            float detail = clamp(log(475.0 / stepLen) / log(2.03), 1.0, 4.0);

            // Jitter the first sample so the step boundaries do not band.
            //
            // Keyed to the world-space ray direction rather than to fragUV. The
            // amount of noise is the same either way, but screen-space keying
            // nails the pattern to the display: turn the camera and the clouds
            // slide through a stationary noise field, which is what makes it
            // read as a Photoshop add-noise filter rather than as grain. Keyed
            // to direction, a patch of sky keeps its jitter as the view moves,
            // so the noise travels with the cloud it belongs to.
            //
            // The scale wants to be large enough that neighbouring pixels
            // decorrelate at any sane resolution: adjacent rays differ by about
            // fov/width in radians, so 4096 keeps them well apart at 4K and
            // further apart at 1080p.
            float jitter = hash3D(dir * 4096.0);
            float t = t0 + stepLen * jitter;

            for (int i = 0; i < STEPS; i++) {
                vec3 p = camPos + dir * t;

                float h = clamp((p.y - CLOUD_BOTTOM) / (CLOUD_TOP - CLOUD_BOTTOM), 0.0, 1.0);
                vec3 q = vec3(p.xz * 0.00035 + wind, p.y * 0.0011);
                float n = fbm3DDetail(q * 3.0, detail);

                // Flatten toward both faces of the slab so clouds have bases
                // and tops rather than being cut off by the boundary.
                float profile = smoothstep(0.0, 0.22, h) * smoothstep(1.0, 0.55, h);
                float density = smoothstep(coverage, coverage + 0.30, n) * profile;

                if (density > 0.002) {
                    // Light march: how much sun reaches this sample.
                    float shadow = 0.0;
                    float ls = 90.0;
                    for (int j = 1; j <= LIGHT_STEPS; j++) {
                        vec3 lp = p + sunDir * (ls * float(j));
                        float lh = clamp((lp.y - CLOUD_BOTTOM) / (CLOUD_TOP - CLOUD_BOTTOM), 0.0, 1.0);
                        vec3 lq = vec3(lp.xz * 0.00035 + wind, lp.y * 0.0011);
                        float ln = fbm3DLow(lq * 3.0);
                        float lprofile = smoothstep(0.0, 0.22, lh) * smoothstep(1.0, 0.55, lh);
                        shadow += smoothstep(coverage - 0.04, coverage + 0.30, ln) * lprofile;
                    }
                    float lightT = exp(-shadow * 1.5);

                    vec3 lit = sunLight * lightT * phase + skyFill * 0.30;

                    float dt = density * stepLen * 0.0075;
                    // Integrate analytically over the step rather than
                    // point-sampling it, which keeps the result stable as the
                    // step length changes with view angle.
                    float absorbed = 1.0 - exp(-dt);
                    scattered += lit * absorbed * transmittance;
                    transmittance *= exp(-dt);

                    if (transmittance < 0.02) {
                        break;
                    }
                }
                t += stepLen;
            }

            // Fade the whole layer out at the horizon, where the march is
            // longest and least accurate, and where real cloud is lost to haze.
            float horizonFade = smoothstep(0.015, 0.16, dir.y);
            cloudScatter = scattered * horizonFade;
            cloudTransmit = 1.0 - (1.0 - transmittance) * horizonFade;
        }
    }

    vec4 current = vec4(cloudScatter, cloudTransmit);

    // ----- Temporal accumulation -----
    //
    // The march is a stochastic estimator: each pixel jitters its ray start, so
    // a single frame is a noisy sample of the right answer. Averaging across
    // frames converges it, which is a real fix rather than the amplitude
    // reduction sky.frag used to do -- that hid the noise by taking less of it.
    //
    // Reprojection is direction-based, using the middle of the marched slab as
    // a stand-in for where the cloud actually is. Clouds sit 620 to 1500 units
    // out and the camera walks at a few units a second, so the parallax error
    // over one frame is far below a half-resolution texel. Rotation is the
    // motion that matters here and this handles it exactly.
    if (reprojectDist > 0.0) {
        vec3 worldPoint = camPos + dir * reprojectDist;
        vec4 prevClip = pc.prevVP * vec4(worldPoint, 1.0);
        if (prevClip.w > 0.0) {
            vec2 prevUV = (prevClip.xy / prevClip.w) * 0.5 + 0.5;
            // Reject history that was off screen last frame: there is nothing
            // behind the edge of the previous frame to blend with, and sampling
            // the clamped edge smears it inward.
            if (all(greaterThanEqual(prevUV, vec2(0.0))) && all(lessThanEqual(prevUV, vec2(1.0)))) {
                vec4 history = texture(historyTex, prevUV);

                // Neighbourhood clamp. The clouds drift under wind, so history
                // is always slightly stale, and without a bound on how far it
                // may differ the blend smears moving edges into ghosts.
                // Clamping to the range of the current frame's neighbours keeps
                // the convergence where the signal is stable and discards it
                // where the picture is genuinely changing.
                vec2 texel = 1.0 / vec2(textureSize(historyTex, 0));
                vec4 lo = current;
                vec4 hi = current;
                for (int i = 0; i < 4; i++) {
                    vec2 o = vec2(i == 0 ? -1 : i == 1 ? 1 : 0, i == 2 ? -1 : i == 3 ? 1 : 0);
                    vec4 n = texture(historyTex, fragUV + o * texel);
                    lo = min(lo, n);
                    hi = max(hi, n);
                }
                history = clamp(history, lo, hi);

                outColor = mix(current, history, 0.8);
                return;
            }
        }
    }

    outColor = current;
}
