---
id: bloom
title: Bloom
summary: >
  Glare around anything brighter than the threshold, from a mip chain composited
  into the tonemap resolve. Off by default, and it needs something above 1 to
  select.
capability: rendering
status: stable
since: v0.4.0
api:
  - renderer.Renderer.SetBloom
assets: none
example: examples/16-materials
run: go run ./16-materials
verified: 2026-08-02
---

# Bloom

```go
func (g *game) Init(e *glyph.Engine) error {
	// intensity, threshold, knee, radius.
	e.Renderer().SetBloom(0.7, 1.2, 0.2, 1.0)
	return nil
}
```

`intensity` scales what the glow contributes. **Zero or less switches the chain
off entirely and skips recording it**, which is the default — a scene that does
not want bloom does not pay for it. See the measurements below.

`threshold` is where a pixel starts to glow and `knee` softens the transition
either side of it. `radius` widens the upsample tent in source texels.

## Keep `threshold - knee` at or above 1

This is the one setting that will bite you, and it does not look like a bug when
it goes wrong.

A daytime sky sits around **0.68** in linear, and clouds reach close to 1. If the
ramp starts below that, the sky joins the bloom and the whole frame lifts. It
reads as haze — a slightly milky image you might accept as atmosphere — rather
than as a mistake.

Measured in `16-materials`: at `SetBloom(0.7, 1.0, 0.5, 1.0)` the ramp began at
0.5 and the sky, far from anything emissive, moved by **RMS 0.025 (max 0.047)**
against the same scene with bloom off. Raising it to `(0.7, 1.2, 0.2, 1.0)` — so
the ramp begins at exactly 1.0 — dropped that to **RMS 0.002**, which is the
animation noise floor, while the emissive panel stayed at RMS 0.31 and the panel
beside it at 0.058. The glow still lands where light should reach; the sky no
longer does.

**Check any threshold against the sky, not against the thing you want to glow.**
The thing you want to glow will look fine either way.

`godray.frag` documents the same failure for the same reason. A screen-space
effect without a threshold is a blur of the whole image.

## It needs something above 1

Bloom on a scene where nothing exceeds 1 is either nothing at all or a blur of
every white pixel, depending on where the threshold sits. There is no useful
setting in between, because there is nothing to separate.

What produces values above 1 today is **`MaterialOptions.EmissiveStrength`** —
see [`material-maps.md`](material-maps.md). The half-float target that carries
them is [`hdr-tonemap.md`](hdr-tonemap.md). Those two are prerequisites, not
related reading.

## How it works

1. **Prefilter** — the scene is downsampled to half resolution with a 13-tap
   kernel and thresholded.
2. **Downsample** — four more halvings, same kernel. At 1280x720 the chain runs
   640x360 down to 40x22.
3. **Upsample** — back up, each level added into the one below through an
   additive blend with a 3x3 tent.
4. **Composite** — the tonemap resolve adds level 0 **before** exposure and the
   curve, so the glow goes through the same response as the rest of the frame.
   Adding it afterwards leaves a linear glow on a compressed image, which reads
   as a decal.

The kernels are Jimenez's, from the SIGGRAPH 2014 course notes. They are wider
than a bilinear step on purpose: halving with one tap aliases, and a bright pixel
that survives at one level but not the next flickers — while being the brightest
thing on screen.

Each level doubles the glow's width for the same tap count, which is what makes a
wide halo affordable. `bloomLevels` is 5 because fewer produces a tight ring that
reads as an artefact rather than as glare.

## What it costs

`task bench`, two runs each side, GPU timestamps, 1280x720:

| scene       | before   | after    | delta      |
| ----------- | -------- | -------- | ---------- |
| cube        | 0.902 ms | 0.904 ms | +0.002     |
| terrain     | 1.740 ms | 1.754 ms | +0.014     |
| grass       | 5.065 ms | 5.015 ms | −0.050     |
| water       | 1.990 ms | 1.989 ms | −0.001     |
| lights      | 0.178 ms | 0.180 ms | +0.002     |
| particles   | 1.174 ms | 1.188 ms | +0.014     |
| **materials** | 1.462 ms | 1.662 ms | **+0.200** |
| kitchensink | 4.180 ms | 4.172 ms | −0.008     |

`materials` is the only scene with bloom on. Everything else is within the
0.02 ms run-to-run spread, two of them negative — the disabled path is free, not
merely cheap.

The 0.200 ms splits into the chain at 0.160 ms and the resolve going 0.013 →
0.069 ms, because it now samples a second texture. The `tonemap` pass stays at
0.013 ms in every scene where bloom is off, which confirms the shader's uniform
branch really does skip the sample.

## Failure mode: the layout check does not care about your branch

The bloom images are bound by the resolve's descriptor set **every frame**,
including when bloom is off and nothing has ever written them. Vulkan validates
the layout a descriptor declares against the image's actual layout at submit,
regardless of whether the shader samples it.

The first attempt relied on the shader's `if (bloomIntensity > 0.0)` to make the
contents irrelevant. It does skip the read, and it is still there, but it does
nothing for the layout: every frame reported
`UNASSIGNED-CoreValidation-DrawState-InvalidImageLayout`.

`primeBloomLayouts` clears every level once at startup and leaves it in
`SHADER_READ_ONLY_OPTIMAL`. It runs immediately after the command pool is
created, alongside `initCubeShadowLayout`, which exists for the same reason. It
also needs `TRANSFER_DST` usage on the images, used only for that one clear.

## Not done

- **No dirt or lens texture.** The composite is a straight add.
- **`bloomLevels` is a constant, not a setting.** Resolution-independent glow
  width would want the level count to follow the frame size.
- **The chain is per swapchain image**, so it costs 3x the memory of one chain.
  Necessary — a frame can be writing it while the GPU still reads the previous
  frame's during its resolve.
- **Resizing rebuilds both chains without resetting the descriptor pool,** so it
  consumes pool capacity each time. `maxBloomSets` is headroom for several
  resizes, not a fix; enough window dragging will fail the reallocation with a
  clear error.
