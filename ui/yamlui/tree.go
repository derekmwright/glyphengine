package yamlui

import (
	"fmt"
	"io/fs"
	"strconv"

	"github.com/derekmwright/glyphengine/renderer"
)

// Node is a runtime widget node produced from a WidgetDef.
type Node struct {
	Def      *WidgetDef
	ID       string
	Children []*Node
	Rect     Rect // resolved screen rect after layout

	// trans is this widget's place in its `transition:` block, and is the only
	// state in this package that survives a frame. It lives here rather than in
	// a map on the tree so that Load and SetChildren throw it away with the
	// nodes they replace: a dialog rebuilt from YAML while open starts at rest,
	// not half faded. See transition.go.
	trans transitionState
}

// AssetProvider maps named assets to GPU resources for rendering.
type AssetProvider struct {
	NineSlices map[string]*renderer.NineSlice
	Font       *renderer.Font
	Fonts      map[string]*renderer.Font
}

// ResolveFont returns the named font if available, otherwise the default Font.
func (a *AssetProvider) ResolveFont(name string) *renderer.Font {
	if name != "" && a.Fonts != nil {
		if f, ok := a.Fonts[name]; ok {
			return f
		}
	}
	return a.Font
}

// PanelBuilder renders a nine-slice panel at a given screen rect.
// This callback is provided by the caller to avoid importing engine/ui.
type PanelBuilder func(r *renderer.Renderer, slice *renderer.NineSlice, color [3]float32, opacity float32, x, y, w, h, scale, sw, sh float32) []renderer.UIRenderObject

// IconBuilder renders an icon texture at a given screen rect.
// This callback is provided by the caller to avoid importing engine/ui.
type IconBuilder func(textureName string, color [3]float32, opacity float32, x, y, w, h, sw, sh float32) []renderer.UIRenderObject

// ShapeBuilder uploads a flat-coloured triangle list and returns the render
// objects that draw it at the given opacity. Like PanelBuilder and IconBuilder
// it is a callback so this package never owns a GPU mesh.
//
// An indicator cannot go through the vertex stream BuildAt returns alongside
// the panels: renderer.Vertex carries position, colour, normal and UV and no
// alpha at all, so a per-object Opacity is the only place an indicator's
// interpolated alpha can live. Emitting render objects also puts the indicator
// in the same slice as the sprite it covers, which is what makes the draw order
// -- widget, sprite, indicator -- a property of this package rather than of
// whichever stream the caller happens to submit first.
type ShapeBuilder func(verts []renderer.Vertex, idxs []uint16, opacity float32) []renderer.UIRenderObject

// WidgetTree is the runtime widget tree built from a parsed YAML definition.
type WidgetTree struct {
	Root     *Node
	index    map[string]*Node
	bindings map[string]string
	// Color bindings for dynamic fg_color_key support.
	colorBindings map[string][3]float32
	// PanelFn renders nine-slice panels (injected to avoid import cycle).
	PanelFn PanelBuilder
	// IconFn renders icon textures (injected to avoid import cycle).
	IconFn IconBuilder
	// ShapeFn renders indicator geometry (injected to avoid import cycle).
	// Without it an `indicator:` block draws nothing, the same way a `sprite:`
	// draws nothing without IconFn.
	ShapeFn ShapeBuilder

	// now is the unscaled clock SetTime last fed in, prevNow what it read on
	// the previous build, and timeSet whether the host has fed one at all.
	// Transitions advance on the difference; see SetTime.
	now, prevNow float32
	timeSet      bool

	// opacityMul is the transition opacity of the subtree currently being
	// rendered, multiplied down through nested transitions. 1 everywhere
	// outside one, which is what keeps a tree at rest on exactly the draw path
	// a tree with no transition block takes.
	opacityMul float32

	// noInput is how many enclosing widgets are playing their out transition.
	// Above zero, nothing under them takes a click -- see renderNode.
	noInput int

	// rectSave holds the untransformed rects of a subtree while its transition
	// is drawn, so they can be put back exactly rather than divided back out.
	// Kept between frames so a steady HUD allocates nothing for it.
	rectSave []Rect

	// dt is how far the unscaled clock moved since the previous build,
	// computed once per BuildAt. Transitions and scroll-into-view both run on
	// it, and calling frameDelta twice would hand the second caller zero.
	dt float32

	// clip is the rectangle the subtree currently being rendered is confined
	// to, and nil outside every scroll_view -- which is every widget in a tree
	// that has none, and therefore exactly the draw path this package took
	// before it could clip at all. Nested views intersect; see pushClip.
	clip *renderer.ClipRect

	// noClip is SetContentClipping turned off: a diagnostic, and the control
	// arm of the pixel gate.
	noClip bool

	// Interactive state
	input     InputState
	events    []UIEvent
	hovered   map[string]bool
	pressed   map[string]bool
	scroll    map[string]*scrollState
	drag      dragState
	focusedID string
	// prevFocusedID is what focusedID was on the previous build, so a view
	// pulls a row into sight when focus ARRIVES on it rather than on every
	// frame it stays there. See scrollFocusIntoView.
	prevFocusedID string
	textBuffers   map[string]string
	cursorTick    int
}

// Load parses a YAML widget definition from fsys and builds a widget tree.
func Load(fsys fs.FS, name string) (*WidgetTree, error) {
	def, err := ParseFS(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("yamlui: load %s: %w", name, err)
	}
	t := &WidgetTree{
		index:         make(map[string]*Node),
		bindings:      make(map[string]string),
		colorBindings: make(map[string][3]float32),
		hovered:       make(map[string]bool),
		pressed:       make(map[string]bool),
		scroll:        make(map[string]*scrollState),
		textBuffers:   make(map[string]string),
	}
	t.Root = t.buildNode(def)
	return t, nil
}

func (t *WidgetTree) buildNode(def *WidgetDef) *Node {
	n := &Node{Def: def, ID: def.ID}
	if n.ID != "" {
		t.index[n.ID] = n
	}
	for i := range def.Children {
		child := t.buildNode(&def.Children[i])
		n.Children = append(n.Children, child)
	}
	return n
}

// Bind sets a string binding value.
func (t *WidgetTree) Bind(key, value string) {
	t.bindings[key] = value
}

// BindFloat sets a float binding value.
func (t *WidgetTree) BindFloat(key string, v float32) {
	t.bindings[key] = strconv.FormatFloat(float64(v), 'f', -1, 32)
}

// BindInt sets an integer binding value.
func (t *WidgetTree) BindInt(key string, v int) {
	t.bindings[key] = strconv.Itoa(v)
}

// BindColor sets a color binding for fg_color_key references.
func (t *WidgetTree) BindColor(key string, c [3]float32) {
	t.colorBindings[key] = c
}

// NodeByID returns the node with the given ID, or nil.
func (t *WidgetTree) NodeByID(id string) *Node {
	return t.index[id]
}

// RootWidth returns the root definition width in reference pixels.
func (t *WidgetTree) RootWidth() float32 {
	if t.Root != nil {
		return t.Root.Def.Width
	}
	return 0
}

// RootHeight returns the root definition height in reference pixels.
func (t *WidgetTree) RootHeight() float32 {
	if t.Root != nil {
		return t.Root.Def.Height
	}
	return 0
}

// SetInput updates the input state for this frame. Call before BuildAt().
func (t *WidgetTree) SetInput(inp InputState) {
	t.input = inp
}

// SetTime updates the clock `transition:` blocks run on. Call before BuildAt(),
// beside SetInput.
//
// Feed it glyphengine.Engine.UnscaledElapsed, not Elapsed: a modal mostly opens
// while the game is paused, and Elapsed is scaled by SetTimeScale, so a fade
// driven off it stops dead at scale 0 and the dialog never arrives. The value
// is a running total rather than a delta, so a host that skips a frame loses no
// time and one that hands the same reading twice advances nothing.
//
// A tree that is never given a clock plays no transitions at all: every widget
// cuts to hidden or to rest the moment `visible` flips, exactly as if the block
// were absent. That is deliberate. The alternative -- a delta that is always
// zero -- leaves a widget that just became visible stuck at the start of its
// fade forever, and a dialog that never opens is a much harder failure to read
// than one that does not fade.
func (t *WidgetTree) SetTime(seconds float32) {
	t.now = seconds
	t.timeSet = true
}

// DrainEvents returns and clears pending UI events.
func (t *WidgetTree) DrainEvents() []UIEvent {
	out := t.events
	t.events = nil
	return out
}

// FocusedID returns the ID of the currently focused text_input, or "".
func (t *WidgetTree) FocusedID() string {
	return t.focusedID
}

// TextBuffer returns the current text in a text_input by ID.
func (t *WidgetTree) TextBuffer(id string) string {
	return t.textBuffers[id]
}

// SetTextBuffer sets the text buffer for a text_input by ID.
func (t *WidgetTree) SetTextBuffer(id, value string) {
	t.textBuffers[id] = value
}

// SetFocus sets focus to the text_input with the given ID.
func (t *WidgetTree) SetFocus(id string) {
	t.focusedID = id
}

// BuildAt performs layout at an explicit screen position and renders the tree.
func (t *WidgetTree) BuildAt(r *renderer.Renderer, assets *AssetProvider, x, y, scale, sw, sh float32) ([]renderer.UIRenderObject, []renderer.Vertex, []uint16, []renderer.TextLine) {
	if t.Root == nil {
		return nil, nil, nil, nil
	}

	t.cursorTick++

	// Transitions advance BEFORE layout, not during the render walk, because
	// layout has to know which widgets are still on their way out: one of them
	// keeps its box until its out finishes. Doing it in renderNode would read
	// last frame's progress during layout and this frame's during the draw,
	// which is a widget one frame ahead of the hole it sits in.
	t.dt = t.frameDelta()
	t.advanceTransitions(t.Root, t.dt)

	t.opacityMul = 1
	t.noInput = 0
	t.clip = nil
	t.rectSave = t.rectSave[:0]

	rootW := t.Root.Def.Width * scale
	rootH := t.Root.Def.Height * scale
	t.Root.Rect = Rect{X: x, Y: y, W: rootW, H: rootH}

	layoutChildren(t.Root, t.Root.Rect, scale, t.bindings)

	// Handle click-to-focus: if mouse pressed and no text_input claims it, clear focus.
	if t.input.MousePressed {
		t.focusedID = ""
	}

	// Focus has to be chased AFTER layout, because the rect it aims a view at
	// is the one layout just resolved, and BEFORE the render walk, because the
	// walk is what draws the children at the new offset. Only on the frame
	// focus moved: a view that re-aimed every build could never be scrolled
	// away from with the wheel.
	if t.focusedID != t.prevFocusedID {
		t.prevFocusedID = t.focusedID
		t.scrollFocusIntoView(scale)
	}

	var panels []renderer.UIRenderObject
	var verts []renderer.Vertex
	var idxs []uint16
	var text []renderer.TextLine

	t.renderNode(r, assets, t.Root, scale, sw, sh, &panels, &verts, &idxs, &text)
	return panels, verts, idxs, text
}

// frameDelta is how far the transition clock moved since the previous build.
//
// A clock that went backwards contributes nothing rather than winding every
// transition back: the only ways it can is a host resetting its own timer or
// feeding two trees from different sources, and neither is a request to rewind
// a fade.
func (t *WidgetTree) frameDelta() float32 {
	if !t.timeSet {
		return 0
	}
	dt := t.now - t.prevNow
	t.prevNow = t.now
	if dt < 0 {
		return 0
	}
	return dt
}

// advanceTransitions walks the whole tree, not only the visible part: a widget
// playing its out is by definition one whose `visible` is false, so a walk that
// stopped at the visibility check could never advance the thing it is there to
// advance.
func (t *WidgetTree) advanceTransitions(node *Node, dt float32) {
	if d := node.Def.Transition; d != nil {
		node.trans.advance(d, t.visible(node.Def), t.timeSet, dt)
	}
	for _, child := range node.Children {
		t.advanceTransitions(child, dt)
	}
}

// visible resolves a widget's `visible` binding. An absent one is visible.
func (t *WidgetTree) visible(def *WidgetDef) bool {
	return def.Visible == "" || resolveBool(def.Visible, t.bindings)
}

// renderNode applies a widget's transition, if it has one, and draws it.
//
// The three things a transition changes all happen here rather than in the
// widget bodies below: the rect (scaled about the anchor and offset, for the
// whole subtree), the opacity (multiplied into t.opacityMul, which every
// builder call reads), and whether anything under this widget takes a click.
// Keeping it in one place is what makes "none of it touches layout" checkable
// -- layout has already run by the time any of this is applied.
func (t *WidgetTree) renderNode(r *renderer.Renderer, assets *AssetProvider, node *Node, scale, sw, sh float32, panels *[]renderer.UIRenderObject, verts *[]renderer.Vertex, idxs *[]uint16, text *[]renderer.TextLine) {
	vis := t.visible(node.Def)
	d := node.Def.Transition
	if d == nil {
		// Visibility check: if Visible is set and resolves to false, skip entirely.
		if !vis {
			return
		}
		t.renderWidget(r, assets, node, scale, sw, sh, panels, verts, idxs, text)
		return
	}

	// Skipped only after the out has finished. Until then the widget is still
	// on screen and still being drawn.
	if node.trans.gone() {
		return
	}

	// Input is refused from the first frame `visible` is false, not when the
	// fade ends. A dismiss that fell through a dialog on its way out would hit
	// whatever the dialog was covering, which is the one click a player is
	// certain not to have meant.
	if !vis {
		t.noInput++
	}

	if node.trans.atRest() {
		// The settled case skips the machinery rather than running it with an
		// identity transform. It draws the same either way -- see atRest -- so
		// this is a frame's worth of walks saved per block on screen and not a
		// correctness branch.
		t.renderWidget(r, assets, node, scale, sw, sh, panels, verts, idxs, text)
	} else {
		eased := d.eased(node.trans)
		opacity, factor, dx, dy := d.transform(eased, scale)

		mark := len(t.rectSave)
		t.saveRects(node)
		transformRects(node, factor, dx, dy, node.Rect.X+node.Rect.W/2, node.Rect.Y+node.Rect.H/2)

		prevOpacity := t.opacityMul
		t.opacityMul *= opacity
		t.renderWidget(r, assets, node, scale, sw, sh, panels, verts, idxs, text)
		t.opacityMul = prevOpacity

		t.restoreRects(node, mark)
		t.rectSave = t.rectSave[:mark]
	}

	if !vis {
		t.noInput--
	}
}

// saveRects and restoreRects stack a subtree's rects in pre-order.
//
// Restoring from a copy rather than applying the inverse transform: the inverse
// of a scale is exact only in arithmetic, and a rect that comes back a
// ten-thousandth off is a hit box that no longer matches the one layout
// computed. Nested transitions nest here too -- the inner pair pushes after the
// outer's block and truncates back to its own mark.
func (t *WidgetTree) saveRects(n *Node) {
	t.rectSave = append(t.rectSave, n.Rect)
	for _, child := range n.Children {
		t.saveRects(child)
	}
}

func (t *WidgetTree) restoreRects(n *Node, at int) int {
	n.Rect = t.rectSave[at]
	at++
	for _, child := range n.Children {
		at = t.restoreRects(child, at)
	}
	return at
}

func (t *WidgetTree) renderWidget(r *renderer.Renderer, assets *AssetProvider, node *Node, scale, sw, sh float32, panels *[]renderer.UIRenderObject, verts *[]renderer.Vertex, idxs *[]uint16, text *[]renderer.TextLine) {
	switch node.Def.Widget {
	case "panel":
		t.renderPanel(r, assets, node, scale, sw, sh, panels, verts, idxs)
	case "label":
		t.renderLabel(r, assets, node, scale, sw, sh, panels, text)
	case "progress_bar":
		t.renderProgressBar(r, assets, node, scale, sw, sh, panels, verts, idxs)
	case "button":
		t.renderButton(r, assets, node, scale, sw, sh, panels, text)
	case "scroll_view":
		t.renderScrollView(r, assets, node, scale, sw, sh, panels, verts, idxs, text)
		return // scroll_view handles its own children
	case "icon":
		t.renderIcon(node, sw, sh, panels)
	case "text_input":
		t.renderTextInput(r, assets, node, scale, sw, sh, panels, text)
	}

	for _, child := range node.Children {
		t.renderNode(r, assets, child, scale, sw, sh, panels, verts, idxs, text)
	}
}

func (t *WidgetTree) renderPanel(r *renderer.Renderer, assets *AssetProvider, node *Node, scale, sw, sh float32, panels *[]renderer.UIRenderObject, verts *[]renderer.Vertex, idxs *[]uint16) {
	def := node.Def
	if def.NineSlice == "" || assets == nil || t.PanelFn == nil {
		// Flat bg_color fallback for panels without a nine-slice (e.g. dividers).
		if def.BgColor != [3]float32{} {
			t.appendFlatQuad(verts, idxs, panels, node.Rect.X, node.Rect.Y, node.Rect.W, node.Rect.H,
				t.indicatorTint(def, def.BgColor))
		}
		t.renderIndicator(node, panels)
		return
	}
	nsName := resolve(def.NineSlice, t.bindings)
	slice, ok := assets.NineSlices[nsName]
	if !ok {
		return
	}
	color := t.indicatorTint(def, t.resolveColor(def))
	objs := t.PanelFn(r, slice, color, t.panelOpacity(def.Opacity), node.Rect.X, node.Rect.Y, node.Rect.W, node.Rect.H, scale, sw, sh)
	t.appendObjs(panels, objs)
	t.renderIndicator(node, panels)
}

func (t *WidgetTree) renderLabel(r *renderer.Renderer, assets *AssetProvider, node *Node, scale, sw, sh float32, panels *[]renderer.UIRenderObject, text *[]renderer.TextLine) {
	def := node.Def

	// Render optional nine-slice background (e.g. craft quantity display).
	if def.NineSlice != "" && assets != nil && t.PanelFn != nil {
		nsName := resolve(def.NineSlice, t.bindings)
		if slice, ok := assets.NineSlices[nsName]; ok {
			color := t.resolveColor(def)
			objs := t.PanelFn(r, slice, color, t.panelOpacity(def.Opacity), node.Rect.X, node.Rect.Y, node.Rect.W, node.Rect.H, scale, sw, sh)
			t.appendObjs(panels, objs)
		}
	}

	if def.Text == "" {
		return
	}
	resolved := resolve(def.Text, t.bindings)
	if resolved == "" {
		return
	}

	fontSize := def.FontSize * scale
	if fontSize <= 0 {
		return
	}

	var resolvedFont *renderer.Font
	if assets != nil {
		resolvedFont = assets.ResolveFont(def.Font)
	}

	tx := node.Rect.X
	if resolvedFont != nil {
		switch def.Align {
		case "center":
			tw := resolvedFont.MeasureText(resolved, fontSize)
			tx = node.Rect.X + (node.Rect.W-tw)/2
		case "right":
			tw := resolvedFont.MeasureText(resolved, fontSize)
			tx = node.Rect.X + node.Rect.W - tw
		}
	}

	// Vertically center label within its rect.
	ty := node.Rect.Y + (node.Rect.H-fontSize)/2

	color := t.resolveColor(def)

	tl := renderer.TextLine{
		Text:  resolved,
		X:     tx,
		Y:     ty,
		Scale: fontSize,
		Color: color,
		Alpha: t.textAlpha(),
	}
	if resolvedFont != nil && resolvedFont != assets.Font {
		tl.Font = resolvedFont
	}
	t.appendText(text, tl)
}

func (t *WidgetTree) renderProgressBar(r *renderer.Renderer, assets *AssetProvider, node *Node, scale, sw, sh float32, panels *[]renderer.UIRenderObject, verts *[]renderer.Vertex, idxs *[]uint16) {
	def := node.Def
	value := resolveFloat(def.Value, t.bindings)
	max := resolveFloat(def.Max, t.bindings)

	white := [3]float32{1, 1, 1}

	// Background: nine-slice or quad.
	if def.NineSlice != "" && assets != nil && t.PanelFn != nil {
		if slice, ok := assets.NineSlices[def.NineSlice]; ok {
			objs := t.PanelFn(r, slice, white, t.panelOpacity(1), node.Rect.X, node.Rect.Y, node.Rect.W, node.Rect.H, scale, sw, sh)
			t.appendObjs(panels, objs)
		}
	} else {
		t.appendFlatQuad(verts, idxs, panels, node.Rect.X, node.Rect.Y, node.Rect.W, node.Rect.H, def.BgColor)
	}

	// Foreground fill: nine-slice or quad.
	if max > 0 {
		ratio := value / max
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
		fillW := node.Rect.W * ratio
		if def.FgNineSlice != "" && assets != nil && t.PanelFn != nil {
			if slice, ok := assets.NineSlices[def.FgNineSlice]; ok {
				if fillW > 0 {
					objs := t.PanelFn(r, slice, white, t.panelOpacity(1), node.Rect.X, node.Rect.Y, fillW, node.Rect.H, scale, sw, sh)
					t.appendObjs(panels, objs)
				}
			}
		} else {
			fgColor := def.FgColor
			if def.FgColorKey != "" {
				if c, ok := t.colorBindings[def.FgColorKey]; ok {
					fgColor = c
				}
			}
			t.appendFlatQuad(verts, idxs, panels, node.Rect.X, node.Rect.Y, fillW, node.Rect.H, fgColor)
		}
	}
}

func (t *WidgetTree) renderButton(r *renderer.Renderer, assets *AssetProvider, node *Node, scale, sw, sh float32, panels *[]renderer.UIRenderObject, text *[]renderer.TextLine) {
	def := node.Def
	id := node.ID

	disabled := def.Disabled != "" && resolveBool(def.Disabled, t.bindings)

	// Hit testing, against the TRANSFORMED rect: a button that is sliding in is
	// clickable where it is drawn, not where layout put it. t.noInput is the
	// out transition of this widget or of something containing it -- see
	// renderNode -- and it refuses the click and the hover with it, so a dialog
	// on its way out neither lights up under the cursor nor fires.
	takesInput := !disabled && t.noInput == 0
	hover := takesInput && node.Rect.Contains(t.input.MouseX, t.input.MouseY)
	t.hovered[id] = hover

	if takesInput {
		if t.input.MousePressed && hover {
			t.pressed[id] = true
		}
		if t.input.MouseReleased {
			if t.pressed[id] && hover {
				t.events = append(t.events, UIEvent{
					Kind:   "click",
					NodeID: id,
					Value:  def.OnClick,
				})
			}
			t.pressed[id] = false
		}
	} else {
		t.pressed[id] = false
	}

	// Color modulation.
	color := t.resolveColor(def)
	switch {
	case disabled:
		color = modulateColor(color, 0.4)
	case t.pressed[id]:
		color = modulateColor(color, 0.6)
	case hover:
		color = modulateColor(color, 1.3)
	}
	color = t.indicatorTint(def, modulateColor(color, t.stateFactor(def)))

	// Render nine-slice background.
	if def.NineSlice != "" && assets != nil && t.PanelFn != nil {
		nsName := resolve(def.NineSlice, t.bindings)
		if slice, ok := assets.NineSlices[nsName]; ok {
			objs := t.PanelFn(r, slice, color, t.panelOpacity(def.Opacity), node.Rect.X, node.Rect.Y, node.Rect.W, node.Rect.H, scale, sw, sh)
			t.appendObjs(panels, objs)
		}
	}

	// The indicator sits between the widget and its label: it covers the
	// artwork and never the text that says what the widget is.
	t.renderIndicator(node, panels)

	// Render centered label.
	if def.Text != "" {
		resolved := resolve(def.Text, t.bindings)
		fontSize := def.FontSize * scale
		if fontSize <= 0 {
			fontSize = 14 * scale
		}
		var btnFont *renderer.Font
		if assets != nil {
			btnFont = assets.ResolveFont(def.Font)
		}
		tx := node.Rect.X
		if btnFont != nil {
			tw := btnFont.MeasureText(resolved, fontSize)
			tx = node.Rect.X + (node.Rect.W-tw)/2
		}
		ty := node.Rect.Y + (node.Rect.H-fontSize)/2

		textColor := def.FgColor
		if disabled {
			textColor = modulateColor(textColor, 0.5)
		}

		tl := renderer.TextLine{
			Text:  resolved,
			X:     tx,
			Y:     ty,
			Scale: fontSize,
			Color: textColor,
			Alpha: t.textAlpha(),
		}
		if btnFont != nil && assets != nil && btnFont != assets.Font {
			tl.Font = btnFont
		}
		t.appendText(text, tl)
	}
}

func (t *WidgetTree) renderScrollView(r *renderer.Renderer, assets *AssetProvider, node *Node, scale, sw, sh float32, panels *[]renderer.UIRenderObject, verts *[]renderer.Vertex, idxs *[]uint16, text *[]renderer.TextLine) {
	def := node.Def
	id := node.ID

	// Render optional nine-slice background.
	if def.NineSlice != "" && assets != nil && t.PanelFn != nil {
		if slice, ok := assets.NineSlices[def.NineSlice]; ok {
			objs := t.PanelFn(r, slice, def.Color, t.panelOpacity(def.Opacity), node.Rect.X, node.Rect.Y, node.Rect.W, node.Rect.H, scale, sw, sh)
			t.appendObjs(panels, objs)
		}
	}

	viewRect := scrollViewRect(node, scale)

	st := t.scrollStateFor(id)
	contentW, contentH := scrollContentSize(node, viewRect)
	dir := def.scrollDirection()

	var maxX, maxY float32
	if dir.horizontal() {
		maxX = maxScroll(contentW, viewRect.W)
	}
	if dir.vertical() {
		maxY = maxScroll(contentH, viewRect.H)
	}

	// Clamped before anything reads it, not after it is written. The offset
	// survives SetChildren on purpose -- a refiltered list keeps its place --
	// which means a list that got SHORTER arrives here scrolled past its own
	// end, and a blank one arrives scrolled off the top of nothing.
	st.X, st.Y = clampf(st.X, 0, maxX), clampf(st.Y, 0, maxY)

	t.scrollInput(node, st, viewRect, contentW, contentH, maxX, maxY, scale)
	t.advanceScroll(def, st, maxX, maxY)

	// Children draw under the view rect, intersected with whatever clip this
	// view is itself inside: that intersection is the whole of the nesting
	// rule, and it is one line because a clip is a rect and rects intersect.
	prevClip := t.pushClip(viewRect)
	for _, child := range node.Children {
		shifted := child.Rect
		shifted.X -= st.X
		shifted.Y -= st.Y

		// Children entirely outside the view are dropped before they cost
		// anything. The clip would have removed them anyway; this is what
		// keeps a thousand-row list the price of the dozen rows on screen.
		if shifted.Y+shifted.H < viewRect.Y || shifted.Y > viewRect.Y+viewRect.H ||
			shifted.X+shifted.W < viewRect.X || shifted.X > viewRect.X+viewRect.W {
			continue
		}

		// The subtree is shifted for the draw and put back from a COPY
		// afterwards, not by shifting it back: adding and subtracting the same
		// offset is exact only in arithmetic, and a row whose rect comes back
		// a ten-thousandth off is a hit box that no longer matches the one
		// layout computed. Same trick, and the same reason, as the transition
		// path's saveRects.
		//
		// Put back at all because a scroll offset is not a layout result: next
		// frame's content size is measured from where layout put the rows, not
		// from where this frame scrolled them.
		mark := len(t.rectSave)
		t.saveRects(child)
		shiftRects(child, -st.X, -st.Y)
		t.renderNode(r, assets, child, scale, sw, sh, panels, verts, idxs, text)
		t.restoreRects(child, mark)
		t.rectSave = t.rectSave[:mark]
	}
	t.clip = prevClip

	// The bar is drawn outside the content's clip, under whatever clip encloses
	// the view itself: it belongs to the container, not to what is in it.
	thumbColor := [3]float32{1, 1, 1}
	if dir.vertical() && def.scrollbarVisible(maxY > 0) {
		b := t.thumbRectY(viewRect, st.Y, maxY, contentH, scale)
		t.appendFlatQuad(verts, idxs, panels, b.X, b.Y, b.W, b.H, thumbColor)
	}
	if dir.horizontal() && def.scrollbarVisible(maxX > 0) {
		b := t.thumbRectX(viewRect, st.X, maxX, contentW, scale)
		t.appendFlatQuad(verts, idxs, panels, b.X, b.Y, b.W, b.H, thumbColor)
	}
}

// shiftRects translates a subtree by (dx, dy).
//
// The whole subtree, not just the child: a row's own label and icon are laid
// out at absolute coordinates inside it, so moving only the row would scroll
// its box out from under its contents. Same shape as transformRects, and for
// the same reason.
func shiftRects(n *Node, dx, dy float32) {
	n.Rect.X += dx
	n.Rect.Y += dy
	for _, c := range n.Children {
		shiftRects(c, dx, dy)
	}
}

func (t *WidgetTree) renderIcon(node *Node, sw, sh float32, panels *[]renderer.UIRenderObject) {
	def := node.Def
	if def.Sprite == "" || t.IconFn == nil {
		return
	}
	resolved := resolve(def.Sprite, t.bindings)
	if resolved == "" {
		return
	}
	color := t.indicatorTint(def, modulateColor(t.resolveColor(def), t.stateFactor(def)))
	objs := t.IconFn(resolved, color, t.panelOpacity(def.Opacity), node.Rect.X, node.Rect.Y, node.Rect.W, node.Rect.H, sw, sh)
	t.appendObjs(panels, objs)
	t.renderIndicator(node, panels)
}

// stateFactor is the tint multiplier a widget's `state` binding asks for.
//
// An absent state is 1: a widget that says nothing about a toggle is not off.
func (t *WidgetTree) stateFactor(def *WidgetDef) float32 {
	if def.State == "" || resolveBool(def.State, t.bindings) {
		return 1
	}
	return stateDim
}

// indicatorTint applies a `type: tint` indicator to a widget's colour. Every
// other type adds geometry instead and leaves the colour alone.
func (t *WidgetTree) indicatorTint(def *WidgetDef, color [3]float32) [3]float32 {
	d := def.Indicator
	if d == nil || d.Type != IndicatorTint {
		return color
	}
	frac, ok := d.frac(t.bindings)
	if !ok || d.cover(frac) <= 0 {
		return color
	}
	return d.tint(color, d.alpha(frac))
}

// renderIndicator appends the indicator's geometry over the widget's rect.
//
// It runs after the widget has drawn itself and before any text, so a cooldown
// covers the artwork and leaves the label that names it readable. A tint adds
// no geometry at all -- it has already been folded into the widget's colour by
// indicatorTint -- and neither does a zero fraction, which is what makes
// "the cooldown finished" byte-identical to "this widget has no indicator".
func (t *WidgetTree) renderIndicator(node *Node, panels *[]renderer.UIRenderObject) {
	d := node.Def.Indicator
	if d == nil || d.Type == IndicatorTint || t.ShapeFn == nil {
		return
	}
	frac, ok := d.frac(t.bindings)
	if !ok {
		return
	}
	cover := d.cover(frac)
	if cover <= 0 {
		return
	}
	alpha := d.alpha(frac) * t.opacityMul
	if alpha <= 0 {
		return
	}
	pts := d.indicatorGeometry(node.Rect, cover)
	if len(pts) < 3 {
		return
	}
	verts, idxs := appendIndicatorFan(nil, nil, pts, d.rgb())
	t.appendObjs(panels, t.ShapeFn(verts, idxs, alpha))
}

func (t *WidgetTree) renderTextInput(r *renderer.Renderer, assets *AssetProvider, node *Node, scale, sw, sh float32, panels *[]renderer.UIRenderObject, text *[]renderer.TextLine) {
	def := node.Def
	id := node.ID

	// Render optional nine-slice background.
	if def.NineSlice != "" && assets != nil && t.PanelFn != nil {
		if slice, ok := assets.NineSlices[def.NineSlice]; ok {
			objs := t.PanelFn(r, slice, def.Color, t.panelOpacity(def.Opacity), node.Rect.X, node.Rect.Y, node.Rect.W, node.Rect.H, scale, sw, sh)
			t.appendObjs(panels, objs)
		}
	}

	// Click to focus. Refused while this field is on its way out, the same as a
	// button's click is.
	if t.noInput == 0 && t.input.MousePressed && node.Rect.Contains(t.input.MouseX, t.input.MouseY) {
		t.focusedID = id
	}

	focused := t.focusedID == id
	buf := t.textBuffers[id]

	// Handle keyboard input when focused.
	if focused {
		for _, ch := range t.input.CharsTyped {
			buf += string(ch)
		}
		if t.input.BackspacePressed && len(buf) > 0 {
			// Remove last rune.
			runes := []rune(buf)
			buf = string(runes[:len(runes)-1])
		}
		if t.input.EnterPressed {
			t.events = append(t.events, UIEvent{
				Kind:   "submit",
				NodeID: id,
				Value:  buf,
			})
			buf = ""
			t.focusedID = ""
		}
		if t.input.EscapePressed {
			buf = ""
			t.focusedID = ""
		}
		t.textBuffers[id] = buf
	}

	// Render text or placeholder.
	fontSize := def.FontSize * scale
	if fontSize <= 0 {
		fontSize = 14 * scale
	}

	var inputFont *renderer.Font
	if assets != nil {
		inputFont = assets.ResolveFont(def.Font)
	}
	var nonDefaultFont *renderer.Font
	if inputFont != nil && assets != nil && inputFont != assets.Font {
		nonDefaultFont = inputFont
	}

	pad := def.Padding * scale
	tx := node.Rect.X + pad
	ty := node.Rect.Y + (node.Rect.H-fontSize)/2

	if buf != "" || focused {
		display := buf
		// Apply mask character (e.g. "*" for password fields).
		if def.Mask != "" && len(display) > 0 {
			maskRune := []rune(def.Mask)[0]
			display = string(repeatRune(maskRune, len([]rune(display))))
		}
		// Cursor blink: append "_" every other 30-tick cycle.
		if focused && (t.cursorTick/30)%2 == 0 {
			display += "_"
		}
		t.appendText(text, renderer.TextLine{
			Text:  display,
			X:     tx,
			Y:     ty,
			Scale: fontSize,
			Color: def.FgColor,
			Alpha: t.textAlpha(),
			Font:  nonDefaultFont,
		})
	} else if def.Placeholder != "" {
		resolved := resolve(def.Placeholder, t.bindings)
		phColor := modulateColor(def.FgColor, 0.5)
		if def.PlaceholderColor != ([3]float32{}) {
			phColor = def.PlaceholderColor
		}
		t.appendText(text, renderer.TextLine{
			Text:  resolved,
			X:     tx,
			Y:     ty,
			Scale: fontSize,
			Color: phColor,
			Alpha: t.textAlpha(),
			Font:  nonDefaultFont,
		})
	}
}

// repeatRune returns a slice of n copies of the given rune.
func repeatRune(r rune, n int) []rune {
	out := make([]rune, n)
	for i := range out {
		out[i] = r
	}
	return out
}

// panelOpacity is what a builder callback is handed for a widget's `opacity:`.
//
// The schema's 0 has always meant "not stated", so it reads as 1; a transition
// then multiplies that. Every PanelFn, IconFn and ShapeFn call in this file
// goes through here, because an opacity that reaches only some of them is a
// dialog that fades except for its progress bar.
func (t *WidgetTree) panelOpacity(base float32) float32 {
	if base == 0 {
		base = 1
	}
	return base * t.opacityMul
}

// textAlpha is what a TextLine's Alpha carries at the current opacity.
//
// renderer.TextLine reads Alpha 0 as fully opaque, so a tree at rest writes 0
// and its lines are byte-identical to a tree with no transition block -- and a
// fully faded line cannot write 0, which would make it opaque at the one moment
// it must not be. minTextAlpha is the smallest thing that is not zero and is
// far below what an 8-bit swapchain can show.
func (t *WidgetTree) textAlpha() float32 {
	if t.opacityMul >= 1 {
		return 0
	}
	if t.opacityMul <= minTextAlpha {
		return minTextAlpha
	}
	return t.opacityMul
}

const minTextAlpha = 1e-4

// appendFlatQuad emits a flat-coloured rectangle at the current opacity.
//
// At full opacity it goes into the shared vertex stream, which is where every
// flat quad has always gone and what the host submits as one mesh.
// renderer.Vertex has no alpha channel, though, so a fading one cannot live
// there: it is handed to ShapeFn instead, the same callback an indicator's
// geometry rides on, where the alpha can sit on the render object. That moves
// it out of the background stream and in among the panels, which for a dialog
// on its way in or out is where it belongs anyway.
//
// Without ShapeFn a fading flat quad falls back to the vertex stream and does
// not fade -- it still scales and slides. That is the same bargain `indicator:`
// makes with `ShapeFn`, except that here the widget is drawn rather than
// dropped, because a dialog that is opaque for two tenths of a second is a
// smaller surprise than one that is missing.
func (t *WidgetTree) appendFlatQuad(verts *[]renderer.Vertex, idxs *[]uint16, panels *[]renderer.UIRenderObject, x, y, w, h float32, col [3]float32) {
	if t.opacityMul >= 1 || t.ShapeFn == nil {
		// The shared stream is ONE mesh and ONE draw in every host that
		// consumes BuildAt, so a per-object clip rect cannot reach an
		// individual quad in it. A clipped quad is therefore trimmed here,
		// which is exactly what the scissor would have done to it and costs
		// nothing on the far side. The fading path below needs none of that:
		// it has a render object of its own to carry the clip.
		*verts, *idxs = appendQuadClipped(*verts, *idxs, x, y, w, h, col, t.clip)
		return
	}
	qv, qi := appendQuad(nil, nil, x, y, w, h, col)
	if len(qv) == 0 {
		return
	}
	t.appendObjs(panels, t.ShapeFn(qv, qi, t.opacityMul))
}

// appendObjs stamps the current clip onto what a builder callback returned and
// appends it.
//
// Every PanelFn, IconFn and ShapeFn result in this file goes through here, for
// the same reason every opacity goes through panelOpacity: a clip that reaches
// only some of them is a list whose rows clip and whose icons do not.
//
// A callback that set a Clip of its own keeps it, intersected rather than
// replaced -- a game clipping its own icon to a mask is not asking to have that
// mask dropped because the icon happens to sit in a list.
func (t *WidgetTree) appendObjs(panels *[]renderer.UIRenderObject, objs []renderer.UIRenderObject) {
	if t.clip == nil {
		*panels = append(*panels, objs...)
		return
	}
	for _, o := range objs {
		if o.Clip != nil {
			c := o.Clip.Intersect(*t.clip)
			o.Clip = &c
		} else {
			o.Clip = t.clip
		}
		*panels = append(*panels, o)
	}
}

// appendText stamps the current clip onto a line and appends it. A clipped line
// is trimmed glyph by glyph in the renderer; see renderer.TextLine.Clip.
func (t *WidgetTree) appendText(text *[]renderer.TextLine, tl renderer.TextLine) {
	tl.Clip = t.clip
	*text = append(*text, tl)
}

// modulateColor multiplies each channel by factor, clamped to [0, 1].
func modulateColor(c [3]float32, factor float32) [3]float32 {
	return [3]float32{
		clamp01(c[0] * factor),
		clamp01(c[1] * factor),
		clamp01(c[2] * factor),
	}
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// resolveColor returns the widget color, checking ColorKey for dynamic overrides.
func (t *WidgetTree) resolveColor(def *WidgetDef) [3]float32 {
	if def.ColorKey != "" {
		if c, ok := t.colorBindings[def.ColorKey]; ok {
			return c
		}
	}
	return def.Color
}

// SetChildren replaces the children of the node identified by parentID.
// Old children are removed from the index; new children are built and indexed.
func (t *WidgetTree) SetChildren(parentID string, defs []WidgetDef) {
	parent := t.index[parentID]
	if parent == nil {
		return
	}
	// Remove old children from index.
	var unindex func(n *Node)
	unindex = func(n *Node) {
		if n.ID != "" {
			delete(t.index, n.ID)
		}
		for _, c := range n.Children {
			unindex(c)
		}
	}
	for _, c := range parent.Children {
		unindex(c)
	}
	// Build new children.
	parent.Children = nil
	parent.Def.Children = defs
	for i := range parent.Def.Children {
		child := t.buildNode(&parent.Def.Children[i])
		parent.Children = append(parent.Children, child)
	}
}

// edgeSkirt is how far a flat quad is grown past its own edge, in pixels, to
// give ui.frag's coverage ramp an outside half.
//
// It is ui.edgeSkirt's value, duplicated the way renderer.edgeSkirt is: this
// package takes its GPU work as callbacks precisely so it never depends on the
// widget toolkit, and importing ui for one float would undo that. Change one
// and change all three.
const edgeSkirt = 0.5

// appendQuad appends 4 vertices and 6 indices for a solid-color rectangle.
//
// This is ui.AppendQuad, and it has to stay that: the quad is emitted half a
// pixel larger than asked for on every side, with UVs running from just below 0
// to just above 1 so that UV 0 and 1 land on the requested edge. ui.frag turns
// that into coverage; see edgeCoverage there.
//
// Writing no UV at all is not a softer edge, it is a wrong colour everywhere.
// Four vertices at (0, 0) make fwidth(uv) zero, so edgeCoverage's distance and
// its pixel scale both vanish and the ramp lands on its clamp of 0.5 across the
// whole quad -- every flat bg_color panel and every non-nine-slice progress bar
// composited at half alpha, edge to edge, whatever opacity asked for (#144).
// The shader's "a quad that was not expanded still gets the inner half" only
// holds for an emitter that writes corner UVs.
//
// A zero-width or zero-height quad is dropped rather than grown. An empty
// progress bar asks for exactly that, and a skirt around nothing is a visible
// one-pixel sliver where the bar is supposed to be empty.
func appendQuad(verts []renderer.Vertex, idxs []uint16, x, y, w, h float32, col [3]float32) ([]renderer.Vertex, []uint16) {
	return appendQuadClipped(verts, idxs, x, y, w, h, col, nil)
}

// appendQuadClipped is appendQuad confined to a rectangle; a nil clip is
// appendQuad exactly.
//
// The trim moves the UVs with the corners, which is what keeps the coverage
// ramp meaning what it means: the un-cut edges still carry 0 and 1 on the
// requested edge and still get their half-pixel of ramp, and the cut edge lands
// on an interior UV, where edgeCoverage is already fully covered -- so the quad
// simply stops there, hard, which is what a scissor would have done to it.
//
// Trimming the skirted rect rather than the requested one is deliberate: the
// skirt is half a pixel of ramp OUTSIDE the quad, and cutting the requested
// rect first would leave that half pixel hanging past the clip.
func appendQuadClipped(verts []renderer.Vertex, idxs []uint16, x, y, w, h float32, col [3]float32, clip *renderer.ClipRect) ([]renderer.Vertex, []uint16) {
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

	if clip != nil {
		var ok bool
		x0, y0, x1, y1, u0, v0, u1, v1, ok = clip.TrimQuad(x0, y0, x1, y1, u0, v0, u1, v1)
		if !ok {
			return verts, idxs
		}
	}

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
