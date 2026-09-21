// Command skyshadowcheck compares a custom SkyFrag and LitFrag reading the
// same directional shadow map, including disabled shadows and swapchain rebuild.
// Against the pre-#98 renderer it fails with layout-07988 for set 1 bindings
// 0/1. With the binding present, both passes read 89/255 shadowed, 231/255 lit.
package main

import (
	"bytes"
	_ "embed"
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

//go:embed sky.frag.spv
var sky []byte

//go:embed lit.frag.spv
var lit []byte

func main() {
	runtime.LockOSThread()
	var messages bytes.Buffer
	log.SetOutput(io.MultiWriter(os.Stderr, &messages))
	err := run()
	log.SetOutput(os.Stderr)
	if err != nil {
		log.Fatal(err)
	}
	if bytes.Contains(messages.Bytes(), []byte("VULKAN ERROR")) || bytes.Contains(messages.Bytes(), []byte("VULKAN WARNING")) {
		log.Fatal("Vulkan validation was not silent")
	}
	if !bytes.Contains(messages.Bytes(), []byte("Vulkan validation layer enabled")) {
		log.Fatal("validation must be enabled for this check")
	}
}

func run() error {
	w, err := window.New(400, 300, "Custom sky shadow sampling")
	if err != nil {
		return err
	}
	defer w.Destroy()
	sh := renderer.DefaultShaders()
	sh.SkyFrag = sky
	sh.LitFrag = lit
	r, err := renderer.New(w, renderer.WithShaders(sh), renderer.WithValidation(true))
	if err != nil {
		return err
	}
	defer r.Destroy()
	v := []renderer.Vertex{
		{Pos: [3]float32{-1, -1, 0.5}, Normal: [3]float32{0, 1, 0}},
		{Pos: [3]float32{0, -1, 0.5}, Normal: [3]float32{0, 1, 0}},
		{Pos: [3]float32{0, 1, 0.5}, Normal: [3]float32{0, 1, 0}},
		{Pos: [3]float32{-1, 1, 0.5}, Normal: [3]float32{0, 1, 0}},
	}
	panel, err := r.CreateIndexedMesh(v, []uint16{0, 1, 2, 0, 2, 3})
	if err != nil {
		return err
	}
	caster, err := r.CreateCube(2)
	if err != nil {
		return err
	}
	draws := []renderer.RenderObject{
		{Mesh: panel, Model: mgl32.Ident4(), MVP: mgl32.Ident4(), NoCastShadow: true},
		{Mesh: caster, Model: mgl32.Translate3D(0, 10, 0), ShadowOnly: true},
	}
	light := renderer.SceneLighting{SunDir: [3]float32{0, 1, 0}, DrawSky: true, InvVP: mgl32.Ident4(), CascadeVPs: renderer.ComputeCascadeVPs([3]float32{0, 1, 0}, mgl32.Vec3{})}
	dir, err := os.MkdirTemp("", "glyph-skyshadow-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	// Each phase overwrites both frame slots; enabled -> disabled catches stale
	// maps, and disabled -> enabled catches a binding accidentally left unset.
	for phase, enabled := range []bool{true, false, true, false} {
		if phase == 2 {
			w.Handle().SetSize(640, 480)
			w.PollEvents()
			r.NotifyResize()
		}
		light.ShadowEnabled = enabled
		for frame := 0; frame < 8; frame++ {
			w.PollEvents()
			if w.WasResized() {
				r.NotifyResize()
			}
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
		var values [2]float64
		for i := range values {
			cx, cy := img.Bounds().Dx()*(1+2*i)/4, img.Bounds().Dy()/2
			for y := cy - 5; y < cy+5; y++ {
				for x := cx - 5; x < cx+5; x++ {
					rr, gg, bb, _ := img.At(x, y).RGBA()
					values[i] += float64(rr+gg+bb) / (3 * 257 * 100)
				}
			}
		}
		fmt.Printf("phase %d shadows=%v: lit %.2f sky %.2f /255\n", phase, enabled, values[0], values[1])
		want := 231.0
		if enabled {
			want = 89
		}
		if math.Abs(values[0]-want) > 3 || math.Abs(values[1]-want) > 3 || math.Abs(values[0]-values[1]) > 1 {
			return fmt.Errorf("sky and lit shadow sampling disagree or control missing")
		}
	}
	return nil
}
