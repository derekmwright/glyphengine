#version 450

layout(location = 0) in vec2 fragUV;

layout(push_constant) uniform PushConstants {
    mat4 invVP;    // inverse view-projection
    mat4 model;    // [0..2] = camera position (reuses model slot)
    vec4 tint;     // x = time, y = nightFactor, z = milky way strength
    vec4 sunDir;
    vec4 sunColor;
    vec4 pointPos;
    vec4 pointColor;
    vec4 ambient;
} pc;

layout(location = 0) out vec4 outColor;

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
    float spine = exp(-lat * lat * 120.0);
    float halo = exp(-lat * lat * 20.0);
    float band = spine * 0.80 + halo * 0.20;

    vec3 col = vec3(0.0);

    // structure is the band's shape: bright star-cloud regions, cut by dust.
    // It drives star density as well as the underglow, because the thing that
    // stops this reading as a cloud is that it is made of stars. A smooth
    // luminous mass with soft edges is a cloud, whatever colour it is and
    // whatever shape it is cut into -- the first four attempts here all looked
    // like weather. Unresolved grain is the cue that says star field.
    float structure = 0.0;
    float core = 0.0;
    vec3 mwTint = vec3(0.7, 0.75, 0.9);

    // The band is a quarter of the sky, so the noise is worth branching around
    // rather than paying for everywhere. The branch is coherent -- neighbouring
    // pixels are on the same side of it almost everywhere.
    if (milkyWay > 0.0 && band > 0.004) {
        // Stretched along the plane, not isotropic. Round blobs of noise are
        // what clouds look like; sampling with the across-band axis at several
        // times the in-band frequency elongates the structure into streaks that
        // follow the band, which is what the real thing does.
        vec3 inPlane = dir - GALACTIC_POLE * lat;
        vec3 q = inPlane * 9.0 + GALACTIC_POLE * lat * 22.0;

        // The dust is sampled with its own, stronger stretch rather than a
        // scaled copy of the first: reusing one warp for both makes every lane
        // parallel, which combs the band into brush strokes.
        vec3 r = inPlane * 13.0 + GALACTIC_POLE * lat * 44.0;

        float clouds = fbm(q);
        float rift = fbm(r + vec3(17.0, 4.0, 9.0));
        float mottle = vnoise(dir * 55.0);

        // Brightness along the band, peaking toward the galactic centre. Flat
        // along its length is the other half of why a band reads as a smear.
        vec3 planeDir = normalize(inPlane);
        float along = dot(planeDir, GALACTIC_CENTER);
        core = smoothstep(-0.15, 0.95, along);
        float lengthwise = 0.25 + 0.75 * core;

        // Dust is opaque and nearly black: it takes the stars with it, not just
        // the glow. Soft grey shading reads as cloud shadow; this reads as dust.
        float dust = 1.0 - 0.95 * smoothstep(0.34, 0.60, rift);
        structure = band * lengthwise * (0.25 + 0.75 * clouds) * (0.75 + 0.35 * mottle) * dust;

        // Warm at the core, cool toward the edges and the far end -- the
        // gradient runs along the band rather than being random patches, which
        // is what the reference actually shows. A little violet drift on top so
        // it is not a single ramp.
        float hue = vnoise(q * 0.30 + vec3(31.0, 7.0, 19.0));
        vec3 amber = vec3(1.00, 0.80, 0.52);
        vec3 pale = vec3(0.72, 0.76, 0.92);
        vec3 violet = vec3(0.52, 0.42, 0.86);
        mwTint = mix(pale, amber, core * (0.55 + 0.45 * spine));
        mwTint = mix(mwTint, violet, 0.30 * smoothstep(0.62, 0.85, hue) * (1.0 - core));

        // A faint underglow only -- the light from stars too small to resolve.
        // Most of the band's brightness comes from the grain below.
        col += mwTint * structure * 0.05 * milkyWay;
    }

    float inBand = structure * milkyWay;

    // The sky everyone recognises: a few bright ones, a field behind them, and
    // a haze of faint ones that thickens into the band.
    col += starLayer(dir, 60.0, 0.10, 0.85, time);
    col += starLayer(dir, 110.0, 0.22 + 0.45 * inBand, 0.30, time);
    col += starLayer(dir, 200.0, 0.30 + 0.60 * inBand, 0.10, time);

    // Two more layers that exist only inside the band, dense and faint enough
    // to merge into grain rather than resolve as points. This is the galaxy.
    col += mwTint * starLayer(dir, 150.0, 0.90 * inBand, 0.22, time);
    col += mwTint * starLayer(dir, 240.0, 0.95 * inBand, 0.13, time);

    col *= nightFactor * horizonFade;

    outColor = vec4(col, 1.0);
}
