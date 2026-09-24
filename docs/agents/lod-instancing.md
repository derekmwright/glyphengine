---
id: lod-instancing
title: Cull and select distance levels per placement
summary: >
  InstanceSetLOD selects on the CPU or GPU, culls placements and cross-fades
  them with ordered coverage. An optional eight-view atlas supplies far billboards.
capability: rendering
status: stable
since: v0.5.0
api:
  - glyphengine.InstancedMesh.LOD
  - renderer.LODLevel
  - renderer.InstanceSetLOD
  - renderer.InstanceSetLODDesc
  - renderer.InstanceSetLOD.Counts
  - renderer.RenderObject.InstancesLOD
  - renderer.Renderer.CreateInstanceSetLOD
  - renderer.Renderer.UpdateInstanceSetLOD
  - renderer.Renderer.DestroyInstanceSetLOD
  - renderer.ImpostorAtlas
  - renderer.Renderer.BakeImpostor
  - renderer.Renderer.DestroyImpostorAtlas
  - renderer.Renderer.LastLODWork
  - renderer.ShaderSet.LitLODVert
  - renderer.ShaderSet.LitLODFrag
  - renderer.ShaderSet.ImpostorVert
  - renderer.ShaderSet.ImpostorFrag
example: examples/25-lod-forest
run: task example:25-lod-forest
requires:
  - cgo
  - vulkan-runtime
assets: procedural
verified: 2026-09-24 # CPU/GPU indirect path and ordered compaction
---

# Cull and select distance levels per placement

```go
package forest

import (
    glyph "github.com/derekmwright/glyphengine"
    "github.com/derekmwright/glyphengine/renderer"
)

func Attach(e *glyph.Engine, meshes [3]*renderer.Mesh, texture *renderer.Texture,
    placements []renderer.MeshInstance) (*renderer.InstanceSetLOD, *renderer.ImpostorAtlas, error) {
    r := e.Renderer()
    atlas, err := r.BakeImpostor(meshes[0], texture, 128)
    if err != nil { return nil, nil, err }
    set, err := r.CreateInstanceSetLOD(renderer.InstanceSetLODDesc{
        Levels: []renderer.LODLevel{
            {Mesh: meshes[0], MaxDistance: 40},
            {Mesh: meshes[1], MaxDistance: 75},
            {Mesh: meshes[2], MaxDistance: 110},
        },
        Impostor: atlas, Capacity: 10000, FadeWidth: 6, ShadowLevel: 1,
    }, placements)
    if err != nil { r.DestroyImpostorAtlas(atlas); return nil, nil, err }
    entity := e.Spawn()
    e.C.InstancedMesh.Set(entity, &glyph.InstancedMesh{LOD: set})
    e.C.MeshRef.Set(entity, &glyph.MeshRef{Roughness: 1})
    e.C.MaterialRef.Set(entity, &glyph.MaterialRef{Texture: texture})
    return set, atlas, nil
}
```

Keep the entity ID when the group will be removed: despawn it before destroying
its set. The renderer-only equivalent is `RenderObject{InstancesLOD: set}`.
`Instances` and `InstancesLOD` are mutually exclusive, as are `InstancedMesh.Set`
and `.LOD`; specifying both panics. No entity transform is needed.

## Selection and coverage

Levels are nearest first, with finite, positive, strictly increasing
`MaxDistance`. Selection uses Euclidean distance from the camera to the model
translation. The first band whose maximum **exceeds** that distance wins.
Beyond the last mesh, the optional impostor draws; without one, the placement
is culled.

`FadeWidth` is the full width centred on a boundary: width 6 at distance 40
transitions from 37 to 43. Both neighbours draw, with complementary coverage
written over `MeshInstance.Tint.w`. At the last boundary without an atlas,
the final mesh fades to empty. Zero means a hard switch. Negative/nonfinite
widths, or widths larger than a distance interval, are rejected rather than
creating overlapping three-band transitions.

`Tint.rgb` still multiplies vertex colour. Games do not set the LOD alpha;
the original placements are copied, and only the renderer's buckets receive
coverage. Ordinary `InstanceSet` still ignores `Tint.w`.

`LitLODFrag` specialises constant **0**: value 0 disables coverage, 1 is
one-sample ordered discard, and 2 uses alpha-to-coverage.
The LOD pipeline variants alone enable alpha-to-coverage. A 4×4 Bayer pattern
uses screen coordinates; neighbouring bands invert its threshold so their
coverage fills complementary pixels. With MSAA, each threshold transitions
through sample coverage. Simply giving both draws complementary alpha would
overlap their coverage masks and reveal background at the midpoint.

In `task lod`, frames 60/61 of `-pose pop`, rectangle `(575,280)-(705,440)`,
mean RGB change at MSAA 4 is **0.069279/255** with width 6 against
**3.698333/255** with width 0. Pixels changing by at least 16/255 fall from
1,493 to 26. At one sample: 0.069872 against 3.885369, and 14 against 1,410
visible pixels. These are transition captures, not proof that arbitrary
authored levels have matching silhouettes or that motion never shimmers.

## Bounds and counts

Each placement's sphere is centred at its **model translation**, with the
largest mesh-level `BoundRadius` multiplied by the largest model axis scale.
Author bounds large enough to enclose each level about that origin. In
particular, a base-anchored mesh may need a larger radius than the loader's
centred sphere. Placement matrices should be finite, nonsingular TRS matrices;
shear is not included in the largest-axis scale rule.

Each frame tests every placement against the main camera frustum. If any mesh
level has no positive bound radius, frustum culling is skipped conservatively;
distance selection still applies. A missing bound never causes the group to
vanish because of a frustum test.

In CPU mode, `Counts()` returns the last prepared frame's per-band counts, including the
impostor if supplied, and a culled count. The returned slice is a read-only
view; copy it to keep a snapshot. Fade duplicates appear in both bands, so
the sum can exceed the number of visible placements. Culled includes the
capacity tail, frustum rejects and placements beyond the final fade.

The edge pose in `25-lod-forest` asserts **[1 0 0 0], culled=3599**, against
**[3600], culled=0** for `-levels 1`, which uses the original per-set path.
Including terrain, sky and shadow passes, submitted work drops from 5 draws /
10,802 instances to 3 draws / 3 instances in that pose.

## Baking and ownership

`BakeImpostor(mesh, texture, size)` runs synchronously on the renderer thread.
`size` is the per-view tile edge (4–2048 pixels); the image is eight tiles wide.
Views are 45 degrees apart around local Y, looking down from 12 degrees above
the horizon. Mesh bounds frame the bake. The shared grass bake shaders write
colour and alpha, with a depth attachment for arbitrary overlapping geometry.
Current directional lighting, shadow lookup and fog are applied at draw time
through the shared grass fragment implementation, so daylight is not frozen
into the atlas. A nil texture bakes white texture colour, multiplied by the
mesh's vertex colours.

The billboard chooses the nearest view in model-local coordinates, including
instance rotation. It faces the camera and scales with the largest model axis;
nonuniformly scaled objects are approximated by that square, not reproduced
exactly. Impostors are a distance approximation, particularly from steep
camera elevations. Each atlas can be shared by several sets.

`UpdateInstanceSetLOD` copies the full placement list. Capacity is fixed:
both create and update drop the tail beyond it and report those placements as
culled. There is no implicit GPU buffer growth.

Sets borrow their meshes and atlas. Destroying a set does not destroy those
resources. Remove all referring draws, call `DestroyInstanceSetLOD`, then
`DestroyImpostorAtlas` when the atlas is no longer shared. Both are nil-safe,
idempotent and deferred over frames in flight. Stale set/atlas draws panic
before recording Vulkan commands. Renderer shutdown releases unreclaimed
sets and atlases. `ResourceCounts.LODSets` and `.ImpostorAtlases` remain live
until deferred release actually runs.

## Shadows and custom shaders

`ShadowLevel` selects **that level's visible bucket**, drawn through the
existing instanced depth pipeline and culled per cascade or point-light face.
It does not redraw every placement at the chosen mesh. Consequently placements
assigned only to other levels, including off-camera placements, do not cast.
Choose `-1` for no shadows. Impostors never cast, and `NoCastShadow`/`Emissive`
apply to the entire group. Depth-only shadows do not dither the transition.

Wind and mesh deformation remain game shader work. `ShaderSet.LitLODVert`
receives binding 0 (`Vertex`, stride 44: position 0, colour 1, normal 2, UV 3)
and binding 1 (`MeshInstance`, stride 80: model columns at locations 4–7,
offsets 0/16/32/48, and tint at 8, offset 64). Forward `Tint.w` to fragment
location 5. Positions and normals use the instance matrix; the first push
matrix is VP alone. The second push matrix is reserved for renderer LOD data,
including `[3][3]` as the coverage phase. `ImpostorVert`/`ImpostorFrag` are
separately replaceable; their descriptor layout is texture at set 0 and
shadow/lights at set 1. The impostor vertex stage generates six corners from
`gl_VertexIndex` and needs only binding 1.

Custom `LitLODFrag` declares `layout(location=5) in float fragFade;`, preserves
specialization constant 0 and applies coverage. Only the dedicated LOD stages
have this interface. `LitVert`, `LitFrag`, `LitInstancedVert`, and custom
terrain/material stages retain their existing interfaces; their authors need
no changes. `LitLODFrag` and ordinary `LitFrag` share `lighting.inc` for
lighting, shadow lookup, local lights and fog. Override the LOD stages
explicitly when the same custom lighting or deformation should apply there.

## GPU selection and indirect drawing

Set `InstanceSetLODDesc.GPU: true` to move frustum tests, distance selection,
coverage and bucket generation to compute. The zero value retains CPU selection.
The same mesh, fade, atlas, placement and shadow rules apply. `25-lod-forest -gpu`
selects this path; add `-indexed` to exercise indexed indirect commands.

GPU mode supports **eight total buckets**, including an impostor, and rejects
an arena larger than the device's `MaxStorageBufferRange`. It owns one
placements buffer and, per frame slot, a device-local bucket arena, indirect
arguments, scan scratch, a small uniform and mapped counter readback. Each
bucket has a disjoint `Capacity × 80` byte range, bound at that range's vertex
offset. Four storage descriptors suffice regardless of bucket count. There is
no memory aliasing between graph resources.

Three dispatches classify and scan 64-placement blocks, scan the block totals
and initialize every command, then scatter in placement order. The middle
phase resets counts even for empty sets. Compute runs before shadows, ahead
of application `StageBeforeScene` nodes, which retain their post-shadow position.
The graph derives storage-to-vertex-input, storage-to-indirect-fetch and
counter-copy barriers outside render passes. Each mesh band issues one
`CmdDrawIndexedIndirect` or `CmdDrawIndirect`; the impostor uses
`CmdDrawIndirect`. ShadowLevel uses that same selected bucket. Vulkan 1.0's
single-command indirect drawing needs no optional multi-draw or indirect-count
feature. An empty bucket still records an indirect call with zero instances.

`UpdateInstanceSetLOD` uploads copied placements through a synchronous staging
submission, ordered against earlier graphics-queue work. This can stall during
updates; static placements have no per-frame upload. Per-frame work on the CPU
is proportional to bucket count. Uniform and readback access happens after the
frame slot's fence. Buffers survive resize and retire with the set.

For a GPU set, `Counts()` waits for the most recently submitted frame's fence
and reads that frame's counters; during `Update` these are the **previous
submitted frame**, not the frame about to be drawn. Before any submission it
returns zero. Treat this as a diagnostic API: polling it can serialize frames.
`Stats().DrawCalls` includes empty indirect calls; its GPU LOD instance/triangle
values estimate work from the last retired slot. Use Counts for exact submitted
bucket counts, and do not compare indirect call count to surviving bucket count.

`LastLODWork` and `cpu_lodcull` measure parameter preparation and dispatch
recording; `cpu_lodupload` is zero in steady GPU frames. `GPUTimings.App` includes
one `lodselect` interval over all GPU LOD sets, also emitted as `gpu_lodselect`
by `task bench`. It has a reserved timing slot independent of the sixteen
application slots. Existing engine `Pass` values and brackets are unchanged.

At fixed forest frame 61, 1280×720, MSAA4, atomic append differed from CPU in
four channel samples (maximum 5/255), and repeated GPU runs differed in one
(1/255). Stable compaction makes CPU/GPU and GPU/GPU captures pixel-identical.
Both modes also produce the same fade metrics recorded above. Disabling GPU
coverage makes the comparison fail on 1,309,981 channel samples (maximum
208/255), so the equality gate checks visible geometry.

On 2026-09-24, RX 7900 XTX, 1280×720, 200 fixed-clock frames per run,
`task lod` ran CPU/GPU/CPU/GPU with the GPU otherwise idle:

| Run | Mode | GPU total | CPU selection/recording | CPU upload | GPU selection |
|---|---|---:|---:|---:|---:|
| A1 | CPU | 1.391 ms | 0.237 ms | 0.026 ms | — |
| B1 | GPU | 1.416 ms | 0.019 ms | 0 | 0.035 ms |
| A2 | CPU | 1.384 ms | 0.279 ms | 0.022 ms | — |
| B2 | GPU | 1.425 ms | 0.054 ms | 0 | 0.035 ms |

Mean CPU selection plus upload falls from 0.282 to 0.0365 ms (87.1%).
Mean GPU total rises from 1.3875 to 1.4205 ms; the new GPU selection interval
is 0.035 ms. This trades a small GPU cost for CPU work independent of placement
count; it is not a GPU frame-time optimization.

The same machine's full-suite main/branch/main/branch comparison (main
`9088bdf`) covers 29 shared scenes. Mean GPU totals range from -2.23% to
+0.60%; the largest increase is 0.020 ms, with no systematic increase in
existing engine pass brackets. Those scenes keep GPU LOD disabled, so the
small decreases are not attributed to this feature. Existing engine command
stream hashes remain unchanged.

## Cost and checks

For the default CPU path and a fixed number of levels, selection is linear in placement count. The
renderer walks placements once, retains bucket scratch, then copies each
survivor into the current frame's host-coherent buffer after its fence signals.
Fade regions copy a placement twice. Uploading one band never overwrites the
other frame's buffer. No allocation occurs per frame after scratch is sized;
the recorder fixture measures **0 allocations at 40 and 160 placements**,
including all mesh bands and the impostor.

Storage is `80 × Capacity × (1 + bands)` CPU bytes and
`80 × Capacity × bands × frames-in-flight` GPU bytes, excluding metadata,
meshes and the atlas. Four bands at 3,600 placements use 1.44 MB CPU and
2.304 MB GPU with two frames in flight. The atlas has additional colour/depth
storage. `LastLODWork`, `cpu_lodcull` and `cpu_lodupload` expose the measured
per-frame CPU cost.

On an RX 7900 XTX, 1280×720, 200 fixed-clock frames per run, sequential
`task bench` full/LOD/full/LOD with no other GPU gates running: mean GPU total
falls from **3.992 to 1.498 ms (62.5%)**. LOD selection costs **0.493 ms/frame**
and upload **0.037 ms/frame**. The full-detail control draws all 3,600 trees
(480 triangles each); LOD draws 480/80/24-triangle meshes and two-triangle
billboards. Shadow work also drops because only the chosen bucket casts.

| Run | Mode | GPU total | CPU selection | CPU upload |
|---|---|---:|---:|---:|
| A1 | full detail | 3.951 ms | 0 | 0 |
| B1 | LOD | 1.502 ms | 0.467 ms | 0.032 ms |
| A2 | full detail | 4.033 ms | 0 | 0 |
| B2 | LOD | 1.494 ms | 0.518 ms | 0.042 ms |

`task lod` runs culling, consecutive-frame transition checks at 1×/4× samples,
far-band visibility, byte determinism, resize/scene-swap/replacement lifetime checks and
sequential `task bench` A/B/A/B runs. `-pose edge|pop|far`, `-impostor=false`,
`-fade 0`, `-levels 1`, `-counts`, `-gpu`, `-indexed`, and `-replace` expose the controls in the
example. Captures remain in `.task/lod` for inspection. The example is also in
`task smoke`, `task validate`, `task determinism`, and `task screenshots`.
Use `task lod -- -reuse-bench` to rerender the correctness checks while checking
the retained benchmark JSON instead of running the four benchmarks again.

GPU storage, excluding meshes/atlas: `80 × Capacity` shared placement bytes;
per frame slot, `80 × Capacity × buckets` output bytes, approximately
`24 × Capacity + 36 × ceil(Capacity/64)` scan bytes, `20 × buckets + 4`
command bytes, the same number of readback bytes, and 64 uniform bytes.
The current implementation also retains the CPU placement and bucket scratch
allocated by the common constructor; this is a memory cost, not per-frame work.
