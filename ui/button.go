package ui

import "github.com/derekmwright/glyphengine/renderer"

// Button is a clickable panel with a text label and hover/pressed visual states.
type Button struct {
	Anchor           Anchor
	OffsetX, OffsetY float32
	Width, Height    float32 // reference pixels
	Label            string
	FontSize         float32 // reference pixels
	LabelColor       [3]float32
	OnClickFn        func()
	panel            *renderer.Panel
	fillColor        [3]float32
	hovered          bool
	pressed          bool
	Hidden           bool
	bounds           Rect
}

// NewButton creates a button with a single 9-slice layer.
func NewButton(r *renderer.Renderer, slice *renderer.NineSlice, color [3]float32, opacity float32) (*Button, error) {
	p := renderer.NewPanel()
	if err := p.AddLayer(r, slice, color, opacity); err != nil {
		return nil, err
	}
	return &Button{
		panel:      p,
		fillColor:  color,
		LabelColor: [3]float32{1, 1, 1},
		FontSize:   12,
	}, nil
}

// Build renders the button panel and label text.
func (b *Button) Build(r *renderer.Renderer, scale, sw, sh float32, font *renderer.Font) ([]renderer.UIRenderObject, []renderer.TextLine) {
	if b.Hidden {
		return nil, nil
	}
	b.bounds = ResolveAnchor(b.Anchor, b.OffsetX, b.OffsetY, b.Width, b.Height, sw, sh, scale)

	// Adjust fill color for hover/pressed states.
	fc := b.fillColor
	if b.pressed {
		fc = [3]float32{fc[0] * 0.6, fc[1] * 0.6, fc[2] * 0.6}
	} else if b.hovered {
		fc = [3]float32{fc[0] * 1.5, fc[1] * 1.5, fc[2] * 1.5}
	}
	b.panel.Layers[0].Color = fc

	b.panel.X = b.bounds.X
	b.panel.Y = b.bounds.Y
	b.panel.Width = b.bounds.W
	b.panel.Height = b.bounds.H
	b.panel.Scale = borderScale(scale)
	b.panel.Rebuild(r)
	panels := b.panel.UIRenderObjects(sw, sh)

	// Center label text in button.
	var text []renderer.TextLine
	if b.Label != "" && font != nil {
		fontSize := b.FontSize * scale
		tw := font.MeasureText(b.Label, fontSize)
		tx := b.bounds.X + (b.bounds.W-tw)/2
		ty := b.bounds.Y + (b.bounds.H-fontSize)/2
		text = append(text, renderer.TextLine{
			Text: b.Label, X: tx, Y: ty, Scale: fontSize, Color: b.LabelColor,
		})
	}

	return panels, text
}

// UpdateHover sets the hover state based on mouse position.
func (b *Button) UpdateHover(mx, my float32) {
	b.hovered = b.bounds.Contains(mx, my)
}

// Clickable interface implementation.

func (b *Button) Contains(mx, my float32) bool { return b.bounds.Contains(mx, my) }
func (b *Button) Bounds() Rect                 { return b.bounds }
func (b *Button) Visible() bool                { return !b.Hidden }
func (b *Button) SetPressed(v bool)            { b.pressed = v }

func (b *Button) OnMouseDown() { b.pressed = true }

func (b *Button) OnMouseUp() {
	if b.pressed && b.OnClickFn != nil {
		b.OnClickFn()
	}
	b.pressed = false
}

// SetFillColor changes the base fill color of the button.
func (b *Button) SetFillColor(c [3]float32) { b.fillColor = c }

// Destroy frees GPU resources.
func (b *Button) Destroy(r *renderer.Renderer) {
	b.panel.Destroy(r)
}
