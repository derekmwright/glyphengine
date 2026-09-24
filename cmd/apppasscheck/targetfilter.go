package main

import (
	_ "embed"
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"

	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/shaders"
	"github.com/derekmwright/glyphengine/window"
	"github.com/go-gl/mathgl/mgl32"
)

//go:embed target-values.frag.spv
var targetValues []byte

//go:embed target-sample.frag.spv
var targetSample []byte

//go:embed target-display.frag.spv
var targetDisplay []byte

var targetFilterNames = [4]string{"nearest/clamp", "nearest/repeat", "linear/clamp", "linear/repeat"}

func runTargetFilter(o options) error {
	if o.frames < 12 {
		return fmt.Errorf("-filter requires at least 12 frames")
	}
	w, err := window.New(320, 320, "Target sampler probe")
	if err != nil {
		return err
	}
	defer w.Destroy()
	r, err := renderer.New(w, renderer.WithValidation(o.validate), renderer.WithMSAASamples(o.msaa))
	if err != nil {
		return err
	}
	defer r.Destroy()
	var targets [4]*renderer.RenderTarget
	var reads []*renderer.Texture
	for i, name := range targetFilterNames {
		desc := renderer.RenderTargetDesc{Name: name, Format: renderer.TargetR16F, Scale: 1.0 / 160}
		if i == 0 {
			desc.Width, desc.Height = 2, 2
		}
		if i >= 2 {
			desc.Filter = renderer.FilterLinear
		}
		if i%2 == 1 {
			desc.Wrap = renderer.WrapRepeat
		}
		// The fourth sampler also exercises both history images and storage
		// usage. The fullscreen writer supplies the same known values each tick.
		if i == 3 {
			desc.History, desc.Storage = true, true
		}
		targets[i], err = r.CreateRenderTarget(desc)
		if err != nil {
			return err
		}
		reads = append(reads, targets[i].Texture())
		_, err = r.CreateAppPass(renderer.AppPassDesc{Name: "write " + name, Stage: renderer.StageBeforeScene, Target: targets[i], Fullscreen: true, Vert: shaders.DepthResolveVertSpv, Frag: targetValues})
		if err != nil {
			return err
		}
	}
	if err = r.SetShaderTarget(0, targets[3]); err != nil {
		return err
	}
	chart, err := r.CreateRenderTarget(renderer.RenderTargetDesc{Name: "sampler probes", Format: renderer.TargetRGBA16F, Width: 12, Height: 4})
	if err != nil {
		return err
	}
	_, err = r.CreateAppPass(renderer.AppPassDesc{Name: "fractional samples", Stage: renderer.StageBeforeScene, Target: chart, Reads: reads, Fullscreen: true, Vert: shaders.DepthResolveVertSpv, Frag: targetSample})
	if err != nil {
		return err
	}
	display, err := r.CreateAppPass(renderer.AppPassDesc{Name: "display probes", Stage: renderer.StageBeforeTonemap, Load: true, Reads: []*renderer.Texture{chart.Texture()}, Fullscreen: true, Vert: shaders.DepthResolveVertSpv, Frag: targetDisplay})
	if err != nil {
		return err
	}
	light := renderer.SceneLighting{VP: mgl32.Ident4(), InvVP: mgl32.Ident4()}
	var old [4][2]renderer.Texture
	var captured *image.RGBA
	size := 320
	for frame := 0; frame < o.frames; frame++ {
		if o.recreate && frame == o.frames/2 {
			size = 384
			w.Handle().SetSize(size, size)
			r.NotifyResize()
		}
		w.PollEvents()
		if w.WasResized() {
			r.NotifyResize()
		}
		var data [16]byte
		binary.LittleEndian.PutUint32(data[0:], math.Float32bits(float32(size)))
		binary.LittleEndian.PutUint32(data[4:], math.Float32bits(float32(size)))
		if err = display.SetPushConstants(data[:]); err != nil {
			return err
		}
		if err = r.DrawFrame(nil, nil, nil, nil, nil, light); err != nil {
			return err
		}
		if frame == 2 || frame == 3 {
			for i, target := range targets {
				old[i][frame-2] = *target.Texture()
			}
		}
		if frame != 3 && frame != o.frames-1 {
			continue
		}
		for i, target := range targets {
			if width, height := target.Extent(); width != 2 || height != 2 {
				return fmt.Errorf("%s probe source is %dx%d, expected 2x2", targetFilterNames[i], width, height)
			}
			if target.Texture() != reads[i] {
				return fmt.Errorf("%s texture pointer changed", targetFilterNames[i])
			}
			if frame == o.frames-1 && o.recreate {
				set := target.Texture().DescriptorSet
				if i == 0 && set != old[i][0].DescriptorSet {
					return fmt.Errorf("fixed target was replaced")
				}
				if i > 0 && (set == old[i][0].DescriptorSet || set == old[i][1].DescriptorSet) {
					return fmt.Errorf("%s relative target was not rebuilt", targetFilterNames[i])
				}
			}
		}
		captured, err = r.CaptureFrame()
		if err != nil {
			return err
		}
		if captured.Bounds().Dx() != size || captured.Bounds().Dy() != size {
			return fmt.Errorf("resize did not reach %dx%d", size, size)
		}
		phase := "initial"
		if frame == o.frames-1 {
			phase = "final"
		}
		if err = checkTargetFilter(captured, phase); err != nil {
			return err
		}
	}
	if o.recreate {
		fmt.Println("filter recreation: 320x320 -> 384x384; fixed target retained; 3 relative samplers/descriptors replaced; stable texture pointers")
	}
	path := o.screenshot
	if path == "" {
		path = filepath.Join(".task", "target-filter.png")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return r.SaveScreenshot(path)
}

// Red/blue compare all four sampler choices. Green independently reads the
// linear/repeat history target through SetShaderTarget, including after resize.
// At 320x320 and rebuilt 384x384, max error was 0.001471; the visible control
// changed 17040/24576 pixels by >=4/255, max 127/255. Forcing linear to nearest
// failed probe 4 (0.125490 vs 0.312500); forcing repeat to clamp failed probe 8
// (0.376471 vs 0.125000). Both mutations also failed the sampler unit tests.
func checkTargetFilter(img *image.RGBA, phase string) error {
	want := [4][12]float64{
		{.125, .375, .625, .875, .125, .875, .375, .625, .375, .125, .375, .125},
		{.125, .375, .625, .875, .125, .875, .375, .625, .125, .625, .125, .125},
		{.125, .375, .625, .875, .3125, .6875, .25, .375, .375, .125, .375, .125},
		{.125, .375, .625, .875, .3125, .6875, .25, .375, .125, .625, .25, .375},
	}
	width, height := img.Bounds().Dx(), img.Bounds().Dy()
	maxError := 0.0
	for row, name := range targetFilterNames {
		fmt.Printf("filter %s %s:", phase, name)
		for col, expected := range want[row] {
			pixel := img.RGBAAt((2*col+1)*width/24, (2*row+1)*height/8)
			got := float64(pixel.R) / 255
			delta := math.Abs(got - expected)
			// Nearest at an exact texel boundary can select either neighbour.
			if row < 2 && (col == 6 || col == 7 || (row == 1 && col >= 10)) {
				other := .125
				if col == 10 {
					other = .375
				}
				if col == 11 {
					other = .625
				}
				delta = math.Min(delta, math.Abs(got-other))
			}
			globalDelta := math.Abs(float64(pixel.G)/255 - want[3][col])
			maxError = math.Max(maxError, math.Max(delta, globalDelta))
			fmt.Printf(" %.0f", float64(pixel.R))
			if delta > 1.0/255 || globalDelta > 1.0/255 || pixel.R != pixel.B {
				fmt.Println()
				return fmt.Errorf("%s %s probe %d: red %.6f expected %.6f; global %.6f expected %.6f (tolerance 1/255)", phase, name, col, got, expected, float64(pixel.G)/255, want[3][col])
			}
		}
		fmt.Println(" /255")
	}
	changed, maxDelta := 0, 0
	// The nearest/clamp row is a visible control for the linear/repeat row.
	for y := 0; y < height/4; y++ {
		for x := 0; x < width; x++ {
			delta := abs(int(img.RGBAAt(x, y).R) - int(img.RGBAAt(x, y+3*height/4).R))
			if delta >= 4 {
				changed++
			}
			maxDelta = max(maxDelta, delta)
		}
	}
	fmt.Printf("filter %s: max probe error %.6f (<=1/255); visible control %d pixels >=4/255, max %d/255\n", phase, maxError, changed, maxDelta)
	if changed == 0 || maxDelta < 32 {
		return fmt.Errorf("sampler control made no visible change")
	}
	return nil
}
