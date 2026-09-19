package main

import (
	"math"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/qmuntal/gltf"
)

// TestNodeWorldThroughParentChain builds a three-level chain -- root
// (translation), middle (rotation about Y), leaf (scale) -- and checks the
// leaf's World against a hand-multiplied matrix, not against the same
// composition code being tested: buildNodes composes Local matrices with
// mgl32's Mat4.Mul4, and this recomputes the product independently with
// plain trigonometry so a bug in composition ORDER (parent*child vs
// child*parent) would not cancel itself out.
func TestNodeWorldThroughParentChain(t *testing.T) {
	doc := &gltf.Document{
		Nodes: []*gltf.Node{
			{Name: "root", Translation: [3]float64{10, 0, 5}, Children: []int{1}},
			{Name: "middle", Rotation: quatY(math.Pi / 2), Children: []int{2}},
			{Name: "leaf", Scale: [3]float64{2, 1, 1}},
		},
	}
	nodes := buildNodes(doc)
	leaf := nodes[2]

	// Apply the leaf's World to a known local point by hand and compare
	// against the formula for Translate(10,0,5) * RotY(90) * Scale(2,1,1):
	// scale first (x*2), then rotate 90 about Y (x'=z, z'=-x for a
	// right-handed Y-up rotation), then translate.
	p := [3]float32{3, 4, 5} // local point
	got := leaf.World.Mul4x1(vec4(p))
	scaled := [3]float64{float64(p[0]) * 2, float64(p[1]), float64(p[2])}
	rotated := [3]float64{scaled[2], scaled[1], -scaled[0]}
	want := [3]float64{rotated[0] + 10, rotated[1] + 0, rotated[2] + 5}

	if !closeVec(got, want, 1e-4) {
		t.Errorf("leaf.World * (3,4,5) = %v, want %v", got, want)
	}
}

// TestNodeLocalTransformMatrix covers a node authored with Matrix instead of
// TRS (the shape a Blender export uses whenever TRS alone cannot represent
// the transform, e.g. a shear or certain mirrors -- see
// docs/agents/blender-pipeline.md's "Mirrored objects" section).
func TestNodeLocalTransformMatrix(t *testing.T) {
	// A translation-only matrix (column-major, as glTF stores it) to (7,8,9).
	m := [16]float64{
		1, 0, 0, 0,
		0, 1, 0, 0,
		0, 0, 1, 0,
		7, 8, 9, 1,
	}
	n := &gltf.Node{Matrix: m}
	local := nodeLocalTransform(n)
	got := local.Mul4x1(vec4([3]float32{0, 0, 0}))
	want := [3]float64{7, 8, 9}
	if !closeVec(got, want, 1e-6) {
		t.Errorf("matrix-authored node local * origin = %v, want %v", got, want)
	}
}

// TestFindMeshNode covers the three selection outcomes: an unambiguous
// auto-pick, an explicit name, and the two error shapes (ambiguous, not
// found).
func TestFindMeshNode(t *testing.T) {
	nodes := []node{
		{Name: "Ground", Mesh: -1},
		{Name: "Terrain", Mesh: 0},
	}
	if i, err := findMeshNode(nodes, ""); err != nil || nodes[i].Name != "Terrain" {
		t.Errorf("auto-pick with one mesh node: got %v, %v, want index of Terrain", i, err)
	}
	if i, err := findMeshNode(nodes, "Terrain"); err != nil || i != 1 {
		t.Errorf("explicit -node Terrain: got %v, %v, want 1", i, err)
	}
	if _, err := findMeshNode(nodes, "Nope"); err == nil {
		t.Error("-node Nope succeeded, want an error naming the available nodes")
	}

	ambiguous := []node{
		{Name: "Terrain", Mesh: 0},
		{Name: "Building", Mesh: 1},
	}
	if _, err := findMeshNode(ambiguous, ""); err == nil {
		t.Error("auto-pick with two mesh nodes succeeded, want an ambiguity error")
	}
}

func quatY(theta float64) [4]float64 {
	return [4]float64{0, math.Sin(theta / 2), 0, math.Cos(theta / 2)}
}

func vec4(p [3]float32) mgl32.Vec4 {
	return mgl32.Vec4{p[0], p[1], p[2], 1}
}

func closeVec(got mgl32.Vec4, want [3]float64, eps float64) bool {
	return math.Abs(float64(got[0])-want[0]) < eps &&
		math.Abs(float64(got[1])-want[1]) < eps &&
		math.Abs(float64(got[2])-want[2]) < eps
}
