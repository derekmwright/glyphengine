package glyphengine

import (
	"math"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/ecs"
	"github.com/derekmwright/glyphengine/input"
)

const (
	cameraMinDistance    = 0.0
	cameraMaxDistance    = 20.0
	cameraFirstPersonTh  = 0.5 // distance at which we snap to first-person
	cameraZoomSpeed      = 1.0
	cameraSpringInSpeed  = 80.0 // fast pull-in (zoom-in + collision)
	cameraSpringOutSpeed = 5.0  // slow ease-out for smooth return
)

// Camera is a third-person orbit camera that follows a target position.
type Camera struct {
	Target     mgl32.Vec3 // position to orbit around (set from player)
	LookOffset float32    // vertical offset above target for look-at point (pushes character down on screen)
	Yaw        float32    // radians, 0 = looking along -Z
	Pitch      float32    // radians, positive = looking up
	Distance   float32    // orbit distance from target

	LookSensitivity float32 // radians per pixel of mouse delta
	EditorMode      bool    // when true, only right-drag orbits (left-click reserved for editor)

	// StickLookRate is radians per second at full thumbstick deflection, and
	// ZoomRate is orbit units per second at full trigger or bumper deflection.
	// Both are used by LookStick and ZoomBy.
	//
	// Rates, where LookSensitivity is a ratio: a mouse reports displacement since
	// last frame, a stick reports only how far it is pushed. Scaling a stick like
	// a mouse gives a camera that orbits faster the higher the frame rate.
	StickLookRate float32
	ZoomRate      float32

	effectiveDistance float32 // clamped by collision; used for eye position
	dragDistance      float32 // cumulative mouse movement during left-click
}

// NewCamera creates an orbit camera at the given distance behind the target.
func NewCamera(distance float32) *Camera {
	return &Camera{
		Distance:          distance,
		LookOffset:        0.75, // look above player, placing character in lower screen
		Pitch:             0.3,  // slightly above horizon
		LookSensitivity:   0.003,
		StickLookRate:     2.2,
		ZoomRate:          8.0,
		effectiveDistance: distance,
	}
}

// LookStick applies one frame of thumbstick orbit, in the convention
// Bindings.Direction returns: +x orbits right, +y raises the camera.
//
// Call it alongside Update, not instead of it, so mouse drag and stick both work.
// dt is required because deflection is a rate; see StickLookRate.
func (c *Camera) LookStick(x, y, dt float32) {
	if x == 0 && y == 0 {
		return
	}
	c.Yaw -= x * c.StickLookRate * dt
	// Positive pitch looks up here — the opposite of FPCamera, which the type's
	// own field comment records — so a stick pushed up raises the camera.
	c.Pitch += y * c.StickLookRate * dt
	c.clampPitch()
}

// ZoomBy changes the orbit distance at ZoomRate, for a trigger or bumper pair.
// Positive pulls the camera in.
//
// Separate from the scroll wheel Update consumes, because a wheel delivers
// discrete notches already scaled per frame while a held button is a rate.
func (c *Camera) ZoomBy(amount, dt float32) {
	if amount == 0 {
		return
	}
	c.Distance -= amount * c.ZoomRate * dt
	c.clampDistance()
}

func (c *Camera) clampPitch() {
	const limit = math.Pi/2 - 0.01
	if c.Pitch > limit {
		c.Pitch = limit
	}
	if c.Pitch < -limit {
		c.Pitch = -limit
	}
}

func (c *Camera) clampDistance() {
	if c.Distance < cameraMinDistance {
		c.Distance = cameraMinDistance
	}
	if c.Distance > cameraMaxDistance {
		c.Distance = cameraMaxDistance
	}
}

// Update processes mouse look (left- or right-click drag) and scroll zoom for one tick.
// Left-drag orbits and turns the character; right-drag orbits without turning.
func (c *Camera) Update(inp *input.Input) {
	leftOrbit := !c.EditorMode // in editor mode, left-click is reserved for picking/dragging

	// Lock cursor while orbit button is held.
	if (leftOrbit && inp.MousePressed(input.MouseButtonLeft)) || inp.MousePressed(input.MouseButtonRight) {
		inp.SetCursorLocked(true)
	}
	if inp.MouseReleased(input.MouseButtonLeft) && !inp.MouseDown(input.MouseButtonRight) {
		inp.SetCursorLocked(false)
	}
	if inp.MouseReleased(input.MouseButtonRight) && !inp.MouseDown(input.MouseButtonLeft) {
		inp.SetCursorLocked(false)
	}

	// Apply mouse delta from orbit button(s) to camera orbit.
	leftDrag := leftOrbit && inp.MouseDown(input.MouseButtonLeft)
	dragging := leftDrag || inp.MouseDown(input.MouseButtonRight)
	if dragging {
		dx, dy := inp.MouseDelta()
		c.Yaw -= float32(dx) * c.LookSensitivity
		c.Pitch += float32(dy) * c.LookSensitivity
		c.clampPitch()

		// Track cumulative drag distance for click-vs-drag detection.
		if dx != 0 || dy != 0 {
			c.dragDistance += float32(math.Abs(dx) + math.Abs(dy))
		}
	}
	if inp.MousePressed(input.MouseButtonLeft) {
		c.dragDistance = 0
	}

	// Scroll wheel adjusts distance (consume so fixed-timestep doesn't double-apply).
	_, sy := inp.ConsumeScroll()
	c.Distance -= float32(sy) * cameraZoomSpeed
	c.clampDistance()
}

// Raycaster is the world-query capability the camera needs to keep itself out
// of walls. Scene implements it. Taking an interface rather than *Scene or
// *Engine keeps the camera usable in tests and against a game's own broadphase.
type Raycaster interface {
	Raycast(origin, dir mgl32.Vec3, maxDist float32, exclude ecs.Entity) (RayHit, bool)
}

// ResolveCollision raycasts from the target toward the ideal eye position and
// pulls the camera forward if geometry blocks the line of sight. dt is the
// frame delta in seconds; the pull-in is fast and the ease-out is slow, so the
// camera snaps away from walls but drifts back smoothly.
func (c *Camera) ResolveCollision(rc Raycaster, exclude ecs.Entity, dt float32) {
	// In first-person mode, skip collision — eye is inside the player.
	if c.Distance < cameraFirstPersonTh {
		c.effectiveDistance = 0
		return
	}

	dir := mgl32.Vec3{
		sin32(c.Yaw) * cos32(c.Pitch),
		sin32(c.Pitch),
		cos32(c.Yaw) * cos32(c.Pitch),
	}

	oy := c.orbitCenterY(c.Distance)
	origin := mgl32.Vec3{c.Target.X(), oy, c.Target.Z()}

	var targetDist float32
	hit, ok := rc.Raycast(origin, dir, c.Distance, exclude)
	if ok {
		targetDist = hit.T - 0.2
	} else {
		targetDist = c.Distance
	}
	if targetDist < 0.3 {
		targetDist = 0.3
	}
	if targetDist > c.Distance {
		targetDist = c.Distance
	}

	speed := float64(cameraSpringOutSpeed)
	if targetDist < c.effectiveDistance {
		speed = cameraSpringInSpeed
	}
	alpha := float32(1 - math.Exp(-speed*float64(dt)))
	c.effectiveDistance += (targetDist - c.effectiveDistance) * alpha

	if c.effectiveDistance < 0.3 {
		c.effectiveDistance = 0.3
	}
}

// FirstPerson returns true when the camera is in first-person mode.
func (c *Camera) FirstPerson() bool { return c.effectiveDistance < 0.01 }

// ViewVectors returns the eye, center, and up vectors for SetCamera.
func (c *Camera) ViewVectors() (eye, center, up mgl32.Vec3) {
	up = mgl32.Vec3{0, 1, 0}

	if c.FirstPerson() {
		// Eye at head height, looking along yaw/pitch.
		eye = mgl32.Vec3{
			c.Target.X(),
			c.Target.Y() + c.LookOffset,
			c.Target.Z(),
		}
		// Forward direction from yaw/pitch.
		// Negate pitch: in third-person positive pitch = eye above target (looking down),
		// so in first-person it must also mean looking down.
		fwd := mgl32.Vec3{
			-sin32(c.Yaw) * cos32(c.Pitch),
			-sin32(c.Pitch),
			-cos32(c.Yaw) * cos32(c.Pitch),
		}
		center = eye.Add(fwd)
		return
	}

	// Third-person: eye offset in spherical coordinates from target.
	// Blend orbit center toward head height as distance decreases for smooth
	// transition into first-person (avoids camera tilting upward at close range).
	d := c.effectiveDistance
	oy := c.orbitCenterY(d)
	eye = mgl32.Vec3{
		c.Target.X() + d*sin32(c.Yaw)*cos32(c.Pitch),
		oy + d*sin32(c.Pitch),
		c.Target.Z() + d*cos32(c.Yaw)*cos32(c.Pitch),
	}
	center = mgl32.Vec3{c.Target.X(), c.Target.Y() + c.LookOffset, c.Target.Z()}
	return
}

// GetYaw returns the camera yaw so player movement can be relative to camera facing.
func (c *Camera) GetYaw() float32 {
	return c.Yaw
}

// WasClick returns true if the left mouse button was released without significant
// dragging, distinguishing a target-pick click from a camera-orbit drag.
func (c *Camera) WasClick(inp *input.Input) bool {
	return inp.MouseReleased(input.MouseButtonLeft) && c.dragDistance < 5
}

func (c *Camera) orbitCenterY(dist float32) float32 {
	blend := 1.0 - clamp32(dist/5.0, 0, 1)
	return c.Target.Y() + blend*c.LookOffset
}

func sin32(x float32) float32 { return float32(math.Sin(float64(x))) }
func cos32(x float32) float32 { return float32(math.Cos(float64(x))) }

func clamp32(x, lo, hi float32) float32 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
