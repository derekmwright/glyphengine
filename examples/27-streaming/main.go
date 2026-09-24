// Command 27-streaming publishes procedural patches while frames render,
// three ways: synchronous device-local meshes, host-visible dynamic meshes,
// and the asynchronous batched uploader. Generation and scheduling belong to
// this example; the renderer only stages, batches and reports readiness.
package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"runtime"
	"sort"
	"time"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/renderer"
	"github.com/go-gl/mathgl/mgl32"
)

func init() { runtime.LockOSThread() }

const patchSide = 33

// game publishes perPatch patches per rendered frame until count of them
// exist. Generation is deliberately on the frame thread here: what is being
// measured is the upload, and a worker would fold its own scheduling jitter
// into the frame-time spikes this reports. A real game generates on a worker
// and hands the slices over through a channel; the constructor call still
// happens here, on the renderer thread, which is the contract.
type game struct {
	count, perPatch int
	mode            string
	made            int
	upload          time.Duration
	frames          int
	last            time.Time
	walls           []float64
	pending         []*renderer.UploadTicket
	skipped         int
}

// Each patch is 1089 vertices and 2048 triangles, with a distinct surface.
func patch(seed int) ([]renderer.Vertex, []uint32) {
	v := make([]renderer.Vertex, patchSide*patchSide)
	for z := 0; z < patchSide; z++ {
		for x := 0; x < patchSide; x++ {
			px, pz := float64(x)/32*2-1, float64(z)/32*2-1
			phase := float64(seed) * 0.173
			h := 0.15 * math.Sin(px*3+phase) * math.Cos(pz*2-phase)
			dx := 0.45 * math.Cos(px*3+phase) * math.Cos(pz*2-phase)
			dz := -0.30 * math.Sin(px*3+phase) * math.Sin(pz*2-phase)
			n := mgl32.Vec3{float32(-dx), 1, float32(-dz)}.Normalize()
			v[z*patchSide+x] = renderer.Vertex{Pos: [3]float32{float32(px), float32(h), float32(pz)}, Color: [3]float32{0.25 + float32(seed%7)*0.03, 0.5, 0.2}, Normal: [3]float32(n), UV: [2]float32{float32(x) / 32, float32(z) / 32}}
		}
	}
	i := make([]uint32, 0, 32*32*6)
	for z := 0; z < 32; z++ {
		for x := 0; x < 32; x++ {
			a := uint32(z*patchSide + x)
			b := a + patchSide
			// Match TerrainMesh's clockwise Vulkan winding; reversing it draws
			// a completely back-face-culled field that still reports draws.
			i = append(i, a, a+1, b+1, b+1, b, a)
		}
	}
	return v, i
}

func (g *game) Init(e *glyph.Engine) error {
	side := int(math.Ceil(math.Sqrt(float64(g.count))))
	e.SetDayCycleSpeed(0)
	e.SetTimeOfDay(0.3)
	e.SetFogDensity(0)
	e.SetCamera(mgl32.Vec3{0, float32(side) * 3, 0}, mgl32.Vec3{0, 0, 0}, mgl32.Vec3{0, 0, -1})
	return nil
}

// publish creates one patch by whichever path this run is measuring.
func (g *game) publish(e *glyph.Engine, n int) error {
	r := e.Renderer()
	v, i := patch(n)
	start := time.Now()
	var m *renderer.Mesh
	var err error
	switch g.mode {
	case "sync":
		m, err = r.CreateIndexedMesh32(v, i)
	case "dynamic":
		// The consumer's workaround: host-visible buffers per frame in
		// flight, uint16 indices, and a copy into this frame's set on every
		// frame the data is dirty.
		idx := make([]uint16, len(i))
		for j, x := range i {
			idx[j] = uint16(x)
		}
		m, err = r.CreateDynamicIndexedMesh(len(v), len(idx))
		if err == nil {
			err = r.UpdateMeshData(m, v, idx)
		}
	case "async":
		var ticket *renderer.UploadTicket
		m, ticket, err = r.CreateIndexedMesh32Async(v, i)
		if err == nil {
			g.pending = append(g.pending, ticket)
		}
	}
	g.upload += time.Since(start)
	if err != nil {
		return err
	}
	side := int(math.Ceil(math.Sqrt(float64(g.count))))
	// Subtract the high-precision camera origin BEFORE converting to the
	// engine's float transforms, as a large-world consumer does.
	const origin = 1e9
	worldX := origin + (float64(n%side)-float64(side-1)/2)*2.25
	worldZ := origin + (float64(n/side)-float64(side-1)/2)*2.25
	ent := e.Spawn()
	e.C.Transform.Set(ent, &glyph.Transform{Position: mgl32.Vec3{float32(worldX - origin), 0, float32(worldZ - origin)}, Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: m, Roughness: 0.9})
	e.C.NoCastShadow.Set(ent, &glyph.NoCastShadow{})
	e.C.Static.Set(ent, &glyph.Static{})
	return nil
}

// Update measures its own wall clock rather than the delta it is handed: a
// fixed frame clock replaces that delta, and the spikes this run exists to
// report are wall time.
func (g *game) Update(e *glyph.Engine, _ float32) {
	now := time.Now()
	if !g.last.IsZero() {
		g.walls = append(g.walls, float64(now.Sub(g.last))/float64(time.Millisecond))
	}
	g.last = now
	g.frames++
	for n := 0; n < g.perPatch && g.made < g.count; n++ {
		if err := g.publish(e, g.made); err != nil {
			log.Fatal(err)
		}
		g.made++
	}
	g.skipped += e.Renderer().Stats().UploadsSkipped
}

// report prints the streamed-upload row cmd/bench parses, beside the engine's
// own BENCH line.
func (g *game) report(e *glyph.Engine) {
	sorted := append([]float64(nil), g.walls...)
	sort.Float64s(sorted)
	pick := func(q float64) float64 {
		if len(sorted) == 0 {
			return 0
		}
		return sorted[int(q*float64(len(sorted)-1))]
	}
	ready := 0
	for _, t := range g.pending {
		if t.Ready() {
			ready++
		}
	}
	counts := e.Renderer().ResourceCounts()
	// Buffers retained for geometry: two device-local per static or streamed
	// mesh, and two host-visible sets of two per dynamic mesh.
	buffers := counts.Meshes * 2
	if g.mode == "dynamic" {
		buffers = counts.Meshes * 4
	}
	log.Printf("STREAM\tupload_ms\t%.6f\tframe_max_ms\t%.3f\tframe_p99_ms\t%.3f\tframe_median_ms\t%.3f\tn_patches\t%d\tn_skipped\t%d\tn_ready\t%d\tgeometry_buffers\t%d\tpending_uploads\t%d",
		float64(g.upload)/float64(time.Millisecond)/float64(max(g.frames, 1)),
		pick(1), pick(0.99), pick(0.5), g.made, g.skipped, ready, buffers, counts.PendingUploads)
}

func main() {
	n := flag.Int("count", 400, "patches to publish")
	per := flag.Int("per-frame", 2, "patches published per rendered frame")
	mode := flag.String("mode", "async", "sync, dynamic, or async")
	frames := flag.Int("frames", 300, "frames to render")
	shot := flag.String("screenshot", "", "capture final frame")
	flag.Parse()
	if *n <= 0 || *per <= 0 || (*mode != "sync" && *mode != "dynamic" && *mode != "async") {
		log.Fatal("invalid count, per-frame or mode")
	}
	g := &game{count: *n, perPatch: *per, mode: *mode}
	opts := []glyph.Option{glyph.WithTitle(fmt.Sprintf("GlyphEngine streaming: %s", *mode)), glyph.WithWindowSize(1280, 720), glyph.WithMSAA(4), glyph.WithProjection(50, 0.1, 2000), glyph.WithMaxFrames(*frames)}
	if *shot != "" {
		opts = append(opts, glyph.WithScreenshot(*shot))
	}
	e, err := glyph.New(g, opts...)
	if err != nil {
		log.Fatal(err)
	}
	defer e.Destroy()
	e.Run()
	g.report(e)
	if g.made != *n {
		log.Fatalf("published %d of %d patches; raise -frames", g.made, *n)
	}
	// A readback outside the timed loop, for the reason 26-mesh-ranges keeps
	// one: draw counts alone accepted a completely back-face-culled field
	// during development, and a streamed field that never landed would report
	// the same counts as one that did.
	img, err := e.Renderer().CaptureFrame()
	if err != nil {
		log.Fatal(err)
	}
	visible := 0
	abs := func(v int) int {
		if v < 0 {
			return -v
		}
		return v
	}
	for y := 0; y < img.Rect.Dy(); y++ {
		for x := 0; x < img.Rect.Dx(); x++ {
			at := y*img.Stride + x*4
			if max(abs(int(img.Pix[at])-int(img.Pix[0])), abs(int(img.Pix[at+1])-int(img.Pix[1])), abs(int(img.Pix[at+2])-int(img.Pix[2]))) >= 20 {
				visible++
			}
		}
	}
	if visible < 1000 {
		log.Fatalf("streaming visibility: %d pixels differ >=20/255 from background, need 1000", visible)
	}
	log.Printf("streaming visibility: %d pixels differ >=20/255 from background (floor 1000)", visible)
}
