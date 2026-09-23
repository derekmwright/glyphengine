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
group: two barriers for the scene-colour copy. Final barriers execute after
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

`Renderer.New` compiles the graph after allocating its tail targets. The plan,
closures, cache and pipelines survive resize. `bindGraphTargets` reconnects
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
