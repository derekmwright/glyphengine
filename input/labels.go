package input

import "fmt"

// Human-readable names for sources, so a rebinding screen has something to print.
//
// These are US-layout names from a table rather than from glfw.GetKeyName, which
// would be layout-aware but requires GLFW to be initialized — and these labels are
// wanted in tests and in headless tools too. A game that cares about AZERTY can
// call glfw.GetKeyName itself; the Source carries the key code.

// maxCaptureKey and maxCaptureMouse bound the scan CapturePressed performs. Both
// are the last code the underlying library defines, so the scan covers every key
// and button a device can report rather than only the named ones.
const (
	maxCaptureKey   = Key(348)       // glfw.KeyLast
	maxCaptureMouse = MouseButton(7) // glfw.MouseButtonLast
)

var keyNames = map[Key]string{
	KeySpace:        "Space",
	KeyEscape:       "Escape",
	KeyEnter:        "Enter",
	KeyTab:          "Tab",
	KeyBackspace:    "Backspace",
	KeyDelete:       "Delete",
	KeyInsert:       "Insert",
	KeyHome:         "Home",
	KeyEnd:          "End",
	KeyPageUp:       "Page Up",
	KeyPageDown:     "Page Down",
	KeyCapsLock:     "Caps Lock",
	KeyLeftShift:    "Left Shift",
	KeyRightShift:   "Right Shift",
	KeyLeftControl:  "Left Ctrl",
	KeyRightControl: "Right Ctrl",
	KeyLeftAlt:      "Left Alt",
	KeyRightAlt:     "Right Alt",
	KeyLeftSuper:    "Left Super",
	KeyRightSuper:   "Right Super",
	KeyUp:           "Up",
	KeyDown:         "Down",
	KeyLeft:         "Left",
	KeyRight:        "Right",
	KeyMinus:        "-",
	KeyEqual:        "=",
	KeyLeftBracket:  "[",
	KeyRightBracket: "]",
	KeyBackslash:    "\\",
	KeySemicolon:    ";",
	KeyApostrophe:   "'",
	KeyGraveAccent:  "`",
	KeyComma:        ",",
	KeyPeriod:       ".",
	KeySlash:        "/",
}

func keyLabel(k Key) string {
	if name, ok := keyNames[k]; ok {
		return name
	}
	switch {
	case k >= KeyA && k <= KeyZ:
		return string(rune('A' + (k - KeyA)))
	case k >= Key0 && k <= Key9:
		return string(rune('0' + (k - Key0)))
	case k >= KeyF1 && k <= KeyF12:
		return fmt.Sprintf("F%d", int(k-KeyF1)+1)
	case k >= KeyKP0 && k <= KeyKP9:
		return fmt.Sprintf("Keypad %d", int(k-KeyKP0))
	}
	return fmt.Sprintf("Key %d", int(k))
}

// buttonLabel names gamepad buttons by position, matching how Button is defined.
//
// Face buttons carry both labels because neither alone is right: a player on an
// Xbox pad looks for A and a player on a DualSense looks for the cross, and the
// same physical position is both.
var buttonNames = map[Button]string{
	ButtonA:           "A / Cross",
	ButtonB:           "B / Circle",
	ButtonX:           "X / Square",
	ButtonY:           "Y / Triangle",
	ButtonLeftBumper:  "Left Bumper",
	ButtonRightBumper: "Right Bumper",
	ButtonBack:        "Back",
	ButtonStart:       "Start",
	ButtonGuide:       "Guide",
	ButtonLeftThumb:   "Left Stick Click",
	ButtonRightThumb:  "Right Stick Click",
	ButtonDPadUp:      "D-Pad Up",
	ButtonDPadRight:   "D-Pad Right",
	ButtonDPadDown:    "D-Pad Down",
	ButtonDPadLeft:    "D-Pad Left",
}

func buttonLabel(b Button) string {
	if name, ok := buttonNames[b]; ok {
		return name
	}
	return fmt.Sprintf("Pad Button %d", int(b))
}

var axisNames = map[Axis]string{
	AxisLeftX:        "Left Stick X",
	AxisLeftY:        "Left Stick Y",
	AxisRightX:       "Right Stick X",
	AxisRightY:       "Right Stick Y",
	AxisLeftTrigger:  "Left Trigger",
	AxisRightTrigger: "Right Trigger",
}

func axisLabel(a Axis) string {
	if name, ok := axisNames[a]; ok {
		return name
	}
	return fmt.Sprintf("Pad Axis %d", int(a))
}
