---
id: grass
title: Instanced grass and flora
summary: >
  Scatter flora meshes across a heightmap as GPU instances, then control what
  it costs with GrassLOD -- density thinning, a cull distance, and an optional
  swap to baked billboards. The default is the heaviest pass the renderer draws.
capability: environment
status: stable
since: v0.3.0
api:
  - renderer.Renderer.InitGrass
  - renderer.GrassModelSpec
  - renderer.GrassDensityMask
  - renderer.NewDensityMask
  - renderer.GrassLOD
  - renderer.DefaultGrassLOD
  - renderer.Renderer.SetGrassLOD
  - renderer.Renderer.GrassLOD
  - renderer.Renderer.GrassImpostorAtlas
  - renderer.GrassMaxDistance
assets: glTF flora meshes with a baseColorTexture
example: examples/08-grass
run: task example:08-grass
verified: 2026-08-04
---

# Instanced grass and flora

```go
mask := renderer.NewDensityMask(-worldSize/2, -worldSize/2, worldSize, worldSize, 1.0)
mask.ClearCircle(0, 0, 1.5, 1.5) // no grass where the player spawns

r.InitGrass(assetsFS, hm, hm.OriginX, hm.OriginZ, hm.WorldW, hm.WorldD,
	[]renderer.GrassModelSpec{
		{Path: "assets/flora/Grass_Common_Short.gltf", Weight: 40},
		{Path: "assets/flora/Grass_Common_Tall.gltf", Weight: 26},
		{Path: "assets/flora/Grass_Wispy_Short.gltf", Weight: 20},
		{Path: "assets/flora/Grass_Wispy_Tall.gltf", Weight: 14},
	}, mask)
```

Instances are scattered deterministically from a hash of their world cell, so
the same seed and heightmap always produce the same field, and placement costs
nothing to store. `Weight` biases the mix between variants. The mask is
optional — pass `nil` for uniform coverage.

Each variant owns one instance buffer, ordered by tile so every tile is a
contiguous draw range. Tiles are 16 world units, and the draw loop culls them
against the frustum and the cull distance, then sorts near to far.

## It is the most expensive thing in the frame

On `08-grass` at 1920x1080, 166k instances across 4 variants:

| | grass pass |
|---|---|
| default | 2.6 ms |
| MSAA 4x → 1x | 3.05 → 1.36 ms |
| 1920x1080 → 640x360 | 3.70 → 1.97 ms |

Nine times the pixels for under twice the cost: the pass is **primitive-bound,
not fill-bound**. A blade is ~340 triangles and most of them are smaller than a
pixel at any distance, and a sub-pixel triangle still shades a full 2x2 quad —
four fragments of work for less than one pixel of coverage. That is why the
knobs that matter are the ones that remove *geometry*, and why MSAA costs so
much here (per-sample coverage on thin slivers).

## GrassLOD

```go
lod := r.GrassLOD()
lod.MaxDistance = 120
lod.ImpostorDistance = 40
r.SetGrassLOD(lod)
```

Every field is a world-space distance except `ThinMin`, which is a fraction.
Zero means "unset" on all of them — a zero field takes the default rather than
disabling the feature, so a partially-filled struct is safe.

| Field | Default | Effect |
|---|---|---|
| `MaxDistance` | 80 | Hard cull. Tiles past it are not drawn at all. |
| `FadeStart` | 50 | Where blades begin shrinking and dissolving toward the cull. |
| `ThinNear` | 30 | Where density starts dropping. |
| `ThinFar` | 70 | Where density reaches `ThinMin`. |
| `ThinMin` | 0.35 | Fraction of instances still drawn at `ThinFar` and beyond. |
| `ImpostorDistance` | 0 (off) | Past this, tiles draw as billboards instead of meshes. |

Thinning works because instances are shuffled within a tile at build time, so
drawing a prefix is a uniform sample of the tile rather than clearing one side
of it.

`GrassMaxDistance` is the default cull as a constant. It is the right figure for
sizing a world: an island much smaller than 80 units has its grass culled before
its far edge.

## Impostors

Off by default. When `ImpostorDistance` is set, tiles past it draw as
camera-facing quads textured from an atlas baked at load — one cell per variant,
256px square, unlit albedo and coverage.

Measured on `08-grass` under a fixed frame clock, against a two-run noise floor
of RMS 0.0094:

| `ImpostorDistance` | grass pass | image vs meshes |
|---|---|---|
| off | 2.557 ms | — |
| 25 | 0.722 ms | 0.025 — the far field reads chunkier |
| 40 | 1.643 ms | 0.017 |
| 60 | 2.376 ms | 0.009 — at the noise floor |

It is a dial, not a free win. A billboard presents its full width where a thin
blade was often seen edge-on, so a field of impostors reads denser than the
meshes it replaces, and the wide dry variants gain most — which is why the far
field also shifts slightly yellower. At night the difference falls to the noise
floor at every distance: what separates the two is mostly how they catch direct
sun.

The far field is also **steadier in motion**, which a screenshot will not show.
Between consecutive frames at distance 25 the far band changes 36% less (RMS
0.046 → 0.030) while the near band, meshes either way, is unchanged. Part of
that is billboards shearing where meshes bend, so read it as less motion *and*
less shimmer, not shimmer alone.

Note the tile granularity: tiles are 16 units, so 15 and 25 select nearly the
same set and cost nearly the same. Pushing below ~15 starts replacing grass
close enough to see, and the near field gets *less* steady, not more — a
billboard's whole quad shifts in the wind where a mesh bends internally.

`GrassImpostorAtlas()` returns the baked atlas as an image. An impostor framed
wrong is invisible in a field and obvious in the atlas, so dump it before
disbelieving the bake:

```
GLYPH_DUMP_IMPOSTOR=atlas.png go run ./08-grass
```

## Wind

Wind is per-instance phase times height up the blade, in the vertex shader.
Nothing drives it from the CPU and nothing is stored per blade.

Impostors use the identical two lines, because a quad has both a per-instance
position and a height — which is all the model needs. The mesh bends as a curve
across its vertices where the quad shears, and at impostor range that difference
is well under a pixel.

## The flora vertex-colour gradient, and centroid

Flora assets carry `COLOR_0` as a greyscale base-to-tip gradient, dark at the
root and bright at the tip, and glTF requires it be multiplied into base colour.
The palette texture supplies hue, the vertex colour supplies shading — which is
why these textures look like colour strips rather than pictures of grass.

`grass.vert` and `grass.frag` declare that varying **`centroid`**, and it is
load-bearing. With MSAA, a pixel-centre sample can land outside a sub-pixel
triangle; the attribute is then *extrapolated* rather than interpolated, past
both ends of the authored 0..1 range. Past the bottom it goes negative, and a
negative albedo shades to a black pixel — thousands of dark specks peppering the
horizon, worst in daylight where they sit against bright grass. Counted in the
horizon band on `08-grass`:

| | morning | noon | night |
|---|---|---|---|
| without `centroid` | 1105 | 1838 | 3516 |
| with | 8 | 7 | 272 |

It costs 0.011 ms on the grass pass — inside run-to-run noise. Extending it to
`fragUV` and `fragFade` buys nothing measurable and costs 0.10 ms, so those stay
plain.

If you author flora with a vertex-colour gradient and see speckle at distance,
this is the first thing to check.

## Failure modes

- **The field is empty.** `InitGrass` needs the heightmap to answer
  `HeightAt` over the region given. Instances whose ground height is unknown
  are skipped silently, so an origin or extent that disagrees with the
  heightmap scatters nothing.
- **Grass disappears before the horizon.** That is `MaxDistance`, 80 by
  default. Raise it and expect the pass to cost more than linearly — the
  culled tiles are the cheap ones.
- **Impostors look wrong but the atlas looks right.** The bake and the draw
  share `grass.frag`; what differs is the vertex stage. Check the atlas cell
  index and the billboard size, which ride in push-constant slots the grass
  path leaves unread (`pcImpostorCell`, `pcImpostorWidth`, `pcImpostorHeight`
  in `renderer/commands.go`). Writing a slot the lighting uses relights the
  billboards without touching the meshes, silently.
- **Comparing two grass renders shows a difference you cannot explain.** Wind
  and clouds run on wall-clock. Set `GLYPHENGINE_FIXED_FRAME_TIME=16.667ms` or
  the difference may be the weather. See `game-loop`.
