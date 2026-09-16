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
example: examples/19-instanced
run: task example:19-instanced
requires:
  - cgo
  - vulkan-runtime
assets: none
verified: 2026-09-16
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
