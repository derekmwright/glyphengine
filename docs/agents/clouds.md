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
  - glyphengine.Sky.CloudSteps
  - glyphengine.Sky.Cirrus
  - glyphengine.EnvironmentState.Cirrus
  - renderer.SceneLighting.Cirrus
  - glyphengine.CloudsOff
  - glyphengine.CloudsLow
  - glyphengine.CloudsHigh
requires:
  - cgo
  - vulkan-runtime
assets: none
example: examples/09-water
run: task example:09-water -- -background -time 0.755 -pitch -0.55 -yaw 1.771 -cirrus 0.5
verified: 2026-09-21
---

# Clouds

```go
import glyph "github.com/derekmwright/glyphengine"

func outdoorEnvironment() *glyph.Environment {
    env := glyph.DefaultEnvironment()
    env.Sky.CloudSteps = glyph.CloudsHigh
    env.Sky.Cirrus = 0.5 // optional high wisps; 0 disables, 1 is full strength
    return env
}
```

Assign that environment to `scene.Env`. `CloudSteps` controls the volumetric
cumulus: `CloudsOff` is 0, `CloudsLow` is 16, and `CloudsHigh` is 32. The count
sets the coarse ray stride; occupied intervals use quarter-sized steps, with a
budget of four times the count. It is not a fixed total number of samples.

`Cirrus` is independent. Set `CloudSteps = CloudsOff` and `Cirrus > 0` for only
high clouds, or set both to zero for clear sky. `DefaultSky` leaves cirrus at
zero so existing games keep their cloud coverage. Both controls can change at
runtime without rebuilding GPU resources. Custom `EnvironmentSource` users
supply the same fields in `EnvironmentState`; direct renderer callers use
`renderer.SceneLighting`.

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
legibility. Ambient fill comes from `Scene.SetSkyPalette`, shared with the
dome, fog and water. Changing direct cloud-light colours requires replacing
`ShaderSet.CloudsFrag` through `WithShaders`.

## Rendering and cost

Cumulus occupies a procedural slab from 700 to 3400 world units. Adaptive
view steps sample a noise field with step-dependent octave fading; six
geometrically spaced light samples estimate self-shadowing. High cirrus is
an 8000-unit plane: domain-warped anisotropic noise, a separate breakup mask,
and distance/horizon fades. It has no volumetric self-shadowing.

Both layers draw into the same half-resolution target. RGB holds scattered
radiance and alpha holds transmittance. Cirrus is composed behind cumulus;
`sky.frag` then composites that result over the full-resolution dome. The
cloud pass draws the full target; only the later sky composite is rejected
by terrain depth. Cirrus adds no render pass, GPU resource or CPU allocation.

Measured on a Radeon RX 7900 XTX, three interleaved 200-frame runs of
`09-water -time 0.755 -pitch -0.38 -yaw 1.771` at 1280x720/MSAA 4, fixed clock:

| Setting | Cloud pass | Whole GPU frame |
| --- | --- | --- |
| Cumulus, cirrus 0 | 0.863–0.868 ms | 1.249–1.264 ms |
| Cumulus, cirrus 1 | 0.888–0.892 ms | 1.281–1.294 ms |

These are one view and one GPU. Measure the game's workload with `task bench`
or `GLYPHENGINE_TIMING=tsv`; grass or other passes can dominate a real scene.

## History and limits

The result blends 80% reprojected history with the current sample. Rotation
uses the previous view-projection. Translation approximates depth with the
middle of the marched cumulus span, or cirrus height when cumulus is disabled.
A mixed pixel has only one history depth, so fast camera translation can
leave trails. This is a ground-view sky: cirrus is omitted when the camera
is at/above its plane, and neither layer supplies a downward view from above.

Off-screen history is rejected. The clamp bounds history with the current
pixel and four samples from the **history** texture; it is not a current-frame
neighbourhood clamp. Still-image checks do not establish motion quality.

History buffers use a frame counter, not the swapchain image index, with
`maxFramesInFlight + 1` targets. The pass runs every frame, including when both
layers are disabled, so its sampled image always has a valid layout. Zero
steps and zero cirrus write fully transmissive pixels.

There is no weather map, user-specified layer altitude or cloud shadow on the
ground. Lowering `CloudSteps` also changes which noise octaves resolve, so it
can change shape as well as quality and cost.

## Verification

`task clouds` checks gold and rose undersides, daytime/night controls, cirrus
with the volume disabled, attenuation behind cumulus, and an independent
fixed-clock repeat. It requires changes above 8/255 before counting visible
wisps or overlap. Captures remain in `examples/.clouds` for inspection. The
check is a regression gate, not a claim of photographic realism. Reverting
only the lighting makes both colour checks fail. Removing cirrus attenuation
raises its contribution behind cumulus from 4.224 to 25.317/255 (ratio 0.182
to 1.093), failing the overlap check. These ablations were rendered and checked.

Also run `task ci`, `task validate`, `task determinism`, `task sky`, and
`task skypalette` after renderer changes. For visual review, the example accepts
`-background` and all automated tasks set `GLYPHENGINE_BACKGROUND=1` to avoid
requesting keyboard focus.
