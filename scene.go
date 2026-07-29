package glyphengine

import (
	"math"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/ecs"
)

// DefaultGravity is the downward acceleration applied by IntegrateBodies and
// the character controller, in world units per second squared.
const DefaultGravity = float32(20.0)

// System is a per-tick callback registered on a Scene. Systems run in
// registration order, after the built-in day/night, integration, and
// pathfinding steps.
type System func(s *Scene, dt float32)

// PointLight describes an unshadowed point light for the renderer.
type PointLight struct {
	Pos   mgl32.Vec3
	Range float32
	Color mgl32.Vec3
}

// Scene owns simulation state: the ECS world, component stores, physics
// acceleration structures, terrain, and the day/night cycle. It has no
// renderer or window dependency, so a headless tool or test can drive one
// directly — Engine adds the frame loop and drawing on top.
type Scene struct {
	world *ecs.World

	// C holds the engine component stores. Games keep their own component
	// structs; the engine never sees them.
	C *Components

	// Env is the sky, light and air around the scene. It is an interface so a
	// game can replace the whole model; see EnvironmentSource. Nil means an
	// empty world -- no sky, no directional light, no fog.
	Env EnvironmentSource

	// envState is Env resolved for the current frame, refreshed once per Tick
	// and once before drawing, so a frame never sees it change under it.
	envState EnvironmentState

	// tickCount is the simulation clock; see TickCount.
	tickCount uint64

	// Gravity is the downward acceleration used by IntegrateBodies and
	// MoveCharacter. Defaults to DefaultGravity.
	Gravity float32

	// Interpolate makes Tick record each non-Static entity's transform before
	// simulating, so a renderer can blend between ticks instead of showing
	// 60Hz steps on a faster display.
	//
	// Off by default, because it is pure cost for anything that does not draw:
	// a headless server ticking the same Scene has no use for it. Engine turns
	// it on — see WithInterpolation.
	Interpolate bool

	// Terrain, when set, provides O(1) ground height for movement and is
	// tested first by downward raycasts.
	Terrain *Heightmap

	// SpatialGrid indexes moving entities for broad-phase queries. Rebuild it
	// with UpdateSpatialGrid. Nil falls back to a linear scan.
	SpatialGrid *SpatialGrid

	// StaticGrid indexes entities tagged Static. Built once by RebuildStatics.
	StaticGrid *SpatialGrid

	// NavGrid and PathFinder are optional A* pathfinding over the terrain.
	NavGrid    *NavGrid
	PathFinder *PathFinder

	pointPos    [3]float32
	pointRange  float32
	pointColor  [3]float32
	pointLights []PointLight

	// staticColliderXZ caches XZ positions of static colliders for the
	// linear-scan fallback used when StaticGrid is nil.
	staticColliderXZ [][2]float32

	// Frozen world-space AABBs for the parallel movement phase. When active,
	// OverlapAABB and Raycast read collider geometry from this snapshot instead
	// of live Transforms, so movement goroutines never read a neighbor's
	// position while another goroutine writes it. Built and cleared by
	// MoveCharactersParallel.
	collisionAABBs       map[ecs.Entity]AABB
	useCollisionSnapshot bool

	systems []System
}

// NewScene creates a Scene with an empty ECS world and default state.
func NewScene() *Scene {
	w := ecs.NewWorld()
	return &Scene{
		world:   w,
		C:       NewComponents(w),
		Env:     DefaultEnvironment(),
		Gravity: DefaultGravity,
	}
}

// World returns the ECS world.
func (s *Scene) World() *ecs.World { return s.world }

// Spawn creates a new entity with no components.
func (s *Scene) Spawn() ecs.Entity { return s.world.Spawn() }

// Despawn removes an entity and every component attached to it, including
// components in stores the game registered on the same World.
//
// Do not call this from inside a query over a store you are iterating —
// collect the entities first, then despawn after the query returns.
func (s *Scene) Despawn(entity ecs.Entity) { s.world.Despawn(entity) }

// SetTerrain installs a heightmap used for ground snapping and downward
// raycasts. Pass nil to remove it.
func (s *Scene) SetTerrain(hm *Heightmap) { s.Terrain = hm }

// AddSystem registers a per-tick callback. Systems run in registration order
// at the end of Tick.
func (s *Scene) AddSystem(fn System) { s.systems = append(s.systems, fn) }

// TickCount returns the number of ticks this scene has run. It increments once
// per Scene.Tick and never resets.
//
// This is the scene's simulation clock, and the thing to stamp on anything that
// has to be correlated across machines: a client and an authoritative server
// running the same tick rate agree on what "tick 4213" means, where wall-clock
// time and frame numbers do not.
func (s *Scene) TickCount() uint64 { return s.tickCount }

// Tick advances the scene by dt seconds: day/night, rigid-body integration,
// queued pathfinding, then registered systems in order.
//
// Character controller entities are driven by MoveCharacter instead, and are
// skipped by the integrator — see IntegrateBodies.
func (s *Scene) Tick(dt float32) {
	s.tickCount++

	// Record where everything is before simulating, so rendering can blend
	// from here to wherever this tick leaves things.
	if s.Interpolate {
		s.snapshotTransforms()
	}

	if s.Env != nil {
		s.Env.Advance(dt)
	}
	IntegrateBodies(s, dt)
	if s.PathFinder != nil {
		s.PathFinder.Tick()
	}
	for _, sys := range s.systems {
		sys(s, dt)
	}
}

// ─────────────────────────── day/night ───────────────────────────

// Environment returns the scene's environment, resolved for this frame.
func (s *Scene) Environment() EnvironmentState {
	if s.Env == nil {
		return EnvironmentState{}
	}
	return s.Env.State()
}

// DayNight returns the scene's day/night cycle, or nil.
//
// It is nil whenever the environment does not have one: a custom
// EnvironmentSource, an interior with fixed lighting, or no environment at
// all. Callers that only want to set the time should use SetTimeOfDay, which
// handles the nil case.
func (s *Scene) DayNight() *DayNight {
	env, ok := s.Env.(*Environment)
	if !ok || env == nil {
		return nil
	}
	return env.Cycle
}

// TimeOfDay returns the current time of day (0=midnight, 0.5=noon), or 0 when
// the environment has no cycle.
func (s *Scene) TimeOfDay() float32 {
	if dn := s.DayNight(); dn != nil {
		return dn.TimeOfDay
	}
	return 0
}

// SetTimeOfDay sets the current time of day (0=midnight, 0.5=noon). Values
// outside [0,1) wrap. It does nothing when the environment has no cycle.
func (s *Scene) SetTimeOfDay(t float32) {
	if dn := s.DayNight(); dn != nil {
		dn.TimeOfDay = t - float32(math.Floor(float64(t)))
	}
}

// SetDayCycleSpeed sets the cycle speed in full cycles per second (e.g.
// 1.0/120 for a two-minute day). Zero freezes it. It does nothing when the
// environment has no cycle.
func (s *Scene) SetDayCycleSpeed(speed float32) {
	if dn := s.DayNight(); dn != nil {
		dn.Speed = speed
	}
}

// StarVisibility returns a 0–1 factor for night visibility (0=day, 1=night).
func (s *Scene) StarVisibility() float32 { return s.Environment().StarFade }

// ─────────────────────────── lighting ───────────────────────────

// SetPointLight sets the single shadow-casting point light's position, color,
// and falloff range.
func (s *Scene) SetPointLight(pos, color mgl32.Vec3, r float32) {
	s.pointPos = [3]float32{pos.X(), pos.Y(), pos.Z()}
	s.pointColor = [3]float32{color.X(), color.Y(), color.Z()}
	s.pointRange = r
}

// SetPointLights sets the unshadowed point lights. The renderer uses at most
// renderer.MaxPointLights of them.
func (s *Scene) SetPointLights(lights []PointLight) { s.pointLights = lights }

// PointLights returns the current unshadowed point lights.
func (s *Scene) PointLights() []PointLight { return s.pointLights }

// ─────────────────────────── spatial ───────────────────────────

// UpdateSpatialGrid rebuilds the moving-entity spatial grid from every entity
// with a Transform. Call once per tick before physics queries; without it,
// OverlapAABB and Raycast fall back to a linear scan of all colliders.
func (s *Scene) UpdateSpatialGrid() {
	if s.SpatialGrid == nil {
		s.SpatialGrid = NewSpatialGrid(0)
	}
	s.SpatialGrid.Update(s.world, s.C.Transform)
}

// RebuildStatics rebuilds the static spatial grid and the cached XZ position
// list from every entity tagged Static that also has a Collider. Call after
// loading or reloading world geometry.
func (s *Scene) RebuildStatics() {
	s.staticColliderXZ = s.staticColliderXZ[:0]
	if s.StaticGrid == nil {
		s.StaticGrid = NewSpatialGrid(0)
	}
	s.StaticGrid.Clear()

	ecs.Query3(s.C.Transform, s.C.Collider, s.C.Static,
		func(entity ecs.Entity, t *Transform, _ *Collider, _ *Static) {
			p := t.Position
			s.staticColliderXZ = append(s.staticColliderXZ, [2]float32{p.X(), p.Z()})
			s.StaticGrid.Insert(entity, p.X(), p.Z())
		})
}

// hasNearbyStaticCollider reports whether any static collider is within
// roughly 10 units of pos. Uses the static grid for an O(1) cell lookup,
// falling back to a linear scan of cached positions.
func (s *Scene) hasNearbyStaticCollider(pos mgl32.Vec3) bool {
	if s.StaticGrid != nil {
		return s.StaticGrid.HasAnyInRadius(pos.X(), pos.Z(), 10)
	}
	const r2 = float32(10 * 10)
	px, pz := pos.X(), pos.Z()
	for _, sp := range s.staticColliderXZ {
		dx := px - sp[0]
		dz := pz - sp[1]
		if dx*dx+dz*dz < r2 {
			return true
		}
	}
	return false
}

// ─────────────────────────── integration ───────────────────────────

// IntegrateBodies applies gravity and integrates velocity into position for
// every entity with a Transform and a Velocity. Entities that also have a
// Collider are additionally snapped to the ground — the terrain heightmap when
// there is one, a downward raycast otherwise.
//
// Bodies are resolved against the world, not against each other: this is not a
// rigid-body solver and does no body-to-body response. Entities with a
// CharacterController are skipped entirely, since MoveCharacter already does
// its own gravity, grounding, and swept collision — integrating them here too
// would apply gravity twice per tick.
func IntegrateBodies(s *Scene, dt float32) {
	gravity := s.Gravity
	ecs.Query2(s.C.Transform, s.C.Velocity,
		func(entity ecs.Entity, t *Transform, v *Velocity) {
			if s.C.CharacterController.Has(entity) {
				return
			}

			// Without a collider there is no footprint to stand on, so the
			// body just integrates — that is the right behavior for debris,
			// projectiles, and anything else that should not touch the floor.
			col, hasCollider := s.C.Collider.Get(entity)
			if !hasCollider {
				v.Vec[1] -= gravity * dt
				t.Position[0] += v.Vec[0] * dt
				t.Position[1] += v.Vec[1] * dt
				t.Position[2] += v.Vec[2] * dt
				return
			}

			halfHeight := col.HalfExtents.Y() * t.Scale.Y()

			// Fast path: a grounded, stationary body needs no work beyond
			// staying snapped to the terrain.
			if v.Vec[0] == 0 && v.Vec[2] == 0 && v.Vec[1] <= 0 && s.Terrain != nil {
				if groundY, ok := s.Terrain.HeightAt(t.Position.X(), t.Position.Z()); ok {
					snapY := groundY + halfHeight + 0.001
					if t.Position.Y() <= snapY+GroundEpsilon {
						v.Vec[1] = 0
						t.Position[1] = snapY
						return
					}
				}
			}

			v.Vec[1] -= gravity * dt

			// Ground detection — prefer the heightmap (O(1)) over a raycast.
			grounded := false
			if s.Terrain != nil {
				if groundY, ok := s.Terrain.HeightAt(t.Position.X(), t.Position.Z()); ok {
					snapY := groundY + halfHeight + 0.001
					if t.Position.Y() <= snapY+GroundEpsilon {
						if v.Vec[1] < 0 {
							v.Vec[1] = 0
							t.Position[1] = snapY
						}
						grounded = true
					}
				}
			}

			// Fall back to a full raycast with no heightmap, or outside it.
			if !grounded {
				maxDist := halfHeight + GroundEpsilon + 1.0
				if hit, ok := s.Raycast(t.Position, mgl32.Vec3{0, -1, 0}, maxDist, entity); ok {
					if hit.T <= halfHeight+GroundEpsilon && v.Vec[1] < 0 {
						v.Vec[1] = 0
						t.Position[1] = hit.Point.Y() + halfHeight + 0.001
					}
				}
			}

			t.Position[0] += v.Vec[0] * dt
			t.Position[1] += v.Vec[1] * dt
			t.Position[2] += v.Vec[2] * dt
		})
}
