package ui

import "github.com/derekmwright/glyphengine/renderer"

// Label renders a single line of MSDF text at an anchored screen position.
type Label struct {
	Anchor           Anchor
	OffsetX, OffsetY float32
	Text             string
	FontSize         float32 // reference pixels
	Color            [3]float32
	Alpha            float32
	Align            Align
	Hidden           bool
	bounds           Rect
}

// BuildAt renders the label at an explicit screen position and font size.
// containerW is used for center/right alignment; pass 0 for left-aligned text.
func (l *Label) BuildAt(x, y, fontSize, containerW float32, font *renderer.Font) []renderer.TextLine {
	if l.Hidden || l.Text == "" {
		return nil
	}
	tx := x
	if containerW > 0 {
		switch l.Align {
		case AlignCenter:
			tw := font.MeasureText(l.Text, fontSize)
			tx = x + (containerW-tw)/2
		case AlignRight:
			tw := font.MeasureText(l.Text, fontSize)
			tx = x + containerW - tw
		}
	}
	return []renderer.TextLine{{
		Text:  l.Text,
		X:     tx,
		Y:     y,
		Scale: fontSize,
		Color: l.Color,
		Alpha: l.Alpha,
	}}
}

// Build resolves anchor position and renders the label.
func (l *Label) Build(scale, sw, sh float32, font *renderer.Font) []renderer.TextLine {
	if l.Hidden || l.Text == "" {
		return nil
	}
	fontSize := l.FontSize * scale
	tw := font.MeasureText(l.Text, fontSize)
	th := fontSize
	l.bounds = ResolveAnchor(l.Anchor, l.OffsetX, l.OffsetY, tw/scale, th/scale, sw, sh, scale)
	return []renderer.TextLine{{
		Text:  l.Text,
		X:     l.bounds.X,
		Y:     l.bounds.Y,
		Scale: fontSize,
		Color: l.Color,
		Alpha: l.Alpha,
	}}
}

func (l *Label) Bounds() Rect  { return l.bounds }
func (l *Label) Visible() bool { return !l.Hidden }
