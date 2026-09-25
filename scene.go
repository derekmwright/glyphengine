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
//
// Scatter as many as the scene wants. They are binned into a view-space
// froxel grid every frame (renderer/lightcluster), so a fragment evaluates
// the lights whose range reaches it rather than every light in the scene, and
// Range is what decides that -- a light with a range far larger than it
// visibly needs is a light in every cell it crosses. renderer.MaxLights is
// the ceiling on how many reach the GPU in one frame; past it the binner
// drops the ones furthest from lighting anything and says so in
// Engine.LightStats.
type PointLight struct {
	Pos   mgl32.Vec3
	Range float32
	Color mgl32.Vec3

	// Volumetric is how much of this light the air itself sends back toward
	// the eye -- Unreal's "volumetric scattering intensity". 0, the default,
	// means none: the light still lights surfaces exactly as it always did
	// and the in-scattering march skips it entirely, so a scene that does not
	// ask pays nothing and renders byte-identically.
	//
	// Above 0 the lamp gets a glow in the air around it, integrated along the
	// view ray through the froxel grid. What it scatters off is the scene's
	// FOG, so a scene with Fog nil or Density 0 sees nothing however high
	// this goes -- there is no medium. Scene.SetVolumetrics tunes the medium;
	// this is per light, so one lamp in a scene can have a visible halo and
	// the rest not.
	//
	// It is not shadowed: no local light in this engine casts a shadow, and
	// the glow is consistent with the light it belongs to. See
	// docs/agents/lights.md.
	Volumetric float32
}

// SpotLight describes an unshadowed spot light for the renderer: a point
// light narrowed to a cone. Dir is the direction the light points (need not
// be unit length -- the engine normalizes it).
//
// A zero Dir has no cone to aim and is treated as omnidirectional, i.e. an
// ordinary point light, rather than as an error. That is deliberate: it
// turns a caller's uninitialized or mistakenly-zeroed Dir into a visibly
// wrong light -- unexpectedly lighting everything around it -- rather than
// a silently missing one, which is easier to notice and debug.
//
// Inner and Outer are half-angles in radians measured from Dir: full
// intensity inside Inner, a smooth falloff to zero between Inner and Outer,
// and nothing beyond Outer. Inner greater than Outer is clamped down to
// Outer rather than rejected, producing a hard-edged cone instead of
// flooding the scene with an unbounded point light -- the opposite of what
// asking for a narrower inner cone means.
type SpotLight struct {
	Pos   mgl32.Vec3
	Dir   mgl32.Vec3
	Range float32
	Color mgl32.Vec3
	Inner float32
	Outer float32

	// Volumetric turns the cone into a visible beam: the air inside it
	// scatters the light back toward the eye instead of only the surface the
	// cone lands on. 0, the default, means none, and costs nothing. See
	// PointLight.Volumetric for what the number means and what it scatters
	// off -- the scene's fog, so a scene with no fog shows no beam.
	//
	// The beam is the same cone the surface lighting uses, evaluated in the
	// air rather than on a surface, so the shaft and the pool it lands in
	// agree by construction. It is not shadowed: a beam crossing a wall keeps
	// glowing on the far side, exactly as the light does.
	Volumetric float32
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

	// Integrator advances rigid bodies — everything with a Transform and a
	// Velocity, minus character controllers — once per Tick, in
	// IntegrateBodies's place. NewScene sets it to IntegrateBodies, and a
	// custom one is free to call IntegrateBodies itself for the bodies it
	// does not want to handle differently.
	//
	// nil does NOT mean "integrate nothing." Unlike Env, Terrain, PathFinder,
	// and SpatialGrid — where nil is a real, supported "off" — Tick treats a
	// nil Integrator as "use IntegrateBodies," and only that function decides
	// per-entity whether there is anything to do. The reason is the failure
	// mode: an empty sky or a missing pathfinder is obviously absent, but a
	// Scene built some way other than NewScene (a struct literal, an
	// embedding game type that forgot to call it) that quietly stopped
	// applying gravity would look like nothing was wrong until something
	// floated. A game that wants no built-in integration says so with a real
	// function value instead of leaving the zero value in place:
	//
	//	scene.Integrator = func(*glyphengine.Scene, float32) {}
	//
	// That is "off," deliberately, the same way an EnvironmentSource that
	// returns an empty EnvironmentState is deliberately no sky rather than an
	// oversight.
	Integrator func(s *Scene, dt float32)

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

	// Queries replaces the built-in spatial-grid collision queries — Raycast
	// and OverlapAABB — with a game's own broadphase. Nil keeps the built-in
	// implementation; NewScene leaves it nil, the same way it leaves Terrain,
	// PathFinder, and SpatialGrid nil for "use the built-in or fallback
	// behavior" rather than setting a default the way it does for Env and
	// Integrator. See QueryBackend for what a replacement must honour: the
	// #57 order contracts, the collision snapshot, and the concurrency
	// requirement.
	Queries QueryBackend

	// NavGrid and PathFinder are optional A* pathfinding over the terrain.
	NavGrid    *NavGrid
	PathFinder *PathFinder

	pointPos    [3]float32
	pointRange  float32
	pointColor  [3]float32
	pointLights []PointLight
	spotLights  []SpotLight

	// nightGrade is the scotopic grade; see SetNightGrade for why it is here
	// and not on EnvironmentState. NewScene sets it to DefaultNightGrade, and
	// that initialisation is the whole safety property -- a zero value here
	// would mean "no night shift" for anyone who never called the setter.
	nightGrade NightGrade

	// skyPalette is the atmosphere's six colours, here for the same reason
	// and with the same initialisation; see SetSkyPalette.
	skyPalette SkyPalette

	// volumetrics is the in-scattering medium, here for the same reason and
	// with the same initialisation again; see SetVolumetrics.
	volumetrics Volumetrics

	// staticColliderXZ caches XZ positions of static colliders for the
	// linear-scan fallback used when StaticGrid is nil.
	staticColliderXZ [][2]float32

	// staticGridCell records which cell RebuildStatics put each static
	// collider in, and staticGridFor is the StaticGrid it recorded them for.
	// Together they answer "is a StaticGrid walk going to produce this
	// entity?" exactly and in O(1), which is how the broad phase drops the
	// duplicate a Static entity in both grids used to produce without risking
	// dropping the entity itself — see Scene.staticWalkProduces and
	// Scene.eachBroadPhaseCandidate (#141). Written only by RebuildStatics, so
	// queries running on the parallel movement phase's goroutines only read it.
	staticGridCell map[ecs.Entity]cellKey
	staticGridFor  *SpatialGrid

	// Frozen world-space AABBs for the parallel movement phase. When active,
	// OverlapAABB and Raycast read collider geometry from this snapshot instead
	// of live Transforms, so movement goroutines never read a neighbor's
	// position while another goroutine writes it. Built and cleared by
	// MoveCharactersParallel, through the same freeze FrozenQueries gives a
	// game; the map is handed back in each tick so that freeze does not
	// reallocate it.
	collisionAABBs       map[ecs.Entity]AABB
	useCollisionSnapshot bool

	systems []System
}

// NewScene creates a Scene with an empty ECS world and default state.
func NewScene() *Scene {
	w := ecs.NewWorld()
	return &Scene{
		world:       w,
		C:           NewComponents(w),
		Env:         DefaultEnvironment(),
		Gravity:     DefaultGravity,
		Integrator:  IntegrateBodies,
		nightGrade:  DefaultNightGrade(),
		skyPalette:  DefaultSkyPalette(),
		volumetrics: DefaultVolumetrics(),
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
	// nil means "use IntegrateBodies," not "skip integration" — see the
	// Integrator field comment for why a Scene built without NewScene must
	// not silently lose physics.
	if s.Integrator != nil {
		s.Integrator(s, dt)
	} else {
		IntegrateBodies(s, dt)
	}
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

// SetPointLights sets the unshadowed point lights. Combined with the spot
// lights (see SetSpotLights), at most renderer.MaxLights of them reach the
// GPU in a frame; the rest are dropped nearest-surface-last and counted in
// Engine.LightStats. renderer.MaxPointLights is an alias for the same number
// and no longer the 32 it once was.
//
// The slice is not copied. Reusing one buffer across frames is the way to
// drive hundreds of lights without allocating for them.
func (s *Scene) SetPointLights(lights []PointLight) { s.pointLights = lights }

// PointLights returns the current unshadowed point lights.
func (s *Scene) PointLights() []PointLight { return s.pointLights }

// SetSpotLights sets the unshadowed spot lights. Combined with the point
// lights, at most renderer.MaxLights of them reach the GPU in a frame, points
// first then spots -- see Engine.gatherLights. Not copied, like
// SetPointLights.
func (s *Scene) SetSpotLights(lights []SpotLight) { s.spotLights = lights }

// SpotLights returns the current unshadowed spot lights.
func (s *Scene) SpotLights() []SpotLight { return s.spotLights }

// NightGrade is the colour grade lit surfaces take on as daylight goes: the
// scotopic shift that stops a night scene being a dimmed day scene. Strength
// is how far a surface with no lamp on it goes toward that grade at full
// night, and Tint is what its luminance is multiplied by to get there —
// blue-biased, because rods are.
//
// These are look decisions, not physics. The defaults are what this engine
// has always used and are documented in docs/agents/day-night.md; a game with
// a different artistic direction is meant to change them rather than vendor
// the lighting chain to get at two constants.
type NightGrade struct {
	// Strength 0 turns the shift off completely, leaving night dim but
	// otherwise ungraded.
	Strength float32
	Tint     mgl32.Vec3
}

// DefaultNightGrade is the grade every scene had before it was tunable.
func DefaultNightGrade() NightGrade {
	return NightGrade{Strength: 0.8, Tint: mgl32.Vec3{0.72, 0.86, 1.30}}
}

// SetNightGrade sets the scotopic grade for this scene.
//
// It lives on Scene, initialised by NewScene, rather than on EnvironmentState
// beside fog and ambient — which is where it otherwise belongs. The reason is
// upgrades. EnvironmentState is produced wholesale by EnvironmentSource.State,
// so a game that has replaced the environment model returns a struct it wrote
// before this field existed, and the field arrives as its zero value: Strength
// 0, which means no night shift at all. That game's nights would change on a
// dependency bump with nobody choosing it, and the only ways out are a
// sentinel (0 meaning "default", so nothing could ever mean "off") or a
// separate "did you set it" flag, both of which are the silent traps the
// capability docs exist to warn about. A Scene field initialised at
// construction, the way Gravity is, cannot be zeroed by a source that does not
// know about it.
//
// A source that legitimately wants the grade to move — moon phase, a storm —
// still can: call this from Update, the same place SetPointLights is called
// from. Varying it per frame never required it to be inside the environment.
func (s *Scene) SetNightGrade(g NightGrade) { s.nightGrade = g }

// NightGrade returns the scene's scotopic grade.
func (s *Scene) NightGrade() NightGrade { return s.nightGrade }

// SkyPalette is the six colours the atmosphere blends between: zenith and
// horizon for day, for twilight and for night. `shaders/atmosphere.inc` mixes
// night toward day on the daylight curve and then toward twilight on the
// twilight curve, so these are endpoints rather than a gradient to sample.
//
// It is the whole atmosphere's palette, not the dome's. Distant geometry fades
// toward the same horizon colour and water reflects the dome, so changing only
// the sky would give a violet sky over a landscape still hazing into
// Earth-blue — which is exactly what replacing `sky.frag` through `WithShaders`
// used to do, and the reason these are data now.
//
// What it does not cover: Rayleigh-versus-Mie behaviour, a different scattering
// model, two suns, the cloud and star colours, and the sun's own glow ember are
// all still shader work, and `WithShaders` is the right escape hatch for them.
// This is the case that is pure palette, which is most of what "another planet"
// means in practice.
type SkyPalette struct {
	ZenithDay, HorizonDay           mgl32.Vec3
	ZenithTwilight, HorizonTwilight mgl32.Vec3
	ZenithNight, HorizonNight       mgl32.Vec3
}

// DefaultSkyPalette is Earth's sky, and is exactly the constants that used to
// be compiled into atmSkyPalette.
//
// The horizon is pale because that is what looking through more atmosphere
// does, but not white: distant geometry fades into this colour, so a
// washed-out horizon washes out the whole landscape with it. The night
// endpoints are deliberately very dark for the same reason — they are what the
// dome, the fog and the water all reach at midnight, so lifting them to make
// the sky legible washes out the entire landscape. Brighten the moon instead.
func DefaultSkyPalette() SkyPalette {
	return SkyPalette{
		ZenithDay:  mgl32.Vec3{0.13, 0.30, 0.78},
		HorizonDay: mgl32.Vec3{0.52, 0.70, 0.93},

		ZenithTwilight:  mgl32.Vec3{0.055, 0.085, 0.26},
		HorizonTwilight: mgl32.Vec3{0.88, 0.42, 0.22},

		ZenithNight:  mgl32.Vec3{0.0014, 0.0017, 0.0060},
		HorizonNight: mgl32.Vec3{0.0034, 0.0050, 0.0130},
	}
}

// SetSkyPalette sets the atmosphere's colours for this scene.
//
// It lives on Scene, initialised by NewScene, rather than on Sky or
// EnvironmentState — which is where the issue that asked for it proposed
// putting it, and where it reads more naturally beside FixedSunElevation and
// fog. Two reasons, and the first is the one SetNightGrade already records:
// EnvironmentState is produced wholesale by EnvironmentSource.State, so a game
// that has replaced the environment model returns a struct written before this
// field existed and the field arrives as its zero value. For the night grade
// that meant a flat night; here it means six black colours, so that game's sky
// goes black on a dependency bump with nobody choosing it.
//
// A sentinel would be defensible here where it was not for the grade — all-zero
// is a palette nobody wants, so reading it as "engine default" costs nothing
// expressible. It is still not the shape chosen, because it only covers the
// all-zero case: a source that sets ZenithDay and leaves the other five at
// their zero value gets five black endpoints and no warning, which is the same
// silent trap one step along. A Scene field initialised at construction cannot
// be zeroed by a source that does not know about it at all.
//
// The second reason is that the palette is not only the sky's. `applyFog`
// blends distant geometry toward the horizon colour whether or not a dome is
// drawn, so a scene with Sky nil still uses this — which makes Sky the wrong
// home for it independently of upgrades.
//
// A source that legitimately wants the palette to move — a storm, an eclipse,
// a second moon — still can: call this from Update, where SetPointLights is
// called from.
func (s *Scene) SetSkyPalette(p SkyPalette) { s.skyPalette = p }

// SkyPalette returns the scene's atmosphere palette.
func (s *Scene) SkyPalette() SkyPalette { return s.skyPalette }

// Volumetrics is the scattering medium a light's beam is made of: how
// forward-scattering the air is, and how many steps the per-pixel march
// spends crossing it.
//
// It is deliberately NOT the medium's density. That is the scene's fog
// (Fog.Density and the height profile beside it) and it stays there, because
// the air that hazes the hills is the air a lamp lights -- one medium, one
// place to tune it. A scene with no fog therefore sees no beams, which is the
// right answer rather than a missing feature: there is nothing in the air to
// light.
type Volumetrics struct {
	// Anisotropy is the Henyey-Greenstein g. 0 scatters equally in every
	// direction; positive scatters forward, so a beam coming toward the eye
	// is brighter than the same beam crossing it. Clamped to (-1, 1).
	Anisotropy float32

	// Steps is how many samples each pixel's march takes: the cost knob and
	// the banding knob at once. 0 turns the march off for the whole scene,
	// whatever the lights ask for. Capped at renderer.MaxVolumetricSteps.
	Steps int
}

// DefaultVolumetrics is the medium a scene gets by saying nothing.
//
// The numbers are repeated from renderer.DefaultVolumetrics rather than read
// from it, because Scene has no renderer dependency -- that is what lets a
// headless tool or test drive one -- and the same is already true of
// DefaultNightGrade and DefaultSkyPalette above. The measurements behind them
// are on the renderer side, next to the shader they configure;
// TestDefaultsMatchTheRenderers is what keeps the two copies from drifting,
// which nothing did for the grade or the palette until now.
func DefaultVolumetrics() Volumetrics {
	return Volumetrics{Anisotropy: 0.4, Steps: 32}
}

// SetVolumetrics sets the scattering medium for this scene.
//
// On Scene, initialised by NewScene, for exactly the reasons SetSkyPalette
// records: EnvironmentState is produced wholesale by EnvironmentSource.State,
// so a game that has replaced the environment model returns a struct written
// before this field existed and it would arrive zero -- and a zero Steps
// marches nothing, so every beam in that game would disappear on a dependency
// bump with nobody choosing it.
//
// Density is the exception and stays on Fog, because it is not a volumetrics
// setting: it is the fog, and a game that animates fog rolling in wants the
// beams to thicken with it without touching this at all.
func (s *Scene) SetVolumetrics(v Volumetrics) { s.volumetrics = v }

// Volumetrics returns the scene's scattering medium.
func (s *Scene) Volumetrics() Volumetrics { return s.volumetrics }

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

	// Reused rather than replaced, the way the collision snapshot's map is: a
	// level reload calls this for every static it has.
	if s.staticGridCell == nil {
		s.staticGridCell = make(map[ecs.Entity]cellKey, 256)
	} else {
		clear(s.staticGridCell)
	}
	s.staticGridFor = s.StaticGrid

	ecs.Query3(s.C.Transform, s.C.Collider, s.C.Static,
		func(entity ecs.Entity, t *Transform, _ *Collider, _ *Static) {
			p := t.Position
			s.staticColliderXZ = append(s.staticColliderXZ, [2]float32{p.X(), p.Z()})
			s.StaticGrid.Insert(entity, p.X(), p.Z())
			s.staticGridCell[entity] = s.StaticGrid.cellFor(p.X(), p.Z())
		})
}

// staticWalkProduces reports whether a StaticGrid walk over (x, z, radius)
// hands back entity — the question Scene.eachBroadPhaseCandidate asks before
// dropping that entity from the moving grid's walk.
//
// Every condition is checked against what the static grid actually holds
// rather than inferred from the Static tag, because the answer decides whether
// a collider is offered to a query at all. A Static entity spawned since the
// last RebuildStatics is not in staticGridCell; a StaticGrid the game swapped
// out after RebuildStatics is not staticGridFor; a static whose cell this
// query does not reach fails walkReaches. Each of those returns false, which
// leaves the entity to the moving grid's walk — at worst the duplicate every
// query paid before #141 — where a wrong true is a missing collider.
func (s *Scene) staticWalkProduces(entity ecs.Entity, x, z, radius float32) bool {
	if s.StaticGrid == nil || s.StaticGrid != s.staticGridFor {
		return false
	}
	key, ok := s.staticGridCell[entity]
	if !ok {
		return false
	}
	return s.StaticGrid.walkReaches(key, x, z, radius)
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
