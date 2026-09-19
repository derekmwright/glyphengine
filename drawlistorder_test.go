package glyphengine

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/ecs"
	"github.com/derekmwright/glyphengine/renderer"
)

// The recorded draw sequence has to be a function of the scene and of nothing
// else. It was not: ecs.Query2 ranges a map[Entity]*T, Go randomises that per
// range statement, and slices.SortFunc is unstable -- so every draw the sort
// left tied kept whatever position the walk happened to hand it, on every frame
// of every run. Issue #53.
//
// "The scene" here means the entities, their ids, their components and the
// resources they point at. It does NOT mean the order the component stores were
// filled in, which is what these permute: that is an implementation detail of
// how a game happened to build its world, and two games that build the same
// world differently must render it in the same sequence.
//
// 200 assemblies rather than two, because a single range statement's
// randomisation can repeat: with a handful of entities the map walk lands on
// the same order often enough that a two-run test passes on the broken code
// perhaps one time in ten.
const drawOrderAssemblies = 200

// drawObject is one entity's worth of scene, held outside the world so the same
// scene can be rebuilt with its stores filled in a different order.
type drawObject struct {
	entity ecs.Entity

	pos     mgl32.Vec3
	mesh    *renderer.Mesh
	texture *renderer.Texture
	alpha   float32
	double  bool
	joints  *renderer.JointBuffer
}

// tieScene is built to tie in every place the comparison can tie, because a
// scene with no ties cannot tell a total order from a partial one.
//
// Ties it contains:
//   - four opaque draws sharing one texture and one pipeline variant, which is
//     one SortKey between them;
//   - two more in the double-sided variant, likewise;
//   - two skinned draws, likewise;
//   - three blended draws at exactly the same distance from the eye, which is
//     the case that would flicker between frames rather than merely differ
//     between runs.
//
// The textures are allocated fresh on every assembly, so no two of them sit at
// the same address twice. That covers the address half of the bug only as far
// as a test without a GPU can: a Texture built here has no descriptor set, so
// the old key read zero out of all of them. The evidence that a real
// descriptor's address no longer reaches the order is the state-trace gate in
// `task determinism`, which runs two real processes.
func tieScene(t *testing.T) (*Engine, []drawObject) {
	t.Helper()

	mesh := &renderer.Mesh{BoundRadius: 1}
	shared := &renderer.Texture{}
	other := &renderer.Texture{}
	joints := &renderer.JointBuffer{}

	e := &Engine{Scene: NewScene(), cameraEye: mgl32.Vec3{0, 0, 0}}

	var objs []drawObject
	add := func(o drawObject) {
		o.entity = e.Scene.Spawn()
		o.mesh = mesh
		objs = append(objs, o)
	}

	// Opaque, one texture, one variant: four-way tie on SortKey.
	for i := 0; i < 4; i++ {
		add(drawObject{pos: mgl32.Vec3{float32(i), 0, -20}, texture: shared})
	}
	// A second texture, so the sort has something to group apart.
	for i := 0; i < 2; i++ {
		add(drawObject{pos: mgl32.Vec3{float32(i), 2, -20}, texture: other})
	}
	// Double-sided and skinned variants, tied within themselves.
	for i := 0; i < 2; i++ {
		add(drawObject{pos: mgl32.Vec3{float32(i), 4, -20}, texture: shared, double: true})
	}
	for i := 0; i < 2; i++ {
		add(drawObject{pos: mgl32.Vec3{float32(i), 6, -20}, texture: shared, joints: joints})
	}
	// Blended, three of them at one distance and two at others, so the tail is
	// both ordered by depth and tied inside that order.
	add(drawObject{pos: mgl32.Vec3{0, 0, -5}, alpha: 0.5})
	for i := 0; i < 3; i++ {
		add(drawObject{pos: mgl32.Vec3{float32(i) * 0.0, 0, -10}, alpha: 0.5})
	}
	add(drawObject{pos: mgl32.Vec3{0, 0, -15}, alpha: 0.5})

	return e, objs
}

// fill puts the objects into the component stores in the given order. The
// stores are emptied first so the map itself is rebuilt, which moves the walk
// order further than re-ranging the same map does.
func fill(e *Engine, objs []drawObject, order []int) {
	c := e.Scene.C
	for _, o := range objs {
		c.Transform.Remove(o.entity)
		c.MeshRef.Remove(o.entity)
		c.MaterialRef.Remove(o.entity)
		c.Translucent.Remove(o.entity)
		c.DoubleSided.Remove(o.entity)
		c.SkeletonRef.Remove(o.entity)
	}
	for _, i := range order {
		o := objs[i]
		c.Transform.Set(o.entity, &Transform{Position: o.pos, Scale: mgl32.Vec3{1, 1, 1}})
		c.MeshRef.Set(o.entity, &MeshRef{Mesh: o.mesh})
		if o.texture != nil {
			c.MaterialRef.Set(o.entity, &MaterialRef{Texture: o.texture})
		}
		if o.alpha > 0 {
			c.Translucent.Set(o.entity, &Translucent{Alpha: o.alpha})
		}
		if o.double {
			c.DoubleSided.Set(o.entity, &DoubleSided{})
		}
		if o.joints != nil {
			c.SkeletonRef.Set(o.entity, &SkeletonRef{JointBuffer: o.joints, Skinned: true})
		}
	}
}

// sequence renders the draw list as one comparable line per draw. It carries
// what the recorder would act on -- which entity, which pipeline variant and
// resource, how far away, how opaque -- so a difference names the draw that
// moved rather than just the index.
func sequence(draws []renderer.RenderObject, eye [3]float32) []string {
	out := make([]string, len(draws))
	for i := range draws {
		d := &draws[i]
		out[i] = fmt.Sprintf("entity=%d key=%016x depth=%.4f alpha=%.2f",
			d.SortID, d.SortKey(), d.ViewDepth(eye), d.Alpha)
	}
	return out
}

func TestDrawListOrderDoesNotDependOnInsertionOrder(t *testing.T) {
	e, objs := tieScene(t)
	eye := [3]float32{0, 0, 0}

	order := make([]int, len(objs))
	for i := range order {
		order[i] = i
	}

	rng := rand.New(rand.NewSource(1))
	var want []string
	for n := 0; n < drawOrderAssemblies; n++ {
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		fill(e, objs, order)

		draws := e.buildDrawList(noCull(), false, mgl32.Mat4{})
		if len(draws) != len(objs) {
			t.Fatalf("assembly %d produced %d draws, want %d -- the rest of this test would prove nothing",
				n, len(draws), len(objs))
		}
		got := sequence(draws, eye)

		if want == nil {
			want = got
			assertScenePutsTheSortToWork(t, draws, eye)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("assembly %d differs at draw %d:\n  got  %s\n  want %s\n"+
					"the recorded sequence depends on the order the stores were filled",
					n, i, got[i], want[i])
			}
		}
	}
}

// assertScenePutsTheSortToWork fails if the scene has no ties, which is the way
// this test would go quietly vacuous: without them every draw is ordered by its
// key alone and any sort at all would pass.
func assertScenePutsTheSortToWork(t *testing.T, draws []renderer.RenderObject, eye [3]float32) {
	t.Helper()

	keyTies, depthTies := 0, 0
	for i := 1; i < len(draws); i++ {
		a, b := &draws[i-1], &draws[i]
		switch {
		case a.IsTranslucent() && b.IsTranslucent():
			if a.ViewDepth(eye) == b.ViewDepth(eye) {
				depthTies++
			}
		case !a.IsTranslucent() && !b.IsTranslucent():
			if a.SortKey() == b.SortKey() {
				keyTies++
			}
		}
	}
	if keyTies == 0 {
		t.Fatal("no two opaque draws share a sort key: this scene cannot detect an unstable sort")
	}
	if depthTies == 0 {
		t.Fatal("no two blended draws are at the same depth: this scene cannot detect the flicker case")
	}
}

// The blended tail is the half where order is the image, so it gets its own
// assertion rather than riding on the sequence comparison: a change that made
// the whole list deterministic while losing back-to-front would pass the test
// above and quietly composite every ghost the wrong way round.
func TestBlendedTailStaysBackToFrontUnderEveryInsertionOrder(t *testing.T) {
	e, objs := tieScene(t)
	eye := [3]float32{0, 0, 0}

	order := make([]int, len(objs))
	for i := range order {
		order[i] = i
	}

	rng := rand.New(rand.NewSource(2))
	for n := 0; n < drawOrderAssemblies; n++ {
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		fill(e, objs, order)

		draws := e.buildDrawList(noCull(), false, mgl32.Mat4{})
		seenBlended := false
		for i := range draws {
			d := &draws[i]
			if !d.IsTranslucent() {
				if seenBlended {
					t.Fatalf("assembly %d: an opaque draw sits after a blended one at index %d", n, i)
				}
				continue
			}
			seenBlended = true
			if i == 0 || !draws[i-1].IsTranslucent() {
				continue
			}
			if prev, cur := draws[i-1].ViewDepth(eye), d.ViewDepth(eye); prev < cur {
				t.Fatalf("assembly %d: draw %d is nearer than draw %d (%.2f then %.2f): blending needs far first",
					n, i-1, i, prev, cur)
			}
		}
	}
}

// The reverse provocation exists to stand in for a permutation the map walk
// could have produced. Now that the sort has a total order it must come out at
// exactly the same sequence, which is a stronger statement than the capture
// gate's "the picture did not change" and costs no GPU to make.
func TestReversedInputSortsToTheSameSequence(t *testing.T) {
	e, objs := tieScene(t)
	eye := [3]float32{0, 0, 0}

	order := make([]int, len(objs))
	for i := range order {
		order[i] = i
	}
	fill(e, objs, order)
	want := sequence(e.buildDrawList(noCull(), false, mgl32.Mat4{}), eye)

	e.provoke.reverse = true
	got := sequence(e.buildDrawList(noCull(), false, mgl32.Mat4{}), eye)

	if len(got) != len(want) {
		t.Fatalf("reversed run produced %d draws, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("reversing the list before the sort changed draw %d:\n  got  %s\n  want %s", i, got[i], want[i])
		}
	}
}
