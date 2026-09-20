package renderer

import (
	"fmt"
	"math"

	"github.com/go-gl/mathgl/mgl32"
)

// ReleaseGeometry drops the decoded vertices and indices from every mesh in the
// model, leaving the GPU meshes alone.
//
// Geometry is retained by default, because the decode allocated it anyway and
// throwing it away was the only reason it was unavailable. This is for a caller
// that has finished deriving whatever it needed — a merged silhouette, a
// collider, a bounding box — and would rather have the memory back. A big
// scene model is megabytes of it.
//
// Calling it makes Bounds and CombineModel fail rather than return something
// wrong, which is the point: silently returning an empty mesh from a released
// model is the kind of green result that gets shipped.
//
// Nodes is untouched. A node is a handful of floats, not the megabytes-scale
// geometry this exists to give back, and a socket found before release stays
// valid to look up and use after it.
func (m *Model) ReleaseGeometry() {
	if m == nil {
		return
	}
	for i := range m.Meshes {
		m.Meshes[i].Verts = nil
		m.Meshes[i].Idx = nil
	}
}

// Bounds returns the axis-aligned box containing every primitive in the model,
// in the model's own space.
//
// ok is false when no primitive has retained geometry — an empty model, or one
// that has been through ReleaseGeometry. It is deliberately not "the box is
// empty", because a caller seating a model on the ground needs to tell "this
// model is zero-height" apart from "I cannot answer".
//
// A box rather than the sphere Mesh.BoundCenter and Mesh.BoundRadius already
// carry: a sphere is what a frustum test wants and the wrong shape for asking
// how tall something is, which is the question that comes up when placing one.
//
// This is over Verts as decoded, the same mesh-local space LoadGLTF has
// always drawn in — it does not walk Model.Nodes, so an instancing node's
// transform (see the space caveat on Model.Nodes) is not folded in here
// either.
func (m *Model) Bounds() (min, max [3]float32, ok bool) {
	if m == nil {
		return min, max, false
	}
	min = [3]float32{math.MaxFloat32, math.MaxFloat32, math.MaxFloat32}
	max = [3]float32{-math.MaxFloat32, -math.MaxFloat32, -math.MaxFloat32}

	for i := range m.Meshes {
		for _, v := range m.Meshes[i].Verts {
			for a := 0; a < 3; a++ {
				if v.Pos[a] < min[a] {
					min[a] = v.Pos[a]
				}
				if v.Pos[a] > max[a] {
					max[a] = v.Pos[a]
				}
			}
			ok = true
		}
	}
	if !ok {
		return [3]float32{}, [3]float32{}, false
	}
	return min, max, true
}

// mergeGeometry concatenates every primitive's geometry into one vertex and
// index array, offsetting each primitive's indices past the vertices already
// placed.
//
// Each primitive's BaseColor is multiplied into its vertices, because the
// merged mesh has one material where the source had several — without it a
// model whose colour came from material factors rather than a texture comes out
// uniformly white.
//
// Split from CombineModel so the index arithmetic can be tested without a
// device. An off-by-one in the offset produces a mesh that still draws, with
// triangles reaching into the wrong primitive's vertices, which is the failure
// worth having a test for.
func mergeGeometry(meshes []ModelMesh) ([]Vertex, []uint32) {
	var nv, ni int
	for i := range meshes {
		nv += len(meshes[i].Verts)
		ni += len(meshes[i].Idx)
	}
	if nv == 0 {
		return nil, nil
	}

	verts := make([]Vertex, 0, nv)
	idx := make([]uint32, 0, ni)

	for i := range meshes {
		mm := &meshes[i]
		base := uint32(len(verts))
		for _, v := range mm.Verts {
			v.Color[0] *= mm.BaseColor[0]
			v.Color[1] *= mm.BaseColor[1]
			v.Color[2] *= mm.BaseColor[2]
			verts = append(verts, v)
		}
		for _, n := range mm.Idx {
			idx = append(idx, base+n)
		}
	}
	return verts, idx
}

// CombineModel uploads every primitive in the model as a single mesh.
//
// A glTF exporter splits a model by material, so one structure arrives as
// several primitives. Drawing it as several entities is fine until the thing
// being drawn is conceptually one object: a translucent placement preview drawn
// per primitive blends against *itself*, coming out denser wherever the model
// overlaps, and needs N entities spawned and despawned every time the selection
// moves. Merged, it is one draw, one entity, and one silhouette.
//
// The merged mesh has no material of its own — per-primitive textures and maps
// are dropped, and base colours are baked into the vertices. That is the right
// trade for a silhouette and the wrong one for drawing the model normally, so
// this is a helper rather than something LoadGLTF does on its own.
//
// Fails on a model whose geometry has been released, rather than returning an
// empty mesh.
//
// Nodes and ModelMesh.Node are ignored. A combined mesh routinely comes from
// several primitives that in turn came from several different nodes — that
// is the whole reason a game reaches for this, to draw N primitives as one —
// and mergeGeometry concatenates their raw Verts exactly as LoadGLTF decoded
// them, in each primitive's own mesh-local space, with no node transform
// applied to any of them. That is consistent with LoadGLTF's existing
// contract (see the space caveat on Model.Nodes) rather than a new gap: this
// helper was already a plain concatenation before Nodes existed, and picking
// one primitive's node transform to apply to the merged whole would privilege
// that primitive over its siblings for no defensible reason.
func (r *Renderer) CombineModel(m *Model) (*Mesh, error) {
	if m == nil || len(m.Meshes) == 0 {
		return nil, fmt.Errorf("combine model: no meshes")
	}

	verts, idx := mergeGeometry(m.Meshes)
	if len(verts) == 0 {
		return nil, fmt.Errorf("combine model: no geometry retained (ReleaseGeometry called?)")
	}

	// Same uint16/uint32 choice LoadGLTF makes per primitive. Merging is
	// exactly the operation that can push a model over the line, so it is
	// decided on the merged count rather than inherited.
	if len(verts) <= 65535 {
		idx16 := make([]uint16, len(idx))
		for i, v := range idx {
			idx16[i] = uint16(v)
		}
		return r.CreateIndexedMesh(verts, idx16)
	}
	return r.CreateIndexedMesh32(verts, idx)
}

// Node returns the first node named name, and whether one was found.
//
// Exact match only. No fuzzy matching and no baked-in convention like a
// "Socket_" prefix — what a node's name means is the game's business, not
// the loader's; this only has to get the name there intact and hand it back.
// "First" matters when an exporter (or an artist) produces duplicate names:
// the earlier one in doc.Nodes order wins, silently, rather than being an
// error at lookup time.
func (m *Model) Node(name string) (ModelNode, bool) {
	if m == nil {
		return ModelNode{}, false
	}
	for _, n := range m.Nodes {
		if n.Name == name {
			return n, true
		}
	}
	return ModelNode{}, false
}

// NodeInMeshSpace returns node's World transform expressed in mm's vertex
// space — the trap in the Model.Nodes space caveat, removed.
//
// A node's World is in the glTF scene's space. mm.Verts are in that same
// space only when the node instancing mm has an identity transform, which is
// true of every model this has been checked against today but is not
// guaranteed by the format. This is the general answer: it inverts mm's
// owning node's World and composes it with node's World, so the result places
// node correctly relative to mm's vertices whether or not that node happened
// to be identity. When mm.Node is -1 (no node instances it), there is no
// mesh-node transform to remove, so node.World is returned unchanged.
func (m *Model) NodeInMeshSpace(node ModelNode, mm ModelMesh) mgl32.Mat4 {
	return nodeInMeshSpace(m.Nodes, mm, node)
}

// nodeInMeshSpace is NodeInMeshSpace's pure arithmetic, split out so it can be
// tested without a Renderer.
func nodeInMeshSpace(nodes []ModelNode, mm ModelMesh, node ModelNode) mgl32.Mat4 {
	if mm.Node < 0 || mm.Node >= len(nodes) {
		return node.World
	}
	return nodes[mm.Node].World.Inv().Mul4(node.World)
}

// NodeMeshes returns the indices into Model.Meshes of every primitive the
// node at index node instances -- every ModelMesh split from the doc mesh
// Model.Nodes[node].Mesh names, not only the one ModelMesh.Node happens to
// point back at.
//
// ModelMesh.Node exists to answer a different question and answers it
// wrong for this one: it names the FIRST node (in glTF node order) that
// instances a doc mesh, because a primitive is drawn once regardless of
// instance count. Placement is the opposite question -- a level with forty
// identical lamp posts sharing one doc mesh needs that mesh's primitives
// drawn once per node, each at that node's own World, and "first owner
// only" would place all forty on top of the first post. NodeMeshes goes
// from a specific node to its primitives instead, so every instance gets
// the right one.
//
// Returns nil for a node with no mesh (Model.Nodes[node].Mesh == -1, an
// empty node, a light, a joint) or an out-of-range index, rather than
// panicking or returning every mesh in the model.
func (m *Model) NodeMeshes(node int) []int {
	return nodeMeshes(m.Nodes, m.Meshes, node)
}

// nodeMeshes is NodeMeshes' pure arithmetic, split out so it can be tested
// without a Renderer.
func nodeMeshes(nodes []ModelNode, meshes []ModelMesh, node int) []int {
	if node < 0 || node >= len(nodes) {
		return nil
	}
	docMesh := nodes[node].Mesh
	if docMesh < 0 {
		return nil
	}
	var out []int
	for i := range meshes {
		if meshes[i].DocMesh == docMesh {
			out = append(out, i)
		}
	}
	return out
}

// MeshInstances returns the indices into Model.Nodes of every node that
// instances the doc mesh at index docMesh -- NodeMeshes' inverse (issue
// #71). NodeMeshes goes from a node to what it draws; a level loader turning
// repetition into an InstancedMesh needs the other direction: from a doc
// mesh to every node placing it, so it can hand Renderer.CreateInstanceSet
// one MeshInstance per node's World in a single call rather than one entity
// per node.
//
// Deliberately shaped like NodeMeshes rather than as one grouping over the
// whole model (e.g. map[int][]int): a spawn loop already walks Model.Nodes
// node by node the way examples/22-level's spawnLevel does for NodeMeshes,
// and the natural shape there is "the first time this node's doc mesh is
// seen, ask who else shares it" -- one MeshInstances call per DISTINCT doc
// mesh actually encountered, not one per node and not a whole-model
// precomputation nothing may need (most doc meshes in a level are not
// shared at all). See docs/agents/instancing.md for the worked recipe.
//
// Returns nil for a doc mesh index no node references (including a
// negative one, which is what Model.Nodes[i].Mesh is for an empty node) --
// the "nobody instances this" case, not an error, the same contract
// NodeMeshes gives for a meshless node.
func (m *Model) MeshInstances(docMesh int) []int {
	return meshInstances(m.Nodes, docMesh)
}

// meshInstances is MeshInstances' pure arithmetic, split out so it can be
// tested without a Renderer.
func meshInstances(nodes []ModelNode, docMesh int) []int {
	if docMesh < 0 {
		return nil
	}
	var out []int
	for i := range nodes {
		if nodes[i].Mesh == docMesh {
			out = append(out, i)
		}
	}
	return out
}
