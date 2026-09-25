---
id: models
title: Treat a loaded model as geometry, not only as a draw call
summary: >
  LoadGLTF keeps the material name, the decoded vertices, and the glTF scene
  graph, so a model can be identified, measured, merged into one mesh,
  validated at load, or asked where a named point on it is, rather than only
  drawn.
capability: rendering
status: stable
since: v0.5.0
api:
  - renderer.Model
  - renderer.ModelMesh
  - renderer.ModelNode
  - renderer.ModelLight
  - renderer.ModelLightKind
  - renderer.LightKindPoint
  - renderer.LightKindSpot
  - renderer.LightKindDirectional
  - renderer.AlphaMode
  - renderer.AlphaModeOpaque
  - renderer.AlphaModeMask
  - renderer.AlphaModeBlend
  - renderer.Model.Bounds
  - renderer.Model.ReleaseGeometry
  - renderer.Model.Node
  - renderer.Model.NodeInMeshSpace
  - renderer.Model.NodeMeshes
  - renderer.Model.MeshInstances
  - renderer.Model.LightWorldPosDir
  - renderer.Renderer.CombineModel
  - renderer.Renderer.LoadGLTF
  - renderer.Renderer.LoadGLTFSkinned
  - renderer.ReadGLTF
  - renderer.ReadGLTFSkinned
  - renderer.Renderer.DestroyModel
  - renderer.Renderer.DestroySkinnedModel
  - renderer.Renderer.DestroyTexture
  - renderer.Renderer.DestroyMaterial
  - renderer.Renderer.DestroyJointBuffer
  - renderer.ResourceCounts
  - renderer.Renderer.ResourceCounts
  - renderer.ResourceCounts.DescriptorSets
  - renderer.ResourceCounts.InstanceSets
  - renderer.ResourceCounts.LODSets
  - renderer.ResourceCounts.ImpostorAtlases
  - renderer.MeshArenaDesc
  - renderer.MeshArena
  - renderer.MeshArena.Alloc
  - renderer.MeshArena.AllocAsync
  - renderer.MeshArena.Free
  - renderer.MeshArena.Stats
  - renderer.Renderer.CreateMeshArena
  - renderer.Renderer.DestroyMeshArena
  - renderer.Renderer.SetMeshRangeBatching
  - renderer.Renderer.CreateIndexedMeshAsync
  - renderer.Renderer.CreateIndexedMesh32Async
  - renderer.Renderer.UploadStorageBufferAsync
  - renderer.UploadTicket
  - renderer.UploadTicket.Ready
  - renderer.RenderStats.UploadsSkipped
  - renderer.ResourceCounts.PendingUploads
  - renderer.ResourceCounts.MeshArenas
  - renderer.ResourceCounts.MeshRanges
example: examples/08-grass
run: task example:08-grass
requires:
  - cgo
  - vulkan-runtime
assets: bundled
verified: 2026-09-25 # texture decode borrows the PNG buffer (#135)
---

# Treat a loaded model as geometry, not only as a draw call

## Shared geometry storage and independent draw ranges

Distinct chunks can share one device-local vertex buffer and one index buffer
while retaining an ordinary `*Mesh` for every range:

```go
arena, err := r.CreateMeshArena(renderer.MeshArenaDesc{
    Name: "world chunks", Vertices: 500_000, Indices: 3_000_000, Index32: true,
})
if err != nil { return err }
mesh, err := arena.Alloc(vertices, indices) // []Vertex, []uint32
if err != nil { return err }
// Use mesh in RenderObject.Mesh or MeshRef, with this chunk's own transform.
usedVertices, usedIndices, ranges := arena.Stats()
_, _, _ = usedVertices, usedIndices, ranges
```

Capacity is fixed and measured in vertices/indices. Input indices are local to
the supplied vertices. `Index32: false` stores uint16 indices and rejects any
local index above 65535; an arena can still contain more than 65536 vertices,
because every draw carries its own base vertex. Bounds are computed from each
range's vertices. `firstIndex` and base vertex are carried through ordinary,
shadow, translucent, instanced, CPU/GPU LOD, application, overlay and bake
recorders. Camera-relative placement stays in the game's transform: subtract a
large camera origin in high precision before converting to float32.

Ranges are **immutable**. A changed patch is a new `Alloc` and a `Free` of the
old mesh after replacing all its draws. `UpdateMeshData` rejects ranges; the
existing dynamic mesh API is separate. Allocation is first-fit with coalescing:
fragmented free space may be too small even when aggregate capacity would fit.
Reserve headroom for replacements while old ranges are still in flight.
Failures name the arena, and a failed allocation returns its reserved spans.

All arena operations require the renderer thread. `Alloc` uses one staging
buffer and one graphics-queue submission for both slices, with transfer
barriers and synchronous completion. It waits for earlier graphics work too.
Do not build a per-frame streaming loop on this upload: use `AllocAsync` and
the [streamed uploads](#streaming-geometry-in-while-frames-render) below. The
measured cost of one 1089-vertex, 6144-index patch is recorded with the
benchmark below.

Remove all draws borrowing a range before `arena.Free(mesh)` (or
`r.DestroyMesh(mesh)`). Reuse waits out all frames in flight, and `Stats` includes
retiring ranges until then. `r.DestroyMeshArena(arena)` panics with the arena's
name if any range has not been freed; after all frees are queued, destruction
may be queued immediately behind them. Both release operations are idempotent.
`ResourceCounts.MeshArenas` and `.MeshRanges` include deferred resources;
`.Meshes` continues to count standalone meshes. Renderer shutdown also sweeps
arenas that the application did not release explicitly.

### Measuring distinct geometry submission

`task bench -- -scene patches -repeat 4 -json .task/patches.json` runs 100,
400 and 1600 distinct patches of 2048 triangles each. At each size it interleaves
separate meshes, arena ranges and indirect batches, retaining every sample.
The scene uses a grid, camera-relative transforms, fixed 16.667 ms time,
1280x720 MSAA4, and disables patch shadow casting to isolate geometry
submission. Draw counts include any other scene geometry and are reported
separately from allocated patches. Geometry bytes and buffer counts exclude
transient upload staging and the renderer's common resources.

The rule set before measuring: ship batching only if at 400 patches it saves
more CPU recording time than the within-mode run range and does not increase
GPU time outside the measured scatter. CPU total includes waits; recording and
GPU total remain separate metrics.

Measured 2026-09-24 on Windows 11 / RX 7900 XTX / Go 1.27 at commit `daff3b7`
plus this change, four interleaved trials per mode and size, 200 frames each,
nothing else on the GPU (the bench refuses to sample while another engine
process is running). Draws include the sky and cloud passes' own geometry.

| N | Mode | Draws | CPU record ms mean (min..max) | GPU total ms mean (min..max) | CPU total ms mean | Alloc ms/patch mean | Geometry buffers |
|---:|---|---:|---|---|---:|---:|---:|
| 100 | separate | 101 | 0.026 (0.013..0.043) | 0.201 (0.196..0.207) | 16.441 | 2.014 | 200 |
| 100 | ranges | 101 | 0.058 (0.023..0.093) | 0.190 (0.186..0.195) | 16.432 | 0.712 | 2 |
| 100 | indirect | 2 | 0.041 (0.015..0.070) | 0.186 (0.183..0.192) | 16.441 | 0.698 | 2 |
| 400 | separate | 401 | 0.316 (0.061..0.556) | 0.604 (0.603..0.605) | 16.442 | 1.876 | 800 |
| 400 | ranges | 401 | 0.157 (0.076..0.234) | 0.579 (0.577..0.582) | 16.439 | 0.666 | 2 |
| 400 | indirect | 2 | 0.050 (0.008..0.107) | 0.583 (0.583..0.583) | 16.441 | 0.735 | 2 |
| 1600 | separate | 1601 | 1.399 (1.337..1.460) | 1.557 (1.556..1.558) | 16.465 | 1.993 | 3200 |
| 1600 | ranges | 1601 | 1.210 (1.093..1.279) | 1.294 (1.291..1.297) | 16.434 | 0.640 | 2 |
| 1600 | indirect | 2 | 0.046 (0.000..0.081) | 1.286 (1.285..1.287) | 16.439 | 0.636 | 2 |

CPU total is the fixed 16.667 ms frame minus present overhead in every row:
the frame is GPU-wait bound, so recording cost only matters once it exceeds
the wait. One `Alloc` of a 1089-vertex, 6144-index patch costs about 0.65 ms
against about 1.9 ms for a separate `CreateIndexedMesh32`, because one staging
buffer and one submission carry both slices instead of two of each.

The rule's outcome, applied as written: **not met at 400, met at 1600.** At
400 patches the batch saves 0.27 ms of recording against separate meshes and
0.11 ms against plain ranges, but the separate-mode recording time itself
ranged over 0.50 ms across four trials, so the saving is inside the scatter of
the baseline. At 1600 the batch saves 1.35 ms against separate meshes and
1.16 ms against ranges, both well outside the 0.12 to 0.19 ms within-mode
ranges, with GPU time unchanged against ranges (1.286 versus 1.294 ms). GPU
time never rose in any row. Batching therefore ships **opt-in and off by
default** rather than as the recommended path: at the consumer's count today
it is not distinguishable from noise, and a game that grows toward a few
thousand ranges can turn it on and measure. Ranges alone, without batching,
cut GPU time by 4 % at 400 and 17 % at 1600 against separate meshes; the
driver's cost of switching vertex and index buffers per draw is the likely
reason, and that saving comes free with the arena.

Batching is opt-in with `r.SetMeshRangeBatching(true)`. It groups adjacent
plain-lit, opaque ranges from one arena with identical texture, tint and
material factors, using the existing instance vertex layout for each model
matrix. `MVP` must correspond to `SceneLighting.VP * Model`. Per-range bounds
still select each shadow view independently. Skinning, PBR material maps,
terrain splat materials, water, translucency and application passes retain
their existing draw paths. As with ordinary instancing, batched geometry is
recorded after individual opaque geometry; do not rely on coplanar draw order.
The instance shader evaluates VP * (Model * vertex), while individual draws use
a CPU-combined MVP. Exact equality is tested on the three-range fixture below;
it is not a promise for arbitrary matrices. The 400-patch frame-30 grid differs
at 29 of 921600 pixels between direct and indirect, maximum channel 6/255,
mean absolute channel difference 0.00001266/255.

Mapped transforms and indirect arguments have separate storage for every frame
in flight, written only after that slot's fence. Each shadow view gets disjoint
arguments. Multi-draw requires both `multiDrawIndirect` and
`drawIndirectFirstInstance`; otherwise the same prepared ranges use direct
indexed draws. Calls split at `maxDrawIndirectCount`. No indirect-count
extension or compute shader is needed for CPU-written commands.

`task ranges` compares three distinct shapes and independent transforms against
separate meshes, with both index widths, repeated allocation/free while frames
are in flight, repeated indirect captures and a missing-middle visibility
control. The control changes 7200 pixels (floor 1000); all equivalent captures
have identical RGBA bytes. Unit tests pin the indexed offsets and exercise
fragmentation, overflow, creation/upload failure unwind, deferred reuse,
material boundaries, fallback and per-view command storage. The balance
meta-check detects an omitted destroy for every new resource kind.
Ordinary instancing and both CPU/GPU LOD also match that fixture exactly under
synchronization validation. The benchmark itself requires at least 1000 pixels
to differ from the background by 20/255; reversing its winding was verified to
fail with zero visible pixels, despite reporting nonzero draw/triangle counts.

See [shared mesh storage](../adr/0010-shared-mesh-storage-and-range-submission.md)
for ownership and [instancing](instancing.md) for repeated geometry.

## Streaming geometry in while frames render

Every synchronous device-local upload ends in a `vkQueueWaitIdle` on the
graphics queue, and an indexed mesh goes through it twice -- once for its
vertices, once for its indices. A game that generates terrain on a worker and
publishes patches as they arrive pays that idle per buffer, which is why the
reported consumer avoided the device-local path altogether and kept
host-visible copies per frame in flight instead.

The asynchronous constructors return before the copy runs:

```go
mesh, ticket, err := r.CreateIndexedMesh32Async(vertices, indices) // []Vertex, []uint32
if err != nil { return err }
// ... later, on any frame:
if ticket.Ready() {
    // the device-local buffers hold the data
}
```

| Synchronous | Asynchronous |
|---|---|
| `CreateIndexedMesh` | `CreateIndexedMeshAsync` |
| `CreateIndexedMesh32` | `CreateIndexedMesh32Async` |
| `MeshArena.Alloc` | `MeshArena.AllocAsync` |
| `UploadStorageBuffer` | `UploadStorageBufferAsync` |

Each one allocates its destination, copies the caller's bytes into a staging
buffer immediately -- so the slices may be reused or dropped on return, exactly
as with the synchronous path -- and queues the copy. The next `DrawFrame`, after
its fence wait, records every queued copy into that frame's command buffer as
one batch, before shadows, GPU LOD selection or any scene work. The staging
buffers are released, and the tickets become ready, when that frame's fence is
next waited on. Nothing waits for a queue.

The synchronous constructors are unchanged and remain the right choice for
load-time geometry, where waiting is free and a ticket is one more thing to
carry.

### Measuring streamed uploads

`task bench -- -scene stream -repeat 3 -json .task/stream.json` publishes 400
patches of 2048 triangles, two per rendered frame, over 300 frames at 1280x720
MSAA4 under a fixed 16.667 ms clock, and interleaves the three paths
A/B/C/A/B/C with every sample retained. All three end up drawing the same 401
draws. The bench refuses to sample while another engine process is running.

Measured 2026-09-24 on Windows 11 / RX 7900 XTX / Go 1.27 at commit `a2c4b44`
plus this change, three interleaved trials per mode, nothing else on the GPU.
"Upload CPU" is wall time inside the constructor call, divided by every frame
of the run; "submit CPU" is the engine's own `cpu_submit` phase, which is where
claiming the batch, retiring staging and the skip filter land.

| Mode | Upload CPU ms/frame | Submit CPU ms/frame | Frame max ms | Frame p99 ms | Frame median ms | GPU total ms | GPU upload ms | Buffers |
|---|---|---|---|---|---|---|---|---:|
| synchronous | 9.630 (9.550..9.679) | 0.206 | 27.639 (27.417..27.825) | 18.479 (17.985..18.784) | 16.656 | 0.449 (0.446..0.454) | — | 804 |
| dynamic | 1.471 (1.341..1.710) | 0.520 | 18.370 (17.913..19.191) | 17.868 (17.664..18.239) | 16.661 | 0.907 (0.824..1.055) | — | 1608 |
| asynchronous | 1.377 (1.327..1.465) | 0.544 | 18.820 (17.914..20.419) | 17.886 (17.689..18.143) | 16.657 | 0.450 (0.449..0.451) | 0.007 | 804 |

Per patch that is 7.22 ms synchronous, 1.10 ms dynamic and 1.03 ms
asynchronous. The synchronous figure is the one worth looking at twice: the
same `CreateIndexedMesh32` costs about 1.9 ms per patch in the benchmark above,
where every patch is built in `Init` before a frame has ever been submitted.
Publishing *while frames render* is what makes it 7.2 ms, because the queue it
waits to drain now has a frame in it. That is the cost issue #95 reported, and
it is why a number taken at load time understates it by nearly four times.

**The claim the run supports.** The batch removes the per-mesh stall:

- Upload CPU falls from 9.630 to 1.377 ms per frame, a saving of 8.25 ms
  against within-mode ranges of 0.13 and 0.14 ms. What is left is the staging
  allocation and the host copy, which is what the dynamic path pays too
  (1.471 ms).
- Frame-time **maximum** separates cleanly: the synchronous runs never came in
  under 27.4 ms and the other two never went over 20.5 ms. The median is
  16.66 ms in every row -- the frame is vsync-paced, so the mean frame time
  says nothing at all here -- and the p99 differs by under 0.6 ms, inside the
  scatter. The maximum is the only frame-time statistic that separates them,
  and it does not separate asynchronous from dynamic.
- GPU cost is the static path's, not the workaround's: 0.450 against 0.449 ms
  for synchronous and 0.907 ms for dynamic, whose host-visible buffers the GPU
  reads over the bus every frame. The batch's own transfer bracket is 0.007 ms.
- Memory is the static path's too: 804 buffers against the dynamic path's 1608,
  because that path keeps a vertex and index buffer per frame in flight.
- `PendingUploads` is 0 at the end of every run, and 800 draws were held back
  over the 400 patches -- exactly two frames each, which is the frames in
  flight and therefore the designed latency, not a backlog.

So the asynchronous path costs what the dynamic workaround costs on the CPU,
what the synchronous path costs on the GPU and in memory, and removes the
27 ms frames. The one thing it does not do is beat the dynamic path on
frame-time spikes; on this workload the two are indistinguishable there.

### A mesh is not drawn until its upload lands

`Ready` exists so a game can publish geometry on its own terms, but forgetting
to check it is not a correctness problem. The renderer drops any draw whose
mesh is still being copied into, before the draw list reaches the water split,
LOD selection, range batching or the recorder, and counts the drop in
`Stats().UploadsSkipped`. A late patch is a visibly missing patch for a frame
or two, never geometry read out of a buffer mid-copy.

The rule covers every mesh the draw would make the GPU read, not only
`RenderObject.Mesh`: an `InstanceSet` is dropped while the mesh it binds is
uploading, and an `InstanceSetLOD` while **any** of its levels is, because
which level gets bound is decided after the list is filtered and a camera that
moved could pick the one still in flight.

A steady nonzero `UploadsSkipped` means patches are being published faster than
they can land; a spike right after a burst of publishing is the normal shape.
The check costs one integer comparison on a frame with nothing in flight, and
the draw lists are handed back untouched.

A storage buffer has no draw to skip, so `UploadStorageBufferAsync` gives that
guarantee only through its ticket: a compute pass reading the buffer will run
whether or not the new contents have arrived.

### Threading

All of it is on the renderer thread, exactly like every other constructor here.
The worker generates, and hands the finished slices over through a channel; the
renderer thread receives them and calls the constructor. What the asynchronous
path removes is the *stall*, not the thread affinity -- see
[game loop](game-loop.md) for where in a frame that handover belongs.

`Ready` is likewise a renderer-thread read. It is a plain bool behind a pointer,
written when the batch retires, with no synchronisation of its own.

### Retirement and cancellation

`ResourceCounts.PendingUploads` is the staging the uploader is holding: copies
enqueued, recorded, or waiting out the fence of the submission that carried
them. It returns to zero a few frames after the last publish, and a streaming
game's steady value is roughly the publish rate times the frames in flight.

`DestroyMesh` and `MeshArena.Free` accept a mesh whose upload has not landed. A
copy that is still only queued is dropped and its staging returned at once; one
already recorded into a command buffer that will run keeps its destination
alive behind the same deferred countdown every other retirement uses, so the
copy always has somewhere to land.

### Barriers

The batch is one engine-owned transfer node at the head of the frame graph. Its
copies sit between at most two barrier groups, one `CmdPipelineBarrier` each,
rather than the per-buffer submit-and-idle the synchronous path uses:

- A **trailing** group makes the writes visible to vertex attribute and index
  reads later in the same command buffer. Every mesh destination is in it.
- A **leading** group is emitted only for destinations a frame still in flight
  may be reading -- an arena range beside ranges being drawn, or a storage
  buffer. A freshly created buffer has no reader to wait for, so a batch of
  `CreateIndexedMesh32Async` calls records exactly one barrier group.

Streamed storage buffers are frame-graph resources, so they are handled the
other way round: the node declares `TransferDst` on them and the compiler
derives the barriers to whatever actually reads them, on both sides. See
[frame graph](frame-graph.md). Mesh and arena buffers are not declared in the
graph and cannot be without rebuilding the plan per created mesh, which is why
the node emits their group itself.

The first asynchronous upload of a renderer's life marks the graph dirty, so
the node's timer bracket and any streamed storage buffer's declaration appear
in the next rebuild. That rebuild happens before the frame that records the
copy, so even the first upload is recorded in the frame after its enqueue. A
program that never streams records exactly the command buffer it always did:
`TestAppPassStreams` and `TestAppComputeStreams` pin that at the level of
driver calls and their arguments.

## Loaded model geometry

`LoadGLTF` returns a `*Model` whose `Meshes` are one `ModelMesh` per primitive.
An exporter splits a model by material, so one structure typically arrives as
several.

Each `ModelMesh` carries how the primitive is drawn — `Texture`, `BaseColor`,
`Metallic`, `Roughness`, `Material` — plus two things that say what it *is*:

```go
model, err := r.LoadGLTF(assets, "models/habitat.glb")

for _, mm := range model.Meshes {
    mm.Name              // the glTF material's name, "" if unnamed
    mm.Verts, mm.Idx     // the decoded geometry, in the engine's winding
}
```

## Identifying a primitive

`Name` is the glTF material's name. It is how a game finds the primitive it
needs to drive from the simulation — a charge strip that fills, a lamp that
follows state:

```go
for i := range model.Meshes {
    if model.Meshes[i].Name == "Charge_Runtime_1" {
        // ...
    }
}
```

**The alternative is matching on appearance, and it does not hold up.** With no
name, base colour is the only handle, and it needs a float tolerance to survive
the exporter's round trip. A tolerance is what makes the scheme fragile: nothing
in the modelling tool says a colour is load-bearing, it cannot be grepped, every
driven part in the game needs a globally distinct colour because the RGB cube is
the only namespace, and a model re-exported with a marker merged away loads
without complaint and simply never lights up. That last one is not
hypothetical — it happened, and the recovery was scanning a stale binary for
glTF headers.

Set on every primitive, skinned or not.

## Alpha: glass, foliage and cutout materials

`AlphaMode`, `AlphaCutoff` and `BaseAlpha` surface glTF's `material.alphaMode`,
`alphaCutoff` and the base colour factor's alpha component as data. The engine
takes no action on any of them:

```go
for i := range model.Meshes {
    mm := model.Meshes[i]
    if mm.AlphaMode == renderer.AlphaModeBlend {
        e.C.Translucent.Set(ent, &glyph.Translucent{Alpha: mm.BaseAlpha})
    }
}
```

`examples/22-level` does exactly this in `spawnPrimitive`: a `BLEND` material
gets [`Translucent`](translucency.md), with `BaseAlpha` as its opacity, and
its `DoubleSided` gets the matching component too -- a glass box that culled
its back faces would leave nothing where the far wall should be. The built-in
level's three materials are all plain opaque PBR, so that branch never fires
for it; it fires on a real Blender export with a glass object (Principled
BSDF alpha < 1 exports `alphaMode: "BLEND"`), verified against one -- see
"What this is checked against" below.

Verified with a real Blender 5.0.1 export: an opaque red wall behind a pale
blue glass box (Principled BSDF alpha 0.3), loaded with `-level` and rendered
under `GLYPHENGINE_FIXED_FRAME_TIME`. A pixel behind the glass reads
`R176 G94 B80` with the glass present and `R241 G128 B106` in a control
render of the identical scene with the glass object removed -- the wall is
genuinely visible and tinted through the glass (about 27% darker, shifted
away from red), not merely painted over or left invisible. Two points just
outside the glass's footprint read byte-identical between the two renders
(`R244 G130 B108` and `R206 G109 B90` in both), which is what confirms the
difference at the covered pixel is the glass's blend and not a difference
between the two scenes or renders.

`BaseColor` stays `[3]float32`: widening it to carry alpha would break every
existing caller that already treats it as three floats, which is why alpha
rides on its own field instead. `BaseAlpha` is 1 and `AlphaMode` is
`AlphaModeOpaque` when a primitive has no material at all, the same defaults
glTF itself uses for an absent material.

**`AlphaModeMask` is reported but not honoured.** There is no alpha-tested
(cutout) path in any lit pipeline today -- `lit.frag` and its variants have no
`discard`, no matter what `AlphaCutoff` says. A `MASK` primitive draws exactly
like an opaque one; building a cutout pipeline is a separate feature this does
not attempt. Assigning `Translucent` to a `MASK` mesh would be the wrong
engine feature for it (cutout wants a hard edge and casts a shadow shaped by
the cutout, not a blended fade), so a game that needs cutout foliage has
nothing to reach for here yet.

## Finding a point on the model

`Name` identifies a *primitive* — something with geometry. An artist also
marks points that carry no geometry at all: an empty node named e.g.
`Socket_Lamp` for a flare stack's muzzle, a doorway, a turret mount. `LoadGLTF`
and `LoadGLTFSkinned` keep the whole glTF node graph in `Model.Nodes`, indexed
the same way `doc.Nodes` is — `Nodes[i]` came from `doc.Nodes[i]` — so a socket
found here is the same node number Blender or another glTF tool shows.

`Model.Node` looks one up by exact name, first match, no fuzzy matching and no
baked-in naming convention — what a name means is the game's business:

```go
model, err := r.LoadGLTF(assets, "structures/flare_stack.glb")
if err != nil {
    return err
}

socket, ok := model.Node("Socket_Lamp")
if !ok {
    return fmt.Errorf("flare_stack.glb: no Socket_Lamp node")
}

// Which local axis means "aim" is between the game and its artist. -Z is the
// one glTF itself uses for cameras and punctual lights (an ASSET faces +Z, so
// do not read this as "glTF forward"). Transforming it by World gives the
// aim, which a hand-measured Vec3 could never carry.
pos := socket.World.Mul4x1(mgl32.Vec4{0, 0, 0, 1}).Vec3()
dir := socket.World.Mul4x1(mgl32.Vec4{0, 0, -1, 0}).Vec3()

scene.SetSpotLights([]glyphengine.SpotLight{{
    Pos:   pos,
    Dir:   dir,
    Range: 8,
    Color: mgl32.Vec3{1, 0.6, 0.2},
    Inner: mgl32.DegToRad(15),
    Outer: mgl32.DegToRad(30),
}})
```

`ModelNode` also carries `Translation`/`Rotation`/`Scale` as glTF authored them
and `Local` (the transform in the parent's space), in case a caller wants the
node's own numbers rather than its world placement.

## A node's own data: `extras`

glTF's `extras` is the format's own place for application data an editor
attaches to a node — a designer tagging one `{"collider": "box", "static":
true}` — and `ModelNode.Extras` surfaces it as raw JSON, `nil` when the node
has none:

```go
node, _ := model.Node("Building_04")

var tags struct {
    Static   bool   `json:"static"`
    Collider string `json:"collider"`
}
if node.Extras != nil {
    if err := json.Unmarshal(node.Extras, &tags); err != nil {
        // a malformed extras block on one node, not a reason to fail the load
    }
}
```

The engine decodes it and stops: it never looks inside the JSON (AGENTS.md
rule 14, "unblock a path, do not ship an opinion") — what the keys mean, and
which ones a game bothers to read, is entirely the game's vocabulary. See
"Loading a level" below for the pattern this exists for.

## Node space

`ModelNode.World` is in the glTF scene's space — the space the node graph
itself is in. That is **not automatically the space `ModelMesh.Verts` is in**.
`LoadGLTF` reads `doc.Meshes` directly and never applies a node's transform to
the vertices it decodes; a mesh's vertices are in its instancing node's world
space only when that node's transform happens to be identity.

A model with one node per mesh and identity TRS throughout — the ordinary
shape of a small hand-authored structure — is always that case. glTF does not
guarantee it in general, though, so using `socket.World` directly to place
something relative to a mesh is a trap for the model that is not.
`Model.NodeInMeshSpace` is the general answer: it
inverts the mesh's owning node's `World` before composing the socket's, so the
result is correct whether or not that node was identity:

```go
mm := model.Meshes[i] // the primitive the socket is placed relative to
local := model.NodeInMeshSpace(socket, mm)
```

`ModelMesh.Node` is how `NodeInMeshSpace` finds the mesh's owning node: the
index into `Model.Nodes` of the first node (in glTF node order) that
instances the doc mesh the primitive split from, or `-1` if no node does. A
doc mesh can be instanced by several nodes or by none — "first" is enough
because a primitive is drawn once regardless of how many nodes point at it.

## Measuring one

```go
if min, max, ok := model.Bounds(); ok {
    height := max[1] - min[1]
}
```

A box rather than the sphere `Mesh.BoundCenter` and `Mesh.BoundRadius` already
carry: a sphere is what a frustum test wants and the wrong shape for asking how
tall something is, which is the question that comes up when seating a model on
the ground.

`ok` distinguishes **cannot answer** from **the model is flat**. Zero height is
a real answer for a plane and a wrong one for a model whose geometry is
unavailable, and a caller that acts on the height needs to tell them apart.

## Merging one

```go
ghost, err := r.CombineModel(model)
```

One mesh from every primitive, indices offset, each primitive's `BaseColor`
baked into its vertices.

This exists for the translucent placement preview. Drawn per primitive, a
preview blends against *itself* — denser wherever the structure overlaps — and
needs N entities spawned and despawned every time the selection moves. Merged,
it is one draw, one entity, one silhouette.

The merged mesh has no material of its own: per-primitive textures and maps are
dropped. That is right for a silhouette and wrong for drawing the model
normally, which is why this is a helper rather than something `LoadGLTF` does.

`Nodes` and `ModelMesh.Node` play no part in this. A combined mesh routinely
comes from primitives that came from different nodes — that is the reason a
game reaches for this, to draw several as one — and the merge concatenates
their raw vertices exactly as decoded, the same mesh-local space `LoadGLTF`
has always drawn in. Picking one primitive's node transform to apply to the
merged whole would privilege that primitive over its siblings for no
defensible reason.

## Reading a model with no GPU

`ReadGLTF` returns the same `*Model` `LoadGLTF` does, with every GPU handle
left nil and no device involved:

```go
model, err := renderer.ReadGLTF(os.DirFS("levels"), "town.glb")
```

`LoadGLTF` **is** this read followed by an upload of what it produced, so the
two cannot drift: there is one decode, and the GPU path walks its output.

This exists for the callers that need a level's *data* and not its pixels: a
dedicated server that wants the same colliders and spawn points its clients
have, from the same file; a tool (a navmesh baker, a level validator that
fails CI on a sheared or untagged node); and a game's own level-loading tests,
which is how `TestLevelGLTFToSceneCollidersWithoutDevice` in the root package
takes `renderer/testdata/blender/level.glb` all the way to `Scene.Raycast`
with no Vulkan anywhere.

**What a read `Model` carries:**

| | |
|---|---|
| `Nodes` | the whole scene graph, including `Extras` |
| `Lights` | every `KHR_lights_punctual` light |
| `Meshes[i].Verts` / `.Idx` | the decoded geometry, byte for byte what `LoadGLTF` retains — UVs with `KHR_texture_transform` already baked, winding already reversed |
| `Meshes[i]` factors | `Name`, `BaseColor`, `Metallic`, `Roughness`, `DoubleSided`, `AlphaMode`, `AlphaCutoff`, `BaseAlpha`, `Node`, `DocMesh` |

**What it does not:** `Mesh`, `Texture` and `Material` are nil.

**It does not decode images.** Not the pixels, and not the bytes — an external
image file that is missing, or present and corrupt, does not stop a read. A
server does not want to spend a 4K PNG decode learning where the doors are,
and a level handed to one without its textures is the normal case rather than
a broken one. `renderer/gltfread_test.go` proves this rather than asserting
it: the same in-memory document reads clean and fails the decode step
`LoadGLTF` takes on it.

Every `Model` method that is pure arithmetic works on a read model —
`Bounds`, `Node`, `NodeInMeshSpace`, `NodeMeshes`, `LightWorldPosDir`,
`ReleaseGeometry` — and they are walked on one by test rather than by
inspection. `Renderer.CombineModel` is the exception and always will be: it
uploads.

`ReadGLTFSkinned` is the same door for a skinned file, returning the
`Skeleton`, the `AnimationClip`s and the armature `RootTransform` alongside
the `Model`. One honest cost: it still decodes each skinned primitive's
vertices even though it cannot return them (`ModelMesh.Verts` is `[]Vertex`
and a skinned primitive decodes to `SkinnedVertex` — see the failure mode
below). Skipping that would mean a second decode path behind a flag, which is
the drift this split exists to prevent.

## Releasing one

```go
r.DestroyModel(model)          // or r.DestroySkinnedModel(skinned)
```

Every GPU resource `LoadGLTF` created for the model, released exactly once,
after the frames currently in flight have finished with it. Afterwards every
`ModelMesh`'s `Mesh`, `Texture` and `Material` is `nil`, so a draw that still
points at the model fails on a nil pointer in Go rather than inside the driver.
`Nodes`, `Lights`, `Verts` and `Idx` are untouched — they are CPU data the
model never owned on the GPU, and a game that keeps using a released level's
collider geometry is doing something reasonable.

It is idempotent, and a `Model` from `ReadGLTF` — which owns no GPU resources —
is a no-op rather than a panic.

**Do not walk `Model.Meshes` and destroy what each entry points at.** That is
the trap this closes. A model SHARES textures and cached materials across its
primitives: a level with one atlas and twenty primitives has twenty
`ModelMesh.Texture` fields naming one `Texture`. In the other direction, an
image the document carries that no material references is uploaded all the
same, appears on no `ModelMesh`, and a walk leaks it. `DestroyModel` releases
what the upload recorded creating, not what the slice happens to name.

The double-free half of that is worth knowing precisely, because it is quieter
than it sounds: `DestroyTexture`, `DestroyMesh` and `DestroyMaterial` each
carry a `destroyed` flag, so a second free returns before it reaches Vulkan
and **the validation layer never sees it**. Measured — deliberately recording
each shared texture twice and running `22-level -reload 20` under the layer
produced zero messages. Only `renderer/modeldestroy_test.go` catches it.

### Stop drawing it first

Remove the `MeshRef` (and `MaterialRef`) components of every entity spawned
from the model, or despawn those entities, **before** calling `DestroyModel`.

The failure if you do not is real and the validation layer does report it.
`DestroyModel` nils the handles on the `Model`, but an entity's `MeshRef` holds
its own copy of the `*Mesh` pointer and the engine draws from that. Measured by
removing the despawn from `examples/22-level`'s release path and running it
under the layer:

```
VULKAN ERROR [Validation] VUID-vkDestroyBuffer-buffer-00922: vkDestroyBuffer():
can't be called on VkBuffer 0x…[] that is currently in use by VkCommandBuffer 0x…[].
VULKAN ERROR [Validation] VUID-vkDestroySampler-sampler-01082: vkDestroySampler():
sampler can't be called on VkSampler 0x…[] that is currently in use by
VkDescriptorSet 0x…[].
```

Thirteen messages in six reload cycles, and a non-zero exit. Without the layer
the same run is silent and draws whatever the driver left in that memory.

`examples/22-level`'s `releaseLevel` is that order in one place: despawn, then
`DestroyModel`.

### Reloading a level without a hole in the frame

The obvious shape — release the old model, load the new one — is wrong, and
wrong in a way no gate here caught: the level is missing for however many
frames the load takes, and the world blinks. The first version of
`22-level -reload` did exactly that, passed the validation layer in silence,
and was caught by someone watching the window.

Load first, swap, release after, all inside one tick:

```go
newModel, err := r.LoadGLTF(levelFS, name)
if err != nil {
    return err // nothing has been given back; the old level is still up
}
newEntities := spawnLevel(e, newModel)

for _, ent := range oldEntities {
    e.Scene.Despawn(ent)
}
r.DestroyModel(oldModel)
```

That also fails safely: a broken re-export returns an error with the old level
still on screen.

This is the HARDER case for `DestroyModel`, not the easier one. At the moment
of the swap the frames still in flight reference the old buffers, which is why
the release is deferred: `DestroyMesh` and `DestroyTexture` free a static
resource immediately (only a dynamic mesh already went through `DeferDestroy`),
which is correct at shutdown, where `Renderer.Destroy` has waited for the
device to go idle, and a use-after-free here. Their behaviour is unchanged for
every other caller; `DestroyModel` routes its own release through
`DeferDestroy` instead.

**Do not expect the validation layer to catch a missing deferral.** Measured:
with `DestroyModel` freeing inline instead of deferring, `22-level -reload 20`
ran all twenty swaps under the layer with **zero** messages. The layer reports
a resource freed while the draw list still names it (the stale-`MeshRef` case
above) and said nothing about one freed while only an already-submitted frame
still referenced it. What catches that is the resource count, which is why
`ResourceCounts` exists and why the reload loop asserts the counts do not drop
in the tick of the swap.

`examples/22-level -reload N` does all of this N times over; `task reload`
asserts the frame's draw count never moves while it does, and `task validate`
runs the same loop under the layer.

### Counting what is live

```go
counts := r.ResourceCounts() // Meshes, Textures, Materials, DescriptorSets, InstanceSets, Deferred
```

The renderer's own cleanup lists, the descriptor sets those resources hold, and
how many destructions are still waiting out the frames in flight. It is
exported because a check that teardown happened cannot otherwise be written
from outside the package, and this repo has shipped a teardown test that
reported zero leaks because teardown never ran.

`Deferred` is not a detail: a count taken immediately after `DestroyModel`
still includes the model, because those resources are genuinely still alive.

`LODSets` and `ImpostorAtlases` count the resources described in
[LOD instancing](lod-instancing.md), including deferred releases. An atlas also
owns one descriptor set, counted in `DescriptorSets`; a LOD set borrows its atlas
and owns only its placement buffers.

`DescriptorSets` is not derivable from the other three, which is why it is
there — see the next section for what it cost to find that out. It counts one
set per `Texture`, one per `Material`, one per `TerrainMaterial` and two (one
per frame in flight) per `JointBuffer`. It does not count the renderer's own
pass sets; `docs/agents/validation.md` lists what else lives in that pool.

### The descriptor set goes back too — and the 676-reload wall is gone

The set is returned now. Same measurement that found the wall,
`22-level -reload 700 -level renderer/testdata/blender/level.glb` on Windows
11 with an RX 7900 XTX, a level carrying one texture and no material: **700
reloads, 50 seconds, and the set count identical at the top of every cycle.**

```
-reload: 700 swaps done; one level is 9 meshes, 1 textures, 0 materials,
1 descriptor sets, and the renderer tracked the same 20/4/0/4 at the top of
every cycle
```

Keep the history, because it is why the gate is 700 cycles and not 20. Before
issue #82 the same command failed, and the shape of that failure is worth
recognising:

```
-reload: reloading the level: load level.glb: load gltf images:
upload texture 0 (GroundTex): allocate descriptor set: vulkan error: out of pool memory
```

**676 loads succeeded and the 677th failed.** Every `Texture` and every
`Material` takes a set from the renderer's single pool, the pool was created
without `VK_DESCRIPTOR_POOL_CREATE_FREE_DESCRIPTOR_SET_BIT`, so
`vkFreeDescriptorSets` was not a legal call on it and nothing could give a set
back. `DestroyModel` released the image, the view, the sampler and the memory;
the set was the one thing it could not. Nothing else about the model
accumulated — the mesh, texture and material counts returned to identical
numbers at the top of every cycle, all 676 of them — and a level with no
textures (`22-level`'s built-in one) took no set and reloaded indefinitely,
which is exactly why `task reload` watched this happen twenty times and
reported nothing.

Where the set is freed, and why there:

- **`DestroyTexture` frees it immediately**, before the view and sampler it
  names. Everything else that function owns is freed immediately too, so the
  set's exposure to a frame in flight is exactly the sampler's, and it is the
  caller's to arrange — `DestroyModel` runs the whole call through
  `DeferDestroy`, and `Renderer.Destroy` has already idled the device.
- **`DestroyMaterial` and `DestroyJointBuffer` free it inside the deferral
  they already queue** for the uniform buffers that set names. There is
  nothing immediate in those for it to be safe alongside.

In both cases the set is freed *before* the objects it names, which is the
order `Renderer.Destroy` already sweeps in.

**The validation layer will not tell you if you get that wrong.** Measured,
both directions, on `22-level -reload 20` under the layer:

- Set freed immediately, while only already-submitted frames still referenced
  it (the despawn left in place): **zero messages over twenty cycles, exit 0.**
  What caught it was `ResourceCounts` — the count dropped in the same tick as
  the swap, and the reload loop asserts it does not.
- Set freed while it was still in the live draw list (the despawn removed):
  the layer does speak, with
  `VUID-vkFreeDescriptorSets-pDescriptorSets-00309`, "pDescriptorSets[0]
  VkDescriptorSet … is in use by VkCommandBuffer …". Free it immediately in
  that state and the complaint moves to the next *bind* instead —
  `VUID-vkCmdBindDescriptorSets-pDescriptorSets-parameter` ("Invalid
  VkDescriptorSet Object"), `…-graphicsPipelineLibrary-06754` ("that does not
  exist") and `VUID-vkCmdDrawIndexed-None-08600` ("uses set #0 but that set is
  not bound").

So the layer reports a set that is still being *drawn with*, and says nothing
about one that is merely still in flight. That is the same blind spot #72
recorded for the sampler, and the reason the count exists.

### An instanced level's InstanceSets are released too

`examples/22-level -instanced -reload N` (issue #84) is the same swap with
the level's repeated props batched into `InstanceSet`s (see
[`instancing.md`](instancing.md#turning-a-levels-repeated-nodes-into-instances))
instead of one entity per node. `Renderer.DestroyInstanceSet` gives each
set's buffer back the same way `DestroyModel` gives a model's resources back
-- deferred past the frames in flight, because the harder case is the same
one: the new level's sets are already in the draw list before the old ones
are released, in the same tick.

An `InstanceSet` holds no descriptor set of its own -- it is a plain vertex
buffer of placements, not a pool allocation -- so it needed a count of its
own rather than riding along on `DescriptorSets`: a `DestroyInstanceSet` that
did nothing would leave all four numbers above at their baseline while an
`InstanceSet`'s buffer leaked every cycle, silently, since nothing else here
would move. `ResourceCounts.InstanceSets` is that count, kept the same way
the others are -- not decremented until the deferred free actually runs. See
[`instancing.md`](instancing.md#releasing-one) for the API, the measured
`-reload -instanced` counts on both committed level files, and the three
breaks recorded against it.

## Memory

Geometry is retained by default, because the decode allocated it anyway and
discarding was the only reason it was unavailable. A big scene model is
megabytes of it, so:

```go
model.ReleaseGeometry()
```

After that, `Bounds` reports `ok == false` and `CombineModel` returns an error
rather than an empty mesh — failing instead of silently producing nothing.

Texture decoding borrows the decoder's buffer when it can. `image/png` returns
an `*image.RGBA` for an RGB file and an `*image.NRGBA` for an RGBA one, both
already packed 4 bytes per pixel, and the loader used to allocate a second
full-size image and convert through the generic per-pixel path anyway: in a
60-prop scene with 2048x2048 sheets that was 3.26 GB of a 3.9 GB total-alloc
load and about 2 s of CPU (issue #135). An opaque `*image.RGBA` (premultiplied
and straight alpha are the same bytes at alpha 255) and a packed
`*image.NRGBA` are now used as-is; a sub-image, a paletted PNG, a JPEG's
YCbCr or an RGBA image with real alpha still takes the conversion.
`BenchmarkDecodeImageRGB2048`, one 2048x2048 RGB PNG, five iterations each:

| | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| generic conversion | 66,528,480 | 33,617,769 | 21 |
| borrowed buffer | 38,615,220 | 16,840,435 | 18 |

`TestStraightRGBABorrowsPackedBuffers` asserts the returned slice shares the
decoder's backing array; deleting the fast paths fails it with "copied" while
the bytes still match, which is why the alias is what the test checks.

## Failure mode: skinned primitives carry no vertices

A skinned primitive decodes to `SkinnedVertex`, a different layout carrying
joint indices and weights, so there is nothing to put in a `[]Vertex`.
`ModelMesh.Verts` is therefore empty on anything loaded through
`LoadGLTFSkinned` with a skin, and a purely skinned model answers `Bounds` with
`ok == false` and cannot be combined.

`Name` is set regardless, so identifying a skinned primitive works, and so is
`DoubleSided` — which `LoadGLTFSkinned` used to drop and now reports, since
both loaders share one decode (issue #75). Nothing in the engine acts on it
for a skinned mesh, and no example's render moved: the only reader of
`ModelMesh.DoubleSided` is `examples/22-level`, which loads through
`LoadGLTF`.

Verified against `examples/06-skinned/assets/character.glb`: two skinned
primitives, both `Name == "colormap"`, both with no retained vertices, and
`Bounds` correctly reporting that it cannot answer. The static primitives in the
same loader do retain geometry.

## Failure mode: a static mesh drawn without its node's transform

`LoadGLTF` never applies a node's transform to the vertices it decodes (see
"Node space" above) — it predates `Model.Nodes` and changing it now would move
every existing model in every game already built on that behaviour. When a
mesh's instancing node carries a transform anyway, that model is silently
wrong: it draws at its mesh-local position and orientation, not where the node
graph says it should be.

`LoadGLTF` and `LoadGLTFSkinned` now make this visible instead of silent: one
log line per load, naming the node, when this is detected. The detection
itself (`untransformedMeshNodes`) is a pure function of `Model.Nodes` and
`Model.Meshes` and is unit-tested without a device. The name list is capped
(first five, then "and N more") so the line stays one line even when a lot
of nodes trip it.

**A level file is the case that fires this for (almost) every mesh node on
purpose** — the per-node pattern in "Loading a level" below is the correct
handling there, and the log line says so rather than reading as an error to
fix. In practice it fires less than that count would suggest: the detection
walks `ModelMesh.Node`, which only ever names the FIRST node instancing a
given doc mesh (see "Node space" above), so a level built from a few
instanced doc meshes — several lamp posts sharing one pole mesh — reports
one node per *doc mesh*, not one per node. `examples/22-level`'s level.glb
has eight instanced nodes (four buildings, four lamp posts) across two
shared doc meshes, plus one identity ground node, and the line names exactly
two: `node(s) [Building0 LampPost0] carry a transform ...`. That undercount
is a property of reusing `ModelMesh.Node` for this check, not something this
issue changes — `Model.NodeMeshes` (below) is unaffected, since it is keyed
by node rather than by "first owner".

## Failure mode: LoadGLTFSkinned's Nodes are bind pose, not the animated pose

`LoadGLTFSkinned` fills `Model.Nodes` the same way `LoadGLTF` does, so a
socket works on a skinned model's non-animated attachment points too. This is
**not** a way to attach something to an animated joint: `ModelNode.World` is
the authored bind-pose transform, computed once at load, not a joint's
transform during playback. For that, see `Skeleton` and `Joint` in
`docs/agents/skeletal-animation.md`.

## Loading a level

A level authored in an editor and exported as one glTF is *nearly* loadable
with everything above: `Model.Nodes` is the scene graph, `extras` is
per-node application data, and `LoadGLTF` never moves geometry off its
mesh-local origin, which is exactly the shape a level needs — one entity per
node, at that node's own placement. `examples/22-level` (`task
example:22-level`) is the end-to-end pattern; this section is the parts of
it worth knowing before writing your own.

It opens its built-in level by default, and any glTF on disk with `-level`:

```
go run ./22-level -level path/to/exported.glb
```

which makes it the quickest way to see what a file exported from Blender turns
into. Checked against a real export (Blender 5.0.1, custom properties and
punctual lights ticked): the extras, the lamp's position and downward aim, its
cone angles and the two Alt-D duplicates sharing one mesh all arrive as
authored. `spawnPrimitive` also draws textures now (`MaterialRef.PBR` when
LoadGLTF built a `Material`, `.Texture` otherwise) and honours tiling -- see
[`material-maps.md`](material-maps.md#khr_texture_transform-tiling-rotation-offset)
for `KHR_texture_transform` and sampler wrap modes (issue #69).

### Placing every instance, not just the first

A level reuses meshes — four identical lamp posts, forty identical lamp
posts — which means several nodes share one doc mesh. `ModelMesh.Node` names
only the first such node (see "Node space"), so it is the wrong tool for
placement. `Model.NodeMeshes` answers the question a spawn loop actually
has, node by node:

```go
for i, node := range model.Nodes {
    for _, mi := range model.NodeMeshes(i) {
        mm := model.Meshes[mi]
        // spawn one entity: mm.Mesh at glyphengine.TransformFromMatrix(node.World)
    }
}
```

A node with no mesh (`NodeMeshes` returns nothing) is an empty node — a
light socket, a spawn marker — not an error.

### Turning `World` into a `Transform`

`glyphengine.Transform` is Position, Euler `Rotation` (radians) and `Scale`,
and `Transform.ModelMatrix` composes them as `Translate * RotY * RotX * RotZ
* Scale` — this engine's own order. A node's `World` is a matrix, so it has to
be taken apart against that same product, and
`glyphengine.TransformFromMatrix` is the engine doing it:

```go
tr, exact := glyphengine.TransformFromMatrix(node.World)
if !exact {
    log.Printf("node %q is sheared; drawn without that part of its transform", node.Name)
}
scene.C.Transform.Set(ent, &tr)
```

Do not write this by hand in a game. The textbook extraction assumes `Rx * Ry *
Rz` and returns perfectly plausible angles that point the object somewhere
else — against this engine's order it fails every one of 2000 random
transforms — and a private copy does not find out if `ModelMatrix` ever
changes. `TestTransformFromMatrixRoundTrips` ties the two together by comparing
matrices, not angles.

`exact` is false when no `Transform` can hold the matrix: **shear**, which is
what a rotated object inside a non-uniformly scaled parent becomes in world
space, or a zero scale on an axis. In Blender the fix is Object > Apply > Scale
on the parent before exporting; the node's name is how you find it. A
**mirrored** object (negative scale, or a mirror that was applied as one) is
representable and comes back exact, with the mirror on `Scale.X`.

Give every entity its **own** `Transform`. The component store keeps the
pointer it is handed, so two entities given the same `&tr` are one object to
anything that moves or interpolates them. `examples/22-level` copies it per
primitive for that reason.

### `KHR_lights_punctual`

`Model.Lights` is every punctual light the document carries — point, spot,
directional (`ModelLightKind`) — attached to a node by `ModelLight.Node`,
built the same way `Model.Nodes` is: a pure function of the document, no GPU
needed to read it.

```go
for _, l := range model.Lights {
    pos, dir := model.LightWorldPosDir(l) // glTF lights aim down -Z, same as a socket's forward
    switch l.Kind {
    case renderer.LightKindSpot:
        spots = append(spots, glyph.SpotLight{
            Pos: pos, Dir: dir, Range: rangeOrDefault(l.Range),
            Color: color(l), Inner: l.InnerCone, Outer: l.OuterCone,
        })
    case renderer.LightKindPoint:
        points = append(points, glyph.PointLight{Pos: pos, Range: rangeOrDefault(l.Range), Color: color(l)})
    }
}
```

Two things a game MUST decide, because the engine will not guess:

- **Units.** `ModelLight.Intensity` is glTF's raw value — candela for
  point/spot, lux for directional — and `glyphengine.PointLight`/`SpotLight`
  carry no photometric unit at all; intensity rides entirely in `Color`, the
  same as a hand-placed light already works (see
  [`lights.md`](lights.md)). A real Blender 1000 W spot exports at
  `Intensity` 54351.4, so "just use the number" saturates every fixture to
  white; a game divides by something. `examples/22-level` divides by 20000,
  chosen visually, and says so in a comment next to the constant — there is
  no physically correct divisor to check this against, only "does the scene
  look right".
- **Range.** `ModelLight.Range` is 0 when the document said "unbounded"
  (glTF's own default), which `glyphengine`'s lights have no notion of.
  **This is the normal case for a Blender-authored level, not a corner
  case**: Blender's glTF exporter never writes `range` at all, so every
  light in a Blender export arrives with `Range == 0`. A game MUST supply a
  finite range for any light whose `Range` is 0.

`InnerCone`/`OuterCone` are half-angles in radians from the light's aim
axis — verified against this page's `Inner`/`Outer` fields and
`glyphengine.SpotLight.Inner`/`Outer` in `docs/agents/lights.md`, which use
the identical convention, so these carry straight across with no conversion.
glTF's own defaults (inner 0, outer π/4) are applied when the document
omits either.

### `extras` as the level's own vocabulary

The engine hands over `ModelNode.Extras` and stops; `examples/22-level`
defines what its own keys mean and reads them itself:
`{"static": true, "collider": "box", "floors": 3}` on the ground and every
building, `{"spawn": "player"}` on an otherwise-empty node the example uses
to place the camera. A different level format would read different keys —
none of this is the engine's vocabulary, only the example's.

`glyphengine.Collider` carries only `HalfExtents`, no centre offset, so a
box collider sized from a mesh whose local origin sits at its BASE rather
than its centre (the ordinary shape for something standing on the ground)
cannot be made both centred-on-the-node and tightly-fitted without an offset
field the engine does not have. `examples/22-level` picks
centred-and-conservative — `max(|min|, |max|)` per axis — over adding one;
see `meshesLocalHalfExtent`'s doc comment for the trade-off this costs.

### A Blender export needs two boxes ticked

Blender is the reference world-building pipeline this pattern targets, and
its glTF exporter ships with **Custom Properties, Punctual Lights and GPU
Instances all OFF by default**. A level exported with the defaults silently
loses its `extras` and its lights — not an error, just an empty
`Model.Lights` and every `ModelNode.Extras` nil, which reads as "the level
has no lamps" rather than "the export dropped them". Tick **Include >
Custom Properties** and **Include > Punctual Lights** before exporting a
level, or run `tools/blender/export_level.py`, which sets those two plus
four more a level needs and cannot be forgotten one at a time. The full
Blender-side recipe — units, axis conventions, Alt-D vs Shift-D, what an
unapplied non-uniform scale does to a rotated child, mirrored objects, light
units, and what does not survive the export at all — is
[`blender-pipeline.md`](blender-pipeline.md); this section is only the
warning and the pointer.

One more Blender default worth knowing rather than working around: its
exporter writes `doubleSided: true` on a material unless the artist enables
Backface Culling, so a level's materials read as double-sided more often
than a hand-authored asset's do. Nothing to build for this — `ModelMesh.DoubleSided`
already carries it — just don't be surprised by it.

## What this is checked against

`08-grass` loads four flora models through `LoadGLTF`, and they are the
end-to-end evidence rather than a fixture:

```
"Grass" verts=153 idx=465   bounds min=[-0.356 -0.022 -0.479] max=[0.283 1.312 0.258]
"Grass" verts=303 idx=978   bounds min=[-0.404 -0.031 -0.479] max=[0.492 1.841 0.514]
"Grass" verts=559 idx=1482  bounds min=[-0.728 -0.086 -0.583] max=[0.594 0.986 0.627]
```

The merge arithmetic is unit-tested without a device, because the failure it
guards is an index offset: get it wrong and the mesh still draws, with triangles
reaching into the wrong primitive's vertices, which reads as a modelling mistake
rather than a loader one.

`Model.Nodes`, `Model.Node`, `Model.NodeInMeshSpace`, `Model.NodeMeshes`, node
`extras`, `Model.Lights`/`LightWorldPosDir`, and the untransformed-node
detection (including its name-list cap) are all checked the same way, but
against glTF documents built in memory rather than against `08-grass` or
another bundled asset — this needs no device either, since it is all
arithmetic over `*gltf.Document` and `Model.Nodes`. See
`renderer/gltfnodes_test.go` and `renderer/gltflights_test.go`.

`renderer/gltfread_test.go` pins the decode itself: a SHA-256 of every
primitive's upload bytes (`Verts` then `Idx`) for both committed level
fixtures, captured on the commit *before* `LoadGLTF` was split into a read and
an upload. That is the only check in the suite that would notice the split
quietly moving a vertex — everything else would still load, still draw and
still pass.

One more test parses the actual bytes of the committed
`examples/22-level/assets/level.glb` and runs the same extractors over it,
asserting node/mesh/light counts and one lamp's world position and direction
against numbers computed by hand from the generator's own inputs
(`renderer/gltflevel_test.go`) — so the generator, the committed file and the
extractors cannot drift apart from each other silently.

## Not done

- **Applying node transforms to geometry.** `Model.Nodes` surfaces the node
  graph, but `LoadGLTF` still never applies a node's transform to the vertices
  it decodes — every primitive stays in its mesh-local space, which is why
  merging is a plain concatenation. See "Node space" and the "static mesh
  drawn without its node's transform" failure mode above; fixing this would
  move every existing model in every game already built on the current
  behaviour, so it is a deliberate non-goal here, not an oversight.
- **Colliders from geometry.** Not missing a generator — `ComputeConvexHull`
  (`docs/agents/physics-queries.md`) already builds one from a point cloud,
  and `ModelMesh.Verts` positions are exactly that point cloud. What is
  missing, if anything, is only convenience: nothing plumbs "this mesh's
  positions" straight into it for you, so a caller does that arithmetic
  itself (or reaches for a box `Collider` from bounds, the way
  `examples/22-level` does — see "Loading a level" above).
- **Turning repeated nodes into instances — done, not a plan anymore (issue
  #71).** `Model.MeshInstances(docMesh int) []int` is `NodeMeshes` run
  backwards: given a doc mesh, every node that instances it, in node order.
  `examples/22-level -instanced` is the worked recipe — group by doc mesh,
  decide which groups qualify (the game's call, not the engine's, per rule
  14 above), build one `renderer.InstanceSet` per qualifying group with one
  `MeshInstance` per node's `World`. See
  [`instancing.md`](instancing.md#turning-a-levels-repeated-nodes-into-instances)
  for the accessor, the rule the example picked, and the measurement (500
  props, draw calls 261 to 4, `cpu_drawlist + cpu_record` down about 78%).
  `EXT_mesh_gpu_instancing` itself stays unread: issue #67's real-Blender
  fixture measurement (`renderer/testdata/blender/level.glb`,
  `docs/agents/blender-pipeline.md`, "Instancing") found that nothing a level
  artist actually does in Blender writes it — Alt-D and collection
  instances both arrive as ordinary "several nodes, one doc mesh" that
  `NodeMeshes` already reads correctly — so there was nothing to gain from
  reading an extension no exporter emits for this use.
