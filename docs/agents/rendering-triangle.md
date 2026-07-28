---
id: render-triangle
title: Verify the Vulkan stack with a diagnostic triangle
summary: >
  Draw the classic tri-color triangle using no assets, vertex buffers, or
  descriptor sets — the fastest way to prove the toolchain and GPU path work.
capability: rendering
status: stable
since: v0.1.0
api:
  - renderer.New
  - renderer.WithApplicationName
  - renderer.WithMSAASamples
  - renderer.Renderer.DrawTriangle
  - renderer.Renderer.NotifyResize
  - renderer.Renderer.Minimized
  - renderer.Renderer.Destroy
example: examples/01-triangle
run: task example:01-triangle
requires:
  - cgo
  - vulkan-runtime
assets: none
verified: 2026-07-28
---

# Verify the Vulkan stack with a diagnostic triangle

`DrawTriangle` renders one frame containing a red/green/blue triangle on a dark
background and presents it. Use it as the first thing you run on a new machine,
and as the first thing you check when rendering breaks.

```go
rend, err := renderer.New(win)
if err != nil {
	log.Fatal(err)
}
defer rend.Destroy()

for !win.ShouldClose() {
	in.Update()
	win.PollEvents()

	if win.WasResized() {
		rend.NotifyResize()
	}
	if rend.Minimized() {
		continue
	}
	if err := rend.DrawTriangle(); err != nil {
		log.Fatal(err)
	}
}
```

Full program: `examples/01-triangle`.

## Why this is the right first test

It touches nothing above core Vulkan. The vertices and their colors are baked
into `shaders/triangle.vert` and indexed with `gl_VertexIndex`, so there are:

- no vertex or index buffers
- no descriptor sets, samplers, or textures
- no push constants or uniforms
- no camera, no scene, no ECS
- no files read from disk, and therefore no working-directory assumptions

If it draws, then CGo, your C compiler, the Vulkan loader, the GPU driver, GLFW,
and the whole instance → device → swapchain → render pass → pipeline → command
buffer → submit → present chain are all working. If it does not, the fault is in
that chain and not in anything the scene renderer layers on top.

## This is a diagnostic, not a drawing API

`DrawTriangle` submits and presents an entire frame by itself. Do not call it in
the same loop as `DrawFrame` — both acquire a swapchain image and advance the
frame index. Real rendering goes through `DrawFrame`.

The pipeline is created lazily on first call and destroyed by `Renderer.Destroy`,
so programs that never call it pay nothing.

## Depth is disabled here on purpose

The engine renders with **reverse-Z**: depth clears to `0.0` and compares with
`CompareOpGreater`. `triangle.vert` emits `gl_Position.z = 0.0`, which loses
that comparison against the cleared buffer.

So the diagnostic pipeline sets `DepthTestEnable: false` and
`DepthWriteEnable: false`. Without that the triangle draws nothing at all, with
no error and no validation message — exactly the confusing failure a smoke test
must not have.

This is the single most common way to get a silent black screen in this engine.
Geometry authored for a conventional `0.0 → 1.0` depth range will be discarded.

## Headless / CI use

The example accepts `-frames N` to render a fixed number of frames and exit,
which makes it usable as a smoke test:

```
task smoke                              # 60 frames, then exit
go run ./01-triangle -frames 240        # from examples/
```

A non-zero exit means the Vulkan path failed. This runs on a software
rasterizer (Mesa lavapipe) as well as real hardware, so it works on GPU-less CI
runners.
