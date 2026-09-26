package yamlui

import (
	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/ui/ease"
)

// Everything a scroll_view does beyond drawing its children: how far its
// content reaches, what the wheel, a drag and the thumb do to the offset, and
// how focus pulls a row back into sight. tree.go keeps the draw walk; this
// file keeps the state, because all of it outlives a frame and none of the
// rest of that file does.

const (
	// scrollWheelStep is how far one wheel notch moves the content, in
	// reference pixels. The value the widget has always used.
	scrollWheelStep = 30

	// scrollDeadZone is how far the pointer may move with the button down
	// before it counts as a drag rather than as a click, in reference pixels.
	//
	// It exists because a list whose rows are buttons has to be both draggable
	// and clickable with the same button, and a hand on a mouse moves a pixel
	// or two during any click. Four is what a pointer wanders during a
	// deliberate click and well under what a deliberate drag covers in its
	// first frame; below about two, ordinary clicks start scrolling the list
	// out from under themselves.
	scrollDeadZone = 4

	// scrollbarWidth and scrollThumbMin are the thumb's geometry in reference
	// pixels. Unchanged from the vertical-only widget, so a HUD that already
	// had a scroll_view keeps the bar it had.
	scrollbarWidth = 4
	scrollThumbMin = 10

	// scrollIntoViewDuration is how long focus takes to pull a row into the
	// view, in seconds on the tree's unscaled clock.
	//
	// Short enough that a held arrow key does not queue up a backlog of
	// animations -- key repeat is slower than this -- and long enough that the
	// eye follows the list rather than finding it somewhere else.
	scrollIntoViewDuration = 0.15
)

// scrollDir is what axes a scroll_view scrolls on.
type scrollDir int

const (
	scrollVertical scrollDir = iota
	scrollHorizontal
	scrollBoth
)

func (d scrollDir) vertical() bool   { return d == scrollVertical || d == scrollBoth }
func (d scrollDir) horizontal() bool { return d == scrollHorizontal || d == scrollBoth }

// scrollState is one scroll_view's place in its own content, keyed by widget id
// on the tree.
//
// On the tree and not on the Node, which is the opposite of where transitionState
// lives, and deliberately so: a list is refilled with SetChildren constantly --
// a filtered inventory, a chat log, a quest list -- and a player who has
// scrolled halfway down expects to still be halfway down when one row's text
// changes. A transition is the other case entirely, where a rebuilt dialog must
// start at rest rather than half faded. Same package, opposite requirement, so
// the two pieces of state live in opposite places.
type scrollState struct {
	// X and Y are how far the content is scrolled, in screen pixels, always in
	// [0, max]. Positive means the content has moved up/left under the view.
	X, Y float32

	// The scroll-into-view tween. Meaningless unless active.
	active       bool
	fromX, fromY float32
	toX, toY     float32
	elapsed      float32
}

// stop abandons any tween, leaving the offset wherever it had reached.
//
// Called whenever the player scrolls by hand. A wheel notch or a drag is a
// statement about where the list should be, and continuing to animate towards
// somewhere focus asked for a moment ago would take it straight back.
func (s *scrollState) stop() { s.active = false }

// SetScroll places a scroll_view's content by widget id, abandoning any
// scroll-into-view tween in flight.
//
// The offset is not clamped here, because content size is a layout result and
// layout has not run yet when a game restores a screen it saved. It is clamped
// on the next build instead, against the content that is actually there.
//
// This exists because a game cannot reach the offset any other way and a list
// that forgets where it was every time a screen is rebuilt is a list nobody can
// use. ScrollOffset is the other half.
func (t *WidgetTree) SetScroll(id string, x, y float32) {
	s := t.scrollStateFor(id)
	s.X, s.Y = x, y
	s.stop()
}

// ScrollOffset is where a scroll_view's content currently sits, in screen
// pixels. Zero for an id that is not a scroll_view or has never been built.
func (t *WidgetTree) ScrollOffset(id string) (x, y float32) {
	if s, ok := t.scroll[id]; ok {
		return s.X, s.Y
	}
	return 0, 0
}

// SetContentClipping turns pixel clipping inside scroll_views off and on. On is
// the default and is what a game wants.
//
// Off exists because "the clip is doing the work" is otherwise unfalsifiable
// from outside: two captures of a correctly clipped list differ only inside the
// view, and so do two captures of a list that has no rows near an edge.
// cmd/scrollcheck renders one frame with this off as its control, and it has to
// differ outside the view or the gate is measuring nothing. It is also the
// quickest way to see what a container is hiding while building one.
func (t *WidgetTree) SetContentClipping(on bool) { t.noClip = !on }

// scrollStateFor returns the state for a widget id, creating it on first sight.
func (t *WidgetTree) scrollStateFor(id string) *scrollState {
	if s, ok := t.scroll[id]; ok {
		return s
	}
	s := &scrollState{}
	t.scroll[id] = s
	return s
}

// scrollDirection resolves the widget's `scroll_direction`. Absent is vertical,
// which is what the widget did before it had the field.
func (def *WidgetDef) scrollDirection() scrollDir {
	switch def.ScrollDirection {
	case "horizontal":
		return scrollHorizontal
	case "both":
		return scrollBoth
	default:
		return scrollVertical
	}
}

// dragEnabled resolves `drag:`. A nil pointer is true, so a YAML that says
// nothing gets drag and a def built in Go gets it too -- the schema's other
// defaulted fields read a zero as "not stated" for exactly this reason, and a
// plain bool could not tell "absent" from "drag: false".
func (def *WidgetDef) dragEnabled() bool { return def.Drag == nil || *def.Drag }

// scrollbarVisible reports whether the bar is drawn and hit-testable.
//
// `auto` (the default) shows it only when there is somewhere to scroll, which
// is what the widget always did. `always` shows it even at rest, so a list that
// happens to fit does not lose the affordance that told the player it was a
// list. `never` draws nothing and takes no press -- the whole view is still
// draggable, which is the touch-style case.
func (def *WidgetDef) scrollbarVisible(overflows bool) bool {
	switch def.Scrollbar {
	case "always":
		return true
	case "never":
		return false
	default:
		return overflows
	}
}

// scrollCurve is the curve focus rides in on, and whether it animates at all.
func (def *WidgetDef) scrollCurve() (ease.Func, bool) {
	if def.ScrollEase == "none" {
		return nil, false
	}
	if f, ok := ease.ByName(def.ScrollEase); ok {
		return f, true
	}
	return ease.OutCubic, true
}

// scrollContentSize is how far a scroll_view's laid-out content reaches past
// the near corner of its view rect.
//
// Measured from the resolved rects rather than summed from declared heights,
// which is issue #149's second gap: a child with `flex:` has no declared height
// at all, a child with its own vertical layout has one that says nothing about
// what is inside it, and a summed height is wrong in both cases -- the list
// stops short of its last row, or scrolls past its end into blank space.
//
// The walk descends into containers, because a nested layout that overflows its
// own box still has to be reachable, and stops at a nested scroll_view, because
// that one's overflow is its own business: its content is already confined to
// its rect, and counting it here would make the outer list scroll by the length
// of the inner one.
func scrollContentSize(node *Node, view Rect) (w, h float32) {
	right, bottom := view.X, view.Y
	var walk func(n *Node)
	walk = func(n *Node) {
		for _, c := range n.Children {
			// A hidden child is given the zero rect by layout and consumes no
			// space; counting its corner would drag the content box to the
			// screen origin.
			if c.Rect == (Rect{}) {
				continue
			}
			if r := c.Rect.X + c.Rect.W; r > right {
				right = r
			}
			if b := c.Rect.Y + c.Rect.H; b > bottom {
				bottom = b
			}
			if c.Def.Widget == "scroll_view" {
				continue
			}
			walk(c)
		}
	}
	walk(node)
	return right - view.X, bottom - view.Y
}

// maxScroll is how far a view can scroll on one axis. Never negative: content
// that fits has nowhere to go, and a negative bound would let an offset settle
// above the first row.
func maxScroll(content, view float32) float32 {
	if content <= view {
		return 0
	}
	return content - view
}

func clampf(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// thumbGeometry is where the scrollbar thumb sits for a given axis.
//
// Returned in the same units the caller draws in: along is the thumb's position
// on the scrolling axis and length its size. A view with nowhere to scroll gets
// a full-length thumb at the near end, which is what `scrollbar: always` shows
// for a list that happens to fit.
func thumbGeometry(offset, maxOff, viewLen, contentLen, scale float32) (along, length float32) {
	length = viewLen
	if contentLen > viewLen && contentLen > 0 {
		length = viewLen * (viewLen / contentLen)
	}
	if min := scrollThumbMin * scale; length < min {
		length = min
	}
	if length > viewLen {
		length = viewLen
	}
	if maxOff > 0 {
		along = (offset / maxOff) * (viewLen - length)
	}
	return along, length
}

// dragState is the press this tree is currently following.
//
// One at a time, on the tree rather than per view, because a pointer has one
// button and a press that started in one list cannot also be dragging another.
type dragState struct {
	id     string // the scroll_view's widget id; empty when nothing is dragging
	active bool   // a press is being followed at all (an empty id is a real id)
	thumb  bool   // dragging the bar rather than the content
	live   bool   // past the dead zone, so this is a drag and not a click

	startX, startY float32 // where the button went down
	baseX, baseY   float32 // the view's offset at that moment
}

// scrollInput applies the wheel, a content drag and a thumb drag to one view.
//
// Runs inside the render walk, before the view's children are drawn, so a drag
// that crosses the dead zone can cancel their presses on the same frame it
// becomes a drag -- see cancelPresses.
func (t *WidgetTree) scrollInput(node *Node, st *scrollState, view Rect, contentW, contentH, maxX, maxY, scale float32) {
	def := node.Def
	id := node.ID
	dir := def.scrollDirection()

	// A view under a widget on its way out takes no input, the same rule a
	// button follows: a flick that fell through a closing dialog would scroll
	// whatever the dialog was covering.
	if t.noInput > 0 {
		if t.drag.active && t.drag.id == id {
			t.drag = dragState{}
		}
		return
	}

	inside := node.Rect.Contains(t.input.MouseX, t.input.MouseY)

	if t.input.ScrollY != 0 && inside {
		step := t.input.ScrollY * scrollWheelStep * scale
		// The wheel drives the vertical axis, except on a view that only
		// scrolls sideways -- a tab strip or an inventory row, where the wheel
		// is the only pointing device gesture that could possibly mean it.
		if dir == scrollHorizontal {
			st.X = clampf(st.X-step, 0, maxX)
		} else {
			st.Y = clampf(st.Y-step, 0, maxY)
		}
		st.stop()
	}

	overflowsX, overflowsY := maxX > 0, maxY > 0
	barX := dir.horizontal() && def.scrollbarVisible(overflowsX)
	barY := dir.vertical() && def.scrollbarVisible(overflowsY)

	if t.input.MousePressed && inside && !t.drag.active {
		switch {
		case barY && overflowsY && t.thumbRectY(view, st.Y, maxY, contentH, scale).Contains(t.input.MouseX, t.input.MouseY),
			barX && overflowsX && t.thumbRectX(view, st.X, maxX, contentW, scale).Contains(t.input.MouseX, t.input.MouseY):
			// A press on the thumb is a drag from the first pixel. There is
			// nothing under the thumb to click, so a dead zone there would only
			// make the bar feel stuck.
			t.drag = dragState{
				id: id, active: true, thumb: true, live: true,
				startX: t.input.MouseX, startY: t.input.MouseY,
				baseX: st.X, baseY: st.Y,
			}
			st.stop()
		case def.dragEnabled():
			t.drag = dragState{
				id: id, active: true,
				startX: t.input.MouseX, startY: t.input.MouseY,
				baseX: st.X, baseY: st.Y,
			}
		}
	}

	if t.drag.active && t.drag.id == id {
		dx := t.input.MouseX - t.drag.startX
		dy := t.input.MouseY - t.drag.startY

		if !t.drag.live && dx*dx+dy*dy > sq(scrollDeadZone*scale) {
			t.drag.live = true
			// The press that started this drag is no longer a click on
			// anything. Cancelled here rather than at release, so a row does
			// not sit lit under the finger for the length of the drag.
			t.cancelPresses()
			st.stop()
		}

		if t.drag.live {
			if t.drag.thumb {
				// The thumb moves through the track; the content moves through
				// its own length. One pixel of thumb is therefore
				// maxScroll/(track - thumb) pixels of content.
				if barY && overflowsY {
					_, th := thumbGeometry(st.Y, maxY, view.H, contentH, scale)
					if track := view.H - th; track > 0 {
						st.Y = clampf(t.drag.baseY+dy*(maxY/track), 0, maxY)
					}
				}
				if barX && overflowsX {
					_, tw := thumbGeometry(st.X, maxX, view.W, contentW, scale)
					if track := view.W - tw; track > 0 {
						st.X = clampf(t.drag.baseX+dx*(maxX/track), 0, maxX)
					}
				}
			} else {
				// Content drag: the content follows the pointer, so dragging
				// down reveals what is above. That is the direction every
				// touch surface uses and the opposite of the thumb's.
				if dir.vertical() {
					st.Y = clampf(t.drag.baseY-dy, 0, maxY)
				}
				if dir.horizontal() {
					st.X = clampf(t.drag.baseX-dx, 0, maxX)
				}
			}
		}

		if t.input.MouseReleased || !t.input.MouseDown {
			t.drag = dragState{}
		}
	}
}

func sq(v float32) float32 { return v * v }

// cancelPresses forgets every button press in flight.
//
// A click fires on release only if the same widget recorded the press, so
// dropping the record is what makes a drag swallow the click. Clearing all of
// them rather than only the ones inside the view is correct and simpler: the
// pointer has one button, so a press being followed as a drag IS every press in
// flight.
func (t *WidgetTree) cancelPresses() {
	for id := range t.pressed {
		t.pressed[id] = false
	}
}

// thumbRectY and thumbRectX are the thumb's hit box, which is also exactly
// where it is drawn -- one function for both so a bar that moved cannot become
// a bar you have to press somewhere else.
func (t *WidgetTree) thumbRectY(view Rect, offset, maxOff, contentH, scale float32) Rect {
	along, length := thumbGeometry(offset, maxOff, view.H, contentH, scale)
	w := scrollbarWidth * scale
	return Rect{X: view.X + view.W - w, Y: view.Y + along, W: w, H: length}
}

func (t *WidgetTree) thumbRectX(view Rect, offset, maxOff, contentW, scale float32) Rect {
	along, length := thumbGeometry(offset, maxOff, view.W, contentW, scale)
	h := scrollbarWidth * scale
	return Rect{X: view.X + along, Y: view.Y + view.H - h, W: length, H: h}
}

// advanceScroll moves a running scroll-into-view tween on by one frame.
func (t *WidgetTree) advanceScroll(def *WidgetDef, st *scrollState, maxX, maxY float32) {
	if !st.active {
		return
	}
	curve, animates := def.scrollCurve()
	if !animates {
		st.X, st.Y, st.active = clampf(st.toX, 0, maxX), clampf(st.toY, 0, maxY), false
		return
	}
	st.elapsed += t.dt
	if st.elapsed >= scrollIntoViewDuration {
		st.X, st.Y, st.active = clampf(st.toX, 0, maxX), clampf(st.toY, 0, maxY), false
		return
	}
	e := curve(st.elapsed / scrollIntoViewDuration)
	st.X = clampf(st.fromX+(st.toX-st.fromX)*e, 0, maxX)
	st.Y = clampf(st.fromY+(st.toY-st.fromY)*e, 0, maxY)
}

// scrollFocusIntoView brings the focused widget inside every scroll_view that
// contains it, innermost first.
//
// Innermost first because an outer view can only aim at where the inner one
// leaves the row, and the inner one has not decided that until it has scrolled:
// running outward first aims the outer view at the row's old position and then
// moves it. The rect handed outward is therefore the row where the inner scroll
// puts it, which is a coordinate in the outer view's own content -- the inner
// view's rect is laid out there, and the row is inside it.
//
// Called only when focus CHANGES, not every frame. A view that re-centred its
// focused row on every build could not be scrolled away from with the wheel.
func (t *WidgetTree) scrollFocusIntoView(scale float32) {
	target := t.index[t.focusedID]
	if target == nil {
		return
	}
	path := nodePath(t.Root, target)
	if len(path) == 0 {
		return
	}
	rect := target.Rect
	// From the target's parent outward. path[len-1] is the target itself.
	for i := len(path) - 2; i >= 0; i-- {
		n := path[i]
		if n.Def.Widget != "scroll_view" {
			continue
		}
		rect = t.scrollNodeIntoView(n, rect, scale)
	}
}

// scrollNodeIntoView aims one view at a rect and returns where that rect ends
// up on screen once the view has arrived.
func (t *WidgetTree) scrollNodeIntoView(node *Node, rect Rect, scale float32) Rect {
	def := node.Def
	view := scrollViewRect(node, scale)
	st := t.scrollStateFor(node.ID)
	contentW, contentH := scrollContentSize(node, view)
	dir := def.scrollDirection()

	maxX, maxY := float32(0), float32(0)
	if dir.horizontal() {
		maxX = maxScroll(contentW, view.W)
	}
	if dir.vertical() {
		maxY = maxScroll(contentH, view.H)
	}

	// Aim from where the view is GOING, not from where it is: two focus moves
	// inside one tween would otherwise each measure against a half-finished
	// offset and the second would undershoot.
	fromX, fromY := st.X, st.Y
	if st.active {
		fromX, fromY = st.toX, st.toY
	}

	toX := clampf(minimalScroll(rect.X, rect.W, view.X, view.W, fromX), 0, maxX)
	toY := clampf(minimalScroll(rect.Y, rect.H, view.Y, view.H, fromY), 0, maxY)

	if toX == st.X && toY == st.Y {
		st.active = false
	} else {
		_, animates := def.scrollCurve()
		// A tree with no clock cuts instead of easing, for the same reason a
		// transition without one cuts: dt is zero forever, so an eased scroll
		// would stop at its start and the row focus just moved to would never
		// appear. See WidgetTree.SetTime.
		if !animates || !t.timeSet {
			st.X, st.Y, st.active = toX, toY, false
		} else {
			st.fromX, st.fromY = st.X, st.Y
			st.toX, st.toY = toX, toY
			st.elapsed = 0
			st.active = true
		}
	}

	return Rect{X: rect.X - toX, Y: rect.Y - toY, W: rect.W, H: rect.H}
}

// minimalScroll is the smallest offset change that puts [pos, pos+size] inside
// [viewPos, viewPos+viewSize], given the current offset.
//
// Already inside means no change at all, which is what makes walking down a
// visible list leave the view alone until focus reaches the edge. A target
// taller than the view aligns to its near edge: showing the top of a row you
// just moved onto beats showing its bottom.
func minimalScroll(pos, size, viewPos, viewSize, offset float32) float32 {
	near := pos - offset
	far := near + size
	switch {
	case near < viewPos:
		return pos - viewPos
	case far > viewPos+viewSize && size <= viewSize:
		return pos + size - (viewPos + viewSize)
	case far > viewPos+viewSize:
		return pos - viewPos
	}
	return offset
}

// scrollViewRect is the rect a scroll_view's content is confined to: its own
// rect less its padding.
func scrollViewRect(node *Node, scale float32) Rect {
	pad := node.Def.Padding * scale
	return Rect{
		X: node.Rect.X + pad,
		Y: node.Rect.Y + pad,
		W: node.Rect.W - 2*pad,
		H: node.Rect.H - 2*pad,
	}
}

// nodePath returns root..target inclusive, or nil if target is not in the tree.
func nodePath(root, target *Node) []*Node {
	if root == nil || target == nil {
		return nil
	}
	if root == target {
		return []*Node{root}
	}
	for _, c := range root.Children {
		if p := nodePath(c, target); p != nil {
			return append([]*Node{root}, p...)
		}
	}
	return nil
}

// pushClip narrows the current clip to r and returns what it was, for the
// caller to restore.
//
// A fresh ClipRect per push rather than one reused from a pool: the pointer is
// handed to every render object the subtree emits and those outlive this
// function by a frame, so a pooled rect would rewrite last frame's draws. It is
// one small allocation per scroll_view per frame, not one per object -- the
// objects share the pointer.
func (t *WidgetTree) pushClip(r Rect) *renderer.ClipRect {
	prev := t.clip
	if t.noClip {
		return prev
	}
	next := renderer.ClipRect{X: r.X, Y: r.Y, W: r.W, H: r.H}
	if prev != nil {
		next = prev.Intersect(next)
	}
	t.clip = &next
	return prev
}
