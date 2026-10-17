package glyphengine

import (
	"testing"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/ecs"
)

// TestLivePointersMutateInPlace backs the example in
// docs/agents/scene-entities.md: the pointer from Store.Get and the ones a
// query hands its callback are the stored components, so writing through them
// needs no Set.
func TestLivePointersMutateInPlace(t *testing.T) {
	s := NewScene()
	e := s.Spawn()
	s.C.Transform.Set(e, &Transform{Scale: mgl32.Vec3{1, 1, 1}})
	s.C.Velocity.Set(e, &Velocity{})

	if tr, ok := s.C.Transform.Get(e); ok {
		tr.Position = tr.Position.Add(mgl32.Vec3{0, 2, 0})
	}
	ecs.Query2(s.C.Transform, s.C.Velocity, func(_ ecs.Entity, tr *Transform, _ *Velocity) {
		tr.Position = tr.Position.Add(mgl32.Vec3{1, 0, 0})
	})

	got, _ := s.C.Transform.Get(e)
	if got.Position != (mgl32.Vec3{1, 2, 0}) {
		t.Errorf("position = %v after in-place writes, want {1 2 0}", got.Position)
	}
}
