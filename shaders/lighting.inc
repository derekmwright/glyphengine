// Shared lighting functions for lit fragment shaders.
// Expects shadowMap (sampler2DArrayShadow), pointShadowMap, pc, and
// shadow (the ShadowData UBO: mat4 cascadeVP[2], vec4 nightGrade, vec4
// skyPalette[6]) to be declared by the including file, and LIGHT_SET #defined
// before this include -- see lights.inc for lb/cg/li (the clustered light
// buffers).
//
// Light contributions use the "irradiance" pattern (same as three.js):
// irradiance = saturate(NdotL) * lightColor, then both diffuse and specular
// are multiplied by irradiance. This structurally prevents specular from
// leaking through surfaces where NdotL <= 0.

#include "atmosphere.inc"
#include "lights.inc"
#include "volumetric.inc"

const int NumCascades = 2;

// calcShadow selects the first cascade whose frustum contains the (normal-
// offset) world position, then applies 5x5 PCF on that cascade's layer.
float calcShadow(vec3 shadowPos, float NdotL) {
    for (int c = 0; c < NumCascades; c++) {
        vec4 lsPos = shadow.cascadeVP[c] * vec4(shadowPos, 1.0);
        vec3 proj = lsPos.xyz; // orthographic: w == 1
        proj.xy = proj.xy * 0.5 + 0.5;

        // Small margin so PCF taps near the edge fall through to the next
        // cascade instead of clamping.
        if (proj.x < 0.01 || proj.x > 0.99 || proj.y < 0.01 || proj.y > 0.99 || proj.z < 0.0 || proj.z > 1.0) {
            continue;
        }

        // Normal-based bias: surfaces at grazing angles need much more bias.
        // The far cascade covers more world units per texel, so scale up.
        float biasScale = (c == 0) ? 1.0 : 4.0;
        float bias = max(0.008 * (1.0 - NdotL), 0.001) * biasScale;
        float ref = proj.z - bias;

        // Four-tap PCF on a comparison sampler.
        //
        // shadowMap is a sampler2DArrayShadow, so every tap is already a hardware
        // 2x2 percentage-closer fetch with bilinear weights -- four taps spaced a
        // texel apart therefore cover a smooth 4x4 neighbourhood, not four points.
        // The 5x5 loop this replaces did 25 of those, paying for a 6x6 footprint
        // and about a hundred depth comparisons per fragment.
        //
        // That cost lands on every lit fragment, in grass, terrain, water and the
        // lit path alike, and grass overdraws itself heavily. Measured with the
        // per-pass GPU timer on 08-grass: grass 4.99 -> 3.97 ms, opaque 0.063 ->
        // 0.030 ms, whole frame 6.89 -> 5.92 ms. The image difference against the
        // 5x5 version is RMS 0.0008 luminance against a 0.00003 noise floor --
        // measurable, and indistinguishable by eye on hard-edged pillar shadows,
        // which is where a weaker kernel would show first.
        //
        // Offsets are fixed rather than rotated per pixel. A rotated kernel trades
        // banding for noise, and noise on sub-pixel grass blades is exactly the
        // "TV static" the grass shader's normal handling already exists to avoid.
        vec2 texelSize = 1.0 / vec2(textureSize(shadowMap, 0).xy);
        float result =
            texture(shadowMap, vec4(proj.xy + vec2(-1.0, -1.0) * texelSize, float(c), ref)) +
            texture(shadowMap, vec4(proj.xy + vec2( 1.0, -1.0) * texelSize, float(c), ref)) +
            texture(shadowMap, vec4(proj.xy + vec2(-1.0,  1.0) * texelSize, float(c), ref)) +
            texture(shadowMap, vec4(proj.xy + vec2( 1.0,  1.0) * texelSize, float(c), ref));
        return mix(0.35, 1.0, result * 0.25);
    }
    return 1.0; // outside all cascades = fully lit
}

float calcPointShadow(vec3 fragPos, vec3 lightPos, float range) {
    vec3 lightToFrag = fragPos - lightPos;
    float faceDepth = max(abs(lightToFrag.x), max(abs(lightToFrag.y), abs(lightToFrag.z)));
    float storedDepth = texture(pointShadowMap, lightToFrag).r;
    float near = 0.1;
    float closestZ = near * range / (range - storedDepth * (range - near));
    float bias = 0.03;
    return faceDepth - bias > closestZ ? 0.35 : 1.0;
}

// fogColor returns the same horizon colour sky.frag draws, so fogged geometry
// melts into the sky rather than into a colour that merely resembles it.
//
// It shares the model rather than reimplementing it. The two used to be
// separate copies of the same constants, kept in step by a comment.
vec3 fogColor(vec3 viewDir) {
    // The scattering has to come from the real sun, not from pc.sunDir -- that
    // is the moon after dusk, and glowing warm around it puts a sunset on the
    // wrong side of the sky at the wrong time of night.
    float sunY = pc.sunColor.w;
    vec3 sunDir = atmSunDirFrom(sunY, pc.fog.zw);
    vec3 zenith, horizon;
    // shadow.skyPalette, not a copy of it: the dome reads the same six vec4s
    // out of the same buffer, so there is no second source for these to drift
    // from. That is the whole point of sharing this file.
    atmSkyPalette(sunY, shadow.skyPalette, zenith, horizon);
    return horizon + atmSunGlow(viewDir, sunDir, pc.sunColor.rgb, sunY);
}

// Rec. 709 luminance, the same weights atmNightShift builds its grey from.
float lightLum(vec3 c) {
    return dot(c, vec3(0.2126, 0.7152, 0.0722));
}

// How much light this fragment sends to the eye, split by where it came from:
// lamps -- the clustered point and spot lights plus the shadowed point light
// in the push constants -- against the moon or sun, and ambient. Luminance
// rather than colour, because the only thing read off them is a ratio.
//
// File scope rather than another parameter because the producers are the two
// evalLighting* functions below and the consumer is applyFog, and all seven
// fragment shaders that include this file call one of the first and then the
// second, in that order, with nothing in between. Threading it through would
// mean changing every one of those call sites to carry a value none of them
// has an opinion about.
//
// The zero initialiser is load-bearing rather than tidiness, and it is what
// water.frag builds on. That shader includes this file for calcShadow,
// fogColor and applyFog but shades its own surface, so neither producer below
// ever runs there; it sets the two by hand from its own finished terms
// instead. Where it has no lamp reaching it, its local sum is exactly +0.0 and
// applyFog's local share is exactly 0.0, which is what keeps a lake with no
// lights near it byte-identical to before water was lit at all.
float lightLocalLum = 0.0;
float lightSkyLum = 0.0;

// Set when this fragment's colour is the debug heatmap rather than a surface,
// so applyFog hands it back untouched.
//
// A froxel occupancy readout is a number drawn in false colour, not something
// the air is between the eye and, and it was being fogged and blue-shifted
// like anything else: at midnight the ramp came back a uniform indigo with
// the red end indistinguishable from the blue end, which is the one time the
// view is worth opening. Neither producer below sets this in normal shading,
// so nothing else changes. water.frag calls neither, and returns the heatmap
// before it reaches applyFog rather than setting this and being handed its
// colour back.
bool lightDebugReadout = false;

// nightLocalShare is the fraction of the light leaving this fragment toward
// the eye that came from a lamp. atmNightShift backs its scotopic blend off by
// exactly this much.
//
// A ratio rather than a threshold on absolute luminance, which would read more
// literally as "is this surface bright enough for cones to work". Two reasons,
// one of them measured. The engine's radiometric units are arbitrary, so the
// two thresholds would be numbers with nothing behind them; and a threshold
// catches every bright fragment whatever lit it. Tried: smoothstep(0.03, 0.15)
// on the shaded luminance, a pair chosen to spare this scene's doorway pool.
// It also moved 41.5% of 08-grass at midnight by up to 47/255, 18.3% of
// 16-materials at midnight and 18.2% at dusk by up to 18/255, and 4.7% of
// 15-kitchen-sink at dusk -- scenes with no lamp in them at all. A ratio is
// exactly zero when nothing local contributed, so those scenes cannot move,
// and "cannot" is checkable: every example in the `screenshots` target except
// 11-lights and 21-streetlights renders byte-identical across this change.
//
// The fog belongs inside the ratio, not outside it. What fog mixes in is
// scattered skylight, which is nobody's lamp, so a pool far enough away to
// have faded into the haze must not keep punching a warm hole in it -- at
// f == 1 the local share is 0 and the fogged fragment takes the same full
// shift the sky does, which is the only answer that lets geometry keep melting
// into the horizon.
float nightLocalShare(float fogAmount, vec3 fogCol) {
    float through = 1.0 - fogAmount;
    float local = through * lightLocalLum;
    float rest = through * lightSkyLum + fogAmount * lightLum(fogCol);
    // The epsilon exists only so an entirely black fragment does not divide
    // zero by zero. It is orders below anything the shift could make visible.
    return local / (local + rest + 1e-6);
}

// applyFog blends toward the horizon colour, then applies the night shift.
//
// Two modes, chosen by pc.fog.x:
//
// Uniform (fog.x == 0) is the original: density is the same at every altitude,
// so the only thing that thickens fog is distance. It is cheap and it is what
// every existing scene was tuned against, so it stays the default.
//
// Height fog (fog.x > 0) makes density fall off exponentially with altitude,
// which is what real fog does — it settles. That gives a valley floor thicker
// air than the ridge above it, and a mountain that emerges from haze as it
// rises, neither of which a distance-only fog can express no matter how it is
// tuned.
//
// The integral along the view ray has a closed form, so this costs two
// exponentials rather than a raymarch:
//
//	density(y) = D * exp(-(y - y0) / H)
//	tau        = integral of density along the ray
//	           = D*H*L/(dy) * (exp(-(Py-y0)/H) - exp(-(Qy-y0)/H))
//
// where L is the ray length and dy its vertical extent. As dy approaches zero
// that expression is 0/0, and a horizontal ray is not an edge case — it is
// where the camera spends most of its time — so it falls back to the constant
// density at eye height.
//
// The night shift lives here because every lit shader calls applyFog exactly
// once as its last step, so this is the one place that cannot miss a surface.
//
// Density rides in pc.cameraPos.w; zero disables fog.
vec3 applyFog(vec3 color, vec3 worldPos) {
    if (lightDebugReadout) {
        return color;
    }

    float density = pc.cameraPos.w;
    if (density <= 0.0) {
        // No medium, so no beams either -- volInscatter would return zero
        // from its own density test and this saves it the call. See
        // volumetric.inc: a lamp in clean air lights what it hits and nothing
        // else, which is the answer rather than a gap.
        return atmNightShift(color, pc.sunColor.w, nightLocalShare(0.0, vec3(0.0)), shadow.nightGrade);
    }

    vec3 toFrag = worldPos - pc.cameraPos.xyz;
    float dist = length(toFrag);
    vec3 viewDir = toFrag / max(dist, 0.001);

    float heightFalloff = pc.fog.x;

    // How much of the nominal density this ray actually passes through. One
    // for uniform fog; less when the ray spends its length above the base
    // height, more when it runs along the bottom.
    float amount = 1.0;
    if (heightFalloff > 0.0) {
        float invH = 1.0 / heightFalloff;
        // volFogHeightExponent is what this raises e to, and it is the same
        // function volInscatter samples the medium with a point at a time --
        // so the air a lamp lights is provably the air that hazes the hills,
        // rather than two expressions a comment claims are equal. It returns
        // the NEGATED height ratio, which is the form both uses want:
        // -camRel and -fragRel below, and dy is their difference either way
        // round. Rewriting it this way is exact, not merely equivalent --
        // (-a)*b is -(a*b) and (-a)-(-b) is b-a to the bit -- and the twelve
        // G1 captures, which include two height-fog scenes, are byte
        // identical across the change.
        float hc = volFogHeightExponent(pc.cameraPos.y, invH);
        float hf = volFogHeightExponent(worldPos.y, invH);
        float dy = hc - hf;

        if (abs(dy) < 1e-4) {
            amount = exp(hc);
        } else {
            amount = (exp(hc) - exp(hf)) / dy;
        }
        amount = max(amount, 0.0);
    }

    // Both modes share the same exp-squared curve, so Density means the same
    // thing either way and Height only redistributes fog vertically. Making
    // the height mode a plain Beer term instead -- which is what the integral
    // naturally gives -- is linear in distance where the other is quadratic,
    // and turning Height on then thickens every scene at the same Density.
    float d = dist * density * amount;
    float f = clamp(1.0 - exp(-d * d), 0.0, 1.0);

    vec3 fogCol = fogColor(viewDir);

    // In-scattering from the local lights, along this same ray, up to this
    // same fragment. It goes here because applyFog is the one call every lit
    // shader makes exactly once as its last step -- the same property that
    // made this the right home for the night shift -- so no surface can be
    // missed, and every surface integrates to its own depth for free.
    //
    // ADDED to the fogged colour rather than mixed into it. What `mix` above
    // does is replace the surface with the haze in front of it; what a beam
    // does is put light INTO that haze, which is an emission along the ray
    // and not a different surface colour. Mixing it would also make a beam
    // dimmer the clearer the air got, which is backwards.
    //
    // 1.0/gl_FragCoord.w is the positive view depth (see evalLightingAO), so
    // dividing it by the distance gives the cosine between this ray and the
    // camera axis -- what the march needs to turn metres along the ray into
    // the depth the froxel grid slices on.
    vec3 scatter = volInscatter(gl_FragCoord.xy, viewDir, dist, (1.0 / gl_FragCoord.w) / max(dist, 0.001));

    // It is lamp light, so it counts as lamp light for the night grade: a
    // warm beam must no more be shifted to scotopic blue than the warm pool
    // it lands in, and nightLocalShare is how applyFog is told that. Adding
    // it here rather than inside the two evalLighting* functions is what
    // covers water.frag too, which shades its own surface and sets these by
    // hand.
    //
    // Exactly +0.0 when nothing scatters, and `x + 0.0` is `x` for every
    // value either of these can hold -- which is the whole of why a scene
    // with no volumetric light renders byte-identically rather than
    // approximately so.
    lightLocalLum += lightLum(scatter);

    return atmNightShift(mix(color, fogCol, f) + scatter, pc.sunColor.w, nightLocalShare(f, fogCol), shadow.nightGrade);
}

// Blinn-Phong specular BRDF (returns unscaled specular color).
vec3 brdfSpecular(vec3 L, vec3 V, vec3 N, vec3 F0, float shininess) {
    vec3 H = normalize(L + V);
    float NdotH = max(dot(N, H), 0.0);
    float VdotH = max(dot(V, H), 0.0);
    float D = pow(NdotH, shininess);
    vec3 F = F0 + (1.0 - F0) * pow(1.0 - VdotH, 5.0);
    return F * D;
}

// evalLightingAO is evalLighting with an ambient occlusion factor.
//
// ao scales the ambient term and nothing else. Baked occlusion describes how
// much of the sky a crevice can see, so it has no business dimming a light that
// can actually reach the surface -- applying it to direct light is what makes
// AO-mapped geometry read as dirty rather than as shaped.
vec3 evalLightingAO(vec3 diffuseColor, vec3 F0, float shininess,
                    vec3 N, vec3 V,
                    vec3 worldPos, vec3 shadowPos, float ao) {
    // Reverse-Z (near maps to 1, far to 0; see reverseZProjection in app.go)
    // uses a projection whose w-row leaves clip.w equal to the positive
    // view-space depth, so after the perspective divide gl_FragCoord.w is
    // 1/viewDepth -- inverting it here recovers viewDepth without needing
    // near or far. Both the cluster lookup and the debug heatmap key off
    // this.
    float viewDepth = 1.0 / gl_FragCoord.w;

    // Debug heatmap replaces the lit colour outright; it reports the grid
    // regardless of the brute-force bit, since the point of the view is to
    // see what binning produced.
    if ((lb.flags.x & 2u) != 0u) {
        lightDebugReadout = true;
        return lightHeatmap(cellLightCount(gl_FragCoord.xy, viewDepth));
    }

    // --- Directional (sun) light ---
    vec3 L = normalize(pc.sunDir.xyz);
    float NdotL = clamp(dot(N, L), 0.0, 1.0);
    float sunShadow = calcShadow(shadowPos, NdotL);
    vec3 irradiance = NdotL * pc.sunColor.rgb * sunShadow;

    vec3 sunDiffuse  = irradiance * diffuseColor;
    vec3 sunSpecular = irradiance * brdfSpecular(L, V, N, F0, shininess);

    // --- Shadowed point light ---
    vec3 toLight = pc.pointPos.xyz - worldPos;
    float dist = length(toLight);
    float range = pc.pointPos.w;
    vec3 pointDiffuse = vec3(0.0);
    vec3 pointSpecular = vec3(0.0);
    if (range > 0.0 && dist < range) {
        float ratio = dist / range;
        float atten = (1.0 - ratio * ratio);
        atten = atten * atten;
        vec3 PL = normalize(toLight);
        float NdotPL = clamp(dot(N, PL), 0.0, 1.0);
        float ptShadow = calcPointShadow(worldPos, pc.pointPos.xyz, range);
        vec3 ptIrradiance = NdotPL * atten * pc.pointColor.rgb * ptShadow;

        pointDiffuse  = ptIrradiance * diffuseColor;
        pointSpecular = ptIrradiance * brdfSpecular(PL, V, N, F0, shininess);
    }

    // --- Unshadowed lights (point + spot) ---
    vec3 uDiffuse = vec3(0.0);
    vec3 uSpecular = vec3(0.0);
    LightRange lr = resolveLightRange(gl_FragCoord.xy, viewDepth);
    for (uint i = 0u; i < lr.count; i++) {
        GpuLight light = lightAt(lr, i);
        vec3 uL;
        vec3 irradiance = lightIrradiance(light, worldPos, N, uL);
        // Out of range, facing away, or outside the cone: this light adds
        // exactly +0.0 to both sums, and x + 0.0 is x for every value either
        // sum can hold. So skipping is bit-for-bit what going round was --
        // which is what lets clustered and brute force be required to match
        // exactly -- and it saves brdfSpecular's two pow() calls on every
        // light that contributes nothing. The old inline loop only paid them
        // inside its range test; hoisting the loop out of seven shaders lost
        // that, in both modes.
        //
        // Worth having, measured rather than assumed: the opaque pass at
        // 1920x1080, mean of 200 frames, with 11-lights' floor of 400 lamps
        // and 32 spots, goes 0.557 ms without this to 0.443 with it -- and
        // four separate runs of the version that has it spread only 0.427 to
        // 0.450, so the gap is not this machine's drift. The 1024-light ring
        // goes 1.739 to 1.510.
        if (irradiance == vec3(0.0)) {
            continue;
        }
        uDiffuse  += irradiance * diffuseColor;
        uSpecular += irradiance * brdfSpecular(uL, V, N, F0, shininess);
    }

    vec3 ambientTerm = diffuseColor * pc.ambient.rgb * ao;

    // What applyFog needs to decide how much of the night shift this fragment
    // has earned. Summed from the finished terms rather than accumulated
    // inside the loop, which costs two dot products instead of one per light
    // and, more to the point, leaves the returned expression below untouched
    // -- so no scene's pixels can move through a regrouped float add, and
    // clustered and brute force cannot disagree about a summation order they
    // never take. The lamp's specular is lamplight too: it is the lamp's
    // colour reflected off the surface, so it belongs on the same side as its
    // diffuse.
    lightLocalLum = lightLum(pointDiffuse + pointSpecular + uDiffuse + uSpecular);
    lightSkyLum = lightLum(ambientTerm + sunDiffuse + sunSpecular);

    return ambientTerm + sunDiffuse + sunSpecular + pointDiffuse + pointSpecular + uDiffuse + uSpecular;
}

// Evaluate all light sources and return final lit color.
// shadowPos is the normal-offset world position used for shadow lookups.
//
// Unoccluded; the material path calls evalLightingAO directly with a sampled
// occlusion factor. Named separately rather than overloaded to match how
// evalLightingDiffuse below already distinguishes itself.
vec3 evalLighting(vec3 diffuseColor, vec3 F0, float shininess,
                  vec3 N, vec3 V,
                  vec3 worldPos, vec3 shadowPos) {
    return evalLightingAO(diffuseColor, F0, shininess, N, V, worldPos, shadowPos, 1.0);
}

// evalLightingDiffuse evaluates all light sources diffuse-only — no specular,
// no Fresnel. Used by grass: on sub-pixel blades the Fresnel term explodes at
// grazing sun angles (dawn/dusk) and turns normal noise into bright speckle.
vec3 evalLightingDiffuse(vec3 diffuseColor, vec3 N, vec3 worldPos, vec3 shadowPos) {
    float viewDepth = 1.0 / gl_FragCoord.w;
    if ((lb.flags.x & 2u) != 0u) {
        lightDebugReadout = true;
        return lightHeatmap(cellLightCount(gl_FragCoord.xy, viewDepth));
    }

    // --- Directional (sun) light ---
    vec3 L = normalize(pc.sunDir.xyz);
    float NdotL = clamp(dot(N, L), 0.0, 1.0);
    float sunShadow = calcShadow(shadowPos, NdotL);
    vec3 sunTerm = NdotL * pc.sunColor.rgb * sunShadow * diffuseColor;
    vec3 color = sunTerm;

    // The lamp half of the same sum, accumulated alongside rather than teased
    // back out of `color` afterwards. A second running total leaves every add
    // that builds `color` exactly where it was, which is what keeps a scene
    // with no lamps byte-identical, and it adds the same +0.0 for a light that
    // does not reach this fragment that `color` does -- so the clustered and
    // brute-force paths stay bit-for-bit equal here too.
    vec3 localColor = vec3(0.0);

    // --- Shadowed point light ---
    vec3 toLight = pc.pointPos.xyz - worldPos;
    float dist = length(toLight);
    float range = pc.pointPos.w;
    if (range > 0.0 && dist < range) {
        float ratio = dist / range;
        float atten = (1.0 - ratio * ratio);
        atten = atten * atten;
        float NdotPL = clamp(dot(N, normalize(toLight)), 0.0, 1.0);
        float ptShadow = calcPointShadow(worldPos, pc.pointPos.xyz, range);
        vec3 ptTerm = NdotPL * atten * pc.pointColor.rgb * ptShadow * diffuseColor;
        color += ptTerm;
        localColor += ptTerm;
    }

    // --- Unshadowed lights (point + spot) ---
    LightRange lr = resolveLightRange(gl_FragCoord.xy, viewDepth);
    for (uint i = 0u; i < lr.count; i++) {
        GpuLight light = lightAt(lr, i);
        vec3 uL;
        vec3 term = lightIrradiance(light, worldPos, N, uL) * diffuseColor;
        color += term;
        localColor += term;
    }

    vec3 ambientTerm = diffuseColor * pc.ambient.rgb;
    lightLocalLum = lightLum(localColor);
    lightSkyLum = lightLum(ambientTerm + sunTerm);

    return ambientTerm + color;
}
