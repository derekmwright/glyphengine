---
id: blender-pipeline
title: Export a level from Blender the way this engine expects
summary: >
  Blender is the reference world-building pipeline: the exact export
  settings a level needs, a script that sets them for you, and what a real
  Blender export turns into once loaded.
capability: rendering
status: stable
since: v0.5.0
api:
  - renderer.LoadGLTF
  - renderer.Model
  - renderer.ModelNode.Extras
  - renderer.Model.Node
  - renderer.Model.NodeMeshes
  - renderer.ModelLight
  - renderer.Model.LightWorldPosDir
  - glyphengine.TransformFromMatrix
example: examples/22-level
run: task example:22-level
requires:
  - cgo
  - vulkan-runtime
assets: bundled
verified: 2026-09-19
---

# Export a level from Blender the way this engine expects

The engine still reads only the open glTF format (AGENTS.md rule 14 -- it
does not gain a Blender-specific loader, an add-on, or an engine-specific
property schema). "Blender is the reference pipeline" means something
narrower and more useful: the recipe on this page, `tools/blender/export_level.py`,
`tools/blender/build_fixture.py`, and `renderer/testdata/blender/level.glb`
are checked against what a real, installed Blender actually writes, so the
facts below are measurements, not assumptions about the format.

Everything on this page was checked against Blender 5.0.1 -- the exporter
source in that install (`io_scene_gltf2` under
`Blender 5.0/5.0/scripts/addons_core/`), a script run through it, and the
resulting file's real bytes read back with `renderer`'s own decoder. Where a
claim differs from what an older Blender's exporter would do, this page says
so and marks it unverified rather than assumed -- see "Verified against"
below for exactly what could and could not be checked on this machine.

## The recipe: three boxes and three more good habits

Blender's own glTF exporter ships with **Include > Custom Properties**,
**Include > Punctual Lights** and **Include > GPU Instances** all **OFF**.
Confirmed by reading the running exporter's own RNA
(`bpy.ops.export_scene.gltf.get_rna_type().properties`) on Blender 5.0.1:
`export_extras`, `export_lights` and `export_gpu_instances` all report
`default=False`. A plain File > Export > glTF 2.0 therefore still succeeds,
still opens in the engine, and silently has **no `extras` on any node and no
lights at all** -- not an error, just an empty `Model.Lights` and every
`ModelNode.Extras` nil, which reads as "the level has no lamps" rather than
"the export dropped them" (see the "Loading a level" section of
[`models.md`](models.md)).

`tools/blender/export_level.py` sets six options so they cannot be forgotten
one at a time:

| Exporter property | Set to | Why |
|---|---|---|
| `export_extras` | `True` | OFF by default. Custom properties are the tagging mechanism (see below) -- without this every `ModelNode.Extras` is nil. |
| `export_lights` | `True` | OFF by default. Without this `Model.Lights` is empty; no lamp in the scene reaches the file. |
| `export_gpu_instances` | `True` | OFF by default. Lets Blender's own instancer flag (particle/geometry-nodes duplicators the exporter recognises as instanced) reach the file as `EXT_mesh_gpu_instancing` rather than being silently baked into N separate meshes. The engine does not read the extension yet (issue #71) -- see "Instancing" below for what actually triggers it in practice, which turned out to be neither of this fixture's two instancing probes. |
| `export_yup` | `True` | Already the exporter's own default on every version checked; set explicitly so this recipe does not depend on that default never changing. |
| `export_import_convert_lighting_mode` | `'SPEC'` | Already the exporter's own default. This is what makes a 1000 W spot arrive as 54351.4 candela -- see "Light units" below. Set explicitly for the same reason as `export_yup`. |
| `export_apply` | `True` | OFF by default ("Apply Modifiers"). An unapplied modifier (mirror, subdivision) exports its CAGE, not its result -- this is the toggle that keeps the file matching the viewport. |

The script does not try/except its way across Blender versions -- it asks
`bpy.ops.export_scene.gltf.get_rna_type().properties` which of these six
exist on the Blender actually running, applies only those, and **prints**
what it applied and what it could not find. On 5.0.1 all six exist:

```
export_level: Blender 5.0.1 -> level.glb
export_level: applied: export_extras, export_lights, export_gpu_instances, export_yup, export_import_convert_lighting_mode, export_apply
```

Run it headless:

```sh
blender -b level.blend --python tools/blender/export_level.py -- out.glb
```

or from Blender's own Scripting tab: open the file, hit Run Script, then
call `export_level("/path/to/out.glb")` from the console. Running the file
with no `-- out.glb` on the command line -- which is what loading it into
the text editor looks like -- only prints usage; it never exports as a side
effect of being opened. It is also a plain importable module
(`from export_level import export_level`), which is how
`tools/blender/build_fixture.py` drives it without shelling back out to a
second Blender process.

`export_level` raises (and the command-line entry point turns into a
non-zero exit) on anything other than a clean `{'FINISHED'}` result plus a
file that actually exists afterward -- printing a warning and continuing is
exactly the silent-failure shape this script exists to remove.

## Units

**Verified:** the engine's docs and examples treat 1 world unit as
approximately 1 metre by convention -- [`lights.md`](lights.md) describes
its froxel grid as starting "1 m" out and a test camera as "1.31 m above" a
hex grid, and `1.6` shows up repeatedly across examples
(`examples/22-level/main.go`, `examples/12-particles/main.go`,
`examples/09-water/main.go`) as eye height, the ordinary approximation for a
standing person in metres. Blender's own default unit scale is metric with
1 Blender unit = 1 metre, and `export_yup`/`export_apply` do not rescale
anything, so a level built at Blender's default scale arrives at the
engine's own approximate "1 unit = 1 m".

**Not derived from gravity, on purpose:** `DefaultGravity` (`scene.go`) is
`20.0` world units/s², not real-world 9.8 m/s² -- a game-feel choice, not a
unit calibration. AGENTS.md's "measure, do not assume" rule is exactly why
this page does not cite it as evidence for the metre claim above.

## Custom properties are the tagging mechanism

A Blender custom property (`obj["collider"] = "box"`, the N-panel's Item >
Custom Properties, or Python) is glTF's own `extras` on that node once
exported. `renderer.LoadGLTF` hands it back as `ModelNode.Extras`
(`json.RawMessage`, `nil` when the node has none) and the engine
deliberately never looks inside it (AGENTS.md rule 14) -- what the keys
mean is the consuming game's vocabulary. `examples/22-level/main.go`'s
`nodeTags` struct is a worked example: `{"static": true, "collider": "box",
"floors": 3}` on a building, `{"spawn": "player"}` on an otherwise-empty
node.

Mixed JSON types round-trip correctly through a real export -- confirmed by
`renderer/testdata/blender/level.glb`'s `Building` node, which carries a
string (`collider`), a bool (`static`) and a number (`floors`) together, and
`renderer/gltfblender_test.go`'s `NodesAndExtras` check, which unmarshals
all three from the real file.

## Empties as sockets and spawn points

An Empty with no mesh becomes a `ModelNode` with `Mesh == -1` --
`Model.NodeMeshes` correctly returns nothing for it, and `Model.Node(name)`
finds it by exact name (issue #46; see "Finding a point on the model" in
[`models.md`](models.md)). This is how a level marks a spawn point, a socket,
a light's own placement (the SPOT/POINT lights in this fixture are each
their own Empty-like light-data object) without adding geometry that would
otherwise need hiding or a special material.

## Alt-D vs Shift-D: which one shares the mesh

Blender's two duplicate commands produce very different glTF:

- **Alt-D ("Duplicate Linked")** -- Python: `obj2 = obj.copy()`, which does
  **not** duplicate `obj.data`; the copy's mesh datablock is the same ID as
  the original's. This is the shape `Model.NodeMeshes` exists for: several
  nodes instancing ONE doc mesh (see "Placing every instance, not just the
  first" in [`models.md`](models.md)). `renderer/testdata/blender/level.glb`'s
  `Building`/`Building_Linked` pair is built exactly this way
  (`tools/blender/build_fixture.py`'s `build_building()`), and
  `renderer/gltfblender_test.go`'s `SharedMeshAndDuplicate` check confirms
  both nodes reference the same doc mesh and that `Model.NodeMeshes` returns
  the same primitive for both.
- **Shift-D ("Duplicate Objects")** -- Python: `obj2 = obj.copy(); obj2.data
  = obj.data.copy()`. Each copy gets its own mesh datablock, so this
  exports as two independent doc meshes -- the case
  `ModelMesh.Node`/`Model.NodeMeshes` were never built for, because there is
  no sharing to place correctly.

Either way, a mesh-sharing situation this fixture found that is **neither**
of the above: a **collection instance** (Blender's "Add > Collection
Instance", or an Empty with `instance_type = 'COLLECTION'`) also ends up as
several nodes referencing one doc mesh, but through a THIRD mechanism --
see "Instancing" below.

## Apply Scale, and why a level artist should

A rotated child object inside a non-uniformly-scaled, **unapplied** parent
is sheared in world space once the hierarchy is composed. glTF's node graph
has no way to represent that either -- a node is Translation/Rotation/Scale
or a Matrix, never a shear term -- so the *file* carries it correctly (as a
Matrix or as nested TRS that compose into a shear), and it is
`glyphengine.TransformFromMatrix` (root package; a renderer test cannot call
it, see below) that cannot: `exact` comes back `false`, and the entity is
drawn with the shear silently dropped. `examples/22-level/main.go` logs this
per node it happens to
(`level: node %q is sheared or has a zero scale; it is drawn without that
part of its transform`).

`renderer/testdata/blender/level.glb`'s `ShearParent` (scale `(1, 1, 3)`,
unapplied) / `ShearChild` (rotated 45 degrees about X inside it) pair
exists to exercise exactly this, end to end: running

```
go run ./22-level -level ../renderer/testdata/blender/level.glb
```

logs `level: node "ShearChild" is sheared or has a zero scale; it is drawn
without that part of its transform` -- confirmed by an actual run under
`GLYPHENGINE_FIXED_FRAME_TIME=16.667ms` on 2026-09-19.

The fix in Blender is **Object > Apply > Scale** on the parent before
exporting -- the node's name (from the log line) is how an artist finds
which object to fix. `renderer/gltfblender_test.go` cannot check this
specific case with hand-computed numbers the way it checks
`Building_Linked`'s TRS: `TransformFromMatrix` lives in the root
`glyphengine` package, which imports `renderer`, so a renderer test cannot
call it without an import cycle (do not restructure packages to work around
this -- see the root of this repository's `go.work` comment on why the two
modules are split). The example run above is the verification instead.

`glyphengine.WorldAABB` (`physics.go`) is the other place this matters: it
computes a collider's box from `Transform.Scale` and explicitly **ignores
rotation** ("Rotation is ignored (axis-aligned assumption)", per its own
comment) -- so a `Collider` on a sheared node is sized from
`TransformFromMatrix`'s already-shear-dropped scale, not the visual shear.
Nothing new to do about this; it is a second, independent reason (on top of
the drawn geometry) that Apply Scale matters before a shape ships in a
level.

## Mirrored objects

A negative scale on one axis (or an applied Mirror modifier) is
representable in glTF and comes back **exact** through
`TransformFromMatrix` -- unlike shear, nothing is dropped. What is worth
knowing: **Blender's own exporter does not necessarily encode a mirror as
"one negative scale component, identity rotation."**

`renderer/testdata/blender/level.glb`'s `Mirrored` object is authored in
Blender with scale `(-1, 1, 1)` (X mirrored, unapplied -- Object > Apply >
Scale would remove the exact thing this probes) and identity rotation. The
real exported node instead carries scale `[-1, -1, -1]` (all three negative)
and rotation `[1, 0, 0, 0]` (a 180 degree turn about X). Both are valid
factorisations of the SAME 3x3 linear map -- `Rx(180) * diag(-1,-1,-1) =
diag(-1, 1, 1)`, confirmed by hand -- because Blender's own matrix
decomposition (`mathutils.Matrix.decompose()`, which the exporter uses to
pull TRS back out of the evaluated world matrix) is free to split a
reflection between rotation and scale however it likes; a quaternion alone
cannot represent a reflection, so something has to carry the sign, and which
something is an implementation detail of the tool that wrote the file, not
a property of the mirror itself.

`renderer/gltfblender_test.go`'s `Mirrored` check reflects this: it asserts
the composed World matrix's determinant is negative (the one fact every
valid decomposition of the same mirror agrees on) rather than checking any
single TRS field.

`glyphengine.TransformFromMatrix` re-decomposes the matrix its OWN way
regardless of how the source file encoded it, and it always normalizes the
mirror onto `Scale.X` (see its doc comment and source: "A reflection has no
Euler angles. Put the mirror in the scale, on X, and what is left is a
rotation again.") -- so a game reading `Model.Nodes[i].World` through
`TransformFromMatrix` never has to care which of the (at least) two
equally-valid encodings a given exporter chose. `glyphengine.WorldAABB`
takes `abs()` of scale for the same reason ("a negative [scale] is a
mirror, and the box around a mirrored box is the same box" -- its own
comment): left signed, a mirrored collider would have `Min` above `Max` on
that axis, which overlaps nothing.

## Backface culling -> doubleSided

Confirmed by reading the Blender 5.0 install's exporter source
(`io_scene_gltf2/blender/exp/material/materials.py`,
`__gather_double_sided`): a material's `doubleSided` is written `true`
**unless** `material.use_backface_culling` is ticked, in which case the
field is omitted (glTF's own default for an absent `doubleSided` is
`false`). `renderer.LoadGLTF` already reads this into `ModelMesh.DoubleSided`.
`renderer/testdata/blender/level.glb`'s `Culled` material (Backface Culling
ON) and `Stone`/`Ground`/`Open` (OFF, the exporter's default) are the probe;
`renderer/gltfblender_test.go`'s `AlphaAndDoubleSided` check confirms both
sides.

Also confirmed from the same file: **alphaMode is no longer read from the
old Eevee `blend_method` property.** The exporter's own comment
(`search_node_tree.py`) says so explicitly -- "Alpha mode is determined by
the nodes too (previously it used the Eevee blend_method)" -- and traces
`alphaMode` from the Principled BSDF's `Alpha` **socket value** instead: a
constant `1` is `OPAQUE`, a detected alpha-clip node setup is `MASK`,
anything else is `BLEND`. `tools/blender/build_fixture.py`'s `Glass`
material sets `Alpha` to `0.3` on the socket directly (and also sets
`use_backface_culling` and, defensively, `blend_method` where the property
exists, since older exporter versions could not be checked on this
machine -- see "Verified against" below) and the real export carries
`alphaMode: "BLEND"` with `baseColorFactor`'s alpha at `0.3`, asserted on the
raw document in `renderer/gltfblender_test.go`'s `AlphaAndDoubleSided` check
and on what the loader reports (`ModelMesh.AlphaMode`, `BaseAlpha`) in
`LoadedTilingAndAlpha`.

## Light units

Confirmed by reading the Blender 5.0 install's exporter source
(`io_scene_gltf2/blender/exp/lights.py`): for a point or spot lamp in the
default **SPEC** lighting mode,

```
intensity_candela = (energy_watts / (4 * pi)) * 683
```

`683` is `PBR_WATTS_TO_LUMENS`
(`io_scene_gltf2/blender/com/conversion.py`), the standard luminous-efficacy
constant. A 1000 W Blender spot therefore exports at `1000 / (4*pi) * 683 =
54351.4` candela; a 100 W point at `5435.1` -- both measured directly off a
real export (`renderer/testdata/blender/level.glb`'s `SpotLamp`/`PointLamp`,
`renderer/gltfblender_test.go`'s `Lights` check) and matching the numbers
`examples/22-level/gen/main.go`'s hand-authored fixture already used from
the original issue #66 measurement -- two independent confirmations of the
same formula, not one copied from the other.

The other two lighting modes, read from the same source: **RAW** returns
`energy_watts * 2**exposure` completely unconverted (no `/4pi`, no `683`);
**COMPAT** applies the `/4pi` term but skips the `683` multiplier (so a
1000 W spot would export at `1000/(4*pi) = 79.6`, not 54351.4). This
recipe's script sets `export_import_convert_lighting_mode = 'SPEC'`
explicitly (already the default) specifically because every number on this
page assumes it.

`ModelLight.Intensity` carries this candela/lux value completely
unconverted -- `glyphengine.PointLight`/`SpotLight` have no photometric unit
at all, so a game divides by something. `examples/22-level/main.go` divides
by `20000`, chosen visually, with the reasoning in a comment next to the
constant; see "Lights from a glTF level" in [`lights.md`](lights.md) for the
general pattern.

**Blender's exporter never writes `range`** -- confirmed on every light in
this fixture (`SpotLamp`, `PointLamp`) and in the original #66 measurement.
`ModelLight.Range == 0` means "unbounded," which `glyphengine`'s lights
cannot represent, so a game MUST supply a finite range for every light
loaded from a Blender level; `examples/22-level/main.go`'s
`defaultLampRange` (14.0) is that number for its own scene.

Cone angles: a Blender spot's `spot_size` (full cone angle, radians) and
`spot_blend` (0-1) become glTF's `outerConeAngle = spot_size / 2` and
`innerConeAngle = outerConeAngle * (1 - spot_blend)` -- `spot_size 1.2`,
`blend 0.3` measures as `outerConeAngle 0.6`, `innerConeAngle 0.42` in both
the original #66 measurement and this fixture's real export. These are
half-angles in radians, the exact convention `glyphengine.SpotLight.Inner`/
`Outer` already use (verified against [`lights.md`](lights.md)), so no
conversion is needed on the way in.

## What does NOT travel through glTF from Blender

None of the following survive a glTF export, and nothing on this page or in
`tools/blender/` tries to make them:

- **Terrain as a sculpted mesh.** glTF has no heightmap concept; a sculpted
  landscape exports as an ordinary (likely large) mesh, not something
  `glyphengine`'s heightmap terrain (`docs/agents/terrain-heightmap.md`) can
  load. A heightmap writer is issue #70, not this one.
- **Particle systems.** Not part of glTF; a Blender particle system exports
  nothing (or, if it is the kind the exporter recognises as an instancer,
  see "Instancing" below for what THAT turns into, which is not the
  particle system itself).
- **The water surface, the sky, and baked/procedural shading in general.**
  glTF materials only carry what fits the metallic-roughness PBR model (plus
  a short list of KHR extensions); Blender's water, sky, and volume shaders
  have no glTF representation at all.
- **Shader nodes beyond a plain Principled BSDF wired to Base
  Color/Alpha/Metallic/Roughness/Normal/Emission.** Anything upstream of
  those sockets that is not one of the specific node shapes the exporter's
  `search_node_tree.py` recognises (a Mapping node before an Image Texture,
  the alpha-clip detection, and so on) does not make it into the file --
  the exporter bakes what it can read off those sockets as a factor or a
  texture and drops the rest.
- **Baked lighting** (lightmaps, baked AO, baked GI). Nothing in the
  Principled BSDF pipeline carries this; only an explicit bake to an image
  texture would, and that is an artist choice, not something this recipe
  does.
- **Modifiers, unless applied.** Covered above under `export_apply` -- an
  unapplied modifier exports its cage.
- **Cutout (`MASK`) materials.** The exporter writes `alphaMode: MASK` and the
  loader reports it (`ModelMesh.AlphaMode`, `AlphaCutoff`), but the lit
  pipelines have no alpha-tested path, so the engine cannot honour it today.
- **Different tiling on different maps of one material.** See below: one
  transform is baked per primitive, and base colour's wins.

Two things that used to be on this list now travel.

**Material tiling.** A Mapping node's scale, rotation and location arrive as
`KHR_texture_transform`, and `LoadGLTF` bakes it into the primitive's UVs at
load (there is no room for a per-material UV matrix in the push-constant
block). `renderer/testdata/blender/level.glb`'s `Ground` is the case:
`tools/blender/build_fixture.py`'s `build_ground()` scales the Mapping node
`(8, 8)`, the export carries `scale: [8, 8]` **and `offset: [0, -7]`**, and
after loading the ground's UVs span exactly `0..8` by `-7..1` -- eight tiles
each way. The offset is not decoration: glTF's V runs the opposite way from
Blender's, and the exporter's flip composes with the scale as `offset_v = 1 -
scale_v`. A reader that drops it gets the tiling frequency right and its
position wrong, which is invisible at a whole-number scale and wrong at any
other. `renderer/gltfblender_test.go` asserts both the raw extension
(`TextureTransform`) and the loaded UV range (`LoadedTilingAndAlpha`).

The base colour map decides the transform for the whole primitive, whether or
not it carries the extension -- a map without it is at the identity -- and any
map that disagrees is named in one log line, because its tiling will be wrong.
Blender writes the same transform on every map that shares a Mapping node
(measured with base colour and a normal map through one node), so this only
bites a material that deliberately tiles its maps differently. Image Texture
"Extension" modes arrive as sampler wrap modes and are honoured: EXTEND is
clamp-to-edge, MIRROR is mirrored repeat. Details in
[`material-maps.md`](material-maps.md).

**Alpha.** `ModelMesh.AlphaMode`, `AlphaCutoff` and `BaseAlpha` carry what the
exporter wrote, as data: the engine does not decide that `BLEND` means
`Translucent`, the game does, and `examples/22-level` shows it. The fixture's
`GlassPane` (Principled alpha 0.3) loads as `BLEND` with `BaseAlpha` 0.3.

## Instancing (issue #71's ground truth)

`tools/blender/build_fixture.py` builds two different instancing
mechanisms, scattering the same `Prop` object, specifically to find out what
each turns into. Both are recorded in `renderer/gltfblender_test.go`'s
`Instancing` check.

**A collection instance** (`bpy.data.objects.new(..., None)` with
`instance_type = 'COLLECTION'`, pointing at a collection holding one `Prop`
object -- Blender's "Add > Collection Instance") exports as the instancing
Empty gaining a **child node**, one per instance, each named after the
source object and referencing the SAME doc mesh. `level.glb` has two such
instances (`PropCollectionInstance`, `PropCollectionInstance2`), each with
one child node named `Prop`, both children pointing at the same doc mesh
index -- exactly the "several nodes, one doc mesh" shape
`Model.NodeMeshes` already exists to place correctly (see "Placing every
instance, not just the first" in [`models.md`](models.md)). Nothing new
needed for this case; it already works.

**A geometry-nodes scatter** (Mesh to Points -> Instance on Points,
referencing the same `Prop` via an Object Info node) was tried two ways:

- **Without** a Realize Instances node before the group output: the
  exported node carried **no mesh at all**. "Apply Modifiers" evaluates the
  modifier's output; unrealized instance data evaluates to an empty mesh,
  and the exporter omits an empty mesh rather than writing a degenerate
  one. Not an error either script or Blender reported -- the node is just
  silently geometry-less.
- **With** a Realize Instances node (what ships in `level.glb`'s
  `GNSourcePoints`): the export carries **one baked mesh** merging every
  scattered copy's geometry (384 vertices = 3 scattered copies of `Prop`'s
  128) at their scattered positions -- not 3 separate nodes, and not an
  instancing extension.

**`EXT_mesh_gpu_instancing` is not used anywhere in `level.glb`**, despite
`export_gpu_instances` being on, despite two collection instances sharing a
mesh, and despite `Building`/`Building_Linked` also sharing one (via Alt-D).
That option recognises Blender's own particle/geometry-node "instancer"
flag specifically -- not plain collection instancing, and not two objects
that merely share a mesh datablock -- and nothing in this fixture sets that
flag. A scene that actually wants `EXT_mesh_gpu_instancing` in the file
would need Blender's recognised instancer types (a particle system in
object-instancing mode, or a geometry-nodes "Instance on Points" output left
UN-realized reaching a mesh some other way); this fixture's two probes
between them cover "ordinary duplication," "collection instancing" and
"geometry-nodes scatter," and none of the three is what that toggle is
for.

## Through the example

```
task example:22-level -- -level ../renderer/testdata/blender/level.glb
```

or, exactly as `task validate` now runs it:

```
cd examples && go run ./22-level -level ../renderer/testdata/blender/level.glb
```

Run on 2026-09-19 under `GLYPHENGINE_FIXED_FRAME_TIME=16.667ms`, `-frames
90`, this logs:

```
gltf "level.glb": static geometry drawn without its node transform -- node(s) [Building GlassPane CulledPanel OpenPanel Mirrored] and 3 more carry a transform ...
level: "Building" has 3 floors (from extras, engine does not use this)
level: "Building_Linked" has 3 floors (from extras, engine does not use this)
level: node "ShearChild" is sheared or has a zero scale; it is drawn without that part of its transform
22-level running: 17 nodes, 9 meshes, 1 spot lights, 1 point lights
```

The first line is expected and not a bug -- see "Loading a level" in
[`models.md`](models.md); every mesh node in a level file trips it on
purpose. The `ShearChild` line is exactly the shear case described above
firing as designed. The Mirrored node draws (visible as an ordinary-looking
box in a screenshot -- a mirrored cube looks identical to an unmirrored one
without a texture on it to reveal the flip, and this one has none; its
geometry is present and correctly
placed, which is what `renderer/gltfblender_test.go`'s determinant check
and this run together confirm).

A rendered frame shows: the ground plane carrying its checker tiled eight
times each way, as the Mapping node asked (it drew as one flat stretch before
the loader read `KHR_texture_transform`); the glass pane blended rather than
opaque; several boxy
buildings and panels; small cones where the collection-instance and
geometry-nodes-scatter props landed; and a warm, bloomed pool of light on
the ground directly under `SpotLamp`. Two renders under the same fixed
clock are byte-identical (`cmp`), confirmed 2026-09-19.

## Verified against

| Blender version | Checked | Options RNA-detected | Fixture built | Notes |
|---|---|---|---|---|
| 5.0.1 | Yes | All 6 present | Yes -- this is the committed `level.glb` | Every number on this page was measured against this version. |
| 4.2 LTS, 4.3, 4.4 | -- | Not checked | Not checked | No working install was available. |

Everything on this page is a measurement of **5.0.1 and nothing else**. Issue
#67 asked for this table across 4.2-5.0 and it could not be produced: the only
launchable Blender where this was written was 5.0.1. Treat every number here as
unverified on any other version until someone fills the table in.

`tools/blender/export_level.py`'s RNA-introspection approach
(`level_export_kwargs`) exists specifically so it does not need per-version
testing to be safe: it only ever applies an option that the RUNNING
exporter's own RNA reports, and prints what it could not find. What could
not be verified is whether 4.2/4.3/4.4 actually have every one of the six
properties this recipe uses (plausible -- `export_extras`, `export_lights`
and `export_apply` are long-standing glTF exporter options -- but
unconfirmed), and whether the alpha-detection and texture-transform
behaviour described above (both cited from Blender 5.0's exporter source,
which uses socket-value detection rather than the older `blend_method`
property per that source's own comment) matches an older version's exporter.
If you have a working 4.2, 4.3 or 4.4 install, `BLENDER_FIXTURE=<path>
go test -run TestBlenderFixtureAltVersion ./renderer` runs the identical
assertions against a fixture built with that version (skips cleanly with no
env var set); please update this table with what you find.
