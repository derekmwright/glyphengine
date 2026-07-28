package glyphengine

import (
	"math"
	"testing"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/ecs"
)

// flatTerrain returns a heightmap that is level at y across a 100x100 area
// centered on the origin.
func flatTerrain(t *testing.T, y float32) *Heightmap {
	t.Helper()
	const grid = 17
	heights := make([]float32, grid*grid)
	for i := range heights {
		heights[i] = y
	}
	hm, err := NewHeightmap(grid, grid, 100, 100, -50, -50, heights)
	if err != nil {
		t.Fatalf("NewHeightmap: %v", err)
	}
	return hm
}

// spawnBody adds a 1x1x1 body with a velocity at pos.
func spawnBody(s *Scene, pos mgl32.Vec3) ecs.Entity {
	e := s.Spawn()
	s.C.Transform.Set(e, &Transform{Position: pos, Scale: mgl32.Vec3{1, 1, 1}})
	s.C.Collider.Set(e, &Collider{HalfExtents: mgl32.Vec3{0.5, 0.5, 0.5}})
	s.C.Velocity.Set(e, &Velocity{})
	return e
}

// spawnCharacter adds a character controller entity at pos.
func spawnCharacter(s *Scene, pos mgl32.Vec3) ecs.Entity {
	e := s.Spawn()
	s.C.Transform.Set(e, &Transform{Position: pos, Scale: mgl32.Vec3{1, 1, 1}})
	s.C.Collider.Set(e, &Collider{HalfExtents: mgl32.Vec3{0.4, 0.9, 0.4}})
	s.C.Velocity.Set(e, &Velocity{})
	cc := NewCharacterController()
	s.C.CharacterController.Set(e, &cc)
	return e
}

// step runs n fixed ticks at 60Hz.
func step(s *Scene, n int) {
	const dt = 1.0 / 60.0
	for i := 0; i < n; i++ {
		s.Tick(dt)
	}
}

func TestIntegrateBodiesFallsAndRestsOnTerrain(t *testing.T) {
	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))

	body := spawnBody(s, mgl32.Vec3{0, 10, 0})
	step(s, 120) // 2 seconds — plenty to fall 10 units

	tr, _ := s.C.Transform.Get(body)
	// Rest height is ground + halfHeight, with the controller's 1mm bias.
	const want = 0.5 + 0.001
	if math.Abs(float64(tr.Position.Y()-want)) > 0.01 {
		t.Errorf("resting Y = %.4f, want %.4f", tr.Position.Y(), want)
	}
	if v, _ := s.C.Velocity.Get(body); v.Vec.Y() != 0 {
		t.Errorf("resting vertical velocity = %v, want 0", v.Vec.Y())
	}
}

func TestIntegrateBodiesSkipsCharacterControllers(t *testing.T) {
	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))

	ch := spawnCharacter(s, mgl32.Vec3{0, 5, 0})
	before, _ := s.C.Transform.Get(ch)
	startY := before.Position.Y()

	step(s, 30)

	// The integrator must not touch it — only MoveCharacter moves a character.
	after, _ := s.C.Transform.Get(ch)
	if after.Position.Y() != startY {
		t.Errorf("character moved to Y=%.4f during Tick; controllers must be driven by MoveCharacter only", after.Position.Y())
	}
}

func TestIntegrateBodiesWithoutColliderStillFalls(t *testing.T) {
	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))

	// No Collider: nothing to stand on, so it should fall straight through.
	e := s.Spawn()
	s.C.Transform.Set(e, &Transform{Position: mgl32.Vec3{0, 5, 0}, Scale: mgl32.Vec3{1, 1, 1}})
	s.C.Velocity.Set(e, &Velocity{})

	step(s, 120)

	tr, _ := s.C.Transform.Get(e)
	if tr.Position.Y() > -1 {
		t.Errorf("collider-less body Y = %.4f, want it to keep falling past the ground", tr.Position.Y())
	}
}

func TestMoveCharacterWalksAtWalkSpeed(t *testing.T) {
	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))

	ch := spawnCharacter(s, mgl32.Vec3{0, 0.9, 0})
	cc, _ := s.C.CharacterController.Get(ch)

	// Yaw 0 faces -Z, so pure forward input walks toward -Z.
	const dt = 1.0 / 60.0
	const ticks = 60
	for i := 0; i < ticks; i++ {
		s.MoveCharacter(ch, MoveIntent{Forward: 1}, dt)
	}

	tr, _ := s.C.Transform.Get(ch)
	wantZ := -cc.WalkSpeed * dt * ticks
	if math.Abs(float64(tr.Position.Z()-wantZ)) > 0.05 {
		t.Errorf("walked to Z=%.3f, want %.3f", tr.Position.Z(), wantZ)
	}
	if math.Abs(float64(tr.Position.X())) > 0.001 {
		t.Errorf("drifted on X to %.4f, want 0", tr.Position.X())
	}
	if !cc.Grounded {
		t.Error("character should be grounded while walking on flat terrain")
	}
}

func TestMoveCharacterSprintIsFasterThanWalk(t *testing.T) {
	const dt = 1.0 / 60.0
	distance := func(sprint bool) float32 {
		s := NewScene()
		s.SetTerrain(flatTerrain(t, 0))
		ch := spawnCharacter(s, mgl32.Vec3{0, 0.9, 0})
		for i := 0; i < 60; i++ {
			s.MoveCharacter(ch, MoveIntent{Forward: 1, Sprint: sprint}, dt)
		}
		tr, _ := s.C.Transform.Get(ch)
		return -tr.Position.Z()
	}

	walked, sprinted := distance(false), distance(true)
	if sprinted <= walked {
		t.Errorf("sprint covered %.3f, walk covered %.3f; sprint must be faster", sprinted, walked)
	}
}

func TestMoveCharacterBlockedByStaticCollider(t *testing.T) {
	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))

	// A wall 3 units toward -Z, 6 wide and 4 tall.
	wall := s.Spawn()
	s.C.Transform.Set(wall, &Transform{Position: mgl32.Vec3{0, 2, -3}, Scale: mgl32.Vec3{1, 1, 1}})
	s.C.Collider.Set(wall, &Collider{HalfExtents: mgl32.Vec3{3, 2, 0.5}})
	s.C.Static.Set(wall, &Static{})
	s.RebuildStatics()

	ch := spawnCharacter(s, mgl32.Vec3{0, 0.9, 0})

	const dt = 1.0 / 60.0
	for i := 0; i < 120; i++ {
		s.UpdateSpatialGrid()
		s.MoveCharacter(ch, MoveIntent{Forward: 1}, dt)
	}

	tr, _ := s.C.Transform.Get(ch)
	// The wall's near face is at Z=-2.5 and the character is 0.4 deep, so it
	// cannot get past -2.1. Without collision it would be at roughly -8.
	if tr.Position.Z() < -2.2 {
		t.Errorf("character reached Z=%.3f, past the wall face at Z=-2.5", tr.Position.Z())
	}
	if tr.Position.Z() > -1.0 {
		t.Errorf("character only reached Z=%.3f; it should have walked up to the wall", tr.Position.Z())
	}
}

func TestMoveCharacterJumpLeavesTheGround(t *testing.T) {
	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))

	ch := spawnCharacter(s, mgl32.Vec3{0, 0.9, 0})
	const dt = 1.0 / 60.0

	// Settle first so the controller reports grounded.
	s.MoveCharacter(ch, MoveIntent{}, dt)
	cc, _ := s.C.CharacterController.Get(ch)
	if !cc.Grounded {
		t.Fatal("character should be grounded before jumping")
	}

	s.MoveCharacter(ch, MoveIntent{Jump: true}, dt)
	if cc.Grounded {
		t.Error("character should not report grounded on the jump tick")
	}

	tr, _ := s.C.Transform.Get(ch)
	peak := tr.Position.Y()
	for i := 0; i < 60; i++ {
		s.MoveCharacter(ch, MoveIntent{}, dt)
		if tr.Position.Y() > peak {
			peak = tr.Position.Y()
		}
	}
	if peak < 1.5 {
		t.Errorf("jump peaked at Y=%.3f, expected well above the 0.9 stand height", peak)
	}
	if math.Abs(float64(tr.Position.Y()-0.901)) > 0.05 {
		t.Errorf("did not land back on the ground: Y=%.3f", tr.Position.Y())
	}
}

func TestRaycastHitsColliderAndReportsDistance(t *testing.T) {
	s := NewScene()

	box := s.Spawn()
	s.C.Transform.Set(box, &Transform{Position: mgl32.Vec3{0, 0, -10}, Scale: mgl32.Vec3{1, 1, 1}})
	s.C.Collider.Set(box, &Collider{HalfExtents: mgl32.Vec3{1, 1, 1}})

	hit, ok := s.Raycast(mgl32.Vec3{0, 0, 0}, mgl32.Vec3{0, 0, -1}, 100, 0)
	if !ok {
		t.Fatal("expected a hit")
	}
	if hit.Entity != box {
		t.Errorf("hit entity %d, want %d", hit.Entity, box)
	}
	if math.Abs(float64(hit.T-9)) > 0.001 {
		t.Errorf("hit distance %.4f, want 9 (box near face at Z=-9)", hit.T)
	}

	// Excluding the only collider must miss.
	if _, ok := s.Raycast(mgl32.Vec3{0, 0, 0}, mgl32.Vec3{0, 0, -1}, 100, box); ok {
		t.Error("excluded entity was still hit")
	}
}

func TestOverlapAABBFindsAndExcludes(t *testing.T) {
	s := NewScene()
	a := spawnBody(s, mgl32.Vec3{0, 0, 0})
	b := spawnBody(s, mgl32.Vec3{0.5, 0, 0}) // overlaps a
	far := spawnBody(s, mgl32.Vec3{20, 0, 0})

	box := WorldAABB(mustTransform(t, s, a), mustCollider(t, s, a))
	results := s.OverlapAABB(box, a)

	if len(results) != 1 {
		t.Fatalf("got %d overlaps, want 1", len(results))
	}
	if results[0].Entity != b {
		t.Errorf("overlapped entity %d, want %d (far entity %d must not match)", results[0].Entity, b, far)
	}
}

func TestSpatialGridAndLinearScanAgree(t *testing.T) {
	s := NewScene()
	for i := 0; i < 40; i++ {
		spawnBody(s, mgl32.Vec3{float32(i%8) * 4, 0, float32(i/8) * 4})
	}
	probe := AABB{Min: mgl32.Vec3{-1, -1, -1}, Max: mgl32.Vec3{9, 1, 9}}

	// No grid: linear scan over every collider.
	linear := len(s.OverlapAABB(probe, 0))

	s.UpdateSpatialGrid()
	grid := len(s.OverlapAABB(probe, 0))

	if linear != grid {
		t.Errorf("spatial grid found %d overlaps, linear scan found %d; the grid must not change results",
			grid, linear)
	}
	if linear == 0 {
		t.Fatal("expected the probe box to overlap something")
	}
}

func TestDayNightAdvancesInSeconds(t *testing.T) {
	dn := DayNight{Speed: 1.0 / 120.0} // one full cycle every two minutes
	for i := 0; i < 60; i++ {
		dn.Advance(1.0 / 60.0) // one second total
	}
	want := float32(1.0 / 120.0)
	if math.Abs(float64(dn.TimeOfDay-want)) > 1e-5 {
		t.Errorf("after 1s TimeOfDay = %.6f, want %.6f", dn.TimeOfDay, want)
	}

	// It must wrap rather than run past 1.
	dn.TimeOfDay = 0.99
	dn.Speed = 1
	dn.Advance(0.02)
	if dn.TimeOfDay >= 1 || dn.TimeOfDay < 0 {
		t.Errorf("TimeOfDay = %.6f, want it wrapped into [0,1)", dn.TimeOfDay)
	}
}

func TestSceneDespawnRemovesComponents(t *testing.T) {
	s := NewScene()
	e := spawnBody(s, mgl32.Vec3{0, 0, 0})

	s.Despawn(e)

	if s.C.Transform.Has(e) || s.C.Collider.Has(e) || s.C.Velocity.Has(e) {
		t.Error("Despawn left components behind")
	}
	if s.World().Alive(e) {
		t.Error("Despawn left the entity alive")
	}
}

func mustTransform(t *testing.T, s *Scene, e ecs.Entity) *Transform {
	t.Helper()
	tr, ok := s.C.Transform.Get(e)
	if !ok {
		t.Fatalf("entity %d has no Transform", e)
	}
	return tr
}

func mustCollider(t *testing.T, s *Scene, e ecs.Entity) *Collider {
	t.Helper()
	c, ok := s.C.Collider.Get(e)
	if !ok {
		t.Fatalf("entity %d has no Collider", e)
	}
	return c
}
