// In-scattering from the clustered local lights, integrated along the view
// ray: the visible beam of a spot light and the glow around a point light.
//
// A beam is not an object. It is the medium you are standing in being lit, so
// this is not a cone mesh or a screen-space smear -- it is the same light
// evaluation lights.inc already does for a surface, done at points in the AIR
// between the eye and whatever the pixel landed on, and summed.
//
// Two screen-space attempts were measured and rejected before this one; the
// numbers are in issue #47. Radiating a god-ray pass from a lamp instead of
// the sun put MORE light where no beam could be (+32.5 straight up, +34.0 to
// the side) than in the cone's own path (+21.3), because that technique
// smears pixels that are already bright and has no idea which way a light
// points. What this does instead costs nothing to bound, because the
// expensive half already exists: the clustered binner builds a 16x9x24 froxel
// grid with a light list per cell every frame, so marching the ray means
// walking the cells it crosses and reading lists that are already there.
//
// The includer must have declared, at these names: `pc` with cameraPos
// (xyz eye, w fog density) and fog (x height falloff, y base height), the
// `shadow` UBO with `volumetric` (x anisotropy, y step count), and
// lights.inc's lb/cg/li behind LIGHT_SET. lighting.inc includes this for the
// seven lit shaders; sky.frag includes it directly, because the sky is not a
// lit surface but is the thing a beam aimed upward has to show against.

// volStartJitter offsets where each pixel's march starts, so the sample
// positions do not line up across neighbouring pixels and produce rings.
//
// It is interleaved gradient noise of the pixel coordinate and NOTHING else.
// The same function, for the same reason, as godray.frag's startJitter:
// anything animated would shimmer (there is no temporal filter here to
// average it out) and would break renders under GLYPHENGINE_FIXED_FRAME_TIME,
// which have to repeat byte for byte. It is duplicated rather than shared
// because godray.frag is a fullscreen post pass that includes none of this
// and has no light buffer to declare.
float volStartJitter(vec2 px) {
    return fract(52.9829189 * fract(dot(px, vec2(0.06711056, 0.00583715))));
}

// volFogHeightExponent is the height profile applyFog integrates, evaluated at
// one altitude, as the exponent rather than the exponential.
//
// It exists so the medium a lamp lights and the medium that hazes the hills
// cannot drift apart: applyFog's closed-form integral and this march's
// point sample are the same density(y) = D * exp(-(y - y0)/H), and this is
// the -(y - y0)/H both of them raise e to. invH is passed in rather than
// derived because applyFog computes it once for a whole fragment, and
// `(y - y0) * (1/H)` and `(y - y0) / H` are not the same float -- sharing the
// expression only means something if it is the same arithmetic.
float volFogHeightExponent(float y, float invH) {
    return -(y - pc.fog.y) * invH;
}

// volPhase is the Henyey-Greenstein phase function, normalised so that g == 0
// returns exactly 1.0.
//
// The usual 1/(4*pi) is deliberately NOT here. This engine's radiometric units
// are arbitrary -- a light's intensity rides in its colour, the fog is an
// exp-squared curve rather than a Beer term -- so a physically normalised
// phase would just make every number a game types 12.6 times larger. Dropping
// it makes the isotropic case exactly 1, which is what lets
// SpotLight.Volumetric read as "the scattering strength relative to
// isotropic" rather than as an arbitrary scale.
//
// cosTheta is the cosine between the direction light TRAVELS (from the lamp
// to the sample point) and the direction it leaves in (from the sample point
// to the eye). Both negate to dot(rayDir, L), so forward scattering -- g > 0,
// the peak at cosTheta == 1 -- is what you see looking INTO a beam, which is
// where a real beam is brightest.
float volPhase(float cosTheta, float g) {
    float g2 = g * g;
    float d = 1.0 + g2 - 2.0 * g * cosTheta;
    // d is at least (1-|g|)^2, and packLitUBO clamps |g| to 0.99, so the
    // smallest d this can see is 1e-4 and the largest value is ~1e6 -- finite,
    // and reached only exactly down the axis of a beam pointed at the eye.
    return (1.0 - g2) / (d * sqrt(d));
}

// volGridFar is the view depth the froxel grid's last slice ends at, in
// metres, recovered from the two numbers the binner already sends.
//
// slice = log(d)*scale + bias, and the binner builds scale = gridZ/log(far/
// start) and bias = -scale*log(start), so (gridZ - bias)/scale is log(far)
// exactly. Recovering it beats sending the far plane again: a second copy is
// a second thing to forget to update when a camera changes it, and this one
// cannot disagree with the grid it is describing.
float volGridFar() {
    return exp((float(lb.grid.z) - lb.zParams.y) / lb.zParams.x);
}

// volInscatter integrates in-scattering from the eye along a view ray.
//
//	px           gl_FragCoord.xy -- the cell column and the jitter seed
//	rayDir       unit world direction from the eye toward the fragment
//	maxDist      how far along it to integrate, in metres
//	depthPerDist view depth per metre along the ray, i.e. the cosine between
//	             the ray and the camera's axis. A lit fragment has it for
//	             free as (1/gl_FragCoord.w)/dist; the sky computes it.
//
// Returns radiance to ADD to the fragment, or exactly vec3(0.0) when there is
// nothing to do -- which is what keeps every scene that asks for no
// volumetrics byte-identical: x + 0.0 is x.
//
// Where the steps go is the whole design. They are spaced geometrically in
// view depth, which is the froxel grid's own metric: the grid's slices are
// logarithmic (a factor of 1.29 each, 24 of them from 1 m to 500 m), so equal
// steps in log(depth) means equal steps in slice index and every cell the ray
// crosses gets the same number of samples. Stepping linearly in metres
// instead gives the far slices twenty samples each and the near ones a
// fraction of one, which is backwards: the near cells are the ones a lamp is
// usually in.
//
// The first segment is the exception and reaches all the way back to the eye.
// A geometric series cannot start at zero, and the span it cannot cover is
// exactly the span the binner folds into slice 0 -- everything nearer than
// the grid's slice start is one cell there too. So segment 0 covers [0, near]
// with one sample, for the same reason and with the same consequence, and the
// ray is covered end to end with no gap for energy to leak through.
vec3 volInscatter(vec2 px, vec3 rayDir, float maxDist, float depthPerDist) {
    // Three uniform early-outs, in the order that costs least to decide.
    //
    // The flag bit is the one that matters: it says no light uploaded this
    // frame asked to scatter, so every existing scene leaves here having
    // executed one branch on a value that is the same for the whole draw.
    // Without it the cheapest honest answer would be to walk each cell's
    // light list once per step looking for a non-zero intensity, which is
    // precisely the work this exists to avoid.
    if ((lb.flags.x & 4u) == 0u) {
        return vec3(0.0);
    }
    int steps = int(shadow.volumetric.y);
    if (steps <= 0 || !(maxDist > 0.0)) {
        return vec3(0.0);
    }
    // No fog is no medium, and no medium is no beam. This is not a
    // degenerate case to guard against -- it is the answer. A lamp in a
    // vacuum lights what it hits and nothing else, and a game that wants a
    // shaft in clean air is asking for fog it has not admitted to.
    float density = pc.cameraPos.w;
    if (density <= 0.0) {
        return vec3(0.0);
    }

    float g = shadow.volumetric.x;
    float heightFalloff = pc.fog.x;
    float invH = heightFalloff > 0.0 ? 1.0 / heightFalloff : 0.0;

    // Where the geometric part of the march starts. The grid's slice start is
    // the natural choice -- below it every depth is slice 0 -- and it is
    // recovered from zParams rather than assumed, for the reason volGridFar
    // gives. The second term handles a fragment close to the eye: without it
    // a wall half a metre away would put its whole ray inside segment 0 and
    // spend the other fifteen samples on zero-length segments.
    float sliceStart = exp(-lb.zParams.y / lb.zParams.x);
    float nearDist = min(sliceStart / max(depthPerDist, 1e-4), maxDist / float(steps));

    float ratio = steps > 1 ? pow(maxDist / max(nearDist, 1e-6), 1.0 / float(steps - 1)) : 1.0;
    // The jittered position inside a geometric segment is a fixed fraction of
    // the way along it in log space, and every segment has the same log
    // length -- so this is one pow() for the whole march rather than one per
    // step.
    float jitter = volStartJitter(px);
    float jstep = pow(ratio, jitter);

    vec3 sum = vec3(0.0);
    float t0 = 0.0;
    float edge = nearDist;

    for (int s = 0; s < steps; s++) {
        // The last segment ends exactly at the fragment rather than at
        // nearDist*ratio^(steps-1), which is the same number only up to the
        // rounding of steps-1 multiplications. Ending short leaves a sliver
        // of unlit air at every surface; ending long puts samples behind it.
        float t1 = (s == steps - 1) ? maxDist : edge;
        float segLen = t1 - t0;
        if (segLen > 0.0) {
            // Segment 0 has no geometric interior -- it starts at the eye --
            // so its sample is placed linearly. Every other segment's is
            // placed in log space, which is where its neighbours are.
            float t = (s == 0) ? t0 + segLen * jitter : t0 * jstep;

            vec3 p = pc.cameraPos.xyz + rayDir * t;

            // The medium at this point: the same profile applyFog integrates
            // in closed form over the whole ray. One is the point sample of
            // the other, which is what makes the air a lamp lights and the
            // air that hazes the hills the same air.
            float dens = density;
            if (heightFalloff > 0.0) {
                dens *= exp(volFogHeightExponent(p.y, invH));
            }

            // The cell this sample is in, from the SAME mapping a lit
            // fragment uses -- clusterIndex, via resolveLightRange, on this
            // pixel's column and this sample's view depth. Re-deriving the
            // froxel here would be a second copy of that arithmetic, and a
            // copy that rounded a slice differently would walk one cell's
            // list while standing in another's, which is a light list that is
            // wrong in a way nothing would report.
            //
            // The view depth is t*depthPerDist and not length(p - eye),
            // because view depth is what the grid slices on: it is an affine
            // function of position along the ray, zero at the eye, so scaling
            // the ray parameter is exact rather than approximate.
            LightRange lr = resolveLightRange(px, t * depthPerDist);

            vec3 cell = vec3(0.0);
            for (uint i = 0u; i < lr.count; i++) {
                GpuLight light = lightAt(lr, i);
                float vol = light.params.x;
                if (vol <= 0.0) {
                    continue;
                }
                vec3 toLight = light.posRange.xyz - p;
                float dist = length(toLight);
                float range = light.posRange.w;
                if (range <= 0.0 || dist >= range) {
                    continue;
                }
                vec3 L = normalize(toLight);
                float spot = lightSpotFactor(light, L);
                if (spot <= 0.0) {
                    continue;
                }
                // The same falloff lightIrradiance uses, minus NdotL: there
                // is no surface here and no normal to take it against. That
                // is the only difference between what the air gets and what
                // the ground under it gets, which is why a beam and the pool
                // it lands in cannot disagree about where the cone is.
                float r = dist / range;
                float atten = (1.0 - r * r);
                atten = atten * atten;
                cell += light.color.rgb * (atten * spot * vol * volPhase(dot(rayDir, L), g));
            }
            sum += cell * (dens * segLen);
        }
        t0 = t1;
        edge *= ratio;
    }

    return sum;
}
