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
  - renderer.Model.LightWorldPosDir
  - renderer.Renderer.CombineModel
  - renderer.Renderer.LoadGLTF
  - renderer.Renderer.LoadGLTFSkinned
  - renderer.ReadGLTF
  - renderer.ReadGLTFSkinned
  - renderer.Renderer.DestroyModel
  - renderer.Renderer.DestroySkinnedModel
  - renderer.ResourceCounts
  - renderer.Renderer.ResourceCounts
example: examples/08-grass
run: task example:08-grass
requires:
  - cgo
  - vulkan-runtime
assets: bundled
verified: 2026-09-19
---

# Treat a loaded model as geometry, not only as a draw call

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

## Memory

Geometry is retained by default, because the decode allocated it anyway and
discarding was the only reason it was unavailable. A big scene model is
megabytes of it, so:

```go
model.ReleaseGeometry()
```

After that, `Bounds` reports `ok == false` and `CombineModel` returns an error
rather than an empty mesh — failing instead of silently producing nothing.

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
- **`EXT_mesh_gpu_instancing`.** A level with several nodes instancing one
  doc mesh (see `Model.NodeMeshes` above) is exactly the shape that
  extension exists for, and it is a plausible next step for a level with
  hundreds of identical props — but nothing here needed it yet, and the
  existing `renderer.InstanceSet` API (`docs/agents/instancing.md`) already
  covers the same case without it: a loader could detect nodes sharing a
  mesh index and build one `InstanceSet` from their `World` transforms with
  no glTF extension involved. Worth deciding deliberately if it comes up
  again, not defaulted to.
