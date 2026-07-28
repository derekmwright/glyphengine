package yamlui

// InputState mirrors mouse/keyboard state without importing engine/input.
type InputState struct {
	MouseX, MouseY   float32
	MousePressed     bool // left button just pressed this frame
	MouseDown        bool // left button held
	MouseReleased    bool // left button just released this frame
	ScrollY          float32
	CharsTyped       []rune
	BackspacePressed bool
	EnterPressed     bool
	EscapePressed    bool
}

// UIEvent is emitted by interactive widgets.
type UIEvent struct {
	Kind   string // "click", "submit"
	NodeID string // widget ID that produced it
	Value  string // text_input submit value, or empty for buttons
}
