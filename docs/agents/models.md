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
  - renderer.Model.Bounds
  - renderer.Model.ReleaseGeometry
  - renderer.Model.Node
  - renderer.Model.NodeInMeshSpace
  - renderer.Renderer.CombineModel
  - renderer.Renderer.LoadGLTF
example: examples/08-grass
run: task example:08-grass
requires:
  - cgo
  - vulkan-runtime
assets: bundled
verified: 2026-09-18
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

`Name` is set regardless, so identifying a skinned primitive works.

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
`Model.Meshes` and is unit-tested without a device.

## Failure mode: LoadGLTFSkinned's Nodes are bind pose, not the animated pose

`LoadGLTFSkinned` fills `Model.Nodes` the same way `LoadGLTF` does, so a
socket works on a skinned model's non-animated attachment points too. This is
**not** a way to attach something to an animated joint: `ModelNode.World` is
the authored bind-pose transform, computed once at load, not a joint's
transform during playback. For that, see `Skeleton` and `Joint` in
`docs/agents/skeletal-animation.md`.

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

`Model.Nodes`, `Model.Node`, `Model.NodeInMeshSpace` and the untransformed-node
detection are checked the same way, but against glTF documents built in memory
rather than against `08-grass` or another bundled asset — this needs no
device either, since it is all arithmetic over `*gltf.Document` and
`Model.Nodes`. See `renderer/gltfnodes_test.go`.

## Not done

- **`extras`.** glTF's own place for application data would let a model carry
  `{"role": "charge_strip", "index": 1}` and let the engine stay uninterested in
  what that means. The name solves the case that came up; this is the more
  general answer and nothing needs it yet.
- **Applying node transforms to geometry.** `Model.Nodes` surfaces the node
  graph, but `LoadGLTF` still never applies a node's transform to the vertices
  it decodes — every primitive stays in its mesh-local space, which is why
  merging is a plain concatenation. See "Node space" and the "static mesh
  drawn without its node's transform" failure mode above; fixing this would
  move every existing model in every game already built on the current
  behaviour, so it is a deliberate non-goal here, not an oversight.
- **Colliders from geometry.** `Verts` is what a convex hull generator would
  need; the generator is not written.
