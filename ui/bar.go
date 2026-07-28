package ui

import "github.com/derekmwright/glyphengine/renderer"

// ProgressBar renders a two-quad bar (background + foreground) sized by Value/Max.
type ProgressBar struct {
	Anchor           Anchor
	OffsetX, OffsetY float32
	Width, Height    float32 // reference pixels
	Value, Max       float32
	FgColor, BgColor [3]float32
	Hidden           bool
	bounds           Rect
}

// BuildAt builds bar quads at an explicit screen position and size.
func (b *ProgressBar) BuildAt(x, y, w, h float32) ([]renderer.Vertex, []uint16) {
	var verts []renderer.Vertex
	var idxs []uint16
	verts, idxs = AppendQuad(verts, idxs, x, y, w, h, b.BgColor)
	if b.Max > 0 {
		ratio := b.Value / b.Max
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
		verts, idxs = AppendQuad(verts, idxs, x, y, w*ratio, h, b.FgColor)
	}
	return verts, idxs
}

// Build resolves anchor position and builds bar quads.
func (b *ProgressBar) Build(scale, sw, sh float32) ([]renderer.Vertex, []uint16) {
	if b.Hidden {
		return nil, nil
	}
	b.bounds = ResolveAnchor(b.Anchor, b.OffsetX, b.OffsetY, b.Width, b.Height, sw, sh, scale)
	return b.BuildAt(b.bounds.X, b.bounds.Y, b.bounds.W, b.bounds.H)
}

func (b *ProgressBar) Bounds() Rect  { return b.bounds }
func (b *ProgressBar) Visible() bool { return !b.Hidden }
