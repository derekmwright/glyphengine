package input

import "github.com/go-gl/glfw/v3.3/glfw"

// Type aliases so game code doesn't need to import GLFW directly.
type Key = glfw.Key
type MouseButton = glfw.MouseButton
type ModifierKey = glfw.ModifierKey

// Keyboard keys.
//
// These are names, not a filter: Input tracks every key GLFW reports, so a key
// missing from this list still works via input.Key(glfw.KeyF13). The list
// covers the printable US-layout keys plus the common modifiers, navigation,
// function, and keypad keys.
const (
	KeyA = glfw.KeyA
	KeyB = glfw.KeyB
	KeyC = glfw.KeyC
	KeyD = glfw.KeyD
	KeyE = glfw.KeyE
	KeyF = glfw.KeyF
	KeyG = glfw.KeyG
	KeyH = glfw.KeyH
	KeyI = glfw.KeyI
	KeyJ = glfw.KeyJ
	KeyK = glfw.KeyK
	KeyL = glfw.KeyL
	KeyM = glfw.KeyM
	KeyN = glfw.KeyN
	KeyO = glfw.KeyO
	KeyP = glfw.KeyP
	KeyQ = glfw.KeyQ
	KeyR = glfw.KeyR
	KeyS = glfw.KeyS
	KeyT = glfw.KeyT
	KeyU = glfw.KeyU
	KeyV = glfw.KeyV
	KeyW = glfw.KeyW
	KeyX = glfw.KeyX
	KeyY = glfw.KeyY
	KeyZ = glfw.KeyZ

	Key0 = glfw.Key0
	Key1 = glfw.Key1
	Key2 = glfw.Key2
	Key3 = glfw.Key3
	Key4 = glfw.Key4
	Key5 = glfw.Key5
	Key6 = glfw.Key6
	Key7 = glfw.Key7
	Key8 = glfw.Key8
	Key9 = glfw.Key9

	KeyF1  = glfw.KeyF1
	KeyF2  = glfw.KeyF2
	KeyF3  = glfw.KeyF3
	KeyF4  = glfw.KeyF4
	KeyF5  = glfw.KeyF5
	KeyF6  = glfw.KeyF6
	KeyF7  = glfw.KeyF7
	KeyF8  = glfw.KeyF8
	KeyF9  = glfw.KeyF9
	KeyF10 = glfw.KeyF10
	KeyF11 = glfw.KeyF11
	KeyF12 = glfw.KeyF12

	KeySpace        = glfw.KeySpace
	KeyEscape       = glfw.KeyEscape
	KeyEnter        = glfw.KeyEnter
	KeyTab          = glfw.KeyTab
	KeyBackspace    = glfw.KeyBackspace
	KeyDelete       = glfw.KeyDelete
	KeyInsert       = glfw.KeyInsert
	KeyHome         = glfw.KeyHome
	KeyEnd          = glfw.KeyEnd
	KeyPageUp       = glfw.KeyPageUp
	KeyPageDown     = glfw.KeyPageDown
	KeyCapsLock     = glfw.KeyCapsLock
	KeyLeftShift    = glfw.KeyLeftShift
	KeyRightShift   = glfw.KeyRightShift
	KeyLeftControl  = glfw.KeyLeftControl
	KeyRightControl = glfw.KeyRightControl
	KeyLeftAlt      = glfw.KeyLeftAlt
	KeyRightAlt     = glfw.KeyRightAlt
	KeyLeftSuper    = glfw.KeyLeftSuper
	KeyRightSuper   = glfw.KeyRightSuper

	KeyUp    = glfw.KeyUp
	KeyDown  = glfw.KeyDown
	KeyLeft  = glfw.KeyLeft
	KeyRight = glfw.KeyRight

	KeyMinus        = glfw.KeyMinus
	KeyEqual        = glfw.KeyEqual
	KeyLeftBracket  = glfw.KeyLeftBracket
	KeyRightBracket = glfw.KeyRightBracket
	KeyBackslash    = glfw.KeyBackslash
	KeySemicolon    = glfw.KeySemicolon
	KeyApostrophe   = glfw.KeyApostrophe
	KeyGraveAccent  = glfw.KeyGraveAccent
	KeyComma        = glfw.KeyComma
	KeyPeriod       = glfw.KeyPeriod
	KeySlash        = glfw.KeySlash

	KeyKP0        = glfw.KeyKP0
	KeyKP1        = glfw.KeyKP1
	KeyKP2        = glfw.KeyKP2
	KeyKP3        = glfw.KeyKP3
	KeyKP4        = glfw.KeyKP4
	KeyKP5        = glfw.KeyKP5
	KeyKP6        = glfw.KeyKP6
	KeyKP7        = glfw.KeyKP7
	KeyKP8        = glfw.KeyKP8
	KeyKP9        = glfw.KeyKP9
	KeyKPDecimal  = glfw.KeyKPDecimal
	KeyKPDivide   = glfw.KeyKPDivide
	KeyKPMultiply = glfw.KeyKPMultiply
	KeyKPSubtract = glfw.KeyKPSubtract
	KeyKPAdd      = glfw.KeyKPAdd
	KeyKPEnter    = glfw.KeyKPEnter
)

// Mouse buttons.
const (
	MouseButtonLeft   = glfw.MouseButtonLeft
	MouseButtonRight  = glfw.MouseButtonRight
	MouseButtonMiddle = glfw.MouseButtonMiddle
)
