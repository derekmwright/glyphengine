package glyphengine

import (
	"testing"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/ecs"
	"github.com/derekmwright/glyphengine/renderer"
)

// The blended pass is ordering, and ordering is where its bugs are. Blending is
// not commutative, so two overlapping translucent objects composited the wrong
// way round produce the wrong colour, and nothing about that fails loudly --
// the frame renders, the validation layer is silent, and the image is subtly
// wrong in a way only a person looking at it notices.
//
// These run without a GPU: buildDrawList reads components, transforms and the
// camera, none of which need a device. A renderer.Mesh with bounds and no
// buffers is enough, because nothing here records a command.

// translucentScene puts n meshes in a line receding from the origin along -Z,
// each translucent, so a camera at +Z sees them near to far in spawn order.
func translucentScene(t *testing.T, alpha float32, n int) *Engine {
	t.Helper()
	mesh := &renderer.Mesh{BoundRadius: 1}
	e := &Engine{Scene: NewScene(), cameraEye: mgl32.Vec3{0, 0, 0}}
	for i := 0; i < n; i++ {
		ent := e.Scene.Spawn()
		z := -float32(i+1) * 10
		e.Scene.C.Transform.Set(ent, &Transform{Position: mgl32.Vec3{0, 0, z}, Scale: mgl32.Vec3{1, 1, 1}})
		e.Scene.C.MeshRef.Set(ent, &MeshRef{Mesh: mesh})
		if alpha > 0 {
			e.Scene.C.Translucent.Set(ent, &Translucent{Alpha: alpha})
		}
	}
	return e
}

// noCull is a projection that keeps everything in frustum, so these tests
// measure ordering rather than culling.
func noCull() mgl32.Mat4 {
	return mgl32.Perspective(mgl32.DegToRad(120), 1, 0.01, 10000).Mul4(
		mgl32.LookAtV(mgl32.Vec3{0, 0, 0}, mgl32.Vec3{0, 0, -1}, mgl32.Vec3{0, 1, 0}))
}

func TestTranslucentDrawsSortBackToFront(t *testing.T) {
	e := translucentScene(t, 0.5, 4)
	draws := e.buildDrawList(noCull(), false, mgl32.Mat4{})

	if len(draws) != 4 {
		t.Fatalf("expected 4 draws, got %d -- the rest of this test would prove nothing", len(draws))
	}

	eye := [3]float32{0, 0, 0}
	for i := 1; i < len(draws); i++ {
		prev, cur := draws[i-1].ViewDepth(eye), draws[i].ViewDepth(eye)
		if prev < cur {
			t.Errorf("draw %d is nearer than draw %d (%.1f then %.1f): blending needs far first",
				i-1, i, prev, cur)
		}
	}

	// The scene was spawned near to far, so a pass that did not sort would come
	// back in exactly the opposite order. Assert the sort actually moved
	// something rather than that the input happened to be right.
	if first, last := draws[0].ViewDepth(eye), draws[len(draws)-1].ViewDepth(eye); first <= last {
		t.Fatal("the farthest draw is not first, so nothing was reordered")
	}
}

// Moving the camera to the other side has to flip the order. A sort done once
// at spawn, or against a fixed point rather than the eye, passes the test above
// and fails this one.
//
// The assertion names the object it expects rather than merely requiring the
// two answers to differ. "Differ" was the first version of this test and it
// passed with the sort deleted entirely: unsorted draws come back in ECS query
// order, which is not stable between calls, so the two runs disagreed by
// accident and the check proved nothing.
func TestTranslucentOrderFollowsTheCamera(t *testing.T) {
	// Four objects at z = -10, -20, -30, -40.
	e := translucentScene(t, 0.5, 4)

	front := e.buildDrawList(noCull(), false, mgl32.Mat4{})
	firstFromFront := front[0].Model[14]
	if firstFromFront != -40 {
		t.Errorf("from the origin the farthest object should be drawn first, got z=%.1f want -40", firstFromFront)
	}

	// Past the far end of the line, so the order has to invert completely.
	e.cameraEye = mgl32.Vec3{0, 0, -100}
	behind := e.buildDrawList(noCull(), false, mgl32.Mat4{})
	firstFromBehind := behind[0].Model[14]
	if firstFromBehind != -10 {
		t.Errorf("from z=-100 the farthest object should be drawn first, got z=%.1f want -10", firstFromBehind)
	}
}

func TestTranslucentDrawsComeAfterOpaqueOnes(t *testing.T) {
	mesh := &renderer.Mesh{BoundRadius: 1}
	e := &Engine{Scene: NewScene(), cameraEye: mgl32.Vec3{0, 0, 0}}

	// Interleaved at spawn so ECS order cannot produce the right answer.
	for i := 0; i < 6; i++ {
		ent := e.Scene.Spawn()
		e.Scene.C.Transform.Set(ent, &Transform{Position: mgl32.Vec3{0, 0, -float32(i+1) * 10}, Scale: mgl32.Vec3{1, 1, 1}})
		e.Scene.C.MeshRef.Set(ent, &MeshRef{Mesh: mesh})
		if i%2 == 0 {
			e.Scene.C.Translucent.Set(ent, &Translucent{Alpha: 0.5})
		}
	}

	draws := e.buildDrawList(noCull(), false, mgl32.Mat4{})
	seenTranslucent := false
	opaque, translucent := 0, 0
	for i := range draws {
		if draws[i].IsTranslucent() {
			seenTranslucent = true
			translucent++
			continue
		}
		opaque++
		if seenTranslucent {
			t.Fatalf("draw %d is opaque but follows a translucent one", i)
		}
	}
	if opaque != 3 || translucent != 3 {
		t.Fatalf("expected 3 opaque and 3 translucent, got %d and %d", opaque, translucent)
	}
}

// Alpha at or above 1 is opaque, and takes the opaque pipeline. A game fading
// something in runs through 1 on the last frame and should not pay for the
// blended path there.
func TestAlphaOfOneIsOpaque(t *testing.T) {
	e := translucentScene(t, 1, 3)
	draws := e.buildDrawList(noCull(), false, mgl32.Mat4{})
	if len(draws) != 3 {
		t.Fatalf("expected 3 draws, got %d", len(draws))
	}
	for i := range draws {
		if draws[i].IsTranslucent() {
			t.Errorf("draw %d with Alpha 1 went through the blended path", i)
		}
		if draws[i].NoCastShadow {
			t.Errorf("draw %d with Alpha 1 stopped casting a shadow", i)
		}
	}
}

// Alpha at or below 0 is faded all the way out, which is Hidden by another
// name. Drawing it would blend a fully transparent object over the frame for
// nothing.
func TestAlphaOfZeroDrawsNothing(t *testing.T) {
	e := translucentScene(t, 0, 3) // no component at all
	if got := len(e.buildDrawList(noCull(), false, mgl32.Mat4{})); got != 3 {
		t.Fatalf("control: expected 3 draws without the component, got %d", got)
	}

	e = translucentScene(t, 0.5, 3)
	e.Scene.C.Translucent.Each(func(_ ecs.Entity, tr *Translucent) { tr.Alpha = 0 })
	if got := len(e.buildDrawList(noCull(), false, mgl32.Mat4{})); got != 0 {
		t.Fatalf("expected 0 draws at Alpha 0, got %d", got)
	}
}

// A translucent placement preview that threw a solid shadow would read as a
// real building, which is the whole thing this is for.
func TestTranslucentDrawsDoNotCastShadows(t *testing.T) {
	e := translucentScene(t, 0.5, 3)
	draws := e.buildDrawList(noCull(), false, mgl32.Mat4{})
	if len(draws) == 0 {
		t.Fatal("no draws, so this proves nothing")
	}
	for i := range draws {
		if !draws[i].NoCastShadow {
			t.Errorf("translucent draw %d still casts a shadow", i)
		}
	}
}

// Which paths have a blended variant, and which stay opaque rather than being
// rerouted and quietly losing the thing that made them special.
//
// Silent is the failure mode to avoid: a rerouted material draw renders, and
// just has no normal map. Better to draw it opaque, which is visible, than to
// draw it blended and wrong.
//
// A skinned mesh that also carries a Material is the case worth pinning. It
// goes through the skinned *material* pipeline, which has no blended twin, so
// it stays opaque even though both of its halves look supported.
func TestOnlyPathsWithABlendedVariantGoTranslucent(t *testing.T) {
	mesh := &renderer.Mesh{BoundRadius: 1}
	cases := []struct {
		name string
		want bool
		d    renderer.RenderObject
	}{
		{"plain lit", true, renderer.RenderObject{Mesh: mesh, Alpha: 0.5}},
		{"skinned", true, renderer.RenderObject{Mesh: mesh, Alpha: 0.5, Joints: &renderer.JointBuffer{}}},
		{"terrain", false, renderer.RenderObject{Mesh: mesh, Alpha: 0.5, TerrainMat: &renderer.TerrainMaterial{}}},
		{"water", false, renderer.RenderObject{Mesh: mesh, Alpha: 0.5, Water: &renderer.WaterParams{}}},
		{"material", false, renderer.RenderObject{Mesh: mesh, Alpha: 0.5, Material: &renderer.Material{}}},
		{"skinned material", false, renderer.RenderObject{
			Mesh: mesh, Alpha: 0.5, Joints: &renderer.JointBuffer{}, Material: &renderer.Material{},
		}},
	}
	for _, tc := range cases {
		if got := tc.d.IsTranslucent(); got != tc.want {
			t.Errorf("%s: IsTranslucent() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A skinned character fading out must not keep casting a solid shadow, for the
// same reason a placement ghost must not: the shadow is what gives away that
// the thing is still really there.
func TestTranslucentSkinnedDoesNotCastShadows(t *testing.T) {
	mesh := &renderer.Mesh{BoundRadius: 1}
	e := &Engine{Scene: NewScene(), cameraEye: mgl32.Vec3{0, 0, 0}}

	ent := e.Scene.Spawn()
	e.Scene.C.Transform.Set(ent, &Transform{Position: mgl32.Vec3{0, 0, -10}, Scale: mgl32.Vec3{1, 1, 1}})
	e.Scene.C.MeshRef.Set(ent, &MeshRef{Mesh: mesh})
	e.Scene.C.Translucent.Set(ent, &Translucent{Alpha: 0.5})

	draws := e.buildDrawList(noCull(), false, mgl32.Mat4{})
	if len(draws) != 1 {
		t.Fatalf("expected 1 draw, got %d", len(draws))
	}
	if !draws[0].NoCastShadow {
		t.Error("a translucent draw still casts a shadow")
	}
}
