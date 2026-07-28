package glyphengine

import (
	"math"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/input"
)

// FPCamera is a first-person camera: the eye sits at the target position plus
// EyeHeight and looks along Yaw/Pitch. It shares the orbit Camera's angle
// convention — yaw 0 looks down -Z, positive pitch looks down — so
// MoveIntent.Yaw can be fed from either camera unchanged.
type FPCamera struct {
	// Target is the position the eye is anchored to, normally the character's
	// Transform.Position. Set it with Follow.
	Target mgl32.Vec3

	// EyeHeight raises the eye above Target — roughly the character's eye
	// level above its origin.
	EyeHeight float32

	Yaw   float32 // radians, 0 = looking along -Z
	Pitch float32 // radians, positive = looking down

	// LookSensitivity is radians of rotation per pixel of mouse movement.
	LookSensitivity float32

	// InvertY flips vertical look.
	InvertY bool

	// MaxPitch clamps how far up and down the camera can look, in radians.
	// Zero means just under a right angle, which avoids gimbal flip at
	// straight up and straight down.
	MaxPitch float32
}

// NewFPCamera returns a first-person camera with sensible defaults: a 1.6 unit
// eye height and the same look sensitivity as the orbit camera.
func NewFPCamera() *FPCamera {
	return &FPCamera{
		EyeHeight:       1.6,
		LookSensitivity: 0.003,
	}
}

// Update applies mouse movement to yaw and pitch. It only looks when the
// cursor is locked, so a game can release the cursor for menus by calling
// inp.SetCursorLocked(false) and the camera holds still.
func (c *FPCamera) Update(inp *input.Input) {
	if !inp.CursorLocked() {
		return
	}

	dx, dy := inp.MouseDelta()
	if c.InvertY {
		dy = -dy
	}
	c.Yaw -= float32(dx) * c.LookSensitivity
	c.Pitch += float32(dy) * c.LookSensitivity

	limit := c.MaxPitch
	if limit <= 0 {
		limit = math.Pi/2 - 0.01
	}
	if c.Pitch > limit {
		c.Pitch = limit
	}
	if c.Pitch < -limit {
		c.Pitch = -limit
	}

	// Keep yaw in [-π, π) so it stays precise over long sessions.
	const twoPi = 2 * math.Pi
	if c.Yaw > math.Pi || c.Yaw < -math.Pi {
		c.Yaw -= twoPi * float32(math.Floor(float64(c.Yaw+math.Pi)/twoPi))
	}
}

// Follow anchors the camera to an entity transform.
func (c *FPCamera) Follow(t *Transform) { c.Target = t.Position }

// Eye returns the world-space eye position.
func (c *FPCamera) Eye() mgl32.Vec3 {
	return mgl32.Vec3{c.Target.X(), c.Target.Y() + c.EyeHeight, c.Target.Z()}
}

// Forward returns the normalized look direction.
func (c *FPCamera) Forward() mgl32.Vec3 {
	return mgl32.Vec3{
		-sin32(c.Yaw) * cos32(c.Pitch),
		-sin32(c.Pitch),
		-cos32(c.Yaw) * cos32(c.Pitch),
	}
}

// ViewVectors returns the eye, look-at center, and up vectors for
// Engine.SetCamera.
func (c *FPCamera) ViewVectors() (eye, center, up mgl32.Vec3) {
	eye = c.Eye()
	return eye, eye.Add(c.Forward()), mgl32.Vec3{0, 1, 0}
}

// GetYaw returns the camera yaw, for use as MoveIntent.Yaw.
func (c *FPCamera) GetYaw() float32 { return c.Yaw }
