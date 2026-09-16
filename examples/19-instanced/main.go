// Command 19-instanced draws the same field of props two ways, so the cost of
// each can be read off the same scene.
//
// A colony of identical habitat domes is the case a builder-style game hits
// early. On the ordinary path each dome is one entity with a MeshRef, and that
// is one CmdDrawIndexed, one push-constant upload and one frustum test apiece.
// With an InstancedMesh the whole field is one of each.
//
//	go run ./19-instanced                      # 900 domes, instanced
//	go run ./19-instanced -instanced=false     # the same 900, one draw each
//	go run ./19-instanced -count 3600          # more of them
//
// The two modes are meant to look identical — same mesh, same lighting, same
// shadows — so that any visible difference is a bug in the instanced path
// rather than a different scene. Press I to switch between them at runtime and
// watch the draw-call count in the corner rather than the image.
//
// Left-drag orbits, scroll zooms, I toggles instancing, Escape quits.
package main

import (
	"flag"
	"log"
	"math"
	"runtime"

	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/ecs"
	"github.com/derekmwright/glyphengine/input"
	"github.com/derekmwright/glyphengine/renderer"
)

func init() {
	// GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

const spacing = 3.0

type game struct {
	camera *glyph.Camera

	count     int
	instanced bool

	// Both representations of the same field are built up front and one of them
	// is hidden, so toggling costs nothing and neither mode is paying a
	// construction cost the other is not.
	set        *renderer.InstanceSet
	setEntity  ecs.Entity
	individual []ecs.Entity
}

// placements lays the field out in a square grid, each dome turned a little so
// the transforms are not all identical — a per-instance model matrix that only
// ever carries a translation would not prove much.
func placements(n int) []renderer.MeshInstance {
	side := int(math.Ceil(math.Sqrt(float64(n))))
	out := make([]renderer.MeshInstance, 0, n)
	for i := 0; i < n; i++ {
		gx := float32(i%side) - float32(side-1)/2
		gz := float32(i/side) - float32(side-1)/2
		yaw := float32(i) * 0.37

		m := mgl32.Translate3D(gx*spacing, 0.8, gz*spacing).Mul4(mgl32.HomogRotate3DY(yaw))
		var model [16]float32
		copy(model[:], m[:])

		// A little colour variation so a wrong per-instance attribute stride
		// shows up as banding rather than as nothing.
		t := float32(i%7) / 7
		out = append(out, renderer.MeshInstance{
			Model: model,
			Tint:  [4]float32{0.7 + 0.3*t, 0.72, 0.8 - 0.2*t, 1},
		})
	}
	return out
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()

	ground, err := r.CreatePlane(400, 400)
	if err != nil {
		return err
	}
	groundEnt := e.Spawn()
	e.C.Transform.Set(groundEnt, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(groundEnt, &glyph.MeshRef{Mesh: ground, Roughness: 0.9})
	e.C.Color.Set(groundEnt, &glyph.Color{R: 0.33, G: 0.40, B: 0.29})
	e.C.Static.Set(groundEnt, &glyph.Static{})

	dome, err := r.CreateCylinder(1.0, 1.6, 20)
	if err != nil {
		return err
	}

	inst := placements(g.count)

	// ── the instanced field: one entity, one draw ──
	g.set, err = r.CreateInstanceSet(dome, g.count, inst)
	if err != nil {
		return err
	}
	g.setEntity = e.Spawn()
	e.C.InstancedMesh.Set(g.setEntity, &glyph.InstancedMesh{Set: g.set})
	e.C.MeshRef.Set(g.setEntity, &glyph.MeshRef{Mesh: dome, Roughness: 0.55, Metallic: 0.1})

	// ── the same field, one entity each ──
	for i := range inst {
		m := mgl32.Mat4{}
		copy(m[:], inst[i].Model[:])
		pos := m.Col(3).Vec3()

		ent := e.Spawn()
		e.C.Transform.Set(ent, &glyph.Transform{
			Position: pos,
			Rotation: mgl32.Vec3{0, float32(i) * 0.37, 0},
			Scale:    mgl32.Vec3{1, 1, 1},
		})
		e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: dome, Roughness: 0.55, Metallic: 0.1})
		e.C.Color.Set(ent, &glyph.Color{
			R: inst[i].Tint[0], G: inst[i].Tint[1], B: inst[i].Tint[2],
		})
		e.C.Static.Set(ent, &glyph.Static{})
		g.individual = append(g.individual, ent)
	}

	g.applyMode(e)

	e.SetDayCycleSpeed(0)
	e.SetTimeOfDay(0.32)

	g.camera = glyph.NewCamera(float32(30 + g.count/40))
	g.camera.Target = mgl32.Vec3{0, 1, 0}

	log.Printf("19-instanced running with %d domes, instanced=%v. I toggles, Escape quits.",
		g.count, g.instanced)
	return nil
}

// applyMode hides one representation and shows the other. Hidden is checked
// before anything else in the draw-list build, so the hidden half costs a map
// lookup and nothing more.
func (g *game) applyMode(e *glyph.Engine) {
	if g.instanced {
		e.C.Hidden.Remove(g.setEntity)
		for _, ent := range g.individual {
			e.C.Hidden.Set(ent, &glyph.Hidden{})
		}
		return
	}
	e.C.Hidden.Set(g.setEntity, &glyph.Hidden{})
	for _, ent := range g.individual {
		e.C.Hidden.Remove(ent)
	}
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	in := e.Input()
	if in.KeyPressed(input.KeyEscape) {
		e.Close()
	}
	if in.KeyPressed(input.KeyI) {
		g.instanced = !g.instanced
		g.applyMode(e)
		log.Printf("instanced=%v", g.instanced)
	}

	st := e.Renderer().Stats()
	mode := "individual"
	if g.instanced {
		mode = "instanced"
	}
	e.Debugf("%s: %d domes", mode, g.count)
	e.Debugf("draw calls %d  instances %d", st.DrawCalls, st.Instances)

	g.camera.Update(in)
	g.camera.ResolveCollision(e.Scene, 0, dt)
	e.SetCamera(g.camera.ViewVectors())
}

func main() {
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	fullscreen := flag.Bool("fullscreen", false, "run fullscreen on the primary monitor")
	frames := flag.Int("frames", 0, "render N frames then exit (0 = run until closed)")
	count := flag.Int("count", 900, "number of domes in the field")
	instanced := flag.Bool("instanced", true, "draw the field as one InstancedMesh rather than one entity each")
	novsync := flag.Bool("novsync", false, "disable vsync, for measuring frame cost")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 19 Instanced"),
		glyph.WithWindowSize(*width, *height),
		glyph.WithMSAA(4),
		glyph.WithProjection(50, 0.1, 2000),
	}
	if *novsync {
		opts = append(opts, glyph.WithVSync(false))
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

	e, err := glyph.New(&game{count: *count, instanced: *instanced}, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
}
