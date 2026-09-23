---
id: frame-graph
title: Record the renderer's frame graph
summary: >
  The renderer compiles its tail into an ordered plan and owns the Vulkan
  render pass cache, physical image bindings, framebuffers and recording closures.
capability: rendering
status: experimental
api:
  - framegraph.Graph.Build
  - framegraph.RenderPassDesc.Key
  - framegraph.Compatible
  - renderer.recordCommandBuffer
  - renderer.Pass
  - renderer.AppPassDesc
  - renderer.RenderTargetDesc
  - renderer.SceneDepth
  - renderer.SceneColor
example: examples/09-water
run: task validate
requires:
  - cgo
  - vulkan-runtime
  - vulkan-sdk
assets: procedural
verified: 2026-09-23
---

# Record the renderer's frame graph

```sh
go test ./renderer/framegraph ./renderer -count=1
task validate
task determinism
```

`renderer/framegraph` compiles declarations without a device. The renderer's
`newFrameGraph` registers those declarations beside draw closures and `Pass`
mappings in `renderer/framegraph_executor.go`. The plan holds no closures or
Vulkan handles. It retains declaration order.

Clouds, both shadow passes and the main scene pass remain one hand-recorded
`Legacy` node. It declares the HDR colour and depth attachments it leaves and
the sampled shadow maps. `recordCommandBuffer` records that body unchanged,
including `PassSceneResolve`, then calls `executeGraph` for the remaining steps:
the scene-colour copy, water, nine bloom stages, the optional UI layer and its
nine glow stages, and tonemap with the composite inside its render pass. A
final declaration marks the swapchain image presented; it records no command.

## Application nodes

`AppPassDesc.Reads` accepts texture pointers. Target textures map to their
current readable graph instance (the previous write for history), while
`SceneColor()` and `SceneDepth()` map to engine outputs. Ordinary textures
need no image declaration. Draw textures reuse set 0; the four pass inputs
occupy set 2 and are independent of draw count.

`CreateAppPass` adds `Graphics` nodes in creation order at a named stage:

| Stage | Position and readable scene inputs |
|---|---|
| `StageBeforeScene` | After the legacy clouds/shadows and before scene geometry; prior application outputs and history. |
| `StageAfterScene` | After the legacy scene and optional depth resolve, before water's colour copy; HDR and resolved scene depth. |
| `StageBeforeBloom` | After water; complete HDR scene, resolved depth and prior application outputs. |
| `StageBeforeTonemap` | After bloom and UI glow; HDR, resolved depth and prior application outputs. |

A legacy declaration records the application images written by the preceding
submission, including history. A pre-scene transfer-kind synchronization node
derives sampled-read visibility
for application textures used by ordinary scene draws. It copies nothing.
The hand-recorded legacy body remains unchanged when there are no application
nodes. With them, its pre-scene boundary executes graph steps after shadows
and before the main render pass begins. The engine depth-resolve node is a
fullscreen `Graphics` step immediately after the legacy scene. The legacy use
explicitly declares its depth attachment exit layout; the compiler derives
the sampling barrier and water's subsequent attachment initial layout.

Creating or destroying a pass, changing target slot dependencies, and the first
`SceneDepth()` request mark the graph dirty. After the next frame fence wait,
the renderer rebuilds the plan and framebuffers while retaining its render-pass
cache. Previous framebuffers retire through `DeferDestroy`. `CreateAppPass`
also builds the pure plan immediately to create its pipeline against the exact
render-pass description. Enabling or disabling an existing pass needs no
rebuild: optional nodes are layout-neutral and both timing edges still execute.

Relative targets and per-swapchain resolved depth are rebuilt under the resize
undo stack; fixed targets survive. Application input sets are per frame slot
and rewritten only after its fence wait, or while the device is idle on resize.
History binds distinct read/write instances chosen by frame index. See
[render targets](render-targets.md) for the public and shader contracts.

## Layouts and optional work

A sampled resource rests in `ShaderReadOnlyOptimal`; storage rests in
`General`, other attachments in their attachment layouts, and the swapchain
in `PresentSrc`. Attachments transition through the render pass's initial and
final layouts, never explicit image barriers. This lets the water attachment
enter from `TransferSrcOptimal` without a redundant transition after the copy.

The compiler emits explicit barriers for non-attachment uses. Each adjacent
run with identical source and destination stages becomes one
`commandScratch.pipelineBarrier` call. Undefined sources already carry the
group's source stage in the plan. Scratch is allocated once to fit the widest
group: two barriers for the scene-colour copy on the engine-only path, or the widest
application dependency group when targets are registered. Final barriers execute after
the last step if present; `TestFrameGraphTailLayoutsAndCache` requires this
tail to produce none.

An optional node must leave every touched resource in its entry layout. The
copy and water draw satisfy that only together, so both belong to one
`OptionalGroup` and use the same runtime predicate. Bloom stages and the UI
layer are individually neutral. Every bloom level is cleared and primed at
allocation. The graph primes the copy target and UI layer's layouts without
clearing: the copy overwrites its destination and the UI node clears before
anything consumes it. Priming a layout does not initialize readable history.

At an optional join, the compiler retains reader stages for subsequent
writers and separately tracks writer stages. A later sampler needs visibility
of those writers, not ordering against readers from the skipped path. This
keeps a bloom reader's incoming colour dependency sufficient without losing a
possible storage-write hazard.

## Registering a renderer pass

Add its image uses and node in `newFrameGraph`, beside its draw closure. The
closure records draws; the executor owns barriers, render-pass begin/end and
node-boundary timer brackets. Set `begin`/`end` to the existing `Pass` value
at the appropriate boundaries. Bloom's stages share one interval, beginning
at prefilter and ending after the last upsample. Water's interval starts
before the copy; its draw body retains the separate shafts and over-water
intervals. `resolve` brackets only `CmdEndRenderPass`. Tonemap and composite
are adjacent intervals inside one render pass.

Skipping optional work still emits every timer edge. Use the same predicate
for every member of an optional group. Keep the public `Pass` enum intact;
examples and profiling consumers use it.

Create pipelines against `frameGraph.renderPass` for their node. This cache
uses `RenderPassDesc.Key()` and excludes framebuffer size and resource
identity. Both bloom chains share downsample and upsample descriptions;
the legacy clouds also use the cached downsample description. Water, UI clear
and tonemap have separate descriptions. There are four cached objects with
the UI layer disabled and five with it enabled. Water supplies
`sceneEntryDependency()` and colour/depth/resolve `AttachmentOrder` explicitly;
`TestFrameGraphWaterOrderCompatibleWithLegacyScene` checks compatibility with
the actual legacy render-pass constructor.

## Resize and teardown

`Renderer.New` compiles the graph after allocating its tail targets. The current plan,
closures, cache and pipelines survive resize unless application declarations
also changed. `bindGraphTargets` reconnects
the newly allocated images and makes framebuffers per node and swapchain
image; the scene-colour copy is a single physical image. Relative extents are
evaluated again at the new size.

`rebuildSwapchainTargets` pushes an undo step for these framebuffers.
`releaseGraphFramebuffers` returns the target descriptor sets first, destroys
the graph framebuffers, then clears its bindings. The existing target owners
subsequently destroy their views and images. Both successful shutdown and
failed initialization use `New`'s init stack; failed rebuilds use the scoped
undo stack. The cache destroys each shared render pass exactly once.

## Failure modes

- A command-stream hash change means a driver call or argument changed. Diff
  the fake driver's call logs; do not replace the golden values.
- A missing timer bracket leaves a reset query unwritten, making the whole
  frame's readback unavailable, including passes that ran correctly.
- Creating a migrated render pass outside the cache bypasses its shared
  description and can silently break pipeline compatibility. The validation
  layer catches incompatible pipelines when they draw.
- A stale framebuffer or descriptor after resize names retired views. The
  framebuffer failure-injection tests check every creation site, and
  validation's provoked rebuilds exercise the real device path.
