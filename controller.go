package glyphengine

import (
	"math"
	"runtime"
	"sync"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/ecs"
)

// GroundEpsilon is the vertical slack, in world units, within which a body is
// still considered to be standing on a surface.
const GroundEpsilon = float32(0.05)

// CharacterController marks an entity as driven by MoveCharacter and holds its
// movement tuning. Entities with this component are skipped by IntegrateBodies
// — the controller does its own gravity, grounding, and swept collision.
type CharacterController struct {
	WalkSpeed float32 // world units per second
	RunSpeed  float32 // world units per second while MoveIntent.Sprint is set
	JumpSpeed float32 // upward velocity applied on a grounded jump
	TurnRate  float32 // radians per second applied by MoveIntent.Turn

	// Grounded reports whether the character was standing on something after
	// the last MoveCharacter call. Read-only for callers; games use it to
	// gate jump input and to select airborne animations.
	Grounded bool
}

// NewCharacterController returns a controller with the engine's default
// tuning: 4/8 units per second walk/run, a 7 unit-per-second jump, and a
// ~200°/s keyboard turn rate.
func NewCharacterController() CharacterController {
	return CharacterController{
		WalkSpeed: 4.0,
		RunSpeed:  8.0,
		JumpSpeed: 7.0,
		TurnRate:  3.5,
	}
}

// MoveIntent is one frame of movement input for a character. It is
// deliberately device- and network-agnostic: a keyboard, a gamepad, an AI
// controller, or a decoded network packet all produce the same struct.
type MoveIntent struct {
	// Forward and Right are movement on the XZ plane in [-1, 1], relative to
	// Yaw. Only the sign is used; the magnitude does not scale speed.
	Forward float32
	Right   float32

	// Turn rotates the character in place at CharacterController.TurnRate,
	// in [-1, 1], positive turning right. Non-zero Turn also makes Forward and
	// Right relative to the character's new facing rather than to Yaw, so
	// keyboard turning and camera-relative movement compose.
	Turn float32

	// Yaw is the reference heading in radians that Forward and Right are
	// relative to — normally the camera yaw.
	Yaw float32

	// Sprint selects RunSpeed instead of WalkSpeed.
	Sprint bool

	// Jump applies JumpSpeed when the character is grounded.
	Jump bool

	// SpeedScale multiplies the final horizontal speed, for game-side effects
	// like haste buffs, snares, or roots. Zero is treated as 1.
	SpeedScale float32
}

// MoveCharacter advances one character entity by dt seconds: turning, gravity,
// ground detection, jumping, and axis-separated slide collision.
//
// The entity must have Transform, Collider, Velocity, and CharacterController.
// Missing any of them makes this a no-op.
func (s *Scene) MoveCharacter(entity ecs.Entity, intent MoveIntent, dt float32) {
	t, ok := s.C.Transform.Get(entity)
	if !ok {
		return
	}
	col, ok := s.C.Collider.Get(entity)
	if !ok {
		return
	}
	vel, ok := s.C.Velocity.Get(entity)
	if !ok {
		return
	}
	cc, ok := s.C.CharacterController.Get(entity)
	if !ok {
		return
	}

	// Keyboard turn.
	if intent.Turn != 0 {
		t.Rotation[1] -= intent.Turn * cc.TurnRate * dt
	}

	// Movement reference yaw: while turning, use the updated character facing
	// so forward/back move where the character now points.
	refYaw := intent.Yaw
	if intent.Turn != 0 {
		refYaw = t.Rotation[1]
	}

	forward := mgl32.Vec3{-sin32(refYaw), 0, -cos32(refYaw)}
	right := mgl32.Vec3{cos32(refYaw), 0, -sin32(refYaw)}

	var moveDir mgl32.Vec3
	if intent.Forward > 0 {
		moveDir = moveDir.Add(forward)
	}
	if intent.Forward < 0 {
		moveDir = moveDir.Sub(forward)
	}
	if intent.Right > 0 {
		moveDir = moveDir.Add(right)
	}
	if intent.Right < 0 {
		moveDir = moveDir.Sub(right)
	}
	if moveDir.Len() > 0 {
		moveDir = moveDir.Normalize()
		// Face the movement direction when moving forward, so observers see the
		// correct heading. Backpedaling keeps the current facing to avoid an
		// oscillation that flips the character 180° every tick. Pure strafing
		// faces the reference yaw.
		if intent.Forward > 0 {
			t.Rotation[1] = float32(math.Atan2(float64(-moveDir.X()), float64(-moveDir.Z())))
		} else if intent.Right != 0 {
			t.Rotation[1] = intent.Yaw
		}
	}

	speed := cc.WalkSpeed
	if intent.Sprint {
		speed = cc.RunSpeed
	}
	if intent.SpeedScale != 0 {
		speed *= intent.SpeedScale
	}

	vel.Vec[0] = moveDir.X() * speed
	vel.Vec[2] = moveDir.Z() * speed

	vel.Vec[1] -= s.Gravity * dt

	// Ground detection — heightmap first (O(1)), then a full raycast only when
	// near static geometry or when the heightmap misses.
	halfHeight := col.HalfExtents.Y() * t.Scale.Y()
	maxDist := halfHeight + GroundEpsilon + 1.0

	grounded := false

	if s.Terrain != nil {
		if groundY, ok := s.Terrain.HeightAt(t.Position.X(), t.Position.Z()); ok {
			snapY := groundY + halfHeight + 0.001
			if t.Position.Y() <= snapY+GroundEpsilon {
				if vel.Vec[1] < 0 {
					vel.Vec[1] = 0
					t.Position[1] = snapY
				}
				grounded = true
			}
		}
	}

	// Slow path: a full raycast against world geometry (hull collision etc.).
	// Needed when not grounded on terrain, or when a static object's hit is
	// higher than the terrain underneath it.
	if !grounded || s.hasNearbyStaticCollider(t.Position) {
		if hit, ok := s.Raycast(t.Position, mgl32.Vec3{0, -1, 0}, maxDist, entity); ok {
			threshold := halfHeight + GroundEpsilon

			// A steep hull face is a wall, not a floor — standing on it would
			// let characters walk up vertical surfaces.
			walkable := true
			if hit.Entity != 0 && s.C.ConvexHullCollider.Has(hit.Entity) {
				walkable = hit.Normal.Y() > 0.5
			}

			if hit.T <= threshold && walkable {
				grounded = true
				if vel.Vec[1] < 0 {
					vel.Vec[1] = 0
					t.Position[1] = hit.Point.Y() + halfHeight + 0.001
				}
			} else if hit.Entity != 0 && hit.T <= maxDist && walkable {
				grounded = true
			}
		}
	}

	if grounded && intent.Jump {
		vel.Vec[1] = cc.JumpSpeed
		grounded = false
	}
	cc.Grounded = grounded

	// Move and collide, one axis at a time so blocked motion slides along the
	// remaining axes instead of stopping dead.
	pos := t.Position
	for axis := 0; axis < 3; axis++ {
		if vel.Vec[axis] == 0 {
			continue
		}
		candidate := pos
		candidate[axis] += vel.Vec[axis] * dt

		candidateT := &Transform{Position: candidate, Scale: t.Scale}
		box := WorldAABB(candidateT, col)

		blocked := false
		for _, ov := range s.OverlapAABB(box, entity) {
			// For hull entities, run a GJK narrow-phase against a feet-only
			// test box so the character collides with the actual hull shape
			// rather than its oversized AABB. Applies on every axis.
			if hc, ok := s.C.ConvexHullCollider.Get(ov.Entity); ok {
				ovT, _ := s.C.Transform.Get(ov.Entity)
				feetBox := AABB{
					Min: mgl32.Vec3{box.Min.X(), box.Min.Y(), box.Min.Z()},
					Max: mgl32.Vec3{box.Max.X(), box.Min.Y() + 0.5, box.Max.Z()},
				}
				if !GJKOverlapAABB(hc, ovT, feetBox) {
					continue
				}
			}
			blocked = true
			break
		}
		if blocked {
			vel.Vec[axis] = 0
		} else {
			pos[axis] = candidate[axis]
		}
	}

	t.Position = pos
}

// CharacterState is everything MoveCharacter reads and writes for one
// character — and deliberately nothing else.
//
// It exists for client-side prediction against an authoritative server: the
// client rewinds a character to the last state the server confirmed, then
// replays the inputs the server has not acknowledged yet. That works because
// MoveCharacter is a pure function of (state, intent, dt) plus the static
// world, so replaying the same intents from the same state reproduces the same
// result exactly.
//
// This is not a save format and not a general scene snapshot. It captures one
// entity's movement state, ignores every other component, and will silently
// fail to preserve anything a game layers on top.
type CharacterState struct {
	Position mgl32.Vec3
	Rotation mgl32.Vec3
	Velocity mgl32.Vec3
	Grounded bool
}

// SnapshotCharacter captures a character's movement state. It reports false if
// the entity is not a character controller.
//
// Pair the result with Scene.TickCount to know which tick it describes —
// that number, not wall-clock time, is what a client and server agree on.
func (s *Scene) SnapshotCharacter(entity ecs.Entity) (CharacterState, bool) {
	t, ok := s.C.Transform.Get(entity)
	if !ok {
		return CharacterState{}, false
	}
	vel, ok := s.C.Velocity.Get(entity)
	if !ok {
		return CharacterState{}, false
	}
	cc, ok := s.C.CharacterController.Get(entity)
	if !ok {
		return CharacterState{}, false
	}
	return CharacterState{
		Position: t.Position,
		Rotation: t.Rotation,
		Velocity: vel.Vec,
		Grounded: cc.Grounded,
	}, true
}

// RestoreCharacter writes a snapshot back onto a character, rewinding it to
// that exact movement state. It reports false if the entity is not a character
// controller.
//
// Restoring does not move the character through the world — it teleports it.
// Nothing is swept, so a state captured somewhere the character can no longer
// legally stand will leave it intersecting geometry; Unstick recovers from
// that if a game needs to be defensive about it.
func (s *Scene) RestoreCharacter(entity ecs.Entity, st CharacterState) bool {
	t, ok := s.C.Transform.Get(entity)
	if !ok {
		return false
	}
	vel, ok := s.C.Velocity.Get(entity)
	if !ok {
		return false
	}
	cc, ok := s.C.CharacterController.Get(entity)
	if !ok {
		return false
	}
	t.Position = st.Position
	t.Rotation = st.Rotation
	vel.Vec = st.Velocity
	cc.Grounded = st.Grounded

	// A restore is a teleport, not motion. Without this the next frame would
	// draw the character sliding from where prediction had put it to where the
	// server says it is — every correction would smear.
	s.ClearInterpolation(entity)
	return true
}

// MoveBatchEntry pairs a character entity with one frame of input.
type MoveBatchEntry struct {
	Entity ecs.Entity
	Intent MoveIntent
}

// MoveCharactersParallel moves many characters concurrently against a frozen
// collision snapshot. It exists for servers stepping hundreds of characters per
// tick; a single-player game should just call MoveCharacter in a loop.
//
// The snapshot is what makes this safe: OverlapAABB and Raycast read every
// collider's world AABB as it was before the phase began, so no goroutine can
// observe a neighbor mid-write. Two invariants come with it:
//
//   - Convex-hull narrow-phase reads live Transforms while the AABB broad
//     phase reads the snapshot. That is only sound because hull entities are
//     Static and therefore never move during the phase. Attaching a hull to a
//     moving entity breaks it.
//   - Work is partitioned by entity hash, not by batch position. A caller that
//     delivers two intents for the SAME entity in one batch needs them applied
//     sequentially in order; a positional split would race on that entity's own
//     Transform and silently drop a frame of movement.
func (s *Scene) MoveCharactersParallel(batch []MoveBatchEntry, dt float32) {
	if len(batch) == 0 {
		return
	}
	if len(batch) == 1 {
		s.MoveCharacter(batch[0].Entity, batch[0].Intent, dt)
		return
	}

	s.buildCollisionSnapshot()
	s.world.EnableLocking()

	workers := runtime.GOMAXPROCS(0)
	if workers > len(batch) {
		workers = len(batch)
	}

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for j := range batch {
				if int(batch[j].Entity%ecs.Entity(workers)) == w {
					s.MoveCharacter(batch[j].Entity, batch[j].Intent, dt)
				}
			}
		}(w)
	}
	wg.Wait()

	s.world.DisableLocking()
	s.clearCollisionSnapshot()
}
