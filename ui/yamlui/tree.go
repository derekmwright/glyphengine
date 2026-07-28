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

	// Interactive state
	input       InputState
	events      []UIEvent
	hovered     map[string]bool
	pressed     map[string]bool
	scrollY     map[string]float32
	focusedID   string
	textBuffers map[string]string
	cursorTick  int
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
		scrollY:       make(map[string]float32),
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

	rootW := t.Root.Def.Width * scale
	rootH := t.Root.Def.Height * scale
	t.Root.Rect = Rect{X: x, Y: y, W: rootW, H: rootH}

	layoutChildren(t.Root, t.Root.Rect, scale, t.bindings)

	// Handle click-to-focus: if mouse pressed and no text_input claims it, clear focus.
	if t.input.MousePressed {
		t.focusedID = ""
	}

	var panels []renderer.UIRenderObject
	var verts []renderer.Vertex
	var idxs []uint16
	var text []renderer.TextLine

	t.renderNode(r, assets, t.Root, scale, sw, sh, &panels, &verts, &idxs, &text)
	return panels, verts, idxs, text
}

func (t *WidgetTree) renderNode(r *renderer.Renderer, assets *AssetProvider, node *Node, scale, sw, sh float32, panels *[]renderer.UIRenderObject, verts *[]renderer.Vertex, idxs *[]uint16, text *[]renderer.TextLine) {
	// Visibility check: if Visible is set and resolves to false, skip entirely.
	if node.Def.Visible != "" && !resolveBool(node.Def.Visible, t.bindings) {
		return
	}

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
			*verts, *idxs = appendQuad(*verts, *idxs, node.Rect.X, node.Rect.Y, node.Rect.W, node.Rect.H, def.BgColor)
		}
		return
	}
	nsName := resolve(def.NineSlice, t.bindings)
	slice, ok := assets.NineSlices[nsName]
	if !ok {
		return
	}
	color := t.resolveColor(def)
	opacity := def.Opacity
	if opacity == 0 {
		opacity = 1.0
	}
	objs := t.PanelFn(r, slice, color, opacity, node.Rect.X, node.Rect.Y, node.Rect.W, node.Rect.H, scale, sw, sh)
	*panels = append(*panels, objs...)
}

func (t *WidgetTree) renderLabel(r *renderer.Renderer, assets *AssetProvider, node *Node, scale, sw, sh float32, panels *[]renderer.UIRenderObject, text *[]renderer.TextLine) {
	def := node.Def

	// Render optional nine-slice background (e.g. craft quantity display).
	if def.NineSlice != "" && assets != nil && t.PanelFn != nil {
		nsName := resolve(def.NineSlice, t.bindings)
		if slice, ok := assets.NineSlices[nsName]; ok {
			opacity := def.Opacity
			if opacity == 0 {
				opacity = 1.0
			}
			color := t.resolveColor(def)
			objs := t.PanelFn(r, slice, color, opacity, node.Rect.X, node.Rect.Y, node.Rect.W, node.Rect.H, scale, sw, sh)
			*panels = append(*panels, objs...)
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
	}
	if resolvedFont != nil && resolvedFont != assets.Font {
		tl.Font = resolvedFont
	}
	*text = append(*text, tl)
}

func (t *WidgetTree) renderProgressBar(r *renderer.Renderer, assets *AssetProvider, node *Node, scale, sw, sh float32, panels *[]renderer.UIRenderObject, verts *[]renderer.Vertex, idxs *[]uint16) {
	def := node.Def
	value := resolveFloat(def.Value, t.bindings)
	max := resolveFloat(def.Max, t.bindings)

	white := [3]float32{1, 1, 1}

	// Background: nine-slice or quad.
	if def.NineSlice != "" && assets != nil && t.PanelFn != nil {
		if slice, ok := assets.NineSlices[def.NineSlice]; ok {
			objs := t.PanelFn(r, slice, white, 1.0, node.Rect.X, node.Rect.Y, node.Rect.W, node.Rect.H, scale, sw, sh)
			*panels = append(*panels, objs...)
		}
	} else {
		*verts, *idxs = appendQuad(*verts, *idxs, node.Rect.X, node.Rect.Y, node.Rect.W, node.Rect.H, def.BgColor)
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
					objs := t.PanelFn(r, slice, white, 1.0, node.Rect.X, node.Rect.Y, fillW, node.Rect.H, scale, sw, sh)
					*panels = append(*panels, objs...)
				}
			}
		} else {
			fgColor := def.FgColor
			if def.FgColorKey != "" {
				if c, ok := t.colorBindings[def.FgColorKey]; ok {
					fgColor = c
				}
			}
			*verts, *idxs = appendQuad(*verts, *idxs, node.Rect.X, node.Rect.Y, fillW, node.Rect.H, fgColor)
		}
	}
}

func (t *WidgetTree) renderButton(r *renderer.Renderer, assets *AssetProvider, node *Node, scale, sw, sh float32, panels *[]renderer.UIRenderObject, text *[]renderer.TextLine) {
	def := node.Def
	id := node.ID

	disabled := def.Disabled != "" && resolveBool(def.Disabled, t.bindings)

	// Hit testing.
	hover := !disabled && node.Rect.Contains(t.input.MouseX, t.input.MouseY)
	t.hovered[id] = hover

	if !disabled {
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

	// Render nine-slice background.
	if def.NineSlice != "" && assets != nil && t.PanelFn != nil {
		nsName := resolve(def.NineSlice, t.bindings)
		if slice, ok := assets.NineSlices[nsName]; ok {
			opacity := def.Opacity
			if opacity == 0 {
				opacity = 1.0
			}
			objs := t.PanelFn(r, slice, color, opacity, node.Rect.X, node.Rect.Y, node.Rect.W, node.Rect.H, scale, sw, sh)
			*panels = append(*panels, objs...)
		}
	}

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
		}
		if btnFont != nil && assets != nil && btnFont != assets.Font {
			tl.Font = btnFont
		}
		*text = append(*text, tl)
	}
}

func (t *WidgetTree) renderScrollView(r *renderer.Renderer, assets *AssetProvider, node *Node, scale, sw, sh float32, panels *[]renderer.UIRenderObject, verts *[]renderer.Vertex, idxs *[]uint16, text *[]renderer.TextLine) {
	def := node.Def
	id := node.ID

	// Render optional nine-slice background.
	if def.NineSlice != "" && assets != nil && t.PanelFn != nil {
		if slice, ok := assets.NineSlices[def.NineSlice]; ok {
			opacity := def.Opacity
			if opacity == 0 {
				opacity = 1.0
			}
			objs := t.PanelFn(r, slice, def.Color, opacity, node.Rect.X, node.Rect.Y, node.Rect.W, node.Rect.H, scale, sw, sh)
			*panels = append(*panels, objs...)
		}
	}

	pad := def.Padding * scale
	viewRect := Rect{
		X: node.Rect.X + pad,
		Y: node.Rect.Y + pad,
		W: node.Rect.W - 2*pad,
		H: node.Rect.H - 2*pad,
	}

	// Compute content height from child layout.
	var contentH float32
	gap := def.Gap * scale
	for i, child := range node.Children {
		childH := child.Def.Height * scale
		if childH <= 0 {
			childH = child.Def.FontSize * scale
		}
		contentH += childH
		if i < len(node.Children)-1 {
			contentH += gap
		}
	}

	// Handle scroll wheel.
	if node.Rect.Contains(t.input.MouseX, t.input.MouseY) && t.input.ScrollY != 0 {
		scroll := t.scrollY[id] - t.input.ScrollY*30*scale
		maxScroll := contentH - viewRect.H
		if maxScroll < 0 {
			maxScroll = 0
		}
		if scroll < 0 {
			scroll = 0
		}
		if scroll > maxScroll {
			scroll = maxScroll
		}
		t.scrollY[id] = scroll
	}

	scrollOff := t.scrollY[id]

	// Render children with scroll offset, clipping those outside the view.
	for _, child := range node.Children {
		// Apply scroll offset to child rect.
		shifted := child.Rect
		shifted.Y -= scrollOff

		// Skip children entirely outside the view rect.
		if shifted.Y+shifted.H < viewRect.Y || shifted.Y > viewRect.Y+viewRect.H {
			continue
		}

		// Temporarily shift the child's rect for rendering.
		origRect := child.Rect
		child.Rect = shifted
		t.renderNode(r, assets, child, scale, sw, sh, panels, verts, idxs, text)
		child.Rect = origRect
	}

	// Render scrollbar thumb if content exceeds view.
	if contentH > viewRect.H && contentH > 0 {
		scrollbarW := 4 * scale
		visibleRatio := viewRect.H / contentH
		thumbH := viewRect.H * visibleRatio
		if thumbH < 10*scale {
			thumbH = 10 * scale
		}
		maxScroll := contentH - viewRect.H
		var thumbY float32
		if maxScroll > 0 {
			thumbY = viewRect.Y + (scrollOff/maxScroll)*(viewRect.H-thumbH)
		} else {
			thumbY = viewRect.Y
		}
		thumbX := viewRect.X + viewRect.W - scrollbarW

		thumbColor := [3]float32{1, 1, 1}
		*verts, *idxs = appendQuad(*verts, *idxs, thumbX, thumbY, scrollbarW, thumbH, thumbColor)
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
	opacity := def.Opacity
	if opacity == 0 {
		opacity = 1.0
	}
	color := t.resolveColor(def)
	objs := t.IconFn(resolved, color, opacity, node.Rect.X, node.Rect.Y, node.Rect.W, node.Rect.H, sw, sh)
	*panels = append(*panels, objs...)
}

func (t *WidgetTree) renderTextInput(r *renderer.Renderer, assets *AssetProvider, node *Node, scale, sw, sh float32, panels *[]renderer.UIRenderObject, text *[]renderer.TextLine) {
	def := node.Def
	id := node.ID

	// Render optional nine-slice background.
	if def.NineSlice != "" && assets != nil && t.PanelFn != nil {
		if slice, ok := assets.NineSlices[def.NineSlice]; ok {
			opacity := def.Opacity
			if opacity == 0 {
				opacity = 1.0
			}
			objs := t.PanelFn(r, slice, def.Color, opacity, node.Rect.X, node.Rect.Y, node.Rect.W, node.Rect.H, scale, sw, sh)
			*panels = append(*panels, objs...)
		}
	}

	// Click to focus.
	if t.input.MousePressed && node.Rect.Contains(t.input.MouseX, t.input.MouseY) {
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
		*text = append(*text, renderer.TextLine{
			Text:  display,
			X:     tx,
			Y:     ty,
			Scale: fontSize,
			Color: def.FgColor,
			Font:  nonDefaultFont,
		})
	} else if def.Placeholder != "" {
		resolved := resolve(def.Placeholder, t.bindings)
		phColor := modulateColor(def.FgColor, 0.5)
		if def.PlaceholderColor != ([3]float32{}) {
			phColor = def.PlaceholderColor
		}
		*text = append(*text, renderer.TextLine{
			Text:  resolved,
			X:     tx,
			Y:     ty,
			Scale: fontSize,
			Color: phColor,
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

// appendQuad appends 4 vertices and 6 indices for a solid-color rectangle.
func appendQuad(verts []renderer.Vertex, idxs []uint16, x, y, w, h float32, col [3]float32) ([]renderer.Vertex, []uint16) {
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
