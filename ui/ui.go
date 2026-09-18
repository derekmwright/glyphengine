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

// edgeSkirt is how far a quad is grown past its own edge, in pixels, to give
// ui.frag's coverage ramp an outside half.
//
// Half a pixel is the whole ramp: a pixel centre exactly on the edge is half
// covered, and one a full pixel outside is not covered at all. The rasterizer
// only generates fragments whose centre lies inside the geometry, so without
// the skirt the shader only ever sees the inner half and the shape sits half a
// pixel fat.
const edgeSkirt = 0.5

// AppendQuad appends 4 vertices and 6 indices for a solid-color rectangle.
//
// The quad is emitted half a pixel larger than asked for on every side, with
// UVs running from just below 0 to just above 1 so that UV 0 and 1 land on the
// requested edge. ui.frag turns that into coverage; see edgeCoverage there.
//
// A zero-width or zero-height quad is dropped rather than grown. An empty
// progress bar asks for exactly that, and a skirt around nothing is a visible
// one-pixel sliver where the bar is supposed to be empty.
func AppendQuad(verts []renderer.Vertex, idxs []uint16, x, y, w, h float32, col [3]float32) ([]renderer.Vertex, []uint16) {
	if w <= 0 || h <= 0 {
		return verts, idxs
	}

	// UV extent of the skirt, in this quad's own UV units.
	eu := edgeSkirt / w
	ev := edgeSkirt / h

	x0, x1 := x-edgeSkirt, x+w+edgeSkirt
	y0, y1 := y-edgeSkirt, y+h+edgeSkirt
	u0, u1 := -eu, 1+eu
	v0, v1 := -ev, 1+ev

	base := uint16(len(verts))
	verts = append(verts,
		renderer.Vertex{Pos: [3]float32{x0, y0, 0}, Color: col, UV: [2]float32{u0, v0}},
		renderer.Vertex{Pos: [3]float32{x1, y0, 0}, Color: col, UV: [2]float32{u1, v0}},
		renderer.Vertex{Pos: [3]float32{x1, y1, 0}, Color: col, UV: [2]float32{u1, v1}},
		renderer.Vertex{Pos: [3]float32{x0, y1, 0}, Color: col, UV: [2]float32{u0, v1}},
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
