package ui

import "github.com/derekmwright/glyphengine/renderer"

// Anchor defines a screen anchor point for widget positioning.
type Anchor int

const (
	AnchorTopLeft Anchor = iota
	AnchorTopCenter
	AnchorTopRight
	AnchorCenterLeft
	AnchorCenter
	AnchorCenterRight
	AnchorBottomLeft
	AnchorBottomCenter
	AnchorBottomRight
)

// Align controls text alignment within a container.
type Align int

const (
	AlignLeft Align = iota
	AlignCenter
	AlignRight
)

// Rect is an axis-aligned rectangle in screen coordinates (Y-down).
type Rect struct{ X, Y, W, H float32 }

// Contains returns true if the point (x, y) is inside the rectangle.
func (r Rect) Contains(x, y float32) bool {
	return x >= r.X && x <= r.X+r.W && y >= r.Y && y <= r.Y+r.H
}

// UIScale computes the UI scale factor for a reference resolution of 1920x1080.
func UIScale(screenW, screenH float32) float32 {
	sx := screenW / 1920
	sy := screenH / 1080
	if sx < sy {
		return sx
	}
	return sy
}

// ResolveAnchor computes a screen-space Rect for a widget given its anchor,
// offset (reference pixels), size (reference pixels), screen dimensions, and scale.
// Screen coordinates use Y-down with (0,0) at top-left.
func ResolveAnchor(anchor Anchor, offsetX, offsetY, refW, refH, screenW, screenH, scale float32) Rect {
	w := refW * scale
	h := refH * scale
	ox := offsetX * scale
	oy := offsetY * scale

	var x, y float32
	switch anchor {
	case AnchorTopLeft:
		x, y = ox, oy
	case AnchorTopCenter:
		x, y = screenW/2-w/2+ox, oy
	case AnchorTopRight:
		x, y = screenW-w+ox, oy
	case AnchorCenterLeft:
		x, y = ox, screenH/2-h/2+oy
	case AnchorCenter:
		x, y = screenW/2-w/2+ox, screenH/2-h/2+oy
	case AnchorCenterRight:
		x, y = screenW-w+ox, screenH/2-h/2+oy
	case AnchorBottomLeft:
		x, y = ox, screenH-h+oy
	case AnchorBottomCenter:
		x, y = screenW/2-w/2+ox, screenH-h+oy
	case AnchorBottomRight:
		x, y = screenW-w+ox, screenH-h+oy
	}
	return Rect{X: x, Y: y, W: w, H: h}
}

// AppendQuad appends 4 vertices and 6 indices for a solid-color rectangle.
func AppendQuad(verts []renderer.Vertex, idxs []uint16, x, y, w, h float32, col [3]float32) ([]renderer.Vertex, []uint16) {
	base := uint16(len(verts))
	verts = append(verts,
		renderer.Vertex{Pos: [3]float32{x, y, 0}, Color: col},
		renderer.Vertex{Pos: [3]float32{x + w, y, 0}, Color: col},
		renderer.Vertex{Pos: [3]float32{x + w, y + h, 0}, Color: col},
		renderer.Vertex{Pos: [3]float32{x, y + h, 0}, Color: col},
	)
	idxs = append(idxs, base, base+1, base+2, base+2, base+3, base)
	return verts, idxs
}

// AccumulateQuads appends new vertices and re-based indices to accumulator slices.
func AccumulateQuads(verts []renderer.Vertex, idxs []uint16, newVerts []renderer.Vertex, newIdx []uint16) ([]renderer.Vertex, []uint16) {
	base := uint16(len(verts))
	verts = append(verts, newVerts...)
	for _, idx := range newIdx {
		idxs = append(idxs, idx+base)
	}
	return verts, idxs
}
