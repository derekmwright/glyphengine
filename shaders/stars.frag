#version 450

layout(location = 0) in vec2 fragUV;

layout(push_constant) uniform PushConstants {
    mat4 invVP;    // inverse view-projection
    mat4 model;    // [0..2] = camera position (reuses model slot)
    vec4 tint;     // x = time, y = nightFactor, z = milky way, w = star density
    vec4 sunDir;   // x = 1 when a real band panorama is bound at set 0
    vec4 sunColor;
    vec4 pointPos;
    vec4 pointColor;
    vec4 ambient;
} pc;

layout(location = 0) out vec4 outColor;

// The band panorama, when one is supplied. The pass binds a descriptor here
// either way -- a 1x1 white fallback otherwise -- so sunDir.x is what says
// whether sampling it means anything.
layout(set = 0, binding = 0) uniform sampler2D bandTex;

// Hash function for pseudo-random star placement
float hash(vec3 p) {
    p = fract(p * vec3(443.897, 441.423, 437.195));
    p += dot(p, p.yzx + 19.19);
    return fract((p.x + p.y) * p.z);
}

// Trilinear value noise on the same hash, for the galaxy band's structure.
float vnoise(vec3 p) {
    vec3 i = floor(p);
    vec3 f = fract(p);
    f = f * f * (3.0 - 2.0 * f);
    return mix(
        mix(mix(hash(i + vec3(0, 0, 0)), hash(i + vec3(1, 0, 0)), f.x),
            mix(hash(i + vec3(0, 1, 0)), hash(i + vec3(1, 1, 0)), f.x), f.y),
        mix(mix(hash(i + vec3(0, 0, 1)), hash(i + vec3(1, 0, 1)), f.x),
            mix(hash(i + vec3(0, 1, 1)), hash(i + vec3(1, 1, 1)), f.x), f.y),
        f.z);
}

float fbm(vec3 p) {
    float sum = 0.0;
    float amp = 0.5;
    for (int i = 0; i < 4; i++) {
        sum += amp * vnoise(p);
        p *= 2.03;
        amp *= 0.5;
    }
    return sum;
}

// Ridged multifractal. Plain fbm makes round patches of one size, which is
// what "splotchy" is; folding each octave about its midpoint and squaring it
// turns the field into filaments and wisps, and feeding each octave's value
// forward as the next one's weight makes the fine detail cling to the ridges
// instead of spreading evenly.
float ridged(vec3 p) {
    float sum = 0.0;
    float amp = 0.5;
    float w = 1.0;
    for (int i = 0; i < 5; i++) {
        float n = 1.0 - abs(2.0 * vnoise(p) - 1.0);
        n *= n;
        sum += n * amp * w;
        w = clamp(n * 2.0, 0.0, 1.0);
        p *= 2.07;
        amp *= 0.5;
    }
    return sum;
}

// The galactic plane, as a fixed axis rather than anything derived from the
// day cycle. The band's tilt across the sky is most of why it reads as a galaxy
// seen edge-on from inside it; hanging it off the sun would swing it around the
// sky over the night and read as a rotating smear.
//
// The pole is close to horizontal on purpose. The band is the plane at right
// angles to it, so a near-vertical pole lays the band flat around the horizon
// where nothing looks at it; a near-horizontal one arcs it from one horizon up
// over the zenith and down the other side, which is the view people picture.
const vec3 GALACTIC_POLE = vec3(0.8619, 0.1603, -0.4810);

// Direction of the galactic centre, in the plane. The real band is not evenly
// bright along its length -- it is dramatically brighter toward Sagittarius and
// thins out toward the anticentre -- and that asymmetry is a strong part of
// reading it as a galaxy seen from inside one arm.
const vec3 GALACTIC_CENTER = vec3(-0.4382, 0.0000, -0.8989);

// Generate stars for one grid scale. Returns colour, not brightness: real stars
// are not white, and the variation is most of what stops a field of identical
// white dots reading as a texture.
vec3 starLayer(vec3 dir, float scale, float threshold, float brightness, float time) {
    vec3 cell = floor(dir * scale);
    vec3 local = fract(dir * scale) - 0.5;

    float h = hash(cell);

    // Only some cells contain a star
    if (h > threshold) return vec3(0.0);

    // Star position within cell
    vec3 starPos = vec3(
        hash(cell + vec3(1.0, 0.0, 0.0)) - 0.5,
        hash(cell + vec3(0.0, 1.0, 0.0)) - 0.5,
        hash(cell + vec3(0.0, 0.0, 1.0)) - 0.5
    );

    float dist = length(local - starPos * 0.4);

    // A soft point a couple of pixels wide, not a hard sub-pixel dot. The dot
    // version looks sharper in a still and is mostly invisible in practice:
    // at these scales it is smaller than a pixel, so whether it appears depends
    // on where the sample lands, and most stars simply never get hit. Same
    // aliasing the grass had, and the same fix -- give it a footprint.
    float star = exp(-dist * dist * 130.0);

    // Twinkling, gently. A strong one on a point this small reads as flicker.
    float twinkle = 0.8 + 0.2 * sin(h * 6283.185 + time * 2.0);

    // Blue-white through to warm, biased toward the cool end the way the real
    // sky is. Driven by a different hash than placement so colour and position
    // do not correlate into visible stripes.
    float ch = hash(cell + vec3(7.31, 3.17, 11.53));
    vec3 tint = mix(vec3(0.74, 0.82, 1.0), vec3(1.0, 0.86, 0.70), ch * ch);

    return tint * star * brightness * twinkle;
}

void main() {
    float nightFactor = pc.tint.y;
    if (nightFactor <= 0.0) {
        outColor = vec4(0.0);
        return;
    }

    float time = pc.tint.x;
    float milkyWay = pc.tint.z;
    float starDensity = pc.tint.w;
    vec3 camPos = pc.model[0].xyz;

    // Reconstruct world-space ray direction from screen UV
    vec2 ndc = fragUV * 2.0 - 1.0;
    vec4 world = pc.invVP * vec4(ndc, 0.0, 1.0);
    vec3 dir = normalize(world.xyz / world.w - camPos);

    // Stars only above horizon
    if (dir.y <= 0.0) {
        outColor = vec4(0.0);
        return;
    }

    // Fade near horizon to avoid harsh cutoff
    float horizonFade = smoothstep(0.0, 0.08, dir.y);

    // Distance from the galactic plane, as a soft band. Everything about the
    // galaxy hangs off this: the glow, the dust, and the star density.
    float lat = dot(dir, GALACTIC_POLE);

    // Two components, not one. A single Gaussian wide enough to be visible is
    // also wide enough to look like weather; the real profile is a narrow
    // bright spine sitting in a much fainter, wider halo.
    float spine = exp(-lat * lat * 75.0);
    float halo = exp(-lat * lat * 16.0);
    float band = spine * 0.72 + halo * 0.28;

    vec3 col = vec3(0.0);

    // structure is the band's shape: bright star-cloud regions, cut by dust.
    // It drives star density as well as the underglow, because the thing that
    // stops this reading as a cloud is that it is made of stars. A smooth
    // luminous mass with soft edges is a cloud, whatever colour it is and
    // whatever shape it is cut into -- the first four attempts here all looked
    // like weather. Unresolved grain is the cue that says star field.
    float structure = 0.0;
    float core = 0.0;
    float dustBlock = 0.0;
    vec3 mwTint = vec3(0.7, 0.75, 0.9);

    // A supplied panorama replaces the procedural band outright. It is one
    // texture fetch against roughly fifty hash lookups, and it is the real
    // sky's structure rather than an approximation of it.
    if (milkyWay > 0.0 && pc.sunDir.x > 0.5) {
        // Hemi-octahedral: the direction is projected onto an octahedron and
        // the upper half unfolded into the square. Nothing but arithmetic --
        // no atan2 to jump by 2*pi and seam the mip selection, no acos to
        // collapse at the zenith, and no half of the image below the horizon
        // that is never sampled. The galactic rotation is baked into the map.
        vec2 p = dir.xz / (abs(dir.x) + abs(dir.y) + abs(dir.z));
        vec2 uv = vec2(p.x + p.y, p.x - p.y) * 0.5 + 0.5;
        vec3 panorama = texture(bandTex, uv).rgb;
        // The panorama carries its own stars however well they were removed,
        // and its own black sky. Scaled down hard: it is a haze behind the
        // renderer's stars, not a lit surface.
        col += panorama * 0.55 * milkyWay;
        structure = 0.0;
    } else if (milkyWay > 0.0 && band > 0.004) {
        // One cloud field, low frequency and stretched hard along the band --
        // big soft masses, not small smears. Everything below reads off this
        // same field so the layers nest instead of fighting: the warm core sits
        // inside the midtone because it is literally the top of it.
        vec3 inPlane = dir - GALACTIC_POLE * lat;
        // Near-isotropic on purpose. Stretching the noise itself smears every
        // feature into a streak; the band's own falloff already supplies the
        // long shape, so the clouds inside it should stay puffy.
        vec3 q = inPlane * 6.0 + GALACTIC_POLE * lat * 9.0;
        float cloud = fbm(q);

        // Brightness along the band, peaking toward the galactic centre.
        vec3 planeDir = normalize(inPlane);
        float along = dot(planeDir, GALACTIC_CENTER);
        // Wide on purpose: this is a galaxy seen edge on from inside it, so the
        // warm inner region runs most of the way along the band rather than
        // brightening over a short stretch.
        core = smoothstep(-0.80, 0.90, along);
        float lengthwise = 0.45 + 0.55 * core;

        // 1. Midtone: the broad body of the band, amber-purple.
        float midMask = smoothstep(0.28, 0.66, cloud);

        // 2. Highlights: the same field further up, and confined to a narrow
        //    lane along the band's spine. Gated on the cloud field alone they
        //    pool into a blob wherever it peaks; multiplying by a tight
        //    across-band falloff makes them run lengthwise inside the warm
        //    region, which is what the core of a galaxy looks like from in it.
        float hotLane = exp(-lat * lat * 300.0);
        float hotMask = smoothstep(0.46, 0.80, cloud) * hotLane;

        // 3. Blockout: its own field, equally chunky, multiplied through hard.
        //    Softening this is what made it invisible before.
        // Stretched far harder than the cloud field: varying quickly across the
        // band and slowly along it is what makes dust read as lanes rather than
        // as a hole punched in the middle of it.
        // Near-isotropic, like the cloud field it sits in front of. Any stretch
        // here pulls the dust into smears; billowing shapes need the sampling
        // to be roughly square.
        vec3 r = inPlane * 5.0 + GALACTIC_POLE * lat * 7.0;
        float blockField = fbm(r + vec3(31.0, 7.0, 19.0));
        // Finer detail added before the threshold, not after: perturbing the
        // field breaks the silhouette into a ragged edge, where thresholding a
        // smooth field can only ever give a smooth contour however hard the
        // ramp is.
        blockField += 0.26 * (fbm(r * 4.4 + vec3(3.0, 11.0, 23.0)) - 0.5);
        // Concentrated along the midline, by raising the bar away from it
        // rather than fading the result. Fading would soften precisely the
        // edges the perturbation and the tight ramp exist to sharpen; biasing
        // the threshold instead makes dust rarer toward the band's edges while
        // whatever does appear stays just as crisp.
        float dustBias = 0.34 * (1.0 - exp(-lat * lat * 55.0));

        // Tight ramp: the edge resolves over a narrow range instead of fading
        // across the whole cloud.
        float block = smoothstep(0.50 + dustBias, 0.58 + dustBias, blockField);

        vec3 purple = vec3(0.38, 0.28, 0.66);
        vec3 amber = vec3(1.00, 0.58, 0.24);
        vec3 hotWhite = vec3(1.00, 0.82, 0.55);

        // Warm toward the core, cool at the edges -- position, not noise, so
        // the two colours occupy their own regions instead of interleaving.
        vec3 mid = mix(purple, amber, core * (0.35 + 0.65 * spine));

        vec3 painted = mid * midMask * band * lengthwise * 1.20;
        painted += hotWhite * hotMask * band * lengthwise * 3.00;
        dustBlock = block;
        painted *= 1.0 - 0.99 * block;

        col += painted * 0.042 * milkyWay;

        mwTint = mix(mid, hotWhite, hotMask);
        structure = band * lengthwise * midMask * (1.0 - 0.99 * block);
    }

    float inBand = structure * milkyWay;

    // Compositing order, far to near: the band, then its own unresolved grain,
    // then the star field, then the camera.
    //
    // The band and its grain are one thing and sit behind everything -- the
    // grain is the galaxy's own stars, too small to resolve, so the dust in
    // front of them takes them with it (structure already carries that).
    //
    // The star field is nearer than all of it and is not touched by the dust.
    // It is drawn last for that reason: a lane with foreground stars across it
    // is what the sky actually looks like, and occluding them to make the dust
    // read as solid would be putting the whole field behind the galaxy.

    // Grain: dense faint points that merge rather than resolve, carrying the
    // band's colour. Points with a footprint rather than high-frequency noise,
    // which crawls the moment the camera turns.
    col += mwTint * starLayer(dir, 130.0, 0.95 * inBand, 0.15, time);
    col += mwTint * starLayer(dir, 190.0, 1.00 * inBand, 0.09, time);
    col += mwTint * starLayer(dir, 280.0, 1.00 * inBand, 0.05, time);

    // Density is not uniform across the sky. A flat field reads as a texture --
    // the eye finds the regularity immediately -- so a low-frequency noise
    // thins whole regions and leaves others crowded, the way dust and depth
    // actually do it.
    //
    // One sample, shared by all three layers, taken per pixel rather than per
    // cell. That is safe because the noise is far coarser than a star: every
    // pixel of a given star reads essentially the same value, so they cannot
    // disagree about whether it exists and flicker along its edge.
    float region = smoothstep(0.30, 0.78, vnoise(dir * 2.3));
    float density = starDensity * mix(0.20, 1.0, region);

    // The sky everyone recognises: a few bright ones, a field behind them, and
    // a haze of faint ones.
    col += starLayer(dir, 60.0, 0.026 * density, 0.85, time);
    col += starLayer(dir, 110.0, 0.046 * density, 0.30, time);
    col += starLayer(dir, 200.0, 0.060 * density, 0.10, time);

    col *= nightFactor * horizonFade;

    outColor = vec4(col, 1.0);
}
