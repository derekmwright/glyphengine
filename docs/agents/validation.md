---
id: vulkan-validation
title: Enable Vulkan validation and find resource leaks
summary: >
  Turn on the Khronos validation layer to catch API misuse, undestroyed objects
  and synchronization hazards, from code or the environment for an already-built binary.
capability: rendering
status: stable
since: v0.2.0
api:
  - glyphengine.WithValidation
  - renderer.WithValidation
  - renderer.ResourceCounts.DescriptorSets
run: task validate
requires:
  - cgo
  - vulkan-runtime
  - vulkan-sdk
assets: none
verified: 2026-09-24 # dynamic rendering, explicit barriers, resource lifetime, the trace's atlas readback
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

### Startup swapchain failures in batch gates

`task smoke` and `task validate` source the same `tools/check-example.sh`.
If a process exits nonzero with `renderer: create swapchain:
vkCreateSwapchainKHR: VkResult=...`, before any successful swapchain or renderer
initialization, the gate prints **STARTUP FAILURE (swapchain)** with the Vulkan
result and retries that example once. This is a new process, with a new window,
surface and device. The renderer itself makes no additional startup attempt.

Every attempt records its exit code and log path under a separate run directory
in `.task/smoke/` or `.task/validate/`. A retry prints both full logs, including
when it succeeds; two startup failures fail the task. A surface query, image
view allocation, mid-frame rebuild, draw failure or other startup error is not
classified as this flake. Any `VULKAN ERROR` or `VULKAN WARNING` fails either
gate without a retry, even if the same log also contains a startup error.
Validation still requires the layer to be enabled, and synchronization
validation still requires its enablement marker.

`task check:self-test` (also in `task ci`) covers the retry bound and negative
classifications. On 2026-09-24 a temporary startup return of `VkResult=-13`
was injected at the creation boundary in `01-triangle` under validation:
one failure printed the classification and then rendered two frames on
attempt 2; a persistent failure made exactly two attempts and Task exited 201.
The injection was removed. Disabling the checker's retry made its regression
check fail with one call instead of two. Measured reproduction rates and the
remaining uncertainty are in [windowing](windowing.md#intermittent-startup-swapchain-failure).

## Synchronization validation

Core validation checks API use, layouts and object lifetime, but does not
track whether the writes one draw makes are available to the next draw.
Synchronization validation adds that hazard analysis. An image can have the
right layout and still have a read-after-write or write-after-write hazard:
an explicit image layout transition is itself a write.

```sh
GLYPHENGINE_VALIDATION=1 GLYPHENGINE_SYNC_VALIDATION=1 ./mygame
task syncvalidate
```

The sync flag only takes effect when validation is requested as well. In
`renderer.New`, before `createInstance`, it sets `VK_LAYER_VALIDATE_SYNC=1`
in the process environment, then restores the previous value immediately
after instance creation. The Khronos layer reads its `validate_sync` setting
from that variable; no wrapper extension or `pNext` structure is required.
Startup logs `Vulkan synchronization validation enabled
(VK_LAYER_VALIDATE_SYNC=1)` when the layer was enabled with this request.

Verified on Windows with Vulkan SDK **1.3.268.0**,
`VK_LAYER_KHRONOS_validation` API version **1.3.268**, implementation **1**.
Its installed `Bin/VkLayer_khronos_validation.json` documents `validate_sync`.
The [Khronos setting guide](https://github.com/KhronosGroup/Vulkan-ValidationLayers/blob/main/docs/updating_from_VK_EXT_validation_features.md)
also maps synchronization validation to this environment variable. On that
SDK the separate alpha `sync_queue_submit` check defaults off; this gate uses
the documented synchronization-validation setting and the layer's defaults.

The setting was verified against a real failure before fixing it: 60 fixed
frames of `02-cube` at 1280x720, 4x MSAA reported **240** synchronization
messages versus zero with core validation alone. Outgoing dependencies fixed
the cloud/scene final-transition reads; incoming attachment-write access fixed
the cloud/tonemap DONT_CARE loads. The same run is now silent. UI glow and
water require the same visibility on their outputs.

The graph derives explicit entry/exit image barriers. They include declared
consumer shader stages and preceding readers, including optional and next-frame
uses. Scene and water pipelines share attachment formats and sample counts;
their synchronization scopes no longer participate in pipeline compatibility.

`task syncvalidate` reuses the entire `validate` matrix, including opt-in
paths, provoked swapchain rebuilds, CPU/GPU LOD replacement and indexed indirect draws, then runs
`cmd/apppasscheck -compute -buffers -churn -provoke-recreate -validate -frames 300`. Both gates
prove the log counter can detect a synthetic validation message and check
that the requested layer actually started. Every warning or error fails the
gate; no synchronization diagnostics are filtered out.

The gate was broken by removing the shared scene/water outgoing dependency
while preserving pass compatibility: `01-triangle` reported 30
`SYNC-HAZARD-READ-AFTER-WRITE` messages in 30 frames, and Task exited 201
(the checker shell exited 1). No false-positive exclusions remain.

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
  targets, and rendering bindings on every resize; a closure that captured the
  originals would destroy stale handles and leak the live ones.

Resources created *after* `New` — textures, meshes, the lazy diagnostic
triangle pipeline — are owned by the application and destroyed by `Destroy`
before the unwind.

`recreateSwapchain` cannot reuse `initStack` for its own rebuild, even though
it is the same creation-order-in-reverse-order-out shape: `initStack`'s
closures already read every swapchain-dependent field through `r`, precisely
so they find whatever the last successful rebuild left there without needing
an entry of their own, and `Destroy` unwinds `initStack` exactly once, at the
end of the renderer's life. Pushing a rebuild's own steps onto it would run
that rebuild's teardown again at that point, once per resize the renderer
ever survived. It gets its own scoped stack instead — `rebuildUndo`, pushed to
and unwound the same way, but local to one `recreateSwapchain` call and
discarded when that call returns. See the next section for what it is for.

## What lives in the descriptor pool

The renderer has exactly one `VkDescriptorPool`, sized in `createDescriptorPool`
(`renderer/texture.go`), and it is created with
`VK_DESCRIPTOR_POOL_CREATE_FREE_DESCRIPTOR_SET_BIT` so that sets can be given
back at all. Three lifetimes come out of it, and the difference is worth knowing
before adding a fourth:

- **Returned when the resource is released.** One set per `Texture`, one per
  `Material`, one per `TerrainMaterial`, two (one per frame in flight) per
  `JointBuffer`. These are what a game's own loads and releases move, they are
  the ones `ResourceCounts().DescriptorSets` counts, and until issue #82 none of
  them came back — a textured level could be reloaded 676 times and the 677th
  load failed with `out of pool memory`. `docs/agents/models.md` has that
  measurement and where each set is now freed.
- **Returned when the swapchain is rebuilt.** The HDR target's scene and
  tonemap sets, the bloom chain's (`bloomLevels` per swapchain image), the
  cloud targets', the scene-colour copy's, and the UI glow layer's own colour
  target and bloom chain. `recreateSwapchain` destroys and rebuilds all of
  these — on any rebuild, not only a resize — and each target's `destroy` frees
  its own sets. These are *not* counted in `ResourceCounts`: they belong to the
  renderer, and a number that moved when a window was dragged would be no use
  to the reload gate that watches the other kind. Before #82 they were not
  freed either: `13-ui -glow on` with `GLYPHENGINE_PROVOKE_RECREATE_FRAMES`
  every other frame died on the 16th rebuild with `allocate bloom descriptor
  sets 1: vulkan error: out of pool memory`. It survives 96 rebuilds silently
  now, as does `09-water`.
- **Returned when `InitGrass` replaces it.** The grass impostor atlas's one
  set, allocated by `InitGrass` (`docs/agents/grass.md`). Until issue #87 a
  second `InitGrass` call abandoned the whole previous atlas — images, views,
  framebuffer, render pass and pipeline, not only the set — because nothing
  checked for a previous one: `r.grassImpostor` was simply overwritten, and
  `Renderer.Destroy` only ever knew about the last generation. `replaceGrass`
  (`renderer/renderer.go`) now retires the previous generation — the atlas,
  the `GrassSystem`'s instance buffers, and the flora models `InitGrass`
  loaded, through `DestroyModel` itself — deferred past the frames in flight
  the same way `DestroyModel` defers a released `Model`. It *is* counted in
  `ResourceCounts.DescriptorSets`, one while a bake is live, zero between
  `New` and the first `InitGrass` call or after a bake failure.
- **Never returned before `vkDestroyDescriptorPool`.** The shadow pass's
  per-frame sets, allocated in `New`. It lives for the renderer's lifetime, so
  there is nothing to give back until the pool itself goes; no leak, and
  nothing to count.

If you add a set to the pool, decide which of those three it is and say so
where you allocate it. The pool is a fixed budget — `MaxSets` is 708 on the
numbers in that file today — and the only signal Vulkan gives when a lifetime
is wrong is the allocation that eventually fails, a long way from the cause.

## A failed rebuild does not draw with a half-built target

Issue #86, found while measuring the pool exhaustion above: before this fix,
`recreateSwapchain` destroyed everything it was about to replace
unconditionally, then rebuilt each piece with a bare `return err` on failure.
A step failing partway through left whatever it returned (`nil`, on every
atomic constructor here) sitting in the corresponding field, with nothing
before it torn back down either — some fields nil, others still pointing at
handles the teardown a few lines above had already destroyed. `DrawFrame`
returned that error correctly, but `app.go` has never stopped the loop on a
draw error, so the next frame read straight through whichever of those was
reached first and either panicked or corrupted a frame, depending on which
step failed.

`recreateSwapchain` now unwinds a failed rebuild the same shape `New` unwinds
a failed construction: everything the attempt created is torn down again and
the fields it touched go back to `nil`, through `rebuildSwapchainTargets`'s
`rebuildUndo` stack. `acquireImage` checks for exactly that (`r.sc == nil`)
before touching the swapchain and retries the rebuild instead of
dereferencing it, so the next `DrawFrame` either succeeds once the rebuild can
complete or returns the same wrapped error again — never a nil pointer.

Proven two ways:

- **GPU-free**, in `renderer/recreateswapchain_test.go`: a fake driver fails a
  named call (`AllocateDescriptorSets`, `CreateImage`, `CreateImageView`) on
  the Nth invocation, and the test counts every Vulkan object kind created
  against how many were destroyed.
- **On the GPU**, by wrapping the real device driver so the Nth
  `AllocateDescriptorSets` call fails regardless of how many the renderer's
  own construction already made — a temporary, reverted test-only edit; a
  smaller descriptor pool turned out not to reproduce this on its own, because
  #82 already made a normal rebuild pool-neutral (old sets are freed before
  new ones are allocated, so nothing here grows the way #82's own bug did).
  Reproduced on `13-ui -glow on`, `GLYPHENGINE_PROVOKE_RECREATE_FRAMES=2`,
  failing the first `AllocateDescriptorSets` call after construction (call
  #17; construction itself takes exactly 16):

  Before this fix, the process did not print a Go panic at all — the nil
  read crossed into the driver's own state and Windows reported a heap
  corruption rather than a clean `SIGSEGV`-shaped panic:

  ```
  BREAK-TEST: failing AllocateDescriptorSets call #17 on purpose
  glyphengine: draw error: allocate hdr descriptor sets: vulkan error: out of pool memory
  exit status 0xc0000374
  ```

  After this fix, same run, `GLYPHENGINE_VALIDATION=1` added: the error names
  the step, the very next `DrawFrame` retries the rebuild and succeeds, and
  the run finishes clean —

  ```
  BREAK-TEST: failing AllocateDescriptorSets call #17 on purpose
  glyphengine: draw error: renderer: recreate HDR targets: allocate hdr descriptor sets: vulkan error: out of pool memory
  Swapchain recreated: 1280x720
  rendered 10 frames
  Renderer destroyed
  ```

  with nothing matching `VULKAN ERROR`, `VULKAN WARNING` or `panic` anywhere
  in the log. A second run left the failure permanent (every
  `AllocateDescriptorSets` call from #17 on, three rebuilds provoked across
  12 frames) to check the deterministic case — a resource that never
  recovers, which is what the original pool exhaustion was — and every retry
  produced the identical wrapped error, every frame still rendered, and
  `Renderer destroyed` still completed with validation on and nothing
  reported. That second run is also what caught two more nil derefs this fix
  needed: `Aspect`/`Extent` (read by a game's own per-frame code, not only by
  `DrawFrame`) and `New`'s own swapchain/depth teardown closures at `Destroy`
  time, both fixed alongside this one — see the commits.

The swapchain-recreation step itself (`createSwapchain`) is not reachable by
the fake driver: it goes through
`khr_swapchain.CreateExtensionDriverFromCoreDriver`, which dereferences a real
device's function table, so a fake driver segfaults rather than returning an
error. Its own partial-failure case (a `CreateImageView` failing partway
through the per-image loop) is still fixed — the swapchain and the views made
before the failure are given back — just checked under `task validate`
instead of GPU-free.

## Dynamic rendering

Every graphics path uses VK_KHR_dynamic_rendering. Core validation checks
pipeline formats, attachment bindings and explicit layouts; synchronization
validation checks entry/exit visibility, including optional/history reuse.
There are no VkRenderPass or VkFramebuffer allocations to fail or retire.
`TestResizeCreatesNoRenderPassesOrFramebuffers` keeps counters for both as
regression traps while the resize tests inject failures into remaining resource
and descriptor allocations. Resolve timing tests require only CmdEndRendering
inside SceneResolve/WaterResolve, with exit barriers after their end timestamps.

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
- **A construction failure inside a swapchain rebuild used to panic instead of
  returning.** Fixed by issue #86 — see "A failed rebuild does not draw with a
  half-built target" above. If you see a nil dereference on the frame after a
  `renderer: recreate ...` draw error on a tree older than that fix, this is
  it.

## Running the layer and the state trace together

`GLYPHENGINE_STATE_TRACE` adds work on the validated path, so the two are worth
running together at least once after either changes. The trace's own additions
are a readback of the grass impostor atlas at load — a device wait, a staging
buffer, and two image barriers either side of a copy, for the `grassatlas=`
field — and per-frame hashing that touches no Vulkan at all.

Running them together is what found the one bug this investigation did find.
`readbackImage` copies the atlas to the host, which needs
`VK_IMAGE_USAGE_TRANSFER_SRC_BIT`, and the atlas was created without it:

```
VUID-VkImageMemoryBarrier-oldLayout-01212  newLayout TRANSFER_SRC_OPTIMAL is not compatible with ... usage flags 0x16
VUID-vkCmdCopyImageToBuffer-srcImage-00186 srcImage ... requires VK_IMAGE_USAGE_TRANSFER_SRC_BIT
VUID-VkImageMemoryBarrier-oldLayout-01212  oldLayout TRANSFER_SRC_OPTIMAL is not compatible with ... usage flags 0x16
```

Three errors, on a driver that reads the image back correctly anyway, so
`GrassImpostorAtlas` — and `08-grass -impdump`, the only way anyone checks a
bake — had shipped like that since it was written. Nothing under the layer had
ever called it. `createOffscreenColor` now takes the extra usage from the
caller that needs it, and only that caller: a usage flag can change the layout
the driver picks and the cloud targets are written and sampled every frame.

Measured after the fix: three cycles of `08-grass -timeofday 0.0 -frames 150`
under `GLYPHENGINE_VALIDATION=1` with a trace being written, six captures, zero
`VULKAN ERROR` and zero `VULKAN WARNING` lines, all six byte-identical. Before
it, the same six logs carried three errors each.

## Comparing captures across toolchains

`task determinism` and the visual gates compare PNG bytes, which is exact
within one build. Across Go toolchains it is not: Go 1.27's PNG encoder
writes different bytes than 1.26's for identical pixels, and the dependency
bump that moved this repository to 1.27 (#121) rewrote all 22 documentation
images with zero pixel differences. When a before/after comparison spans a
toolchain change, compare pixels:

```sh
go run ./cmd/pngsame old.png new.png   # exit 0 only when every pixel matches
```
