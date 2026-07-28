package yamlui

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	bindings := map[string]string{
		"name":  "Player1",
		"level": "5",
	}

	tests := []struct {
		template string
		want     string
	}{
		{"{name}", "Player1"},
		{"Lv. {level}", "Lv. 5"},
		{"no placeholders", "no placeholders"},
		{"{name} ({level})", "Player1 (5)"},
		{"{missing}", "{missing}"},
		{"", ""},
	}

	for _, tt := range tests {
		got := resolve(tt.template, bindings)
		if got != tt.want {
			t.Errorf("resolve(%q) = %q, want %q", tt.template, got, tt.want)
		}
	}
}

func TestResolveFloat(t *testing.T) {
	bindings := map[string]string{
		"hp":     "85.5",
		"max_hp": "100",
	}

	tests := []struct {
		template string
		want     float32
	}{
		{"{hp}", 85.5},
		{"{max_hp}", 100},
		{"42.5", 42.5},
		{"{missing}", 0},
		{"", 0},
	}

	for _, tt := range tests {
		got := resolveFloat(tt.template, bindings)
		if math.Abs(float64(got-tt.want)) > 0.001 {
			t.Errorf("resolveFloat(%q) = %f, want %f", tt.template, got, tt.want)
		}
	}
}

func TestParseFS(t *testing.T) {
	yaml := `widget: panel
width: 280
height: 90
padding: 10
gap: 3
children:
  - widget: label
    id: name
    text: "{name}"
    font_size: 14
  - widget: progress_bar
    id: hp_bar
    height: 10
    value: "{hp}"
    max: "{max_hp}"
    fg_color: [0, 0.8, 0]
    bg_color: [0.2, 0.05, 0.05]
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	def, err := ParseFS(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}

	if def.Widget != "panel" {
		t.Errorf("root widget = %q, want panel", def.Widget)
	}
	if def.Width != 280 {
		t.Errorf("root width = %f, want 280", def.Width)
	}
	if len(def.Children) != 2 {
		t.Fatalf("children count = %d, want 2", len(def.Children))
	}
	if def.Children[0].Widget != "label" {
		t.Errorf("child 0 widget = %q, want label", def.Children[0].Widget)
	}
	if def.Children[0].ID != "name" {
		t.Errorf("child 0 id = %q, want name", def.Children[0].ID)
	}
	if def.Children[1].Widget != "progress_bar" {
		t.Errorf("child 1 widget = %q, want progress_bar", def.Children[1].Widget)
	}
	if def.Children[1].FgColor != [3]float32{0, 0.8, 0} {
		t.Errorf("child 1 fg_color = %v, want [0, 0.8, 0]", def.Children[1].FgColor)
	}
}

func TestLoadAndBind(t *testing.T) {
	yaml := `widget: panel
width: 200
height: 50
padding: 5
gap: 2
children:
  - widget: label
    id: name
    text: "{name}"
    font_size: 12
  - widget: progress_bar
    id: bar
    height: 8
    value: "{val}"
    max: "{max}"
    fg_color: [0, 1, 0]
    bg_color: [0.1, 0.1, 0.1]
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	tree, err := Load(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}

	if tree.Root == nil {
		t.Fatal("root is nil")
	}
	if tree.Root.Def.Widget != "panel" {
		t.Errorf("root widget = %q, want panel", tree.Root.Def.Widget)
	}
	if len(tree.Root.Children) != 2 {
		t.Fatalf("root children = %d, want 2", len(tree.Root.Children))
	}

	// Test ID index.
	nameNode := tree.NodeByID("name")
	if nameNode == nil {
		t.Fatal("node 'name' not found in index")
	}
	barNode := tree.NodeByID("bar")
	if barNode == nil {
		t.Fatal("node 'bar' not found in index")
	}

	// Test bindings.
	tree.Bind("name", "TestPlayer")
	tree.BindFloat("val", 75)
	tree.BindFloat("max", 100)

	if tree.bindings["name"] != "TestPlayer" {
		t.Errorf("binding name = %q, want TestPlayer", tree.bindings["name"])
	}
}

func TestLayoutVertical(t *testing.T) {
	def := &WidgetDef{
		Widget:  "panel",
		Width:   200,
		Height:  100,
		Padding: 10,
		Gap:     5,
		Children: []WidgetDef{
			{Widget: "label", ID: "a", FontSize: 14},
			{Widget: "progress_bar", ID: "b", Height: 10},
		},
	}

	tree := &WidgetTree{
		index:         make(map[string]*Node),
		bindings:      make(map[string]string),
		colorBindings: make(map[string][3]float32),
	}
	root := tree.buildNode(def)
	scale := float32(1.0)
	root.Rect = Rect{X: 0, Y: 0, W: 200, H: 100}
	layoutChildren(root, root.Rect, scale)

	// First child: label at Y=10 (padding), X=10, W=180.
	a := root.Children[0]
	if a.Rect.X != 10 {
		t.Errorf("child a X = %f, want 10", a.Rect.X)
	}
	if a.Rect.Y != 10 {
		t.Errorf("child a Y = %f, want 10", a.Rect.Y)
	}
	if a.Rect.W != 180 {
		t.Errorf("child a W = %f, want 180", a.Rect.W)
	}
	if a.Rect.H != 14 {
		t.Errorf("child a H = %f, want 14", a.Rect.H)
	}

	// Second child: bar at Y=10+14+5=29.
	b := root.Children[1]
	if b.Rect.Y != 29 {
		t.Errorf("child b Y = %f, want 29", b.Rect.Y)
	}
	if b.Rect.H != 10 {
		t.Errorf("child b H = %f, want 10", b.Rect.H)
	}
}

func TestLayoutOverlay(t *testing.T) {
	def := &WidgetDef{
		Widget:  "panel",
		Width:   200,
		Height:  100,
		Padding: 10,
		Gap:     5,
		Children: []WidgetDef{
			{Widget: "label", ID: "name", FontSize: 14},
			{Widget: "label", ID: "level", FontSize: 12, Overlay: "name"},
			{Widget: "progress_bar", ID: "bar", Height: 10},
		},
	}

	tree := &WidgetTree{
		index:         make(map[string]*Node),
		bindings:      make(map[string]string),
		colorBindings: make(map[string][3]float32),
	}
	root := tree.buildNode(def)
	scale := float32(1.0)
	root.Rect = Rect{X: 0, Y: 0, W: 200, H: 100}
	layoutChildren(root, root.Rect, scale)

	name := root.Children[0]
	level := root.Children[1]
	bar := root.Children[2]

	// Overlay should share Y with the "name" node.
	if level.Rect.Y != name.Rect.Y {
		t.Errorf("overlay Y = %f, want %f (same as name)", level.Rect.Y, name.Rect.Y)
	}

	// Bar should come after name (overlay doesn't advance cursor).
	expectedBarY := float32(10 + 14 + 5) // padding + label height + gap
	if bar.Rect.Y != expectedBarY {
		t.Errorf("bar Y = %f, want %f", bar.Rect.Y, expectedBarY)
	}
}

func TestLayoutScale(t *testing.T) {
	def := &WidgetDef{
		Widget:  "panel",
		Width:   200,
		Height:  100,
		Padding: 10,
		Gap:     5,
		Children: []WidgetDef{
			{Widget: "progress_bar", ID: "bar", Height: 10},
		},
	}

	tree := &WidgetTree{
		index:         make(map[string]*Node),
		bindings:      make(map[string]string),
		colorBindings: make(map[string][3]float32),
	}
	root := tree.buildNode(def)
	scale := float32(2.0)
	root.Rect = Rect{X: 0, Y: 0, W: 400, H: 200}
	layoutChildren(root, root.Rect, scale)

	bar := root.Children[0]
	// At 2x scale: padding=20, height=20, innerW=360.
	if bar.Rect.X != 20 {
		t.Errorf("bar X = %f, want 20", bar.Rect.X)
	}
	if bar.Rect.Y != 20 {
		t.Errorf("bar Y = %f, want 20", bar.Rect.Y)
	}
	if bar.Rect.W != 360 {
		t.Errorf("bar W = %f, want 360", bar.Rect.W)
	}
	if bar.Rect.H != 20 {
		t.Errorf("bar H = %f, want 20", bar.Rect.H)
	}
}

func TestHorizontalFlex(t *testing.T) {
	// Container 300 wide, padding 10 => inner 280, gap 10.
	// Child A: fixed 60. Child B: flex 2. Child C: flex 1.
	// Remaining = 280 - 60 - 2*10(gaps) = 200. B gets 2/3*200≈133.33, C gets 1/3*200≈66.67.
	def := &WidgetDef{
		Widget:  "panel",
		Width:   300,
		Height:  50,
		Padding: 10,
		Layout:  "horizontal",
		Gap:     10,
		Children: []WidgetDef{
			{Widget: "panel", ID: "a", Width: 60},
			{Widget: "panel", ID: "b", Flex: 2},
			{Widget: "panel", ID: "c", Flex: 1},
		},
	}

	tree := &WidgetTree{
		index:         make(map[string]*Node),
		bindings:      make(map[string]string),
		colorBindings: make(map[string][3]float32),
	}
	root := tree.buildNode(def)
	root.Rect = Rect{X: 0, Y: 0, W: 300, H: 50}
	layoutChildren(root, root.Rect, 1.0)

	a := root.Children[0]
	b := root.Children[1]
	c := root.Children[2]

	if a.Rect.W != 60 {
		t.Errorf("child a W = %f, want 60", a.Rect.W)
	}

	// remaining = 280 - 60 - 20 = 200
	expectB := float32(200) * 2 / 3
	expectC := float32(200) * 1 / 3
	if math.Abs(float64(b.Rect.W-expectB)) > 0.1 {
		t.Errorf("child b W = %f, want ~%f", b.Rect.W, expectB)
	}
	if math.Abs(float64(c.Rect.W-expectC)) > 0.1 {
		t.Errorf("child c W = %f, want ~%f", c.Rect.W, expectC)
	}
}

func TestVerticalFlex(t *testing.T) {
	// Container 200 tall, padding 10 => inner 180, gap 5.
	// Child A: fixed height 20. Child B: flex 1.
	// Remaining = 180 - 20 - 5(gap) = 155. B gets all 155.
	def := &WidgetDef{
		Widget:  "panel",
		Width:   100,
		Height:  200,
		Padding: 10,
		Gap:     5,
		Children: []WidgetDef{
			{Widget: "panel", ID: "a", Height: 20},
			{Widget: "panel", ID: "b", Flex: 1},
		},
	}

	tree := &WidgetTree{
		index:         make(map[string]*Node),
		bindings:      make(map[string]string),
		colorBindings: make(map[string][3]float32),
	}
	root := tree.buildNode(def)
	root.Rect = Rect{X: 0, Y: 0, W: 100, H: 200}
	layoutChildren(root, root.Rect, 1.0)

	a := root.Children[0]
	b := root.Children[1]

	if a.Rect.H != 20 {
		t.Errorf("child a H = %f, want 20", a.Rect.H)
	}
	if math.Abs(float64(b.Rect.H-155)) > 0.1 {
		t.Errorf("child b H = %f, want 155", b.Rect.H)
	}
}

func TestNaturalHeight(t *testing.T) {
	// A vertical container with nested horizontal rows of buttons (no explicit height on rows).
	// Row natural height = max(button heights) = 46. Container = 2*pad + 2*46 + gap = 0 + 92 + 10 = 102.
	def := &WidgetDef{
		Widget: "panel",
		Width:  300,
		Height: 400,
		Children: []WidgetDef{
			{Widget: "panel", ID: "grid", Gap: 10, Children: []WidgetDef{
				{Widget: "panel", Layout: "horizontal", Gap: 5, Children: []WidgetDef{
					{Widget: "button", Width: 46, Height: 46},
					{Widget: "button", Width: 46, Height: 46},
				}},
				{Widget: "panel", Layout: "horizontal", Gap: 5, Children: []WidgetDef{
					{Widget: "button", Width: 46, Height: 46},
					{Widget: "button", Width: 46, Height: 46},
				}},
			}},
		},
	}

	tree := &WidgetTree{
		index:         make(map[string]*Node),
		bindings:      make(map[string]string),
		colorBindings: make(map[string][3]float32),
	}
	root := tree.buildNode(def)
	root.Rect = Rect{X: 0, Y: 0, W: 300, H: 400}
	layoutChildren(root, root.Rect, 1.0)

	grid := root.Children[0]
	// Grid natural height = 2*0(pad) + 46 + 46 + 10(gap) = 102.
	if grid.Rect.H != 102 {
		t.Errorf("grid H = %f, want 102", grid.Rect.H)
	}
	// Row 1 at Y=0, Row 2 at Y=46+10=56.
	row1 := grid.Children[0]
	row2 := grid.Children[1]
	if row1.Rect.H != 46 {
		t.Errorf("row1 H = %f, want 46", row1.Rect.H)
	}
	if row2.Rect.Y != 56 {
		t.Errorf("row2 Y = %f, want 56", row2.Rect.Y)
	}
}

func TestParseNewFields(t *testing.T) {
	yaml := `widget: panel
width: 100
height: 50
children:
  - widget: button
    id: btn
    on_click: do_thing
    flex: 2.5
    font: display
    visible: "{show}"
    color_key: my_color
    children:
      - widget: icon
        sprite: sword_icon
        placeholder_color: [0.5, 0.5, 0.5]
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	def, err := ParseFS(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}

	btn := def.Children[0]
	if btn.OnClick != "do_thing" {
		t.Errorf("on_click = %q, want do_thing", btn.OnClick)
	}
	if btn.Flex != 2.5 {
		t.Errorf("flex = %f, want 2.5", btn.Flex)
	}
	if btn.Font != "display" {
		t.Errorf("font = %q, want display", btn.Font)
	}
	if btn.Visible != "{show}" {
		t.Errorf("visible = %q, want {show}", btn.Visible)
	}
	if btn.ColorKey != "my_color" {
		t.Errorf("color_key = %q, want my_color", btn.ColorKey)
	}

	icon := btn.Children[0]
	if icon.Sprite != "sword_icon" {
		t.Errorf("sprite = %q, want sword_icon", icon.Sprite)
	}
	if icon.PlaceholderColor != [3]float32{0.5, 0.5, 0.5} {
		t.Errorf("placeholder_color = %v, want [0.5 0.5 0.5]", icon.PlaceholderColor)
	}
}

func TestSetChildren(t *testing.T) {
	def := &WidgetDef{
		Widget: "panel",
		ID:     "root",
		Width:  200,
		Height: 100,
		Children: []WidgetDef{
			{Widget: "panel", ID: "container"},
		},
	}

	tree := &WidgetTree{
		index:         make(map[string]*Node),
		bindings:      make(map[string]string),
		colorBindings: make(map[string][3]float32),
	}
	tree.Root = tree.buildNode(def)

	container := tree.NodeByID("container")
	if container == nil {
		t.Fatal("container not found")
	}
	if len(container.Children) != 0 {
		t.Fatalf("initial children = %d, want 0", len(container.Children))
	}

	// Add children dynamically.
	tree.SetChildren("container", []WidgetDef{
		{Widget: "label", ID: "child_a", Text: "A", FontSize: 12},
		{Widget: "label", ID: "child_b", Text: "B", FontSize: 12},
	})

	if len(container.Children) != 2 {
		t.Fatalf("after SetChildren = %d, want 2", len(container.Children))
	}
	if tree.NodeByID("child_a") == nil {
		t.Error("child_a not indexed")
	}
	if tree.NodeByID("child_b") == nil {
		t.Error("child_b not indexed")
	}

	// Replace children, old ones should be unindexed.
	tree.SetChildren("container", []WidgetDef{
		{Widget: "label", ID: "child_c", Text: "C", FontSize: 12},
	})

	if len(container.Children) != 1 {
		t.Fatalf("after replace = %d, want 1", len(container.Children))
	}
	if tree.NodeByID("child_a") != nil {
		t.Error("child_a should be unindexed after replace")
	}
	if tree.NodeByID("child_c") == nil {
		t.Error("child_c not indexed")
	}
}

// TestParseKitchenSink round-trips a realistic, deeply nested layout that
// exercises every supported widget type and the full set of layout and
// binding attributes. The fixture lives in testdata/ so the engine's tests
// stay self-contained — they must not reach into a consuming game's assets.
func TestParseKitchenSink(t *testing.T) {
	path := filepath.Join("testdata", "kitchen_sink.yaml")

	def, err := ParseFS(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	if def.Widget != "panel" {
		t.Errorf("root widget = %q, want panel", def.Widget)
	}
	if def.ID != "root_window" {
		t.Errorf("root id = %q, want root_window", def.ID)
	}
	if def.Width != 640 || def.Height != 480 {
		t.Errorf("root size = %fx%f, want 640x480", def.Width, def.Height)
	}

	tree, err := Load(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}

	// Every widget type must survive parse and land in the ID index.
	for _, id := range []string{
		"header",          // panel
		"title_label",     // label
		"action_button",   // button
		"action_progress", // progress_bar
		"item_scroll",     // scroll_view
		"detail_icon",     // icon
		"filter_input",    // text_input
	} {
		if tree.NodeByID(id) == nil {
			t.Errorf("node %q not found", id)
		}
	}

	// Deeply nested nodes must be indexed too, not just top-level ones.
	for _, id := range []string{"detail_view", "detail_name", "detail_scroll", "action_bar"} {
		if tree.NodeByID(id) == nil {
			t.Errorf("nested node %q not found", id)
		}
	}
}
