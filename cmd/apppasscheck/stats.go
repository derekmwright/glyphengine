package main

// The -stats mode: what a named application pass's pipeline-statistics counters
// read on real hardware, against numbers known from the geometry rather than
// from the counters themselves.
//
// This mode lives in apppasscheck rather than in quadcheck, and the split is
// along what each one establishes. quadcheck answers a question about the
// DEVICE -- whether FRAGMENT_SHADER_INVOCATIONS includes the helper lanes of a
// 2x2 quad -- from engine geometry, and that answer is a precondition here
// rather than something to re-derive. This mode answers a question about the
// ENGINE: whether a named application pass's counters are that pass's. It needs
// several application passes, a dispatch, a disable mid-run and a graph rebuild,
// which is what this harness already owns, including the SPIR-V for an
// application mesh pass.
//
// The geometry is axis-aligned rectangles on exact pixel boundaries, which is
// what makes the two assertions exact rather than approximate. Every quad a
// rectangle touches is fully covered, so there are no helper lanes to argue
// about and the invocation count is the covered pixel count whichever answer
// quadcheck got on the device in hand. The two triangles of a rectangle share
// its diagonal, and the rasterizer's fill rule gives each sample on that edge to
// exactly one of them, so no pixel is covered twice and no pixel is missed.
//
// MSAA 1, likewise not a preference: at higher sample counts a pixel on a
// primitive edge is shaded once per covering primitive and the resolve makes the
// readback's idea of "covered" its own question. The rectangles have no
// partially covered edge pixels at all, and one sample per pixel is what keeps
// that true.
//
// Measured on an AMD Radeon RX 7900 XTX (2026-10-09) at 640x360, MSAA 1, under
// GLYPHENGINE_FIXED_FRAME_TIME: "stats wide" reads 50,400 invocations over
// 50,400 covered pixels and 2 primitives after clipping for 2 submitted; "stats
// grid" reads 7,632 over 7,632 and 8 for 8. Exact to the unit, both passes, and
// bit-identical across three consecutive frames. The dispatch reads not
// bracketed, and the grid pass reads invalid-and-bracketed once disabled.
//
// Verified to fail, by transposing the readback's row-to-query mapping in
// pipelineStats.collectApps so each pass is given its neighbour's query:
// "stats wide: 8 primitives after clipping, submitted 2". The counts cross and
// the mode stops.
//
// A break that did NOT fail is worth recording, because it is the obvious one to
// try: swapping the two indices in the slot table at reset, so the whole
// mapping is relabelled. The recording side and the readback side read that one
// table, so a consistent relabelling is invisible -- and correctly so, since
// which query a pass owns is an internal name. Only a transposition between the
// two sides can cross the counts, which is what the break above does.

import (
	"encoding/binary"
	"fmt"
	"image"
	"log"
	"math"

	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/window"
	"github.com/go-gl/mathgl/mgl32"
)

// statsVisible is how far from black a channel has to be to count as covered.
// The frame clears to an exact zero and nothing in it spreads -- bloom is off --
// so anything above the tonemap's darkest dither is this mode's geometry.
const statsVisible = 8

// statsFrames is how many consecutive readings have to agree. Three, because two
// could agree by both being the same stale reading, and the point of the
// repeatability claim is that the counts are exact rather than sampled.
const statsFrames = 3

// statsPass is one application graphics pass and what its geometry says its
// counters must be.
type statsPass struct {
	name   string
	colour [3]float32
	// rects are x0,y0,x1,y1 in pixels, half-open: a sample at pixel centre
	// (i+0.5, j+0.5) is covered exactly when x0 <= i < x1 and y0 <= j < y1.
	rects [][4]int
	pass  *renderer.AppPass
}

func (p *statsPass) triangles() int { return 2 * len(p.rects) }

func (p *statsPass) pixels() int {
	n := 0
	for _, r := range p.rects {
		n += (r[2] - r[0]) * (r[3] - r[1])
	}
	return n
}

// vertices turns the rectangles into clip-space triangles.
//
// Clip space directly, with the view-projection and the model matrix both
// identity, because the claim is about pixel boundaries and anything else in the
// chain would put a rounding step between a pixel index and a vertex. No
// culling is configured for application pipelines, so the winding is free.
func (p *statsPass) vertices(w, h int) []renderer.Vertex {
	at := func(x, y int) renderer.Vertex {
		return renderer.Vertex{
			Pos:    [3]float32{2*float32(x)/float32(w) - 1, 2*float32(y)/float32(h) - 1, 0.5},
			Normal: [3]float32{0, 0, 1},
			Color:  p.colour,
		}
	}
	verts := make([]renderer.Vertex, 0, 6*len(p.rects))
	for _, r := range p.rects {
		x0, y0, x1, y1 := r[0], r[1], r[2], r[3]
		verts = append(verts, at(x0, y0), at(x1, y0), at(x1, y1))
		verts = append(verts, at(x0, y0), at(x1, y1), at(x0, y1))
	}
	return verts
}

// statsLayout places the two passes' geometry for an extent.
//
// Derived from the extent rather than written as constants for 640x360: the
// window manager is free to round a requested size, and a constant that stopped
// matching the frame would move the covered pixel count without moving the
// expectation. Integer arithmetic throughout, so every edge lands on a pixel
// boundary.
func statsLayout(w, h int) (wide, grid [][4]int) {
	wide = [][4]int{{w / 16, h / 8, w / 2, h * 5 / 8}}
	cw, ch := w/12, h/10
	x0, y0 := w*5/8, h/8
	for j := 0; j < 2; j++ {
		for i := 0; i < 2; i++ {
			x, y := x0+i*cw*3/2, y0+j*ch*3/2
			grid = append(grid, [4]int{x, y, x + cw, y + ch})
		}
	}
	return wide, grid
}

// statsCovered counts the pixels each pass reached, by colour.
//
// By colour and not by region: a region count would be the same statement as
// the expectation and could not catch a pass painting outside its own
// rectangles, which is exactly what a swapped query index or a wrong viewport
// looks like. A pixel both passes reached is counted by neither and shows up as
// both arms disagreeing with their geometry.
func statsCovered(img *image.RGBA) (red, green, other int) {
	for y := 0; y < img.Rect.Dy(); y++ {
		for x := 0; x < img.Rect.Dx(); x++ {
			at := y*img.Stride + x*4
			r, g := img.Pix[at] >= statsVisible, img.Pix[at+1] >= statsVisible
			switch {
			case r && !g:
				red++
			case g && !r:
				green++
			case r && g:
				other++
			}
		}
	}
	return red, green, other
}

// runStats is the mode's body.
func runStats(o options) error {
	w, err := window.New(640, 360, "Application pass statistics")
	if err != nil {
		return err
	}
	defer w.Destroy()
	sh := renderer.DefaultShaders()
	sh.LitFrag = lit
	// MSAA 1 whatever -msaa asked for, for the reason at the top of this file.
	r, err := renderer.New(w, renderer.WithShaders(sh), renderer.WithValidation(o.validate),
		renderer.WithMSAASamples(1), renderer.WithPipelineStatistics())
	if err != nil {
		return err
	}
	defer r.Destroy()
	if !r.PipelineStatsSupported() {
		caps := r.Capabilities()
		return fmt.Errorf("%s does not support pipelineStatisticsQuery, so there is nothing to measure here", caps.GPUName)
	}
	if !r.Capabilities().GPUTimestamps {
		return fmt.Errorf("this device cannot timestamp the graphics queue, so no application pass can be Timed and none can be counted")
	}
	// Bloom off: it is the only thing in this frame that would spread a covered
	// pixel onto its neighbour, and a covered pixel count is the denominator of
	// every claim below.
	r.SetBloom(0, 1, 0, 1)

	width, height := r.Extent()
	if width <= 0 || height <= 0 {
		return fmt.Errorf("the swapchain is %dx%d", width, height)
	}
	wideRects, gridRects := statsLayout(width, height)
	passes := []*statsPass{
		{name: "stats wide", colour: [3]float32{1, 0, 0}, rects: wideRects},
		{name: "stats grid", colour: [3]float32{0, 1, 0}, rects: gridRects},
	}
	if err := statsSane(passes, width, height); err != nil {
		return err
	}

	for _, p := range passes {
		mesh, err := r.CreateMesh(p.vertices(width, height))
		if err != nil {
			return err
		}
		p.pass, err = r.CreateAppPass(renderer.AppPassDesc{
			Name: p.name, Stage: renderer.StageBeforeBloom, Load: true,
			Reads: []*renderer.Texture{r.FallbackTexture()},
			Vert:  meshVert, Frag: flat, Timed: true,
		})
		if err != nil {
			return err
		}
		p.pass.SetDraws([]renderer.RenderObject{{Mesh: mesh, Model: mgl32.Ident4(), MVP: mgl32.Ident4()}})
		push := make([]byte, 16)
		putFloats(push, p.colour[0], p.colour[1], p.colour[2], 1)
		if err := p.pass.SetPushConstants(push); err != nil {
			return err
		}
	}

	// A dispatch in the same run, so "a compute pass reads not bracketed" is
	// measured on the device beside the passes that are rather than asserted in
	// a unit test alone. It writes a target nothing samples; what it computes is
	// beside the point, its row is the point.
	computed, err := r.CreateRenderTarget(renderer.RenderTargetDesc{
		Name: "stats compute output", Format: renderer.TargetR32F, Scale: 0.25, Storage: true})
	if err != nil {
		return err
	}
	compute, err := r.CreateAppCompute(renderer.AppComputeDesc{
		Name: "stats dispatch", Stage: renderer.StageBeforeScene, Comp: blur,
		Reads:  []*renderer.Texture{r.FallbackTexture(), r.FallbackTexture(), r.FallbackTexture()},
		Writes: []*renderer.RenderTarget{computed}, Timed: true,
	})
	if err != nil {
		return err
	}
	cw, chh := computed.Extent()
	compute.SetDispatch(uint32((cw+7)/8), uint32((chh+7)/8), 1)

	// A black frame with no sky dome and no draws: every non-black pixel below is
	// one of the two passes.
	light := renderer.SceneLighting{VP: mgl32.Ident4(), InvVP: mgl32.Ident4(), SkyColor: [4]float32{0, 0, 0, 1}}
	draw := func(n int) error {
		for i := 0; i < n; i++ {
			if err := r.DrawFrame(nil, nil, nil, nil, nil, light); err != nil {
				return err
			}
		}
		return nil
	}
	// Enough frames for a slot's results to have come back before the readings
	// that are compared; -frames is the harness default and this mode ignores
	// anything below its own minimum rather than reporting an unready reading.
	warmup := max(o.frames, 8)
	if err := draw(warmup); err != nil {
		return err
	}

	img, err := r.CaptureFrame()
	if err != nil {
		return err
	}
	red, green, both := statsCovered(img)
	if both != 0 {
		return fmt.Errorf("%d pixels were reached by both passes; their regions must be disjoint", both)
	}
	covered := map[string]int{"stats wide": red, "stats grid": green}
	for _, p := range passes {
		if covered[p.name] != p.pixels() {
			return fmt.Errorf("%s covered %d pixels, its geometry says %d; the frame is not what this mode thinks it is",
				p.name, covered[p.name], p.pixels())
		}
		fmt.Printf("%s: %d triangles submitted, %d pixels covered (read back from the frame)\n",
			p.name, p.triangles(), p.pixels())
	}

	// statsFrames consecutive readings, each asserted against the geometry and
	// against the first: exact counters under a fixed clock are bit-identical
	// frame to frame, and a counter that drifted would be measuring something
	// other than this pass.
	var first []renderer.AppPipelineStats
	for frame := 0; frame < statsFrames; frame++ {
		if err := draw(1); err != nil {
			return err
		}
		st, err := r.PipelineStats()
		if err != nil {
			return err
		}
		if !st.Valid {
			return fmt.Errorf("the statistics are not valid after %d frames", warmup+frame+1)
		}
		for _, p := range passes {
			row, ok := st.Pass(p.name)
			if !ok {
				return fmt.Errorf("no statistics row for %q", p.name)
			}
			if !row.Bracketed || !row.Valid {
				return fmt.Errorf("%s: bracketed=%v valid=%v; a graphics pass that renders must be both",
					p.name, row.Bracketed, row.Valid)
			}
			if int(row.ClippingPrimitives) != p.triangles() {
				return fmt.Errorf("%s: %d primitives after clipping, submitted %d",
					p.name, row.ClippingPrimitives, p.triangles())
			}
			if int(row.FragmentInvocations) != p.pixels() {
				return fmt.Errorf("%s: %d fragment invocations over %d covered pixels",
					p.name, row.FragmentInvocations, p.pixels())
			}
			fmt.Printf("APPSTATS frame=%d pass=%q invocations=%d clipped=%d covered=%d\n",
				frame, p.name, row.FragmentInvocations, row.ClippingPrimitives, p.pixels())
		}
		if row, ok := st.Pass("stats dispatch"); !ok || row.Bracketed || row.Valid ||
			row.FragmentInvocations != 0 || row.ClippingPrimitives != 0 {
			return fmt.Errorf("the dispatch reads %+v (found %v); a dispatch has no fragment stage and must read not bracketed", row, ok)
		}
		if frame == 0 {
			first = st.App
			continue
		}
		if err := statsSameRows(first, st.App); err != nil {
			return fmt.Errorf("frame %d: %w", frame, err)
		}
	}
	fmt.Println("the dispatch reads not bracketed, and every row is identical across three frames")

	// Switched off mid-run: the row has to go invalid rather than keep the number
	// it had or report a measured zero. Three frames is past the slot delay.
	passes[1].pass.SetEnabled(false)
	if err := draw(6); err != nil {
		return err
	}
	st, err := r.PipelineStats()
	if err != nil {
		return err
	}
	off, ok := st.Pass(passes[1].name)
	if !ok {
		return fmt.Errorf("%s lost its row when it was disabled; a disabled pass keeps its row", passes[1].name)
	}
	if off.Valid || off.FragmentInvocations != 0 || off.ClippingPrimitives != 0 {
		return fmt.Errorf("%s still reads %+v after being disabled", passes[1].name, off)
	}
	if !off.Bracketed {
		return fmt.Errorf("%s reads not bracketed while disabled; it can hold a query, it just did not run", passes[1].name)
	}
	on, ok := st.Pass(passes[0].name)
	if !ok || !on.Valid || int(on.FragmentInvocations) != passes[0].pixels() {
		return fmt.Errorf("%s changed when its neighbour was disabled: %+v", passes[0].name, on)
	}
	fmt.Printf("disabled %s reads invalid and bracketed; %s is unchanged at %d invocations\n",
		passes[1].name, passes[0].name, on.FragmentInvocations)

	// A rebuild cannot leave a stale number either. Destroying the dispatch is a
	// rebuild, and the live pass's row has to come back invalid and then be
	// measured again rather than carrying its old count through.
	r.DestroyAppCompute(compute)
	if err := draw(1); err != nil {
		return err
	}
	st, err = r.PipelineStats()
	if err != nil {
		return err
	}
	if _, ok := st.Pass("stats dispatch"); ok {
		return fmt.Errorf("the destroyed dispatch kept its row")
	}
	if row, ok := st.Pass(passes[0].name); !ok || row.Valid {
		return fmt.Errorf("%s reads %+v immediately after a graph rebuild; a rebuild invalidates", passes[0].name, row)
	}
	if err := draw(6); err != nil {
		return err
	}
	st, err = r.PipelineStats()
	if err != nil {
		return err
	}
	if row, ok := st.Pass(passes[0].name); !ok || !row.Valid || int(row.FragmentInvocations) != passes[0].pixels() {
		return fmt.Errorf("%s did not come back after the rebuild: %+v", passes[0].name, row)
	}
	fmt.Println("a graph rebuild invalidates the rows and the next measured frame refills them")

	r.DestroyRenderTarget(computed)
	for _, p := range passes {
		r.DestroyAppPass(p.pass)
	}
	log.Printf("apppasscheck -stats: %dx%d, %s %d px / %d tris, %s %d px / %d tris",
		width, height, passes[0].name, passes[0].pixels(), passes[0].triangles(),
		passes[1].name, passes[1].pixels(), passes[1].triangles())
	return nil
}

// statsSane is the mode's claim about its own geometry, checked before any
// counter is believed: inside the frame, non-empty, and no two rectangles
// overlapping. Overlap is the one that matters -- two passes sharing a pixel
// would make the colour readback disagree with the geometry for a reason that
// has nothing to do with the counters.
func statsSane(passes []*statsPass, w, h int) error {
	var all [][4]int
	for _, p := range passes {
		if p.pixels() == 0 {
			return fmt.Errorf("%s covers nothing at %dx%d", p.name, w, h)
		}
		for _, r := range p.rects {
			if r[0] < 0 || r[1] < 0 || r[2] > w || r[3] > h || r[0] >= r[2] || r[1] >= r[3] {
				return fmt.Errorf("%s: rectangle %v is not inside the %dx%d frame", p.name, r, w, h)
			}
			for _, q := range all {
				if r[0] < q[2] && q[0] < r[2] && r[1] < q[3] && q[1] < r[3] {
					return fmt.Errorf("rectangles %v and %v overlap", r, q)
				}
			}
			all = append(all, r)
		}
	}
	if passes[0].pixels() == passes[1].pixels() || passes[0].triangles() == passes[1].triangles() {
		return fmt.Errorf("the two passes must differ in both area and triangle count, or swapping their counts would be invisible")
	}
	return nil
}

// statsSameRows compares two readings row by row.
func statsSameRows(a, b []renderer.AppPipelineStats) error {
	if len(a) != len(b) {
		return fmt.Errorf("%d rows, want %d", len(b), len(a))
	}
	for i := range a {
		if a[i] != b[i] {
			return fmt.Errorf("row %d: %+v, want %+v", i, b[i], a[i])
		}
	}
	return nil
}

// putFloats writes little-endian float32s, which is what the push block is.
func putFloats(dst []byte, values ...float32) {
	for i, v := range values {
		binary.LittleEndian.PutUint32(dst[i*4:], math.Float32bits(v))
	}
}
