// Camera, view ray and water path length, shared by the scattering and
// composite passes so the two cannot disagree about how much water a pixel has
// in front of it. Included by bare relative name: it is a file of this package,
// not of the engine's shader tree, so it travels with the package's own GLSL.
// The engine's shared fragments are still named bare and resolved by -I; see
// AGENTS.md rule 2.
//
// The including file must have declared `pc.inverseVP`.

// waterEye recovers the camera's world position from the inverse
// view-projection, so it costs no push constants.
//
// The camera is the one world point whose clip-space image has w == 0: for this
// engine's reverse-Z perspective, VP maps it to (0, 0, k, 0) with
// k = far*near/(far-near), nonzero for every positive near. Inverting that,
// inverseVP * vec4(0,0,1,0) is the eye's homogeneous position scaled by 1/k --
// and the perspective divide cancels the scale, so no one needs to know k.
//
// This is four push floats the options get to use instead, which is what makes
// the whole parameter set fit in the 128 application bytes.
vec3 waterEye(mat4 inverseVP) {
    vec4 e = inverseVP * vec4(0.0, 0.0, 1.0, 0.0);
    return e.xyz / e.w;
}

// waterUnproject takes NDC xy and a reverse-Z depth to a world position.
// Depth 0 is the background, and it unprojects to the far plane rather than to
// infinity, so an unobstructed ray is bounded by the frustum. That is the job
// the source's sphere intersection did for it, and the frustum is the honest
// equivalent for a world that is not a planet.
vec3 waterUnproject(mat4 inverseVP, vec2 ndc, float depth) {
    vec4 p = inverseVP * vec4(ndc, depth, 1.0);
    return p.xyz / p.w;
}

// waterPath is how far along dir the ray stays under the surface: the opaque
// scene, or the surface overhead, whichever comes first. A ray that is level or
// descending never leaves, so only a climbing ray has an exit.
float waterPath(vec3 eye, vec3 dir, float opaqueDistance, float level) {
    float exitDistance = 1e30;
    if (dir.y > 1e-6) {
        exitDistance = max((level - eye.y) / dir.y, 0.0);
    }
    return min(opaqueDistance, exitDistance);
}
