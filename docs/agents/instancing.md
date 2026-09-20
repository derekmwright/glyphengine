---
id: instancing
title: Draw repeated static meshes in one call
summary: >
  An InstancedMesh draws one mesh at many placements in a single draw call, for
  the case a builder-style game hits early: hundreds of identical props whose
  cost is entirely CPU-side command recording.
capability: rendering
status: stable
since: v0.5.0
api:
  - glyphengine.InstancedMesh
  - renderer.MeshInstance
  - renderer.InstanceSet
  - renderer.Renderer.CreateInstanceSet
  - renderer.Renderer.UpdateInstanceSet
  - renderer.Model.MeshInstances
example: examples/19-instanced
run: task example:19-instanced
requires:
  - cgo
  - vulkan-runtime
assets: none
verified: 2026-09-19
---

# Draw repeated static meshes in one call

```go
placements := make([]renderer.MeshInstance, 0, 900)
for _, p := range colony.Domes {
    m := mgl32.Translate3D(p.X, p.Y, p.Z).Mul4(mgl32.HomogRotate3DY(p.Yaw))
    var model [16]float32
    copy(model[:], m[:])
    placements = append(placements, renderer.MeshInstance{
        Model: model,
        Tint:  [4]float32{1, 1, 1, 1},
    })
}

set, err := e.Renderer().CreateInstanceSet(domeMesh, 1000, placements)
if err != nil {
    return err
}

ent := e.Spawn()
e.C.InstancedMesh.Set(ent, &glyph.InstancedMesh{Set: set})
e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: domeMesh, Roughness: 0.55})
```

The entity needs no `Transform`: every placement carries its own model matrix.
`MeshRef` on the same entity is read only for `Roughness` and `Metallic`, which
apply to the set as a whole.

To move, add or remove a placement, call `Renderer.UpdateInstanceSet` with the
new slice. Capacity is fixed at creation — a set that has to grow is a new set,
because resizing means reallocating a buffer the GPU may still be reading.

## What it is for

A colony with 900 identical habitat domes is 900 `CmdDrawIndexed` calls, 900
push-constant uploads and 900 frustum tests on the ordinary `MeshRef` path. The
GPU does not notice 180k triangles; the CPU recording them does.

The two workarounds each give something up. Merging props into one mesh per
chunk — what the terrain mesher does — trades away per-instance transforms, and
every placement or removal rebuilds and re-uploads the chunk. Scattering them
with the grass system is a scatter, not a placement: `CreateGrassFromModels`
distributes across a heightmap and has no way to say "this instance, at this
transform".

## What it costs, measured

`19-instanced` draws the same field both ways from one binary, so the comparison
is against an identical scene rather than two. Under `task bench` conventions,
200 frames, same machine:

| props | instanced | individual | draw calls |
| --- | --- | --- | --- |
| 100 | 0.249 ms | 0.310 ms | 8 vs 293 |
| 300 | 0.269 ms | 0.971 ms | 8 vs 689 |
| 900 | 0.232 ms | 1.584 ms | 8 vs 1528 |
| 2500 | 0.296 ms | 3.686 ms | 8 vs 3578 |

Those are `cpu_drawlist + cpu_record` — building the draw list and recording the
command buffer, which is the cost this removes. The instanced column is flat
because it is one draw call whatever the count; the variation in it is noise.

**The threshold is around 300.** At 100 props the saving is 0.06 ms, which is
nothing; by 300 it is 0.7 ms and by 2500 it is 3.4 ms, a fifth of a 60Hz frame's
entire budget. The design note guessed "no urgency at a few hundred objects" and
that turns out to be right.

GPU cost does not move against it:

| | instanced | individual |
| --- | --- | --- |
| gpu total | 1.030 ms | 1.050 ms |
| gpu shadow | 0.089 ms | 0.163 ms |
| gpu opaque | 0.080 ms | 0.072 ms |

The opaque pass is slightly *higher* instanced, which is the per-set culling
below being paid for. The shadow pass is close to half, because 900 separate
draws cost the GPU something too. Net, the GPU is unchanged and the CPU halves.

The two modes render the same image: 27 pixels of 832,000 differ below the HUD,
at up to 15/255, all on MSAA edges — the example builds the same rotation two
ways (`HomogRotate3DY` against `Transform`'s Euler composition) and the two
disagree in the last bits.

## Culling is per set

The set carries one bounding sphere over every placement, and the draw list
frustum-tests that. **A set with one dome on screen draws all of them.**

The alternative is culling per placement on the CPU and re-uploading the visible
subset every frame, which trades the command-recording cost this feature exists
to remove for a different CPU cost. What the current choice costs is vertex
shading for placements that are off screen, and the table above prices it: 0.080
ms against 0.072 ms in the opaque pass at 900 props.

If a set is spread across a whole map, split it into several — one per region —
and the bounds do the work. That is the cheap version of per-instance culling
and it needs no engine change.

## Shadows

Instanced geometry casts, through its own depth-only stage.

That stage exists because it has to: `shadow.vert` takes the model matrix from a
push constant, which the instanced path does not write, so without
`shadow_instanced.vert` every instance would land on top of the first one. The
draw would still record and nothing would error. That failure is invisible in a
still frame of a scene whose props happen to sit where their shadows would, so
it is the one to watch for when changing this.

Cascade culling is per set, as above. `NoCastShadow` and `Emissive` on the
entity apply to the whole set.

## Turning a level's repeated nodes into instances

`InstancedMesh` above is the primitive: a game hands it placements it already
has. A level LOADED from glTF (`docs/agents/models.md`'s "Loading a level")
has the opposite problem -- the placements are node transforms already
sitting in `Model.Nodes`, several of them sharing one doc mesh, and turning
that repetition into one `InstanceSet` needs the inverse of `Model.NodeMeshes`
(issue #71).

**Reading `EXT_mesh_gpu_instancing` is explicitly not the answer here.**
Issue #67 built a real-Blender fixture specifically to find out what a level
artist's repetition (Alt-D, a collection instance, a geometry-nodes scatter)
turns into on the wire, and the answer, measured, is: never that extension.
Every mechanism a Blender level actually uses arrives as ordinary "several
nodes, one doc mesh" -- `Model.NodeMeshes` already reads that correctly, and
`export_gpu_instances` recognises only Blender's own particle/geometry-node
instancer flag, which none of the three mechanisms sets. See
[`blender-pipeline.md`](blender-pipeline.md#instancing-issue-71s-ground-truth)
for the measurement.
So this is entirely engine- and game-side: batch what the file already gives
you, not a new format to read.

### `Model.MeshInstances`

```go
func (m *Model) MeshInstances(docMesh int) []int
```

`NodeMeshes(node int) []int` answers "which primitives does this node draw";
a level loader turning repetition into instances needs the other direction,
so `MeshInstances` is its mirror image: given a doc mesh index (the same
number `ModelNode.Mesh` and `ModelMesh.DocMesh` already use), every node
index that instances it, in glTF node order. Pure arithmetic over
`Model.Nodes` -- no GPU, works on a `Model` from `ReadGLTF` as well as
`LoadGLTF` -- and nil for a doc mesh nothing references, the same "not an
error" contract `NodeMeshes` gives a meshless node.

Shaped as a single-doc-mesh query rather than one grouping over the whole
model (`map[int][]int`) because that is what a spawn loop over
`Model.Nodes` actually wants: ask once, the first time a node naming a given
doc mesh is reached, not once per node and not as a whole-model
precomputation most levels do not need (most doc meshes in a level are not
shared at all). `examples/22-level`'s `spawnLevel` does exactly this --
see below.

### The recipe, in `examples/22-level -instanced`

```
go run ./22-level -instanced
go run ./22-level -instanced -level ../renderer/testdata/blender/level.glb
```

Default off, so the ordinary per-node path (`docs/agents/models.md`'s
"Loading a level") is exactly what runs without the flag -- verified
byte-identical under `GLYPHENGINE_FIXED_FRAME_TIME=16.667ms` (see "What it
costs" below).

**Deciding WHICH nodes to instance is the example's call, not the engine's**
(AGENTS.md rule 14) -- `instancedGroupCandidate` in
`examples/22-level/main.go` reads the level's own `extras` vocabulary
(`docs/agents/models.md`'s "extras as the level's own vocabulary") to decide,
per doc mesh:

- **Shared**: `Model.MeshInstances(docMesh)` names more than one node, or
  there is nothing to batch.
- **Every node sharing it is tagged `{"static": true}`.** A set's placements
  only change through `UpdateInstanceSet`, which nothing in this example
  drives per frame, so a node that might move cannot safely be folded into
  one -- and "static" is already this engine's own word for "never moves"
  (`glyph.Static`'s doc comment), not a second vocabulary invented for the
  occasion.
- **None of the doc mesh's primitives is `alphaMode` `BLEND`.** `Translucent`
  does nothing on an `InstancedMesh` (see "Failure modes" above); excluding a
  blended doc mesh here keeps that failure from happening at all, rather than
  silently drawing a glass pane opaque.

A node that also carries `{"collider": "box"}` keeps its collider through a
companion entity (`spawnInstancedCollider`): `Transform` (via
`glyph.TransformFromMatrix`, same as the individual path), `Collider`,
`Static`, and deliberately **no `MeshRef`** -- the `InstanceSet` already drew
that node's geometry once, in the shared draw call, and a `MeshRef` on the
companion would draw it a second time on top of itself.

**What an instanced node loses that the companion collider does NOT give
back**: picking (an `InstanceSet` placement is not an entity, so there is no
way to ask "which building did a raycast hit" -- only "did it hit the set at
all"), and any other per-instance component a game might want to attach to
one building rather than the whole set. A physical obstacle is the only
thing this recipe restores; nothing here works around the rest, because
nothing in `InstanceSet` or `MeshInstance` has anywhere to put it.

**Shear is not the obstacle it looks like.** `MeshInstance.Model` is a full
4x4 matrix, not a Position/Rotation/Scale triple, so a sheared node's `World`
carries into an `InstanceSet` placement exactly -- unlike the individual
path, which has to fit the same matrix through `TransformFromMatrix` and
drops the shear when `exact` comes back false. `instancedGroupCandidate`
does not gate on shear at all for this reason; `TransformFromMatrix` is only
reached for a collider companion's own `Transform`, where a sheared
collider-tagged node loses exactly the part it always has on the individual
path. (Neither level file this example loads has a sheared node sharing a
mesh with anything else, so this is a property of the code, not something
either render below exercises.)

### With and without: the same picture

Screenshots at `GLYPHENGINE_FIXED_FRAME_TIME=16.667ms`, `-frames 90`, `cmp`
against the default (`-instanced` off):

- **Built-in level** (`examples/22-level/assets/level.glb`): byte-identical,
  `cmp` confirms. Both groups qualify (four buildings sharing one doc mesh,
  four lamp posts sharing a second, all tagged `static`) and both become one
  `InstanceSet` apiece -- checked directly, not assumed: three of the four
  buildings are rotated nodes, and running their `World` through
  `TransformFromMatrix` and back through `Transform.ModelMatrix()` (what the
  individual path does, and the instanced path does not -- see "Shear"
  above) shows the same non-zero round-trip noise the Blender fixture has
  below (`Building1`/`2`/`3` differ by 1.19e-7 to 2.38e-7 per matrix
  element). It simply never lands on a different MSAA sample at this
  camera's framing, so the two renders agree to the byte anyway -- a property
  of this one capture angle, not a guarantee.
- **Real Blender fixture** (`renderer/testdata/blender/level.glb`): 7 pixels
  of 921,600 differ (0.0008%), max single-channel delta 1/255, clustered at
  the base of the two `Building`/`Building_Linked` boxes -- the only pair in
  that file both shared and tagged `static`. Same root cause as above, just
  visible this time: `Building_Linked`'s node is NOT sheared
  (`TransformFromMatrix` reports `exact=true`), and its round-trip noise
  (up to 1.19e-7 per matrix element) crosses an MSAA sample boundary at this
  camera's framing where the built-in level's did not. This is the same
  class of last-bit MSAA-edge difference `19-instanced`'s own table
  documents (27 of 832,000 pixels, up to 15/255, from `HomogRotate3DY` vs
  `Transform`'s Euler composition disagreeing) -- smaller here because the
  two paths here agree on everything except one float32 round trip, not two
  independently-built rotations.

### What it costs, measured

The built-in level's eight shared props (four buildings, four lamp posts)
are all tagged `collider`, which the rule above allows -- the collider
companion keeps them working as obstacles -- and draw calls do drop, 10 to
4, but `cpu_drawlist + cpu_record` moves from 0.140 ms to 0.071 ms in a
single sample at that size, well inside the run-to-run noise the interleaved
methodology below measures at 261 props (0.446-0.695 ms). `19-instanced`'s
own table already established the threshold where this stops being noise is
around 300 props, so the measurement worth trusting is against a synthetic
level built for the purpose, NOT the committed one:

```
go run ./22-level/gen -big -buildings 100 -lamps 400 -out /scratch/biglevel.glb
go run ./22-level -level /scratch/biglevel.glb -frames 200 -camdist 180 -campitch 0.7
go run ./22-level -level /scratch/biglevel.glb -frames 200 -camdist 180 -campitch 0.7 -instanced
```

500 static, `collider`-tagged props (100 buildings sharing one doc mesh, 400
lamp posts sharing a second), camera framed so 261 of them clear the
frustum. 5 runs each, interleaved (individual, instanced, individual, ...,
not five-then-five, so a drifting machine load cannot land on only one
side), 200 frames, 1280x720, `GLYPHENGINE_TIMING=tsv`:

| | draw calls | instances drawn | triangles | cpu_drawlist+cpu_record (mean) | range | gpu_total (mean) |
| --- | --- | --- | --- | --- | --- | --- |
| individual | 261 | 261 | 3,144 | 0.574 ms | 0.446–0.695 ms | 0.161 ms |
| instanced | 4 | 502 | 6,036 | 0.123 ms | 0.071–0.171 ms | 0.161 ms |

Draw calls: 261 to 4 -- one per instanced doc mesh (2: buildings, lamp
posts), the ground (still its own individual entity, not a candidate: it is
the only node instancing its doc mesh), and one constant draw this example
always issues regardless of the level (the night sky). That constant is what
makes the arithmetic on the BUILT-IN level check out too: its non-instanced
run draws 10 (9 level entities, all in frustum, plus the same 1), and its
instanced run draws 4 (ground + 2 InstanceSets + the same 1) -- both matching
what was measured for it above, byte-identical picture included. `cpu_drawlist + cpu_record`
-- building the draw list and recording the command buffer, the cost this
removes, same metric `19-instanced`'s table uses -- drops by a mean of 0.45
ms, about 78%, consistent with that table's shape at this scale (0.7 ms
saved at 300 individually-drawn props there; this scene has fewer actually
in frustum but two doc meshes' worth of savings compounding).

**Instances drawn and triangles both roughly double (261 to 502; 3,144 to
6,036, a 1.92x ratio in each case), because culling is per SET, not per
placement** (see "Culling is per
set" above) -- every one of the 500 props draws once the set itself is in
frustum, not just the 261 that were individually visible. `gpu_total` does
not move against it (0.161 ms both ways, mean over the same 5 runs): these
are 12-triangle boxes (`boxMesh`'s own count -- 24 vertices, 36 indices), so
doubling the triangle count doubles a number too small to matter, the same
finding `19-instanced`'s own GPU table already made at a different scale.

**The honest cost, restated**: an instanced prop has no per-entity picking
and no per-entity component beyond the physical box `spawnInstancedCollider`
restores for a `collider`-tagged node. A node with neither `static` nor
`collider` extras, or one that needs to move or be looked up individually,
stays on the ordinary path -- this recipe batches what is provably safe to
batch and leaves the rest exactly as `docs/agents/models.md` already
describes it.

## Failure modes

- **The whole field vanishes when you look away from its centre.** The set's
  bound is wrong or stale. `UpdateInstanceSet` recomputes it; writing the mapped
  buffer some other way does not.
- **Every instance is drawn at the origin, or on top of the first one.** The
  per-instance vertex binding is not reaching the shader. A `mat4` attribute
  occupies four consecutive locations, so the model is at 4–7 and the tint at 8,
  not 5. `TestInstanceAttributeLayout` pins this.
- **Placements past the capacity silently disappear.** `UpdateInstanceSet`
  truncates rather than reallocating under a buffer the GPU may be reading.
  `InstanceSet.Capacity` reports the limit.
- **`Translucent` on the entity does nothing.** There is no blended instanced
  pipeline; the set stays opaque rather than silently losing its placements. See
  [`translucency.md`](translucency.md).

## Not done

GPU culling, indirect draws, and an automatic batcher that detects shared meshes
in the draw list and groups them. An explicit opt-in is the cheap 90%, and the
implicit version cannot be measured against the explicit one until the explicit
one exists — which it now does, with the table above as the baseline.

Per-instance CPU culling, for the same reason: the numbers say the off-screen
vertex work costs 0.008 ms at 900 props, and re-uploading a visible subset every
frame would cost more than that.

Instanced skinned meshes and instanced materials. Both are a pipeline and a
branch; neither has been needed.
