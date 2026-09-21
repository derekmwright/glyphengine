// Command normalcheck renders supplied normals through the ordinary lit pipeline.
// It needs a GPU; run with GLYPHENGINE_VALIDATION=1 to check Vulkan use as well.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image/png"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"

	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/window"
	"github.com/go-gl/mathgl/mgl32"
)

func main() {
	runtime.LockOSThread()
	rotated := flag.Bool("rotated", false, "use geometric normals on rotated planes")
	flag.Parse()
	var messages bytes.Buffer
	log.SetOutput(io.MultiWriter(os.Stderr, &messages))
	err := run(*rotated) // includes teardown, where validation reports leaked resources
	log.SetOutput(os.Stderr)
	if err != nil {
		log.Fatal(err)
	}
	if bytes.Contains(messages.Bytes(), []byte("VULKAN ERROR")) || bytes.Contains(messages.Bytes(), []byte("VULKAN WARNING")) {
		log.Fatal("Vulkan validation was not silent")
	}
	if os.Getenv("GLYPHENGINE_VALIDATION") == "1" && !bytes.Contains(messages.Bytes(), []byte("Vulkan validation layer enabled")) {
		log.Fatal("validation requested but not enabled")
	}
}

func run(rotated bool) error {
	w, err := window.New(400, 300, "Supplied normal regression")
	if err != nil {
		return err
	}
	defer w.Destroy()
	r, err := renderer.New(w)
	if err != nil {
		return err
	}
	defer r.Destroy()
	// Independent orthographic swatches isolate the supplied normal from
	// silhouette, fog, ambient, shadows, animation and material-map differences.
	normals := [][3]float32{{0, 0, 1}, {0, -1, 0}, {0, 1, 0}}
	var draws []renderer.RenderObject
	for i, n := range normals {
		x := float32(i)*0.6 - 0.9
		model, mvp := mgl32.Ident4(), mgl32.Ident4()
		z := float32(0.5)
		if rotated {
			// Here shading and geometric normals agree: rotate actual XY
			// planes, viewed with a shared orthographic reverse-Z camera.
			// Before #93, roughness .98 lit the downward plane to 175.67/255;
			// after, it reads 0 at .9/.98/1, with the control at 165.33.
			n = [3]float32{0, 0, 1}
			z = 0
			if i == 1 {
				model = mgl32.HomogRotate3DX(math.Pi / 4)
			}
			if i == 2 {
				model = mgl32.HomogRotate3DX(-math.Pi / 4)
			}
			mvp = mgl32.Translate3D(0, 0, 0.5).Mul4(mgl32.Scale3D(1, 1, 0.25)).Mul4(model)
		}
		v := []renderer.Vertex{
			{Pos: [3]float32{x, -0.7, z}, Normal: n, Color: [3]float32{1, 1, 1}},
			{Pos: [3]float32{x + 0.5, -0.7, z}, Normal: n, Color: [3]float32{1, 1, 1}},
			{Pos: [3]float32{x + 0.5, 0.7, z}, Normal: n, Color: [3]float32{1, 1, 1}},
			{Pos: [3]float32{x, 0.7, z}, Normal: n, Color: [3]float32{1, 1, 1}},
		}
		m, err := r.CreateIndexedMesh(v, []uint16{0, 1, 2, 0, 2, 3})
		if err != nil {
			return err
		}
		draws = append(draws, renderer.RenderObject{Mesh: m, Model: model, MVP: mvp, Color: [3]float32{0.5, 0.5, 0.5}})
	}
	light := renderer.SceneLighting{SunDir: [3]float32{0, 1, 0}, SunColor: [3]float32{1, 1, 1}, CameraPos: [3]float32{0, 0, 3}, SkyColor: [4]float32{0, 0, 0, 1}}
	dir, err := os.MkdirTemp("", "glyph-normalcheck-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for _, roughness := range []float32{0.9, 0.98, 1} {
		for i := range draws {
			draws[i].Roughness = roughness
		}
		for frame := 0; frame < 3; frame++ {
			w.PollEvents()
			if err := r.DrawFrame(draws, nil, nil, nil, nil, light); err != nil {
				return err
			}
		}
		path := filepath.Join(dir, "capture.png")
		if err := r.SaveScreenshot(path); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		img, err := png.Decode(f)
		f.Close()
		if err != nil {
			return err
		}
		var values [3]float64
		for i := range values {
			cx := int((float32(i)*0.6 - 0.65 + 1) * 0.5 * float32(img.Bounds().Dx()))
			cy := img.Bounds().Dy() / 2
			for y := cy - 5; y < cy+5; y++ {
				for x := cx - 5; x < cx+5; x++ {
					rr, gg, bb, _ := img.At(x, y).RGBA()
					values[i] += float64(rr+gg+bb) / (3 * 257 * 100)
				}
			}
		}
		fmt.Printf("roughness %.2f: perpendicular %.2f, downward %.2f, lit control %.2f /255\n", roughness, values[0], values[1], values[2])
		// Absolute visibility floor, plus a positive control: black captures
		// (missing geometry, wrong winding, empty files) cannot pass.
		if values[0] > 2 || values[1] > 2 || values[2] < 100 {
			return fmt.Errorf("supplied normals not respected or lit control missing")
		}
	}
	return nil
}
