package ui

import "github.com/derekmwright/glyphengine/input"

// Widget is the base interface for all UI widgets.
type Widget interface {
	Bounds() Rect
	Visible() bool
}

// Clickable is a widget that responds to mouse clicks.
type Clickable interface {
	Widget
	Contains(mx, my float32) bool
	OnMouseDown()
	OnMouseUp()
	SetPressed(bool) // called when click is cancelled (released outside)
}

// Focusable is a widget that receives keyboard input when focused.
type Focusable interface {
	Widget
	HandleChar(ch rune)
	HandleKey(key input.Key, pressed bool)
	Focused() bool
	SetFocused(bool)
}

// UIManager routes input to UI widgets and tracks consumed input state.
type UIManager struct {
	clickables      []Clickable
	focused         Focusable
	activeClickable Clickable // currently pressed clickable
	scale           float32
	mouseX, mouseY  float32
	mouseConsumed   bool
	keyConsumed     bool
}

// NewUIManager creates an empty UIManager.
func NewUIManager() *UIManager {
	return &UIManager{scale: 1.0}
}

// RegisterClickable adds a clickable widget for hit testing.
// Widgets registered later have higher z-order (tested first).
func (m *UIManager) RegisterClickable(c Clickable) {
	m.clickables = append(m.clickables, c)
}

// SetFocused sets the currently focused widget for keyboard routing.
func (m *UIManager) SetFocused(f Focusable) {
	if m.focused != nil && m.focused != f {
		m.focused.SetFocused(false)
	}
	m.focused = f
	if f != nil {
		f.SetFocused(true)
	}
}

// HasFocus returns true if any widget currently has keyboard focus.
func (m *UIManager) HasFocus() bool {
	return m.focused != nil && m.focused.Focused()
}

// HandleInput processes mouse and keyboard input, routing to appropriate widgets.
// Must be called once per frame before game input processing.
func (m *UIManager) HandleInput(inp *input.Input) {
	m.mouseConsumed = false
	m.keyConsumed = false

	mx, my := inp.MousePos()
	m.mouseX = float32(mx)
	m.mouseY = float32(my)

	// Route keyboard to focused widget.
	chars := inp.ConsumeChars()
	if m.focused != nil && m.focused.Focused() {
		m.keyConsumed = true
		for _, ch := range chars {
			m.focused.HandleChar(ch)
		}
		for _, key := range []input.Key{input.KeyBackspace, input.KeyEnter, input.KeyEscape} {
			if inp.KeyPressed(key) {
				m.focused.HandleKey(key, true)
			}
		}
		// Check if focus was released by Escape.
		if m.focused != nil && !m.focused.Focused() {
			m.focused = nil
		}
	}

	// Mouse down: reverse-order hit test.
	if inp.MousePressed(input.MouseButtonLeft) {
		for i := len(m.clickables) - 1; i >= 0; i-- {
			c := m.clickables[i]
			if c.Visible() && c.Contains(m.mouseX, m.mouseY) {
				c.OnMouseDown()
				m.activeClickable = c
				m.mouseConsumed = true
				break
			}
		}
	}

	// Mouse up: complete or cancel click.
	if inp.MouseReleased(input.MouseButtonLeft) && m.activeClickable != nil {
		if m.activeClickable.Contains(m.mouseX, m.mouseY) {
			m.activeClickable.OnMouseUp()
		} else {
			m.activeClickable.SetPressed(false)
		}
		m.activeClickable = nil
	}
}

// ConsumedMouse returns true if a UI widget consumed the mouse click this frame.
func (m *UIManager) ConsumedMouse() bool { return m.mouseConsumed }

// ConsumedKeyboard returns true if a focused widget consumed keyboard input this frame.
func (m *UIManager) ConsumedKeyboard() bool { return m.keyConsumed }

// MousePos returns the current mouse position tracked by the manager.
func (m *UIManager) MousePos() (float32, float32) { return m.mouseX, m.mouseY }

// UpdateScale recomputes the UI scale factor from screen dimensions.
func (m *UIManager) UpdateScale(sw, sh float32) { m.scale = UIScale(sw, sh) }

// Scale returns the current UI scale factor.
func (m *UIManager) Scale() float32 { return m.scale }
