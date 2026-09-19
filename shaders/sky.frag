#version 450
#extension GL_GOOGLE_include_directive : require

layout(location = 0) in vec2 fragUV;

// Offsets match the lit shaders' block exactly. pointPos, pointColor and
// ambient are unused here and declared only so what follows lands where every
// other shader reads it from -- one packing convention beats a per-shader one
// nobody can keep in step.
//
// cameraPos and fog.xy stopped being padding when this shader started
// marching in-scattering: the eye and the fog are what volumetric.inc needs,
// and the fog IS the medium it scatters off. They are filled by the sky draw
// in renderer/commands.go.
layout(push_constant) uniform PushConstants {
    mat4 invVP;    // inverse view-projection
    mat4 model;    // [0].xyz = camera position
    vec4 tint;     // x = time, y = nightFactor, z = cloud raymarch steps
    vec4 sunDir;   // xyz = direction toward the body lighting the scene
    vec4 sunColor; // rgb, w = the real sun's elevation
    vec4 pointPos;
    vec4 pointColor;
    vec4 ambient;
    vec4 cameraPos; // xyz = eye, w = fog density
    vec4 fog;       // x = height falloff, y = base height, zw = real sun horizontal
} pc;

// The half-resolution cloud target: rgb = in-scattered radiance, a =
// transmittance. Always bound; a scene with clouds disabled gets a cleared
// target that composites to a no-op rather than a special case here.
layout(set = 0, binding = 0) uniform sampler2D cloudTex;

// The same per-frame block every lit pipeline binds at binding 0 of its shadow
// set, bound here at binding 1 of the cloud set. The dome, the fog distant
// geometry fades into and the water's reflection have to agree on the palette,
// and reading the SAME BUFFER is the only form of agreement that cannot drift
// -- a second copy packed from the same Go value still has two places to go
// wrong. It rides on the cloud descriptor set because that is the set this
// pass already binds; see createDescriptorSetLayout.
//
// cascadeVP and nightGrade are declared solely so skyPalette lands at the
// offset renderer/shadow.go packs it at, exactly as the push block above
// declares pointPos through cameraPos to land fog where every shader reads it.
layout(set = 0, binding = 1) uniform ShadowData {
    mat4 cascadeVP[2];
    vec4 nightGrade;
    vec4 skyPalette[6];
    // x = the in-scattering march's Henyey-Greenstein anisotropy, y = its
    // step count. Read by volInscatter below.
    vec4 volumetric;
} shadow;

// The clustered light data at set 1, bindings 3-5, on the shadow/light set --
// the same buffers the seven lit shaders read, bound here through
// skyPipelineLayout. The sky is not a lit surface and takes no light from
// these directly; what it needs them for is the air in FRONT of it, which is
// the one place a beam aimed upward can be seen at all.
#define LIGHT_SET 1

layout(location = 0) out vec4 outColor;

#include "atmosphere.inc"
#include "lights.inc"
#include "volumetric.inc"

// The noise and the raymarch moved to clouds.frag, which runs at half
// resolution. What is left here is the dome, which measured 0.008 ms against
// the march's 1.146 -- the sky was never the expensive part of the sky.

void main() {
    float time = pc.tint.x;
    float nightFactor = pc.tint.y;
    vec3 camPos = pc.model[0].xyz;
    vec3 sunDir = normalize(pc.sunDir.xyz);
    vec3 sunCol = pc.sunColor.xyz;

    // Reconstruct world-space ray direction from screen UV
    vec2 ndc = fragUV * 2.0 - 1.0;
    vec4 world = pc.invVP * vec4(ndc, 0.0, 1.0);
    vec3 dir = normalize(world.xyz / world.w - camPos);

    // ----- Sky gradient -----
    float elevation = dir.y;
    // The real sun's elevation, not the current light's: at night pc.sunDir is
    // the moon, which is high when the sky should be darkest.
    float sunElevation = pc.sunColor.w;

    // And the real sun's *direction*, for the same reason. sunDir above stays
    // the lighting body, which is what the clouds below want -- they are lit by
    // the moon at night and their palette already accounts for that. Only the
    // scattering halo has to follow the sun itself.
    vec3 realSunDir = atmSunDirFrom(sunElevation, pc.fog.zw);

    vec3 zenith, horizon;
    atmSkyPalette(sunElevation, shadow.skyPalette, zenith, horizon);

    // Rayleigh-ish falloff rather than a linear ramp: most of the colour
    // change happens in the first part of the climb from the horizon, which is
    // what gives the sky depth instead of a flat wash.
    float t = pow(smoothstep(-0.08, 0.75, elevation), 0.65);
    vec3 skyColor = mix(horizon, zenith, t);

    skyColor += atmSunGlow(dir, realSunDir, sunCol, sunElevation);

    // Below-horizon: darken toward ground
    if (elevation < 0.0) {
        float belowFade = smoothstep(0.0, -0.3, elevation);
        vec3 groundColor = mix(horizon, vec3(0.15, 0.18, 0.12), belowFade) * mix(0.3, 1.0, atmDaylight(sunElevation));
        skyColor = mix(skyColor, groundColor, belowFade);
    }

    // ----- Volumetric clouds -----
    //
    // A slab of 3D noise, raymarched. The previous version sampled 2D noise on
    // a single plane, which looks convincing straight up and falls apart at a
    // shallow angle: a plane has no thickness, so clouds near the horizon are
    // as thin as clouds overhead, when they should be the longest sightline in
    // the sky. Marching a volume gets that for free, along with self-shadowing
    // and edges that light up from behind.
    //
    // This is only affordable because the sky now draws last and depth-tested,
    // so nothing here runs for a pixel the terrain covers.
    // Composite the half-resolution cloud target over the dome.
    //
    // rgb is in-scattered radiance and alpha is transmittance, so this is a
    // premultiplied over: what gets through the layer, plus what the layer
    // itself sends toward the eye. Sampling bilinearly upscales it, which is
    // acceptable precisely because clouds are soft -- there is no edge here for
    // the interpolation to blur that was not already soft.
    vec4 clouds = texture(cloudTex, fragUV);
    float cloudTransmit = clouds.a;
    skyColor = skyColor * cloudTransmit + clouds.rgb;

    // ----- In-scattering from the local lights, in front of all of it -----
    //
    // Without this a beam aimed at the night sky stops dead at the horizon,
    // which is what the first capture of this feature showed: a column of
    // light rising off the plaza, crossing the dark hills, and vanishing at
    // the skyline as if it had hit something. The sky is drawn last and
    // depth-tested, so these are exactly the pixels nothing else covered --
    // the air between the eye and the end of the froxel grid, with no surface
    // in it.
    //
    // Two numbers the lit path gets for free have to be built here. A lit
    // fragment knows how far away it is and what its view depth is; a
    // fullscreen triangle knows neither -- gl_FragCoord.w is 1.0 for every
    // pixel of it, because sky.vert emits w = 1. So:
    //
    //   - how far to integrate is the grid's own far plane, recovered from
    //     the binner's slice parameters (volGridFar), because there are no
    //     froxels past it and nothing there could light the air anyway;
    //   - view depth per metre along the ray is the cosine to the camera
    //     axis, and the axis is the ray through NDC (0,0) -- the same
    //     reconstruction `dir` above uses, at the centre of the screen.
    //
    // Nothing is composited over this and nothing fogs it: the dome IS the
    // far distance, so light scattered in front of it is simply added.
    vec4 axisWorld = pc.invVP * vec4(0.0, 0.0, 0.0, 1.0);
    vec3 axis = normalize(axisWorld.xyz / axisWorld.w - camPos);
    float depthPerDist = max(dot(dir, axis), 1e-4);
    skyColor += volInscatter(gl_FragCoord.xy, dir, volGridFar() / depthPerDist, depthPerDist);

    outColor = vec4(skyColor, cloudTransmit);
}
