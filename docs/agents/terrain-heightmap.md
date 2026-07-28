---
id: terrain-heightmap
title: Terrain from a heightmap
summary: >
  Build a heightmap procedurally or load one from disk, use it as O(1)
  collision, and generate the matching renderable mesh from the same grid.
capability: terrain
status: stable
since: v0.2.0
api:
  - glyphengine.Heightmap
  - glyphengine.NewHeightmap
  - glyphengine.LoadHeightmap
  - glyphengine.Heightmap.HeightAt
  - glyphengine.Heightmap.NormalAt
  - glyphengine.Heightmap.HeightAtRayDown
  - glyphengine.Heightmap.Bounds
  - glyphengine.Scene.SetTerrain
  - glyphengine.TerrainMesh
  - glyphengine.TerrainOptions
  - glyphengine.Engine.CreateTerrainMesh
  - glyphengine.SplatTiles
example: examples/07-terrain
run: task example:07-terrain
requires:
  - cgo
  - vulkan-runtime
assets: none
verified: 2026-07-28
---

# Terrain from a heightmap

A `Heightmap` is one grid serving two purposes, which is what keeps what you
see and what you stand on from drifting apart:

- **collision** — `Scene.SetTerrain` makes it the ground. Height lookups are
  O(1) bilinear samples, so the character controller snaps to the surface
  without raycasting against terrain triangles.
- **geometry** — `TerrainMesh` builds vertices and indices from the same grid,
  with normals from the heightmap's own central differences.

```go
heights := make([]float32, 129*129) // fill however you like
hm, err := glyphengine.NewHeightmap(
	129, 129,          // grid resolution
	200, 200,          // world size
	-100, -100,        // world-space origin (min corner)
	heights,
)
if err != nil {
	return err
}
e.SetTerrain(hm)

mesh, err := e.CreateTerrainMesh(hm, &glyphengine.TerrainOptions{Tint: tint})
if err != nil {
	return err
}
ent := e.Spawn()
e.C.Transform.Set(ent, &glyphengine.Transform{Scale: mgl32.Vec3{1, 1, 1}})
e.C.MeshRef.Set(ent, &glyphengine.MeshRef{Mesh: mesh, Roughness: 0.95})
```

Full program: `examples/07-terrain`, which generates its heightmap from
value-noise fBm and loads nothing from disk.

## Layout

Heights are `[z*GridW + x]`, row-major. The grid covers the world rectangle
from `(OriginX, OriginZ)` to `(OriginX+WorldW, OriginZ+WorldD)`. Grid spacing is
`WorldW/(GridW-1)` by `WorldD/(GridH-1)`.

`LoadHeightmap` reads the same structure from a binary file:
`gridW(u32) gridH(u32) worldW(f32) worldD(f32) originX(f32) originZ(f32)` then
`gridW*gridH` little-endian `f32` heights.

> Loading is currently a path-relative `os.Open`. An `fs.FS`-based asset layer
> lands in a later phase; until then the working directory matters.

## Terrain generation is a game concern

The engine has no noise functions and no terrain generator. Every game wants a
different one, and a mediocre built-in generator is worse than none. Generate
the `[]float32` however you like and hand it to `NewHeightmap`.

## Coloring

`TerrainOptions.Tint` colors each vertex from its height and normal — the usual
grass/rock/snow rule:

```go
func tint(height float32, normal [3]float32) [3]float32 {
	if slope := 1 - normal[1]; slope > 0.45 {
		return [3]float32{0.42, 0.40, 0.38} // rock on anything steep
	}
	if height > 10 {
		return [3]float32{0.92, 0.93, 0.95} // snow
	}
	return [3]float32{0.24, 0.48, 0.22} // grass
}
```

Nil `Tint` means flat white, which the lit pipeline renders untinted.

For textured terrain, attach a `MaterialRef` with a `Terrain` splat material —
that routes the entity through the multi-texture blend pipeline instead of the
single-texture lit one. `TerrainMesh` already emits UVs at `SplatTiles` (10)
repeats across the extent, which is what that shader expects.

## 32-bit indices

`TerrainMesh` returns `[]uint32` and uploads through `CreateIndexedMesh32`,
because terrain grids routinely exceed 65,536 vertices — a 256×256 heightmap is
exactly 65,536, right at the `uint16` boundary.

## Bounds behavior

`HeightAt` returns `ok == false` outside the grid. The character controller
treats that as "no terrain here" and falls back to raycasting against colliders,
so a character walking off the edge of a heightmap does not stop — it falls.

The usual fix is a radial falloff that drops the terrain to zero before the
edge, plus an invisible wall, so players never reach the boundary. `07-terrain`
does the falloff.

## Failure modes

- **Character stands slightly inside the ground.** The controller snaps to
  `groundY + halfHeight + 0.001`, using `Collider.HalfExtents.Y * Scale.Y`. A
  half-height that does not match the visual model puts the feet in the wrong
  place.
- **Terrain renders but you fall through it.** `SetTerrain` was never called —
  the mesh is only geometry. Collision comes from the `Heightmap` object.
- **Terrain is invisible from one side.** Backface culling. `TerrainMesh` winds
  its triangles to match `CreatePlane`; do not flip them.
- **`NewHeightmap` returns an error.** `len(heights)` must be exactly
  `gridW*gridH`, the grid must be at least 2×2, and world size must be
  positive.
