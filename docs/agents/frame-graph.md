---
id: frame-graph
title: Record the renderer's frame graph
summary: >
  The renderer compiles its tail into an ordered plan and owns the Vulkan
  dynamic-rendering attachment bindings, physical images/buffers and recording closures.
capability: rendering
status: experimental
api:
  - framegraph.Graph.AddBuffer
  - framegraph.Transfer
  - framegraph.BufferDesc
  - framegraph.Graph.Build
  - framegraph.Step.AfterBarriers
  - renderer.recordCommandBuffer
  - renderer.Pass
  - renderer.AppComputeDesc
  - renderer.AppPassDesc
  - renderer.RenderTargetDesc
  - renderer.SceneDepth
  - renderer.SceneColor
example: examples/09-water
run: task validate
requires:
  - cgo
  - vulkan-runtime
  - VK_KHR_dynamic_rendering
  - vulkan-sdk
assets: procedural
verified: 2026-09-24 # dynamic rendering, attachment barriers and the streamed upload node
---

# Record the renderer's frame graph

```sh
go test ./renderer/framegraph ./renderer -count=1
task validate
task syncvalidate
task determinism
```

`renderer/framegraph` compiles declarations without a device. The renderer's
`newFrameGraph` registers those declarations beside draw closures and `Pass`
mappings in `renderer/framegraph_executor.go`. The plan holds no closures or
Vulkan handles. It retains declaration order.

Clouds, both shadow passes and the main scene pass remain one hand-recorded
`Legacy` node. It declares the HDR colour and depth attachments it leaves and
the sampled shadow maps. `recordCommandBuffer` records that body using dynamic rendering,
including `PassSceneResolve`, then calls `executeGraph` for the remaining steps:
the scene-colour copy, water, nine bloom stages, the optional UI layer and its
nine glow stages, and tonemap with the composite inside the same rendering instance. A
final declaration marks the swapchain image presented; it records no command.

## Application nodes

`AppPassDesc.Reads` accepts texture pointers. Target textures map to their
current readable graph instance (the previous write for history), while
`SceneColor()` and `SceneDepth()` map to engine outputs. Ordinary textures
need no image declaration. Draw textures reuse set 0; the four pass inputs
occupy set 2 and are independent of draw count.

`CreateAppPass` adds `Graphics` nodes and `CreateAppCompute` adds `Compute`
nodes. Both interleave in creation order at a named stage:

| Stage | Position and readable scene inputs |
|---|---|
| `StageBeforeScene` | After the legacy clouds/shadows and before scene geometry; prior application outputs and history. |
| `StageAfterScene` | After the legacy scene and optional depth resolve, before water's colour copy; HDR and resolved scene depth. |
| `StageBeforeBloom` | After water; complete HDR scene, resolved depth and prior application outputs. |
| `StageBeforeTonemap` | After bloom and UI glow; HDR, resolved depth and prior application outputs. |

Compute nodes bind a compute pipeline, fallback/light/input descriptor sets,
push VP plus identity model and application data, then dispatch on the graphics
queue. They execute outside rendering instances. Sampled inputs and storage
outputs use the same target/scene identity mapping. History reads use the
previous image, while `StorageReadWrite` describes the destination when the
same target is also declared as an input; other destinations use `StorageWrite`.
The compiler derives the barriers into `General` and back to sampled layout.
A dispatch and its trailing synchronization node form one optional group, so
disabling it or setting a zero workgroup axis skips both transitions while
retaining timing edges. There is no async compute.

A legacy declaration records the application images written by the preceding
submission, including history. Storage-capable targets join possible graphics and compute
producers with preceding readers, since either kind can have written them. A pre-scene transfer-kind synchronization node
derives sampled-read visibility
for application textures used by ordinary scene draws. It copies nothing.
The hand-recorded legacy body remains unchanged when there are no application
nodes. With them, its pre-scene boundary executes graph steps after shadows
and before scene rendering begins. The engine depth-resolve node is a
fullscreen `Graphics` step immediately after the legacy scene. The legacy use
explicitly declares its depth attachment exit layout; the compiler derives
the sampling barrier and water's subsequent attachment initial layout.

Creating or destroying a pass, changing target slot dependencies, and the first
`SceneDepth()` request mark the graph dirty. After the next frame fence wait,
the renderer rebuilds the plan and Go-side attachment bindings. No render-pass
or framebuffer objects are created or retired. `CreateAppPass` also builds the
pure plan immediately to supply its pipeline's attachment formats and samples.
Enabling or disabling an existing pass needs no
rebuild: optional nodes are layout-neutral and both timing edges still execute.

Relative targets and per-swapchain resolved depth are rebuilt under the resize
undo stack; fixed targets survive. Application input sets are per frame slot
and rewritten only after its fence wait, or while the device is idle on resize.
History binds distinct read/write instances chosen by frame index. See
[render targets](render-targets.md) for the public and shader contracts.

## Buffers and generated draws

`Graph.AddBuffer(BufferDesc{Name, Size, Persistent, Imported, Usage})` returns
the same ResourceID type as AddImage. A buffer supports StorageRead,
StorageWrite, StorageReadWrite, TransferSrc, TransferDst, VertexRead and
IndirectRead. Image-only accesses on buffers and VertexRead/IndirectRead on
images are rejected. The compiler derives and ORs usage flags. Buffers have
no layout, clear, resolve or priming transition.

Transient reads require a guaranteed write. Persistent/imported contents must
be initialized by their owner; the initial synchronization scope conservatively
includes preceding submissions. Optional-group writes initialize only that
group's executed path. Joins retain both paths' hazards. Read/write and
write/write hazards emit BufferMemoryBarrier; readers are accumulated for the
next writer. A producer's visibility to vertex input does not imply visibility
to indirect fetch, even when both reads belong to the same node.

`Barrier.Buffer`, `Offset` and `Size` identify a buffer range. The current
compiler emits whole logical buffers and does not alias them. Graphics buffer
reads become explicit barriers **before** CmdBeginRendering. A stage-pair group may mix
buffer and image barriers. The executor allocates both scratch arrays from the
plan at build time and emits one CmdPipelineBarrier per adjacent stage pair.
Draw-side pinned streams retain their original calls and arguments.

An engine-owned `Transfer` node named "streamed uploads" heads every graph a
renderer builds, ahead of GPU LOD selection and the legacy body. It is optional
and runs only on a frame that has copies queued, so a program that never
streams records the command buffer it always did. It carries the batch of
staged buffer copies the asynchronous mesh and storage-buffer constructors
enqueue; see [models](models.md#streaming-geometry-in-while-frames-render).

Its declared uses are the streamed storage buffers, as `TransferDst`, and only
those a `UploadStorageBufferAsync` has actually targeted -- the first such
upload marks the graph dirty so the next rebuild declares it. The compiler
derives those barriers to the real consumers on both sides. Mesh and arena
buffers are not graph resources and cannot be without a rebuild per created
mesh, so the node records their trailing transfer-to-vertex-input group itself,
plus a leading group for destinations a frame in flight may still be reading.
The node is timed like an application pass, under the name `upload`, once the
renderer has streamed anything.

GPU LOD adds engine-owned classify/prefix/scatter nodes before shadows and
before application StageBeforeScene work. A trailing copy obtains diagnostic
counts; a synchronization step exposes bucket vertex ranges and draw arguments
to the hand-recorded shadow and scene passes. The prefix node also initializes
counts, so no workgroup assumes another workgroup has already reset them.
These nodes precede application nodes without moving application work across
its existing shadow boundary. See [ADR 0008](../adr/0008-buffer-resources-and-gpu-draw-generation.md).

## Layouts and optional work

A sampled resource rests in `ShaderReadOnlyOptimal`; storage rests in
`General`, other attachments in their attachment layouts, and the swapchain
in `PresentSrc`. `Step.Barriers` transitions each attachment into its color or
depth attachment layout before CmdBeginRendering. `Step.AfterBarriers` exposes
attachment writes and restores resting layouts after CmdEndRendering. Water
therefore transitions the copied HDR image from TransferSrc to ColorAttachment
explicitly. Resolve destinations are included in these transitions.

Each adjacent run with identical source and destination stages becomes one
CmdPipelineBarrier call. Undefined sources carry the group's source stage in
the plan. Scratch is allocated once to fit the widest image/buffer group across
entry, exit and final barriers. Final barriers run after the last step if
needed; `TestFrameGraphTailLayouts` checks that the engine tail restores all
layouts without them. Consumer scopes include all declared shader stages and
next-frame readers.

An optional node must leave every touched resource in its entry layout. The
copy and water draw satisfy that only together, so both belong to one
`OptionalGroup` and use the same runtime predicate. Bloom stages and the UI
layer are individually neutral. Every bloom level is cleared and primed at
allocation. The graph primes the copy target and UI layer's layouts without
clearing: the copy overwrites its destination and the UI node clears before
anything consumes it. Priming a layout does not initialize readable history.

At an optional join, the compiler retains both paths' reader/writer scopes.
A later writer waits for preceding readers even if the optional producer did
not run. A later sampler cannot lose an earlier storage-write hazard.

## Registering a renderer pass

Add its image uses and node in `newFrameGraph`, beside its draw closure. The
closure records draws; the executor owns barriers, dynamic-rendering begin/end and
node-boundary timer brackets. Set `begin`/`end` to the existing `Pass` value
at the appropriate boundaries. Bloom's stages share one interval, beginning
at prefilter and ending after the last upsample. Water's interval starts
before the copy; its draw body retains the separate shafts and over-water
intervals. `resolve` brackets only `CmdEndRendering`. Tonemap and composite
are adjacent intervals inside one rendering instance.

Skipping optional work still emits every timer edge. Use the same predicate
for every member of an optional group. Keep the public `Pass` enum intact;
examples and profiling consumers use it.

Create graphics pipelines using `frameGraph.pipelineFormats` for their node,
with a null RenderPass and subpass zero. `PipelineRenderingCreateInfo` supplies
color/depth formats and view mask zero; rasterization samples come from the
compiled attachment description. Load/store operations and resolves are bound
at CmdBeginRendering. `RenderPassDesc`, `AttachmentDesc` and `Step.RenderPass`
remain the public metadata names. There is no compatibility key or object cache.
Scene and water reuse pipelines because their formats and sample counts match.
`TestFrameGraphWaterFormatsMatchScene` checks both single-sample and MSAA cases.

The device requires `VK_KHR_dynamic_rendering` and its `dynamicRendering`
feature. It retains a Vulkan 1.0 API request with the complete extension
dependency closure enabled; see [ADR 0009](../adr/0009-execute-render-passes-with-dynamic-rendering.md).

## Resize and teardown

`Renderer.New` compiles the graph after allocating its tail targets. Plans,
closures and pipelines survive resize unless application declarations also
change. `bindGraphTargets` reconnects replacement images, resolves extents and
builds retained RenderingInfo/RenderingAttachmentInfo values per physical
instance. The scene-color copy remains one physical image; history still uses
frame-indexed double buffers.

`releaseGraphBindings` returns descriptor sets before image owners destroy
views, then drops Go-side rendering bindings. Shutdown and constructor failure
use `New`'s init stack; failed rebuilds use the existing scoped undo stack.
Graph rebuild no longer needs deferred framebuffer destruction. Images and
other GPU objects still retire through their existing lifetime mechanisms.

## Failure modes

- A command-stream hash change means a driver call or argument changed.
  `TestMigrationDrawStreams` independently pins draw-side arguments to the
  pre-migration baseline, excluding only rendering and barrier calls.
- A missing timer bracket leaves a reset query unwritten, making frame readback
  unavailable. SceneResolve/WaterResolve contain only CmdEndRendering.
- A missing entry/exit barrier can leave an attachment in its sampled layout
  or hide writes from a later reader. Run both core and synchronization validation.
- Stale attachment bindings or descriptors after resize name retired views.
  Failure-injection tests cover remaining allocation sites;
  `TestResizeCreatesNoRenderPassesOrFramebuffers` requires zero obsolete objects.
