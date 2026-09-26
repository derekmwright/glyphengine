package renderer

import "github.com/vkngwrapper/core/v3/core1_0"

// ClipRect is a screen-space rectangle a UI draw or a text line is confined to.
//
// Screen pixels, Y-down, the same coordinates every screen-space overlay in
// this package is already built in -- so a caller that knows where it put a
// panel knows what to write here without a transform.
//
// It is a pointer on UIRenderObject and TextLine rather than a value with a
// "clipped" bool because the overwhelmingly common case is no clipping at all,
// and a nil pointer is the one encoding of that which cannot be confused with
// an empty rect. An empty rect means "draw nothing", which is a real thing a
// scrolled-away row asks for.
//
// The two channels honour it by different means, because they are not the same
// shape of draw. A UIRenderObject is one draw, so the composite pass turns its
// clip into a scissor (see recordUIComposite). Text lines are merged into a
// single mesh and a single draw -- one overlay covers every line it was given
// -- so a scissor there could only clip all of them or none, and they are
// clipped geometrically as the glyph quads are built instead.
type ClipRect struct{ X, Y, W, H float32 }

// Empty reports that nothing can survive this clip.
func (c ClipRect) Empty() bool { return c.W <= 0 || c.H <= 0 }

// Intersect returns the overlap of two clip rects. An empty result is returned
// as-is rather than normalised: every caller here tests Empty, and a rect with
// a negative extent is still an honest answer to "what do these two share".
func (c ClipRect) Intersect(o ClipRect) ClipRect {
	x0, y0 := maxf(c.X, o.X), maxf(c.Y, o.Y)
	x1, y1 := minf(c.X+c.W, o.X+o.W), minf(c.Y+c.H, o.Y+o.H)
	return ClipRect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// scissor turns a clip rect into a Vulkan scissor inside the given extent.
//
// Rounded outward -- floor on the near edge, ceil on the far one -- so a clip
// whose edge falls between two pixel centres keeps the pixel it partly covers
// rather than dropping it. The alternative loses a row of a straddling widget
// at some scales and not at others, which is the kind of off-by-one that only
// shows up on someone else's monitor.
//
// Clamped to the extent because vkCmdSetScissor rejects a negative offset and
// an extent past the framebuffer: a row scrolled half off the top of the screen
// has a perfectly sensible negative clip Y, and it is this function's job to
// make that drawable rather than a validation error.
func (c ClipRect) scissor(extent core1_0.Extent2D) core1_0.Rect2D {
	x0 := clampInt(floorInt(c.X), 0, extent.Width)
	y0 := clampInt(floorInt(c.Y), 0, extent.Height)
	x1 := clampInt(ceilInt(c.X+c.W), 0, extent.Width)
	y1 := clampInt(ceilInt(c.Y+c.H), 0, extent.Height)
	if x1 < x0 {
		x1 = x0
	}
	if y1 < y0 {
		y1 = y0
	}
	return core1_0.Rect2D{
		Offset: core1_0.Offset2D{X: x0, Y: y0},
		Extent: core1_0.Extent2D{Width: x1 - x0, Height: y1 - y0},
	}
}

func floorInt(v float32) int {
	i := int(v)
	if float32(i) > v {
		i--
	}
	return i
}

func ceilInt(v float32) int {
	i := int(v)
	if float32(i) < v {
		i++
	}
	return i
}

func maxf(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func minf(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

// TrimQuad trims an axis-aligned quad and its UVs to this clip rect.
//
// This is the geometric half of clipping, for the emitters whose output is
// merged into one draw before it reaches the recorder and can therefore never
// be reached by a scissor: text lines here, and ui/yamlui's shared flat-quad
// stream. It returns the trimmed corners and the UVs that belonged to THOSE
// corners in the original quad, so whatever the UV means -- an atlas lookup, a
// coverage ramp -- goes on meaning it over the pixels that survive. Cutting the
// quad and leaving the UVs put squeezes the whole glyph into what is left of
// its box, which reads as a smear rather than as a clip.
//
// ok is false when nothing survives, so the caller drops the quad rather than
// emitting a degenerate one.
func (c ClipRect) TrimQuad(x0, y0, x1, y1, u0, v0, u1, v1 float32) (nx0, ny0, nx1, ny1, nu0, nv0, nu1, nv1 float32, ok bool) {
	if x1 <= x0 || y1 <= y0 || c.Empty() {
		return 0, 0, 0, 0, 0, 0, 0, 0, false
	}
	cx0, cy0 := c.X, c.Y
	cx1, cy1 := c.X+c.W, c.Y+c.H
	nx0, ny0 = maxf(x0, cx0), maxf(y0, cy0)
	nx1, ny1 = minf(x1, cx1), minf(y1, cy1)
	if nx1 <= nx0 || ny1 <= ny0 {
		return 0, 0, 0, 0, 0, 0, 0, 0, false
	}
	fx0, fx1 := (nx0-x0)/(x1-x0), (nx1-x0)/(x1-x0)
	fy0, fy1 := (ny0-y0)/(y1-y0), (ny1-y0)/(y1-y0)
	nu0, nu1 = u0+(u1-u0)*fx0, u0+(u1-u0)*fx1
	nv0, nv1 = v0+(v1-v0)*fy0, v0+(v1-v0)*fy1
	return nx0, ny0, nx1, ny1, nu0, nv0, nu1, nv1, true
}
