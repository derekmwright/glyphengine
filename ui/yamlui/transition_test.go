package yamlui

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/derekmwright/glyphengine/renderer"
)

// ── harness ──

// drawLog records the rect and the opacity every builder callback was handed.
//
// The rect has to be captured here rather than read off the Node afterwards:
// renderNode puts the untransformed rects back the moment the subtree is drawn,
// which is the property that keeps hit boxes and layout in agreement, and it
// means node.Rect after a build is the layout rect and says nothing about where
// the widget was painted.
type drawLog struct {
	kinds   []string
	rects   []Rect
	opacity []float32
}

func (l *drawLog) reset() {
	l.kinds, l.rects, l.opacity = nil, nil, nil
}

// install wires the three callbacks. The nine-slice path is the one that
// reports a rect directly; the shape path reports the bounds of the geometry it
// was handed, shrunk by the half-pixel skirt appendQuad grows every flat quad
// by, so both report the rect the YAML asked for.
func (l *drawLog) install(tree *WidgetTree) {
	tree.PanelFn = func(r *renderer.Renderer, slice *renderer.NineSlice, color [3]float32, opacity float32, x, y, w, h, scale, sw, sh float32) []renderer.UIRenderObject {
		l.kinds = append(l.kinds, "panel")
		l.rects = append(l.rects, Rect{X: x, Y: y, W: w, H: h})
		l.opacity = append(l.opacity, opacity)
		return []renderer.UIRenderObject{{RenderObject: renderer.RenderObject{Color: color}, Opacity: opacity}}
	}
	tree.IconFn = func(name string, color [3]float32, opacity float32, x, y, w, h, sw, sh float32) []renderer.UIRenderObject {
		l.kinds = append(l.kinds, "sprite")
		l.rects = append(l.rects, Rect{X: x, Y: y, W: w, H: h})
		l.opacity = append(l.opacity, opacity)
		return []renderer.UIRenderObject{{RenderObject: renderer.RenderObject{Color: color}, Opacity: opacity, TextureMode: true}}
	}
	tree.ShapeFn = func(verts []renderer.Vertex, idxs []uint16, opacity float32) []renderer.UIRenderObject {
		l.kinds = append(l.kinds, "shape")
		l.rects = append(l.rects, vertBounds(verts))
		l.opacity = append(l.opacity, opacity)
		var col [3]float32
		if len(verts) > 0 {
			col = verts[0].Color
		}
		return []renderer.UIRenderObject{{RenderObject: renderer.RenderObject{Color: col}, Opacity: opacity}}
	}
}

// vertBounds recovers the requested rect from a quad's vertices. appendQuad
// emits every flat quad half a pixel larger on each side so ui.frag has an
// outside half to ramp its coverage over; undoing that here keeps the numbers
// in these tests the ones the YAML wrote.
func vertBounds(verts []renderer.Vertex) Rect {
	if len(verts) == 0 {
		return Rect{}
	}
	lo := [2]float32{verts[0].Pos[0], verts[0].Pos[1]}
	hi := lo
	for _, v := range verts[1:] {
		for i := 0; i < 2; i++ {
			if v.Pos[i] < lo[i] {
				lo[i] = v.Pos[i]
			}
			if v.Pos[i] > hi[i] {
				hi[i] = v.Pos[i]
			}
		}
	}
	return Rect{X: lo[0] + edgeSkirt, Y: lo[1] + edgeSkirt, W: hi[0] - lo[0] - 2*edgeSkirt, H: hi[1] - lo[1] - 2*edgeSkirt}
}

// clock drives a tree the way a game does: bindings, then the unscaled time,
// then a build.
type clock struct {
	tree   *WidgetTree
	assets *AssetProvider
	log    *drawLog
	now    float32
}

func newClock(t *testing.T, src string) *clock {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ui.yaml")
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	tree, err := Load(os.DirFS(dir), "ui.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	log := &drawLog{}
	log.install(tree)
	return &clock{
		tree:   tree,
		assets: &AssetProvider{NineSlices: map[string]*renderer.NineSlice{"slot": {}}},
		log:    log,
	}
}

type frame struct {
	panels []renderer.UIRenderObject
	verts  []renderer.Vertex
	idxs   []uint16
	text   []renderer.TextLine
}

// step advances the clock by dt and builds one frame. dt 0 is the first frame,
// which every transition uses to learn where it starts.
func (c *clock) step(dt float32) frame {
	c.now += dt
	c.log.reset()
	c.tree.SetTime(c.now)
	p, v, i, tx := c.tree.BuildAt(nil, c.assets, 0, 0, 1, 400, 400)
	return frame{p, v, i, tx}
}

func (c *clock) open(v bool) { c.tree.Bind("open", fmt.Sprintf("%t", v)) }

// drawnRect returns the rect the nth logged draw was made at.
func (c *clock) drawnRect(n int) Rect {
	if n >= len(c.log.rects) {
		return Rect{}
	}
	return c.log.rects[n]
}

func near(a, b, tol float32) bool { return math.Abs(float64(a-b)) <= float64(tol) }

func rectClose(a, b Rect, tol float32) bool {
	return near(a.X, b.X, tol) && near(a.Y, b.Y, tol) && near(a.W, b.W, tol) && near(a.H, b.H, tol)
}

// ── the curves, pinned at the midpoint ──

// dialogYAML is one nine-slice panel that scales, slides and fades, inside a
// root that draws nothing. The nine-slice path is used because PanelFn is
// handed x, y, w, h and an opacity directly, so what is asserted below is what
// the host would draw rather than a reconstruction of it.
const dialogYAML = `widget: panel
id: root
width: 200
height: 200
children:
  - widget: panel
    id: dialog
    nine_slice: slot
    width: 100
    height: 40
    visible: "{open}"
    transition:
      duration: 0.2
      ease: %s
      scale: 0.5
      offset_x: 20
      offset_y: -12
`

// Layout puts the dialog at (0, 0) 100x40 -- no padding, no gap, an explicit
// width -- so its anchor is (50, 20).
var dialogRect = Rect{X: 0, Y: 0, W: 100, H: 40}

// TestTransitionMidpointForEveryCurve pins opacity, scale and offset halfway
// through a 0.2s in-transition, for every curve in the set.
//
// The eased values are the hand-computed midpoints from the standard formulas,
// written here rather than obtained from ui/ease: a midpoint read out of the
// package under test would agree with any curve that package got wrong.
//
//	linear 0.5   in_quad 0.25    out_quad 0.75     in_out_quad 0.5
//	in_cubic 0.125   out_cubic 0.875   in_out_cubic 0.5   out_back 1.0876975
//
// out_back is the one above 1: at the midpoint the dialog is already larger
// than its rect and past its resting position, which is what a "back" curve
// is for, while its opacity is clamped to fully opaque because there is
// nothing above that.
func TestTransitionMidpointForEveryCurve(t *testing.T) {
	cases := []struct {
		name  string
		eased float32
	}{
		{"linear", 0.5},
		{"in_quad", 0.25},
		{"out_quad", 0.75},
		{"in_out_quad", 0.5},
		{"in_cubic", 0.125},
		{"out_cubic", 0.875},
		{"in_out_cubic", 0.5},
		{"out_back", 1.0876975},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClock(t, fmt.Sprintf(dialogYAML, tc.name))

			// Closed on the first build, so the flip below happens at t = 0
			// from a widget that is genuinely hidden.
			c.open(false)
			if f := c.step(0); len(f.panels) != 0 {
				t.Fatalf("a closed dialog drew %d objects on the first frame", len(f.panels))
			}

			c.open(true)
			f := c.step(0.1) // duration/2
			if len(f.panels) != 1 {
				t.Fatalf("midway through the in, the dialog drew %d objects, want 1", len(f.panels))
			}

			e := tc.eased
			factor := 0.5 + e*0.5 // scale: 0.5 .. 1
			cx, cy := dialogRect.X+dialogRect.W/2, dialogRect.Y+dialogRect.H/2
			want := Rect{
				X: cx + (dialogRect.X-cx)*factor + 20*(1-e),
				Y: cy + (dialogRect.Y-cy)*factor + -12*(1-e),
				W: dialogRect.W * factor,
				H: dialogRect.H * factor,
			}
			if got := c.drawnRect(0); !rectClose(got, want, 1e-4) {
				t.Errorf("rect = %+v, want %+v (eased %v, factor %v)", got, want, e, factor)
			}

			wantOpacity := e
			if wantOpacity > 1 {
				wantOpacity = 1
			}
			if got := c.log.opacity[0]; !near(got, wantOpacity, 1e-6) {
				t.Errorf("opacity = %v, want %v", got, wantOpacity)
			}
		})
	}
}

// ── the frame past the end ──

// plainDialogYAML is dialogYAML with the block deleted and nothing else
// changed, which is what "equals a widget with no transition" is measured
// against.
const plainDialogYAML = `widget: panel
id: root
width: 200
height: 200
children:
  - widget: panel
    id: dialog
    nine_slice: slot
    width: 100
    height: 40
    visible: "{open}"
`

// TestFramePastTheEndMatchesNoTransition is the byte-for-byte end of the
// contract: once the in has finished, a transitioning widget is on exactly the
// draw path a widget with no block is on -- same render objects, same vertex
// stream, same text -- so nothing downstream can tell them apart and a
// screenshot of a settled HUD is unchanged by adding the block.
//
// The control in the middle is not decoration: if the two trees agreed at the
// midpoint as well, they would be agreeing because the transition never ran.
//
// It does NOT uniquely guard renderNode's atRest fast path, and that was
// measured rather than assumed: stubbing atRest to false leaves this test, the
// rest of the package and `task transition` green, because at progress 1 the
// transform is the identity in float32 arithmetic. See atRest's comment for the
// numbers. What this test does guard is everything downstream of the transform
// -- the diverted flat quads, the text alpha, the opacity multiply -- coming
// back to exactly where a plain tree is.
func TestFramePastTheEndMatchesNoTransition(t *testing.T) {
	withBlock := newClock(t, fmt.Sprintf(dialogYAML, "out_cubic"))
	plain := newClock(t, plainDialogYAML)

	run := func(c *clock) (mid, past frame) {
		c.open(false)
		c.step(0)
		c.open(true)
		mid = c.step(0.1)
		// Three more frames of 0.05: 0.25s, comfortably past the 0.2s duration.
		c.step(0.05)
		c.step(0.05)
		past = c.step(0.05)
		return mid, past
	}

	midA, pastA := run(withBlock)
	midB, pastB := run(plain)

	if reflect.DeepEqual(midA, midB) {
		t.Fatal("the two trees agree halfway through the transition, so the transition is not running and this test proves nothing")
	}
	if !reflect.DeepEqual(pastA.panels, pastB.panels) {
		t.Errorf("past the duration the render objects differ:\n with %+v\n without %+v", pastA.panels, pastB.panels)
	}
	if !reflect.DeepEqual(pastA.verts, pastB.verts) || !reflect.DeepEqual(pastA.idxs, pastB.idxs) {
		t.Errorf("past the duration the vertex stream differs:\n with %d verts %d idxs\n without %d verts %d idxs",
			len(pastA.verts), len(pastA.idxs), len(pastB.verts), len(pastB.idxs))
	}
	if !reflect.DeepEqual(pastA.text, pastB.text) {
		t.Errorf("past the duration the text differs:\n with %+v\n without %+v", pastA.text, pastB.text)
	}
	if got, want := withBlock.drawnRect(0), plain.drawnRect(0); got != want {
		t.Errorf("past the duration the dialog is drawn at %+v, want its layout rect %+v", got, want)
	}
}

// ── reversal ──

// fadeOnlyYAML fades and does nothing else, so a rect never moves and a test
// about input or progress cannot be answered by geometry.
const fadeOnlyYAML = `widget: panel
id: root
width: 200
height: 200
children:
  - widget: button
    id: confirm
    nine_slice: slot
    width: 100
    height: 40
    text: OK
    font_size: 12
    on_click: confirm
    visible: "{open}"
    transition:
      duration: 0.2
`

// TestOutReversesFromMidProgress is the issue's "flipping mid-transition
// reverses from the current progress, not from the start".
//
// The dialog is flipped open, run to exactly half of a 0.2s in, then flipped
// shut. Progress must descend from 0.5 -- 0.4, 0.3, 0.2 -- and reach 0 four
// frames later, taking half the duration because it was only half in.
//
// BROKEN ONCE to prove it: transitionState.advance reset progress to 1 when
// visible went false, which is the obvious way to write "play the out". The
// package test printed
//
//	--- FAIL: TestOutReversesFromMidProgress
//	    the first out frame is at progress 0.9, which is above the 0.5 it
//	    reversed from -- the out restarted instead of reversing
//	    out frame 1: progress 0.9, want 0.4
//	    out frame 2: progress 0.8, want 0.3
//	    out frame 3: progress 0.70000005, want 0.2
//	    out frame 4: progress 0.6000001, want 0.1
//	    out frame 5: progress 0.5000001, want 0
//	    the dialog is still drawn 5 frames after a half-finished in was
//	    reversed; the out is taking longer than the in got
func TestOutReversesFromMidProgress(t *testing.T) {
	c := newClock(t, fadeOnlyYAML)
	node := c.tree.NodeByID("confirm")

	c.open(false)
	c.step(0)
	c.open(true)
	c.step(0.1)

	if got := node.trans.progress; !near(got, 0.5, 1e-5) {
		t.Fatalf("after 0.1s of a 0.2s in the progress is %v, want 0.5; the rest of this test is about what happens from there", got)
	}

	c.open(false)
	want := []float32{0.4, 0.3, 0.2, 0.1, 0}
	for i, w := range want {
		f := c.step(0.02)
		got := node.trans.progress
		if i == 0 && got >= 0.5 {
			t.Errorf("the first out frame is at progress %v, which is above the 0.5 it reversed from -- the out restarted instead of reversing", got)
		}
		if !near(got, w, 1e-5) {
			t.Errorf("out frame %d: progress %v, want %v", i+1, got, w)
		}
		if w == 0 && len(f.panels) != 0 {
			t.Errorf("the dialog is still drawn %d frames after a half-finished in was reversed; the out is taking longer than the in got", i+1)
		}
		if w > 0 && len(f.panels) == 0 {
			t.Errorf("out frame %d drew nothing at progress %v; the widget is skipped before its out has finished", i+1, got)
		}
	}
}

// The out is skipped only after it finishes, and an in reversed straight back
// to visible continues up from where it was rather than restarting.
func TestReversalWorksInBothDirections(t *testing.T) {
	c := newClock(t, fadeOnlyYAML)
	node := c.tree.NodeByID("confirm")

	c.open(false)
	c.step(0)
	c.open(true)
	c.step(0.2) // fully in
	c.open(false)
	c.step(0.05) // a quarter of the way out: 0.75
	if got := node.trans.progress; !near(got, 0.75, 1e-5) {
		t.Fatalf("progress = %v after a quarter of the out, want 0.75", got)
	}
	c.open(true)
	c.step(0.05)
	if got := node.trans.progress; !near(got, 1, 1e-5) {
		t.Errorf("reversing a quarter-finished out took progress to %v, want it back at 1", got)
	}
}

// ── input ──

// pressRelease clicks at (x, y) over two frames, which is what the widget tree
// requires: a press latches and the release fires.
func pressRelease(c *clock, x, y, dt float32) []UIEvent {
	c.tree.SetInput(InputState{MouseX: x, MouseY: y, MousePressed: true, MouseDown: true})
	c.step(dt)
	c.tree.SetInput(InputState{MouseX: x, MouseY: y, MouseReleased: true})
	c.step(dt)
	ev := c.tree.DrainEvents()
	c.tree.SetInput(InputState{})
	return ev
}

// TestInputIsRefusedFromTheFirstOutFrame is the issue's dismiss-fall-through
// case: a dialog stops taking clicks the frame `visible` flips false, not when
// its fade ends, so the click that closed it cannot also hit what was under it.
// An in-transitioning widget takes input normally.
//
// The transition here only fades, so the button's rect never moves and neither
// arm can be answered by the pointer having missed.
//
// BROKEN ONCE to prove it: renderNode's `if !vis { t.noInput++ }` deleted, so a
// widget on its way out went on hit-testing. The package test printed
//
//	--- FAIL: TestInputIsRefusedFromTheFirstOutFrame
//	    the first out frame produced [{click confirm confirm}]; a dialog on its
//	    way out must not take the click that dismissed it
//	    a later out frame produced [{click confirm confirm}] too
//	--- FAIL: TestOutTransitionDropsTheHover
//	    a button on its way out still reports as hovered
//
// and nothing else in the package moved, which is the point: every other test
// here is about pixels and progress, and none of them can see a click. The
// hover arm firing with it is the same hit test read a second way.
func TestInputIsRefusedFromTheFirstOutFrame(t *testing.T) {
	// The button lands at (0, 0) 100x40, so (50, 20) is its middle.
	const hitX, hitY = 50, 20

	c := newClock(t, fadeOnlyYAML)
	c.open(false)
	c.step(0)

	// In-transition: halfway through the fade, the button still works.
	c.open(true)
	c.step(0.05)
	if ev := pressRelease(c, hitX, hitY, 0.02); len(ev) != 1 || ev[0].NodeID != "confirm" {
		t.Errorf("a click during the in produced %v, want one click on confirm", ev)
	}

	// Fully in, then dismissed. From the very first out frame, nothing.
	c.step(0.2)
	c.open(false)
	if ev := pressRelease(c, hitX, hitY, 0.02); len(ev) != 0 {
		t.Errorf("the first out frame produced %v; a dialog on its way out must not take the click that dismissed it", ev)
	}
	if ev := pressRelease(c, hitX, hitY, 0.02); len(ev) != 0 {
		t.Errorf("a later out frame produced %v too", ev)
	}

	// And the vacuity guard: the widget was still being drawn on those frames.
	// Without this, deleting the widget entirely would pass the two arms above.
	c2 := newClock(t, fadeOnlyYAML)
	c2.open(false)
	c2.step(0)
	c2.open(true)
	c2.step(0.2)
	c2.open(false)
	if f := c2.step(0.02); len(f.panels) == 0 {
		t.Fatal("the button is not drawn on its first out frame, so refusing its clicks proves nothing")
	}
}

// A widget that is merely hovered while leaving must not light up either: the
// hover tint is drawn from the same hit test the click is.
func TestOutTransitionDropsTheHover(t *testing.T) {
	c := newClock(t, fadeOnlyYAML)
	c.open(false)
	c.step(0)
	c.open(true)
	c.step(0.2)

	c.tree.SetInput(InputState{MouseX: 50, MouseY: 20})
	c.step(0.02)
	if !c.tree.hovered["confirm"] {
		t.Fatal("the pointer is not over the button while it is at rest, so the next assertion proves nothing")
	}

	c.open(false)
	c.step(0.02)
	if c.tree.hovered["confirm"] {
		t.Error("a button on its way out still reports as hovered")
	}
}

// ── layout isolation ──

// siblingsYAML stacks three fixed-height panels. The middle one carries a
// transition big enough that any leak into layout moves the third.
const siblingsYAML = `widget: panel
id: root
width: 200
height: 300
padding: 10
gap: 8
children:
  - widget: panel
    id: above
    nine_slice: slot
    height: 40
  - widget: panel
    id: middle
    nine_slice: slot
    height: 60
    visible: "{open}"
%s
  - widget: panel
    id: below
    nine_slice: slot
    height: 40
`

const siblingsBlock = `    transition:
      duration: 0.2
      ease: out_back
      scale: 0.6
      offset_x: 30
      offset_y: -40
`

// TestSiblingLayoutIsIdenticalWithAndWithoutTheBlock is "transitions never
// affect layout", measured where it can actually go wrong: on the rects the
// siblings are DRAWN at, halfway through the middle panel's transition. Reading
// the Nodes after the build would prove nothing -- the rects are restored by
// then -- so the comparison is between what the builder callbacks were handed.
//
// BROKEN ONCE to prove it: layoutVerticalInner advanced its cursor by the
// child's transition offset --
//
//	cursorY += childH + gap
//	if child.Def.Transition != nil { cursorY += child.Def.Transition.OffsetY }
//
// which is the shape of the mistake this rules out: a transition read during
// layout instead of after it. The package test printed
//
//	--- FAIL: TestSiblingLayoutIsIdenticalWithAndWithoutTheBlock
//	    midway: sibling "below" is drawn at {X:10 Y:86 W:180 H:40} with the
//	    block and {X:10 Y:126 W:180 H:40} without it; the transition reached layout
//	    past the end: sibling "below" is drawn at {X:10 Y:86 W:180 H:40} with the
//	    block and {X:10 Y:126 W:180 H:40} without it; the transition reached layout
//
// Both arms fired, which is right: a layout leak is there whether the widget is
// transitioning or at rest. The panel ABOVE the transitioning one stayed put in
// both, which is what says the leak is the cursor and not the whole stack.
func TestSiblingLayoutIsIdenticalWithAndWithoutTheBlock(t *testing.T) {
	withBlock := newClock(t, fmt.Sprintf(siblingsYAML, siblingsBlock))
	plain := newClock(t, fmt.Sprintf(siblingsYAML, ""))

	// Draw order is above, middle, below, so index 1 is the transitioning one.
	const above, middle, below = 0, 1, 2

	settle := func(c *clock) {
		c.open(false)
		c.step(0)
		c.open(true)
	}
	settle(withBlock)
	settle(plain)

	check := func(name string) {
		t.Helper()
		for _, s := range []struct {
			id string
			n  int
		}{{"above", above}, {"below", below}} {
			got, want := withBlock.drawnRect(s.n), plain.drawnRect(s.n)
			if got != want {
				t.Errorf("%s: sibling %q is drawn at %+v with the block and %+v without it; the transition reached layout",
					name, s.id, got, want)
			}
		}
	}

	withBlock.step(0.1)
	plain.step(0.1)
	check("midway")
	// The control: the transitioning widget itself HAS to differ, or the three
	// rects above are equal because nothing is transitioning.
	if withBlock.drawnRect(middle) == plain.drawnRect(middle) {
		t.Fatalf("the middle panel is drawn at %+v either way; the transition is not running and this test proves nothing",
			plain.drawnRect(middle))
	}

	withBlock.step(0.2)
	plain.step(0.2)
	check("past the end")
	if got, want := withBlock.drawnRect(middle), plain.drawnRect(middle); got != want {
		t.Errorf("past the end the middle panel is drawn at %+v, want its layout rect %+v", got, want)
	}
}

// A widget on its way out keeps its layout box until the out finishes, so it
// has somewhere to fade; siblings close up when it is gone rather than on the
// frame `visible` flipped. This is the one place a transition and layout meet,
// and it is written down here so a change to it is a changed test rather than a
// surprise.
func TestOutTransitionHoldsItsLayoutBoxUntilItFinishes(t *testing.T) {
	c := newClock(t, fmt.Sprintf(siblingsYAML, siblingsBlock))
	c.open(false)
	c.step(0)
	c.open(true)
	c.step(0.2)

	held := c.drawnRect(2) // "below", while the middle panel is at rest

	c.open(false)
	c.step(0.02)
	if got := c.drawnRect(2); got != held {
		t.Errorf("a sibling moved to %+v on the first out frame, want it held at %+v until the out finishes", got, held)
	}

	// Once the out is over, the middle panel is gone and the layout closes up.
	c.step(0.2)
	if got := c.drawnRect(1); got == held || got.Y >= held.Y {
		t.Errorf("after the out finished, the second drawn panel is at %+v; the layout did not close up", got)
	}
}

// ── first build ──

// TestAlreadyVisibleAtFirstBuildStartsAtRest: a HUD whose panels are visible
// from the start must not fade in on the first frame of every level load. The
// widget begins at rest, and only a LATER flip plays anything.
func TestAlreadyVisibleAtFirstBuildStartsAtRest(t *testing.T) {
	c := newClock(t, fmt.Sprintf(dialogYAML, "out_cubic"))
	plain := newClock(t, plainDialogYAML)

	c.open(true)
	plain.open(true)
	c.step(0)
	plain.step(0)

	if got, want := c.drawnRect(0), plain.drawnRect(0); got != want {
		t.Errorf("first build: drawn at %+v, want the untransformed %+v", got, want)
	}
	if got := c.log.opacity[0]; got != 1 {
		t.Errorf("first build: opacity %v, want 1", got)
	}

	// The control: the same tree, starting closed, does fade when it opens.
	c2 := newClock(t, fmt.Sprintf(dialogYAML, "out_cubic"))
	c2.open(false)
	c2.step(0)
	c2.open(true)
	c2.step(0.05)
	if c2.log.opacity[0] >= 1 {
		t.Fatalf("a dialog that opened after the first build is already opaque (%v); this test cannot tell a starting state from a missing transition",
			c2.log.opacity[0])
	}
}

// Without a clock a transition is an instant cut. A host that has not wired
// SetTime gets the behaviour it had before the block existed, rather than a
// dialog stuck at the start of a fade it can never finish.
func TestWithoutAClockTransitionsCut(t *testing.T) {
	c := newClock(t, fmt.Sprintf(dialogYAML, "out_cubic"))
	plain := newClock(t, plainDialogYAML)

	build := func(c *clock, open bool) frame {
		c.open(open)
		c.log.reset()
		p, v, i, tx := c.tree.BuildAt(nil, c.assets, 0, 0, 1, 400, 400)
		return frame{p, v, i, tx}
	}

	build(c, false)
	build(plain, false)
	a, b := build(c, true), build(plain, true)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("with no clock the transitioning tree drew %+v, want the untransitioned %+v", a, b)
	}
	if got := c.drawnRect(0); got != plain.drawnRect(0) {
		t.Errorf("with no clock the dialog is drawn at %+v, want %+v", got, plain.drawnRect(0))
	}
}

// ── out curve ──

// `out:` names a separate curve for the departure; without it the out is the in
// curve read at a descending progress, which is the same shape played back.
func TestOutCurveIsSeparateWhenNamed(t *testing.T) {
	// At progress 0.5 going out:
	//   default (in curve out_quad played back): out_quad(0.5)      = 0.75
	//   out: in_quad                            : 1 - in_quad(0.5)  = 0.75
	// -- equal, which would make this test vacuous, so the pair below is
	// out_quad in and out_cubic out:
	//   default : out_quad(0.5)      = 0.75
	//   named   : 1 - out_cubic(0.5) = 0.125
	const src = `widget: panel
id: root
width: 200
height: 200
children:
  - widget: panel
    id: dialog
    nine_slice: slot
    width: 100
    height: 40
    visible: "{open}"
    transition:
      duration: 0.2
      ease: out_quad
%s
`
	run := func(block string) float32 {
		c := newClock(t, fmt.Sprintf(src, block))
		c.open(false)
		c.step(0)
		c.open(true)
		c.step(0.2)
		c.open(false)
		c.step(0.1) // progress 0.5
		return c.log.opacity[0]
	}

	if got := run(""); !near(got, 0.75, 1e-5) {
		t.Errorf("with no out curve, the opacity at half progress is %v, want out_quad(0.5) = 0.75", got)
	}
	if got := run("      out: out_cubic\n"); !near(got, 0.125, 1e-5) {
		t.Errorf("with out: out_cubic, the opacity at half progress is %v, want 1 - out_cubic(0.5) = 0.125", got)
	}
}

// ── state lifetime ──

// Transition state lives on the Node, so replacing a subtree through
// SetChildren discards it: a list rebuilt while an entry was fading does not
// bring the fade back with it.
func TestRebuildingTheTreeResetsTransitionState(t *testing.T) {
	c := newClock(t, fadeOnlyYAML)
	c.open(false)
	c.step(0)
	c.open(true)
	c.step(0.1)
	if got := c.tree.NodeByID("confirm").trans.progress; !near(got, 0.5, 1e-5) {
		t.Fatalf("progress = %v before the rebuild, want 0.5", got)
	}

	c.tree.SetChildren("root", []WidgetDef{{
		Widget:     "button",
		ID:         "confirm",
		NineSlice:  "slot",
		Width:      100,
		Height:     40,
		Visible:    "{open}",
		Transition: &TransitionDef{Duration: 0.2, Opacity: true},
	}})

	c.step(0.02)
	if got := c.tree.NodeByID("confirm").trans.progress; got != 1 {
		t.Errorf("progress = %v on the first build after a rebuild, want 1 (at rest, not resumed)", got)
	}
}

// ── flat quads ──

// A `bg_color` panel has no opacity to carry -- renderer.Vertex has no alpha --
// so a fading one is handed to ShapeFn instead of the shared vertex stream, and
// goes back into the stream the moment it is at rest.
func TestFadingFlatQuadGoesThroughTheShapeBuilder(t *testing.T) {
	const src = `widget: panel
id: root
width: 200
height: 200
children:
  - widget: panel
    id: dialog
    width: 100
    height: 40
    bg_color: [0.2, 0.3, 0.4]
    visible: "{open}"
    transition:
      duration: 0.2
      scale: 0.5
`
	c := newClock(t, src)
	c.open(false)
	c.step(0)
	c.open(true)

	f := c.step(0.1)
	if len(f.verts) != 0 {
		t.Errorf("a fading flat quad put %d vertices in the shared stream, where they would be drawn opaque", len(f.verts))
	}
	if len(c.log.kinds) != 1 || c.log.kinds[0] != "shape" {
		t.Fatalf("builders called: %v, want one shape", c.log.kinds)
	}
	if got := c.log.opacity[0]; !near(got, 0.5, 1e-5) {
		t.Errorf("the faded quad was handed opacity %v, want 0.5", got)
	}
	// eased 0.5, so the factor is 0.5 + 0.5*0.5 = 0.75 about the anchor (50, 20).
	if got := c.log.rects[0]; !rectClose(got, Rect{X: 12.5, Y: 5, W: 75, H: 30}, 1e-4) {
		t.Errorf("the faded quad covers %+v, want the scaled {12.5 5 75 30}", got)
	}

	f = c.step(0.2)
	if len(f.verts) != 4 {
		t.Errorf("at rest the flat quad put %d vertices in the shared stream, want 4", len(f.verts))
	}
	if len(c.log.kinds) != 0 {
		t.Errorf("at rest the flat quad still went through a builder: %v", c.log.kinds)
	}
}

// Text fades with the widget it is on. TextLine reads Alpha 0 as opaque, so a
// settled line has to write exactly 0 and a faded one something that is not.
func TestTextFadesWithTheWidget(t *testing.T) {
	c := newClock(t, fadeOnlyYAML)
	c.open(false)
	c.step(0)
	c.open(true)

	f := c.step(0.1)
	if len(f.text) != 1 {
		t.Fatalf("drew %d text lines, want 1", len(f.text))
	}
	if got := f.text[0].Alpha; !near(got, 0.5, 1e-5) {
		t.Errorf("text alpha halfway through the fade = %v, want 0.5", got)
	}

	f = c.step(0.2)
	if got := f.text[0].Alpha; got != 0 {
		t.Errorf("text alpha at rest = %v, want exactly 0 (which TextLine reads as opaque)", got)
	}
}

// ── errors ──

// Every transition error is a load error and names the widget, the same way the
// indicator block's are: a misspelled key or curve renders plausible motion
// that nobody can distinguish from a deliberate choice.
func TestTransitionErrors(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			"unknown key",
			"widget: panel\nid: dlg\ntransition:\n  duration: 0.2\n  offset: 4\n",
			`widget "dlg": transition: unknown key "offset"`,
		},
		{
			"missing duration",
			"widget: panel\nid: dlg\ntransition:\n  ease: out_cubic\n",
			`widget "dlg": transition: duration is required and must be positive, got 0`,
		},
		{
			"negative duration",
			"widget: panel\nid: dlg\ntransition:\n  duration: -1\n",
			`widget "dlg": transition: duration is required and must be positive, got -1`,
		},
		{
			"unknown ease",
			"widget: panel\nid: dlg\ntransition:\n  duration: 0.2\n  ease: out_bounce\n",
			`widget "dlg": transition: unknown ease "out_bounce" (want linear, in_quad,`,
		},
		{
			"unknown out",
			"widget: panel\nid: dlg\ntransition:\n  duration: 0.2\n  out: elastic\n",
			`widget "dlg": transition: unknown out "elastic" (want linear, in_quad,`,
		},
		{
			"zero scale",
			"widget: panel\nid: dlg\ntransition:\n  duration: 0.2\n  scale: 0\n",
			`widget "dlg": transition: scale must be positive, got 0`,
		},
		{
			"negative scale",
			"widget: panel\nid: dlg\ntransition:\n  duration: 0.2\n  scale: -0.5\n",
			`widget "dlg": transition: scale must be positive, got -0.5`,
		},
		{
			"not a mapping",
			"widget: panel\nid: dlg\ntransition: out_cubic\n",
			"transition: want a mapping, got a scalar",
		},
		{
			"a child is named too",
			"widget: panel\nid: root\nchildren:\n  - widget: icon\n    sprite: x\n    transition:\n      duration: 0.2\n      ease: bogus\n",
			`widget of type "icon" with no id: transition: unknown ease "bogus"`,
		},
		{
			"indicator opacity ease",
			"widget: icon\nid: slot\nsprite: x\nindicator:\n  type: tint\n  value: \"{v}\"\n  max: \"{m}\"\n  opacity: { start: 1, end: 0, ease: swoosh }\n",
			`widget "slot": indicator: opacity: unknown ease "swoosh"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseYAML(t, tc.src)
			if err == nil {
				t.Fatalf("parsed without error, want %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// The defaults the YAML path applies, which a Go-built block never sees.
func TestTransitionDefaults(t *testing.T) {
	def := mustParse(t, `widget: panel
id: dlg
transition:
  duration: 0.25
`)
	d := def.Transition
	if d == nil {
		t.Fatal("transition is nil")
	}
	if !d.Opacity {
		t.Error("opacity defaulted to false; a transition with no `opacity:` key fades")
	}
	if d.Scale != 1 {
		t.Errorf("scale = %v, want 1 (no scale)", d.Scale)
	}
	if d.OffsetX != 0 || d.OffsetY != 0 {
		t.Errorf("offsets = %v, %v, want 0", d.OffsetX, d.OffsetY)
	}
	if got := d.inCurve()(0.5); got != 0.5 {
		t.Errorf("an absent ease evaluated to %v at 0.5, want linear's 0.5", got)
	}
	if _, ok := d.outCurve(); ok {
		t.Error("an absent out named a curve")
	}
}

// A block assembled in Go through SetChildren runs no unmarshaller, so its
// zero-value Scale has to read as "no scale" rather than collapsing the widget
// to nothing forever. Same bargain the indicator's all-zero opacity makes.
func TestGoBuiltTransitionZeroScaleIsNoScale(t *testing.T) {
	d := &TransitionDef{Duration: 0.2}
	if got := d.scaleFrom(); got != 1 {
		t.Errorf("scaleFrom() = %v on a Go-built block, want 1", got)
	}
	// And a scale someone typed is still honoured.
	d.Scale = 0.8
	if got := d.scaleFrom(); got != 0.8 {
		t.Errorf("scaleFrom() = %v, want 0.8", got)
	}
}

// ── indicator ease ──

// indicator.opacity.ease shapes the ramp on frac, so a cooldown that runs down
// linearly can still lose its alpha on a curve.
func TestIndicatorOpacityEase(t *testing.T) {
	linear := &IndicatorDef{Type: IndicatorRoll, Direction: "down", Opacity: IndicatorOpacity{Start: 1, End: 0}}
	eased := &IndicatorDef{Type: IndicatorRoll, Direction: "down", Opacity: IndicatorOpacity{Start: 1, End: 0, Ease: "in_quad"}}

	if got := linear.alpha(0.5); !near(got, 0.5, 1e-6) {
		t.Errorf("no ease: alpha(0.5) = %v, want 0.5", got)
	}
	// in_quad(0.5) = 0.25, so the ramp is a quarter of the way up rather than
	// half -- a cooldown that has already gone quiet by its midpoint.
	if got := eased.alpha(0.5); !near(got, 0.25, 1e-6) {
		t.Errorf("in_quad: alpha(0.5) = %v, want 0.25", got)
	}
	// The endpoints are untouched, which is what keeps `frac 0 draws nothing`
	// true whatever curve is named.
	if got := eased.alpha(0); got != 0 {
		t.Errorf("in_quad: alpha(0) = %v, want 0", got)
	}
	if got := eased.alpha(1); got != 1 {
		t.Errorf("in_quad: alpha(1) = %v, want 1", got)
	}
}

// A Go-built ramp that names a curve and nothing else keeps the curve when the
// all-zero endpoints are read as opaque.
func TestIndicatorEaseSurvivesTheOpaqueDefault(t *testing.T) {
	d := &IndicatorDef{Type: IndicatorRoll, Direction: "down", Opacity: IndicatorOpacity{Ease: "out_cubic"}}
	if got := d.ramp(); got.Start != 1 || got.End != 1 || got.Ease != "out_cubic" {
		t.Errorf("ramp() = %+v, want {1, 1, out_cubic}", got)
	}
}
