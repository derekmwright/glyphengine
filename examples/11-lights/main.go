// Command 11-lights shows the two kinds of point light and the shadows one of
// them casts.
//
// The engine has a deliberate asymmetry:
//
//   - Scene.SetPointLight is the single shadow-casting light. It renders the
//     scene six more times into a cube map, so there is exactly one, and it is
//     the one you spend on the lantern the player is carrying.
//   - Scene.SetPointLights are unshadowed fill lights, up to
//     renderer.MaxPointLights of them. They cost a loop in the fragment
//     shader and nothing else, so they are what you scatter around a level.
//
// Telling them apart is the point of this example: watch the orbiting white
// light throw pillar shadows across the floor while the coloured lights pool
// on the walls without casting anything.
//
// The scene is set at night with no cycle, so the lights are the only thing
// lighting it — see the Environment below.
//
//	go run ./11-lights              # windowed
//	go run ./11-lights -frames 200  # render 200 frames, then exit
//	go run ./11-lights -static      # stop the lights moving
//
// The camera orbits on its own. Escape quits.
package main

import (
	"flag"
	"log"
	"math"
	"runtime"

	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/input"
)

func init() {
	// GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

const (
	pillarCount  = 7
	pillarRadius = 6.0
	lanternRange = 22.0
)

// autoOrbitRate circles the camera slowly, in radians per second.
//
// No mouse look: this example is about how the two light types differ, and camera
// plumbing in front of that is just noise to read past. Orbiting on its own
// also makes -frames screenshots repeatable.
const autoOrbitRate = 0.15

type game struct {
	camera  *glyph.Camera
	lantern glyph.Entity
	fills   []glyph.Entity
	static  bool
	t       float32
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()

	// Night, with no sky and no cycle: the only light in this scene is the
	// light this example places. A day/night cycle would drown it in sun.
	e.Scene.Env = &glyph.Environment{
		Ambient:    &glyph.AmbientLight{Color: [3]float32{0.035, 0.038, 0.055}},
		ClearColor: [3]float32{0.02, 0.02, 0.035},
	}

	floor, err := r.CreatePlane(46, 46)
	if err != nil {
		return err
	}
	ent := e.Spawn()
	e.C.Transform.Set(ent, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: floor, Roughness: 0.9})
	e.C.Color.Set(ent, &glyph.Color{R: 0.42, G: 0.42, B: 0.46})
	e.C.Static.Set(ent, &glyph.Static{})
	// A flat floor casts nothing; it only receives.
	e.C.NoCastShadow.Set(ent, &glyph.NoCastShadow{})

	// Pillars in a ring. These are what the shadow-casting light throws
	// shadows from, and a point light's shadows fan outward from it, which is
	// the giveaway that it is not the sun.
	pillar, err := r.CreateCube(1.0)
	if err != nil {
		return err
	}
	for i := 0; i < pillarCount; i++ {
		a := float64(i) / pillarCount * 2 * math.Pi
		p := e.Spawn()
		e.C.Transform.Set(p, &glyph.Transform{
			Position: mgl32.Vec3{float32(math.Cos(a)) * pillarRadius, 2.5, float32(math.Sin(a)) * pillarRadius},
			Scale:    mgl32.Vec3{0.9, 5, 0.9},
		})
		e.C.MeshRef.Set(p, &glyph.MeshRef{Mesh: pillar, Roughness: 0.8})
		e.C.Color.Set(p, &glyph.Color{R: 0.62, G: 0.60, B: 0.57})
		e.C.Static.Set(p, &glyph.Static{})
	}

	// A small emissive marker for each light, so it is obvious where the light
	// is coming from. Emissive bypasses lighting entirely — the marker is not
	// lit by the light it represents.
	bulb, err := r.CreateCube(0.35)
	if err != nil {
		return err
	}
	g.lantern = e.Spawn()
	e.C.Transform.Set(g.lantern, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(g.lantern, &glyph.MeshRef{Mesh: bulb})
	e.C.Color.Set(g.lantern, &glyph.Color{R: 1, G: 0.96, B: 0.88})
	e.C.Emissive.Set(g.lantern, &glyph.Emissive{})

	for _, c := range fillColors {
		f := e.Spawn()
		e.C.Transform.Set(f, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
		e.C.MeshRef.Set(f, &glyph.MeshRef{Mesh: bulb})
		// The marker has to be given a Color here for Update to have one to
		// modify; Store.Get on an entity without the component returns not-ok
		// and the write goes nowhere.
		e.C.Color.Set(f, &glyph.Color{R: c[0], G: c[1], B: c[2]})
		e.C.Emissive.Set(f, &glyph.Emissive{})
		g.fills = append(g.fills, f)
	}

	g.camera = glyph.NewCamera(17)
	g.camera.Pitch = 0.42
	g.camera.Target = mgl32.Vec3{0, 2, 0}

	log.Println("11-lights running. One shadow-casting light, several fill lights. Escape quits.")
	return nil
}

// fillColors are the unshadowed lights: saturated, so it is easy to see which
// pool of light came from which.
var fillColors = [][3]float32{
	{0.95, 0.25, 0.30},
	{0.25, 0.55, 1.00},
	{0.35, 0.95, 0.45},
	{0.95, 0.75, 0.20},
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	if !g.static {
		g.t += dt
	}
	g.camera.Yaw += autoOrbitRate * dt
	e.SetCamera(g.camera.ViewVectors())

	// ── the shadow-casting light ──
	// One only. Every frame it costs six more renders of the scene into a cube
	// map, which is why the engine offers exactly one and makes you choose.
	lx := float32(math.Cos(float64(g.t)*0.55)) * 3.4
	lz := float32(math.Sin(float64(g.t)*0.55)) * 3.4
	ly := 3.2 + float32(math.Sin(float64(g.t)*0.9))*0.8
	e.Scene.SetPointLight(
		mgl32.Vec3{lx, ly, lz},
		mgl32.Vec3{1.0, 0.94, 0.82},
		lanternRange,
	)
	if t, ok := e.C.Transform.Get(g.lantern); ok {
		t.Position = mgl32.Vec3{lx, ly, lz}
	}

	// ── the unshadowed fill lights ──
	// These pass through the whole scene without occlusion, so they light the
	// far side of a pillar as happily as the near side. That is the trade:
	// many of them, none of them casting.
	lights := make([]glyph.PointLight, 0, len(fillColors))
	for i, c := range fillColors {
		a := float64(i)/float64(len(fillColors))*2*math.Pi - float64(g.t)*0.3
		pos := mgl32.Vec3{
			float32(math.Cos(a)) * 9.5,
			1.6 + float32(math.Sin(float64(g.t)*1.3+float64(i)))*0.5,
			float32(math.Sin(a)) * 9.5,
		}
		lights = append(lights, glyph.PointLight{
			Pos:   pos,
			Range: 11,
			Color: mgl32.Vec3{c[0], c[1], c[2]},
		})
		if t, ok := e.C.Transform.Get(g.fills[i]); ok {
			t.Position = pos
		}
		if col, ok := e.C.Color.Get(g.fills[i]); ok {
			col.R, col.G, col.B = c[0], c[1], c[2]
		}
	}
	e.Scene.SetPointLights(lights)
}

func main() {
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	fullscreen := flag.Bool("fullscreen", false, "run fullscreen on the primary monitor")
	frames := flag.Int("frames", 0, "render N frames then exit (0 = run until closed)")
	static := flag.Bool("static", false, "stop the lights moving")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 11 Lights"),
		glyph.WithWindowSize(*width, *height),
		glyph.WithMSAA(4),
		glyph.WithQuitKey(input.KeyEscape),
		glyph.WithDebugKeys(),
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

	e, err := glyph.New(&game{static: *static}, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
}
