// Command 26-mesh-ranges compares distinct procedural patches as separate
// meshes, arena ranges, and indirect batches. Generation and placement belong
// to this example; the renderer only shares storage and submission.
package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"runtime"
	"time"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/renderer"
	"github.com/go-gl/mathgl/mgl32"
)

func init() { runtime.LockOSThread() }

const patchSide = 33

type game struct {
	count   int
	mode    string
	index32 bool
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
			// Match TerrainMesh's clockwise Vulkan winding. 400 patches,
			// 1280x720 frame 30: reversed order gave 0 pixels >=20/255 from
			// background; this order gives 191739. main checks a 1000 floor.
			i = append(i, a, a+1, b+1, b+1, b, a)
		}
	}
	return v, i
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()
	r.SetMeshRangeBatching(g.mode == "indirect")
	var a *renderer.MeshArena
	var err error
	if g.mode != "separate" {
		a, err = r.CreateMeshArena(renderer.MeshArenaDesc{Name: "procedural patches", Vertices: g.count * patchSide * patchSide, Indices: g.count * 32 * 32 * 6, Index32: g.index32})
		if err != nil {
			return err
		}
	}
	side := int(math.Ceil(math.Sqrt(float64(g.count))))
	var alloc time.Duration
	for n := 0; n < g.count; n++ {
		v, i := patch(n)
		start := time.Now()
		var m *renderer.Mesh
		if a != nil {
			m, err = a.Alloc(v, i)
		} else if g.index32 {
			m, err = r.CreateIndexedMesh32(v, i)
		} else {
			idx := make([]uint16, len(i))
			for j, x := range i {
				idx[j] = uint16(x)
			}
			m, err = r.CreateIndexedMesh(v, idx)
		}
		alloc += time.Since(start)
		if err != nil {
			return err
		}
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
	}
	e.SetDayCycleSpeed(0)
	e.SetTimeOfDay(0.3)
	e.SetFogDensity(0)
	e.SetCamera(mgl32.Vec3{0, float32(side) * 3, 0}, mgl32.Vec3{0, 0, 0}, mgl32.Vec3{0, 0, -1})
	width := 2
	if g.index32 {
		width = 4
	}
	buffers := g.count * 2
	if a != nil {
		buffers = 2
	}
	log.Printf("PATCHES\talloc_ms\t%.6f\tn_geometry_buffers\t%d\tgeometry_bytes\t%d\tn_patches\t%d", float64(alloc)/float64(time.Millisecond)/float64(g.count), buffers, g.count*(patchSide*patchSide*44+32*32*6*width), g.count)
	return nil
}
func (g *game) Update(*glyph.Engine, float32) {}
func main() {
	n := flag.Int("count", 400, "number of distinct patches")
	mode := flag.String("mode", "ranges", "separate, ranges, or indirect")
	wide := flag.Bool("index32", true, "store uint32 indices (false: uint16)")
	frames := flag.Int("frames", 200, "frames to render")
	shot := flag.String("screenshot", "", "capture final frame")
	flag.Parse()
	if *n <= 0 || (*mode != "separate" && *mode != "ranges" && *mode != "indirect") {
		log.Fatal("invalid count or mode")
	}
	opts := []glyph.Option{glyph.WithTitle(fmt.Sprintf("GlyphEngine mesh ranges: %s", *mode)), glyph.WithWindowSize(1280, 720), glyph.WithMSAA(4), glyph.WithProjection(50, 0.1, 2000), glyph.WithMaxFrames(*frames)}
	if *shot != "" {
		opts = append(opts, glyph.WithScreenshot(*shot))
	}
	e, err := glyph.New(&game{*n, *mode, *wide}, opts...)
	if err != nil {
		log.Fatal(err)
	}
	defer e.Destroy()
	e.Run()
	// Draw counts alone accepted a completely back-face-culled field during
	// development. A readback outside the timed loop must contain visible
	// geometry; the corner is the clear/background reference at this camera.
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
		log.Fatalf("patches visibility: %d pixels differ >=20/255 from background, need 1000", visible)
	}
	log.Printf("patches visibility: %d pixels differ >=20/255 from background (floor 1000)", visible)
}
