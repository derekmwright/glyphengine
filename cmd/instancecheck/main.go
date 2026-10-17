// Command instancecheck pins that replacing an instance set's placements every
// frame never disturbs a frame that is still being drawn.
//
// It is issue #188's reproduction reduced to a scene of its own: one ordinary
// InstanceSet alternating between two placement arrays of different lengths
// every frame, with the depth prepass on and shadows enabled so three passes
// read the set in the same frame. At a few chosen frames it captures the frame
// it has just submitted AFTER staging the next frame's placements and before
// that frame's DrawFrame. That ordering is the only one that can see the
// hazard: capturing first waits for the device and hides it.
//
// Every such capture must be byte-identical to a capture of the same frame with
// no update in between. Two controls keep that from being vacuous: consecutive
// frames must differ (the two arrays are visually distinct and the comparator
// sees it), and two runs of the quiet arm must agree (captures are comparable
// at all on this device).
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/window"
	"github.com/go-gl/mathgl/mgl32"
)

// cube is a unit cube centred on the origin. Solid geometry rather than quads
// because the hazard shows up along silhouettes, and a cube has eight of them
// at every angle.
func cube() ([]renderer.Vertex, []uint16) {
	type face struct {
		normal [3]float32
		corner [4][3]float32
	}
	faces := []face{
		{[3]float32{0, 0, 1}, [4][3]float32{{-0.5, -0.5, 0.5}, {0.5, -0.5, 0.5}, {0.5, 0.5, 0.5}, {-0.5, 0.5, 0.5}}},
		{[3]float32{0, 0, -1}, [4][3]float32{{0.5, -0.5, -0.5}, {-0.5, -0.5, -0.5}, {-0.5, 0.5, -0.5}, {0.5, 0.5, -0.5}}},
		{[3]float32{1, 0, 0}, [4][3]float32{{0.5, -0.5, 0.5}, {0.5, -0.5, -0.5}, {0.5, 0.5, -0.5}, {0.5, 0.5, 0.5}}},
		{[3]float32{-1, 0, 0}, [4][3]float32{{-0.5, -0.5, -0.5}, {-0.5, -0.5, 0.5}, {-0.5, 0.5, 0.5}, {-0.5, 0.5, -0.5}}},
		{[3]float32{0, 1, 0}, [4][3]float32{{-0.5, 0.5, 0.5}, {0.5, 0.5, 0.5}, {0.5, 0.5, -0.5}, {-0.5, 0.5, -0.5}}},
		{[3]float32{0, -1, 0}, [4][3]float32{{-0.5, -0.5, -0.5}, {0.5, -0.5, -0.5}, {0.5, -0.5, 0.5}, {-0.5, -0.5, 0.5}}},
	}
	var v []renderer.Vertex
	var idx []uint16
	for _, f := range faces {
		base := uint16(len(v))
		for _, c := range f.corner {
			v = append(v, renderer.Vertex{Pos: c, Normal: f.normal, Color: [3]float32{1, 1, 1}})
		}
		idx = append(idx, base, base+1, base+2, base, base+2, base+3)
	}
	return v, idx
}

// layout builds one placement array: a slab of cubes filling the view, several
// deep so most pixels are covered more than once. shift and spin are what makes
// the two arrays differ in every instance, so a frame drawn from a half-replaced
// buffer cannot look like either of them.
func layout(cols, rows, layers int, shift, spin float32) []renderer.MeshInstance {
	out := make([]renderer.MeshInstance, 0, cols*rows*layers)
	for l := 0; l < layers; l++ {
		z := -6 - float32(l)*3.5
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				fx := (float32(x) - float32(cols-1)/2) * (1.6 + shift)
				fy := (float32(y) - float32(rows-1)/2) * (1.25 + shift)
				ang := spin + float32(x*7+y*3+l*11)*0.1
				m := mgl32.Translate3D(fx+shift, fy-shift, z).
					Mul4(mgl32.HomogRotate3DY(ang)).
					Mul4(mgl32.HomogRotate3DX(ang * 0.5)).
					Mul4(mgl32.Scale3D(1.2, 1.2, 1.2))
				out = append(out, renderer.MeshInstance{
					Model: m,
					Tint: [4]float32{
						0.3 + 0.7*float32((x*5+l)%7)/7,
						0.3 + 0.7*float32((y*3+l)%5)/5,
						0.3 + 0.7*float32((x+y+l)%11)/11,
						1,
					},
				})
			}
		}
	}
	return out
}

// reverseZ is the engine's own projection: near maps to 1, far to 0, with Y
// flipped for Vulkan clip space. Geometry authored for a conventional 0 to 1
// depth range fails the depth test and draws nothing at all, so this check
// would pass on two empty frames with any other convention.
func reverseZ(fovDegrees, aspect, near, far float32) mgl32.Mat4 {
	proj := mgl32.Perspective(mgl32.DegToRad(fovDegrees), aspect, near, far)
	proj[10] = near / (far - near)
	proj[14] = (far * near) / (far - near)
	proj[5] *= -1
	return proj
}

type config struct {
	frames, width, height, msaa int
	bases                       []int
	floor                       float64
	validate, churn             bool
	dir                         string
}

// churnFrame reports whether frame i is one of the awkward ones: a swapchain
// rebuilt underneath the run, or a frame that is never drawn at all, which is
// the path a minimised window takes. Both leave an update staged across a frame
// that does not copy it, and a slot that forgot it still owed a copy would then
// draw placements from before the update. Never a capture frame, so what the
// comparison reads is an ordinary frame either way.
func churnFrame(cfg config, i int, capturing bool) (resize, skip bool) {
	if !cfg.churn || capturing || i < 5 || i%7 != 0 {
		return false, false
	}
	return i%14 == 0, i%14 != 0
}

// run renders the scene and returns the captures it was asked for. With early
// set, the placements for frame i+1 are staged after frame i is submitted and
// before frame i is read back -- the window in which the old single-buffer
// update corrupted the frame on the GPU.
func run(cfg config, early bool) (map[int]*image.RGBA, renderer.RenderStats, int, error) {
	var stats renderer.RenderStats
	w, err := window.New(cfg.width, cfg.height, "Instance staging check")
	if err != nil {
		return nil, stats, 0, err
	}
	defer w.Destroy()
	r, err := renderer.New(w,
		renderer.WithValidation(cfg.validate),
		renderer.WithMSAASamples(cfg.msaa),
		renderer.WithVSync(false),
		renderer.WithDepthPrepass(renderer.DepthPrepassOn),
	)
	if err != nil {
		return nil, stats, 0, err
	}
	defer r.Destroy()

	verts, idx := cube()
	mesh, err := r.CreateIndexedMesh(verts, idx)
	if err != nil {
		return nil, stats, 0, err
	}

	// Two counts as well as two sets of transforms: a count the GPU reads one
	// frame late draws instances out of memory the frame never wrote.
	arrays := [2][]renderer.MeshInstance{
		layout(22, 18, 10, 0, 0),
		layout(20, 18, 10, 0.35, 0.6),
	}
	capacity := max(len(arrays[0]), len(arrays[1]))
	set, err := r.CreateInstanceSet(mesh, capacity, arrays[0])
	if err != nil {
		return nil, stats, 0, err
	}

	aspect := float32(cfg.width) / float32(cfg.height)
	vp := reverseZ(60, aspect, 0.1, 200).Mul4(mgl32.LookAtV(
		mgl32.Vec3{0, 0, 0}, mgl32.Vec3{0, 0, -1}, mgl32.Vec3{0, 1, 0}))
	light := renderer.SceneLighting{
		VP:            vp,
		InvVP:         vp.Inv(),
		SunDir:        [3]float32{0.3, 0.8, 0.5},
		SunColor:      [3]float32{0.9, 0.85, 0.7},
		Ambient:       [3]float32{0.15, 0.17, 0.2},
		SkyColor:      [4]float32{0.05, 0.07, 0.1, 1},
		ShadowEnabled: true,
	}
	// The cascades are the camera's own view-projection: the shadow pass only
	// has to read the set in the same frame the other two do, and a cascade
	// that sees exactly what the camera sees is the strictest version of that.
	for i := range light.CascadeVPs {
		light.CascadeVPs[i] = vp
	}
	draws := []renderer.RenderObject{{
		Mesh:      mesh,
		Instances: set,
		MVP:       vp,
		Model:     mgl32.Ident4(),
		Color:     [3]float32{1, 1, 1},
		Roughness: 0.5,
	}}

	want := map[int]bool{}
	for _, b := range cfg.bases {
		want[b], want[b+1] = true, true
	}
	out := map[int]*image.RGBA{}
	for i := 0; i < cfg.frames; i++ {
		w.PollEvents()
		r.UpdateInstanceSet(set, arrays[i%2])
		resize, skip := churnFrame(cfg, i, want[i])
		if resize {
			r.NotifyResize()
		}
		if skip {
			continue
		}
		if err := r.DrawFrame(draws, nil, nil, nil, nil, light); err != nil {
			return nil, stats, 0, err
		}
		// The first frame drawn at or after the halfway point: a churn frame
		// may be skipped, and a frame that was never drawn has nothing to say
		// about how many passes read the set.
		if stats.DrawCalls == 0 && i >= cfg.frames/2 {
			stats = r.Stats()
		}
		if !want[i] {
			continue
		}
		if early {
			r.UpdateInstanceSet(set, arrays[(i+1)%2])
		}
		img, err := r.CaptureFrame()
		if err != nil {
			return nil, stats, 0, err
		}
		out[i] = img
	}
	return out, stats, len(arrays[0]), nil
}

// compare reports how many pixels differ at all, how many differ by more than
// eight channel levels -- the issue's own threshold -- and the largest single
// channel difference.
func compare(a, b *image.RGBA) (pixels, strong, maxLevel int) {
	for i := 0; i+3 < len(a.Pix) && i+3 < len(b.Pix); i += 4 {
		d := 0
		for c := 0; c < 3; c++ {
			x := int(a.Pix[i+c]) - int(b.Pix[i+c])
			if x < 0 {
				x = -x
			}
			if x > d {
				d = x
			}
		}
		if d > 0 {
			pixels++
		}
		if d > 8 {
			strong++
		}
		if d > maxLevel {
			maxLevel = d
		}
	}
	return pixels, strong, maxLevel
}

func writePNG(dir, name string, img *image.RGBA) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	err = png.Encode(f, img)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func check(cfg config) error {
	quiet, stats, count, err := run(cfg, false)
	if err != nil {
		return fmt.Errorf("quiet run: %w", err)
	}
	if stats.PrepassDraws == 0 {
		return fmt.Errorf("the depth prepass recorded nothing, so only two passes read the set")
	}
	if stats.Instances < 3*count {
		return fmt.Errorf("%d instances submitted for a %d-placement set: fewer than the shadow, prepass and lit passes together", stats.Instances, count)
	}
	fmt.Printf("scene: %d placements, %d instances submitted per frame over %d draw calls, %d prepass draws\n",
		count, stats.Instances, stats.DrawCalls, stats.PrepassDraws)

	// Control: the comparator has to be able to see the difference between the
	// two arrays, or "identical" below means nothing. Consecutive frames draw
	// different arrays.
	total := cfg.width * cfg.height
	floor := int(cfg.floor * float64(total))
	for _, b := range cfg.bases {
		_, strong, maxLevel := compare(quiet[b], quiet[b+1])
		if strong < floor {
			return fmt.Errorf("control: frames %d and %d differ at only %d pixels (floor %d); the two placement arrays are not distinct enough for this check to prove anything", b, b+1, strong, floor)
		}
		fmt.Printf("control: frames %d/%d differ at %d pixels, max %d/255 (floor %d)\n", b, b+1, strong, maxLevel, floor)
	}

	// Control: two runs of the same arm have to agree, or the comparison below
	// is measuring the device rather than the update.
	repeat, _, _, err := run(cfg, false)
	if err != nil {
		return fmt.Errorf("repeat run: %w", err)
	}
	for _, i := range frameList(cfg) {
		if !bytes.Equal(quiet[i].Pix, repeat[i].Pix) {
			pixels, strong, maxLevel := compare(quiet[i], repeat[i])
			return fmt.Errorf("control: two quiet runs disagree at frame %d (%d pixels, %d over 8 levels, max %d/255); captures are not comparable on this device", i, pixels, strong, maxLevel)
		}
	}
	fmt.Printf("control: two quiet runs are byte-identical at %d captures\n", len(frameList(cfg)))

	// The gate.
	probe, _, _, err := run(cfg, true)
	if err != nil {
		return fmt.Errorf("probe run: %w", err)
	}
	bad := 0
	for _, i := range frameList(cfg) {
		pixels, strong, maxLevel := compare(quiet[i], probe[i])
		if pixels == 0 {
			fmt.Printf("frame %d: identical with the next frame's placements staged before the capture\n", i)
			continue
		}
		bad++
		fmt.Printf("frame %d: %d pixels differ, %d by more than 8 levels, max %d/255\n", i, pixels, strong, maxLevel)
		if err := writePNG(cfg.dir, fmt.Sprintf("quiet-%d.png", i), quiet[i]); err != nil {
			return err
		}
		if err := writePNG(cfg.dir, fmt.Sprintf("probe-%d.png", i), probe[i]); err != nil {
			return err
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d of %d captures changed because the next frame's placements were staged while they were in flight", bad, len(frameList(cfg)))
	}
	return nil
}

func frameList(cfg config) []int {
	out := make([]int, 0, len(cfg.bases)*2)
	for _, b := range cfg.bases {
		out = append(out, b, b+1)
	}
	return out
}

func parseBases(s string, frames int) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("capture frame %q: %w", part, err)
		}
		if n < 1 || n+1 >= frames {
			return nil, fmt.Errorf("capture frame %d is outside 1..%d", n, frames-2)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no capture frames")
	}
	return out, nil
}

func main() {
	runtime.LockOSThread()
	cfg := config{}
	flag.IntVar(&cfg.frames, "frames", 200, "frames to render per run")
	flag.IntVar(&cfg.width, "width", 1280, "window width")
	flag.IntVar(&cfg.height, "height", 720, "window height")
	flag.IntVar(&cfg.msaa, "msaa", 4, "MSAA samples")
	flag.Float64Var(&cfg.floor, "floor", 0.02, "minimum share of pixels consecutive frames must differ by")
	flag.BoolVar(&cfg.validate, "validate", false, "enable the Vulkan validation layer")
	flag.StringVar(&cfg.dir, "out", ".task/instances", "where mismatching captures are written")
	flag.BoolVar(&cfg.churn, "churn", false, "rebuild the swapchain and drop frames between the captures")
	bases := flag.String("captures", "60,100,140,180", "frames to capture; each is captured with the one after it")
	flag.Parse()

	var err error
	cfg.bases, err = parseBases(*bases, cfg.frames)
	if err != nil {
		log.Fatal(err)
	}

	var messages bytes.Buffer
	log.SetOutput(io.MultiWriter(os.Stderr, &messages))
	err = check(cfg)
	log.SetOutput(os.Stderr)
	if err != nil {
		log.Fatal(err)
	}
	if bytes.Contains(messages.Bytes(), []byte("VULKAN ERROR")) || bytes.Contains(messages.Bytes(), []byte("VULKAN WARNING")) {
		log.Fatal("validation was not silent")
	}
	if cfg.validate && !bytes.Contains(messages.Bytes(), []byte("Vulkan validation layer enabled")) {
		log.Fatal("validation layer did not run")
	}
	fmt.Println("instancecheck: PASS")
}
