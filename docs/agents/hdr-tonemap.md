---
id: hdr-tonemap
title: HDR rendering and the tonemap resolve
summary: >
  The scene renders into a half-float target and a fullscreen pass resolves it
  to the swapchain. That pass is where exposure and a tonemap curve live, and
  every path that presents a frame has to run it.
capability: rendering
status: stable
since: v0.4.0
api:
  - renderer.Renderer.SetTonemap
assets: none
run: task bench
verified: 2026-08-02
---

# HDR rendering and the tonemap resolve

The scene no longer draws into the swapchain. It draws into an offscreen
`R16G16B16A16_SFLOAT` image, one per swapchain image, and a fullscreen pass at
the end of the frame reads that image and writes the presentable one.

Nothing about the default look changed. Exposure defaults to off and the curve
defaults to identity, so the resolve is a copy. That is deliberate: adding a
render target and choosing a look are separate decisions, and doing both at once
makes it impossible to say which one moved the image.

## Choosing a look

Set it from `Game.Init`, where the renderer is available:

```go
func (g *game) Init(e *glyph.Engine) error {
	// exposure, curve, whitePoint.
	//   exposure <= 0 leaves the scene alone.
	//   curve 0 is identity; curve 1 is extended Reinhard.
	//   whitePoint is the value that maps to white, and is clamped to >= 1.
	e.Renderer().SetTonemap(1.2, 1, 4.0)
	return nil
}
```

Passing `(0, 0, 0)` restores the default. The values are read fresh each frame,
so they can be animated. `examples/16-materials` selects Reinhard this way,
because it is the one scene with a surface emitting above 1.

## Why the target exists

An 8-bit channel cannot represent a value above 1, so on the old path a
highlight was clipped at the moment it was written. Everything downstream
follows from that: a tonemap curve has nothing to compress, bloom has nothing to
bloom, and a bright sun disc is simply white rather than bright. The engine's own
notes record ACES being tried and reverted for exactly this reason — it is built
for HDR input and there was none.

That the target really carries values above 1, rather than being a more
expensive route to the same clipped image, was verified rather than assumed.
`lit.frag` was temporarily changed to emit `outColor.rgb *= 4.0` and the tonemap
exposure set to `0.25`, then `11-lights` was captured and compared against an
unmodified capture. The lit geometry — posts, ground, coloured light pools,
shadow edges — came back intact. On an 8-bit target the 4x would have clipped
every lit surface to white and the 0.25 exposure would have returned a flat
quarter-grey silhouette with no shading in it at all. Only the sky and the light
gizmos darkened, which is correct: they are drawn by other shaders that were not
scaled.

## Failure mode: a frame that presents nothing

Every path that acquires a swapchain image and presents it must record the
tonemap pass, because the scene passes no longer touch the presentable image at
all. Miss it and the swapchain is presented in `UNDEFINED` layout holding
whatever it held last.

This is not hypothetical. `Renderer.DrawTriangle` has its own record-and-present
loop separate from `recordCommandBuffer`, and it was missed on the first pass.
Nothing in `task ci` caught it — the frame still presented, the example still
exited zero. `task validate` caught it immediately, as
`VUID-VkPresentInfoKHR-pImageIndices-01430`. Use `recordTonemap` rather than
open-coding the pass, and run `task validate` on anything that adds a present
path.

## What it costs

Measured with `task bench` on the same machine, two runs each side, GPU
timestamps:

| scene       | GPU before | GPU after | tonemap pass |
| ----------- | ---------- | --------- | ------------ |
| cube        | 0.902 ms   | 0.902 ms  | 0.013 ms     |
| terrain     | 1.735 ms   | 1.743 ms  | 0.013 ms     |
| grass       | 4.977 ms   | 5.077 ms  | 0.015 ms     |
| water       | 1.898 ms   | 2.030 ms  | 0.013 ms     |
| lights      | 0.156 ms   | 0.178 ms  | 0.013 ms     |
| particles   | 1.155 ms   | 1.194 ms  | 0.013 ms     |
| materials   | 1.476 ms   | 1.500 ms  | 0.013 ms     |
| kitchensink | 4.120 ms   | 4.161 ms  | 0.017 ms     |

Run-to-run spread on these scenes is about 0.02 ms, so cube, terrain, materials
and particles are unchanged within noise.

The resolve itself is 0.013 ms almost everywhere — it is one fullscreen
triangle. The rest of the cost is bandwidth from passes that now write 64 bits
per pixel instead of 32. Water is the clearest case: its pass goes 0.171 → 0.260
ms, because it copies the scene image to feed refraction and that copy doubled
in size. Sky did not move at all (1.594 → 1.593 ms), which says the cloud march
is ALU-bound rather than fill-bound.

## Interactions

- **MSAA colour images carry `hdrFormat`, not the swapchain format.** They are
  the scene pass's colour attachment and resolve into the HDR target. A
  framebuffer's attachments must match the formats its render pass declares, so
  getting this wrong is a `VUID-VkFramebufferCreateInfo-pAttachments-00880` at
  startup rather than anything subtle.

- **The scene passes end in `SHADER_READ_ONLY_OPTIMAL`,** not `PRESENT_SRC`,
  because the tonemap pass samples them.

- **Light shafts read linear values and always did.** `godray.frag` samples the
  refraction copy of the scene. Sampling the old sRGB image decoded to linear,
  and the half-float image is linear already, so the numbers did not move and
  its thresholds did not need retuning. What did change is the ceiling: its
  `smoothstep(0.62, 0.88, l)` window assumes `l` tops out near 1, and once
  anything in the engine emits above that, the window admits everything instead
  of selecting the sun.

- **Screenshots are unaffected.** `capture.go` reads the swapchain image, which
  is the tonemapped result.

## Not done

- Emissive materials are the only thing that emits above 1 so far. A sun disc
  written at its real intensity would be the other obvious one, and would make
  the curve do visible work in every outdoor scene rather than in one example.
- No bloom. The target it needs now exists, and so does something bright enough
  for a threshold to select: see
  [`material-maps.md`](material-maps.md) on `EmissiveStrength`.
- The curve is a branch on a push-constant rather than a pipeline variant. That
  is a fullscreen pass with a uniform branch, so it costs nothing measurable,
  but it does mean the set of curves is closed and lives in `tonemap.frag`.
