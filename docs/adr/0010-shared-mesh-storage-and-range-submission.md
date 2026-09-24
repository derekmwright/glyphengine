# 0010: Shared mesh storage and range submission

- Status: Accepted
- Recorded: 2026-09-24

## Context

Issue #96 describes hundreds of distinct procedural patches, each needing its
own bounds and transform while sharing geometry storage and material state.
InstanceSet repeats one geometry; combining patches removes independent
placement and culling. Allocations and recorded draws are separate costs.

## Decision

Expose fixed-capacity MeshArena storage with immutable Mesh ranges. Each arena
owns one vertex buffer and one index buffer; each range owns allocator spans,
counts and bounds, but no Vulkan allocations. Local indices retain their chosen
16/32-bit width. All Mesh recorders carry firstIndex and vertexOffset.

Allocation uses the staged buffer uploader in one synchronous submission.
Free retires spans after frames in flight; arena destruction refuses live
unfreed ranges and queues behind retiring ranges. Shutdown releases remaining
arenas after device idle. All mutation stays on the renderer thread. Games own
partitioning, streaming, transforms, materials and replacement policy.

Measure opt-in indirect grouping of adjacent compatible ranges using the
existing instanced vertex interface. CPU-written commands use one independent
mapped slot per frame in flight and disjoint arguments for each shadow view.
Per-range culling remains independent. Enable supported Vulkan core features
only; without multiDrawIndirect or drawIndirectFirstInstance use direct indexed
draws. Respect maxDrawIndirectCount. Do not change existing streams when disabled.

The shipping rule was written before measuring: ship batching only if the
saving in CPU recording at 400 patches exceeds within-mode scatter, without
GPU cost. The measurement (models page) did not meet it at 400 and met it
clearly at 1600. Batching ships opt-in and off by default on that basis: no
stream changes while disabled, and the consumer decides at its own count.

## Alternatives

- One mesh per patch preserves granularity but cannot share allocations.
- A merged mesh removes independent transforms and bounds.
- Replacing the dynamic mesh API would mix mutable per-slot buffers with
  immutable shared storage. Changed ranges instead allocate and retire.
- Asynchronous upload requires issue #95's separate threading/upload contract.
- Compute-generated commands add work when CPU culling already supplies the
  visible list. CPU-written indirect commands suffice for this measurement.

## Consequences

Free space can fragment; arenas never compact or grow under borrowed ranges.
Uploads can stall behind prior rendering. Ordinary ranges preserve existing
materials and recorders. Batching supports plain lit opaque state and assumes
MVP = frame VP * Model; it retains the ordinary instanced pass ordering.
Other material/pipeline variants keep direct submission. The public option
does not infer chunking or terrain policy.

## References and evidence

- [Engine ownership](0002-keep-game-ownership-outside-the-engine.md)
- [Buffer generation](0008-buffer-resources-and-gpu-draw-generation.md)
- [Vulkan indexed indirect draw requirements](https://docs.vulkan.org/refpages/latest/refpages/source/vkCmdDrawIndexedIndirect.html)
- [Models API and measurements](../agents/models.md)
- `renderer/mesharena_test.go`, `renderer/meshbatch_test.go`, `task ranges`
- Existing engine stream hashes remain pinned; new ranges have an independent
  three-draw stream pin and exact GPU comparisons for both index widths.
