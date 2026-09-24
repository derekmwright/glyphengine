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

//go:embed blur.comp.spv
var blur []byte

//go:embed buffers.comp.spv
var bufferBlur []byte

//go:embed add.frag.spv
var add []byte

type options struct {
	frames                                                                 int
	screenshot                                                             string
	recreate, churn, validate, disabled, history, compute, buffers, filter bool
	msaa                                                                   int
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
	flag.BoolVar(&o.compute, "compute", false, "blur and accumulate the pattern with a compute pass")
	flag.BoolVar(&o.buffers, "buffers", false, "exercise four storage buffers with history, staged updates and churn")
	flag.BoolVar(&o.filter, "filter", false, "probe target filtering and addressing before and after recreation")
	flag.Parse()
	if o.buffers {
		o.compute = true
	}
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
	if o.filter {
		return runTargetFilter(o)
	}
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
	var compute *renderer.AppCompute
	var computed *renderer.RenderTarget
	var buffers []*renderer.StorageBuffer
	bufferData := make([]byte, 672*384*4)
	for i := 0; i < len(bufferData); i += 4 {
		binary.LittleEndian.PutUint32(bufferData[i:], math.Float32bits(1))
	}
	var temporal = make(map[int]image.Image)
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
		output := t
		if o.compute {
			computed, err = r.CreateRenderTarget(renderer.RenderTargetDesc{Name: "accumulated blur", Format: renderer.TargetR32F, Scale: 1, Storage: true, History: true})
			if err != nil {
				return err
			}
			targets = append(targets, computed)
			code := blur
			if o.buffers {
				code = bufferBlur
				buffers = nil
				for i := 0; i < 4; i++ {
					b, e := r.CreateStorageBuffer(renderer.StorageBufferDesc{Name: fmt.Sprintf("probe %d", i), Size: len(bufferData) + 16, History: i%2 == 1})
					if e != nil {
						return e
					}
					buffers = append(buffers, b)
				}
				if err = r.UploadStorageBuffer(buffers[0], bufferData); err != nil {
					return err
				}
			}
			compute, err = r.CreateAppCompute(renderer.AppComputeDesc{Name: "compute blur", Stage: renderer.StageBeforeScene, Comp: code, Buffers: buffers, Reads: []*renderer.Texture{t.Texture(), computed.Texture(), r.FallbackTexture()}, Writes: []*renderer.RenderTarget{computed}, Timed: true})
			if err != nil {
				return err
			}
			data := make([]byte, 16)
			binary.LittleEndian.PutUint32(data, math.Float32bits(0.5))
			if err = compute.SetPushConstants(data); err != nil {
				return err
			}
			compute.SetEnabled(!o.disabled)
			output = computed
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
		return r.SetShaderTarget(0, output)
	}
	if err = create(); err != nil {
		return err
	}
	for frame := 0; frame < o.frames; frame++ {
		if o.churn && frame > 0 && frame%30 == 0 {
			for _, b := range buffers {
				r.DestroyStorageBuffer(b)
			}
			r.DestroyAppCompute(compute)
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
		if o.buffers && frame%15 == 0 {
			if err = r.UploadStorageBuffer(buffers[0], bufferData[:64]); err != nil {
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
		if compute != nil {
			// Cover both window sizes used by this harness, including a resize
			// handled inside DrawFrame. The shader checks the actual image extent.
			width, height := computed.Extent()
			compute.SetDispatch((max(width, 672)+7)/8, (max(height, 384)+7)/8, 1)
		}
		if err = r.DrawFrame(draws, nil, nil, nil, nil, light); err != nil {
			return err
		}
		if o.compute && !o.disabled && !o.churn && !o.recreate && o.frames >= 260 {
			n := frame + 1
			if n == 2 || n == 60 || n == 200 || n == 260 {
				if err = os.MkdirAll(".task", 0755); err != nil {
					return err
				}
				path := filepath.Join(".task", fmt.Sprintf("compute-frame-%d.png", n))
				if err = r.SaveScreenshot(path); err != nil {
					return err
				}
				temporal[n], err = readPNG(path)
				if err != nil {
					return err
				}
			}
		}
	}
	if len(temporal) > 0 {
		if err = checkAccumulation(temporal); err != nil {
			return err
		}
	}
	if o.frames >= 4 && r.GPUTimingSupported() {
		timing := r.GPUTimings()
		wantTimings := 3
		if o.compute {
			wantTimings++
		}
		if !timing.Valid || len(timing.App) != wantTimings {
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
		if o.compute && o.frames < 20 {
			break
		}
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
	// 640x360, frame 260: box blur gives 121/255 here against band interiors
	// 96/255 and 160/255. Replacing all nine taps with the centre gives 96/255
	// and fails this check, while both interior-band probes still pass.
	if o.compute && o.frames >= 20 {
		x, y := on.Bounds().Dx()/16-1, on.Bounds().Dy()*3/4
		_, g, _, _ := on.At(x, y).RGBA()
		got := float64(g) / 257
		fmt.Printf("compute blur edge: green %.2f/255 (strictly between band interiors 96 and 160)\n", got)
		if got <= 100 || got >= 156 {
			return fmt.Errorf("compute blur edge is not between pattern bands")
		}
	}
	if compute != nil {
		compute.SetEnabled(false)
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

// At 640x360, frames 2/60 differ in 73728 region pixels by >=4/255 (max
// 44/255); whole frames 200/260 match. Setting the blend to zero gives zero
// changed pixels and fails; replacing it with previous+0.001 changes 73728
// pixels (max 41/255) but fails convergence. Both controls were run at 260
// frames with validation. A constant output cannot satisfy the temporal probe.
func checkAccumulation(frames map[int]image.Image) error {
	changed, maxDelta, converged := 0, 0, frames[200].Bounds() == frames[260].Bounds()
	box := frames[2].Bounds()
	for y := 0; y < box.Dy(); y++ {
		for x := 0; x < box.Dx(); x++ {
			_, a, _, _ := frames[2].At(x, y).RGBA()
			_, b, _, _ := frames[60].At(x, y).RGBA()
			delta := abs(int(a)-int(b)) / 257
			if delta >= 4 && y >= box.Dy()/2 && y < box.Dy()*9/10 && x >= box.Dx()/10 && x < box.Dx()*9/10 {
				changed++
			}
			maxDelta = max(maxDelta, delta)
			c, d, e, f := frames[200].At(x, y).RGBA()
			g, h, i, j := frames[260].At(x, y).RGBA()
			converged = converged && c == g && d == h && e == i && f == j
		}
	}
	fmt.Printf("compute accumulation frames 2/60: %d pixels >=4/255, max %d/255; frames 200/260 identical: %v\n", changed, maxDelta, converged)
	if changed == 0 || !converged {
		return fmt.Errorf("compute accumulation/convergence probe failed")
	}
	return nil
}
