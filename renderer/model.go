package renderer

import (
	"fmt"
	"math"
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
