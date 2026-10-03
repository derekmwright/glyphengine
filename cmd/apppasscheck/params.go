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

//go:embed params-probe.frag.spv
var paramsProbe []byte

//go:embed params-display.frag.spv
var paramsDisplay []byte

// paramBlockFloats is the declared block, four vec4s, and also how many columns
// the probe chart has: one float per column, so a capture carries the whole
// block rather than a sample of it.
const paramBlockFloats = 16

// paramValue is the float the block's kth slot holds in generation g.
//
// Multiples of 1/255 on purpose: the display pass undoes the swapchain's sRGB
// encoding, so a captured byte is this number times 255 exactly, and the check
// below can hold itself to 1/255 instead of to a tolerance wide enough to hide
// a swapped field. The 13 and the 3 are coprime with nothing in particular --
// what they buy is that every slot differs from its neighbours within a
// generation (step 13) and from itself across generations (step 3), so neither
// a transposed block nor a block that is one generation stale can read as
// correct.
func paramValue(k, g int) float32 { return float32(13*(k+1)+3*g) / 255 }

// runParams proves the per-pass uniform block on real hardware, which is the
// half the unit tests cannot reach: they assert which bytes land in which host
// buffer, and this asserts that a shader reading the block sees them.
//
// A fullscreen pass declaring Params writes its own block out to a 16x1 target,
// one float per column; a second pass stretches that target over the frame with
// the sRGB encoding undone, and the capture is read back byte for byte.
//
// The block is then rewritten between consecutive frames and each frame checked
// against the generation that frame staged. That is the slot discipline on
// hardware: with two frames in flight, a block copied into the wrong slot
// publishes one generation late, and every frame after the first would read the
// previous generation's values -- which is a frame the unit tests can describe
// but only a device can actually run.
func runParams(o options) error {
	if o.frames < 8 {
		return fmt.Errorf("-params requires at least 8 frames")
	}
	w, err := window.New(320, 180, "Per-pass uniform block")
	if err != nil {
		return err
	}
	defer w.Destroy()
	r, err := renderer.New(w, renderer.WithValidation(o.validate), renderer.WithMSAASamples(o.msaa))
	if err != nil {
		return err
	}
	defer r.Destroy()

	chart, err := r.CreateRenderTarget(renderer.RenderTargetDesc{
		Name: "param block", Format: renderer.TargetRGBA16F, Width: paramBlockFloats, Height: 1,
	})
	if err != nil {
		return err
	}
	probe, err := r.CreateAppPass(renderer.AppPassDesc{
		Name: "param probe", Stage: renderer.StageBeforeScene, Target: chart, Fullscreen: true,
		Vert: shaders.DepthResolveVertSpv, Frag: paramsProbe, Params: paramBlockFloats * 4,
	})
	if err != nil {
		return err
	}
	display, err := r.CreateAppPass(renderer.AppPassDesc{
		Name: "param display", Stage: renderer.StageBeforeTonemap, Load: true,
		Reads: []*renderer.Texture{chart.Texture()}, Fullscreen: true,
		Vert: shaders.DepthResolveVertSpv, Frag: paramsDisplay,
	})
	if err != nil {
		return err
	}
	// The size error is part of the contract and costs nothing to assert here,
	// where a real pass with a real block is the one raising it.
	if err = probe.SetParams(make([]byte, paramBlockFloats*4+16)); err == nil {
		return fmt.Errorf("an oversized block was accepted")
	}
	fmt.Printf("oversized block refused: %v\n", err)
	if err = display.SetParams(make([]byte, 16)); err == nil {
		return fmt.Errorf("a pass declaring no block accepted one")
	}
	fmt.Printf("undeclared block refused: %v\n", err)

	light := renderer.SceneLighting{VP: mgl32.Ident4(), InvVP: mgl32.Ident4()}
	var block [paramBlockFloats * 4]byte
	var push [16]byte
	// The last four frames are the mid-run change: a new generation staged per
	// frame, each one checked in the frame that staged it. The frames before
	// them are warm-up on generation 0, so the first checked frame is already a
	// change and is not also the first frame the swapchain ever presented.
	first := o.frames - 4
	for frame := 0; frame < o.frames; frame++ {
		w.PollEvents()
		if w.WasResized() {
			r.NotifyResize()
		}
		generation := 0
		if frame >= first {
			generation = frame - first + 1
		}
		for k := range paramBlockFloats {
			binary.LittleEndian.PutUint32(block[k*4:], math.Float32bits(paramValue(k, generation)))
		}
		if err = probe.SetParams(block[:]); err != nil {
			return err
		}
		// The live swapchain extent, not the window size asked for: the display
		// pass maps a pixel to a chart column by dividing by it, and a frame one
		// pixel wider reads a column off the end of the block.
		width, height := r.Extent()
		binary.LittleEndian.PutUint32(push[0:], math.Float32bits(float32(width)))
		binary.LittleEndian.PutUint32(push[4:], math.Float32bits(float32(height)))
		if err = display.SetPushConstants(push[:]); err != nil {
			return err
		}
		if err = r.DrawFrame(nil, nil, nil, nil, nil, light); err != nil {
			return err
		}
		if frame < first {
			continue
		}
		captured, err := r.CaptureFrame()
		if err != nil {
			return err
		}
		if err = checkParamBlock(captured, generation); err != nil {
			return err
		}
	}
	path := o.screenshot
	if path == "" {
		path = filepath.Join(".task", "params.png")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return r.SaveScreenshot(path)
}

// checkParamBlock reads every float of the block out of one capture and
// compares it against the generation that frame staged.
//
// It reports the generation it matched as well as the one it wanted, because
// the interesting failure is not "wrong number" but "right number, one frame
// late" -- that is what a block written into the slot the GPU is still reading
// looks like from outside.
func checkParamBlock(img *image.RGBA, generation int) error {
	box := img.Bounds()
	fmt.Printf("param block generation %d:", generation)
	worst, lag := 0.0, generation > 0
	for k := range paramBlockFloats {
		pixel := img.RGBAAt(box.Min.X+(2*k+1)*box.Dx()/(2*paramBlockFloats), box.Min.Y+box.Dy()/2)
		got := float64(pixel.R) / 255
		want := float64(paramValue(k, generation))
		fmt.Printf(" %d", pixel.R)
		worst = math.Max(worst, math.Abs(got-want))
		lag = lag && math.Abs(got-float64(paramValue(k, generation-1))) <= 1.0/255
		if pixel.R != pixel.G || pixel.R != pixel.B {
			fmt.Println()
			return fmt.Errorf("block slot %d read %d/%d/%d; the probe writes one value to all three channels",
				k, pixel.R, pixel.G, pixel.B)
		}
	}
	fmt.Printf(" /255 (max error %.6f)\n", worst)
	if worst > 1.0/255 {
		if lag {
			return fmt.Errorf("the block is exactly one generation stale: the copy reached a slot this frame did not submit")
		}
		return fmt.Errorf("block generation %d: max error %.6f exceeds 1/255", generation, worst)
	}
	return nil
}
