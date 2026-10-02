// Command 28-overdraw is the engine's overdraw baseline: a grid of terrain
// patches under the most expensive fragment path it has, in two arms -- one
// where nearly all of them are hidden behind each other, and a control where
// none of them is.
//
// It is a measuring instrument rather than a demonstration of a feature. Nothing
// in the engine removes hidden opaque fragments today: the depth test discards
// them after their fragment shader has run, so an expensive shader pays for every
// surface behind every other one. Anything proposed to fix that -- an opt-in
// depth prepass, hierarchical-Z occlusion culling -- has to be measured against
// a scene where there is hidden work to remove AND against one where there is
// not, because every such mechanism costs something unconditionally and the only
// honest question is whether what it removes is worth what it costs. This is
// that pair of scenes, with the amount hidden measured rather than claimed.
//
//	go run ./28-overdraw                 # grazing view, patches hiding each other
//	go run ./28-overdraw -overlap=false  # the control, the same field from overhead
//	task bench -- -scene overdraw        # both arms, interleaved, three trials
//
// The control is the half that keeps the other half honest. A change that made
// the overlap arm faster and the control faster too is not removing hidden
// fragments, it is measuring something else -- and one that made the control
// slower is charging every scene for a win only this one gets. That is not
// hypothetical: ordering opaque draws front to back inside their state group
// saved 0.82 ms of a 7.06 ms opaque pass on the overlap arm at 3840x2160 and cost
// 1.01 ms on the control, which is what the control was built to find and why the
// policy does not exist. See docs/agents/game-loop.md.
//
// The fragment path is the material pipeline from 16-materials -- albedo,
// normal, metallic-roughness, occlusion and emissive maps, a Cook-Torrance BRDF
// per clustered light, shadow sampling and fog -- rather than the custom
// LitFrag from 24-custom-passes, which is the stock lit shader plus one texture
// lookup. The material path is the more expensive of the two the engine ships
// and it is the one a game reaches without writing a shader. Every patch shares
// ONE material, so every patch shares one SortKey and the whole field is a single
// state group: nothing in the recorder's state switching is mixed into the
// numbers. Five materials would be five groups and five sets of binds.
//
// GLYPHENGINE_FIXED_FRAME_TIME makes the run repeatable; GLYPHENGINE_BACKGROUND
// opens the same hidden window as the other examples.
package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"runtime"

	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/x/terrainfield"
)

func init() {
	// GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

const (
	// patchGrid is vertices per patch side and patchSize its world extent, so
	// one patch is 2048 triangles over 4 metres -- the shape of a terrain tile
	// rather than of a billboard, because a billboard field would overlap for
	// reasons terrain does not.
	patchGrid = 33
	patchSize = 4.0

	// mapSize is the edge of every generated material map. 256 rather than
	// 16-materials' 512: this example's startup builds five of them and the
	// occlusion blur is the slow one, and the maps are being sampled for their
	// cost here rather than read for their detail.
	mapSize = 256

	// relief scales the heightmap under each patch. Large enough that a near
	// patch hides the ones behind it at a grazing angle, which is the entire
	// mechanism this example depends on -- a flat field would overlap not at all
	// and the overlap arm would be a second control.
	relief = 6.0

	// near is the near plane; the far plane is per arm, because the overhead
	// camera's height grows with the field and a fixed 500 put the whole control
	// behind the far plane at 1024 patches -- which the visibility floor caught,
	// after a run that had already printed timings of an empty frame. See
	// game.far.
	near = 0.1

	// overlapFOV is the grazing arm's: a normal game field of view.
	overlapFOV = 50

	// lampRange is how far each lamp reaches. Generous on purpose: see the lamp
	// loop in Init.
	lampRange = 55

	// controlFOV is the overhead arm's, and it is narrow on purpose. A wide
	// overhead view of a field with 6 metres of relief is not overlap-free: a
	// ridge 25 degrees off the axis leans 2.8 metres across its neighbour, which
	// is most of a 4-metre patch, and the control would be quietly measuring
	// overdraw of its own. 12 degrees from four times the height leans 0.6
	// metres instead, and the measured depth complexity below is what says so.
	controlFOV = 12
)

type game struct {
	count   int
	cols    int
	overlap bool
	spawn   string
	eyeY    float32
	pitch   float32
	lamps   int

	// bounds is each patch's world-space AABB, kept so the overlap measurement
	// can be made on the geometry that was actually submitted rather than on
	// what the layout intended.
	bounds []aabb
}

type aabb struct{ min, max mgl32.Vec3 }

// patchMesh builds one patch from a window of the shared heightmap.
//
// A distinct window per patch, so no two patches are the same mesh: a field of
// one shared mesh drawn a thousand times would let a vertex cache flatter
// whichever arm happened to submit it in a tidier order, and the question here
// is about fragments.
func patchMesh(hm *glyph.Heightmap, seed int) ([]renderer.Vertex, []uint32, aabb) {
	// Walk the heightmap diagonally so successive patches sample genuinely
	// different ground rather than neighbouring strips of it. The window stays
	// inside |x|,|z| < 42: terrainfield's island falls off to flat zero at its
	// edges, and patches sampled out there have no relief at all, which is
	// exactly the thing this example needs them to have.
	ox := -40 + float32(seed%9)*9
	oz := -40 + float32((seed*5)%9)*9

	v := make([]renderer.Vertex, patchGrid*patchGrid)
	lo := mgl32.Vec3{math.MaxFloat32, math.MaxFloat32, math.MaxFloat32}
	hi := mgl32.Vec3{-math.MaxFloat32, -math.MaxFloat32, -math.MaxFloat32}
	for z := 0; z < patchGrid; z++ {
		for x := 0; x < patchGrid; x++ {
			fx := float32(x) / (patchGrid - 1)
			fz := float32(z) / (patchGrid - 1)
			h, _ := hm.HeightAt(ox+fx*patchSize*2, oz+fz*patchSize*2)
			n := hm.NormalAt(ox+fx*patchSize*2, oz+fz*patchSize*2)

			p := [3]float32{(fx - 0.5) * patchSize, h * relief / 14, (fz - 0.5) * patchSize}
			v[z*patchGrid+x] = renderer.Vertex{
				Pos:    p,
				Color:  [3]float32{0.55, 0.52, 0.46},
				Normal: n,
				UV:     [2]float32{fx * 2, fz * 2},
			}
			for i := 0; i < 3; i++ {
				lo[i] = min(lo[i], p[i])
				hi[i] = max(hi[i], p[i])
			}
		}
	}

	i := make([]uint32, 0, (patchGrid-1)*(patchGrid-1)*6)
	for z := 0; z < patchGrid-1; z++ {
		for x := 0; x < patchGrid-1; x++ {
			a := uint32(z*patchGrid + x)
			b := a + patchGrid
			// TerrainMesh's clockwise Vulkan winding, the same order
			// 26-mesh-ranges uses. Reversed, the whole field is back-face
			// culled and every timing in this example is a timing of nothing;
			// main's visibility floor is what catches that.
			i = append(i, a, a+1, b+1, b+1, b, a)
		}
	}
	return v, i, aabb{lo, hi}
}

// tileHeight is the field all five material maps are derived from: bevelled
// tiles separated by grooves. Deriving them from one function rather than
// authoring images is 16-materials' recipe, and it is here for the same reason
// -- five maps that disagree with each other are confusing to look at and none
// of them is wrong on its own.
func tileHeight(u, v float64) float64 {
	const tiles = 6
	fu := u*tiles - math.Floor(u*tiles)
	fv := v*tiles - math.Floor(v*tiles)
	d := math.Min(math.Min(fu, 1-fu), math.Min(fv, 1-fv))
	t := (d - 0.05) / 0.13
	t = math.Max(0, math.Min(1, t))
	return t * t * (3 - 2*t)
}

func buildMaps(r *renderer.Renderer) (renderer.MaterialOptions, error) {
	rgba := func(f func(u, v float64) (byte, byte, byte)) []byte {
		pix := make([]byte, mapSize*mapSize*4)
		for y := 0; y < mapSize; y++ {
			for x := 0; x < mapSize; x++ {
				u := (float64(x) + 0.5) / mapSize
				v := (float64(y) + 0.5) / mapSize
				cr, cg, cb := f(u, v)
				i := (y*mapSize + x) * 4
				pix[i+0], pix[i+1], pix[i+2], pix[i+3] = cr, cg, cb, 255
			}
		}
		return pix
	}

	albedo, err := r.CreateTexture(rgba(func(u, v float64) (byte, byte, byte) {
		h := tileHeight(u, v)
		return byte((0.52*h + 0.19*(1-h)) * 255), byte((0.49*h + 0.18*(1-h)) * 255), byte((0.42*h + 0.17*(1-h)) * 255)
	}), mapSize, mapSize)
	if err != nil {
		return renderer.MaterialOptions{}, err
	}

	// A normal map is numbers, not colour, so it goes up as data: read as sRGB,
	// 128 would arrive as 0.216 rather than 0.502 and every normal would lean
	// the same wrong way.
	const texel = 1.0 / mapSize
	normal, err := r.CreateDataTexture(rgba(func(u, v float64) (byte, byte, byte) {
		dhdu := (tileHeight(u+texel, v) - tileHeight(u-texel, v)) * 0.5 * 6
		dhdv := (tileHeight(u, v+texel) - tileHeight(u, v-texel)) * 0.5 * 6
		l := math.Sqrt(dhdu*dhdu + dhdv*dhdv + 1)
		return byte((-dhdu/l*0.5 + 0.5) * 255), byte((-dhdv/l*0.5 + 0.5) * 255), byte((1/l*0.5 + 0.5) * 255)
	}), mapSize, mapSize)
	if err != nil {
		return renderer.MaterialOptions{}, err
	}

	metalRough, err := r.CreateDataTexture(rgba(func(u, v float64) (byte, byte, byte) {
		// glTF's packing: roughness in G, metallic in B.
		return 255, byte((0.95 - 0.78*tileHeight(u, v)) * 255), 0
	}), mapSize, mapSize)
	if err != nil {
		return renderer.MaterialOptions{}, err
	}

	occlusion, err := r.CreateDataTexture(rgba(func(u, v float64) (byte, byte, byte) {
		// A box blur of the height field, so the darkening is wider than the
		// groove -- the tile edge beside a groove is shadowed by it too.
		const radius = 4
		sum, n := 0.0, 0.0
		for dy := -radius; dy <= radius; dy++ {
			for dx := -radius; dx <= radius; dx++ {
				sum += tileHeight(u+float64(dx)*texel, v+float64(dy)*texel)
				n++
			}
		}
		ao := byte((0.2 + 0.8*(sum/n)) * 255)
		return ao, ao, ao
	}), mapSize, mapSize)
	if err != nil {
		return renderer.MaterialOptions{}, err
	}

	emissive, err := r.CreateTexture(rgba(func(u, v float64) (byte, byte, byte) {
		g := 1 - tileHeight(u, v)
		c := byte(g * g * 255)
		return c, c, c
	}), mapSize, mapSize)
	if err != nil {
		return renderer.MaterialOptions{}, err
	}

	return renderer.MaterialOptions{
		Albedo: albedo, Normal: normal, MetallicRoughness: metalRough,
		Occlusion: occlusion, Emissive: emissive,
		EmissiveFactor: [3]float32{0.30, 0.55, 0.85}, EmissiveStrength: 2,
	}, nil
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()

	opts, err := buildMaps(r)
	if err != nil {
		return err
	}
	// ONE material for the whole field: see the package comment. Five would be
	// five state groups and five sets of binds mixed into the numbers.
	mat, err := r.CreateMaterial(opts)
	if err != nil {
		return err
	}

	hm, err := terrainfield.Load("", 20261002)
	if err != nil {
		return err
	}

	// A grid tiled edge to edge, laid out away from the camera along +Z. Rows
	// are what stack in screen space at a grazing angle, so the ROW count is the
	// depth complexity the overlap arm can reach: a field with as many rows as
	// columns tops out around 2.5 because the near row is wide on screen and the
	// far ones are narrow, and the union is mostly the near row. Fewer columns
	// and more rows buys depth complexity, and costs the control screen area.
	cols, rows := g.layout()
	g.bounds = make([]aabb, 0, g.count)
	for n := 0; n < g.count; n++ {
		v, idx, b := patchMesh(hm, n)
		m, err := r.CreateIndexedMesh32(v, idx)
		if err != nil {
			return err
		}

		col, row := n%cols, n/cols
		if g.spawn == "backtofront" {
			// Spawn the FARTHEST row first, so entity ids run back to front.
			//
			// Load-bearing, and the reason this is a flag rather than a
			// constant: the engine records an opaque state group in SortID
			// order, SortID is the entity id, and entity ids are handed out in
			// spawn order -- so spawn order IS submission order and it decides
			// how much hidden shading the depth buffer gets a chance to reject.
			// Back to front is the worst case, which is the one an occlusion
			// mechanism has the most to win on, so it is the default here.
			//
			// -spawn fronttoback is the other end of the range and it is worth
			// more than it looks: on this field at 3840x2160 it takes the opaque
			// pass from 7.06 ms to 5.95 ms with no engine change at all, which is
			// a larger saving than the rejected front-to-back sort managed.
			// Anything proposed to remove hidden work has to beat that, not just
			// beat the default. See docs/agents/game-loop.md.
			row = rows - 1 - row
		}
		pos := mgl32.Vec3{
			(float32(col) - float32(cols-1)/2) * patchSize,
			0,
			float32(row) * patchSize,
		}
		ent := e.Spawn()
		e.C.Transform.Set(ent, &glyph.Transform{Position: pos, Scale: mgl32.Vec3{1, 1, 1}})
		e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: m, Roughness: 0.6})
		e.C.MaterialRef.Set(ent, &glyph.MaterialRef{PBR: mat})
		// No shadow casting, so the shadow pass stays near zero and gpu_opaque
		// is the column the measurement is about. The patches still RECEIVE the
		// sun's shadow, so the fragment shader still does its cascade lookup.
		e.C.NoCastShadow.Set(ent, &glyph.NoCastShadow{})
		e.C.Static.Set(ent, &glyph.Static{})

		g.bounds = append(g.bounds, aabb{b.min.Add(pos), b.max.Add(pos)})
	}
	e.RebuildStatics()

	// Lamps over the field, because the clustered light loop is the engine's
	// main per-fragment cost multiplier and the issue behind this measurement
	// is about an expensive fragment shader. Unshadowed, so they cost fragment
	// time and not a second geometry pass.
	if g.lamps > 0 {
		lights := make([]glyph.PointLight, 0, g.lamps)
		// Spread over the field and ranged so that several reach any given
		// fragment: a clustered light a fragment is outside costs that fragment
		// nothing, so lights the froxel grid can cull add nothing to the
		// per-fragment price this example is about.
		lampCols := max(1, int(math.Round(math.Sqrt(float64(g.lamps)))))
		for i := 0; i < g.lamps; i++ {
			col, row := i%lampCols, i/lampCols
			lights = append(lights, glyph.PointLight{
				Pos: mgl32.Vec3{
					(float32(col) - float32(lampCols-1)/2) * float32(cols) * patchSize / float32(lampCols),
					7,
					float32(row) * float32(rows) * patchSize / float32(lampCols),
				},
				Range: lampRange,
				Color: mgl32.Vec3{1.0, 0.86, 0.68},
			})
		}
		e.SetPointLights(lights)
	}

	// Clouds off, and the sky left as plain as it comes. The cloud march costs
	// about as much as the whole opaque pass here, and gpu_opaque is the column
	// this example exists to move: a confounder the same size as the signal, in
	// a pass the policy cannot touch, buys nothing but noise. Fog stays on,
	// because the fragment shader's fog is part of what the patches pay.
	env := glyph.DefaultEnvironment()
	env.Sky.CloudSteps = glyph.CloudsOff
	e.Scene.Env = env

	// A fixed low sun and no day cycle: two runs of the same arm have to be
	// comparable, and a moving sun makes the shadow cascades and the fog
	// different work on every frame.
	e.SetTimeOfDay(0.32)
	e.SetDayCycleSpeed(0)

	e.SetCamera(g.view())

	log.Printf("28-overdraw: %d patches in a %d x %d grid, overlap=%v, spawn=%s, %d lamps", g.count, cols, rows, g.overlap, g.spawn, g.lamps)
	return nil
}

// layout is the grid shape: columns across, rows away from the camera. A
// partial last row is allowed rather than rounded off, so -count means what it
// says and the draw count is the same in both arms.
func (g *game) layout() (cols, rows int) {
	cols = g.cols
	if cols <= 0 || cols > g.count {
		cols = int(math.Ceil(math.Sqrt(float64(g.count))))
	}
	return cols, (g.count + cols - 1) / cols
}

// view returns the eye, centre and up for this arm.
//
// The two arms differ by where the camera is, not by where the patches are, and
// that is the one place this example does not do what the obvious reading of
// "spread the patches apart" would ask for. Terracing the field into as many
// non-overlapping screen bands as it has rows is not possible inside one
// frustum: non-overlapping draws need their own screen area, so separating a
// 16-row field at a grazing angle needs more vertical field of view than there
// is. Looking straight down at the SAME field gives exactly what the control is
// for -- the same patches, the same meshes, the same count, the same material,
// filling the same frame with nothing hidden behind anything -- and
// overlapRatio below measures that it really is overlap-free rather than
// assuming it.
func (g *game) view() (eye, center, up mgl32.Vec3) {
	_, rows := g.layout()
	midZ := float32(rows-1) * patchSize / 2
	if !g.overlap {
		// High enough that the field fills the frame vertically: half its depth
		// over the tangent of half the vertical field of view.
		extent := float32(rows) * patchSize / 2
		h := extent / float32(math.Tan(float64(mgl32.DegToRad(g.fov()))/2))
		eye = mgl32.Vec3{0, h, midZ}
		// Looking straight down, so up has to be a horizontal axis.
		return eye, mgl32.Vec3{0, 0, midZ}, mgl32.Vec3{0, 0, -1}
	}
	// An eye low over the field looking almost along it: the whole reason the
	// overlap arm overlaps, because at this angle a patch's relief covers the
	// patches behind it instead of sitting above them.
	eye = mgl32.Vec3{0, g.eyeY, -patchSize * 2}
	fwd := mgl32.Vec3{0, float32(math.Sin(float64(g.pitch))), float32(math.Cos(float64(g.pitch)))}
	return eye, eye.Add(fwd), mgl32.Vec3{0, 1, 0}
}

func (g *game) Update(*glyph.Engine, float32) {}

// overlapRatio is the mean screen-space depth complexity of the field: the
// summed screen area of every patch's projected bound over the area at least
// one of them covers.
//
// Measured rather than asserted, because the two arms' names are the claim this
// whole example makes and a layout that quietly stopped overlapping would turn
// the measurement into a comparison of two controls. It is computed from the
// patches' bounds rather than read off the GPU because it counts one DRAW hiding
// another, which is what a per-draw mechanism can act on; the relief inside a
// single patch hides itself and is left out on purpose. A per-pixel mechanism
// such as a depth prepass sees more hidden work than this reports, so read it as
// the floor on what is there.
//
// A bound's projected box is an over-estimate of the patch inside it, so this
// is an upper bound on depth complexity. It is used as a floor and a ceiling on
// the two arms, not as a number anything is divided by.
func overlapRatio(bounds []aabb, vp mgl32.Mat4, cells int) float64 {
	hits := make([]int, cells*cells)
	for _, b := range bounds {
		// NDC bounding box of the eight corners. A corner behind the eye has
		// w <= 0 and would project to the wrong side of the screen, so a patch
		// with any such corner is skipped rather than smeared across the grid.
		lo := mgl32.Vec2{math.MaxFloat32, math.MaxFloat32}
		hi := mgl32.Vec2{-math.MaxFloat32, -math.MaxFloat32}
		behind := false
		for c := 0; c < 8; c++ {
			p := mgl32.Vec4{b.min[0], b.min[1], b.min[2], 1}
			if c&1 != 0 {
				p[0] = b.max[0]
			}
			if c&2 != 0 {
				p[1] = b.max[1]
			}
			if c&4 != 0 {
				p[2] = b.max[2]
			}
			q := vp.Mul4x1(p)
			if q[3] <= 0 {
				behind = true
				break
			}
			lo[0], hi[0] = min(lo[0], q[0]/q[3]), max(hi[0], q[0]/q[3])
			lo[1], hi[1] = min(lo[1], q[1]/q[3]), max(hi[1], q[1]/q[3])
		}
		if behind {
			continue
		}
		x0, x1 := ndcSpan(lo[0], hi[0], cells)
		y0, y1 := ndcSpan(lo[1], hi[1], cells)
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				hits[y*cells+x]++
			}
		}
	}

	covered, total := 0, 0
	for _, n := range hits {
		if n > 0 {
			covered++
			total += n
		}
	}
	if covered == 0 {
		return 0
	}
	return float64(total) / float64(covered)
}

// fov is this arm's vertical field of view in degrees. See controlFOV.
func (g *game) fov() float32 {
	if g.overlap {
		return overlapFOV
	}
	return controlFOV
}

// far is this arm's far plane: far enough behind the field to hold all of it,
// whatever -count made it. Derived rather than fixed, because a far plane in
// front of the geometry culls the whole scene and times an empty frame.
func (g *game) far() float32 {
	eye, _, _ := g.view()
	_, rows := g.layout()
	depth := float32(rows) * patchSize
	return 1.5 * (eye.Len() + depth)
}

// ndcSpan is the cells of the occupancy grid an NDC interval covers, by CELL
// CENTRE rather than by overlap.
//
// By overlap, two boxes that merely share an edge each claim the cell the edge
// falls in, and a field of patches tiled edge to edge reads as 1.25 times
// covered when nothing is behind anything -- which is a measurement of the grid,
// not of the scene. Sampling centres makes a tiling come out at 1.00.
//
// Clamped rather than discarded at the screen edge: a patch wider than the
// screen still covers the screen. An interval that falls between two centres
// keeps the nearer one, so a patch too small to contain a centre still counts as
// one cell instead of vanishing -- the far rows of the grazing arm are exactly
// that, and dropping them would understate the thing being measured.
func ndcSpan(lo, hi float32, cells int) (int, int) {
	n := float32(cells)
	first := int(math.Ceil(float64((lo*0.5+0.5)*n - 0.5)))
	last := int(math.Floor(float64((hi*0.5+0.5)*n - 0.5)))
	if last < first {
		first = int(math.Round(float64(((lo+hi)*0.25+0.5)*n - 0.5)))
		last = first
	}
	first = max(0, min(cells-1, first))
	last = max(0, min(cells-1, last))
	if last < first {
		last = first
	}
	return first, last
}

func main() {
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	count := flag.Int("count", 1024, "number of patches")
	cols := flag.Int("cols", 0, "patches across the field; 0 makes it square")
	overlap := flag.Bool("overlap", true, "lay the patches out so they hide each other; false views the same field from above as the control")
	spawn := flag.String("spawn", "backtofront", "the order the patches are spawned in, which is the order they are submitted in: backtofront or fronttoback")
	lamps := flag.Int("lamps", 196, "unshadowed point lights over the field")
	eyeY := flag.Float64("eye", 2.4, "camera height")
	pitch := flag.Float64("pitch", -0.005, "camera pitch in radians; near zero is the grazing view the overlap arm needs")
	frames := flag.Int("frames", 200, "frames to render")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	flag.Parse()

	if *count <= 0 {
		log.Fatal("28-overdraw: -count must be positive")
	}
	if *spawn != "backtofront" && *spawn != "fronttoback" {
		log.Fatalf("28-overdraw: -spawn %q is not backtofront or fronttoback", *spawn)
	}
	arm := "overlap"
	if !*overlap {
		arm = "control"
	}
	g := &game{count: *count, cols: *cols, overlap: *overlap, spawn: *spawn, eyeY: float32(*eyeY), pitch: float32(*pitch), lamps: *lamps}
	opts := []glyph.Option{
		glyph.WithTitle(fmt.Sprintf("GlyphEngine - 28 Overdraw (%s)", arm)),
		glyph.WithWindowSize(*width, *height),
		glyph.WithProjection(g.fov(), near, g.far()),
		glyph.WithMaxFrames(*frames),
	}
	if *shot != "" {
		opts = append(opts, glyph.WithScreenshot(*shot))
	}

	e, err := glyph.New(g, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	e.Run()

	// Everything below is outside the timed loop: it is the evidence that the
	// numbers above describe the scene they claim to.
	eye, center, up := g.view()
	proj := mgl32.Perspective(mgl32.DegToRad(g.fov()), float32(*width)/float32(*height), near, g.far())
	vp := proj.Mul4(mgl32.LookAtV(eye, center, up))
	ratio := overlapRatio(g.bounds, vp, 180)
	log.Printf("OVERDRAW\toverlap_ratio\t%.3f\tn_patches\t%d", ratio, len(g.bounds))

	// A draw count alone accepted a completely back-face-culled field during
	// 26-mesh-ranges' development, and it would accept one here too: a field that
	// renders nothing is fast whatever is done to it and says nothing about any
	// of it. The corner pixel is the background reference at this camera.
	img, err := e.Renderer().CaptureFrame()
	if err != nil {
		log.Fatal(err)
	}
	visible := 0
	for y := 0; y < img.Rect.Dy(); y++ {
		for x := 0; x < img.Rect.Dx(); x++ {
			at := y*img.Stride + x*4
			d := max(absDiff(img.Pix[at], img.Pix[0]), absDiff(img.Pix[at+1], img.Pix[1]), absDiff(img.Pix[at+2], img.Pix[2]))
			if d >= 20 {
				visible++
			}
		}
	}
	floor := img.Rect.Dx() * img.Rect.Dy() / 20
	if visible < floor {
		log.Fatalf("28-overdraw: %d pixels differ >=20/255 from background, need %d -- this frame is too empty to time", visible, floor)
	}

	// The arm labels, checked. The overlap arm has to be genuinely stacked and
	// the control has to be genuinely flat, or the comparison between them is
	// between two things that are not what they are called -- and since this
	// scene exists to judge other people's changes, a drifted arm would not fail
	// here, it would quietly approve or reject whatever was measured on it.
	//
	// Measured: 3.28 and 1.02 at the default 1024 patches, 2.56 and 1.01 at the
	// 256 the smoke and validate matrices use. The floors sit under both, and
	// they are not a noise tolerance -- this number comes from projected bounds
	// and an occupancy grid, so it is the same on every machine at a given
	// -count, and a floor that trips means the LAYOUT changed. 3.3 is close to
	// the ceiling this layout can reach: the patches tile edge to edge because
	// the control needs them to, so all the overlap has to come from the grazing
	// view, and the near row is wide on screen while the far rows are narrow.
	switch {
	case g.overlap && ratio < 2.5:
		log.Fatalf("28-overdraw: the overlap arm measures depth complexity %.2f, need 2.5 -- nothing is hiding behind anything", ratio)
	case !g.overlap && ratio > 1.05:
		log.Fatalf("28-overdraw: the control measures depth complexity %.2f, need at most 1.05 -- the control overlaps too", ratio)
	}
	log.Printf("28-overdraw: depth complexity %.2f, %d visible pixels (floor %d), %d frames", ratio, visible, floor, e.FrameCount())
}

func absDiff(a, b byte) int {
	if a > b {
		return int(a) - int(b)
	}
	return int(b) - int(a)
}
