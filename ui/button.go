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

	// Glow is how much light the button emits on top of its own colours, panel
	// and label alike; see renderer.UIRenderObject.Glow. Zero, the default, is
	// no glow, and it does nothing unless the engine was built with
	// glyphengine.WithUIGlow. A disabled button does not glow whatever this
	// says: glowing is the loudest way a control has of asking to be pressed.
	Glow float32

	// Disabled greys the button out and takes it out of the keyboard traversal
	// order. A disabled item the highlight can still land on is a dead end the
	// player has to arrow back out of.
	Disabled  bool
	panel     *renderer.Panel
	fillColor [3]float32
	hovered   bool
	pressed   bool
	Hidden    bool
	bounds    Rect
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

	// Adjust fill color for disabled/hover/pressed states. Disabled is checked
	// first: a disabled button cannot be pressed or highlighted, so showing it
	// either way would invite a click that does nothing.
	fc := b.fillColor
	switch {
	case b.Disabled:
		fc = [3]float32{fc[0] * 0.45, fc[1] * 0.45, fc[2] * 0.45}
	case b.pressed:
		fc = [3]float32{fc[0] * 0.6, fc[1] * 0.6, fc[2] * 0.6}
	case b.hovered:
		fc = [3]float32{fc[0] * 1.5, fc[1] * 1.5, fc[2] * 1.5}
	}
	b.panel.Layers[0].Color = fc

	glow := b.Glow
	if b.Disabled {
		glow = 0
	}
	for i := range b.panel.Layers {
		b.panel.Layers[i].Glow = glow
	}

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
			Glow: glow,
		})
	}

	return panels, text
}

// UpdateHover sets the hover state based on mouse position.
//
// Not needed, and not safe, for a button registered with
// UIManager.RegisterNavigable: the manager drives the highlight from both the
// keyboard and the pointer, and this would overwrite it from the pointer alone
// every frame, which makes the arrow keys look broken.
func (b *Button) UpdateHover(mx, my float32) {
	b.hovered = b.bounds.Contains(mx, my)
}

// Navigable interface implementation.

// SetHighlighted is the single writer of the button's highlight when it is
// registered for navigation; the UIManager drives it from both the keyboard and
// the pointer. One highlight rather than two is the whole point: a menu where
// the pointer and the keyboard each keep their own is a menu where the mouse
// says one thing and Enter does another.
func (b *Button) SetHighlighted(v bool) { b.hovered = v }

// Activate runs the click handler, which is what Enter, Space and a mouse
// release all do.
func (b *Button) Activate() {
	if b.OnClickFn != nil {
		b.OnClickFn()
	}
}

// Enabled reports whether the highlight may rest here.
func (b *Button) Enabled() bool { return !b.Disabled && !b.Hidden }

// Clickable interface implementation.

func (b *Button) Contains(mx, my float32) bool { return b.bounds.Contains(mx, my) }
func (b *Button) Bounds() Rect                 { return b.bounds }
func (b *Button) Visible() bool                { return !b.Hidden }
func (b *Button) SetPressed(v bool)            { b.pressed = v }

// A disabled button takes no press and fires nothing. Traversal already skips
// it, but the pointer does not go through traversal, so without this a greyed
// out item would still be clickable -- which is worse than not greying it out
// at all, because it looks unavailable and acts available.
func (b *Button) OnMouseDown() {
	if b.Disabled {
		return
	}
	b.pressed = true
}

func (b *Button) OnMouseUp() {
	if b.pressed && !b.Disabled && b.OnClickFn != nil {
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
