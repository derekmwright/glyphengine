package ui

import (
	"math"

	"github.com/derekmwright/glyphengine/renderer"
)

// PanelWidget wraps a renderer.Panel with anchor-based positioning.
type PanelWidget struct {
	Anchor           Anchor
	OffsetX, OffsetY float32
	Width, Height    float32 // reference pixels
	Panel            *renderer.Panel
	Hidden           bool
	bounds           Rect
}

// NewPanelWidget creates a PanelWidget with a single 9-slice layer.
func NewPanelWidget(r *renderer.Renderer, slice *renderer.NineSlice, color [3]float32, opacity float32) (*PanelWidget, error) {
	p := renderer.NewPanel()
	if err := p.AddLayer(r, slice, color, opacity); err != nil {
		return nil, err
	}
	return &PanelWidget{Panel: p}, nil
}

// borderScale dampens the nine-slice border scaling for high-DPI displays.
// At scale=1.0 (1080p) borders are unchanged; at scale=2.0 (4K) borders
// grow by ~1.41x instead of 2x, keeping them crisp without looking chunky.
func borderScale(scale float32) float32 {
	if scale <= 1 {
		return scale
	}
	return float32(math.Sqrt(float64(scale)))
}

// Build resolves anchor position, rebuilds the panel mesh, and returns UIRenderObjects.
func (pw *PanelWidget) Build(r *renderer.Renderer, scale, sw, sh float32) []renderer.UIRenderObject {
	if pw.Hidden {
		return nil
	}
	pw.bounds = ResolveAnchor(pw.Anchor, pw.OffsetX, pw.OffsetY, pw.Width, pw.Height, sw, sh, scale)
	pw.Panel.X = pw.bounds.X
	pw.Panel.Y = pw.bounds.Y
	pw.Panel.Width = pw.bounds.W
	pw.Panel.Height = pw.bounds.H
	pw.Panel.Scale = borderScale(scale)
	pw.Panel.Rebuild(r)
	return pw.Panel.UIRenderObjects(sw, sh)
}

// BuildAt positions the panel at explicit screen coordinates (bypasses anchor).
func (pw *PanelWidget) BuildAt(r *renderer.Renderer, x, y, w, h, scale, sw, sh float32) []renderer.UIRenderObject {
	if pw.Hidden {
		return nil
	}
	pw.bounds = Rect{X: x, Y: y, W: w, H: h}
	pw.Panel.X = x
	pw.Panel.Y = y
	pw.Panel.Width = w
	pw.Panel.Height = h
	pw.Panel.Scale = borderScale(scale)
	pw.Panel.Rebuild(r)
	return pw.Panel.UIRenderObjects(sw, sh)
}

func (pw *PanelWidget) Bounds() Rect  { return pw.bounds }
func (pw *PanelWidget) Visible() bool { return !pw.Hidden }

// Destroy frees GPU resources for all panel layers.
func (pw *PanelWidget) Destroy(r *renderer.Renderer) {
	pw.Panel.Destroy(r)
}
