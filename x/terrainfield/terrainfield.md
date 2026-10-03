---
id: x-terrainfield
title: An island heightmap from value-noise fBm
summary: >
  Generate the island terrain 07-terrain and 25-lod-forest render, or the ridge
  29-ridge measures occlusion against, or load a sculpted .heightmap in their
  place, for Scene.SetTerrain and CreateTerrainMesh.
capability: terrain
status: stable
since: v0.1.0
api:
  - terrainfield.Load
  - terrainfield.Ridge
example: examples/07-terrain
run: task example:07-terrain
requires:
  - cgo
  - vulkan-runtime
assets: procedural
verified: 2026-10-02 # Ridge and its occlusion property; moved out of examples/internal; field digests pinned against the pre-move generator
---

# An island heightmap from value-noise fBm

This is an `x` package, not an engine one. The engine owns
`glyphengine.Heightmap` -- the grid, the O(1) height query, the `.heightmap`
format, the mesh built from the same grid -- and this package owns one
*particular island* on top of it. See
[ADR 0012](../../docs/adr/0012-an-x-module-for-opinionated-systems.md) for why
that split exists, and [`terrain-heightmap.md`](../../docs/agents/terrain-heightmap.md)
for everything the engine side can do.

```go
import (
	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/x/terrainfield"
)

func (g *game) Init(e *glyph.Engine) error {
	// "" generates the island; a path loads that .heightmap instead and the
	// seed is ignored.
	hm, err := terrainfield.Load("", 1)
	if err != nil {
		return err
	}
	e.SetTerrain(hm) // collision and HeightAt

	mesh, err := e.CreateTerrainMesh(hm, &glyph.TerrainOptions{})
	if err != nil {
		return err
	}
	ent := e.Spawn()
	e.C.Transform.Set(ent, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: mesh, Roughness: 0.95})
	return nil
}
```

The heightmap and the mesh are two separate things on purpose: `SetTerrain`
gives the scene collision and `HeightAt`, and `CreateTerrainMesh` is what draws
it. A game that wants terrain collision under its own geometry calls only the
first. See [`terrain-heightmap.md`](../../docs/agents/terrain-heightmap.md).

Both arguments matter:

- **`path`** names a `.heightmap` file produced by `cmd/heightmapconv` from a
  sculpted mesh or a heightmap image. When it is non-empty the generator is not
  run at all and `seed` is ignored. The escape hatch is here so a game can swap
  a sculpted terrain in without the call site branching;
  `task smoke` exercises it with `-heightmap 07-terrain/assets/blender_terrain.heightmap`.
- **`seed`** selects the lattice. Both examples default to 1, which is therefore
  the island every committed screenshot and every GPU gate renders. `-seed 7` in
  07-terrain is a different island.

## What the opinion is

A 129x129 grid over 200x200 world units centred on the origin, five octaves of
value noise at halving amplitude and doubling frequency, a radial falloff
starting at 0.35 of the half-diagonal, and a 14-unit height scale. Change any of
those and you have a different terrain, not a better one.

The falloff is the one part that is load-bearing rather than aesthetic:
`Heightmap.HeightAt` stops returning ground past the heightmap bounds, so a
player walking to the edge of a terrain that is still high there falls through
the world. The falloff brings the border samples to exactly zero, which keeps
them away from it. `TestIslandEdgesDropToZero` pins that, with a centre sample as
the control so it cannot pass on an empty field.

## The other shape: Ridge

`Ridge(seed)` is a second opinion in the same package: a crest 26 units high with
a 46-unit cosine profile running along X at z = 0, two octaves of the same value
noise for texture, and the island's radial falloff. Same grid, same world extent,
same determinism.

It is here rather than in the example that uses it because it is a terrain shape,
which is what this package holds, and because what it is for outlives one
example: ground that definitely hides what is behind it. `examples/29-ridge`
measures the GPU LOD occlusion test on it, and an occlusion measurement is void
on an island -- nothing on an island reliably hides anything from a camera low on
its flank, so both arms of the comparison would be controls.

`TestRidgeHidesItsFarFlank` is the property, checked as geometry rather than as a
picture: from an eye 2 m over the near flank, the sight line to a point 6 m over
a far-flank sample (a tree top, which is what the bench hides) must pass below
the crest. Verified by breaking it three ways; the numbers are in the test.

## Determinism

Deterministic for a given seed by construction. The lattice is hashed from the
integer coordinates and the seed rather than drawn from a stateful generator, so
the same seed produces the same `[]float32` regardless of platform or evaluation
order -- which is what lets `task determinism` and `task screenshots` compare
bytes at all.

`terrain_test.go` digests the generated field for seeds 1 and 7. The two digests
were taken from the generator as it stood at `examples/internal/terrainfield`,
run out of `git show` rather than out of the moved file, so they are evidence
that the move to this module changed nothing rather than a restatement of what
the moved code does. Verified by breaking it, 2026-10-02: `heightScale` 14.0 to
14.1 fails both subtests, and so does raising the octave count from 5 to 6.

## Failure modes

- **Passing a path that does not exist** returns a wrapped error naming it.
  `Load` splits the path and reads through `os.DirFS`, so a bare filename is
  resolved against the process working directory -- which for an example run by
  `task example:07-terrain` is the example's own directory, not the repository
  root.
- **Calling `SetTerrain` and forgetting `CreateTerrainMesh`** gives a scene with
  working ground collision and nothing drawn: the player walks on an invisible
  island. There is no error, because both halves are legitimate on their own.
- **Changing the generator** does not fail visibly at run time. It changes four
  gates' worth of images, and `terrain_test.go` is the cheap place to find out
  rather than a PNG diff.
