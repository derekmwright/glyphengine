// Command apppasscheck exercises application graphics nodes and their lifetime
// against a rendered control. All generated captures live under .task.
package main

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"

	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/shaders"
	"github.com/derekmwright/glyphengine/window"
	"github.com/go-gl/mathgl/mgl32"
)

//go:embed pattern.vert.spv
var meshVert []byte

//go:embed pattern.frag.spv
var pattern []byte

//go:embed lit.frag.spv
var lit []byte

//go:embed filter.frag.spv
var filter []byte

//go:embed add.frag.spv
var add []byte

type options struct {
	frames                                       int
	screenshot                                   string
	recreate, churn, validate, disabled, history bool
	msaa                                         int
}

func main() {
	runtime.LockOSThread()
	o := options{}
	flag.IntVar(&o.frames, "frames", 90, "number of frames (use 300 with churn)")
	flag.StringVar(&o.screenshot, "screenshot", "", "PNG output path")
	flag.BoolVar(&o.recreate, "provoke-recreate", false, "rebuild and resize the swapchain during the run")
	flag.BoolVar(&o.churn, "churn", false, "replace passes and targets every 30 frames")
	flag.BoolVar(&o.validate, "validate", false, "enable Vulkan validation and require silence")
	flag.BoolVar(&o.disabled, "disabled", false, "render the control with passes disabled")
	flag.IntVar(&o.msaa, "msaa", 4, "sample count")
	flag.BoolVar(&o.history, "history", false, "exercise a target reading its own previous frame")
	flag.Parse()
	var messages bytes.Buffer
	log.SetOutput(io.MultiWriter(os.Stderr, &messages))
	err := run(o)
	log.SetOutput(os.Stderr)
	if err != nil {
		log.Fatal(err)
	}
	if bytes.Contains(messages.Bytes(), []byte("VULKAN ERROR")) || bytes.Contains(messages.Bytes(), []byte("VULKAN WARNING")) {
		log.Fatal("Vulkan validation was not silent")
	}
	if o.validate && !bytes.Contains(messages.Bytes(), []byte("Vulkan validation layer enabled")) {
		log.Fatal("validation layer did not run")
	}
	fmt.Println("apppasscheck: PASS")
}

func run(o options) error {
	w, err := window.New(640, 360, "Application render passes")
	if err != nil {
		return err
	}
	defer w.Destroy()
	sh := renderer.DefaultShaders()
	sh.LitFrag = lit
	r, err := renderer.New(w, renderer.WithShaders(sh), renderer.WithValidation(o.validate), renderer.WithMSAASamples(o.msaa))
	if err != nil {
		return err
	}
	defer r.Destroy()
	quad, err := r.CreateIndexedMesh([]renderer.Vertex{
		{Pos: [3]float32{-1, -1, 0.5}, UV: [2]float32{0, 0}},
		{Pos: [3]float32{1, -1, 0.5}, UV: [2]float32{1, 0}},
		{Pos: [3]float32{1, 1, 0.5}, UV: [2]float32{1, 1}},
		{Pos: [3]float32{-1, 1, 0.5}, UV: [2]float32{0, 1}},
	}, []uint16{0, 1, 2, 0, 2, 3})
	if err != nil {
		return err
	}
	draws := []renderer.RenderObject{{Mesh: quad, Model: mgl32.Ident4(), MVP: mgl32.Ident4(), NoCastShadow: true}}
	light := renderer.SceneLighting{VP: mgl32.Ident4(), InvVP: mgl32.Ident4(), SkyColor: [4]float32{0.02, 0.03, 0.04, 1}}
	var targets []*renderer.RenderTarget
	var passes []*renderer.AppPass
	create := func() error {
		t, err := r.CreateRenderTarget(renderer.RenderTargetDesc{Name: "pattern", Format: renderer.TargetR16F, Scale: 1, Depth: true, History: o.history})
		if err != nil {
			return err
		}
		filtered, err := r.CreateRenderTarget(renderer.RenderTargetDesc{Name: "filtered scene", Format: renderer.TargetRGBA16F, Scale: 0.5})
		if err != nil {
			return err
		}
		targets = []*renderer.RenderTarget{t, filtered}
		p, err := r.CreateAppPass(renderer.AppPassDesc{Name: "pattern", Stage: renderer.StageBeforeScene, Target: t, DepthTest: true, Reads: []*renderer.Texture{r.FallbackTexture(), r.FallbackTexture()}, Vert: meshVert, Frag: pattern, Timed: true})
		if err != nil {
			return err
		}
		p.SetDraws(draws)
		if o.history {
			r.DestroyAppPass(p)
			p, err = r.CreateAppPass(renderer.AppPassDesc{Name: "history pattern", Stage: renderer.StageBeforeScene, Target: t, DepthTest: true, Reads: []*renderer.Texture{t.Texture(), r.FallbackTexture()}, Vert: meshVert, Frag: pattern, Timed: true})
			if err != nil {
				return err
			}
			p.SetDraws(draws)
			data := make([]byte, 16)
			binary.LittleEndian.PutUint32(data, math.Float32bits(1))
			if err = p.SetPushConstants(data); err != nil {
				return err
			}
		}
		f, err := r.CreateAppPass(renderer.AppPassDesc{Name: "depth filter", Stage: renderer.StageBeforeBloom, Target: filtered, Reads: []*renderer.Texture{r.SceneColor(), r.SceneDepth()}, Vert: shaders.DepthResolveVertSpv, Frag: filter, Fullscreen: true, Timed: true})
		if err != nil {
			return err
		}
		a, err := r.CreateAppPass(renderer.AppPassDesc{Name: "additive composite", Stage: renderer.StageBeforeBloom, Load: true, Blend: renderer.BlendAdditive, Reads: []*renderer.Texture{filtered.Texture()}, Vert: shaders.DepthResolveVertSpv, Frag: add, Fullscreen: true, Timed: true})
		if err != nil {
			return err
		}
		passes = []*renderer.AppPass{p, f, a}
		for _, p := range passes {
			p.SetEnabled(!o.disabled)
		}
		if o.disabled {
			return r.SetShaderTarget(0, nil)
		}
		return r.SetShaderTarget(0, t)
	}
	if err = create(); err != nil {
		return err
	}
	for frame := 0; frame < o.frames; frame++ {
		if o.churn && frame > 0 && frame%30 == 0 {
			for _, p := range passes {
				r.DestroyAppPass(p)
			}
			for _, t := range targets {
				r.DestroyRenderTarget(t)
			}
			if err = create(); err != nil {
				return err
			}
		}
		if o.recreate && frame > 0 && frame+4 < o.frames && frame%23 == 0 {
			if (frame/23)%2 == 0 {
				w.Handle().SetSize(640, 360)
			} else {
				w.Handle().SetSize(672, 384)
			}
			r.NotifyResize()
		}
		w.PollEvents()
		if w.WasResized() {
			r.NotifyResize()
		}
		if err = r.DrawFrame(draws, nil, nil, nil, nil, light); err != nil {
			return err
		}
	}
	if o.frames >= 4 && r.GPUTimingSupported() {
		timing := r.GPUTimings()
		if !timing.Valid || len(timing.App) != 3 {
			return fmt.Errorf("app timings missing: valid=%v count=%d", timing.Valid, len(timing.App))
		}
		for _, t := range timing.App {
			fmt.Printf("app timing %s: %.4f ms\n", t.Name, t.Ms)
		}
	}
	if err = os.MkdirAll(".task", 0755); err != nil {
		return err
	}
	path := o.screenshot
	if path == "" {
		path = filepath.Join(".task", "apppasscheck.png")
	}
	if err = r.SaveScreenshot(path); err != nil {
		return err
	}
	if o.disabled {
		return nil
	}
	on, err := readPNG(path)
	if err != nil {
		return err
	}
	// Known linear values prove every link executed: without the mesh the
	// pattern is zero; without depth/filter/addition, the high band's green
	// is about 150/255 instead of 160/255. A mere nonzero diff would miss both.
	for i, patternValue := range []float64{0.3, 0.9} {
		x := on.Bounds().Dx() * (1 + 2*i) / 32
		y := on.Bounds().Dy() * 3 / 4
		_, green, _, _ := on.At(x, y).RGBA()
		linear := 0.34 * patternValue * 1.15
		want := (1.055*math.Pow(linear, 1/2.4) - 0.055) * 255
		got := float64(green) / 257
		fmt.Printf("pattern band %d: green %.2f/255, expected %.2f/255\n", i, got, want)
		if math.Abs(got-want) > 3 {
			return fmt.Errorf("pattern/depth/additive probe %d failed", i)
		}
	}
	for _, p := range passes {
		p.SetEnabled(false)
	}
	if err = r.SetShaderTarget(0, nil); err != nil {
		return err
	}
	for range 4 {
		if err = r.DrawFrame(draws, nil, nil, nil, nil, light); err != nil {
			return err
		}
	}
	control := filepath.Join(".task", "apppasscheck-control.png")
	if err = r.SaveScreenshot(control); err != nil {
		return err
	}
	off, err := readPNG(control)
	if err != nil {
		return err
	}
	changed := 0
	maxDelta := 0
	box := on.Bounds()
	for y := box.Dy() / 2; y < box.Dy()*9/10; y++ {
		for x := box.Dx() / 10; x < box.Dx()*9/10; x++ {
			a, b, c, _ := on.At(x, y).RGBA()
			d, e, f, _ := off.At(x, y).RGBA()
			delta := max(abs(int(a)-int(d)), abs(int(b)-int(e)), abs(int(c)-int(f))) / 257
			if delta >= 4 {
				changed++
			}
			maxDelta = max(maxDelta, delta)
		}
	}
	fmt.Printf("terrain region: %d pixels differ by >=4/255; max contrast %d/255\n", changed, maxDelta)
	if changed == 0 {
		return fmt.Errorf("application passes made no visible change in terrain region")
	}
	return nil
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
func readPNG(path string) (image.Image, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	return png.Decode(f)
}
