package yamlui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/ui/ease"
)

// ── harness ──
//
// Same shape as transition_test.go's clock -- it reuses drawLog and frame from
// there -- with the pointer added, because everything below is about what the
// pointer and the clock do to one offset.

type scrollRig struct {
	tree   *WidgetTree
	assets *AssetProvider
	log    *drawLog
	now    float32
	in     InputState
}

func newScrollRig(t *testing.T, src string) *scrollRig {
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
	return &scrollRig{
		tree:   tree,
		assets: &AssetProvider{NineSlices: map[string]*renderer.NineSlice{"slot": {}}},
		log:    log,
	}
}

// step advances the clock by dt, feeds the pointer state built up by the
// helpers below, and builds one frame. The edge-triggered flags are consumed
// here, the way a game's input layer consumes them: leaving MousePressed set
// for two builds is a second press, which is not what any caller means.
func (r *scrollRig) step(dt float32) frame {
	r.now += dt
	r.log.reset()
	r.tree.SetTime(r.now)
	r.tree.SetInput(r.in)
	r.in.MousePressed, r.in.MouseReleased, r.in.ScrollY = false, false, 0
	p, v, i, tx := r.tree.BuildAt(nil, r.assets, 0, 0, 1, 400, 400)
	return frame{p, v, i, tx}
}

func (r *scrollRig) moveTo(x, y float32)   { r.in.MouseX, r.in.MouseY = x, y }
func (r *scrollRig) wheel(notches float32) { r.in.ScrollY = notches }

func (r *scrollRig) press(x, y float32) {
	r.moveTo(x, y)
	r.in.MousePressed, r.in.MouseDown = true, true
}

func (r *scrollRig) release() {
	r.in.MouseDown, r.in.MouseReleased = false, true
}

// offset is the live scroll offset of a view, which is the one number every
// test here is about.
func (r *scrollRig) offset(id string) (x, y float32) {
	s := r.tree.scroll[id]
	if s == nil {
		return 0, 0
	}
	return s.X, s.Y
}

// listYAML is a 100px-tall view over five 40px rows: content 200, so exactly
// 100px of travel. The rows are flat bg_color panels, which go through the
// shared vertex stream, plus a nine-slice each so PanelFn reports a rect and a
// clip for them.
const listYAML = `widget: panel
id: root
width: 200
height: 300
children:
  - widget: scroll_view
    id: list
    height: 100
    children:
      - widget: panel
        id: row0
        height: 40
        nine_slice: slot
      - widget: panel
        id: row1
        height: 40
        nine_slice: slot
      - widget: panel
        id: row2
        height: 40
        nine_slice: slot
      - widget: panel
        id: row3
        height: 40
        nine_slice: slot
      - widget: panel
        id: row4
        height: 40
        nine_slice: slot
`

// ── content size ──

// The gap issue #149 lists second: contentH summed each child's DECLARED
// height, so a child sized by flex (which has none) counted as zero and the
// list stopped short of its own end.
//
// BROKEN by summing declared heights again -- scrollContentSize replaced with
// a loop over each child's `Height` (or `FontSize`) plus gaps, and the width
// returned as the view's, which is what the widget did before.
// `go test ./ui/yamlui/` printed:
//
//	--- FAIL: TestContentSizeComesFromLaidOutRects
//	    content height = 40, want 100: the flex row was measured as its
//	    declared height, which is zero
//	--- FAIL: TestWheelAxisFollowsTheDirection/horizontal
//	    offset = (0, 0), want (30, 0)
//	--- FAIL: TestHorizontalDragAndClip
//	    horizontal drag moved x to 0, want 30
//	--- FAIL: TestBothScrollsOnEitherAxis
//	    offset = (0, 30), want (20, 30)
//
// Four arms, one cause: a declared height says nothing about a flex row, and
// a summed height says nothing at all about a width, so the horizontal axis
// had no travel to give.
func TestContentSizeComesFromLaidOutRects(t *testing.T) {
	const src = `widget: panel
id: root
width: 200
height: 300
children:
  - widget: scroll_view
    id: list
    height: 100
    children:
      - widget: panel
        id: fixed
        height: 40
        nine_slice: slot
      - widget: panel
        id: stretchy
        flex: 1
        nine_slice: slot
`
	r := newScrollRig(t, src)
	r.step(0)

	list := r.tree.NodeByID("list")
	view := scrollViewRect(list, 1)
	_, h := scrollContentSize(list, view)

	// Layout gives the flex row the 60px the fixed row left, so the content is
	// the full 100. Summing declared heights would say 40.
	if h != 100 {
		t.Errorf("content height = %v, want 100: the flex row was measured as its declared height, which is zero", h)
	}
}

func TestContentSizeIncludesNestedLayouts(t *testing.T) {
	const src = `widget: panel
id: root
width: 200
height: 300
children:
  - widget: scroll_view
    id: list
    height: 100
    children:
      - widget: panel
        id: group
        layout: horizontal
        height: 120
        children:
          - widget: panel
            id: left
            flex: 1
            nine_slice: slot
`
	r := newScrollRig(t, src)
	r.step(0)

	list := r.tree.NodeByID("list")
	_, h := scrollContentSize(list, scrollViewRect(list, 1))
	if h != 120 {
		t.Errorf("content height = %v, want 120", h)
	}
}

// A nested scroll_view's own content does not belong to the outer one: it is
// already confined to its rect, and counting it would make the outer list
// scroll by the inner list's length.
func TestContentSizeStopsAtANestedScrollView(t *testing.T) {
	const src = `widget: panel
id: root
width: 200
height: 300
children:
  - widget: scroll_view
    id: outer
    height: 100
    children:
      - widget: scroll_view
        id: inner
        height: 60
        children:
          - widget: panel
            id: tall
            height: 400
            nine_slice: slot
`
	r := newScrollRig(t, src)
	r.step(0)

	outer := r.tree.NodeByID("outer")
	_, h := scrollContentSize(outer, scrollViewRect(outer, 1))
	if h != 60 {
		t.Errorf("outer content height = %v, want 60: the inner view's own overflow was counted", h)
	}
}

// ── clamping ──

func TestScrollClampsWhenContentShrinks(t *testing.T) {
	r := newScrollRig(t, listYAML)
	r.step(0)

	r.moveTo(50, 50)
	r.wheel(-10) // far past the end; the clamp is what stops it
	r.step(0.016)
	if _, y := r.offset("list"); y != 100 {
		t.Fatalf("offset after scrolling to the end = %v, want 100", y)
	}

	// Two rows instead of five: 80px of content in a 100px view, so there is
	// nowhere left to scroll and the offset has to come back to zero.
	r.tree.SetChildren("list", []WidgetDef{
		{Widget: "panel", ID: "row0", Height: 40, NineSlice: "slot"},
		{Widget: "panel", ID: "row1", Height: 40, NineSlice: "slot"},
	})
	r.step(0.016)
	if _, y := r.offset("list"); y != 0 {
		t.Errorf("offset after the list shrank = %v, want 0", y)
	}
}

// The other half of the same promise: a list refilled with the SAME amount of
// content keeps its place, which is what makes a live-filtered list usable.
func TestScrollSurvivesSetChildren(t *testing.T) {
	r := newScrollRig(t, listYAML)
	r.step(0)
	r.moveTo(50, 50)
	r.wheel(-1)
	r.step(0.016)
	_, before := r.offset("list")
	if before == 0 {
		t.Fatal("the wheel moved nothing; the rest of this test proves nothing")
	}

	rows := make([]WidgetDef, 5)
	for i := range rows {
		rows[i] = WidgetDef{Widget: "panel", Height: 40, NineSlice: "slot"}
	}
	r.tree.SetChildren("list", rows)
	r.step(0.016)
	if _, after := r.offset("list"); after != before {
		t.Errorf("offset after SetChildren = %v, want %v", after, before)
	}
}

// ── the wheel ──

func TestWheelScrollsTheVerticalAxis(t *testing.T) {
	r := newScrollRig(t, listYAML)
	r.step(0)
	r.moveTo(50, 50)
	r.wheel(-1)
	r.step(0.016)
	if _, y := r.offset("list"); y != scrollWheelStep {
		t.Errorf("one wheel notch moved %v, want %v", y, float32(scrollWheelStep))
	}
	r.moveTo(50, 50)
	r.wheel(1)
	r.step(0.016)
	if _, y := r.offset("list"); y != 0 {
		t.Errorf("the notch back moved to %v, want 0", y)
	}
}

func TestWheelIsIgnoredOutsideTheView(t *testing.T) {
	r := newScrollRig(t, listYAML)
	r.step(0)
	r.moveTo(50, 250) // below the view, still inside the root
	r.wheel(-1)
	r.step(0.016)
	if _, y := r.offset("list"); y != 0 {
		t.Errorf("a wheel notch outside the view moved it to %v, want 0", y)
	}
}

// A view that only scrolls sideways takes the wheel on the axis it has. A view
// with both takes it on the vertical one, which is what a wheel means.
func TestWheelAxisFollowsTheDirection(t *testing.T) {
	for _, tc := range []struct {
		dir          string
		wantX, wantY float32
	}{
		{"horizontal", scrollWheelStep, 0},
		{"both", 0, scrollWheelStep},
		{"vertical", 0, scrollWheelStep},
	} {
		t.Run(tc.dir, func(t *testing.T) {
			r := newScrollRig(t, wideListYAML(tc.dir))
			r.step(0)
			r.moveTo(50, 50)
			r.wheel(-1)
			r.step(0.016)
			x, y := r.offset("list")
			if x != tc.wantX || y != tc.wantY {
				t.Errorf("offset = (%v, %v), want (%v, %v)", x, y, tc.wantX, tc.wantY)
			}
		})
	}
}

// wideListYAML is a 100x100 view over content that overflows on both axes: a
// horizontal row of three 100px columns, each 200 tall.
func wideListYAML(dir string) string {
	return `widget: panel
id: root
width: 300
height: 300
children:
  - widget: scroll_view
    id: list
    width: 100
    height: 100
    layout: horizontal
    scroll_direction: ` + dir + `
    children:
      - widget: panel
        id: col0
        width: 100
        height: 200
        nine_slice: slot
      - widget: panel
        id: col1
        width: 100
        height: 200
        nine_slice: slot
      - widget: panel
        id: col2
        width: 100
        height: 200
        nine_slice: slot
`
}

// ── drag ──

func TestContentDragFollowsThePointer(t *testing.T) {
	r := newScrollRig(t, listYAML)
	r.step(0)

	r.press(50, 80)
	r.step(0.016)
	// Up 30: the content goes up with the finger, so the offset goes down the
	// list by the same 30.
	r.moveTo(50, 50)
	r.step(0.016)
	if _, y := r.offset("list"); y != 30 {
		t.Errorf("offset after dragging 30px up = %v, want 30", y)
	}
	r.moveTo(50, 90)
	r.step(0.016)
	if _, y := r.offset("list"); y != 0 {
		t.Errorf("offset after dragging back past the start = %v, want 0 (clamped)", y)
	}
	r.release()
	r.step(0.016)
	r.moveTo(50, 20)
	r.step(0.016)
	if _, y := r.offset("list"); y != 0 {
		t.Errorf("the pointer moved %v after the button came up; the drag was not released", y)
	}
}

func TestDragIsRefusedWhenDragIsFalse(t *testing.T) {
	src := strings.Replace(listYAML, "    id: list\n", "    id: list\n    drag: false\n", 1)
	r := newScrollRig(t, src)
	r.step(0)
	r.press(50, 80)
	r.step(0.016)
	r.moveTo(50, 20)
	r.step(0.016)
	if _, y := r.offset("list"); y != 0 {
		t.Errorf("drag: false still scrolled to %v", y)
	}
}

// ── the dead zone ──
//
// The whole reason it exists: a list of rows that are buttons has to be both
// clickable and draggable with one button.
//
// BROKEN by removing the dead zone -- the distance test in scrollInput
// replaced with `dx != 0 || dy != 0`, so any movement at all is a drag.
// `go test ./ui/yamlui/` printed:
//
//	--- FAIL: TestDragUnderTheDeadZoneStillClicks
//	    a 2px wobble swallowed the click: 0 events, want 1
//
// and BROKEN the other way -- scrollDeadZone raised from 4 to 40, past the
// distance a real drag covers -- printed:
//
//	--- FAIL: TestContentDragFollowsThePointer
//	    offset after dragging 30px up = 0, want 30
//	--- FAIL: TestDragOverTheDeadZoneCancelsTheClick
//	    the drag moved 0, want 20; the click arm below would prove nothing
//	--- FAIL: TestHorizontalDragAndClip
//	    horizontal drag moved x to 0, want 30
//	--- FAIL: TestBothScrollsOnEitherAxis
//	    offset = (0, 0), want (20, 30)
//
// A third break was tried and is written down because of what it does NOT
// catch: going live on the press frame itself rather than on movement. The
// cancel then runs BEFORE the row records its press -- scrollInput runs ahead
// of the view's children -- so the click survives both arms and only
// TestDragOverTheDeadZoneCancelsTheClick fires, with "the drag delivered a
// click anyway: [{click row1 pick}]". The order of the two is the thing that
// makes the cancel work, and nothing here states it on its own.
func TestDragUnderTheDeadZoneStillClicks(t *testing.T) {
	r := newScrollRig(t, buttonListYAML)
	r.step(0)

	r.press(50, 50)
	r.step(0.016)
	r.moveTo(51, 52) // 2.2px: a hand on a mouse, not a drag
	r.step(0.016)
	r.release()
	f := r.step(0.016)
	_ = f

	evs := r.tree.DrainEvents()
	if len(evs) != 1 {
		t.Fatalf("a 2px wobble swallowed the click: %d events, want 1", len(evs))
	}
	if _, y := r.offset("list"); y != 0 {
		t.Errorf("a 2px wobble scrolled the list to %v, want 0", y)
	}
}

func TestDragOverTheDeadZoneCancelsTheClick(t *testing.T) {
	r := newScrollRig(t, buttonListYAML)
	r.step(0)

	r.press(50, 50)
	r.step(0.016)
	r.moveTo(50, 30) // 20px: unambiguously a drag
	r.step(0.016)
	if _, y := r.offset("list"); y != 20 {
		t.Fatalf("the drag moved %v, want 20; the click arm below would prove nothing", y)
	}
	r.release()
	r.step(0.016)

	if evs := r.tree.DrainEvents(); len(evs) != 0 {
		t.Errorf("the drag delivered a click anyway: %v", evs)
	}
}

// buttonListYAML is listYAML's rows turned into buttons, so a click has
// somewhere to land.
const buttonListYAML = `widget: panel
id: root
width: 200
height: 300
children:
  - widget: scroll_view
    id: list
    height: 100
    children:
      - widget: button
        id: row0
        height: 40
        nine_slice: slot
        on_click: pick
      - widget: button
        id: row1
        height: 40
        nine_slice: slot
        on_click: pick
      - widget: button
        id: row2
        height: 40
        nine_slice: slot
        on_click: pick
      - widget: button
        id: row3
        height: 40
        nine_slice: slot
        on_click: pick
      - widget: button
        id: row4
        height: 40
        nine_slice: slot
        on_click: pick
`

// ── the thumb ──

func TestThumbDragMovesTheContentByTheTrackRatio(t *testing.T) {
	r := newScrollRig(t, listYAML)
	r.step(0)

	list := r.tree.NodeByID("list")
	view := scrollViewRect(list, 1)
	_, contentH := scrollContentSize(list, view)
	_, thumbH := thumbGeometry(0, 100, view.H, contentH, 1)
	// 100px view over 200px content: a half-length thumb, so 50px of track for
	// 100px of travel and one pixel of thumb is two of content.
	if thumbH != 50 {
		t.Fatalf("thumb height = %v, want 50", thumbH)
	}

	bar := r.tree.thumbRectY(view, 0, 100, contentH, 1)
	r.press(bar.X+bar.W/2, bar.Y+bar.H/2)
	r.step(0.016)
	r.moveTo(bar.X+bar.W/2, bar.Y+bar.H/2+10)
	r.step(0.016)
	if _, y := r.offset("list"); y != 20 {
		t.Errorf("10px of thumb moved the content %v, want 20", y)
	}
}

func TestThumbIsNotHitTestableWhenTheScrollbarIsNever(t *testing.T) {
	src := strings.Replace(listYAML, "    id: list\n", "    id: list\n    scrollbar: never\n    drag: false\n", 1)
	r := newScrollRig(t, src)
	r.step(0)

	list := r.tree.NodeByID("list")
	view := scrollViewRect(list, 1)
	_, contentH := scrollContentSize(list, view)
	bar := r.tree.thumbRectY(view, 0, 100, contentH, 1)

	r.press(bar.X+bar.W/2, bar.Y+bar.H/2)
	r.step(0.016)
	r.moveTo(bar.X+bar.W/2, bar.Y+bar.H/2+10)
	r.step(0.016)
	if _, y := r.offset("list"); y != 0 {
		t.Errorf("a press on where the thumb would be scrolled to %v with scrollbar: never", y)
	}
}

// ── the bar's modes ──
//
// The bar is the only flat quad a scroll_view emits itself, so counting quads
// in the shared stream counts bars.
func TestScrollbarModes(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		rows  int
		quads int
	}{
		{"", 5, 1}, // auto, overflowing
		{"", 2, 0}, // auto, fits
		{"always", 2, 1},
		{"always", 5, 1},
		{"never", 5, 0},
	} {
		name := tc.mode
		if name == "" {
			name = "auto"
		}
		t.Run(name+"/"+map[bool]string{true: "overflows", false: "fits"}[tc.rows > 2], func(t *testing.T) {
			src := listYAML
			if tc.mode != "" {
				src = strings.Replace(src, "    id: list\n", "    id: list\n    scrollbar: "+tc.mode+"\n", 1)
			}
			r := newScrollRig(t, src)
			if tc.rows != 5 {
				r.step(0)
				rows := make([]WidgetDef, tc.rows)
				for i := range rows {
					rows[i] = WidgetDef{Widget: "panel", Height: 40, NineSlice: "slot"}
				}
				r.tree.SetChildren("list", rows)
			}
			f := r.step(0)
			if got := len(f.verts) / 4; got != tc.quads {
				t.Errorf("%d flat quads, want %d", got, tc.quads)
			}
		})
	}
}

// ── clipping ──

func TestEveryDrawInAViewCarriesItsClip(t *testing.T) {
	r := newScrollRig(t, listYAML)
	f := r.step(0)

	list := r.tree.NodeByID("list")
	view := scrollViewRect(list, 1)
	want := renderer.ClipRect{X: view.X, Y: view.Y, W: view.W, H: view.H}

	var clipped int
	for _, p := range f.panels {
		if p.Clip == nil {
			continue
		}
		clipped++
		if *p.Clip != want {
			t.Errorf("row clip = %+v, want %+v", *p.Clip, want)
		}
	}
	// Three rows are on screen in a 100px view over 40px rows.
	if clipped != 3 {
		t.Errorf("%d clipped row draws, want 3", clipped)
	}
}

func TestATreeWithNoScrollViewClipsNothing(t *testing.T) {
	const src = `widget: panel
id: root
width: 200
height: 100
nine_slice: slot
children:
  - widget: label
    id: caption
    text: hello
    font_size: 12
`
	r := newScrollRig(t, src)
	f := r.step(0)
	for _, p := range f.panels {
		if p.Clip != nil {
			t.Errorf("a panel in a tree with no scroll_view carries a clip: %+v", *p.Clip)
		}
	}
	for _, l := range f.text {
		if l.Clip != nil {
			t.Errorf("a line in a tree with no scroll_view carries a clip: %+v", *l.Clip)
		}
	}
}

// Nesting is the rule that cannot be got right by accident: the inner clip is
// the INTERSECTION, not the inner rect, or a row inside a nested list draws
// outside the list that holds it.
//
// BROKEN by replacing the intersection in pushClip with the new rect alone
// (`t.clip = &next` without the `if prev != nil` fold). `go test ./ui/yamlui/`
// printed:
//
//	--- FAIL: TestNestedClipsIntersect
//	    inner clip = {X:0 Y:120 W:200 H:60}, want {X:0 Y:120 W:200 H:30}
func TestNestedClipsIntersect(t *testing.T) {
	// The outer view is 150 tall and holds a 120px spacer then a 60px inner
	// view, so the inner view runs from y 120 to 180 and the outer clip ends at
	// 150: the intersection is 30 tall.
	const src = `widget: panel
id: root
width: 200
height: 400
children:
  - widget: scroll_view
    id: outer
    height: 150
    children:
      - widget: panel
        id: spacer
        height: 120
        nine_slice: slot
      - widget: scroll_view
        id: inner
        height: 60
        children:
          - widget: panel
            id: deep
            height: 40
            nine_slice: slot
`
	r := newScrollRig(t, src)
	f := r.step(0)

	outer := r.tree.NodeByID("outer")
	inner := r.tree.NodeByID("inner")
	o := scrollViewRect(outer, 1)
	i := scrollViewRect(inner, 1)
	want := renderer.ClipRect{X: o.X, Y: o.Y, W: o.W, H: o.H}.
		Intersect(renderer.ClipRect{X: i.X, Y: i.Y, W: i.W, H: i.H})
	if want.H != 30 {
		t.Fatalf("the fixture is wrong: the intersection is %v tall, want 30", want.H)
	}

	// The last clipped panel is the deep one: spacer, then inner's background
	// (which has none), then deep.
	var got *renderer.ClipRect
	for i := range f.panels {
		if f.panels[i].Clip != nil {
			got = f.panels[i].Clip
		}
	}
	if got == nil {
		t.Fatal("nothing inside the nested view was clipped at all")
	}
	if *got != want {
		t.Errorf("inner clip = %+v, want %+v", *got, want)
	}
}

// The shared flat-quad stream is one mesh and one draw in every host, so a
// clipped quad in it is trimmed geometrically rather than scissored. A row
// straddling the bottom edge has to come back cut at the edge.
func TestFlatQuadsAreTrimmedToTheClip(t *testing.T) {
	clip := &renderer.ClipRect{X: 0, Y: 0, W: 100, H: 50}
	verts, idxs := appendQuadClipped(nil, nil, 10, 30, 40, 40, [3]float32{1, 0, 0}, clip)
	if len(verts) != 4 || len(idxs) != 6 {
		t.Fatalf("got %d verts and %d indices, want 4 and 6", len(verts), len(idxs))
	}
	b := vertBounds(verts)
	// The quad asked for y 30..70; the clip ends at 50. The skirt is undone by
	// vertBounds on the edges that survived and not on the cut one, so the
	// bottom comes back at 50 - edgeSkirt.
	if b.Y != 30 || b.Y+b.H != 50-edgeSkirt {
		t.Errorf("trimmed quad spans y %v..%v, want 30..%v", b.Y, b.Y+b.H, 50-edgeSkirt)
	}
	// And the UV at the cut edge is an interior value, which is what makes the
	// edge hard rather than a coverage ramp in the middle of the row.
	var maxV float32
	for _, v := range verts {
		if v.UV[1] > maxV {
			maxV = v.UV[1]
		}
	}
	if maxV >= 1 {
		t.Errorf("the cut edge carries UV v = %v, want an interior value below 1", maxV)
	}

	if _, _, _, _, _, _, _, _, ok := clip.TrimQuad(200, 200, 240, 240, 0, 0, 1, 1); ok {
		t.Error("a quad entirely outside the clip survived")
	}
}

func TestClippedTextLinesCarryTheClip(t *testing.T) {
	const src = `widget: panel
id: root
width: 200
height: 300
children:
  - widget: scroll_view
    id: list
    height: 100
    children:
      - widget: label
        id: line0
        height: 40
        font_size: 12
        text: hello
`
	r := newScrollRig(t, src)
	f := r.step(0)
	if len(f.text) != 1 {
		t.Fatalf("%d lines, want 1", len(f.text))
	}
	if f.text[0].Clip == nil {
		t.Fatal("a label inside a scroll_view was handed no clip")
	}
	list := r.tree.NodeByID("list")
	view := scrollViewRect(list, 1)
	if want := (renderer.ClipRect{X: view.X, Y: view.Y, W: view.W, H: view.H}); *f.text[0].Clip != want {
		t.Errorf("line clip = %+v, want %+v", *f.text[0].Clip, want)
	}
}

// ── horizontal and both ──

func TestHorizontalDragAndClip(t *testing.T) {
	r := newScrollRig(t, wideListYAML("horizontal"))
	r.step(0)

	r.press(50, 50)
	r.step(0.016)
	r.moveTo(20, 50)
	r.step(0.016)
	x, y := r.offset("list")
	if x != 30 {
		t.Errorf("horizontal drag moved x to %v, want 30", x)
	}
	if y != 0 {
		t.Errorf("a horizontal view scrolled vertically to %v", y)
	}
}

func TestBothScrollsOnEitherAxis(t *testing.T) {
	r := newScrollRig(t, wideListYAML("both"))
	r.step(0)
	r.press(60, 60)
	r.step(0.016)
	r.moveTo(40, 30)
	r.step(0.016)
	x, y := r.offset("list")
	if x != 20 || y != 30 {
		t.Errorf("offset = (%v, %v), want (20, 30)", x, y)
	}
}

// ── scroll into view ──

func TestFocusScrollsTheMinimumDistance(t *testing.T) {
	r := newScrollRig(t, listYAML)
	r.step(0)

	// row3 is laid out at y 120..160 in a view that ends at 100: the minimum
	// move is 60, which puts its bottom edge exactly on the view's.
	r.tree.SetFocus("row3")
	r.step(0)
	st := r.tree.scroll["list"]
	if !st.active {
		t.Fatal("focus outside the view started no scroll")
	}
	if st.toY != 60 {
		t.Errorf("target offset = %v, want 60", st.toY)
	}

	// Halfway through 0.15s, on out_cubic.
	r.step(scrollIntoViewDuration / 2)
	want := 60 * ease.OutCubic(0.5)
	if _, y := r.offset("list"); !near(y, want, 1e-4) {
		t.Errorf("offset at the midpoint = %v, want %v", y, want)
	}

	r.step(scrollIntoViewDuration)
	if _, y := r.offset("list"); y != 60 {
		t.Errorf("offset after the tween = %v, want 60", y)
	}
	if r.tree.scroll["list"].active {
		t.Error("the tween never finished")
	}

	// A row already on screen moves nothing at all.
	r.tree.SetFocus("row2")
	r.step(0.016)
	if _, y := r.offset("list"); y != 60 {
		t.Errorf("focus on a visible row moved the list to %v", y)
	}
}

func TestScrollEaseNoneSnaps(t *testing.T) {
	src := strings.Replace(listYAML, "    id: list\n", "    id: list\n    scroll_ease: none\n", 1)
	r := newScrollRig(t, src)
	r.step(0)
	r.tree.SetFocus("row4")
	r.step(0)
	if _, y := r.offset("list"); y != 100 {
		t.Errorf("scroll_ease: none left the offset at %v on the frame focus moved, want 100", y)
	}
}

func TestFocusScrollsNestedViewsFromTheInsideOut(t *testing.T) {
	// The inner view is 60 tall inside a 100-tall outer, at outer-content y
	// 200. The focused row sits at inner-content y 100.
	const src = `widget: panel
id: root
width: 200
height: 400
children:
  - widget: scroll_view
    id: outer
    height: 100
    scroll_ease: none
    children:
      - widget: panel
        id: pad
        height: 200
        nine_slice: slot
      - widget: scroll_view
        id: inner
        height: 60
        scroll_ease: none
        children:
          - widget: panel
            id: a
            height: 100
            nine_slice: slot
          - widget: panel
            id: b
            height: 40
            nine_slice: slot
`
	r := newScrollRig(t, src)
	r.step(0)
	r.tree.SetFocus("b")
	r.step(0)

	// b is at inner y 100..140 in a 60-tall view: the inner scrolls 80 so b's
	// bottom lands on the inner's bottom edge, putting b at screen y 220..260.
	_, iy := r.offset("inner")
	if iy != 80 {
		t.Errorf("inner offset = %v, want 80", iy)
	}
	// The outer view is 100 tall and b now sits at 220..260, so the outer has
	// to move 160 to bring b's bottom to its own.
	_, oy := r.offset("outer")
	if oy != 160 {
		t.Errorf("outer offset = %v, want 160", oy)
	}
}

// ── errors ──

func TestScrollFieldErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{
			"direction on a panel",
			"widget: panel\nid: p\nscroll_direction: vertical\n",
			`widget "p": scroll_direction is only supported on scroll_view, not panel`,
		},
		{
			"scrollbar on a label",
			"widget: label\nid: l\nscrollbar: auto\n",
			`widget "l": scrollbar is only supported on scroll_view, not label`,
		},
		{
			"drag on a button",
			"widget: button\nid: b\ndrag: true\n",
			`widget "b": drag is only supported on scroll_view, not button`,
		},
		{
			"scroll_ease on an icon",
			"widget: icon\nid: i\nscroll_ease: linear\n",
			`widget "i": scroll_ease is only supported on scroll_view, not icon`,
		},
		{
			"unknown direction",
			"widget: scroll_view\nid: s\nscroll_direction: sideways\n",
			`widget "s": unknown scroll_direction "sideways" (want vertical, horizontal, both)`,
		},
		{
			"unknown scrollbar",
			"widget: scroll_view\nid: s\nscrollbar: sometimes\n",
			`widget "s": unknown scrollbar "sometimes" (want auto, always, never)`,
		},
		{
			"unknown ease",
			"widget: scroll_view\nid: s\nscroll_ease: out_wobble\n",
			`widget "s": unknown scroll_ease "out_wobble" (want none, linear,`,
		},
		{
			"nested child",
			"widget: panel\nid: root\nchildren:\n  - widget: panel\n    id: kid\n    scrollbar: never\n",
			`widget "kid": scrollbar is only supported on scroll_view, not panel`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "ui.yaml")
			if err := os.WriteFile(path, []byte(tc.src), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := ParseFS(os.DirFS(dir), "ui.yaml")
			if err == nil {
				t.Fatalf("no error; want one containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestValidScrollFieldsLoad(t *testing.T) {
	const src = `widget: scroll_view
id: s
scroll_direction: both
scrollbar: always
drag: false
scroll_ease: out_quad
`
	dir := t.TempDir()
	path := filepath.Join(dir, "ui.yaml")
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	def, err := ParseFS(os.DirFS(dir), "ui.yaml")
	if err != nil {
		t.Fatalf("a valid block was rejected: %v", err)
	}
	if def.scrollDirection() != scrollBoth {
		t.Error("scroll_direction: both did not resolve to both")
	}
	if def.dragEnabled() {
		t.Error("drag: false resolved to enabled")
	}
	if !def.scrollbarVisible(false) {
		t.Error("scrollbar: always hid itself on a view that fits")
	}
	// An absent drag is on, which is the default a list needs.
	if !(&WidgetDef{}).dragEnabled() {
		t.Error("an absent drag resolved to disabled")
	}
}
