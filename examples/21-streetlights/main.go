// Command 21-streetlights is a small settlement at night: warm doorway
// spotlights over heightmap terrain and PBR-mapped walls, plus a ring of
// street lamps along the path between them.
//
// It exists to demonstrate what clustered spot lights are for outside a test
// pattern. Each building mounts one downward, slightly outward-aimed spot
// over its door -- soft-edged, so the pool of light on the ground fades
// rather than cuts off, and the same cone lights the wall above the door on
// its way down. The buildings' walls carry a Material (albedo, normal and
// roughness maps, generated at startup the way 16-materials does it), so the
// spotlight's grazing light catches real relief rather than a flat texture.
// The street lamps are unshadowed point lights on posts, doing what
// SetPointLights is for: a few dozen small lights scattered along a path
// rather than one light trying to cover the whole scene.
//
// The scene is set just after midnight, with the moon low and the sky kept
// deliberately understated -- see day-night.md -- so the warm door light
// reads against something cool rather than competing with a bright sky.
//
//	go run ./21-streetlights                    # windowed, drag-orbit camera
//	go run ./21-streetlights -frames 200         # render 200 frames, then exit
//	go run ./21-streetlights -lamps 120          # more street lamps
//	go run ./21-streetlights -sweep              # rotate building 0's spotlight
//	go run ./21-streetlights -off                # start with spotlights cleared
//	go run ./21-streetlights -lightdebug heatmap # see the froxel grid instead
//	go run ./21-streetlights -lightstats         # log the binner's stats on exit
//
// Left-drag orbits, scroll zooms, L toggles the spotlights, Escape quits.
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

const (
	gridSize    = 97    // heightmap resolution per side
	worldSize   = 120.0 // world units per side
	heightScale = 5.0   // peak height in world units

	// The plaza the buildings ring is flattened rather than left to the noise:
	// a building on a slope is a separate problem this example is not about.
	// Amplitude ramps back up to full strength between flattenRadius and
	// flattenRadius+flattenWidth, so the settlement sits in a bowl of gently
	// rolling ground rather than a perfectly flat disc with a hard edge.
	flattenRadius = 30.0
	flattenWidth  = 12.0

	buildingCount  = 6
	ringRadius     = 24.0
	buildingWidth  = 6.4
	buildingDepth  = 5.2
	buildingHeight = 4.4
	roofOverhang   = 0.5
	roofThickness  = 0.35

	doorWidth     = 1.5
	doorHeight    = 2.5
	doorThickness = 0.14

	// The doorstep patch approximates the spotlight's footprint on the
	// ground: wide enough to sit under the whole pool, and centred on where
	// the cone's axis actually lands (see buildingSpot) rather than jammed
	// against the wall.
	doorstepWidth   = 4.0
	doorstepDepth   = 3.2
	doorstepOutward = 1.3

	// The fixture sits above the door, proud of the wall by just enough that
	// its marker cube does not z-fight with the wall it is mounted on -- kept
	// small deliberately, because the fixture position doubles as the cone's
	// apex, and pushing it far from the wall aims the cone's near edge away
	// from the wall it is supposed to be lighting on the way down.
	fixtureHeight  = doorHeight + 0.7
	fixtureForward = 0.06

	// Soft cone: smooth falloff between Inner and Outer rather than a crisp
	// edge, per the acceptance criteria. Aimed mostly down with a shallow
	// outward lean, so the near edge of the cone grazes the wall and the door
	// lintel right below the fixture on its way to the ground, instead of
	// leaning away from the wall from the apex out.
	spotRange = 10.0
	spotInner = 18 * math.Pi / 180
	spotOuter = 38 * math.Pi / 180

	// sweepRate is how fast -sweep rotates building 0's spotlight around the
	// vertical axis, in radians per second. Slow enough that two captures a
	// couple of seconds apart show a footprint that has clearly moved without
	// the cone leaving the building behind it.
	sweepRate = 0.3

	lampPathRadius = 34.0
	lampPoleHeight = 3.0
	lampRange      = 7.0

	wallMapSize = 256 // resolution of the generated wall material maps
)

// building is one settlement building's ground anchor: where it sits and
// which way its door faces. Everything else -- wall, door, roof, fixture,
// spotlight -- is derived from these two per frame or at spawn, so there is
// exactly one source of truth for a building's placement.
type building struct {
	pos mgl32.Vec3 // ground point (terrain height), not the wall's centre
	yaw float32    // rotation so local +Z (the door face) points at the plaza
}

type game struct {
	camera *glyph.Camera

	buildings []building
	lamps     []glyph.PointLight

	lampCount int  // -lamps
	sweep     bool // -sweep
	spotsOn   bool // toggled by -off at startup and by L at runtime

	t float32

	// Camera pose flags, in the same spirit as 11-lights': the default is a
	// good overview, and moving these is how a scripted capture gets a close
	// view of one doorway without a second example to maintain.
	camDist, camPitch, camYaw, camTargetX, camTargetY, camTargetZ, camLook float32
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()

	// ── terrain ──
	heights := generateHeights(gridSize, gridSize)
	hm, err := glyph.NewHeightmap(
		gridSize, gridSize,
		worldSize, worldSize,
		-worldSize/2, -worldSize/2,
		heights,
	)
	if err != nil {
		return err
	}
	e.SetTerrain(hm)

	terrainMesh, err := e.CreateTerrainMesh(hm, &glyph.TerrainOptions{Tint: terrainTint})
	if err != nil {
		return err
	}
	terrainEnt := e.Spawn()
	e.C.Transform.Set(terrainEnt, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(terrainEnt, &glyph.MeshRef{Mesh: terrainMesh, Roughness: 0.95})

	// ── wall material: albedo, normal and roughness generated the way
	// 16-materials builds its maps, so this example carries no asset files ──
	albedo, err := r.CreateTexture(buildWallAlbedo(), wallMapSize, wallMapSize)
	if err != nil {
		return err
	}
	normal, err := r.CreateDataTexture(buildWallNormal(5.0), wallMapSize, wallMapSize)
	if err != nil {
		return err
	}
	rough, err := r.CreateDataTexture(buildWallRoughness(), wallMapSize, wallMapSize)
	if err != nil {
		return err
	}
	wallMat, err := r.CreateMaterial(renderer.MaterialOptions{Albedo: albedo, Normal: normal, MetallicRoughness: rough})
	if err != nil {
		return err
	}

	cube, err := r.CreateCube(1.0)
	if err != nil {
		return err
	}
	pole, err := r.CreateCylinder(0.07, lampPoleHeight, 8)
	if err != nil {
		return err
	}

	// ── buildings, ringed around the plaza and facing its centre ──
	g.buildings = make([]building, buildingCount)
	for i := 0; i < buildingCount; i++ {
		angle := float64(i) / float64(buildingCount) * 2 * math.Pi
		bx := float32(math.Cos(angle)) * ringRadius
		bz := float32(math.Sin(angle)) * ringRadius
		yaw := float32(math.Atan2(float64(-bx), float64(-bz))) // local +Z toward the origin

		by, _ := hm.HeightAt(bx, bz)
		b := building{pos: mgl32.Vec3{bx, by, bz}, yaw: yaw}
		g.buildings[i] = b

		// Wall.
		wall := e.Spawn()
		e.C.Transform.Set(wall, &glyph.Transform{
			Position: b.pos.Add(mgl32.Vec3{0, buildingHeight / 2, 0}),
			Rotation: mgl32.Vec3{0, yaw, 0},
			Scale:    mgl32.Vec3{buildingWidth, buildingHeight, buildingDepth},
		})
		e.C.MeshRef.Set(wall, &glyph.MeshRef{Mesh: cube, Roughness: 0.85})
		e.C.MaterialRef.Set(wall, &glyph.MaterialRef{PBR: wallMat})
		e.C.Static.Set(wall, &glyph.Static{})

		// Door: a separate mesh rather than baking a door shape into the wall
		// texture, because CreateCube maps the same 0..1 UV onto all six
		// faces -- a door painted into the map would appear on every wall,
		// not just the front one.
		door := e.Spawn()
		e.C.Transform.Set(door, &glyph.Transform{
			Position: b.pos.Add(rotateY(yaw, mgl32.Vec3{0, doorHeight / 2, buildingDepth/2 + doorThickness/2})),
			Rotation: mgl32.Vec3{0, yaw, 0},
			Scale:    mgl32.Vec3{doorWidth, doorHeight, doorThickness},
		})
		e.C.MeshRef.Set(door, &glyph.MeshRef{Mesh: cube, Roughness: 0.65})
		e.C.Color.Set(door, &glyph.Color{R: 0.26, G: 0.16, B: 0.09})
		e.C.Static.Set(door, &glyph.Static{})

		// Doorstep: a worn dirt patch under the spotlight's pool, in place of
		// the terrain's own grass tint there.
		//
		// Measured, not assumed (AGENTS.md rule 13): atmosphere.inc's
		// atmNightShift blends every lit fragment toward a blue-weighted grey
		// at this time of night (80% at TimeOfDay 0.03), on the SHADED colour
		// -- light times albedo -- not on the light alone. It is also
		// provably one-sided: for any non-negative colour with no blue
		// component, the maths of the blend (scotopic weights 0.72/0.86/1.30
		// against luminance weights 0.2126/0.7152/0.0722) make the shifted
		// blue channel exceed the shifted green channel every time, so true
		// orange (red > green > blue) cannot survive full night in this
		// engine -- the best reachable ordering is red > blue > green, a
		// warm rose rather than amber. Grass albedo (0.20, 0.30, 0.18) is
		// green-dominant, and green carries most of the luminance the shift
		// keys off, so a warm spot straight on grass measured red 151, green
		// 159, blue 175 in the rendered PNG -- cooler than the light aimed at
		// it. This dirt-brown patch under the pool, paired with spotColor below,
		// measures red 154, green 129, blue 147: warm side wins clearly
		// against the moonlit ground nearby, which measures red 42, green
		// 47, blue 55.
		patch := e.Spawn()
		e.C.Transform.Set(patch, &glyph.Transform{
			Position: b.pos.Add(rotateY(yaw, mgl32.Vec3{0, 0.02, buildingDepth/2 + doorstepOutward})),
			Rotation: mgl32.Vec3{0, yaw, 0},
			Scale:    mgl32.Vec3{doorstepWidth, 0.04, doorstepDepth},
		})
		e.C.MeshRef.Set(patch, &glyph.MeshRef{Mesh: cube, Roughness: 0.9})
		e.C.Color.Set(patch, &glyph.Color{R: 0.42, G: 0.22, B: 0.08})
		e.C.Static.Set(patch, &glyph.Static{})
		e.C.NoCastShadow.Set(patch, &glyph.NoCastShadow{})

		// Roof slab.
		roof := e.Spawn()
		e.C.Transform.Set(roof, &glyph.Transform{
			Position: b.pos.Add(mgl32.Vec3{0, buildingHeight + roofThickness/2, 0}),
			Rotation: mgl32.Vec3{0, yaw, 0},
			Scale:    mgl32.Vec3{buildingWidth + 2*roofOverhang, roofThickness, buildingDepth + 2*roofOverhang},
		})
		e.C.MeshRef.Set(roof, &glyph.MeshRef{Mesh: cube, Roughness: 0.8})
		e.C.Color.Set(roof, &glyph.Color{R: 0.17, G: 0.15, B: 0.16})
		e.C.Static.Set(roof, &glyph.Static{})

		// Fixture marker: a small emissive cube where the spotlight's beam
		// originates, so the doorway light reads as coming from something
		// rather than from nowhere. It does not rotate with -sweep -- the
		// fixture stays mounted; only the beam it throws does.
		fixture := e.Spawn()
		e.C.Transform.Set(fixture, &glyph.Transform{
			Position: b.pos.Add(rotateY(yaw, mgl32.Vec3{0, fixtureHeight, buildingDepth/2 + fixtureForward})),
			Scale:    mgl32.Vec3{0.16, 0.16, 0.16},
		})
		e.C.MeshRef.Set(fixture, &glyph.MeshRef{Mesh: cube})
		e.C.Color.Set(fixture, &glyph.Color{R: 1.0, G: 0.86, B: 0.62})
		e.C.Emissive.Set(fixture, &glyph.Emissive{})
	}

	// ── street lamps ──
	g.lamps = buildLamps(hm, g.lampCount)
	for _, l := range g.lamps {
		poleEnt := e.Spawn()
		e.C.Transform.Set(poleEnt, &glyph.Transform{
			Position: mgl32.Vec3{l.Pos.X(), l.Pos.Y() - lampPoleHeight/2, l.Pos.Z()},
			Scale:    mgl32.Vec3{1, 1, 1},
		})
		e.C.MeshRef.Set(poleEnt, &glyph.MeshRef{Mesh: pole, Roughness: 0.5, Metallic: 0.3})
		e.C.Color.Set(poleEnt, &glyph.Color{R: 0.22, G: 0.22, B: 0.24})
		e.C.Static.Set(poleEnt, &glyph.Static{})

		bulbEnt := e.Spawn()
		e.C.Transform.Set(bulbEnt, &glyph.Transform{Position: l.Pos, Scale: mgl32.Vec3{0.22, 0.22, 0.22}})
		e.C.MeshRef.Set(bulbEnt, &glyph.MeshRef{Mesh: cube})
		e.C.Color.Set(bulbEnt, &glyph.Color{R: 1.0, G: 0.82, B: 0.55})
		e.C.Emissive.Set(bulbEnt, &glyph.Emissive{})
	}
	e.RebuildStatics()

	// ── night ──
	//
	// Just after midnight: the moon is up but low, so it lights the terrain
	// coolly and dimly rather than competing with the doorway spots for
	// attention. Clouds off -- a night sky with a modest star field reads
	// better here than a raymarched cloud layer, and it is the cheaper
	// choice besides.
	env := glyph.DefaultEnvironment()
	env.Sky.CloudSteps = glyph.CloudsOff
	env.Sky.MilkyWay = 0.5
	env.Fog = &glyph.Fog{Density: 0.006, Height: 6}
	e.Scene.Env = env
	e.SetTimeOfDay(0.03)
	e.SetDayCycleSpeed(0)

	g.camera = glyph.NewCamera(g.camDist)
	g.camera.Pitch = g.camPitch
	g.camera.Yaw = g.camYaw
	g.camera.LookOffset = g.camLook
	g.camera.Target = mgl32.Vec3{g.camTargetX, g.camTargetY, g.camTargetZ}

	log.Printf("21-streetlights running: %d buildings, %d street lamps. L toggles spotlights, Escape quits.",
		buildingCount, len(g.lamps))
	return nil
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	in := e.Input()
	if in.KeyPressed(input.KeyEscape) {
		e.Close()
	}
	if in.KeyPressed(input.KeyL) {
		g.spotsOn = !g.spotsOn
		log.Printf("spotlights: %v", g.spotsOn)
	}

	g.t += dt

	g.camera.Update(in)
	g.camera.ResolveCollision(e.Scene, 0, dt)
	e.SetCamera(g.camera.ViewVectors())

	// The lamps never move, so this is the same slice every frame -- no
	// allocation, and Update never has to disagree with Init about what a
	// lamp's position or colour is.
	e.Scene.SetPointLights(g.lamps)

	if !g.spotsOn {
		e.Scene.SetSpotLights(nil)
		return
	}
	spots := make([]glyph.SpotLight, buildingCount)
	for i, b := range g.buildings {
		spots[i] = g.buildingSpot(i, b)
	}
	e.Scene.SetSpotLights(spots)
}

// spotColor is pushed redder than a real 2800K incandescent would be -- see
// the doorstep comment in Init for why: at this time of night the shader's
// night shift takes 80% of every lit fragment toward a blue-weighted grey,
// so a colour with green anywhere near incandescent's real proportion loses
// the fight for luminance (green carries 0.7152 of it) and reads pale blue
// instead of warm. This is the colour that measured out ahead once, paired
// with the doorstep patch's albedo -- not a physical 2800K, but what reads
// as "warm doorway" after the shift this engine applies at night.
var spotColor = mgl32.Vec3{1.0, 0.28, 0.0}.Mul(2.4)

// buildingSpot returns building i's current spotlight. Only building 0's
// beam direction turns under -sweep -- the fixture and every other building
// stay put, so a capture can show one moving footprint against a scene that
// otherwise proves nothing has shifted.
func (g *game) buildingSpot(i int, b building) glyph.SpotLight {
	yaw := b.yaw
	if g.sweep && i == 0 {
		yaw += g.t * sweepRate
	}
	dir := rotateY(yaw, mgl32.Vec3{0, -1, 0.35})
	pos := b.pos.Add(rotateY(b.yaw, mgl32.Vec3{0, fixtureHeight, buildingDepth/2 + fixtureForward}))
	return glyph.SpotLight{
		Pos:   pos,
		Dir:   dir,
		Range: spotRange,
		Color: spotColor,
		Inner: spotInner,
		Outer: spotOuter,
	}
}

// rotateY rotates v about the Y axis by yaw, matching the convention
// Transform.ModelMatrix uses (mgl32.HomogRotate3DY): local +Z at yaw==0 maps
// to world (sin(yaw), 0, cos(yaw)). Every building offset here -- the door,
// the roof, the fixture, the spotlight direction -- is expressed in the same
// local frame as the wall's own Transform.Rotation, so a value computed with
// this function and a mesh rotated by the same yaw always agree about which
// way "the door side" is.
func rotateY(yaw float32, v mgl32.Vec3) mgl32.Vec3 {
	s, c := float32(math.Sin(float64(yaw))), float32(math.Cos(float64(yaw)))
	return mgl32.Vec3{v.X()*c + v.Z()*s, v.Y(), -v.X()*s + v.Z()*c}
}

// buildLamps places n point lights on a ring path around the settlement,
// each at the top of a post, sampling the actual terrain height at every
// post so lamps on the sloped ground outside the flattened plaza do not end
// up floating or buried.
func buildLamps(hm *glyph.Heightmap, n int) []glyph.PointLight {
	lamps := make([]glyph.PointLight, 0, n)
	for i := 0; i < n; i++ {
		angle := float64(i) / float64(n) * 2 * math.Pi
		x := float32(math.Cos(angle)) * lampPathRadius
		z := float32(math.Sin(angle)) * lampPathRadius
		y, _ := hm.HeightAt(x, z)
		lamps = append(lamps, glyph.PointLight{
			Pos:   mgl32.Vec3{x, y + lampPoleHeight, z},
			Range: lampRange,
			// Warm sodium amber, dimmer than the door spots: these are meant
			// to read as a path of small lights, not as a second set of
			// doorways.
			Color: mgl32.Vec3{1.0, 0.68, 0.32}.Mul(0.45),
		})
	}
	return lamps
}

// terrainTint colors terrain by height and slope: rock on anything steep,
// grass everywhere else. There is no waterline and no snowcap at this scale,
// so the rule is simpler than 07-terrain's.
func terrainTint(height float32, normal [3]float32) [3]float32 {
	slope := 1 - normal[1]
	if slope > 0.5 {
		return [3]float32{0.34, 0.32, 0.30}
	}
	return [3]float32{0.20, 0.30, 0.18}
}

// ─────────────────────── procedural heightmap ───────────────────────
//
// Value-noise fBm, the same shape as 07-terrain's, with amplitude ramped
// down near the origin so the settlement sits on ground flat enough to
// build on without a special case for every building's footing.

func generateHeights(gridW, gridH int) []float32 {
	heights := make([]float32, gridW*gridH)
	for iz := 0; iz < gridH; iz++ {
		for ix := 0; ix < gridW; ix++ {
			u := float64(ix) / float64(gridW-1)
			v := float64(iz) / float64(gridH-1)

			wx := (u - 0.5) * worldSize
			wz := (v - 0.5) * worldSize
			d := math.Sqrt(wx*wx + wz*wz)
			amp := smoothstep(clamp((d-flattenRadius)/flattenWidth, 0, 1))

			h := fbm(u*4, v*4)
			heights[iz*gridW+ix] = float32(h * amp * heightScale)
		}
	}
	return heights
}

func hash(x, y int) float64 {
	n := int64(x)*374761393 + int64(y)*668265263
	n = (n ^ (n >> 13)) * 1274126177
	n = n ^ (n >> 16)
	return float64(n&0x7fffffff) / float64(0x7fffffff)
}

func smoothstep(t float64) float64 { return t * t * (3 - 2*t) }

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func valueNoise(x, y float64) float64 {
	xi, yi := math.Floor(x), math.Floor(y)
	xf, yf := x-xi, y-yi
	ix, iy := int(xi), int(yi)

	v00 := hash(ix, iy)
	v10 := hash(ix+1, iy)
	v01 := hash(ix, iy+1)
	v11 := hash(ix+1, iy+1)

	sx, sy := smoothstep(xf), smoothstep(yf)
	top := v00 + (v10-v00)*sx
	bot := v01 + (v11-v01)*sx
	return top + (bot-top)*sy
}

func fbm(x, y float64) float64 {
	const octaves = 4
	amp, freq, sum, norm := 1.0, 1.0, 0.0, 0.0
	for i := 0; i < octaves; i++ {
		sum += valueNoise(x*freq, y*freq) * amp
		norm += amp
		amp *= 0.5
		freq *= 2
	}
	return sum/norm - 0.5
}

// ─────────────────────── wall material maps ───────────────────────
//
// Coursed masonry: rows of bricks offset by half a brick every other row,
// the same smoothstep-groove technique 16-materials uses for its tiles, so
// this example needs no asset files either.

const (
	brickRows = 9.0
	brickCols = 12.0
)

// hash01 is a cheap deterministic value in [0,1) from two integers, for
// per-brick shade variation only -- it needs to look arbitrary, not be random.
func hash01(x, y int) float64 {
	h := uint32(x)*374761393 + uint32(y)*668265263
	h = (h ^ (h >> 13)) * 1274126177
	return float64(h^(h>>16)) / float64(1<<32)
}

// brickHeight is 1 on a brick face and 0 in the mortar joint between them.
func brickHeight(u, v float64) float64 {
	row := math.Floor(v * brickRows)
	offset := 0.0
	if int(row)%2 == 1 {
		offset = 0.5 / brickCols
	}
	fu := math.Mod(u+offset, 1)
	fu -= math.Floor(fu)
	fu = fu*brickCols - math.Floor(fu*brickCols)
	fv := v*brickRows - math.Floor(v*brickRows)

	du := math.Min(fu, 1-fu)
	dv := math.Min(fv, 1-fv)
	d := math.Min(du, dv)

	const mortar = 0.05
	const bevel = 0.12
	return smoothstep(clamp((d-mortar)/bevel, 0, 1))
}

func buildWallAlbedo() []byte {
	pix := make([]byte, wallMapSize*wallMapSize*4)
	for y := 0; y < wallMapSize; y++ {
		for x := 0; x < wallMapSize; x++ {
			u := (float64(x) + 0.5) / wallMapSize
			v := (float64(y) + 0.5) / wallMapSize
			h := brickHeight(u, v)

			bx := int(u * brickCols)
			by := int(v * brickRows)
			shade := 0.85 + 0.15*hash01(bx, by)

			r := (0.58*shade)*h + 0.22*(1-h)
			g := (0.40*shade)*h + 0.20*(1-h)
			b := (0.32*shade)*h + 0.19*(1-h)

			i := (y*wallMapSize + x) * 4
			pix[i+0] = byte(clamp(r, 0, 1) * 255)
			pix[i+1] = byte(clamp(g, 0, 1) * 255)
			pix[i+2] = byte(clamp(b, 0, 1) * 255)
			pix[i+3] = 255
		}
	}
	return pix
}

// buildWallNormal derives the tangent-space normal from the same height
// field the albedo used, by central differences -- see material-maps.md for
// why this has to be uploaded as a data texture rather than a colour one.
func buildWallNormal(strength float64) []byte {
	pix := make([]byte, wallMapSize*wallMapSize*4)
	const texel = 1.0 / wallMapSize
	for y := 0; y < wallMapSize; y++ {
		for x := 0; x < wallMapSize; x++ {
			u := (float64(x) + 0.5) / wallMapSize
			v := (float64(y) + 0.5) / wallMapSize

			dhdu := (brickHeight(u+texel, v) - brickHeight(u-texel, v)) * 0.5
			dhdv := (brickHeight(u, v+texel) - brickHeight(u, v-texel)) * 0.5

			nx := -dhdu * strength
			ny := -dhdv * strength
			nz := 1.0
			l := math.Sqrt(nx*nx + ny*ny + nz*nz)
			nx, ny, nz = nx/l, ny/l, nz/l

			i := (y*wallMapSize + x) * 4
			pix[i+0] = byte((nx*0.5 + 0.5) * 255)
			pix[i+1] = byte((ny*0.5 + 0.5) * 255)
			pix[i+2] = byte((nz*0.5 + 0.5) * 255)
			pix[i+3] = 255
		}
	}
	return pix
}

// buildWallRoughness follows glTF packing (roughness in G, metallic in B):
// the mortar joints are rougher than the brick faces.
func buildWallRoughness() []byte {
	pix := make([]byte, wallMapSize*wallMapSize*4)
	for y := 0; y < wallMapSize; y++ {
		for x := 0; x < wallMapSize; x++ {
			u := (float64(x) + 0.5) / wallMapSize
			v := (float64(y) + 0.5) / wallMapSize
			h := brickHeight(u, v)
			roughness := 0.55 + 0.30*(1-h)

			i := (y*wallMapSize + x) * 4
			pix[i+0] = 255
			pix[i+1] = byte(roughness * 255)
			pix[i+2] = 0
			pix[i+3] = 255
		}
	}
	return pix
}

func main() {
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	fullscreen := flag.Bool("fullscreen", false, "run fullscreen on the primary monitor")
	frames := flag.Int("frames", 0, "render N frames then exit (0 = run until closed)")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	lamps := flag.Int("lamps", 20, "number of street lamps on the ring path")
	sweep := flag.Bool("sweep", false, "rotate building 0's spotlight over time, so its footprint moves")
	off := flag.Bool("off", false, "start with the spotlight set cleared (SetSpotLights(nil))")
	lightDebug := flag.String("lightdebug", "", "light debug mode: heatmap or bruteforce (default: off)")
	lightStats := flag.Bool("lightstats", false, "log the light binner's stats for the last frame on exit")
	camDist := flag.Float64("camdist", 42, "camera orbit distance")
	camPitch := flag.Float64("campitch", 0.5, "camera pitch in radians")
	camYaw := flag.Float64("camyaw", 0.4, "camera yaw in radians")
	camTargetX := flag.Float64("camtargetx", 0, "camera orbit target X")
	camTargetY := flag.Float64("camtargety", 2.5, "camera orbit target Y")
	camTargetZ := flag.Float64("camtargetz", 0, "camera orbit target Z")
	camLook := flag.Float64("camlook", 1.2, "how far above the target the camera looks")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 21 Streetlights"),
		glyph.WithWindowSize(*width, *height),
		glyph.WithMSAA(4),
		glyph.WithDebugKeys(),
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

	g := &game{
		lampCount:  *lamps,
		sweep:      *sweep,
		spotsOn:    !*off,
		camDist:    float32(*camDist),
		camPitch:   float32(*camPitch),
		camYaw:     float32(*camYaw),
		camTargetX: float32(*camTargetX),
		camTargetY: float32(*camTargetY),
		camTargetZ: float32(*camTargetZ),
		camLook:    float32(*camLook),
	}
	e, err := glyph.New(g, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	switch *lightDebug {
	case "heatmap":
		e.SetLightDebugMode(glyph.LightDebugHeatmap)
	case "bruteforce":
		e.SetLightDebugMode(glyph.LightDebugBruteForce)
	case "":
		// default: off
	default:
		log.Fatalf("unknown -lightdebug %q, want heatmap or bruteforce", *lightDebug)
	}

	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
	if *lightStats {
		log.Printf("light stats: %+v", e.LightStats())
	}
}
