package input

import "github.com/go-gl/glfw/v3.3/glfw"

// Input tracks keyboard, mouse button, cursor, and scroll state per frame.
type Input struct {
	handle *glfw.Window

	// Keyboard state indexed by glfw.Key.
	keys     [glfw.KeyLast + 1]bool
	prevKeys [glfw.KeyLast + 1]bool

	// Mouse button state indexed by glfw.MouseButton.
	buttons     [glfw.MouseButtonLast + 1]bool
	prevButtons [glfw.MouseButtonLast + 1]bool

	// Cursor position.
	mouseX, mouseY         float64
	prevMouseX, prevMouseY float64

	// Scroll accumulated since last Update.
	scrollX, scrollY float64

	// Character input from GLFW char callback.
	charBuf []rune

	cursorLocked bool

	// Gamepad state, snapshotted once per frame in Update so that button edges
	// work the same way keyboard edges do. padSrc is an interface only so tests
	// can supply state without a controller plugged in.
	padSrc   padSource
	pads     [MaxPads]padState
	prevPads [MaxPads]padState
	deadzone float32
}

// New creates an Input and registers GLFW callbacks on the window.
func New(handle *glfw.Window) *Input {
	x, y := handle.GetCursorPos()
	inp := &Input{
		handle:     handle,
		mouseX:     x,
		mouseY:     y,
		prevMouseX: x,
		prevMouseY: y,
		padSrc:     glfwPads{},
		deadzone:   DefaultDeadzone,
	}

	handle.SetKeyCallback(func(_ *glfw.Window, key glfw.Key, _ int, action glfw.Action, _ glfw.ModifierKey) {
		if key < 0 || int(key) > len(inp.keys)-1 {
			return
		}
		switch action {
		case glfw.Press, glfw.Repeat:
			inp.keys[key] = true
		case glfw.Release:
			inp.keys[key] = false
		}
	})

	handle.SetMouseButtonCallback(func(_ *glfw.Window, button glfw.MouseButton, action glfw.Action, _ glfw.ModifierKey) {
		if button < 0 || int(button) > len(inp.buttons)-1 {
			return
		}
		switch action {
		case glfw.Press:
			inp.buttons[button] = true
		case glfw.Release:
			inp.buttons[button] = false
		}
	})

	handle.SetCursorPosCallback(func(_ *glfw.Window, x, y float64) {
		inp.mouseX = x
		inp.mouseY = y
	})

	handle.SetScrollCallback(func(_ *glfw.Window, xoff, yoff float64) {
		inp.scrollX += xoff
		inp.scrollY += yoff
	})

	handle.SetCharCallback(func(_ *glfw.Window, ch rune) {
		inp.charBuf = append(inp.charBuf, ch)
	})

	return inp
}

// Update snapshots previous-frame state and clears per-frame accumulators.
// Call once per frame BEFORE PollEvents so that prevKeys/prevButtons hold
// the end-of-last-frame state while callbacks update the current arrays.
func (inp *Input) Update() {
	inp.prevKeys = inp.keys
	inp.prevButtons = inp.buttons
	inp.prevMouseX = inp.mouseX
	inp.prevMouseY = inp.mouseY
	inp.scrollX = 0
	inp.scrollY = 0
	inp.pollPads()
}

// --- Keyboard queries ---

// KeyDown returns true if the key is currently held.
func (inp *Input) KeyDown(key Key) bool {
	return inp.keys[key]
}

// KeyPressed returns true if the key transitioned from up to down this frame.
func (inp *Input) KeyPressed(key Key) bool {
	return inp.keys[key] && !inp.prevKeys[key]
}

// KeyReleased returns true if the key transitioned from down to up this frame.
func (inp *Input) KeyReleased(key Key) bool {
	return !inp.keys[key] && inp.prevKeys[key]
}

// --- Mouse button queries ---

// MouseDown returns true if the mouse button is currently held.
func (inp *Input) MouseDown(button MouseButton) bool {
	return inp.buttons[button]
}

// MousePressed returns true if the mouse button transitioned from up to down this frame.
func (inp *Input) MousePressed(button MouseButton) bool {
	return inp.buttons[button] && !inp.prevButtons[button]
}

// MouseReleased returns true if the mouse button transitioned from down to up this frame.
func (inp *Input) MouseReleased(button MouseButton) bool {
	return !inp.buttons[button] && inp.prevButtons[button]
}

// --- Cursor queries ---

// MousePos returns the current cursor position in window coordinates.
func (inp *Input) MousePos() (x, y float64) {
	return inp.mouseX, inp.mouseY
}

// MouseDelta returns cursor movement since last frame.
func (inp *Input) MouseDelta() (dx, dy float64) {
	return inp.mouseX - inp.prevMouseX, inp.mouseY - inp.prevMouseY
}

// --- Scroll queries ---

// Scroll returns the scroll wheel delta accumulated this frame.
func (inp *Input) Scroll() (x, y float64) {
	return inp.scrollX, inp.scrollY
}

// ConsumeScroll returns the scroll delta and zeroes it so subsequent
// reads in the same frame see zero. Use this for input that should only
// be applied once per frame even when the caller runs in a fixed-timestep loop.
func (inp *Input) ConsumeScroll() (x, y float64) {
	x, y = inp.scrollX, inp.scrollY
	inp.scrollX, inp.scrollY = 0, 0
	return
}

// ConsumeChars returns all characters typed since the last call and clears the buffer.
func (inp *Input) ConsumeChars() []rune {
	chars := inp.charBuf
	inp.charBuf = nil
	return chars
}

// --- Cursor lock ---

// SetCursorLocked enables or disables FPS-style cursor capture.
func (inp *Input) SetCursorLocked(locked bool) {
	inp.cursorLocked = locked
	if locked {
		inp.handle.SetInputMode(glfw.CursorMode, glfw.CursorDisabled)
		if glfw.RawMouseMotionSupported() {
			inp.handle.SetInputMode(glfw.RawMouseMotion, glfw.True)
		}
	} else {
		if glfw.RawMouseMotionSupported() {
			inp.handle.SetInputMode(glfw.RawMouseMotion, glfw.False)
		}
		inp.handle.SetInputMode(glfw.CursorMode, glfw.CursorNormal)
	}
}

// CursorLocked returns whether the cursor is currently locked.
func (inp *Input) CursorLocked() bool {
	return inp.cursorLocked
}
