package ui

import (
	"unicode/utf8"

	"github.com/derekmwright/glyphengine/input"
	"github.com/derekmwright/glyphengine/renderer"
)

// InputField handles text input with cursor blink and focus state.
// It renders as a text line (no panel — the parent container provides the background).
type InputField struct {
	FontSize   float32 // reference pixels
	Color      [3]float32
	Prompt     string // prefix shown before text (e.g. "> ")
	OnSubmitFn func(string)

	text       string
	focused    bool
	cursorTick int
	bounds     Rect
}

// Text returns the current input text.
func (f *InputField) Text() string { return f.text }

// SetText sets the input text programmatically.
func (f *InputField) SetText(s string) { f.text = s }

// Clear resets the input text.
func (f *InputField) Clear() { f.text = "" }

// BuildAt renders the input field at an explicit screen position.
func (f *InputField) BuildAt(x, y, fontSize float32) []renderer.TextLine {
	if !f.focused {
		return nil
	}
	f.cursorTick++
	cursor := "_"
	if (f.cursorTick/30)%2 == 1 {
		cursor = ""
	}
	return []renderer.TextLine{{
		Text:  f.Prompt + f.text + cursor,
		X:     x,
		Y:     y,
		Scale: fontSize,
		Color: f.Color,
	}}
}

// Focusable interface implementation.

func (f *InputField) HandleChar(ch rune) {
	f.text += string(ch)
	f.cursorTick = 0
}

func (f *InputField) HandleKey(key input.Key, pressed bool) {
	if !pressed {
		return
	}
	switch key {
	case input.KeyBackspace:
		if len(f.text) > 0 {
			_, size := utf8.DecodeLastRuneInString(f.text)
			f.text = f.text[:len(f.text)-size]
			f.cursorTick = 0
		}
	case input.KeyEnter:
		if f.OnSubmitFn != nil && len(f.text) > 0 {
			f.OnSubmitFn(f.text)
		}
		f.text = ""
		f.focused = false
		f.cursorTick = 0
	case input.KeyEscape:
		f.text = ""
		f.focused = false
		f.cursorTick = 0
	}
}

func (f *InputField) Focused() bool     { return f.focused }
func (f *InputField) SetFocused(v bool) { f.focused = v; f.cursorTick = 0 }
func (f *InputField) Bounds() Rect      { return f.bounds }
func (f *InputField) Visible() bool     { return f.focused }
