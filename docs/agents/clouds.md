---
id: clouds
title: Volumetric clouds
summary: >
  A raymarched cloud slab rendered at half resolution and accumulated across
  frames. It is the single most expensive thing the renderer draws, and the
  knobs that control it are not the ones you would guess.
capability: environment
status: stable
since: v0.4.0
api:
  - glyphengine.Sky.CloudSteps
  - glyphengine.CloudsOff
  - glyphengine.CloudsLow
  - glyphengine.CloudsHigh
assets: none
example: examples/09-water
run: go run ./09-water -clouds 32
verified: 2026-08-04
---

# Volumetric clouds

```go
env := glyph.DefaultEnvironment()
env.Sky.CloudSteps = glyph.CloudsHigh // or CloudsLow, or CloudsOff
scene.Env = env
```

Three presets, and the count is the raymarch's sample count along each view ray.
`CloudsOff` is 0 and skips the layer entirely — the sky keeps its gradient and
sun glow.

## This is where the frame's time goes

Not the sky dome. Measured on `09-water`, sky pass total:

| | cost |
| --- | --- |
| clouds on, 32 steps | 1.146 ms |
| clouds off | **0.008 ms** |

Over **99 percent** of "the sky is the top GPU pass" is this march. The dome —
gradient, sun glow, horizon falloff, the whole analytic sky — is free by
comparison.

This is worth stating plainly because it has already misled someone once. A
Hillaire-style LUT atmosphere was built on the assumption that replacing the
dome would make the sky cheaper, and it changed the sky pass from 0.917 ms to
0.918 ms. **If you want the sky cheaper, the march is the only thing that
matters.** One command tells you the split: `-clouds 0`.

## Architecture

The march runs in its **own pass at half resolution**, writing in-scattered
radiance to rgb and transmittance to alpha. `sky.frag` composites it over the
dome as a premultiplied over.

Half resolution is where the win is — GPU totals went water 1.399 → 0.688 ms,
terrain 1.296 → 0.591 ms, kitchensink 3.515 → 3.131 ms. And it is invisible:
sky-region RMS between full and half resolution is 0.00079 against a
run-to-run noise floor of 0.00067 on the same view, max difference 2 in 255.
Clouds are soft, so a bilinear upscale blurs nothing that was sharp.

The result is then **accumulated across frames**, each one blending with the
previous reprojected through the previous view-projection. The march jitters
every ray's start, so a single frame is a noisy sample; averaging converges it.

## Three noise fixes, and why the obvious one is wrong

The march produces grain. Measured over the night sky band against a clouds-off
floor of 0.000054:

| | grain |
| --- | --- |
| original | 0.001374 |
| + Nyquist octave fading | 0.000994 |
| + world-anchored jitter | 0.000856 |
| + half res and temporal accumulation | **0.000741** |

**Do not fix this by turning the jitter down.** Removing it entirely gives the
best static number and brings back visible horizontal banding at the horizon; a
partial amplitude is worse than either, because the residual banding beats
against the hash into a structured dither that reads worse than smooth grain.

**Octaves fade by Nyquist.** The finest fbm octave has features about 113 world
units across and a step covers 30 to 170 units at ordinary elevations, so that
octave sat at or under the sample spacing and could only alias. `fbm3DDetail`
drops octaves the step length cannot resolve, renormalised by the amplitude
actually used so coverage does not shift with them.

**The jitter is keyed to world direction, not screen position.** It was
`hash2D(fragUV)`, which nails the noise pattern to the display: turn the camera
and the clouds slide through a stationary field, which reads as a Photoshop
add-noise filter rather than as grain. `hash3D(dir * 4096)` gives a patch of sky
its own jitter so the noise travels with the cloud. This one is invisible in any
still frame and obvious the moment the camera moves.

## Temporal accumulation

Reprojection uses the **middle of the marched slab** as a stand-in for where the
cloud is, taken from the geometry rather than a constant — a grazing ray crosses
the slab tens of kilometres out while an overhead one crosses it in hundreds of
units. Clouds are far enough away that one frame of camera translation is well
under a half-resolution texel, so rotation is the motion that matters and this
handles it exactly.

Two guards stop it ghosting:

- **History off screen last frame is rejected.** There is nothing behind the
  edge to blend with, and sampling the clamped edge smears it inward.
- **History is clamped to the current frame's neighbourhood.** The clouds drift
  under wind so history is always slightly stale; clamping keeps convergence
  where the signal is stable and discards it where the picture is genuinely
  changing.

Cost is free within noise — the history fetches hide behind the march's ALU
work.

**The history chain is indexed by a frame counter, not the swapchain image
index.** The presentation engine may hand indices back in any order, and history
has to be strictly the previous frame. It needs `maxFramesInFlight + 1` buffers;
two would let a frame overwrite a buffer another frame in flight is still
sampling.

## Failure mode: a pass that sometimes does not run

The cloud pass runs **every frame**, even with `CloudsOff`. A pass that
sometimes does not run leaves its target in an undefined layout while the sky
still binds it every frame, and Vulkan validates a descriptor's declared layout
at submit whether or not the shader samples it. With zero steps the shader
early-outs to fully transmissive and the composite is a no-op.

This rule has now caught three separate targets — the bloom chain, the cloud
history, and a sky-view table that no longer exists. `primeSampledImages` exists
for it.

## Night lighting is an artistic floor

Moonlight in the march is `(0.085, 0.10, 0.145)` against daylight's
`(1.0, 0.97, 0.92)`. Real moonlight is roughly a **millionth** of sunlight, so
this is not a measurement — a physically honest night sky is a black one.

It was `0.55`, two thirds of daylight, which made night clouds read as white: a
lit cloud measured 0.507 luminance at midnight against 0.811 at noon. Night now
measures 0.161 median.

If night needs rebalancing, move the sky palette, the moon boost and this
together — darkening the sky alone leaves the moon as a hole punched in it. And
be careful with the palette specifically: `atmSkyPalette`'s night endpoints are
read by the dome, by the fog distant geometry fades into, and by the water's
reflection, so lifting them to make the sky legible washes out the whole
landscape. **Brighten the moon, not the air.**

## Not done

- **Step count is still 32 at `CloudsHigh`.** That number was chosen to keep
  *single-frame* noise tolerable, and temporal accumulation removes that
  constraint — a lower count is the next available saving.
- **No weather map.** Coverage is a single constant, slightly heavier at night
  so the sky is not empty.
- **The march is not distance-adaptive.** Step length is uniform along the ray,
  so a grazing view spends the same samples on the near slab as the far one.
