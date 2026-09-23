# 0006: Expose application graphics nodes and sampled targets

- Status: Accepted
- Recorded: 2026-09-23

## Context

A consuming game cannot allocate renderer attachments, schedule work between
engine passes, or extend descriptor bindings. Issue #99 needs those capabilities
without making the engine own application effects. The frame graph from
[0005](0005-schedule-the-frame-through-a-frame-graph.md) already owns ordering,
synchronization and render-pass descriptions. Application uniforms at light-set
binding 6 provide an established fixed binding convention.

## Decision

Expose floating-point render targets and graphics nodes at four named frame
stages. Applications supply SPIR-V, draws, push data and sampled inputs. Keep
effect policy in the consuming game. Use the existing graph compiler, cached
render passes, command scratch and deferred resource lifetime.

Application passes reuse each draw texture's existing descriptor set at set 0,
bind the engine's shared light set at set 1, and keep four input samplers at
set 2. Mesh draw counts do not allocate descriptor sets. Extend that light set with four vertex/fragment sampler
slots at bindings 7–10. Keep push constants fixed at 256 bytes: engine VP and
model matrices followed by 128 application bytes. The existing `RenderObject`
field for the model transform is `Model`.

Two-instance history alternates by frame slot. Descriptor updates affect only
the waited frame; swapchain recreation rewrites descriptors while the GPU is
idle. Fixed-size targets survive resize. Graph rebuilding retires previous
framebuffers without destroying cached render passes still used by pipelines.

Resolved scene depth is opt-in R32F and becomes a graph graphics node. Reverse-Z
MSAA resolves to the maximum sample. The legacy scene explicitly declares its
actual depth attachment exit layout so the compiler derives the transition.
Application timings use a separate reserved query region, preserving every
engine pass bracket and existing stream when no application work is registered.

## Alternatives

- Hand-inserted callbacks would bypass the graph's synchronization and lifetime
  checks and introduce a second scheduling mechanism.
- Arbitrary reflected descriptor layouts would require a separate material and
  pipeline interface. The fixed layout follows the existing `ShaderSet` seam.
- Adding application effect structures would prescribe game policy beyond the
  inaccessible Vulkan capabilities identified in [0002](0002-keep-game-ownership-outside-the-engine.md).

## Consequences

The renderer owns additional graph rebuild, descriptor and resize paths.
Shader authors must obey fixed layouts and declare sampled target dependencies.
`Reads` takes textures: target outputs, the borrowed `SceneColor()` and
`SceneDepth()` views, or ordinary loaded images. Scene outputs and target
textures carry their graph identity; ordinary textures need no graph node.
Scene colour can only be read by own-target passes after the scene, and scene
depth only after the scene. Nil inputs are rejected. Mesh textures at set 0
remain independent of pass inputs at set 2. Resource creation and graph changes
can allocate; steady draw recording cannot.

Compute scheduling, a user-facing example and higher-level effect design remain
separate work. Pipeline creation, resize failure injection, pinned streams,
allocation checks and validation exercise this boundary.

## References and evidence

- [API and shader layouts](../agents/render-targets.md)
- [Frame graph](../agents/frame-graph.md)
- `renderer/apppass_test.go`, `renderer/appbindings_test.go`,
  `renderer/appcreate_test.go`, `renderer/appresize_test.go`
- `cmd/apppasscheck`: visible control comparison, history and runtime churn
- Three existing stream hashes remain unchanged; new fixtures add 8 calls for
  scene depth and 32 for depth plus mesh/fullscreen application work.
