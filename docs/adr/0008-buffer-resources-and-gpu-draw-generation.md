# 0008: Buffer resources and GPU draw generation

- Status: Accepted
- Recorded: 2026-09-24

## Context

Issue #115 moves per-placement LOD selection off the CPU. Compute writes
instance data and draw arguments that graphics consumes through vertex input
and indirect fetch. Images alone cannot describe these dependencies. Issue #96
also needs a future route from shared geometry ranges to GPU-generated draws.

## Decision

Add buffers to the ordered frame graph with storage, transfer, vertex-input
and indirect access. Buffers have no layouts. The compiler derives whole-buffer
barriers and preserves all readers and optional paths. A writer stays a
producer until each consumer scope has seen it. Graphics buffer reads are
barriers before render-pass entry, not subpass dependencies: vertex input and
indirect fetch are outside attachment operations, and keeping their hazards
outside render passes preserves existing pipeline compatibility.

Expose application-owned device-local storage buffers at compute set 2,
bindings 8–11. Uploads use staging and complete on the graphics queue before
returning. History alternates two independent read/write copies by frame slot;
there is no implicit previous-copy binding. Buffers keep their sizes across
resize. Referencing passes detach immediately on destruction, and their
descriptors and buffers retire after submitted work.

Keep CPU LOD as the default from [0007](0007-cpu-selected-instance-lod.md).
GPU mode uses stable parallel compaction: classify and scan placement blocks,
scan block totals while initializing draw arguments, then scatter in placement
order. The prefix dispatch resets every counter; it replaces a separate reset
dispatch and avoids an invalid assumption about cross-workgroup execution order.
Atomic append was measured first and produced order-dependent pixel differences.

Each set owns placements, per-slot bucket ranges, indirect commands, scan
scratch, a small uniform, and counter readback. Bucket ranges share a single
buffer arena so eight levels fit within Vulkan 1.0's minimum four storage
descriptors. The graph sees the arena as one buffer; there is no memory aliasing
between graph resources. Memory aliasing remains out of scope until the graph
can prove lifetimes across optional nodes and frame slots.

Engine selection executes before shadows, ahead of application StageBeforeScene
work, which retains its existing post-shadow position. Graph-derived barriers
expose buckets to both shadow and scene draws. Use one Vulkan 1.0 indirect call
per level: indexed meshes use indexed arguments; nonindexed meshes and impostors
use nonindexed arguments. No multi-draw or indirect-count feature is required.
This is the engine's draw-generation mechanism for future shared range work
under #96; this change does not expose a public shared-mesh-range API.

## Alternatives

- CPU selection remains useful for small sets and is unchanged by default.
- Atomic compaction is smaller but failed exact capture repeatability.
- Per-level storage descriptors constrain level counts to device descriptor
  limits; disjoint ranges use a constant four storage bindings.
- Waiting for current counts before recording draws would serialize rendering.
  Steady frames use indirect arguments; diagnostic Counts waits explicitly.
- A second compute queue would require queue ownership and semaphore contracts;
  all work stays on the graphics queue.

## Consequences

GPU mode supports eight total buckets, constrained further by storage-buffer
range limits. Updating placements is synchronous; static placements pay no
per-frame copy. Counts reports the most recent submitted frame after its fence;
statistics use retired-slot estimates. Selection has a separate lodselect
timing entry without renumbering engine Pass values or consuming the sixteen
application timing slots. Three dispatches trade scratch memory and GPU work
for deterministic output and CPU cost independent of placement count.

## References and evidence

- [Frame graph](../agents/frame-graph.md), [application buffers](../agents/render-targets.md), [LOD](../agents/lod-instancing.md)
- `renderer/framegraph/buffers_test.go`, `renderer/gpulod_test.go`, `renderer/storagebuffer_test.go`
- `task lod`, `task syncvalidate`, and `cmd/apppasscheck -buffers`
- Fixed-clock forest frame 61: atomic append differed in four channel samples
  from CPU (maximum 5/255) and one between repeats (1/255); stable scatter
  produced zero differing channel samples in both comparisons.

## Addendum — 2026-09-24

[0010](0010-shared-mesh-storage-and-range-submission.md) adds shared immutable
geometry ranges. GPU LOD's existing indexed arguments now carry each mesh's
firstIndex and vertexOffset; nonindexed commands remain unchanged. Distinct
range submission is measured using CPU-written indirect commands and the
existing instance vertex layout, with independent storage per frame slot and
per shadow view. The staged uploader also accepts offset copies so an arena
allocation uploads vertices and indices in one submission. The benchmark and
shipping decision are recorded on the [models page](../agents/models.md).
