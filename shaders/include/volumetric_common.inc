// The leaf half of the volumetric march: the helpers that are pure arithmetic
// on their arguments and bind to nothing.
//
// They are here rather than in volumetric.inc so a scattering pass outside the
// engine can call them. volumetric.inc cannot be included without first
// declaring the clustered light buffers, the shadow UBO and a push block with
// cameraPos and fog -- a light set a water or atmosphere pass never reads -- and
// before this split the only way to reach these two was to copy them, which is
// the vendored-copy failure shaders/include/include.go exists to remove. The
// boundary is the point: anything added here must compile with no bindings
// declared at all, and x/internal/shaderinclude's compile test is what holds
// that line, with volumetric.inc as its control.
//
// volumetric.inc includes this, so nothing that already included volumetric.inc
// has to change.

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
