package renderer

import (
	"testing"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/qmuntal/gltf"
)

// These build glTF documents in memory with github.com/qmuntal/gltf rather
// than loading a fixture, so node extraction is tested as a pure function of
// *gltf.Document and none of it needs a Renderer or a GPU.

func mat4Close(a, b mgl32.Mat4, eps float32) bool {
	for i := range a {
		d := a[i] - b[i]
		if d > eps || d < -eps {
			return false
		}
	}
	return true
}

func vec3Close(a, b mgl32.Vec3, eps float32) bool {
	for i := 0; i < 3; i++ {
		d := a[i] - b[i]
		if d > eps || d < -eps {
			return false
		}
	}
	return true
}

// identQuat is the glTF default rotation, (x,y,z,w) = (0,0,0,1).
var identQuat = [4]float64{0, 0, 0, 1}

// TestExtractNodesWorldTransform builds a root -> child -> "Socket_Lamp"
// chain, each with its own authored TRS, and checks World against a product
// hand-composed with mgl32's own primitives independently of extractNodes —
// the same building blocks nodeLocalTransform uses, assembled here rather
// than reused, so a bug in extractNodes' parent-chain composition has
// something independent to disagree with.
//
// It has teeth: swapping resolveWorld's multiplication order (to
// `nodes[i].Local.Mul4(resolve(p))`) fails both the child's and the
// grandchild's World -- the child node carries a rotation, so the two orders
// diverge from there down the chain -- while leaving Parent untouched.
// Introduced and reverted to confirm.
func TestExtractNodesWorldTransform(t *testing.T) {
	doc := &gltf.Document{
		Nodes: []*gltf.Node{
			{ // 0: root
				Name:        "Root",
				Translation: [3]float64{1, 0, 0},
				Rotation:    identQuat,
				Scale:       [3]float64{1, 1, 1},
				Children:    []int{1},
			},
			{ // 1: child, rotated so composition order matters
				Name:        "Child",
				Translation: [3]float64{0, 2, 0},
				Rotation:    [4]float64{0, 0.70710678, 0, 0.70710678}, // 90 deg about Y
				Scale:       [3]float64{1, 1, 1},
				Children:    []int{2},
			},
			{ // 2: grandchild, the socket
				Name:        "Socket_Lamp",
				Translation: [3]float64{0, 0, 3},
				Rotation:    identQuat,
				Scale:       [3]float64{2, 2, 2},
			},
		},
	}

	nodes := extractNodes(doc)
	if len(nodes) != 3 {
		t.Fatalf("got %d nodes, want 3", len(nodes))
	}

	if nodes[0].Parent != -1 {
		t.Errorf("root Parent = %d, want -1", nodes[0].Parent)
	}
	if nodes[1].Parent != 0 {
		t.Errorf("child Parent = %d, want 0", nodes[1].Parent)
	}
	if nodes[2].Parent != 1 {
		t.Errorf("grandchild Parent = %d, want 1", nodes[2].Parent)
	}
	if nodes[2].Name != "Socket_Lamp" {
		t.Errorf("grandchild Name = %q, want Socket_Lamp", nodes[2].Name)
	}

	// Independent oracle: the same TRS composed by hand with mgl32's own
	// primitives, not by calling nodeLocalTransform or resolveWorld.
	rotY90 := mgl32.Quat{W: 0.70710678, V: mgl32.Vec3{0, 0.70710678, 0}}.Mat4()
	wantLocal0 := mgl32.Translate3D(1, 0, 0)
	wantLocal1 := mgl32.Translate3D(0, 2, 0).Mul4(rotY90)
	wantLocal2 := mgl32.Translate3D(0, 0, 3).Mul4(mgl32.Scale3D(2, 2, 2))
	wantWorld0 := wantLocal0
	wantWorld1 := wantWorld0.Mul4(wantLocal1)
	wantWorld2 := wantWorld1.Mul4(wantLocal2)

	if !mat4Close(nodes[0].World, wantWorld0, 1e-5) {
		t.Errorf("root World = %v, want %v", nodes[0].World, wantWorld0)
	}
	if !mat4Close(nodes[1].World, wantWorld1, 1e-5) {
		t.Errorf("child World = %v, want %v", nodes[1].World, wantWorld1)
	}
	if !mat4Close(nodes[2].World, wantWorld2, 1e-5) {
		t.Errorf("socket World = %v, want %v", nodes[2].World, wantWorld2)
	}
}

// TestExtractNodesLocalFollowsMatrixVsTRSRule checks that a node's Local
// reuses nodeLocalTransform's own TRS-vs-Matrix rule rather than a second one
// written for ModelNode, while Translation/Rotation/Scale stay the raw
// authored TRS fields (their glTF defaults when a node has none) regardless
// of which one produced Local.
//
// It has teeth: reversing nodeLocalTransform's precedence (checking Matrix
// before TRS) leaves the matrix-only case unchanged but fails the
// both-authored case, which is exactly the case this rule exists for --
// verified by making the swap and reverting it.
func TestExtractNodesLocalFollowsMatrixVsTRSRule(t *testing.T) {
	// Column-major glTF matrix for a pure translation by (5,0,0).
	matOnly := [16]float64{
		1, 0, 0, 0,
		0, 1, 0, 0,
		0, 0, 1, 0,
		5, 0, 0, 1,
	}
	// A different translation, so "both" is unambiguous about which one won.
	conflictingMat := [16]float64{
		1, 0, 0, 0,
		0, 1, 0, 0,
		0, 0, 1, 0,
		99, 99, 99, 1,
	}

	doc := &gltf.Document{
		Nodes: []*gltf.Node{
			{Name: "MatrixOnly", Matrix: matOnly},
			{Name: "Both", Translation: [3]float64{7, 0, 0}, Rotation: identQuat, Matrix: conflictingMat},
		},
	}
	nodes := extractNodes(doc)

	wantMatOnly := mgl32.Translate3D(5, 0, 0)
	if !mat4Close(nodes[0].Local, wantMatOnly, 1e-6) {
		t.Errorf("matrix-only Local = %v, want %v", nodes[0].Local, wantMatOnly)
	}
	// Kept "as authored": no Translation/Rotation/Scale was given, so these
	// sit at glTF's TRS defaults even though Local came from the matrix.
	if nodes[0].Translation != (mgl32.Vec3{}) {
		t.Errorf("matrix-only Translation = %v, want zero (nothing was authored)", nodes[0].Translation)
	}
	if nodes[0].Scale != (mgl32.Vec3{1, 1, 1}) {
		t.Errorf("matrix-only Scale = %v, want (1,1,1) default", nodes[0].Scale)
	}

	wantBoth := mgl32.Translate3D(7, 0, 0)
	if !mat4Close(nodes[1].Local, wantBoth, 1e-6) {
		t.Errorf("both-authored Local = %v, want the TRS translation %v, not the matrix", nodes[1].Local, wantBoth)
	}
	if nodes[1].Translation != (mgl32.Vec3{7, 0, 0}) {
		t.Errorf("both-authored Translation = %v, want (7,0,0)", nodes[1].Translation)
	}
}

// TestExtractNodesOrientationSurvives is the half of this feature a Vec3
// could never give: a socket's forward direction, not just its position.
//
// The node is rotated 90 degrees about X with no translation, so its World
// carries only that rotation. Rotating the glTF forward axis (0,0,-1) by 90
// degrees about X points it at +Y by hand: y' = y*cos - z*sin = 0 - (-1)(1)
// = 1, z' = y*sin + z*cos = 0. The test also builds the same rotation via
// mgl32.HomogRotate3DX -- a construction path independent of
// nodeLocalTransform's Quat.Mat4() -- as a second, machine-checked oracle.
//
// It has teeth: flipping the sign of the quaternion's X component in
// nodeLocalTransform (V: {-r[0], r[1], r[2]}) reverses the rotation's
// handedness and sends the same forward vector to (0,-1,0) instead, which
// fails both the hand-computed and the HomogRotate3DX comparison. Introduced
// and reverted to confirm.
func TestExtractNodesOrientationSurvives(t *testing.T) {
	doc := &gltf.Document{
		Nodes: []*gltf.Node{
			{
				Name:     "Socket_Lamp",
				Rotation: [4]float64{0.70710678, 0, 0, 0.70710678}, // 90 deg about X
			},
		},
	}
	nodes := extractNodes(doc)

	forward := mgl32.Vec4{0, 0, -1, 0} // direction, w=0
	got := nodes[0].World.Mul4x1(forward)

	wantHand := mgl32.Vec3{0, 1, 0}
	if !vec3Close(got.Vec3(), wantHand, 1e-5) {
		t.Errorf("rotated forward = %v, want %v (hand-computed)", got.Vec3(), wantHand)
	}

	oracle := mgl32.HomogRotate3DX(mgl32.DegToRad(90))
	wantOracle := oracle.Mul4x1(forward)
	if !vec3Close(got.Vec3(), wantOracle.Vec3(), 1e-5) {
		t.Errorf("rotated forward = %v, want %v (HomogRotate3DX oracle)", got.Vec3(), wantOracle.Vec3())
	}
}

// TestNodeInMeshSpace covers the helper that removes the trap in the space
// caveat on Model.Nodes: a socket's World is in the glTF scene's space, which
// is a mesh's vertex space only when the mesh's own instancing node is
// identity.
//
// It has teeth: dropping the .Inv() (using
// `nodes[mm.Node].World.Mul4(node.World)`) fails both the translation-only
// case -- it lands at P+T=(12,2,14) rather than P-T=(6,6,0) -- and the
// rotation/scale case, since neither is inverting anything anymore.
// Introduced and reverted to confirm.
func TestNodeInMeshSpace(t *testing.T) {
	t.Run("translation only: P - T", func(t *testing.T) {
		nodes := []ModelNode{{World: mgl32.Translate3D(3, -2, 7)}} // mesh node, T
		socket := ModelNode{Name: "Socket_Lamp", World: mgl32.Translate3D(9, 4, 7)}
		mm := ModelMesh{Node: 0}

		got := nodeInMeshSpace(nodes, mm, socket)
		gotT := mgl32.Vec3{got[12], got[13], got[14]}
		want := mgl32.Vec3{6, 6, 0} // (9,4,7) - (3,-2,7)
		if !vec3Close(gotT, want, 1e-5) {
			t.Errorf("translation = %v, want %v", gotT, want)
		}
	})

	t.Run("rotation and non-uniform scale: the full inverse", func(t *testing.T) {
		// Mesh node with all three components, none of them trivial.
		meshWorld := mgl32.Translate3D(10, -3, 2).
			Mul4(mgl32.HomogRotate3DY(mgl32.DegToRad(90))).
			Mul4(mgl32.Scale3D(2, 1, 1))
		wantLocal := mgl32.Translate3D(5, 1, -2) // the answer nodeInMeshSpace must recover

		nodes := []ModelNode{{World: meshWorld}}
		socket := ModelNode{World: meshWorld.Mul4(wantLocal)} // socket placed via wantLocal in mesh space
		mm := ModelMesh{Node: 0}

		got := nodeInMeshSpace(nodes, mm, socket)
		if !mat4Close(got, wantLocal, 1e-3) {
			t.Errorf("mesh-space transform = %v, want %v", got, wantLocal)
		}
	})

	t.Run("no instancing node: World unchanged", func(t *testing.T) {
		socket := ModelNode{World: mgl32.Translate3D(1, 2, 3)}
		mm := ModelMesh{Node: -1}
		if got := nodeInMeshSpace(nil, mm, socket); got != socket.World {
			t.Errorf("got %v, want socket.World unchanged (%v)", got, socket.World)
		}
	})

	t.Run("identity mesh node: World unchanged", func(t *testing.T) {
		nodes := []ModelNode{{World: mgl32.Ident4()}}
		socket := ModelNode{World: mgl32.Translate3D(1, 2, 3)}
		mm := ModelMesh{Node: 0}
		if got := nodeInMeshSpace(nodes, mm, socket); !mat4Close(got, socket.World, 1e-6) {
			t.Errorf("got %v, want socket.World unchanged (%v)", got, socket.World)
		}
	})
}

// TestUntransformedMeshNodesDetection covers the pure check behind the log
// line LoadGLTF and LoadGLTFSkinned each emit when a static mesh's
// instancing node carries a transform that is never applied to its vertices.
//
// It has teeth: inverting the condition (`if isIdentityTransform(...)`
// instead of `!isIdentityTransform(...)`) reports "RootMesh" instead of
// "ScaledMeshNode" -- exactly backwards -- which fails the comparison.
// Introduced and reverted to confirm.
func TestUntransformedMeshNodesDetection(t *testing.T) {
	doc := &gltf.Document{
		Nodes: []*gltf.Node{
			{Name: "RootMesh", Mesh: gltf.Index(0)}, // identity: not flagged
			{Name: "ScaledMeshNode", Scale: [3]float64{2, 2, 2}, Mesh: gltf.Index(1)},
			{Name: "Light", Translation: [3]float64{0, 5, 0}}, // non-identity, but no Mesh
		},
		Meshes: []*gltf.Mesh{{}, {}},
	}

	nodes := extractNodes(doc)
	owners := meshOwnerNodes(doc)
	meshes := []ModelMesh{{Node: owners[0]}, {Node: owners[1]}}

	got := untransformedMeshNodes(nodes, meshes)
	if len(got) != 1 || got[0] != "ScaledMeshNode" {
		t.Errorf("flagged nodes = %v, want [ScaledMeshNode]", got)
	}
}

// TestMeshOwnerNodesInstancingAndOrphans covers fact: one glTF mesh can be
// instanced by several nodes, and a mesh can be referenced by none.
//
// It has teeth: removing the "first wins" guard (`if owner[mi] == -1`, always
// overwriting instead) reports node 1 as mesh 0's owner instead of node 0,
// failing the instancing assertion. Introduced and reverted to confirm.
func TestMeshOwnerNodesInstancingAndOrphans(t *testing.T) {
	doc := &gltf.Document{
		Nodes: []*gltf.Node{
			{Name: "InstanceA", Mesh: gltf.Index(0)},
			{Name: "InstanceB", Mesh: gltf.Index(0)}, // same mesh, second instance
			{Name: "NoMesh"},
		},
		Meshes: []*gltf.Mesh{{}, {}}, // mesh 1 is never instanced
	}

	owners := meshOwnerNodes(doc)
	if owners[0] != 0 {
		t.Errorf("mesh 0 owner = %d, want 0 (the first instancing node)", owners[0])
	}
	if owners[1] != -1 {
		t.Errorf("mesh 1 owner = %d, want -1 (instanced by no node)", owners[1])
	}
}

// TestExtractNodesToleratesMalformedGraph covers glTF from the wild: a
// parent/child cycle, an out-of-range child index, and an out-of-range mesh
// index, none of which may hang or panic extraction.
//
// It has teeth, three ways, each introduced and reverted to confirm:
//   - removing the child bounds check (`ci < 0 || ci >= n`) in extractNodes'
//     parent-assignment loop panics with an index out of range on the
//     out-of-range child;
//   - removing the bounds check in meshOwnerNodes (`mi >= len(owner)`) panics
//     the same way on the out-of-range mesh index;
//   - removing resolveWorld's "already on this path" guard (letting the
//     `resolving` state fall through to the recursive branch) turns the
//     2-node cycle into unbounded recursion, which crashes the test binary
//     with a stack-overflow fatal error rather than a clean test failure --
//     still definitively "not silently correct", which is what the guard
//     is there to prevent.
func TestExtractNodesToleratesMalformedGraph(t *testing.T) {
	doc := &gltf.Document{
		Nodes: []*gltf.Node{
			{Name: "A", Children: []int{1, 99}},                  // 99 is out of range
			{Name: "B", Children: []int{0}, Mesh: gltf.Index(5)}, // cycle back to A; mesh 5 doesn't exist
		},
		Meshes: []*gltf.Mesh{{}}, // only index 0 is valid
	}

	nodes := extractNodes(doc) // must not panic or hang
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(nodes))
	}
	if nodes[1].Mesh != -1 {
		t.Errorf("out-of-range mesh index = %d, want -1", nodes[1].Mesh)
	}
	// The valid half of A's Children still claims B; the out-of-range half
	// is silently skipped rather than crashing the whole extraction.
	if nodes[1].Parent != 0 {
		t.Errorf("B's Parent = %d, want 0", nodes[1].Parent)
	}
	if nodes[0].Parent != 1 {
		t.Errorf("A's Parent = %d, want 1 (B claims it too, forming the cycle)", nodes[0].Parent)
	}

	owners := meshOwnerNodes(doc) // must not panic on the out-of-range mesh index
	if len(owners) != 1 || owners[0] != -1 {
		t.Errorf("owners = %v, want [-1] (nothing validly instances mesh 0)", owners)
	}
}

// TestModelNodeLookup covers Model.Node: exact match, first of duplicates, a
// miss.
//
// It has teeth: changing the loop to keep the last match instead of
// returning on the first (naming is the game's business, but "first wins" on
// a duplicate is this API's own contract) returns index 2 instead of index 1
// for the duplicate case, failing it. Introduced and reverted to confirm.
func TestModelNodeLookup(t *testing.T) {
	m := &Model{Nodes: []ModelNode{
		{Name: "A"},
		{Name: "Socket_Lamp", Translation: mgl32.Vec3{1, 2, 3}},
		{Name: "Socket_Lamp", Translation: mgl32.Vec3{9, 9, 9}},
		{Name: "B"},
	}}

	got, ok := m.Node("Socket_Lamp")
	if !ok {
		t.Fatal("Node reported no match for a name that exists")
	}
	if got.Translation != (mgl32.Vec3{1, 2, 3}) {
		t.Errorf("Node returned %v, want the first Socket_Lamp (index 1), not the duplicate", got.Translation)
	}

	if _, ok := m.Node("Socket_Missing"); ok {
		t.Error("Node reported a match for a name that is not in the model")
	}
}
