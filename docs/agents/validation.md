---
id: vulkan-validation
title: Enable Vulkan validation and find resource leaks
summary: >
  Turn on the Khronos validation layer to catch API misuse and undestroyed
  objects, either from code or from the environment for an already-built binary.
capability: rendering
status: stable
since: v0.2.0
api:
  - glyphengine.WithValidation
  - renderer.WithValidation
run: task validate
requires:
  - cgo
  - vulkan-runtime
  - vulkan-sdk
assets: none
verified: 2026-07-28
---

# Enable Vulkan validation and find resource leaks

Vulkan does almost no error checking on its own. The Khronos validation layer
does, and without it most API misuse is either silent or shows up as a corrupt
frame on somebody else's GPU.

```go
e, err := glyph.New(&game{},
	glyph.WithTitle("My Game"),
	glyph.WithValidation(true),
)
```

Or without touching the code, which also works on a binary you did not build:

```
GLYPHENGINE_VALIDATION=1 ./mygame
GLYPHENGINE_VALIDATION=1 task example:02-cube
```

The environment variable wins over `WithValidation` in both directions, so
`GLYPHENGINE_VALIDATION=0` also force-disables it.

Messages are routed through the standard `log` package, tagged `VULKAN ERROR`
or `VULKAN WARNING`, with the involved object handles printed underneath —
which is usually what turns "some image view is wrong" into "*this* image view
is wrong".

## It is off by default, and degrades rather than fails

Validation costs real frame time, and the layer ships with the **Vulkan SDK**,
not the runtime — so a player's machine does not have it. Requesting validation
where the layer is missing logs a warning and continues without it. A debug
build must not refuse to start on a machine that only has drivers.

## What it catches that nothing else does

A worked example, because it is the shape of bug this layer exists for.

The point-light shadow cube maps are sampled by the lit fragment shader on
every draw, and their descriptors declare
`VK_IMAGE_LAYOUT_DEPTH_STENCIL_READ_ONLY_OPTIMAL`. The transition into that
layout happened when the cube render pass began — but that pass is skipped
entirely unless a point light is actually casting. So any scene without a point
light sampled an image still in `VK_IMAGE_LAYOUT_UNDEFINED`, on every frame,
forever.

Undefined behavior per spec. On the development GPU it silently returned
something harmless and every example looked correct. The only symptom was six
validation errors per frame, one per cube face.

The fix was to clear the cube maps to depth 1.0 — "nothing occludes" — and
transition them at startup, so they are legal and meaningful whether or not the
pass that fills them ever runs. The general rule:

> If a resource is bound and sampled unconditionally, it must be initialized
> unconditionally. A producing pass that runs "only when needed" does not
> satisfy a consumer that reads it always.

## Leak checking is the part worth automating

The layer reports every child object still alive when `vkDestroyDevice` runs:

```
VUID-vkDestroyDevice-device-05137: ... VkPipelineLayout 0x... has not been destroyed.
```

That message is the *only* practical way a teardown bug is visible — a leaked
pipeline layout changes nothing observable until it does.

`task validate` runs every example under the layer and fails on **any**
validation message, leak or otherwise:

```
task validate
```

Every example is expected to be completely silent, so the gate is strict on
purpose. It needs a GPU and the SDK, so it is deliberately not part of
`task ci`.

## How teardown stays correct

`renderer.New` records one teardown step per resource as it creates it, and
both the failure path in `New` and `Renderer.Destroy` unwind that same stack in
reverse. There is exactly one place that knows destruction order, so it cannot
drift from creation order the way a hand-maintained reverse listing does.

A failure partway through `New` therefore destroys everything created before
it: `New` either returns a usable renderer or leaves nothing behind.

Two consequences worth knowing when adding a resource to `New`:

- **Push a teardown step immediately after the resource is created**, not
  later. A step pushed out of order unwinds out of order.
- **Read the resource through `r` inside the closure** rather than capturing
  the value. `recreateSwapchain` replaces the swapchain, depth buffer, MSAA
  targets, and framebuffers on every resize; a closure that captured the
  originals would destroy stale handles and leak the live ones.

Resources created *after* `New` — textures, meshes, the lazy diagnostic
triangle pipeline — are owned by the application and destroyed by `Destroy`
before the unwind.

## Failure modes

- **"validation requested but VK_LAYER_KHRONOS_validation is not installed".**
  Install the Vulkan SDK. The runtime alone does not include layers.
- **No validation output at all, but the layer says it is enabled.** The
  engine creates a debug messenger to capture layer output; without one the
  layer writes to a platform default (`OutputDebugString` on Windows) that a
  terminal never shows.
- **Frame rate collapses with validation on.** Expected — it is doing
  per-call state tracking. Do not benchmark with it enabled.
- **A flood of the same message every frame.** The layer does not deduplicate.
  Fix the first one; the rest are usually the same root cause.
