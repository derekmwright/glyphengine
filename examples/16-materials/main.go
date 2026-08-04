// Command 16-materials shows what a material map does that an albedo texture
// cannot.
//
// Five panels, left to right, adding one map at a time to the same geometry and
// the same albedo:
//
//  1. albedo only        — flat, lit as one uniform material
//  2. + normal           — relief appears; the geometry is still four triangles
//  3. + metallic-roughness — the mortar goes matte while the tiles stay slick
//  4. + occlusion        — ambient light stops reaching into the grooves
//  5. + emissive         — the mortar glows, and keeps glowing after sunset
//
// Panel 1 is what the engine could do before: colour varies per pixel while the
// surface still lights as a single perfectly smooth material, which is why the
// tiles read as a printed picture of tiles. Panel 2 is the whole point — the
// normal map perturbs the shading normal per pixel, so the light that grazes
// across it during the day cycle catches the edges.
//
// Panel 5 is emission, which is not lighting at all: it is radiance the surface
// adds regardless of what reaches it. Its strength is 6, well above 1, which
// only means anything because the scene renders into a half-float target — on
// the old 8-bit path it would have clipped to white at the moment it was
// written. Watch past sunset: panels 1 to 4 go dark and panel 5 does not.
//
// Every map is generated at startup from one height field, so this example
// carries no asset files at all and the five maps cannot disagree with each
// other. Watch a while: the sun moves, and the relief moves with it.
//
//	go run ./16-materials
//	go run ./16-materials -frames 60
//	go run ./16-materials -flat        # force every panel back to albedo only
package main

import (
	"flag"
	"log"
	"math"
	"runtime"

	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/input"
	"github.com/derekmwright/glyphengine/renderer"
)

func init() {
	// GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

// autoOrbitRate circles the camera slowly, in radians per second.
//
// No mouse look: this example is about what the material maps do, and camera
// plumbing in front of that is just noise to read past. Orbiting on its own
// also makes -frames screenshots repeatable.
const autoOrbitRate = 0.15

type game struct {
	camera *glyph.Camera
	flat   bool
	tod    float32
	speed  float32
	curve  float32
	dist   float32
}

// mapSize is the edge length of every generated map, and tileCount how many
// tiles fit across it. The panels' UVs run 0..1 over one face, so tileCount is
// also how many tiles appear on a panel.
const (
	mapSize   = 512
	tileCount = 4.0
)

func smoothstep(edge0, edge1, x float64) float64 {
	t := (x - edge0) / (edge1 - edge0)
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return t * t * (3 - 2*t)
}

// hash01 is a cheap deterministic value in [0,1) from two integers. Only used
// for per-tile variation, so it needs to look arbitrary rather than be random.
func hash01(x, y int) float64 {
	h := uint32(x)*374761393 + uint32(y)*668265263
	h = (h ^ (h >> 13)) * 1274126177
	return float64(h^(h>>16)) / float64(1<<32)
}

// height is the field every map is derived from: a grid of bevelled tiles
// separated by grooves, with a little grain on the tile faces.
//
// Deriving four maps from one function rather than authoring four images is what
// keeps them consistent. A normal map that disagrees with its occlusion map is a
// genuinely confusing thing to debug, because each looks correct alone.
func height(u, v float64) float64 {
	fu := u*tileCount - math.Floor(u*tileCount)
	fv := v*tileCount - math.Floor(v*tileCount)

	// Distance to the nearest tile edge, 0 in the groove, 0.5 mid-tile.
	du := math.Min(fu, 1-fu)
	dv := math.Min(fv, 1-fv)
	d := math.Min(du, dv)

	const groove = 0.045 // half-width of the mortar line
	const bevel = 0.130  // how far the tile edge rolls over into it
	h := smoothstep(groove, groove+bevel, d)

	// A gentle undulation across the tile faces, so the maps have something to
	// say inside a tile too.
	//
	// Kept deliberately low-frequency. An earlier version used sin(u*220), which
	// is about fourteen texels per period: fine in the texture and a shimmering
	// crosshatch on screen, because the panel is drawn narrower than the map is
	// wide and the pattern lands near Nyquist. Mips soften that; they do not
	// rescue detail that was never resolvable.
	grain := math.Sin(u*26.0)*math.Cos(v*22.0)*0.5 + 0.5
	return h * (0.90 + 0.10*grain)
}

// buildAlbedo is the base colour: a warm stone whose tiles vary slightly, going
// darker into the grooves.
func buildAlbedo() []byte {
	pix := make([]byte, mapSize*mapSize*4)
	for y := 0; y < mapSize; y++ {
		for x := 0; x < mapSize; x++ {
			u := (float64(x) + 0.5) / mapSize
			v := (float64(y) + 0.5) / mapSize
			h := height(u, v)

			tx := int(u * tileCount)
			ty := int(v * tileCount)
			shade := 0.88 + 0.12*hash01(tx, ty)

			// The grooves are mortar: darker and greyer than the tile face.
			r := (0.62*shade)*h + 0.20*(1-h)
			g := (0.55*shade)*h + 0.19*(1-h)
			b := (0.47*shade)*h + 0.18*(1-h)

			i := (y*mapSize + x) * 4
			pix[i+0] = byte(r * 255)
			pix[i+1] = byte(g * 255)
			pix[i+2] = byte(b * 255)
			pix[i+3] = 255
		}
	}
	return pix
}

// buildNormal encodes the height field's surface normal in tangent space.
//
// Central differences on the height field, scaled by strength: a taller relief
// tilts the normal further from straight up. The result is stored in the usual
// (n+1)/2 encoding, which is why it has to be uploaded as a data texture —
// decoded as sRGB colour, 128 would arrive as 0.216 rather than 0.502 and every
// normal would lean the same wrong way.
func buildNormal(strength float64) []byte {
	pix := make([]byte, mapSize*mapSize*4)
	const texel = 1.0 / mapSize
	for y := 0; y < mapSize; y++ {
		for x := 0; x < mapSize; x++ {
			u := (float64(x) + 0.5) / mapSize
			v := (float64(y) + 0.5) / mapSize

			dhdu := (height(u+texel, v) - height(u-texel, v)) * 0.5
			dhdv := (height(u, v+texel) - height(u, v-texel)) * 0.5

			// The gradient points uphill, so the normal leans against it.
			nx := -dhdu * strength
			ny := -dhdv * strength
			nz := 1.0
			l := math.Sqrt(nx*nx + ny*ny + nz*nz)
			nx, ny, nz = nx/l, ny/l, nz/l

			i := (y*mapSize + x) * 4
			pix[i+0] = byte((nx*0.5 + 0.5) * 255)
			pix[i+1] = byte((ny*0.5 + 0.5) * 255)
			pix[i+2] = byte((nz*0.5 + 0.5) * 255)
			pix[i+3] = 255
		}
	}
	return pix
}

// buildMetallicRoughness follows glTF's packing: roughness in G, metallic in B.
// The tile faces are glazed and the mortar between them is not, which is a
// difference no albedo map can express.
func buildMetallicRoughness() []byte {
	pix := make([]byte, mapSize*mapSize*4)
	for y := 0; y < mapSize; y++ {
		for x := 0; x < mapSize; x++ {
			u := (float64(x) + 0.5) / mapSize
			v := (float64(y) + 0.5) / mapSize
			h := height(u, v)

			// h == 1 on a tile face, 0 in a groove. The spread is wide on
			// purpose: a glazed tile and dry mortar are genuinely far apart, and
			// a narrow range is indistinguishable from no map at all.
			roughness := 0.97 - 0.80*h

			i := (y*mapSize + x) * 4
			pix[i+0] = 255 // unused by the shader
			pix[i+1] = byte(roughness * 255)
			pix[i+2] = 0 // metallic: none of this is metal
			pix[i+3] = 255
		}
	}
	return pix
}

// buildOcclusion approximates how much of the sky each point can see, in R.
//
// A box blur of the height field rather than a real occlusion bake: what makes
// the effect read is that the darkening is *wider* than the groove itself, since
// the tile edge beside a groove is shadowed by it too. Reusing the height field
// directly gives a hard-edged line that reads as a painted stripe.
func buildOcclusion() []byte {
	pix := make([]byte, mapSize*mapSize*4)
	const radius = 6
	const texel = 1.0 / mapSize
	for y := 0; y < mapSize; y++ {
		for x := 0; x < mapSize; x++ {
			u := (float64(x) + 0.5) / mapSize
			v := (float64(y) + 0.5) / mapSize

			sum, n := 0.0, 0.0
			for dy := -radius; dy <= radius; dy++ {
				for dx := -radius; dx <= radius; dx++ {
					sum += height(u+float64(dx)*texel, v+float64(dy)*texel)
					n++
				}
			}
			ao := 0.18 + 0.82*(sum/n)

			i := (y*mapSize + x) * 4
			pix[i+0] = byte(ao * 255)
			pix[i+1] = byte(ao * 255)
			pix[i+2] = byte(ao * 255)
			pix[i+3] = 255
		}
	}
	return pix
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()

	// Albedo is colour, so it is sRGB. The other three are numbers, so they are
	// not — see CreateDataTexture.
	albedo, err := r.CreateTexture(buildAlbedo(), mapSize, mapSize)
	if err != nil {
		return err
	}
	normal, err := r.CreateDataTexture(buildNormal(6.0), mapSize, mapSize)
	if err != nil {
		return err
	}
	metalRough, err := r.CreateDataTexture(buildMetallicRoughness(), mapSize, mapSize)
	if err != nil {
		return err
	}
	occlusion, err := r.CreateDataTexture(buildOcclusion(), mapSize, mapSize)
	if err != nil {
		return err
	}
	// Emission is a colour, so this one is sRGB like the albedo.
	emissive, err := r.CreateTexture(buildEmissive(), mapSize, mapSize)
	if err != nil {
		return err
	}

	// One material per panel, each adding a map to the one before it. Materials
	// share the textures; they hold views and samplers rather than owning them.
	variants := []renderer.MaterialOptions{
		{Albedo: albedo},
		{Albedo: albedo, Normal: normal},
		{Albedo: albedo, Normal: normal, MetallicRoughness: metalRough},
		{Albedo: albedo, Normal: normal, MetallicRoughness: metalRough, Occlusion: occlusion},
		{
			Albedo: albedo, Normal: normal, MetallicRoughness: metalRough, Occlusion: occlusion,
			Emissive:       emissive,
			EmissiveFactor: [3]float32{0.35, 0.75, 1.0},
			// Above 1 on purpose. At strength 1 this is a pale blue tint on the
			// mortar; at 6 it is a light source that survives the tonemap as a
			// highlight rather than as a slightly brighter surface.
			EmissiveStrength: 6,
		},
	}
	if g.flat {
		for i := range variants {
			variants[i] = renderer.MaterialOptions{Albedo: albedo}
		}
	}

	ground, err := r.CreatePlane(60, 60)
	if err != nil {
		return err
	}
	groundEnt := e.Spawn()
	e.C.Transform.Set(groundEnt, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(groundEnt, &glyph.MeshRef{Mesh: ground, Roughness: 0.95})
	e.C.Color.Set(groundEnt, &glyph.Color{R: 0.26, G: 0.29, B: 0.25})
	e.C.Static.Set(groundEnt, &glyph.Static{})

	panel, err := r.CreateCube(1.0)
	if err != nil {
		return err
	}

	const spacing = 3.6
	for i, opts := range variants {
		mat, err := r.CreateMaterial(opts)
		if err != nil {
			return err
		}

		ent := e.Spawn()
		x := (float32(i) - float32(len(variants)-1)/2) * spacing
		e.C.Transform.Set(ent, &glyph.Transform{
			Position: mgl32.Vec3{x, 1.9, 0},
			// Leaned back so the sun's specular reflection heads toward the
			// camera. Face-on to the viewer with the sun overhead there is no
			// highlight to vary, and a roughness map with nothing to roughen
			// looks exactly like no roughness map.
			Rotation: mgl32.Vec3{-0.34, 0, 0},
			Scale:    mgl32.Vec3{3.2, 3.2, 0.3},
		})
		// Roughness here is the factor the metallic-roughness map multiplies, so
		// panels 1 and 2 — which have no such map — use it directly.
		e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: panel, Roughness: 0.55})
		e.C.MaterialRef.Set(ent, &glyph.MaterialRef{PBR: mat})
		e.C.Static.Set(ent, &glyph.Static{})
	}
	e.RebuildStatics()

	// A low sun, moving. Grazing light is what makes a normal map legible: with
	// the sun overhead the relief flattens out, because the shading difference
	// between a tile face and its bevel is largest when the light is nearly
	// parallel to the surface.
	e.SetDayCycleSpeed(g.speed / 150.0)
	e.SetTimeOfDay(0.30)
	if g.tod >= 0 {
		// Freeze the clock, so a given time of day can be inspected and
		// screenshots of it are reproducible. Same flag as 09-water.
		e.SetTimeOfDay(g.tod)
		e.SetDayCycleSpeed(0)
	}

	// ACES filmic. Measured against the alternatives on this scene, at a fixed
	// sun, on the two things that matter here -- how much surface shading
	// survives on the panels, and how much of the sunset sky ends up pinned at
	// the top of the 8-bit range:
	//
	//	curve      panel contrast   sky at ceiling
	//	identity   0.01234          63.9%
	//	reinhard   0.01095           0.0%
	//	ACES       0.01725           2.5%
	//
	// Identity blows the sky out, which is what makes a sunset read wrong: the
	// sun clips, the sky clips with it, and there is nothing left to tell them
	// apart. Reinhard fixes that by compressing everything equally, and pays for
	// it with the flattest panels of the three. ACES beats identity on both
	// counts at once, because it lifts midtones -- aces(0.5) is 0.62 -- while
	// rolling the highlights off.
	//
	// -curve 0 and 1 are still there to see the difference.
	r.SetTonemap(0, g.curve, 6)

	// Bloom is what makes the emissive panel read as a light rather than as a
	// bright surface: a real light spills onto what is around it, and a
	// threshold above 1 selects only panel 5's glow, so the other four are
	// untouched. It is also why the identity curve is enough here -- the halo
	// carries the colour that a clipped white core loses.
	// Threshold 1.2 with a 0.2 knee, so the ramp starts at exactly 1.0 and
	// nothing the old 8-bit path could represent contributes at all. Measured:
	// with a 0.5 knee the ramp reached down to 0.5, the daytime sky sits around
	// 0.68 in linear, and the whole frame lifted by RMS 0.025 -- milky rather
	// than glowing. The sky is the thing to check any bloom threshold against.
	r.SetBloom(0.7, 1.2, 0.2, 1.0)

	// Far enough back for five panels without pushing them so far away the maps
	// stop being legible. 16 fitted them all at any orbit angle but cost another
	// 14% of local contrast on top of what the curve was already taking; 14 is
	// the compromise, and the orbit brings the fifth panel fully into view.
	g.camera = glyph.NewCamera(g.dist)
	g.camera.Target = mgl32.Vec3{0, 1.9, 0}
	g.camera.Pitch = 0.12

	log.Println("16-materials running. The camera orbits on its own. Escape quits.")
	return nil
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	g.camera.Yaw += autoOrbitRate * dt
	e.SetCamera(g.camera.ViewVectors())
}

func main() {
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	fullscreen := flag.Bool("fullscreen", false, "run fullscreen on the primary monitor")
	frames := flag.Int("frames", 0, "render N frames then exit (0 = run until closed)")
	flat := flag.Bool("flat", false, "give every panel albedo only, for comparison")
	tod := flag.Float64("time", -1, "freeze time of day in [0,1): 0=midnight, 0.25=sunrise, 0.5=noon, 0.75=sunset")
	speed := flag.Float64("speed", 1, "day cycle rate multiplier; 1 is a full cycle in 150 seconds")
	curve := flag.Float64("curve", 2, "tonemap curve: 0 identity, 1 extended Reinhard, 2 ACES filmic")
	dist := flag.Float64("dist", 14, "camera orbit radius; smaller shows the maps better")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 16 Materials"),
		glyph.WithWindowSize(*width, *height),
		glyph.WithMSAA(4),
		glyph.WithQuitKey(input.KeyEscape),
	}
	if *fullscreen {
		opts = append(opts, glyph.WithFullscreen())
	}
	if *frames > 0 {
		opts = append(opts, glyph.WithMaxFrames(*frames))
	}
	if *shot != "" {
		opts = append(opts, glyph.WithScreenshot(*shot))
	}

	e, err := glyph.New(&game{flat: *flat, tod: float32(*tod), speed: float32(*speed), curve: float32(*curve), dist: float32(*dist)}, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
}

// buildEmissive lights the mortar and leaves the tile faces dark.
//
// It reuses the same height field as every other map, so the glow lands exactly
// in the grooves the normal map carves and the occlusion map darkens — which is
// the point of the panel: the same pixels are simultaneously in shadow from
// ambient light and emitting their own.
//
// Full white in the grooves rather than the final colour, because the map
// multiplies EmissiveFactor. Putting the colour here as well would square it.
func buildEmissive() []byte {
	pix := make([]byte, mapSize*mapSize*4)
	for y := 0; y < mapSize; y++ {
		for x := 0; x < mapSize; x++ {
			u := (float64(x) + 0.5) / mapSize
			v := (float64(y) + 0.5) / mapSize

			// height() is 1 on a tile face and 0 in the mortar, so invert it and
			// square to keep the glow inside the groove rather than bleeding up
			// the bevel.
			g := 1 - height(u, v)
			g *= g

			c := byte(g * 255)
			i := (y*mapSize + x) * 4
			pix[i+0] = c
			pix[i+1] = c
			pix[i+2] = c
			pix[i+3] = 255
		}
	}
	return pix
}
