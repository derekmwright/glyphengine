// Command 20-screens is a main menu, a world, and a pause menu over it.
//
// The engine ships no scene manager and no screen stack, deliberately: those
// would be structure rather than capability, and your program owns main(). What
// it ships instead are the two things a game cannot do for itself. This example
// is the pattern built out of them, and it is about ninety lines of game code.
//
//   - **Swapping scenes is an assignment.** Engine embeds *Scene as an exported
//     field, so `e.Scene = g.world` is the whole of it. GPU resources — meshes,
//     textures, materials — live on the Renderer and survive the swap, so
//     nothing is re-uploaded and nothing leaks.
//
//   - **Pausing is SetTimeScale(0).** A game cannot pause itself: Scene.Tick
//     runs before FixedUpdate, so returning early there stops the game's own
//     simulation and none of the engine's. Watch the cube: it keeps spinning on
//     the menu, because that is Update, and stops dead when paused, because
//     that is the tick.
//
//   - **Menus work on the keyboard.** Up/Down or W/S move the highlight, Enter
//     or Space activates. The mouse writes to the same highlight rather than
//     keeping its own, so moving the mouse and then pressing Enter picks what
//     is under the mouse.
//
//   - **A container scrolls to what has focus.** -scroll puts a twelve-entry
//     inventory beside the menu, six of which fit. The arrow keys move focus
//     through it and the game says nothing more than SetFocus("row7"); where
//     the view has to go to show that row is the container's business. Watch
//     the row at the top edge as it moves: the clip trims the glyph quads, so
//     letters lose their tops rather than the line disappearing whole.
//
//     go run ./20-screens              # windowed
//     go run ./20-screens -frames 60   # render 60 frames, then exit
//     go run ./20-screens -scroll      # the inventory, on the arrow keys
//
// Escape opens and closes the pause menu.
package main

import (
	"flag"
	"fmt"
	"log"
	"runtime"

	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/input"
	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/ui"
	"github.com/derekmwright/glyphengine/ui/yamlui"
	xsky "github.com/derekmwright/glyphengine/x/sky"
)

func init() {
	// GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

// screen is the game's own state machine. The engine has no opinion about it,
// which is the point — a stack, a graph or three booleans would all work, and
// none of them is the engine's business.
type screen int

const (
	screenMenu screen = iota
	screenPlaying
	screenPaused
)

type game struct {
	screen screen

	// Two scenes, swapped by assignment. The menu one is empty apart from its
	// sky; the world one holds the cube and its ground.
	menuScene  *glyph.Scene
	worldScene *glyph.Scene

	camera *glyph.Camera
	cube   glyph.Entity

	uiMgr   *ui.UIManager
	buttons []*ui.Button
	slice   *renderer.NineSlice
	font    *renderer.Font
	text    *renderer.MSDFText

	// The -scroll demo: a twelve-entry list beside the menu that the arrow
	// keys move focus through, so a row that is not on screen has to be
	// brought there. Everything here stays nil when the flag is absent, the
	// same bargain 13-ui's -yamlui HUD makes -- `task screenshots` renders
	// this example with stock flags and compares the image byte for byte.
	scroll     bool
	listTree   *yamlui.WidgetTree
	listAssets *yamlui.AssetProvider
	listMesh   *renderer.Mesh
	listProj   [16]float32
	listFocus  int
}

// listRows is how many entries menulist.yaml holds. Six fit in the view, which
// is what makes the other six worth scrolling to.
const listRows = 12

// listMaxQuads bounds the one mesh every flat quad in the list goes into: a
// background and a fill per row, the root panel, and the bar.
const listMaxQuads = 2*listRows + 4

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()

	// A flat white 9-slice, so the example needs no art. The UI shader's panel
	// mode reads the texture's alpha to separate frame from fill; an opaque
	// texel is all border, which is what a solid button wants.
	white := []byte{255, 255, 255, 255}
	tex, err := r.CreateTextureNearest(white, 1, 1)
	if err != nil {
		return err
	}
	g.slice = renderer.NewNineSlice(tex, 1, 0)

	if g.font, err = renderer.LoadFont(r, assetsFS,
		"assets/fonts/goregular.json", "assets/fonts/goregular.png"); err != nil {
		return err
	}
	if g.text, err = renderer.NewMSDFText(r, g.font); err != nil {
		return err
	}

	// ── the world, built once and kept ──
	//
	// Engine.Scene is whatever it was constructed with; this becomes the world
	// and a fresh one becomes the menu backdrop. Building both up front means a
	// screen change never waits on a load.
	g.worldScene = e.Scene
	g.menuScene = glyph.NewScene()

	ground, err := r.CreatePlane(40, 40)
	if err != nil {
		return err
	}
	groundEnt := g.worldScene.Spawn()
	g.worldScene.C.Transform.Set(groundEnt, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	g.worldScene.C.MeshRef.Set(groundEnt, &glyph.MeshRef{Mesh: ground, Roughness: 0.9})
	g.worldScene.C.Color.Set(groundEnt, &glyph.Color{R: 0.33, G: 0.40, B: 0.29})
	g.worldScene.C.Static.Set(groundEnt, &glyph.Static{})

	cube, err := r.CreateCube(1.0)
	if err != nil {
		return err
	}
	g.cube = g.worldScene.Spawn()
	g.worldScene.C.Transform.Set(g.cube, &glyph.Transform{Position: mgl32.Vec3{0, 1, 0}, Scale: mgl32.Vec3{1, 1, 1}})
	g.worldScene.C.MeshRef.Set(g.cube, &glyph.MeshRef{Mesh: cube, Metallic: 0.1, Roughness: 0.4})
	g.worldScene.C.Color.Set(g.cube, &glyph.Color{R: 0.85, G: 0.45, B: 0.25})

	// A spinner the tick drives, so "paused" is visible rather than asserted.
	g.worldScene.AddSystem(func(s *glyph.Scene, dt float32) {
		if t, ok := s.C.Transform.Get(g.cube); ok {
			t.Rotation[1] += 1.2 * dt
		}
	})

	worldEnv := xsky.DefaultEnvironment()
	worldEnv.Cycle.TimeOfDay = 0.35
	worldEnv.Cycle.Speed = 1.0 / 60.0
	g.worldScene.Env = worldEnv

	menuEnv := xsky.DefaultEnvironment()
	menuEnv.Cycle.TimeOfDay = 0.78 // dusk behind the menu
	g.menuScene.Env = menuEnv

	g.camera = glyph.NewCamera(8)
	g.camera.Target = mgl32.Vec3{0, 1, 0}

	if g.scroll {
		if err := g.initList(e); err != nil {
			return err
		}
	}

	g.uiMgr = ui.NewUIManager()
	if err := g.showMenu(e); err != nil {
		return err
	}

	// Open on the menu scene. Engine.Scene is the world it was constructed
	// with, so this first swap is the same assignment every later one is —
	// which is the point worth seeing: the cube appears when Play swaps it
	// back, and nothing was loaded or unloaded to make that happen.
	e.Scene = g.menuScene

	log.Println("20-screens running. Arrows or W/S move, Enter selects, Escape pauses.")
	return nil
}

// button builds one menu entry. Registering it as navigable is what puts it in
// the keyboard traversal order; registering it as clickable is what puts it
// under the mouse. A menu entry wants both.
func (g *game) button(e *glyph.Engine, label string, y float32, onClick func()) error {
	b, err := ui.NewButton(e.Renderer(), g.slice, [3]float32{0.16, 0.19, 0.24}, 0.92)
	if err != nil {
		return err
	}
	b.Label, b.FontSize = label, 16
	b.Width, b.Height = 220, 44
	b.Anchor, b.OffsetX, b.OffsetY = ui.AnchorCenter, 0, y
	b.OnClickFn = onClick

	// In the -scroll demo the arrow keys belong to the list, so the menu
	// entries are left off the keyboard traversal order rather than fighting
	// it for the same keys. They are still clickable, which is how you get
	// into the world from there.
	if !g.scroll {
		g.uiMgr.RegisterNavigable(b)
	}
	g.uiMgr.RegisterClickable(b)
	g.buttons = append(g.buttons, b)
	return nil
}

// rebuild replaces the current menu. ClearNavigables drops the old traversal
// order so the highlight cannot point into a menu that is gone.
func (g *game) rebuild(e *glyph.Engine, build func() error) error {
	for _, b := range g.buttons {
		b.Destroy(e.Renderer())
	}
	g.buttons = nil
	g.uiMgr.ClearNavigables()
	g.uiMgr.ClearClickables()
	return build()
}

func (g *game) showMenu(e *glyph.Engine) error {
	return g.rebuild(e, func() error {
		if err := g.button(e, "Play", -30, func() { g.play(e) }); err != nil {
			return err
		}
		return g.button(e, "Quit", 30, e.Close)
	})
}

func (g *game) showPause(e *glyph.Engine) error {
	return g.rebuild(e, func() error {
		if err := g.button(e, "Resume", -30, func() { g.resume(e) }); err != nil {
			return err
		}
		return g.button(e, "Main Menu", 30, func() { g.toMenu(e) })
	})
}

// ── the three transitions, which is all a "scene manager" would have done ──

func (g *game) play(e *glyph.Engine) {
	g.screen = screenPlaying
	e.Scene = g.worldScene // the swap, in full
	e.SetTimeScale(1)
	_ = g.rebuild(e, func() error { return nil })
}

func (g *game) pause(e *glyph.Engine) {
	g.screen = screenPaused
	e.SetTimeScale(0) // the world stops; Update and rendering carry on
	_ = g.showPause(e)
}

func (g *game) resume(e *glyph.Engine) {
	g.screen = screenPlaying
	e.SetTimeScale(1)
	_ = g.rebuild(e, func() error { return nil })
}

func (g *game) toMenu(e *glyph.Engine) {
	g.screen = screenMenu
	e.Scene = g.menuScene
	e.SetTimeScale(1) // the menu scene has nothing to simulate, but leaving the
	_ = g.showMenu(e) // clock stopped would freeze its sky too
}

func (g *game) Update(e *glyph.Engine, _ float32) {
	in := e.Input()

	// Escape toggles the pause menu, and only while playing. On the main menu
	// there is nothing to pause and nothing to return to.
	if in.KeyPressed(input.KeyEscape) {
		switch g.screen {
		case screenPlaying:
			g.pause(e)
		case screenPaused:
			g.resume(e)
		}
	}

	// One call drives both the pointer and the keyboard. It has to come before
	// the game reads input, so ConsumedKeyboard can tell the game the menu took
	// the key.
	g.uiMgr.UpdateScale(g.screenSize(e))
	g.uiMgr.HandleInput(in)

	g.moveListFocus(in)

	g.camera.Update(in)
	e.SetCamera(g.camera.ViewVectors())

	g.buildUI(e)
}

// Shutdown frees the one GPU resource the -scroll demo creates. It is a no-op
// without the flag, because without it there is nothing to free.
func (g *game) Shutdown(e *glyph.Engine) {
	if g.listMesh != nil {
		e.Renderer().DestroyMesh(g.listMesh)
		g.listMesh = nil
	}
}

// initList loads the list and the one mesh every flat quad in it goes into.
func (g *game) initList(e *glyph.Engine) error {
	tree, err := yamlui.Load(assetsFS, "assets/ui/menulist.yaml")
	if err != nil {
		return err
	}
	if g.listMesh, err = e.Renderer().CreateDynamicIndexedMesh(listMaxQuads*4, listMaxQuads*6); err != nil {
		return err
	}
	g.listTree = tree
	g.listAssets = &yamlui.AssetProvider{Font: g.font}
	g.listTree.SetFocus("row0")
	return nil
}

// moveListFocus is the whole keyboard path: the arrow keys pick a row, and
// SetFocus is the only thing this example tells the widget tree.
//
// Where the view has to go to show that row is the tree's business, not the
// game's -- which is the point. A row six places down is off screen, and the
// game neither knows that nor has to: it names a widget and the container that
// holds it scrolls the shortest distance that brings it into sight.
func (g *game) moveListFocus(in *input.Input) {
	if g.listTree == nil {
		return
	}
	switch {
	case in.KeyPressed(input.KeyDown), in.KeyPressed(input.KeyS):
		g.listFocus++
	case in.KeyPressed(input.KeyUp), in.KeyPressed(input.KeyW):
		g.listFocus--
	default:
		return
	}
	if g.listFocus < 0 {
		g.listFocus = 0
	}
	if g.listFocus >= listRows {
		g.listFocus = listRows - 1
	}
	g.listTree.SetFocus(fmt.Sprintf("row%d", g.listFocus))
}

// buildList lays the list out and returns its draws: one mesh of flat quads
// and the rows' labels.
func (g *game) buildList(e *glyph.Engine, sw, sh float32) ([]renderer.UIRenderObject, []renderer.TextLine) {
	if g.listTree == nil {
		return nil, nil
	}
	for i := 0; i < listRows; i++ {
		// The focused row's bar is full and every other row's is empty, which
		// is the highlight. Bound every frame because focus moves.
		v := float32(0)
		if i == g.listFocus {
			v = 100
		}
		g.listTree.BindFloat(fmt.Sprintf("v%d", i), v)
	}

	// UnscaledElapsed, not Elapsed: the pause menu is where a list like this
	// is read, and Elapsed does not advance at TimeScale 0 -- an eased scroll
	// driven off it would stop dead exactly where it is most wanted.
	g.listTree.SetTime(e.UnscaledElapsed())

	g.listProj = mgl32.Ortho(0, sw, 0, sh, -1, 1)
	_, verts, idxs, text := g.listTree.BuildAt(e.Renderer(), g.listAssets, 60, 120, 1, sw, sh)
	if len(verts) == 0 {
		return nil, text
	}
	e.Renderer().UpdateMeshData(g.listMesh, verts, idxs)
	return []renderer.UIRenderObject{{
		RenderObject: renderer.RenderObject{Mesh: g.listMesh, MVP: g.listProj},
		Opacity:      1,
	}}, text
}

func (g *game) screenSize(e *glyph.Engine) (float32, float32) {
	w, h := e.Renderer().Extent()
	return float32(w), float32(h)
}

func (g *game) buildUI(e *glyph.Engine) {
	sw, sh := g.screenSize(e)

	var panels []renderer.UIRenderObject
	var lines []renderer.TextLine

	for _, b := range g.buttons {
		p, tl := b.Build(e.Renderer(), g.uiMgr.Scale(), sw, sh, g.font)
		panels = append(panels, p...)
		lines = append(lines, tl...)
	}

	title, hint := "", ""
	switch g.screen {
	case screenMenu:
		title, hint = "GLYPHENGINE", "arrows or W/S, Enter to select"
	case screenPlaying:
		title, hint = "", "Escape to pause"
	case screenPaused:
		title, hint = "PAUSED", "the world is stopped; the camera still moves"
	}
	if title != "" {
		lines = append(lines, renderer.TextLine{
			Text: title, X: sw/2 - 120, Y: sh/2 - 120, Scale: 34,
			Color: [3]float32{0.92, 0.88, 0.72},
		})
	}
	lines = append(lines, renderer.TextLine{
		Text: hint, X: 24, Y: sh - 32, Scale: 14,
		Color: [3]float32{0.75, 0.78, 0.82},
	})

	listPanels, listText := g.buildList(e, sw, sh)
	panels = append(panels, listPanels...)
	lines = append(lines, listText...)

	g.text.SetText(e.Renderer(), lines, sw, sh)
	e.SetUIOverlays(panels)
	e.SetMSDFOverlays([]renderer.RenderObject{g.text.RenderObject(sw, sh, 48)})
}

func main() {
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	fullscreen := flag.Bool("fullscreen", false, "run fullscreen on the primary monitor")
	frames := flag.Int("frames", 0, "render N frames then exit (0 = run until closed)")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	scroll := flag.Bool("scroll", false, "show a scrollable inventory the arrow keys move focus through")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithShaders(xsky.Shaders()),
		glyph.WithTitle("GlyphEngine - 20 Screens"),
		glyph.WithWindowSize(*width, *height),
		glyph.WithMSAA(4),
	}
	if *fullscreen {
		opts = append(opts, glyph.WithFullscreen())
	}
	if *frames > 0 {
		opts = append(opts, glyph.WithMaxFrames(*frames))
	}
	if *shot != "" {
		opts = append(opts, glyph.WithScreenshot(*shot))
	}

	e, err := glyph.New(&game{scroll: *scroll}, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
}
