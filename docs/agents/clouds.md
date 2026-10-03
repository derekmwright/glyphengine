---
id: clouds
title: Volumetric clouds and high cirrus
summary: >
  Adaptive raymarched cumulus and an optional thin cirrus layer, sharing a
  half-resolution target and temporal history, with warm sunset lighting.
capability: environment
status: stable
since: v0.4.0
api:
  - sky.Sky.CloudSteps
  - sky.Sky.Cirrus
  - sky.CloudsOff
  - sky.CloudsLow
  - sky.CloudsHigh
  - sky.Shaders
  - sky.Fill
  - glyphengine.EnvironmentState.CloudSteps
  - glyphengine.EnvironmentState.Cirrus
  - renderer.ShaderSet.CloudsFrag
  - renderer.SceneLighting.CloudSteps
  - renderer.SceneLighting.Cirrus
requires:
  - cgo
  - vulkan-runtime
assets: none
example: examples/09-water
run: task example:09-water -- -background -time 0.755 -pitch -0.55 -yaw 1.771 -cirrus 0.5
verified: 2026-10-02 # the cloud shape and lighting are x/sky/clouds.frag now, in the sky slot; the half-res target, its history, its barriers and the palette descriptor stayed in the engine (#161 step 4)
---

# Clouds

The cloud *shape and lighting* are `x/sky/clouds.frag`. The half-resolution
target they march into, its temporal history, its barriers and the descriptor set
that carries the palette are the engine's. `CloudsFrag` is one of the three
stages in the sky slot (see [environment](environment.md)), and the engine embeds
nothing for it: with it nil no cloud pipeline is created and no march is recorded.
Both halves are needed.

```go
import (
    glyph "github.com/derekmwright/glyphengine"
    xsky "github.com/derekmwright/glyphengine/x/sky"
)

// the shader half, at glyph.New
opts := []glyph.Option{glyph.WithShaders(xsky.Shaders())}

// the values half
func outdoorEnvironment() *xsky.Environment {
    env := xsky.DefaultEnvironment()
    env.Sky.CloudSteps = xsky.CloudsHigh
    env.Sky.Cirrus = 0.5 // optional high wisps; 0 disables, 1 is full strength
    return env
}
```

Assign that environment to `scene.Env`. `CloudSteps` controls the volumetric
cumulus: `CloudsOff` is 0, `CloudsLow` is 16, and `CloudsHigh` is 32. The count
sets the coarse ray stride; occupied intervals use quarter-sized steps, with a
budget of four times the count. It is not a fixed total number of samples.

`Cirrus` is independent. Set `CloudSteps = CloudsOff` and `Cirrus > 0` for only
high clouds, or set both to zero for clear sky. `sky.DefaultSky` leaves cirrus at
zero so existing games keep their cloud coverage. Both controls can change at
runtime without rebuilding GPU resources. Custom `EnvironmentSource` users
supply the same fields in `EnvironmentState`; direct renderer callers use
`renderer.SceneLighting`. None of the three reaches the march without a shader in
the slot to run it.

## Sunset and night

The volume's light march follows the directional light, including the sun
below the horizon. This lights exposed undersides while leaving the interior
shaded. Direct cloud light changes from daylight white through gold to rose
as the sun sets. The colours are an artistic approximation, not a spectral
atmosphere. The high layer uses a small elevation offset to retain warm light
later than cumulus.

`task clouds` measures a sunlit underside in `09-water` under a fixed 16.667 ms
clock at 1280x720, frame 120, yaw 1.771, pitch -0.38. In the rectangle
(420,365)-(475,400), the time .755 red-minus-green mean is 39.92/255, versus
0.37 with the former washed-out lighting. At .765 blue-minus-green is
15.33/255 versus -30.05, distinguishing rose from yellow. The check also
requires visible brightness and contrast against the shaded core. Noon is
byte-identical to the previous lighting with cirrus disabled.

Night direct light remains `(0.030, 0.036, 0.055)`, deliberately boosted for
legibility. Ambient fill comes from the frame's `EnvironmentState.SkyPalette` --
`Scene.SetSkyPalette` for a source with no opinion about it, the source's own
otherwise -- shared with the dome, fog and water. The palette and its default
stayed in the engine for that reason: fog and water read it with no clouds and no
dome at all.

Changing direct cloud-light colours means supplying a different `CloudsFrag`.
`sky.Fill` is the way to keep this package's dome and stars and replace only the
clouds: it leaves any stage the caller already set alone.

## Rendering and cost

Cumulus occupies a procedural slab from 700 to 3400 world units. Adaptive
view steps sample a noise field with step-dependent octave fading; six
geometrically spaced light samples estimate self-shadowing. High cirrus is
an 8000-unit plane: domain-warped anisotropic noise, a separate breakup mask,
and distance/horizon fades. It has no volumetric self-shadowing.

Both layers draw into the same half-resolution target. RGB holds scattered
radiance and alpha holds transmittance. Cirrus is composed behind cumulus;
`x/sky/sky.frag` then composites that result over the full-resolution dome. The
cloud pass draws the full target; only the later sky composite is rejected
by terrain depth. Cirrus adds no render pass, GPU resource or CPU allocation.

The target, its sampler, its descriptor sets and the two barriers around the
march are `renderer/clouds.go` and stay in the engine. That is a deliberate
split rather than where the knife happened to fall: those sets are the only place
in the engine that writes binding 1 of the shared texture set, which is the
per-frame block the *dome* reads the palette out of. A dome supplied with no
cloud stage beside it is a supported configuration, so the target cannot be
conditional on `CloudsFrag` — and `shaders/sky.vert`, the fullscreen triangle the
march draws, is shared with seven other passes besides.

Measured on a Radeon RX 7900 XTX, three interleaved 200-frame runs of
`09-water -time 0.755 -pitch -0.38 -yaw 1.771` at 1280x720/MSAA 4, fixed clock:

| Setting | Cloud pass | Whole GPU frame |
| --- | --- | --- |
| Cumulus, cirrus 0 | 0.863-0.868 ms | 1.249-1.264 ms |
| Cumulus, cirrus 1 | 0.888-0.892 ms | 1.281-1.294 ms |

These are one view and one GPU. Measure the game's workload with `task bench`
or `GLYPHENGINE_TIMING=tsv`; grass or other passes can dominate a real scene.

## History and limits

The result blends 80% reprojected history with the current sample. When both
shader time and view-projection are unchanged, it resolves the current sample
directly: direction-keyed jitter supplies no new samples while stationary.
Without this, paused captures 60 frames apart differed by 1 pixel in the water
fixture and 185 with sunset cirrus, each by 1/255. Both now repeat exactly.
Rotation
uses the previous view-projection. Translation approximates depth with the
middle of the marched cumulus span, or cirrus height when cumulus is disabled.
A mixed pixel has only one history depth, so fast camera translation can
leave trails. This is a ground-view sky: cirrus is omitted when the camera
is at/above its plane, and neither layer supplies a downward view from above.

Off-screen history is rejected. The clamp bounds history with the current
pixel and four samples from the **history** texture; it is not a current-frame
neighbourhood clamp. Still-image checks do not establish motion quality.

History buffers use a frame counter, not the swapchain image index, with
`maxFramesInFlight + 1` targets (`cloudBufferCount`). With a cloud shader in the
slot the pass runs every frame, including when both layers are disabled: zero
steps and zero cirrus write fully transmissive pixels, so the sampled image
always has a valid layout and the dome's composite is a no-op.

With **no** cloud shader the march is skipped outright — no barrier, no pass, no
draw — and the valid layout comes from `primeSampledImages` instead, which clears
the targets once at creation and after a resize. It clears them to
`{0, 0, 0, 1}`, and alpha is transmittance, so that is exactly the no-op the
march would have written: `skyColor * a + rgb` is the identity at `a = 1`. A dome
with no clouds beside it is therefore correct rather than undefined, and it is the
prime that makes that a guarantee rather than a coincidence about what fresh
device memory happened to hold.

There is no weather map, user-specified layer altitude or cloud shadow on the
ground. Lowering `CloudSteps` also changes which noise octaves resolve, so it
can change shape as well as quality and cost.

## Verification

`task clouds` checks gold and rose undersides, daytime/night controls, cirrus
with the volume disabled, attenuation behind cumulus, and an independent
fixed-clock repeat, including a paused sunset. It requires changes above 8/255 before counting visible
wisps or overlap. Captures remain in `examples/.clouds` for inspection. The
check is a regression gate, not a claim of photographic realism. Reverting
only the lighting makes both colour checks fail. Removing cirrus attenuation
raises its contribution behind cumulus from 4.224 to 25.317/255 (ratio 0.182
to 1.093), failing the overlap check. These ablations were rendered and checked.

Also run `task ci`, `task validate`, `task determinism`, `task sky`, and
`task skypalette` after renderer changes. For visual review, the example accepts
`-background` and all automated tasks set `GLYPHENGINE_BACKGROUND=1` to avoid
requesting keyboard focus.

Editing `clouds.frag` now means recompiling it with `task xsky:shaders`, not
`task shaders`: the source and its committed `.spv` are in `x/sky`, and
`x/sky/spirv_test.go` fails under `task ci` if the two have parted company. The
`.spv` is compiled through the engine's exported include set — `clouds.frag`
calls `atmSkyPalette` and `atmTwilight` out of `atmosphere.inc` — so a changed
signature in that set fails at build rather than at draw. `task skymigration`
holds the other end: one of its three scenes is this example at its default hour
with cumulus on, compared against a capture taken before the shader left the
engine.
