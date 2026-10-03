// Command 29-ridge is the engine's occlusion baseline: a dense LOD forest on
// both sides of a ridge, in two arms -- one where the far side is hidden behind
// the crest, and a control where the same trees are all in view.
//
// It is a measuring instrument rather than a demonstration of a feature, and it
// is the LOD-shaped twin of 28-overdraw. That example measures hidden FRAGMENTS
// under an expensive material; this one measures hidden INSTANCES: what a
// mechanism that rejects placements before they are scattered into a bucket
// could remove, which is vertex work, shading and the indirect instances behind
// it.
//
// It was built for the hierarchical-Z occlusion test of issue #154, which was
// measured on exactly these arms and REMOVED by its own rule -- it saved 0.343 ms
// of 1.018 at 1280x720 and 1.361 of 5.198 at 4K on the ridge arm, and cost 0.055
// and 0.188 ms on the control, where there was nothing to find. The scene stays
// because the next candidate needs it, the way 28-overdraw stayed when the
// front-to-back ordering it rejected did. docs/agents/lod-instancing.md has the
// numbers, the verdict and what it takes to build the mechanism again.
//
//	go run ./29-ridge                 # the ridge, far flank hidden
//	go run ./29-ridge -arm open       # the control: the same trees from overhead, nothing hidden
//	task bench -- -scene ridge        # both arms, interleaved, three trials
//
// The control is the half that keeps the other half honest, and it is the half
// that rejected all three mechanisms this engine has measured: a mechanism that
// makes the ridge faster and the open field slower is charging every scene for a
// win only some scenes get. See docs/agents/game-loop.md for the other two.
//
// Both arms are the same 6,000 placements on the same ridge; they differ in where
// the camera is. The ridge arm is at eye level, where the ridge AND the forest
// itself hide most of the field. The control looks straight down from 150 m,
// where neither does -- which is 28-overdraw's recipe and, it turns out, the only
// shape that works here.
//
// The control was twice something else first, and the numbers are why. A camera
// at eye level on ground with the crest taken out still has 97 % of its
// placements hidden, because 6,000 trees two metres apart hide each other
// perfectly well without a ridge: measured, the test rejected 3,133 placements
// on that "control" and 3,133 on the ridge arm -- the same number, so it bounded
// nothing. Terrain occlusion is the smaller half of this scene, and the control
// has to remove BOTH halves. Looking down removes both.
//
// What that costs is comparability between the arms: from 150 m every placement
// is in the far LOD band, while at eye level the survivors are in the near one.
// That is acceptable because the rule compares each arm with the test off
// against the SAME arm with it on; the arms are never compared with each other.
// It is the same trade 28-overdraw makes for the same reason.
//
// The number the run prints is the evidence that each arm is what it is called:
// the share of placements the TERRAIN hides along the sight line. Read it as a
// FLOOR on what is hidden, because it counts no tree hiding another -- and the
// trees do most of the hiding here, which is the measurement that caught two
// earlier versions of the control. When the occlusion test existed, the second
// evidence was its own count: 3,521 of 6,000 placements rejected on the ridge
// arm and 0 on this control.
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

const (
	// defaultTrees is the placement count, the same in both arms. 6000 rather
	// than 25-lod-forest's 3600 because half of them are on the far flank and
	// the saving worth measuring is a share of that half. -count lowers it for
	// the smoke and validation matrices, where startup time is the cost and the
	// placement count is not what is being checked there.
	defaultTrees = 6000

	// crestZ is where terrainfield.Ridge puts its crest, and nearZ/farZ are the
	// bands the placements are spread over on each side of it. The near band
	// starts past the camera so no tree is in front of the eye.
	crestZ = 0.0
	nearZ  = 62.0
	farZ   = 78.0

	// eyeHeight is how far above the ground the camera sits: a person's eye, so
	// the crest is between it and the far flank. Raise it and the far flank
	// comes back into view, which is what the measured hidden share reports.
	eyeHeight = 2.0

	// eyeZ is the camera's distance from the crest on the near side. Far enough
	// back that the near flank's own trees are in front of it rather than
	// around it, close enough that the crest covers the far flank.
	eyeZ = -46.0

	// crestBand is how far either side of the crest the straddling group sits.
	// 16 m keeps them on the slopes and the top, where the ground surface passes
	// through their bounds.
	crestBand = 16.0

	// overheadY is the control's height. 150 m looks down on the whole field --
	// half of 140 m of depth over tan(fov/2) is 150 -- while keeping every
	// placement inside the last LOD band at 260, so nothing is distance-culled
	// and the frame is the same 6,000 trees. Higher empties the frame; lower
	// leaves the field off the edges of it.
	overheadY = 150.0

	near = 0.1
	fov  = 50
)

type game struct {
	arm        string
	trees      int
	pan        bool
	counts     bool
	set        *renderer.InstanceSetLOD
	placements []renderer.MeshInstance
	radius     float32
	heightmap  *glyph.Heightmap
	time       float32
}

func hash(i uint32) float32 {
	i ^= i >> 16
	i *= 0x7feb352d
	i ^= i >> 15
	i *= 0x846ca68b
	i ^= i >> 16
	return float32(i&0xffffff) / float32(0x1000000)
}

// tree is the same cone-stack as 25-lod-forest's, at three tessellations. Shared
// shape on purpose: the thing being measured is which placements are submitted,
// and a second silhouette would make the two examples' numbers incomparable.
func tree(r *renderer.Renderer, segments, tiers int) (*renderer.Mesh, error) {
	var verts []renderer.Vertex
	tri := func(a, b, c mgl32.Vec3, color [3]float32) {
		n := b.Sub(a).Cross(c.Sub(a)).Normalize()
		for _, p := range []mgl32.Vec3{a, b, c} {
			verts = append(verts, renderer.Vertex{Pos: [3]float32(p), Normal: [3]float32(n), Color: color})
		}
	}
	cone := func(y, h, radius float32, color [3]float32) {
		for j := 0; j < segments; j++ {
			a, b := float64(j)*2*math.Pi/float64(segments), float64(j+1)*2*math.Pi/float64(segments)
			p := mgl32.Vec3{radius * float32(math.Cos(a)), y, radius * float32(math.Sin(a))}
			q := mgl32.Vec3{radius * float32(math.Cos(b)), y, radius * float32(math.Sin(b))}
			tri(p, mgl32.Vec3{0, y + h, 0}, q, color)
			tri(q, mgl32.Vec3{0, y, 0}, p, color)
		}
	}
	cone(0, 5.8, 0.3, [3]float32{0.27, 0.12, 0.045})
	for i := 0; i < tiers; i++ {
		t := float32(i) / float32(tiers)
		cone(1.2+3.9*t, 2.3-0.8*t, 1.7*(1-0.7*t), [3]float32{0.10 + 0.025*t, 0.34 + 0.07*t, 0.075})
	}
	m, err := r.CreateMesh(verts)
	if err == nil {
		m.BoundCenter = [3]float32{0, 3, 0}
		// The tallest tier reaches y=6.256, and both the frustum and the
		// occlusion test measure about the base translation, so the radius has
		// to reach the top rather than fit a sphere about y=3.
		m.BoundRadius = 6.5
	}
	return m, err
}

// build is the scene's geometry -- the ground and the placements on it -- and no
// GPU resource at all. Split out of Init so that which camera hides what can be
// checked without a device; see main_test.go.
func (g *game) build() error {
	// One ridge for both arms: from 150 m up it hides nothing, so the control
	// needs no second terrain and the two arms draw the same ground.
	hm, err := terrainfield.Ridge(1)
	if err != nil {
		return err
	}
	g.heightmap = hm
	g.radius = 6.5
	// Three groups: half on the far flank, which is what the ridge hides; a third
	// on the near flank, which is what hides itself; and a sixth ON the crest.
	//
	// The crest group is not scenery. It is the only population in this scene
	// whose bounding box STRADDLES the ridge surface -- in front of it at the
	// near face and behind it at the far one -- and that is the only population
	// where an occlusion test getting its comparison wrong is VISIBLE. Measured
	// while #154's test existed: with the forest in two flanks only, a build that
	// compared each instance's farthest point instead of its nearest rejected 11
	// more placements of 6,000 and changed no pixel, because everything it got
	// wrong was behind the front rows anyway. With this group, at 1,200
	// placements, the same mistake changed 38,489 pixels of 921,600. A gate for
	// the next candidate needs these trees; trees do grow on ridges.
	place := func(x, z float32, i int) {
		y, _ := hm.HeightAt(x, z)
		yaw := hash(uint32(i+9000)) * 2 * math.Pi
		scale := 0.85 + hash(uint32(i+7000))*0.3
		m := mgl32.Translate3D(x, y, z).Mul4(mgl32.HomogRotate3DY(yaw)).Mul4(mgl32.Scale3D(scale, scale, scale))
		g.placements = append(g.placements, renderer.MeshInstance{
			Model: [16]float32(m), Tint: [4]float32{0.85 + hash(uint32(i+133))*0.15, 1, 0.9, 0}})
	}
	for i := 0; i < g.trees; i++ {
		x := -88 + 176*hash(uint32(i*2))
		depth := hash(uint32(i*2 + 1))
		switch {
		case i%6 == 0:
			place(x, float32(crestZ)+(depth-0.5)*2*crestBand, i) // on the crest
		case i%2 == 1:
			place(x, float32(crestZ)+18+(farZ-18)*depth, i) // the far flank
		default:
			place(x, float32(crestZ)-18-(nearZ-18)*depth, i) // the near flank
		}
	}
	return nil
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()
	if err := g.build(); err != nil {
		return err
	}
	hm := g.heightmap
	e.SetTerrain(hm)
	ground, err := e.CreateTerrainMesh(hm, &glyph.TerrainOptions{
		Tint: func(_ float32, _ [3]float32) [3]float32 { return [3]float32{0.28, 0.34, 0.15} }})
	if err != nil {
		return err
	}
	ent := e.Spawn()
	e.C.Transform.Set(ent, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: ground, Roughness: 1})
	// The ground casts no shadow, so the shadow pass stays out of the columns
	// this example is read for. It still RECEIVES the sun's shadow.
	e.C.NoCastShadow.Set(ent, &glyph.NoCastShadow{})

	desc := renderer.InstanceSetLODDesc{GPU: true, Capacity: g.trees, FadeWidth: 6, ShadowLevel: 1}
	for i, detail := range [][2]int{{24, 9}, {8, 4}, {4, 2}} {
		mesh, err := tree(r, detail[0], detail[1])
		if err != nil {
			return err
		}
		desc.Levels = append(desc.Levels, renderer.LODLevel{Mesh: mesh, MaxDistance: []float32{40, 90, 260}[i]})
		log.Printf("level %d: %d triangles", i, mesh.VertexCount/3)
	}
	g.set, err = r.CreateInstanceSetLOD(desc, g.placements)
	if err != nil {
		return err
	}
	forest := e.Spawn()
	e.C.InstancedMesh.Set(forest, &glyph.InstancedMesh{LOD: g.set})
	e.C.MeshRef.Set(forest, &glyph.MeshRef{Roughness: 1})
	e.C.DoubleSided.Set(forest, &glyph.DoubleSided{})

	// Clouds off and a fixed low sun, for 28-overdraw's reasons: the cloud march
	// costs about as much as the pass being measured, and a moving sun makes the
	// cascades and the fog different work on every frame.
	env := xsky.DefaultEnvironment()
	env.Sky.CloudSteps = xsky.CloudsOff
	env.Cycle.TimeOfDay = 0.34
	env.Cycle.Speed = 0
	env.Fog.Density = 0
	e.Scene.Env = env
	e.SetCamera(g.view(0))
	log.Printf("29-ridge: arm=%s %d placements, half of them on the far flank", g.arm, len(g.placements))
	return nil
}

// view is this arm's camera: an eye 2 m over the near flank looking along the
// field at the crest, or the control's view straight down from 150 m.
//
// Three cameras were tried for the control before this one, and each was caught
// by a number rather than by looking at it. Along the ridge from one end: the
// ridge is then in front of the camera and hides half the field, terrain share
// 0.499. Lifted 34 m: a 28 m crest 46 m ahead still hides a 7 m tree 50 m
// beyond it, 0.498. At eye level on ground with the crest removed: terrain share
// 0.000 and the test still rejected 3,133 placements, because the forest hides
// itself. Straight down is the first one where neither the ridge nor the forest
// hides anything, and it is the shape 28-overdraw's control already had.
//
// Looking straight down means up cannot be the Y axis; -Z keeps the field's
// depth running up the frame, as 28-overdraw does.
func (g *game) view(t float32) (eye, center, up mgl32.Vec3) {
	pan := mgl32.Vec3{float32(math.Sin(float64(t)*0.35)) * 26, 0, 0}
	if g.arm == "open" {
		eye = mgl32.Vec3{0, overheadY, float32(crestZ) + 8}.Add(pan.Mul(0.25))
		return eye, mgl32.Vec3{eye[0], 0, eye[2]}, mgl32.Vec3{0, 0, -1}
	}
	eye = mgl32.Vec3{0, 0, eyeZ}.Add(pan)
	eye[1] = g.groundAt(eye) + eyeHeight
	center = mgl32.Vec3{eye[0], eye[1] - 0.5, eye[2] + 40}
	return eye, center, mgl32.Vec3{0, 1, 0}
}

func (g *game) groundAt(p mgl32.Vec3) float32 {
	y, _ := g.heightmap.HeightAt(p[0], p[2])
	return y
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	if g.pan {
		g.time += dt
	}
	e.SetCamera(g.view(g.time))
}

// hiddenShare is the share of placements whose bounding sphere is entirely
// behind the terrain from this camera, measured along the sight line rather than
// claimed.
//
// Measured for the same reason 28-overdraw measures its depth complexity: the
// arm names are the claim this example makes, and an arm that quietly stopped
// being the arm it is named after would not fail -- it would silently approve or
// reject whatever was being measured on it. It walks the heightmap along the ray
// to each placement and asks whether the ground ever rises above the ray; a
// placement whose top is still below the ground at some step is hidden by the
// terrain. It is terrain-only, so it is a FLOOR on what the GPU test can find:
// trees also hide each other, and this counts none of that.
func (g *game) hiddenShare(eye mgl32.Vec3) float64 {
	hidden := 0
	for i := range g.placements {
		m := &g.placements[i].Model
		top := mgl32.Vec3{m[12], m[13] + g.radius, m[14]}
		d := top.Sub(eye)
		steps := int(d.Len() / 2)
		blocked := false
		for s := 1; s < steps && !blocked; s++ {
			p := eye.Add(d.Mul(float32(s) / float32(steps)))
			h, ok := g.heightmap.HeightAt(p[0], p[2])
			blocked = ok && h > p[1]
		}
		if blocked {
			hidden++
		}
	}
	return float64(hidden) / float64(len(g.placements))
}

func main() {
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	arm := flag.String("arm", "ridge", "ridge: the far flank is hidden behind the crest; open: the same trees, nothing hidden")
	count := flag.Int("count", defaultTrees, "placements, half on each flank")
	pan := flag.Bool("pan", false, "sweep the camera along the ridge, so the previous frame's depth is never this frame's")
	frames := flag.Int("frames", 200, "frames to render")
	counts := flag.Bool("counts", false, "print bucket counts and the occluded count each second")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	msaa := flag.Int("msaa", 4, "samples per pixel, 1 or 4")
	sky := flag.Bool("sky", true, "draw the sky dome. False leaves the frame's flat clear colour behind the field, which is what makes the visible-pixel count a count of geometry rather than of geometry plus horizon")
	flag.Parse()
	if *arm != "ridge" && *arm != "open" {
		log.Fatalf("29-ridge: -arm %q is not ridge or open", *arm)
	}

	if *count < 2 {
		log.Fatal("29-ridge: -count must be at least 2, one placement per flank")
	}
	g := &game{arm: *arm, trees: *count, pan: *pan, counts: *counts}
	opts := []glyph.Option{
		glyph.WithTitle(fmt.Sprintf("GlyphEngine - 29 Ridge (%s)", *arm)),
		glyph.WithWindowSize(*width, *height),
		glyph.WithMSAA(*msaa),
		glyph.WithProjection(fov, near, 600),
		glyph.WithMaxFrames(*frames),
	}
	if *sky {
		// The dome is a shader slot the engine leaves empty, so omitting this
		// option draws no sky at all while the environment below -- the sun, the
		// palette, the fog -- stays exactly as it was.
		opts = append(opts, glyph.WithShaders(xsky.Shaders()))
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
	drawn, culled := g.set.Counts()
	eye, _, _ := g.view(g.time)
	share := g.hiddenShare(eye)
	log.Printf("RIDGE\thidden_share\t%.3f\tn_culled\t%d\tn_placements\t%d",
		share, culled, len(g.placements))
	// The same four numbers in one human line, which is what cmd/hizcheck reads:
	// the TSV line above is for the bench's column parser.
	fmt.Printf("RIDGE counts=%v culled=%d hidden_share=%.3f placements=%d\n",
		drawn, culled, share, len(g.placements))

	// A pixel floor, for the reason 28-overdraw has one: a frame that renders
	// nothing is fast whatever is done to it, and a camera or a far plane that
	// drifted would time an empty picture. The corner pixel is the background
	// reference at both cameras.
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
		log.Fatalf("29-ridge: %d pixels differ >=20/255 from background, need %d -- this frame is too empty to time", visible, floor)
	}

	// The fragment-invocation counters, when a run asked for them
	// (GLYPHENGINE_PIPELINE_STATS=1). Read with -sky=false, where every visible
	// pixel is geometry.
	//
	// Both passes, summed, because this scene's geometry is split across them: the
	// trees are an instanced LOD set in the opaque pass and the ground is the
	// terrain pipeline in its own, and a ratio over one pass's invocations against
	// a pixel count that includes the other's coverage is not a ratio of anything.
	//
	// What this scene CANNOT give is a quad factor. The depth prepass declines
	// every draw in it -- an instance set with LOD buckets and a double-sided draw
	// are both excluded by depthPrepassQualifies, and the terrain has its own
	// pipeline -- so nothing here removes the hidden fragments, and the ratio is
	// depth complexity times the quad factor rather than the quad factor alone.
	// It is an upper bound, and it is in the record as one.
	if st, err := e.MeanPipelineStats(); err == nil && st.Valid {
		inv := st.FragmentInvocations[renderer.PassOpaque] + st.FragmentInvocations[renderer.PassTerrain]
		clipped := st.ClippingPrimitives[renderer.PassOpaque] + st.ClippingPrimitives[renderer.PassTerrain]
		gpu := e.MeanGPUTimings()
		log.Printf("QUADS	scene	29-ridge	arm	%s	prepass	none	msaa	%d	sky	%v	width	%d	height	%d	covered_px	%d	invocations	%d	inv_per_covered	%.4f	clipped	%d	tri_per_covered	%.4f	gpu_opaque	%.4f	gpu_terrain	%.4f	gpu_total	%.4f	hidden_share	%.3f	frames	%d",
			*arm, e.Capabilities().MSAASamples, *sky, img.Rect.Dx(), img.Rect.Dy(), visible,
			inv, float64(inv)/float64(visible), clipped, float64(clipped)/float64(visible),
			gpu.Pass[renderer.PassOpaque], gpu.Pass[renderer.PassTerrain], gpu.Total, share, st.Frames)
	}

	// The arm labels, checked, in the two ways this scene can be wrong.
	//
	// First the terrain: 0.500 on the ridge arm, which is its whole far flank,
	// and 0.000 from overhead. That share is computed from the placements and the
	// heightmap, so it is the same number on every machine and a floor that trips
	// means the camera or the ground moved, not that the renderer did.
	//
	// The share above cannot see a tree hiding another tree, and in this scene the
	// trees do most of the hiding -- so a future mechanism's own count of what it
	// rejected is the other half of the evidence, and the reason the bench table
	// in docs/agents/lod-instancing.md carries one. When #154's test existed it
	// rejected 3,521 of 6,000 here and 0 on the control.
	switch {
	case g.arm == "ridge" && share < 0.25:
		log.Fatalf("29-ridge: the ridge arm hides %.3f of its placements, need 0.25 -- nothing is behind the crest", share)
	case g.arm == "open" && share > 0.02:
		log.Fatalf("29-ridge: the control's terrain hides %.3f of its placements, need at most 0.02", share)
	}
	log.Printf("29-ridge: hidden share %.3f, %d visible pixels (floor %d), %d frames",
		share, visible, floor, e.FrameCount())
}

func absDiff(a, b byte) int {
	if a > b {
		return int(a) - int(b)
	}
	return int(b) - int(a)
}
