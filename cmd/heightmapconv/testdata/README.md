# blender_terrain.glb

Built and exported by `tools/blender/build_terrain_fixture.py`, the same
procedural-fixture pattern `tools/blender/build_fixture.py` uses for
`renderer/testdata/blender/level.glb` (see that file's own README for why a
script beats a committed `.blend`: it is diffable, and it is what a real
Blender actually wrote rather than what someone assumed it would write).

Built and exported on **Blender 5.0.1**, **2026-09-19**, with:

```
blender -b --factory-startup --python tools/blender/build_terrain_fixture.py -- cmd/heightmapconv/testdata/blender_terrain.glb
```

Regenerate it the same way if `build_terrain_fixture.py` changes.

## What it is for

`TestBlenderFixturePeakAndRidgeLandWhereHandComputationSays` (in
`blender_fixture_test.go`) is the "for real" half of the orientation proof
`docs/agents/terrain-heightmap.md` asks for issue #70: an asymmetric terrain
(a single-vertex spike at one named corner, a ridge along one axis only, flat
everywhere else -- a symmetric hill cannot catch an axis swap or a dropped
rotation, because it looks the same either way), run through a REAL Blender
export rather than a hand-written glTF document, with the terrain object
translated and its parent Empty rotated -- so a converter that ignores the
node's transform, or gets the Z-up-to-Y-up conversion backwards, fails
visibly rather than by coincidence.

## The scene, in Blender's own X/Y/Z

- `TerrainParent` (an Empty, the scene root -- no Blender parent of its own):
  location `(200, 0, 0)`, rotated 90 degrees about Blender's own Z axis
  (`rotation_euler = (0, 0, radians(90))`).
- `Terrain` (a mesh, parented to `TerrainParent`, `matrix_parent_inverse`
  left at identity -- "Keep Transform" was NOT used, the same choice
  `renderer/testdata/blender`'s `ShearChild` makes and for the same reason:
  the authored local numbers must be the actual composed local transform):
  location `(0, 0, 3)` in `TerrainParent`'s local space, identity rotation.
- `Terrain`'s mesh: an 11x11 grid of vertices at integer Blender X/Y from 0
  to 10 (unit spacing), Blender Z (height) given by
  `build_terrain_fixture.py`'s `height(ix, iy)`:
  - **flat** (Z=0) everywhere except:
  - a **ridge** running along Blender +X (constant across every X column --
    its long axis is the X axis) on rows Y=6..10: a tent profile peaking at
    Y=8 (height 4), falling to 2 at Y=6 and Y=10, 0 at Y=5 and below.
  - a single-vertex **peak** of height 20 at the corner (X=10, Y=0) -- the
    opposite Y edge from the ridge, and far taller than it, so the two
    features cannot be mistaken for each other.

## Hand-derived Blender -> engine world-space formula

This is worked out from first principles (not read off the export and
reverse-fitted) so the Go test is checking the converter against an
independent computation, not against itself. It was then verified against
the real file's actual node transforms and vertex data (see "Verified"
below) before being trusted.

Blender's Z-up -> glTF Y-up conversion is a constant change of basis,
`C = RotX(-90 deg)`, applied so that every node's WORLD matrix obeys
`WorldGLTF(node) = C * WorldBlender(node)`. Composing that recursively down
a parent chain (full derivation: for any node with a parent,
`WorldGLTF(parent) * LocalGLTF(node) = C * WorldBlender(parent) * LocalBlender(node)`,
and substituting `WorldGLTF(parent) = C * WorldBlender(parent)` cancels `C`
on both sides) gives a clean, general rule:

- A **root** node's local transform (which equals its world transform, since
  it has no parent) gets `C` applied: `LocalGLTF(root) = C * LocalBlender(root)`.
- Every **non-root** node's local transform is numerically **unchanged**:
  `LocalGLTF(child) = LocalBlender(child)`. This matches
  `docs/agents/blender-pipeline.md`'s empirical finding that a root-level
  light gets a baked-in "-90 degrees about X" rotation, and generalizes it.

`C = RotX(-90 deg)` applied to a point is exactly the permutation the issue
names: `(x, y, z) -> (x, z, -y)`.

Working through this fixture's two nodes (`R*T(t) = T(R.t)*R` is used twice,
the standard identity for pulling a rotation through a translation):

```
WorldGLTF(TerrainParent) = C * Translate(200,0,0) * RotZ_blender(90deg)
                         = Translate(200, 0, 0) * Rtotal        (C.(200,0,0) = (200,0,0))

  where Rtotal(x,y,z) = C(RotZ_blender(90deg)(x,y,z)) = C(-y,x,z) = (-y, z, -x)

WorldGLTF(Terrain) = WorldGLTF(TerrainParent) * Translate(0,0,3)   (Terrain's local, unchanged)
                   = Translate( Rtotal(0,0,3) + (200,0,0) ) * Rtotal
                   = Translate( 200, 3, 0 ) * Rtotal
```

so a Terrain-local mesh vertex `(vx, vy, vz)` lands at engine world position:

```
world = Rtotal(vx,vy,vz) + (200, 3, 0) = (-vy, vz, -vx) + (200, 3, 0)

world_x = 200 - vy
world_y = 3 + vz
world_z = -vx
```

Applied to this fixture's features:

| Feature | Blender local (vx, vy, vz) | Engine world (x, y, z) |
|---|---|---|
| Peak (corner X=10,Y=0) | (10, 0, 20) | (200, 23, -10) |
| Ridge crest (any X, Y=8) | (vx, 8, 4) | (192, 7, -vx) |
| Flat elsewhere (e.g. X=0,Y=0) | (0, 0, 0) | (200, 3, 0) |

Mesh XZ bounds: X in [190, 200] (vy 0..10), Z in [-10, 0] (vx 0..10).

Note the direction: world X DECREASES as vy increases (`world_x = 200 - vy`),
so on an 11-wide output grid over these exact bounds, grid index
`ix = worldX - 190 = 10 - vy` -- the vy=0 row (the peak's row) lands at the
FAR column, ix=10, not ix=0. Symmetrically `iz = 10 - vx`. Easy to get
backwards by eye; `blender_fixture_test.go`'s own `at()` helper got it wrong
on the first pass (assumed `ix=vy` directly) and every non-symmetric check
failed with the peak reading 5 instead of 23 until the flip was accounted
for -- which is exactly why this fixture uses an asymmetric mesh instead of
a hill: the bug was caught by a wrong NUMBER, not just a right-looking
picture.

## Verified

`cmd/heightmapconv/testdata`'s own real export matches every number above
exactly (`go test ./cmd/heightmapconv/ -run TestBlenderFixturePeakAndRidge -v`):
the converted grid's peak cell reads 23, its ridge cells read 5/6/7/6/5 (the
tent profile plus the +3 base offset), and every flat cell reads exactly 3.
See that test's own comments for the one thing this fixture found that the
hand derivation above does NOT predict: `baryEdgeEpsRel` in `raster.go` had
to become a coordinate-magnitude-scaled tolerance rather than a fixed
constant, because this fixture's ~200-unit world-space magnitude (chosen
specifically to be unlike every earlier near-origin synthetic test) exposed
float32 rounding a fixed absolute epsilon could not absorb -- 17 of 121
samples read as false holes before that fix.
