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
	xsky "github.com/derekmwright/glyphengine/x/sky"
	"github.com/derekmwright/glyphengine/x/terrainfield"
)

func init() {
	// GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

// patchGrid is vertices per patch side, so one patch is 2*(patchGrid-1)^2
// triangles over patchSize metres -- at the default, 2048 triangles over 4
// metres, which is the shape of a terrain tile rather than of a billboard,
// because a billboard field would overlap for reasons terrain does not.
//
// A variable rather than a constant because -density subdivides it, to put this
// field at a stated triangle density per covered pixel. It is written once in
// main before anything reads it, and read-only afterwards; the alternative --
// threading it through patchMesh and probeBounds -- would add a parameter to two
// functions that already duplicate each other's layout for the GPU-free probe.
var patchGrid = 33

const (
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

	// sky draws the dome. Off makes the frame clear to a flat colour and nothing
	// draw over it, which is what makes the captured frame's non-background pixel
	// count an exact count of COVERED pixels -- the denominator of every ratio in
	// the quad-overshading measurement. With the dome on, the horizon gradient
	// differs from the corner reference by more than the visibility threshold and
	// the count takes sky with it.
	//
	// Nothing else moves with it. The lighting, the fog and the palette come from
	// the environment, which is unchanged; only the dome's own draw goes, and that
	// is in PassSky, not in the pass being measured.
	sky bool

	// density is the target triangles after clipping per covered pixel, 0 when
	// nothing asked. It is reached by subdividing the patches rather than by adding
	// them, so the draw count, the layout and the depth complexity stay where they
	// are and the only thing that moves is how finely each patch is diced.
	density    float64
	densityTol float64

	// sweep turns the two arm assertions below into a report. The assertions
	// exist because the arms' NAMES are the claim this example makes and a
	// drifted arm would quietly approve or reject whatever was measured on it.
	// A sweep is the one caller for which that argument does not hold: it does
	// not assume a complexity, it READS the one this cell produced and plots
	// against it, so a cell at 1.8 is a cell at 1.8 rather than a broken
	// overlap arm. Everything else -- the visibility floor, the prepass-draw
	// check, the estimate agreement -- still applies, because a sweep cell that
	// rendered nothing or measured nothing is as useless as a drifted arm.
	sweep bool

	// bounds is each patch's world-space AABB, kept so the overlap measurement
	// can be made on the geometry that was actually submitted rather than on
	// what the layout intended.
	bounds []aabb

	// The engine's own per-frame estimate, sampled over the run rather than
	// read once at the end. The decision rule asks whether the estimate is on
	// the same side of the threshold on EVERY frame, and the last frame's
	// number cannot answer that -- a mode that toggled would leave no trace in
	// it. Sampled in LateUpdate, which runs once per frame, so these cover
	// every frame but the first (whose stats do not exist yet).
	estSamples        int
	estMin, estMax    float32
	estActive         int
	estPrepassDrawMin int
	estPrepassDrawMax int
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
			fx := float32(x) / float32(patchGrid-1)
			fz := float32(z) / float32(patchGrid-1)
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
			b := a + uint32(patchGrid)
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

// probeBounds is the patch layout Init builds, without the engine.
//
// It duplicates Init's loop over -count, which is the cost of having a GPU-free
// probe at all, and the duplication is bounded to the three lines that decide
// WHERE a patch goes: the mesh, the material, the lamps and the sky play no part
// in a bound. The spawn order is included even though a bound does not depend on
// it, so that a future layout change that does depend on it cannot make the two
// disagree silently.
func probeBounds(g *game) ([]aabb, error) {
	hm, err := terrainfield.Load("", 20261002)
	if err != nil {
		return nil, err
	}
	cols, rows := g.layout()
	bounds := make([]aabb, 0, g.count)
	for n := 0; n < g.count; n++ {
		_, _, b := patchMesh(hm, n)
		col, row := n%cols, n/cols
		if g.spawn == "backtofront" {
			row = rows - 1 - row
		}
		pos := mgl32.Vec3{
			(float32(col) - float32(cols-1)/2) * patchSize,
			0,
			float32(row) * patchSize,
		}
		bounds = append(bounds, aabb{b.min.Add(pos), b.max.Add(pos)})
	}
	return bounds, nil
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
	env := xsky.DefaultEnvironment()
	env.Sky.CloudSteps = xsky.CloudsOff
	e.Scene.Env = env

	// A fixed low sun and no day cycle: two runs of the same arm have to be
	// comparable, and a moving sun makes the shadow cascades and the fog
	// different work on every frame.
	env.Cycle.TimeOfDay = 0.32

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

// LateUpdate samples the previous frame's prepass estimate and decision.
//
// LateUpdate rather than Update because it runs once per frame rather than once
// per fixed tick, and the question is about frames. It reads the frame BEFORE
// this one, which is the only reading available from inside the loop -- the
// current frame's stats do not exist until it has been recorded.
func (g *game) LateUpdate(e *glyph.Engine, _ float32) {
	if e.FrameCount() == 0 {
		return
	}
	st := e.Renderer().Stats()
	if g.estSamples == 0 {
		g.estMin, g.estMax = st.PrepassEstimate, st.PrepassEstimate
		g.estPrepassDrawMin, g.estPrepassDrawMax = st.PrepassDraws, st.PrepassDraws
	}
	g.estSamples++
	g.estMin = min(g.estMin, st.PrepassEstimate)
	g.estMax = max(g.estMax, st.PrepassEstimate)
	g.estPrepassDrawMin = min(g.estPrepassDrawMin, st.PrepassDraws)
	g.estPrepassDrawMax = max(g.estPrepassDrawMax, st.PrepassDraws)
	if st.PrepassActive {
		g.estActive++
	}
}

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
//
// The FIRST return is the quantity the engine's own online estimate computes
// (renderer's depthComplexityEstimate, reported as RenderStats.PrepassEstimate),
// by the same method at a coarser grid. The second is the same grid divided by
// the whole viewport instead of by the covered part, and it is returned because
// it is the estimate that was tried first and does not work: measured here, the
// overlap arm reads 0.742 and the control 0.576, 29 % apart, where the covered
// normalisation puts them 3.2x apart. A grazing camera's field covers 23 % of
// the screen, so dividing by the whole viewport divides out most of the signal.
// -probe prints both, which is how that was settled without a GPU.
func overlapRatio(bounds []aabb, vp mgl32.Mat4, cells int) (overlap, sumOverViewport float64) {
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
		return 0, 0
	}
	return float64(total) / float64(covered), float64(total) / float64(cells*cells)
}

// onScreenRatio is overlapRatio with one rule changed: a bound whose projected
// rectangle lies entirely outside the viewport contributes nothing, rather than
// being clamped onto an edge cell.
//
// It exists because that is the rule the ENGINE's online estimate uses, and
// because the engine is right about it: a patch off the side of the screen
// rasterises no fragments, so it is neither overdraw nor work a prepass could
// remove. overlapRatio keeps the clamping it has always had, because its number
// is the one every record of this scene quotes and the one the arm floors are
// set against, and silently redefining it would make those records disagree with
// a tree that had not changed.
//
// The two differ only where bounds leave the frame. Measured on this field with
// -probe: 3.279 against 3.089 on the grazing arm, where the near rows are wider
// than the screen, and 1.0222 against 1.0222 on the overhead control, where
// nothing leaves it. So the gap is 5.8 % on one arm and nil on the other, which
// is exactly the shape that would have turned the engine-against-example check
// below into a coin toss against a 5 % tolerance.
func onScreenRatio(bounds []aabb, vp mgl32.Mat4, cells int) float64 {
	hits := make([]int, cells*cells)
	for _, b := range bounds {
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
		if hi[0] < -1 || lo[0] > 1 || hi[1] < -1 || lo[1] > 1 {
			continue
		}
		x0, x1 := ndcSpan(max(lo[0], -1), min(hi[0], 1), cells)
		y0, y1 := ndcSpan(max(lo[1], -1), min(hi[1], 1), cells)
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

// onScreenCoverage is onScreenRatio's two raw counts rather than their ratio: how
// many grid cells at least one bound reached, and how many bounds reached the
// frame at all.
//
// It exists so -density can derive a tessellation without a GPU. The target is
// triangles per COVERED pixel, so the solver needs the covered share of the
// viewport and the number of patches sharing it, and both fall out of the same
// walk onScreenRatio already does. A separate function rather than two more
// return values on that one, because its number is quoted in several records and
// a changed signature is where a quoted number quietly starts meaning something
// else.
func onScreenCoverage(bounds []aabb, vp mgl32.Mat4, cells int) (covered, visible int) {
	hits := make([]int, cells*cells)
	for _, b := range bounds {
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
		if behind || hi[0] < -1 || lo[0] > 1 || hi[1] < -1 || lo[1] > 1 {
			continue
		}
		visible++
		x0, x1 := ndcSpan(max(lo[0], -1), min(hi[0], 1), cells)
		y0, y1 := ndcSpan(max(lo[1], -1), min(hi[1], 1), cells)
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				hits[y*cells+x]++
			}
		}
	}
	for _, n := range hits {
		if n > 0 {
			covered++
		}
	}
	return covered, visible
}

// solvePatchGrid is the vertices-per-side that puts this layout at `density`
// triangles per covered pixel, from the projected bounds alone.
//
// An estimate, and deliberately a crude one: it counts every triangle of every
// on-screen patch, where what the GPU reports after clipping leaves out the
// patches and the parts of patches off the frame and keeps the back-facing
// triangles that shade nothing. So the number it lands on is checked against the
// MEASURED density after the run rather than trusted -- see the assertion in main,
// which is what makes this a knob with a gate rather than a claim.
//
// MEASURED, and the gap is large enough to matter: on the overlap arm at 1024
// patches and 1280x720 this estimates 207,019 covered pixels where the frame
// covers 75,656, a factor of 2.7, because a patch's bound is a BOX and the 180-cell
// grid rounds its coverage outward. The first solve therefore lands about 2.9x too
// dense -- 0.500 triangles per covered pixel against the 0.170 asked for -- and
// -grid converges it in one step: grid 4 measures 0.183 at 1280x720 and grid 10
// measures 0.182 at 3840x2160, both within 8 % of the target. Correcting the
// estimate by that factor was the alternative and it is not taken, because 2.7 is
// one camera's number on one layout and a constant fitted to it would be wrong,
// silently, on the next scene. A first guess with a gate is honest; a fitted
// constant that looks exact is not.
func solvePatchGrid(bounds []aabb, vp mgl32.Mat4, width, height int, density float64) (int, float64) {
	covered, visible := onScreenCoverage(bounds, vp, 180)
	if covered == 0 || visible == 0 || density <= 0 {
		return patchGrid, 0
	}
	coveredPixels := float64(covered) / (180 * 180) * float64(width) * float64(height)
	perPatch := density * coveredPixels / float64(visible)
	// 2*(n-1)^2 triangles per patch.
	n := 1 + int(math.Ceil(math.Sqrt(perPatch/2)))
	return max(2, min(512, n)), coveredPixels
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
	prepass := flag.String("prepass", "off", "depth prepass: off, on, auto, or empty -- auto decides per frame from the estimated depth complexity, and empty is on with the prepass's own draws withheld, which is `task prepass`'s control")
	sweep := flag.Bool("sweep", false, "this is one cell of a depth-complexity sweep: report the measured complexity instead of asserting an arm's floor")
	msaa := flag.Int("msaa", 0, "samples per pixel; 0 keeps the renderer default. Fragment shading is per pixel rather than per sample, so this moves the denominator of a per-covered-sample ratio and not the numerator")
	sky := flag.Bool("sky", true, "draw the sky dome. False leaves the frame's flat clear colour behind the field, which is what makes the visible-pixel count an exact covered-pixel count; the lighting and the fog do not move with it")
	density := flag.Float64("density", 0, "subdivide the patches to reach this many triangles (after clipping) per covered pixel; 0 leaves the tessellation alone. The achieved figure is measured and asserted, and it needs the pipeline statistics")
	densityTol := flag.Float64("densitytol", 0.5, "how far the measured triangles per covered pixel may sit from -density, as a fraction of it")
	grid := flag.Int("grid", 0, "vertices per patch side, overriding whatever -density derived; 0 leaves it to -density or to the default")
	probe := flag.Bool("probe", false, "print this layout and camera's depth complexity and exit, without opening a window or touching the GPU")
	flag.Parse()

	if *count <= 0 {
		log.Fatal("28-overdraw: -count must be positive")
	}
	if *spawn != "backtofront" && *spawn != "fronttoback" {
		log.Fatalf("28-overdraw: -spawn %q is not backtofront or fronttoback", *spawn)
	}
	// The third dimension of the measurement. The patches go through the
	// material pipeline and are neither skinned nor double-sided, so every one
	// of them qualifies for the prepass -- which is what makes this scene a
	// reading of the mechanism rather than of how much of the field it happened
	// to cover. `task bench -- -scene overdraw` runs off and on against both
	// arms; see renderer.WithDepthPrepass and docs/agents/game-loop.md.
	if *prepass != "off" && *prepass != "on" && *prepass != "auto" && *prepass != "empty" {
		log.Fatalf("28-overdraw: -prepass %q is not off, on, auto or empty", *prepass)
	}
	arm := "overlap"
	if !*overlap {
		arm = "control"
	}
	if *density < 0 {
		log.Fatal("28-overdraw: -density cannot be negative")
	}
	g := &game{count: *count, cols: *cols, overlap: *overlap, spawn: *spawn, eyeY: float32(*eyeY), pitch: float32(*pitch), lamps: *lamps, sweep: *sweep,
		sky: *sky, density: *density, densityTol: *densityTol}

	// -probe is the sweep's planning tool and it is deliberately GPU-free.
	// Depth complexity here is a function of the patch bounds and the
	// view-projection, both of which are plain arithmetic, so choosing which
	// (eye, pitch) cells span 1.0 to 3.5 does not need a window, a device or an
	// idle card -- and a sweep that spent GPU time discovering it was sampling
	// the same complexity five times over would be worse than no sweep.
	if *probe {
		bounds, err := probeBounds(g)
		if err != nil {
			log.Fatal(err)
		}
		g.bounds = bounds
		eye, center, up := g.view()
		proj := mgl32.Perspective(mgl32.DegToRad(g.fov()), float32(*width)/float32(*height), near, g.far())
		vp := proj.Mul4(mgl32.LookAtV(eye, center, up))
		ratio, sumOverViewport := overlapRatio(g.bounds, vp, 180)
		// The engine's grid is 192 cells per axis, not this measurement's 180,
		// and the 192-cell column is what says the difference costs nothing
		// worth caring about. Printed rather than asserted: it is a property of
		// the grid, not of the scene, and renderer.depthComplexityCells is
		// where the numbers are recorded. This figure does NOT converge quickly
		// in the cell count -- a sliver counts as one cell whatever the
		// resolution -- so a grid chosen for the convenience of a bitmask has to
		// be checked against the one the records quote rather than assumed close
		// to it.
		coarse, _ := overlapRatio(g.bounds, vp, 192)
		log.Printf("PROBE\tdepth_complexity\t%.3f\tdepth_complexity_192\t%.3f\ton_screen\t%.3f\ton_screen_192\t%.3f\tsum_over_viewport\t%.3f\tn_patches\t%d\teye\t%.2f\tpitch\t%.4f\tcols\t%d\tcount\t%d",
			ratio, coarse, onScreenRatio(g.bounds, vp, 180), onScreenRatio(g.bounds, vp, 192),
			sumOverViewport, len(g.bounds), g.eyeY, g.pitch, *cols, g.count)
		return
	}

	// The tessellation, before any mesh is built. Derived from the projected
	// bounds at the DEFAULT grid: the bound is the patch's own box and a finer
	// sampling of the same heightmap window moves it by a fraction of a metre, so
	// solving once is enough and solving twice would only chase that fraction.
	coveredEstimate := 0.0
	switch {
	case *grid > 0:
		patchGrid = *grid
	case *density > 0:
		bounds, err := probeBounds(g)
		if err != nil {
			log.Fatal(err)
		}
		eye, center, up := g.view()
		proj := mgl32.Perspective(mgl32.DegToRad(g.fov()), float32(*width)/float32(*height), near, g.far())
		patchGrid, coveredEstimate = solvePatchGrid(bounds, proj.Mul4(mgl32.LookAtV(eye, center, up)), *width, *height, *density)
	}
	if *grid > 0 || *density > 0 {
		log.Printf("28-overdraw: patch grid %d vertices per side, %d triangles per patch, %.0f covered pixels estimated",
			patchGrid, 2*(patchGrid-1)*(patchGrid-1), coveredEstimate)
	}

	opts := []glyph.Option{
		glyph.WithTitle(fmt.Sprintf("GlyphEngine - 28 Overdraw (%s)", arm)),
		glyph.WithWindowSize(*width, *height),
		glyph.WithProjection(g.fov(), near, g.far()),
		glyph.WithMaxFrames(*frames),
	}
	if *sky {
		// The dome is a shader slot the engine leaves empty; without this option
		// nothing draws a sky at all, which is exactly what -sky=false wants. The
		// environment below is set either way, so the sun, the fog and the clear
		// colour are the same in both.
		opts = append(opts, glyph.WithShaders(xsky.Shaders()))
	}
	if *msaa != 0 {
		opts = append(opts, glyph.WithMSAA(*msaa))
	}
	if *density > 0 {
		// The density claim is measured on the GPU, so the counters have to exist.
		// GLYPHENGINE_PIPELINE_STATS=1 turns them on for a run that only wants the
		// report; -density needs them and says so by asking.
		opts = append(opts, glyph.WithPipelineStatistics())
	}
	if *shot != "" {
		opts = append(opts, glyph.WithScreenshot(*shot))
	}
	switch *prepass {
	case "on", "empty":
		opts = append(opts, glyph.WithDepthPrepass(renderer.DepthPrepassOn))
	case "auto":
		opts = append(opts, glyph.WithDepthPrepass(renderer.DepthPrepassAuto))
	}

	e, err := glyph.New(g, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()
	if *prepass == "empty" {
		e.Renderer().SetDepthPrepassDebug(renderer.DepthPrepassDebugEmpty)
	}

	e.Run()

	// Everything below is outside the timed loop: it is the evidence that the
	// numbers above describe the scene they claim to.
	eye, center, up := g.view()
	proj := mgl32.Perspective(mgl32.DegToRad(g.fov()), float32(*width)/float32(*height), near, g.far())
	vp := proj.Mul4(mgl32.LookAtV(eye, center, up))
	ratio, sumOverViewport := overlapRatio(g.bounds, vp, 180)
	onScreen := onScreenRatio(g.bounds, vp, 180)
	// prepass_draws is in the line for the reason overlap_ratio is: the arm has
	// to be checkable from the sample rather than from the label on it. A
	// prepass arm that recorded zero prepass draws would otherwise read as a
	// prepass that cost nothing and saved nothing, which is indistinguishable
	// from a well-behaved one on the timings alone.
	st := e.Renderer().Stats()
	log.Printf("OVERDRAW\toverlap_ratio\t%.3f\tdepth_complexity\t%.3f\ton_screen\t%.3f\tsum_over_viewport\t%.3f\tn_patches\t%d\tprepass_draws\t%d\tprepass_estimate\t%.3f\tprepass_estimate_min\t%.3f\tprepass_estimate_max\t%.3f\tprepass_covered\t%.3f\tprepass_active_frames\t%d\tframes_sampled\t%d",
		ratio, ratio, onScreen, sumOverViewport, len(g.bounds), st.PrepassDraws, st.PrepassEstimate, g.estMin, g.estMax, st.PrepassCovered, g.estActive, g.estSamples)
	if *prepass == "on" && st.PrepassDraws == 0 {
		log.Fatal("28-overdraw: -prepass on recorded no prepass draws -- this sample measures the prepass not at all")
	}
	if *prepass == "off" && st.PrepassDraws != 0 {
		log.Fatalf("28-overdraw: -prepass off recorded %d prepass draws", st.PrepassDraws)
	}
	// The engine's online estimate against this example's offline grid, on the
	// same bounds and the same view-projection. They are the same quantity by
	// construction -- a sum of projected bound areas over the viewport's area --
	// computed two different ways: the engine in closed form from each mesh's
	// object-space box through its MVP, this grid by stamping NDC rectangles
	// into 180x180 cells. So a disagreement is a bug in one of them, not a
	// difference of definition, and this is the check that says so on a real
	// scene rather than on synthetic bounds.
	//
	// onScreenRatio and not overlapRatio: see that function for why the engine
	// is right to drop a bound that left the frame, and for the 5.8 % the two
	// differ by on this arm.
	//
	// What is left for the tolerance is the two grids' resolutions, 180 here
	// against the engine's 192, which -probe measures directly on these same
	// bounds: 0.9 % apart on the grazing arm and 0.07 % on the control. 3 % is
	// comfortably above that and far below the factor a real mistake in either
	// would produce. Both of those were measured rather than guessed: swapping the
	// mesh's box for its bounding SPHERE's box is a factor of 2.3 on an overhead
	// camera over this geometry and 3.7 on a grazing one (renderer's
	// TestDepthComplexityEstimateAgreesWithTheOfflineMeasurement reports both),
	// and normalising by the viewport instead of the covered area is 4.4x here --
	// 3.279 against 0.742, which -probe prints side by side.
	//
	// The engine's number also comes from a FRUSTUM-CULLED draw list, where this
	// one walks every patch. The two agree because the engine's own off-screen
	// rule already drops what the cull would have: a patch the cull keeps and the
	// rect test rejects contributes nothing either way. A disagreement here that
	// is not a grid difference is most likely that stopping to be true.
	if *prepass != "off" {
		if g.estSamples == 0 {
			log.Fatal("28-overdraw: no frames sampled the prepass estimate -- LateUpdate did not run")
		}
		const tol = 0.03
		if d := math.Abs(float64(st.PrepassEstimate) - onScreen); d > tol*onScreen {
			log.Fatalf("28-overdraw: the engine's prepass estimate %.3f and this example's grid %.3f differ by %.3f, more than %.0f%% -- one of the two is wrong",
				st.PrepassEstimate, onScreen, d, tol*100)
		}
		// Every frame on the same side of the threshold, or Auto was toggling
		// and the timings of this run are the timings of two different modes
		// averaged together.
		if g.estActive != 0 && g.estActive != g.estSamples {
			log.Fatalf("28-overdraw: the prepass was active on %d of %d sampled frames -- the mode toggled mid-run and these timings mix both",
				g.estActive, g.estSamples)
		}
		if g.estPrepassDrawMin != g.estPrepassDrawMax {
			log.Fatalf("28-overdraw: prepass draws ranged %d..%d over the run, so the frames being timed are not the same frame",
				g.estPrepassDrawMin, g.estPrepassDrawMax)
		}
	}
	// Auto's decision against the threshold it is supposed to be taken on,
	// rather than against this arm's name. The arm names are the right thing to
	// assert about the SCENE and the wrong thing to assert about the decision:
	// the overlap arm measures 3.28 at the default count and 2.56 at the 256 the
	// check matrices use, so a gate that said "auto must be on for the overlap
	// arm" would be a gate on -count, and it would have to be rewritten every
	// time the sweep moved the threshold.
	if *prepass == "auto" && !g.sweep {
		threshold, hysteresis := renderer.DepthPrepassThreshold()
		switch {
		case float32(onScreen) >= threshold && st.PrepassDraws == 0:
			log.Fatalf("28-overdraw: -prepass auto left the prepass off at on-screen depth complexity %.3f (estimate %.3f), at or above the threshold %.3f", onScreen, st.PrepassEstimate, threshold)
		case float32(onScreen) < threshold-hysteresis && st.PrepassDraws != 0:
			log.Fatalf("28-overdraw: -prepass auto ran the prepass at on-screen depth complexity %.3f (estimate %.3f), below the threshold %.3f less its band %.3f", onScreen, st.PrepassEstimate, threshold, hysteresis)
		}
		log.Printf("28-overdraw: auto decided %v at on-screen depth complexity %.3f against threshold %.3f (band %.3f)", st.PrepassActive, onScreen, threshold, hysteresis)
	}

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

	// The quad-overshading measurement, when this run is recording the counters.
	//
	// visible is the denominator and it is a count of pixels that differ from the
	// corner reference, so read it as the covered-pixel count only with
	// -sky=false: with the dome on, the horizon gradient differs from the corner
	// too and the count takes sky with it. Over-counting the denominator
	// UNDERSTATES the ratio, which is the conservative direction for a threshold
	// that reopens a decision, and the line says which way the run was taken so
	// nobody has to guess.
	//
	// Fragment shading is per covered pixel rather than per covered sample,
	// because this engine does not enable sample shading; cmd/quadcheck is what
	// establishes that on this device, and whether the invocation count includes
	// the helper lanes of a 2x2 quad at all.
	if st, err := e.MeanPipelineStats(); err == nil && st.Valid {
		inv := st.FragmentInvocations[renderer.PassOpaque]
		clipped := st.ClippingPrimitives[renderer.PassOpaque]
		perCovered := float64(inv) / float64(visible)
		trisPerCovered := float64(clipped) / float64(visible)
		gpu := e.MeanGPUTimings()
		log.Printf("QUADS	arm	%s	prepass	%s	msaa	%d	sky	%v	width	%d	height	%d	covered_px	%d	invocations	%d	inv_per_covered	%.4f	clipped	%d	tri_per_covered	%.4f	gpu_opaque	%.4f	gpu_total	%.4f	depth_complexity	%.3f	patch_grid	%d	frames	%d",
			arm, *prepass, e.Capabilities().MSAASamples, *sky, img.Rect.Dx(), img.Rect.Dy(), visible,
			inv, perCovered, clipped, trisPerCovered,
			gpu.Pass[renderer.PassOpaque], gpu.Total, ratio, patchGrid, st.Frames)
		// A ratio below 1.0 is arithmetically impossible on a pass that shades
		// every covered pixel at least once, so it does not mean "no overshading",
		// it means the denominator is wrong -- a capture that is not this frame, or
		// pixels in it that no opaque draw covered. Only asserted with the dome
		// off, because with it on the sky IS such a pixel and the ratio is
		// legitimately below 1 there; that is the same reason the measurement is
		// taken with -sky=false.
		if !*sky && perCovered < 1 {
			log.Fatalf("28-overdraw: %.4f invocations per covered pixel is below 1.0, which is impossible; the covered-pixel count is wrong (sky=%v)", perCovered, *sky)
		}
		if g.density > 0 {
			if d := math.Abs(trisPerCovered-g.density) / g.density; d > g.densityTol {
				log.Fatalf("28-overdraw: -density %.3f asked for, %.3f triangles after clipping per covered pixel measured (%.0f%% off, limit %.0f%%) at patch grid %d -- the flag did not reach its stated density; -grid converges it",
					g.density, trisPerCovered, d*100, g.densityTol*100, patchGrid)
			}
			log.Printf("28-overdraw: density %.3f asked for, %.3f measured at patch grid %d", g.density, trisPerCovered, patchGrid)
		}
	} else if g.density > 0 {
		log.Fatalf("28-overdraw: -density needs the pipeline statistics and this device or build has none: %v", err)
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
	case g.sweep:
		log.Printf("28-overdraw: sweep cell, measured depth complexity %.2f -- arm floors not applied", ratio)
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
