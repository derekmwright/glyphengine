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
  - glyphengine.Heightmap.WriteTo
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
assets: bundled
verified: 2026-09-19
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

`LoadHeightmap(fsys, name)` reads the same structure from a binary file. Like
every loader in the engine it takes an `fs.FS`, so an embedded heightmap and
one on disk are the same call. `(*Heightmap).WriteTo(w io.Writer)` is the
inverse — the only encoder the format has, shared by every writer rather than
each reimplementing the header order and endianness.

## File format

| Field | Type | Notes |
|---|---|---|
| `GridW` | `uint32` | grid columns, minimum 2 |
| `GridH` | `uint32` | grid rows, minimum 2 |
| `WorldW` | `float32` | world-space width, must be positive and finite |
| `WorldD` | `float32` | world-space depth, must be positive and finite |
| `OriginX` | `float32` | world-space X of the min corner |
| `OriginZ` | `float32` | world-space Z of the min corner |
| `Heights` | `GridW*GridH` × `float32` | row-major, `[z*GridW+x]`, immediately after the header |

All fields little-endian. 24-byte header, then the height data — nothing
else in the file. `LoadHeightmap` rejects a grid smaller than 2×2, larger
than 16384 per side (`maxHeightmapGridDim` in `heightmap.go` — see "Failure
modes" below for why), or a non-positive/non-finite world size; `WriteTo`
rejects the same three plus a `Heights` slice whose length does not match
`GridW*GridH`.

```go
f, err := os.Create("terrain.heightmap")
if err != nil {
    return err
}
defer f.Close()
if _, err := hm.WriteTo(f); err != nil {
    return err
}
```

## Converting a Blender terrain or a heightmap image

`cmd/heightmapconv` is the tool that actually produces a `.heightmap` file
from outside the engine — nothing else in the repo writes one. Three input
shapes:

```sh
# A sculpted mesh, rasterised top-down. -node is only required when the
# document has more than one mesh-bearing node (a real level file, terrain
# next to buildings, usually does).
go run ./cmd/heightmapconv -mesh terrain.glb -node Terrain -grid 129x129 -out terrain.heightmap

# -cell (metres per grid cell) instead of -grid, and an explicit bounds
# override instead of the mesh's own XZ extent:
go run ./cmd/heightmapconv -mesh terrain.glb -cell 2 -bounds -100,-100,100,100 -out terrain.heightmap

# A 16-bit greyscale heightmap PNG (World Machine, Gaea, a Blender
# displacement bake rendered to an image).
go run ./cmd/heightmapconv -image heights.png -world 200x200 -origin -100,-100 -range 0,40 -out terrain.heightmap

# A headerless uint16 grid (.r16 -- the World Machine/Gaea/Unity convention),
# little-endian unless -bigendian is passed.
go run ./cmd/heightmapconv -raw heights.r16 -rawsize 512x512 -world 200x200 -range 0,40 -out terrain.heightmap

# Sanity-check any .heightmap without writing code.
go run ./cmd/heightmapconv -info terrain.heightmap
```

Full flag reference:

| Flag | Applies to | Meaning |
|---|---|---|
| `-mesh` | mesh | sculpted terrain mesh (`.glb`/`.gltf`) |
| `-node` | mesh | node name; required if the document has more than one mesh-bearing node |
| `-grid WxH` | mesh | output grid size |
| `-cell N` | mesh | output grid spacing in metres, alternative to `-grid` |
| `-bounds minX,minZ,maxX,maxZ` | mesh | override; default is the mesh's own XZ extent |
| `-fill nearest\|min\|value:N` | mesh | fill samples that hit no surface instead of failing the run |
| `-image` | image | 16-bit (or 8-bit, with a terracing warning) greyscale PNG |
| `-raw` | raw | headerless uint16 grid |
| `-rawsize WxH` | raw | required with `-raw` |
| `-bigendian` | raw | `-raw` is big-endian instead of the format's usual little-endian |
| `-world WxD` | image/raw | world-space size in metres |
| `-origin X,Z` | image/raw | world-space origin (min corner), default `0,0` |
| `-range minY,maxY` | image/raw | what sample 0 and sample 65535 map to |
| `-out` | all | output `.heightmap` path |
| `-info` | all | print a header/min/max/mean summary of an existing `.heightmap` instead of converting |

It reads glTF directly with `github.com/qmuntal/gltf` rather than the
`renderer` package's loader: rasterising a mesh onto a grid needs no GPU, and
`renderer` pulls in the Vulkan bindings. Node world transforms (parent chain,
TRS-or-Matrix) are reimplemented against the document for the same reason —
see `cmd/heightmapconv/nodes.go`.

### Orientation

This is the thing every heightmap import gets flipped or mirrored once, so it
is worth pinning down exactly.

**Mesh path**: glTF's world space already matches the engine's — `renderer.LoadGLTF`
applies no axis conversion on load (docs/agents/blender-pipeline.md), and
Blender's own exporter bakes its Z-up→Y-up conversion into the scene's ROOT
node transform (`(x, y, z)` → `(x, z, -y)`, applied once at the root; every
non-root node's LOCAL transform is numerically unchanged — see
`cmd/heightmapconv/testdata/README.md` for the full derivation). So applying
a node's `World` matrix (parent chain composed down, exactly like
`renderer/gltf.go`'s `extractNodes`/`resolveWorld`) to its mesh's local
vertex positions lands them in the same world space the engine's own
`Heightmap` already uses — no separate "Blender axis" step. `OriginX`/`OriginZ`
are the mesh's minimum X/Z (or `-bounds`'s), exactly like `NewHeightmap`'s own
convention.

**Image/raw path** (no mesh, so no glTF world space to inherit): column 0
(leftmost pixel) stays at grid `x=0` (`OriginX`, no flip — left in the image
is the world's -X/west edge, the ordinary left-to-right reading of a map).
Row 0 (top of the image, first row in the file) maps to grid `z=GridH-1`
(`OriginZ+WorldD`, the FAR edge) — image viewers show row 0 at the top, and a
top-down map's "top" is conventionally the far/away direction from a viewer
standing at the near edge.

```
image (row 0 = top)              engine world (+X right, +Z up the page)
┌─────────────────┐              ┌─────────────────┐  z = OriginZ+WorldD (far)
│ row 0 (far, +Z)  │   ────►      │ ...     ...      │
│ ...              │              │ ...     ...      │
│ row H-1 (near)   │              │ ...     ...      │
└─────────────────┘              └─────────────────┘  z = OriginZ (near)
col 0 (left, -X) ──────────────────────► col W-1 (right, +X)
```

Both paths were pinned with an ASYMMETRIC test case (a single tall spike at
one known corner, a ridge along one axis only) rather than a symmetric hill,
which cannot catch an axis swap because it looks the same either way:
`cmd/heightmapconv/mesh_test.go`'s in-memory test and
`cmd/heightmapconv/blender_fixture_test.go`'s real-Blender-export test for
the mesh path (the latter's terrain object translated AND its parent Empty
rotated, so a converter that drops the node transform fails immediately, not
by luck), `cmd/heightmapconv/image_test.go`'s
`TestHeightsFromSamplesOrientation` for the image/raw path.

### Holes and overhangs

A heightmap has exactly one height per XZ column — a mesh does not have to.
The mesh path's rasteriser casts a vertical ray per grid sample and:

- **zero hits (a hole)**: fails the run by default, printing the count and
  up to 8 grid/world coordinates — a heightmap cannot have holes, and
  silently inventing ground under a player is how they fall through the
  world. `-fill nearest|min|value:N` opts in explicitly: `nearest` spreads
  the closest valid sample's height (a multi-source BFS from every valid
  cell at once), `min` uses the lowest valid height in the grid, `value:N`
  uses a fixed height.
- **more than one hit (an overhang, a cave, a bridge)**: takes the TOPMOST
  surface and reports the count as a warning — a heightmap cannot represent
  the rest, and the artist needs to know it was dropped, not have it
  silently picked for them.
- **exactly on a shared triangle edge or vertex**: neither of the above.
  `cmd/heightmapconv/raster_test.go`'s `TestBarycentricHeightOnSharedEdgesAndVertices`
  samples a flat two-triangle quad along its diagonal and at its corners —
  the classic rasteriser bug this guards against reports either a false hole
  (edge tolerance too tight) or a false overhang (the same hit counted once
  per triangle that shares the point, unmerged).

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

## Through the example

`07-terrain -heightmap assets/blender_terrain.heightmap` loads
`cmd/heightmapconv`'s conversion of the real-Blender fixture (see
`cmd/heightmapconv/testdata/README.md`) instead of generating terrain. That
fixture is a 10×10-unit patch chosen for exact hand computation, not scenery
— its single-vertex peak is 20 units tall (twice the whole patch's width),
so at `FPCamera`'s default 1.6-unit eye height with a level gaze the frame
reads as mostly sky above a thin strip of ground. Spawn point, initial
camera aim (toward the loaded grid's own bounds centre) and a downward pitch
all key off the loaded `Heightmap`'s `Bounds()` for exactly this reason —
see `examples/07-terrain/main.go`'s `heightmapPath` branches.

Run 2026-09-19 under `GLYPHENGINE_FIXED_FRAME_TIME=16.667ms`, `-frames 90`:
the frame shows lit, correctly-tinted grass-green terrain filling most of
the lower frame, sky and cumulus clouds above, and a visible near/far
grade — brighter, closer geometry in the lower-right against a hazier,
further plane behind it. Being honest about what this specific frame does
and does not show: the spawn point (20% in from the flat corner, per the
code above) sits almost exactly on the ridge's own crest row, so the near
bright geometry is very likely the ridge seen close up rather than the far
taller peak, which sits on the opposite edge of the patch from the ridge
and is not unambiguously identifiable by eye in this framing. The numeric
proof that the peak and ridge are in the geometrically correct world
position is `cmd/heightmapconv/blender_fixture_test.go`, not this
screenshot — the render confirms the pipeline runs end to end (loads,
builds a mesh, renders, textures/lighting apply correctly) rather than
independently re-confirming the coordinates the Go test already checked
by hand.

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
- **`LoadHeightmap` on a corrupt or truncated file.** Before this page's
  `verified` date, a header with `GridW`/`GridH` below 2 or a non-positive/
  non-finite world size loaded "successfully" into a `Heightmap` that then
  divided by zero or negative numbers the first time `HeightAt`/`TerrainMesh`
  touched it — not a load-time error, wrong ground (or a panic) somewhere
  else entirely. A `GridW`/`GridH` near `0xFFFFFFFF` (a plausible shape for a
  file that lost its header, or was handed a different format) made
  `int(GridW)*int(GridH)` overflow `int64` and either panicked in `make()`
  with "len out of range" or, for a pair that stayed positive after the
  overflow, attempted a multi-gigabyte allocation. All of these are now
  load-time errors — see `heightmap_test.go`'s
  `TestLoadHeightmapRejectsUndersizedGrid`/`RejectsAbsurdGrid`/`RejectsBadWorldSize`
  for the exact break/restore verification.
- **`cmd/heightmapconv -mesh` reports false holes on a mesh far from the
  origin.** Found on the real Blender fixture (`cmd/heightmapconv/testdata/blender_terrain.glb`,
  translated to world X~190-200 specifically to catch this): the
  point-in-triangle edge tolerance used to be a fixed `1e-7`, which is fine
  for a triangle near the origin but too tight once float32 (what every
  glTF-sourced vertex position and node transform actually are) loses
  precision proportional to coordinate magnitude rather than triangle size.
  17 of that fixture's 121 samples read as holes before `raster.go`'s
  `baryEdgeEpsRel` started scaling the tolerance by the largest coordinate
  magnitude involved.
