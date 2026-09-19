package main

import (
	"github.com/go-gl/mathgl/mgl32"
	"github.com/qmuntal/gltf"
)

// node is the slice of a glTF node this tool needs to rasterise a mesh: its
// name, the doc mesh it instances (if any), and its transform composed down
// the parent chain.
//
// This is reimplemented here rather than imported from the renderer
// package's equivalent (extractNodes/resolveWorld in renderer/gltf.go) on
// purpose: renderer pulls in the Vulkan bindings through vkngwrapper, and
// heightmapconv reads a glTF file with nothing but the CPU, so it should not
// need a GPU or the Vulkan SDK installed to run. The algorithm is the same
// because glTF's node graph only has one correct way to compose a transform
// down a parent chain -- TRS-or-Matrix per node, multiplied parent-to-child.
type node struct {
	Name   string
	Parent int // index into the returned slice, -1 for a root
	Mesh   int // index into doc.Meshes, -1 if the node carries none
	Local  mgl32.Mat4
	World  mgl32.Mat4
}

// buildNodes computes World for every node in doc, the same TRS-or-Matrix
// rule and parent-chain composition renderer/gltf.go's extractNodes and
// resolveWorld use, reimplemented for this GPU-free tool (see the node type
// doc above).
func buildNodes(doc *gltf.Document) []node {
	n := len(doc.Nodes)
	if n == 0 {
		return nil
	}
	nodes := make([]node, n)
	for i := range nodes {
		nodes[i].Parent = -1
		nodes[i].Mesh = -1
	}

	// Parent indices come from walking every node's Children rather than
	// from anything glTF stores on the child, so an out-of-range or cyclic
	// entry cannot leave a node without a well-defined (if arbitrary)
	// parent -- see resolveNodeWorld below for how a cycle terminates.
	for pi, gn := range doc.Nodes {
		if gn == nil {
			continue
		}
		for _, ci := range gn.Children {
			if ci < 0 || ci >= n {
				continue
			}
			if nodes[ci].Parent == -1 {
				nodes[ci].Parent = pi
			}
		}
	}

	for i, gn := range doc.Nodes {
		if gn == nil {
			nodes[i].Local = mgl32.Ident4()
			continue
		}
		nodes[i].Name = gn.Name
		nodes[i].Local = nodeLocalTransform(gn)
		if gn.Mesh != nil {
			if mi := *gn.Mesh; mi >= 0 && mi < len(doc.Meshes) {
				nodes[i].Mesh = mi
			}
		}
	}

	state := make([]uint8, n)
	const (
		unresolved = iota
		resolving
		resolved
	)
	var resolve func(i int) mgl32.Mat4
	resolve = func(i int) mgl32.Mat4 {
		switch state[i] {
		case resolved:
			return nodes[i].World
		case resolving:
			// A parent/child cycle in the source file: land on this node's
			// own Local rather than recursing forever. glTF from the wild
			// is not guaranteed valid, and terminating is the only property
			// invalid input needs here.
			return nodes[i].Local
		}
		state[i] = resolving
		w := nodes[i].Local
		if p := nodes[i].Parent; p >= 0 && p < n {
			w = resolve(p).Mul4(nodes[i].Local)
		}
		nodes[i].World = w
		state[i] = resolved
		return w
	}
	for i := range nodes {
		resolve(i)
	}
	return nodes
}

// nodeLocalTransform mirrors renderer/gltf.go's function of the same name
// (see the node type doc for why this is a separate copy): TRS when the node
// authored any of translation/rotation/scale, else its Matrix, else
// identity. glTF's own spec calls TRS and Matrix mutually exclusive, but
// some exporters set Matrix to identity AND populate TRS, so TRS is
// preferred whenever it is non-default.
func nodeLocalTransform(n *gltf.Node) mgl32.Mat4 {
	hasTRS := n.Translation != [3]float64{} || n.Rotation != [4]float64{} || n.Scale != [3]float64{}
	if hasTRS {
		t := n.TranslationOrDefault()
		r := n.RotationOrDefault()
		s := n.ScaleOrDefault()

		trans := mgl32.Translate3D(float32(t[0]), float32(t[1]), float32(t[2]))
		// glTF quaternion order is (x, y, z, w).
		rot := mgl32.Quat{W: float32(r[3]), V: mgl32.Vec3{float32(r[0]), float32(r[1]), float32(r[2])}}.Mat4()
		scale := mgl32.Scale3D(float32(s[0]), float32(s[1]), float32(s[2]))
		return trans.Mul4(rot).Mul4(scale)
	}

	if n.Matrix != [16]float64{} {
		var m mgl32.Mat4
		for i := 0; i < 16; i++ {
			m[i] = float32(n.Matrix[i])
		}
		return m
	}

	return mgl32.Ident4()
}

// findMeshNode resolves -node to a node index. An empty name requires the
// document to carry exactly one mesh-bearing node -- a real level file
// (buildings alongside the terrain, per the issue) usually has several, so
// this only auto-picks when there is no ambiguity to get wrong.
func findMeshNode(nodes []node, name string) (int, error) {
	var meshNodes []int
	for i, nd := range nodes {
		if nd.Mesh >= 0 {
			meshNodes = append(meshNodes, i)
		}
	}
	if name != "" {
		for _, i := range meshNodes {
			if nodes[i].Name == name {
				return i, nil
			}
		}
		return -1, meshNodeError(name, nodes, meshNodes)
	}
	if len(meshNodes) == 1 {
		return meshNodes[0], nil
	}
	return -1, meshNodeError("", nodes, meshNodes)
}

func meshNodeError(want string, nodes []node, meshNodes []int) error {
	names := make([]string, len(meshNodes))
	for i, ni := range meshNodes {
		names[i] = nodes[ni].Name
	}
	if want != "" {
		return &nodeSelectionError{msg: "no node named " + want + " carries a mesh", available: names}
	}
	if len(names) == 0 {
		return &nodeSelectionError{msg: "the document has no node that carries a mesh"}
	}
	return &nodeSelectionError{msg: "the document has more than one mesh-bearing node; pass -node to pick one", available: names}
}

type nodeSelectionError struct {
	msg       string
	available []string
}

func (e *nodeSelectionError) Error() string {
	if len(e.available) == 0 {
		return e.msg
	}
	s := e.msg + " (available:"
	for _, n := range e.available {
		s += " " + n
	}
	return s + ")"
}
