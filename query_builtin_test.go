package glyphengine_test

import (
	"testing"

	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/ecs"
)

// ghostBackend is the backend a game actually writes first: the engine's own
// queries, with one thing changed. Here the change is that one entity does not
// exist as far as collision is concerned.
//
// It is in the external test package on purpose. Every backend in
// query_backend_test.go delegates to raycastBuiltin and overlapAABBBuiltin,
// which no game can name, so none of them shows that the seam is usable from
// outside -- and the delegation a game CAN spell, scene.Raycast, is the backend
// calling itself. This one has only what a game has.
type ghostBackend struct {
	inner glyph.QueryBackend
	ghost ecs.Entity

	// depth and reentered catch a BuiltinQueries that routes back through
	// Scene.Queries. Left alone that is a stack overflow, which kills the test
	// binary instead of failing the test; this turns it into a failure.
	depth     int
	reentered bool
}

func (b *ghostBackend) enter() bool {
	b.depth++
	if b.depth > 1 {
		b.reentered = true
		return false
	}
	return true
}

func (b *ghostBackend) Raycast(origin, dir mgl32.Vec3, maxDist float32, exclude ecs.Entity) (glyph.RayHit, bool) {
	defer func() { b.depth-- }()
	if !b.enter() {
		return glyph.RayHit{}, false
	}
	hit, ok := b.inner.Raycast(origin, dir, maxDist, exclude)
	if ok && hit.Entity == b.ghost {
		// Only one entity can be excluded per cast, and the tests below never
		// need two, so the caller's own exclusion is given up rather than
		// re-cast from behind the ghost.
		return b.inner.Raycast(origin, dir, maxDist, b.ghost)
	}
	return hit, ok
}

func (b *ghostBackend) OverlapAABB(box glyph.AABB, exclude ecs.Entity) []glyph.OverlapResult {
	defer func() { b.depth-- }()
	if !b.enter() {
		return nil
	}
	var kept []glyph.OverlapResult
	for _, r := range b.inner.OverlapAABB(box, exclude) {
		if r.Entity != b.ghost {
			kept = append(kept, r)
		}
	}
	return kept
}

func spawnWall(s *glyph.Scene, z float32) ecs.Entity {
	e := s.Spawn()
	s.C.Transform.Set(e, &glyph.Transform{Position: mgl32.Vec3{0, 0, z}, Scale: mgl32.Vec3{1, 1, 1}})
	s.C.Collider.Set(e, &glyph.Collider{HalfExtents: mgl32.Vec3{5, 5, 0.5}})
	return e
}

// TestABackendCanDelegateToTheBuiltinQueries: a replacement built from exported
// API alone can hand the engine's implementation the part of the question it
// does not want to answer, and BuiltinQueries answers it without coming back
// through Scene.Queries.
//
// Verified to fail: with builtinQueries.Raycast calling b.s.Raycast, and
// separately with builtinQueries.OverlapAABB calling b.s.OverlapAABB -- which is
// what a game without BuiltinQueries would have had to write -- the backend is
// re-entered and each half reports it.
func TestABackendCanDelegateToTheBuiltinQueries(t *testing.T) {
	s := glyph.NewScene()
	near := spawnWall(s, -3)
	far := spawnWall(s, -6)

	backend := &ghostBackend{inner: s.BuiltinQueries(), ghost: near}
	s.Queries = backend

	origin, dir := mgl32.Vec3{0, 0, 0}, mgl32.Vec3{0, 0, -1}

	hit, ok := s.Raycast(origin, dir, 20, 0)
	if backend.reentered {
		t.Fatal("BuiltinQueries().Raycast came back through Scene.Queries")
	}
	if !ok || hit.Entity != far {
		t.Fatalf("through the backend the ray hit entity %d (ok=%v), want the far wall %d: the near one is a ghost", hit.Entity, ok, far)
	}

	// And the built-in one, asked directly while a replacement is installed,
	// still answers as the engine does -- it has not become the replacement.
	hit, ok = s.BuiltinQueries().Raycast(origin, dir, 20, 0)
	if !ok || hit.Entity != near {
		t.Fatalf("the built-in ray hit entity %d (ok=%v), want the near wall %d", hit.Entity, ok, near)
	}

	box := glyph.AABB{Min: mgl32.Vec3{-1, -1, -8}, Max: mgl32.Vec3{1, 1, 0}}
	got := s.OverlapAABB(box, 0)
	if backend.reentered {
		t.Fatal("BuiltinQueries().OverlapAABB came back through Scene.Queries")
	}
	if len(got) != 1 || got[0].Entity != far {
		t.Fatalf("through the backend the box overlaps %v, want only the far wall %d", got, far)
	}
	if all := s.BuiltinQueries().OverlapAABB(box, 0); len(all) != 2 || all[0].Entity != near || all[1].Entity != far {
		t.Fatalf("the built-in overlap returned %v, want both walls in ascending id order", all)
	}
}
