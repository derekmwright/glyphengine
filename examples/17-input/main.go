// Command 17-input is a controller test harness and a demonstration of named
// actions.
//
// Walk around with WASD or the left stick, look with the mouse or the right
// stick, sprint with Shift or the left bumper, jump with Space or the bottom face
// button. Nothing in Update names a device: it asks the bindings for "move",
// "look", "jump" and "sprint", and either kind of hardware answers.
//
// The panel along the bottom is a live readout of the first connected pad — both
// sticks as dots in boxes, both triggers as bars, and every button as a cell that
// lights while held. That is there for diagnosis rather than decoration. A wrong
// mapping, an uncentred stick, or a dead zone set too wide all look identical
// through gameplay ("the movement feels off") and are obvious here.
//
// Press Tab to rebind jump: the panel border turns amber, and the next key or
// button pressed becomes the new binding. That is the whole rebinding loop —
// CapturePressed, then Rebind.
//
//	go run ./17-input
//	go run ./17-input -frames 60
//
// The readout is drawn with ui.AppendQuad rather than text, so the example needs
// no font atlas and no assets at all. Pad names go to the log on connect.
package main

import (
	"flag"
	"log"
	"math"
	"runtime"

	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/input"
	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/ui"
)

func init() {
	// GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

const playerHalfHeight = 0.9

type game struct {
	camera *glyph.FPCamera
	player glyph.Entity
	intent glyph.MoveIntent

	// One handle per action, held rather than looked up by name. A typo here is a
	// compile error instead of an action that silently never fires.
	binds  *input.Bindings
	move   input.VectorID
	look   input.VectorID
	jump   input.Action
	sprint input.Action
	rebind input.Action
	quit   input.Action

	// jumpQueued latches the jump edge sampled in Update so FixedUpdate can
	// consume it. Reading an edge straight from FixedUpdate drops it on the
	// frames that run no tick — see the note above Engine.FixedUpdate.
	jumpQueued bool

	// awaitingRebind is set while the next press is being captured for jump.
	awaitingRebind bool

	uiMesh *renderer.Mesh

	// lastPad and padReported track the connected controller so a change is
	// logged once. The bool is load-bearing: "" is both the initial value and the
	// name of no pad, so without it the very first report, the one that says
	// whether a controller was found at all, is the one that gets skipped.
	lastPad     string
	padReported bool
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()

	ground, err := r.CreatePlane(80, 80)
	if err != nil {
		return err
	}
	groundEnt := e.Spawn()
	e.C.Transform.Set(groundEnt, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(groundEnt, &glyph.MeshRef{Mesh: ground, Roughness: 0.95})
	e.C.Color.Set(groundEnt, &glyph.Color{R: 0.28, G: 0.31, B: 0.27})
	// A collider, not just a mesh. The character controller finds the ground by
	// heightmap first and raycast second, and this scene has no heightmap, so
	// without a collision shape here the player falls through the floor forever
	// and the only symptom is a camera pointing at empty sky.
	e.C.Collider.Set(groundEnt, &glyph.Collider{HalfExtents: mgl32.Vec3{40, 0.05, 40}})
	e.C.Static.Set(groundEnt, &glyph.Static{})

	// Posts to give the movement a sense of scale and something to walk around.
	post, err := r.CreateCube(1.0)
	if err != nil {
		return err
	}
	for i := 0; i < 10; i++ {
		a := float64(i) / 10 * 2 * math.Pi
		p := e.Spawn()
		e.C.Transform.Set(p, &glyph.Transform{
			Position: mgl32.Vec3{float32(math.Cos(a)) * 9, 1.5, float32(math.Sin(a)) * 9},
			Scale:    mgl32.Vec3{0.8, 3, 0.8},
		})
		e.C.MeshRef.Set(p, &glyph.MeshRef{Mesh: post, Roughness: 0.8})
		e.C.Color.Set(p, &glyph.Color{R: 0.34, G: 0.31, B: 0.29})
		e.C.Collider.Set(p, &glyph.Collider{HalfExtents: mgl32.Vec3{0.4, 1.5, 0.4}})
		e.C.Static.Set(p, &glyph.Static{})
	}

	g.player = e.Spawn()
	e.C.Transform.Set(g.player, &glyph.Transform{
		Position: mgl32.Vec3{0, playerHalfHeight, 0},
		Scale:    mgl32.Vec3{1, 1, 1},
	})
	e.C.Collider.Set(g.player, &glyph.Collider{
		HalfExtents: mgl32.Vec3{0.4, playerHalfHeight, 0.4},
	})
	e.C.Velocity.Set(g.player, &glyph.Velocity{})
	cc := glyph.NewCharacterController()
	e.C.CharacterController.Set(g.player, &cc)
	e.RebuildStatics()

	// The bindings. Each action lists every source that should trigger it, so the
	// keyboard and the pad are equal citizens and neither is a special case.
	g.binds = input.NewBindings(e.Input())

	g.move = g.binds.Vector("move",
		input.Keyboard(input.KeyW), input.Keyboard(input.KeyS),
		input.Keyboard(input.KeyA), input.Keyboard(input.KeyD))
	g.binds.SetVectorStick(g.move, input.StickLeft)

	// Look has no digital sources: the mouse drives it through the camera's own
	// path, because a mouse reports displacement and a stick reports deflection.
	// Binding arrow keys here would work too, and would be a third kind of thing
	// again — a rate, like the stick.
	g.look = g.binds.Vector("look", input.Source{}, input.Source{}, input.Source{}, input.Source{})
	g.binds.SetVectorStick(g.look, input.StickRight)

	g.jump = g.binds.Action("jump", input.Keyboard(input.KeySpace), input.PadButton(input.ButtonA))
	g.sprint = g.binds.Action("sprint",
		input.Keyboard(input.KeyLeftShift), input.PadButton(input.ButtonLeftBumper))
	g.rebind = g.binds.Action("rebind jump",
		input.Keyboard(input.KeyTab), input.PadButton(input.ButtonBack))
	g.quit = g.binds.Action("quit", input.Keyboard(input.KeyEscape))

	g.uiMesh, err = r.CreateDynamicIndexedMesh(2048, 3072)
	if err != nil {
		return err
	}

	g.camera = glyph.NewFPCamera()
	g.camera.EyeHeight = 0.7
	e.Input().SetCursorLocked(true)

	e.SetDayCycleSpeed(1.0 / 240.0)
	e.SetTimeOfDay(0.33)

	log.Println("17-input running. WASD or left stick to move, Tab rebinds jump, Escape quits.")
	g.logPad(e)
	return nil
}

// logPad reports the connected controller by name, once, when it changes.
//
// Worth logging rather than only drawing: if GLFW has no mapping for a pad it
// reads as absent, and the difference between "not plugged in" and "plugged in but
// unrecognised" is invisible on screen while being the first thing to check.
func (g *game) logPad(e *glyph.Engine) {
	in := e.Input()
	name := ""
	if p, ok := in.FirstPad(); ok {
		name = in.PadName(p)
	}
	if g.padReported && name == g.lastPad {
		return
	}
	g.lastPad, g.padReported = name, true
	if name == "" {
		log.Println("no mapped gamepad; keyboard and mouse only")
	} else {
		log.Printf("gamepad connected: %s", name)
	}
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	g.logPad(e)

	if g.binds.Pressed(g.quit) {
		e.Close()
	}

	// Rebinding: one press to arm it, then the next press becomes the binding.
	if g.awaitingRebind {
		if src, ok := g.binds.CapturePressed(); ok {
			g.binds.Rebind(g.jump, src)
			g.awaitingRebind = false
			log.Printf("jump rebound to %s", input.SourceLabel(src))
		}
	} else if g.binds.Pressed(g.rebind) {
		g.awaitingRebind = true
		log.Println("press any key or button to bind jump...")
	}

	// Movement. The vector's magnitude survives all the way into MoveIntent, so a
	// half-pushed stick walks at half speed.
	mx, my := g.binds.Direction(g.move)
	g.intent.Forward = my
	g.intent.Right = mx
	g.intent.Sprint = g.binds.Down(g.sprint)
	g.intent.Yaw = g.camera.Yaw

	if g.binds.Pressed(g.jump) {
		g.jumpQueued = true
	}

	// Look, from both devices at once. Update takes the mouse; LookStick takes the
	// stick and needs dt, because deflection is a rate rather than a displacement.
	g.camera.Update(e.Input())
	lx, ly := g.binds.Direction(g.look)
	g.camera.LookStick(lx, ly, dt)

	if t, ok := e.C.Transform.Get(g.player); ok {
		g.camera.Follow(t)
	}
	e.SetCamera(g.camera.ViewVectors())

	sw, sh := e.Window().GetFramebufferSize()
	g.buildReadout(e, float32(sw), float32(sh))
}

func (g *game) FixedUpdate(e *glyph.Engine, dt float32) {
	g.intent.Jump = g.jumpQueued
	g.jumpQueued = false
	e.Scene.MoveCharacter(g.player, g.intent, dt)
}

// Panel colours, kept together so the readout reads as one thing.
var (
	panelBG   = [3]float32{0.05, 0.06, 0.08}
	panelEdge = [3]float32{0.30, 0.34, 0.40}
	armedEdge = [3]float32{0.85, 0.65, 0.22}
	cellOff   = [3]float32{0.16, 0.18, 0.22}
	cellOn    = [3]float32{0.42, 0.80, 0.52}
	boxBG     = [3]float32{0.10, 0.12, 0.15}
	dotColor  = [3]float32{0.95, 0.85, 0.35}
	barFill   = [3]float32{0.35, 0.62, 0.88}
	deadRing  = [3]float32{0.28, 0.22, 0.22}
)

// buildReadout draws the live device state, rebuilt wholesale each frame the way
// 13-ui does.
func (g *game) buildReadout(e *glyph.Engine, sw, sh float32) {
	in := e.Input()
	pad, connected := in.FirstPad()

	var verts []renderer.Vertex
	var idxs []uint16
	quad := func(x, y, w, h float32, c [3]float32) {
		verts, idxs = ui.AppendQuad(verts, idxs, x, y, w, h, c)
	}

	const (
		panelH = 132
		margin = 16
		boxSz  = 96
	)
	top := sh - panelH - margin

	edge := panelEdge
	if g.awaitingRebind {
		edge = armedEdge
	}
	quad(margin, top, sw-2*margin, panelH, panelBG)
	quad(margin, top, sw-2*margin, 3, edge)

	// Stick boxes. A dot inside a square is the fastest way to see the two things
	// that go wrong: a resting position that is not centred, and a dead zone so
	// wide the dot jumps rather than slides.
	stickBox := func(x float32, s input.Stick) {
		y := top + 18
		quad(x, y, boxSz, boxSz, boxBG)

		// The dead zone drawn to scale, so "the stick does nothing yet" is
		// visibly the dead zone rather than a broken axis.
		dz := in.PadDeadzone() * boxSz
		quad(x+boxSz/2-dz/2, y+boxSz/2-dz/2, dz, dz, deadRing)

		var dx, dy float32
		if connected {
			dx, dy = in.PadStick(pad, s)
		}
		// +y is up in the stick's convention and down in screen space.
		cx := x + boxSz/2 + dx*(boxSz/2-5)
		cy := y + boxSz/2 - dy*(boxSz/2-5)
		quad(cx-4, cy-4, 8, 8, dotColor)
	}
	stickBox(margin+16, input.StickLeft)
	stickBox(margin+16+boxSz+14, input.StickRight)

	// Triggers as vertical bars, filling upward.
	trigger := func(x float32, a input.Axis) {
		y := top + 18
		quad(x, y, 16, boxSz, boxBG)
		var v float32
		if connected {
			v = in.PadAxis(pad, a)
		}
		h := v * boxSz
		quad(x, y+boxSz-h, 16, h, barFill)
	}
	tx := float32(margin + 16 + 2*(boxSz+14))
	trigger(tx, input.AxisLeftTrigger)
	trigger(tx+24, input.AxisRightTrigger)

	// Every button as a cell, laid out in two rows in Button order. Lit means held.
	buttons := []input.Button{
		input.ButtonA, input.ButtonB, input.ButtonX, input.ButtonY,
		input.ButtonLeftBumper, input.ButtonRightBumper, input.ButtonBack, input.ButtonStart,
		input.ButtonGuide, input.ButtonLeftThumb, input.ButtonRightThumb,
		input.ButtonDPadUp, input.ButtonDPadRight, input.ButtonDPadDown, input.ButtonDPadLeft,
	}
	const cell, gap float32 = 26, 5
	bx := tx + 60
	for i, btn := range buttons {
		col, row := i%8, i/8
		x := bx + float32(col)*(cell+gap)
		y := top + 18 + float32(row)*(cell+gap)
		c := cellOff
		if connected && in.PadDown(pad, btn) {
			c = cellOn
		}
		quad(x, y, cell, cell, c)
	}

	// A connection lamp, so "no pad" is stated rather than inferred from a panel
	// full of dark cells.
	lamp := cellOff
	if connected {
		lamp = cellOn
	}
	quad(sw-margin-36, top+18, 20, 20, lamp)

	e.Renderer().UpdateMeshData(g.uiMesh, verts, idxs)
	proj := mgl32.Ortho(0, sw, 0, sh, -1, 1)
	e.SetUIOverlays([]renderer.UIRenderObject{{
		RenderObject: renderer.RenderObject{Mesh: g.uiMesh, MVP: proj},
		Opacity:      0.94,
	}})
}

func main() {
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	fullscreen := flag.Bool("fullscreen", false, "run fullscreen on the primary monitor")
	frames := flag.Int("frames", 0, "render N frames then exit (0 = run until closed)")
	deadzone := flag.Float64("deadzone", 0, "override the stick dead zone (0 keeps the default)")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 17 Input"),
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

	g := &game{}
	e, err := glyph.New(g, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	if *deadzone > 0 {
		e.Input().SetPadDeadzone(float32(*deadzone))
	}

	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
}
