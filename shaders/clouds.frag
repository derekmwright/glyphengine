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

// The per-frame environment block, at binding 1 of the same set, for the sky
// palette the march fills its ambient with. It is the buffer the lit
// pipelines read at binding 0 of their shadow set and sky.frag reads here --
// one buffer, so the fill inside a cloud cannot drift from the dome behind it.
// cascadeVP and nightGrade are declared only to place skyPalette at the offset
// renderer/shadow.go packs it at.
layout(set = 0, binding = 1) uniform ShadowData {
    mat4 cascadeVP[2];
    vec4 nightGrade;
    vec4 skyPalette[6];
} shadow;

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

// billow3D is value noise folded about its midpoint.
//
// That fold is the whole difference between cauliflower and dunes. Ordinary
// value noise is as smooth through its troughs as through its peaks, so
// eroding with it carves rolling waves; folding it puts a crease at every zero
// crossing and leaves rounded lumps between them, which is what a cumulus edge
// is made of. Reference photographs of towering cumulus show near-spherical
// bulges stacked on bulges at three or four visible scales and no smooth
// stretches at all.
float billow3D(vec3 p) {
    return 1.0 - abs(2.0 * valueNoise3D(p) - 1.0);
}

// billowFbm sums folded octaves, Nyquist-limited the same way fbm3DDetail is
// and normalised to 0..1 so the erosion strength outside means one thing
// regardless of how many octaves survived.
float billowFbm(vec3 p, float detail) {
    float v = 0.0;
    float used = 0.0;
    float a = 0.5;
    for (int i = 0; i < 4; i++) {
        float w = clamp(detail - float(i), 0.0, 1.0);
        if (w <= 0.0) {
            break;
        }
        v += a * w * billow3D(p);
        used += a * w;
        // 2.17 rather than 2.03, and a different offset from the shape noise:
        // octaves on a near-integer ratio line their features up and reinstate
        // the regularity the fold exists to destroy.
        p = p * 2.17 + vec3(19.7, 11.3, 27.1);
        a *= 0.5;
    }
    return v / max(used, 1e-4);
}

// hg is the Henyey-Greenstein phase function, normalised so that isotropic
// scattering (g = 0) returns exactly 1. That normalisation is what lets the
// multiple-scattering octaves below be written as plain weights.
float hg(float cosT, float g) {
    float g2 = g * g;
    return (1.0 - g2) / pow(max(1.0 + g2 - 2.0 * g * cosT, 1e-4), 1.5);
}

// remap rescales v from [lo,1] into [0,1], the standard way to cut a cloud out
// of a noise field: raising lo both thins the cloud and sharpens its edge,
// where multiplying would only dim it.
float remap(float v, float lo) {
    return clamp((v - lo) / max(1.0 - lo, 1e-4), 0.0, 1.0);
}

// ----- The cloud -----

// The slab the layer lives in. The base is where cumulus condense and the top
// is where the tallest tower reaches -- not where the average cloud stops.
// Most of this volume is empty on purpose: that headroom is what vertical
// development means, and a layer whose clouds all reach the ceiling is stratus
// however it was generated.
const float CLOUD_BOTTOM = 700.0;
const float CLOUD_TOP    = 3400.0;

// Extinction per unit of density per world unit. Shared by the view march and
// the light march so a cloud is as opaque to the sun as it is to the eye.
const float SIGMA = 0.020;

// cloudDensity is the whole cloud, and is the ONLY place the shape is defined.
//
// Both the view march and the light march call it. They used to build density
// from separate expressions -- different noise scale, different threshold ramp,
// and the light march kept a flat slab profile after the view march had moved
// to towers. Nothing failed; the clouds were simply lit as though a stratus
// deck sat where the towers are, which is unfixable by tuning because the two
// models never described the same object.
//
// detail is how many noise octaves to sample, and the MINIMUM IS 1. Below that
// fbm3DDetail's first octave weight is zero, it breaks out immediately and
// returns 0, so the whole function returns no cloud -- which is exactly what the
// light march was passing. Every shadow sample came back empty, lightT was 1
// everywhere, and there was no self-shadowing at all; the clouds were lit as
// though they were infinitely thin. Nothing failed and nothing looked obviously
// broken, it just looked flat.
//
// A lower detail is a level of detail, not a different cloud: the silhouette
// survives and only the fine structure goes, which is what the light march can
// afford.
float cloudDensity(vec3 p, float coverage, vec2 wind, float detail) {
    float h = clamp((p.y - CLOUD_BOTTOM) / (CLOUD_TOP - CLOUD_BOTTOM), 0.0, 1.0);

    // How tall this column is allowed to get. A cumulus field is not one
    // ceiling -- the big ones tower and the small ones stay flat, and that
    // spread is most of what reads as "towering". This is the ONLY analytic
    // term left that varies horizontally; everything about the silhouette comes
    // from noise below.
    float cov = fbm3DLow(vec3(p.xz * 0.00012 + wind, 0.0) * 3.0);
    float top = mix(0.22, 1.0, smoothstep(coverage - 0.08, coverage + 0.30, cov));
    float hn = clamp(h / max(top, 0.001), 0.0, 1.0);

    // The envelope, and nothing more: a hard flat base at the condensation
    // level and a fade out at this column's own ceiling.
    //
    // It used to also contract the footprint on a quadratic in height, which is
    // exactly what it looked like -- every cloud in the sky was the same
    // parabola at a different scale. An equation that shapes the silhouette
    // will always read as an equation. The envelope's job is to say where cloud
    // is allowed to be; what it looks like is the noise's job.
    float envelope = smoothstep(0.0, 0.04, h) * (1.0 - smoothstep(0.55, 1.0, hn));
    if (envelope <= 0.0) {
        return 0.0;
    }

    // The body: genuinely three-dimensional, so a cloud has structure through
    // its depth rather than being a footprint extruded upward. The vertical
    // scale is looser than the horizontal because cumulus are stratified --
    // features stretch across more than they do up.
    // Deliberately near-coherent vertically: about one noise period across the
    // whole slab, so a column that is dense at the base stays dense as it
    // climbs. That coherence is what gives a cumulus mass and lets it tower.
    //
    // The obvious fix for the stacked-pancake look was to raise this, and it
    // does remove the stacking -- at 0.00022 the field varies enough that no
    // two horizontal slices match. It also cuts every tower off partway up,
    // because a column now goes sparse at some height and the cloud ends there.
    // Mass and vertical variation are different jobs and this term can only do
    // one of them. It does mass; the erosion below, which is fully
    // three-dimensional, does the variation.
    float body = fbm3DDetail(vec3(p.xz * 0.00019 + wind, p.y * 0.00012) * 3.0, min(detail, 3.0));

    // Cut the cloud out of the field FIRST, then shape what survives.
    //
    // Order matters and getting it wrong is not subtle. Remapping the product
    // instead -- body * envelope against one threshold -- leaves no clear sky at
    // all: envelope is a function of height alone and body is nonzero
    // everywhere, so every column holds some cloud and the result is flat
    // overcast. The threshold is what makes gaps; it has to act on a field that
    // varies horizontally, before anything vertical is applied.
    //
    // A remap rather than a multiply because raising the bar thins the cloud AND
    // sharpens its edge, where multiplying would only dim it. The reference's
    // clouds meet the sky at a hard boundary, not a fade.
    float base = remap(body, coverage);
    if (base <= 0.0) {
        return 0.0;
    }

    float density = base * envelope;
    // detail <= 1 means resolved below is zero, so the erosion would subtract
    // nothing -- skip the billow octaves rather than paying for them and
    // multiplying the result by zero.
    if (density <= 0.001 || detail <= 1.0) {
        return clamp(density, 0.0, 1.0);
    }

    // Carve the cauliflower. Folded noise, subtracted from the edge inward, at a
    // scale a few times finer than the body -- this is what turns a smooth mass
    // into stacked bulges.
    //
    // Strength climbs with height because that is where a cumulus boils; the
    // base stays nearly uncarved so it holds the flat underside.
    //
    // Erosion has to stay inside what the step spacing can resolve, or it stops
    // carving billows and starts sampling as speckle -- the same failure the
    // grass and the star field had, detail finer than the rate it is sampled
    // at. detail carries that limit, so the strength is scaled by how much of
    // it survived rather than applied flat.
    float resolved = clamp((detail - 1.0) / 3.0, 0.0, 1.0);
    float b = billowFbm(vec3(p.xz * 0.00062 + wind * 1.7, p.y * 0.00048) * 3.0, detail);
    return remap(density, mix(0.14, 0.62, hn) * resolved * (1.0 - b));
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
    atmSkyPalette(sunElevation, shadow.skyPalette, zenith, horizon);

    vec3 cloudScatter = vec3(0.0);
    float cloudTransmit = 1.0;

    // Where to reproject from. Zero means "no history", which is what a ray
    // that never entered the slab should get.
    float reprojectDist = 0.0;

    int cloudSteps = int(pc.tint.z);
    if (dir.y > 0.015 && cloudSteps > 0) {
        const int LIGHT_STEPS = 6;
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

            // Sparse on purpose. Cumulus are individual clouds with sky
            // between them; raising the bar is what separates them, and the
            // tight ramp below is what gives them an edge to be seen against.
            float coverage = mix(0.56, 0.52, day);

            vec2 wind = vec2(time * 0.0016, time * 0.0007);

            float stepLen = (t1 - t0) / float(STEPS);
            float transmittance = 1.0;
            vec3 scattered = vec3(0.0);

            // Scattering asymmetry. Forward-biased, which is what makes edges
            // glow when the sun is behind them; the phase itself is evaluated
            // per octave inside the march.
            float cosT = dot(dir, sunDir);
            // 0.42 rather than 0.55. At 0.55 the forward lobe is 6.3 times the
            // sideways one, and a sunset is mostly spent looking near the sun --
            // that peak alone drove the result past white however the rest was
            // scaled. 0.42 keeps a visible silver lining at 3.4 times without
            // taking the whole cloud with it.
            float g = 0.42;

            // The light reaching the cloud: moonlight at night, sunlight by day.
            //
            // The night value used to be (0.55, 0.60, 0.75), which is 65 percent
            // of the daylight figure and made moonlit cloud read as white --
            // measured, a bright cloud at midnight sat at 0.507 luminance
            // against 0.811 at noon. Whatever moonlight is, it is not two thirds
            // of sunlight.
            //
            // Still far brighter than physics would allow -- real moonlight is
            // about a millionth of sunlight -- because a physically dim sky is
            // a black one. This is a deliberate artistic floor, not a
            // measurement.
            //
            // The blue bias is kept: moonlight is sunlight, but the eye's
            // scotopic response shifts toward blue at these levels, so a cool
            // cast is what a night scene is expected to look like.
            // Daylight endpoint below 1. A sunlit cloud top is bright but it is not
            // the sun, and at 1.0 it clipped across a tenth of the sky -- flat
            // white with no internal shape, and nothing to separate it from the
            // disc. Measured: clipped sky 10.7 percent to 3.2 percent.
            vec3 sunLight = mix(vec3(0.030, 0.036, 0.055), vec3(0.95, 0.93, 0.88), day);
            sunLight = mix(sunLight, vec3(1.0, 0.62, 0.34), twi * 0.8);
            vec3 skyFill = mix(zenith, horizon, 0.5) * 1.6;

            // Adaptive stepping: long strides through empty air, short ones
            // inside cloud.
            //
            // A uniform step has to be short enough for the densest part of the
            // march everywhere, and almost all of this slab is empty -- it is
            // 2700 units tall precisely so the towers have somewhere to go, and
            // most rays cross hundreds of units of nothing before touching
            // anything. At a uniform 84 units the erosion sat below the sample
            // spacing and aliased into per-pixel static instead of carving.
            //
            // So: probe coarsely, and once a probe lands in cloud, refine. The
            // fine step is what sets how much noise is resolvable, so detail is
            // computed from it rather than from the coarse stride.
            float coarse = stepLen;
            float fine = stepLen * 0.25;

            // How much of the noise the fine step can actually resolve.
            //
            // The body's base octave is about 1750 world units across after the
            // 0.00019 and 3.0 scalings; each further octave is 2.03 times finer.
            // An octave survives while its wavelength stays above twice the
            // step, so the count is log(875/fine) in base 2.03. Floored at 1
            // because a cloud layer with no octaves at all is a flat sheet.
            float detail = clamp(log(875.0 / fine) / log(2.03), 1.0, 4.0);

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
            float t = t0 + coarse * jitter;

            // Start coarse; drop to fine on the first hit and stay there until
            // a few consecutive misses say the ray has left the cloud. The
            // hysteresis matters -- switching back on a single miss makes the
            // rate flap across every internal gap and reintroduces the aliasing
            // this exists to remove.
            float step = coarse;
            int misses = 0;

            // The budget covers the worst case, a ray that spends its whole
            // crossing inside cloud at the fine rate. Rays through open sky exit
            // on t > t1 long before this, and dense ones exit on transmittance,
            // so the average cost is far below the bound.
            int budget = STEPS * 4;

            for (int i = 0; i < budget; i++) {
                if (t > t1) {
                    break;
                }
                vec3 p = camPos + dir * t;

                float density = cloudDensity(p, coverage, wind, detail);

                if (density > 0.002) {
                    if (step > fine) {
                        // Entered on a coarse stride. Back up and re-enter at
                        // the fine rate rather than integrating this sample --
                        // taking it now would credit a full coarse step of
                        // optical depth to a boundary the ray only just crossed,
                        // which thickens every cloud edge by the stride length.
                        t = max(t0, t - coarse) + fine * jitter;
                        step = fine;
                        misses = 0;
                        continue;
                    }
                    misses = 0;
                    // Light march: how much sun reaches this sample.
                    //
                    // Same cloud, sampled coarsely. The steps are long and the
                    // erosion octaves are dropped, which is a level of detail --
                    // the silhouette casting the shadow is still the silhouette
                    // being drawn. Accumulating density * step rather than a
                    // bare density keeps the optical depth in the same units the
                    // view march integrates in, so the extinction constant means
                    // one thing rather than two.
                    // Geometrically growing steps, not even ones.
                    //
                    // Even 150-unit spacing put the very first sample 150 units
                    // inside the cloud, so a sunward face -- which by definition
                    // has almost nothing between it and the sun -- came back as
                    // shadowed as the core, and the whole field rendered in
                    // permanent shade. What matters for a lit surface is the
                    // first few tens of units; what matters for the interior is
                    // total reach. Growing the step gives both from six samples:
                    // 30 units at the surface out to roughly 1500 in total.
                    float shadow = 0.0;
                    float ls = 30.0;
                    float ldist = 0.0;
                    for (int j = 0; j < LIGHT_STEPS; j++) {
                        ldist += ls;
                        vec3 lp = p + sunDir * ldist;
                        shadow += cloudDensity(lp, coverage, wind, 1.0) * ls;
                        ls *= 1.9;
                    }
                    // Beer with a powder term. Beer alone makes the sunward
                    // side of a cloud uniformly bright; the powder factor darkens
                    // shallow depths, which is what gives cumulus their dark
                    // creased edges instead of a flat lit face.
                    //
                    // SIGMA is shared with the view march below, so "how opaque
                    // is this cloud" has one answer. They were separate constants
                    // -- 2.6 against 0.020 on differently-scaled sums -- which
                    // meant a cloud could be thick to the eye and thin to the sun.
                    // Powder: Beer alone makes the sunward side of a cloud
                    // uniformly bright, and this darkens shallow depths, which
                    // is what gives cumulus their creased edges.
                    float powder = mix(1.0, 1.0 - exp(-density * 6.0), 0.6);

                    // Ambient falls off toward the cloud base.
                    //
                    // Skylight arrives from above, so the underside of a cumulus
                    // is shadowed from the sky as well as from the sun -- that
                    // dark flat base is half of what identifies the shape. A
                    // constant fill lit it as brightly as the crown and left the
                    // whole field reading as pale mush with no third dimension.
                    //
                    // Height within the column, not the slab, or a short cloud
                    // would be uniformly dark and a tall one uniformly bright.
                    float hAmb = clamp((p.y - CLOUD_BOTTOM) / (CLOUD_TOP - CLOUD_BOTTOM), 0.0, 1.0);
                    // 0.45 rather than 0.28: at the lower floor the base went
                    // nearly black and a cloud read as two flat tones, a white
                    // top and a grey bottom, with no gradation between them.
                    float ambOcc = mix(0.45, 1.0, hAmb);

                    // Multiple scattering, as three octaves.
                    //
                    // A single Henyey-Greenstein lobe is single-scattering only,
                    // and it caps a fully lit cloud at 0.41 of the sun anywhere
                    // except looking straight at it -- dark grey before any
                    // shadowing applies. That is why the field rendered as flat
                    // mush no matter what the light march did, and no amount of
                    // brightening the sun fixes it: the whole sky scales
                    // together and the clouds stay grey relative to it.
                    //
                    // Real cumulus are white from every angle because most of
                    // the light leaving them has scattered many times. Each
                    // octave here stands for one more order: dimmer, less
                    // attenuated (light that scattered sideways took a shorter
                    // path through the cloud) and more isotropic (each bounce
                    // forgets more of the original direction). Three is enough
                    // to read as white; the cost is two extra exp calls.
                    // Normalised by the octave weights, so isotropic scattering
                    // off an unshadowed sample returns exactly 1 rather than
                    // their sum. Without that division the weights are a hidden
                    // 1.85x gain on top of the phase peak, and a lit top came
                    // out at 5.5 -- far past where ACES stops preserving hue.
                    // Every cloud in a sunset rendered pure white: measured, the
                    // brightest one percent of pixels were 100 percent exactly
                    // (1,1,1) with zero saturation, so the warm light reaching
                    // them was being thrown away.
                    vec3 ms = vec3(0.0);
                    float att = 1.0;
                    float ext = 1.0;
                    float ecc = 1.0;
                    float wsum = 0.0;
                    for (int o = 0; o < 3; o++) {
                        ms += att * exp(-shadow * SIGMA * ext) * hg(cosT, g * ecc);
                        wsum += att;
                        att *= 0.55;
                        ext *= 0.5;
                        ecc *= 0.6;
                    }
                    // MS_GAIN puts a lit face just under the clip point so it
                    // reads as bright white at noon while still carrying the
                    // sun's colour at dawn and dusk. Raising it past about 1.5
                    // trades the sunset back for a marginally brighter midday.
                    const float MS_GAIN = 1.35;
                    ms *= MS_GAIN / wsum;

                    vec3 lit = sunLight * ms * powder + skyFill * 0.16 * ambOcc;

                    // step, not a fixed stride: the march changes rate as it
                    // enters and leaves cloud, and integrating the wrong length
                    // would make a cloud's opacity depend on how it was sampled.
                    float dt = density * step * SIGMA;
                    // Integrate analytically over the step rather than
                    // point-sampling it, which keeps the result stable as the
                    // step length changes with view angle.
                    float absorbed = 1.0 - exp(-dt);
                    scattered += lit * absorbed * transmittance;
                    transmittance *= exp(-dt);

                    if (transmittance < 0.02) {
                        break;
                    }
                } else if (step < coarse) {
                    // Inside the fine rate but sampling nothing. Two misses is
                    // one internal gap and is worth staying fine for; three
                    // means the ray is out.
                    if (++misses >= 3) {
                        step = coarse;
                    }
                }
                t += step;
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
