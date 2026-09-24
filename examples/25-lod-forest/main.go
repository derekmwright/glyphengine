// Command 25-lod-forest compares per-placement LOD with a single full mesh set.
package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"runtime"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/ecs"
	"github.com/derekmwright/glyphengine/examples/internal/terrainfield"
	"github.com/derekmwright/glyphengine/input"
	"github.com/derekmwright/glyphengine/renderer"
	"github.com/go-gl/mathgl/mgl32"
)

func init() { runtime.LockOSThread() }

type game struct {
	fade                                float32
	levels                              int
	pose, heightmap                     string
	impostors, counts, replace, indexed bool
	set                                 *renderer.InstanceSetLOD
	control                             *renderer.InstanceSet
	desc                                renderer.InstanceSetLODDesc
	placements                          []renderer.MeshInstance
	entity                              ecs.Entity
	witness                             mgl32.Vec3
	time, printTime                     float32
	swaps                               int
	baseline                            renderer.ResourceCounts
	heightfield                         *glyph.Heightmap
	ground                              *renderer.Mesh
	sceneSwaps                          int
}

func hash(i uint32) float32 {
	i ^= i >> 16
	i *= 0x7feb352d
	i ^= i >> 15
	i *= 0x846ca68b
	i ^= i >> 16
	return float32(i&0xffffff) / float32(0x1000000)
}

// Trees are example content: cones with different tessellation and silhouettes.
func tree(r *renderer.Renderer, segments, tiers int, indexed bool) (*renderer.Mesh, error) {
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
	var m *renderer.Mesh
	var err error
	if indexed {
		indices := make([]uint16, len(verts))
		for i := range indices {
			indices[i] = uint16(i)
		}
		m, err = r.CreateIndexedMesh(verts, indices)
	} else {
		m, err = r.CreateMesh(verts)
	}
	if err == nil {
		m.BoundCenter = [3]float32{0, 3, 0}
		// The tallest tier reaches y=6.256; LOD culling is about the base
		// translation, so use 6.5 rather than a sphere fitted about y=3.
		m.BoundRadius = 6.5
	}
	return m, err
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()
	hm, err := terrainfield.Load(g.heightmap, 1)
	if err != nil {
		return err
	}
	e.SetTerrain(hm)
	ground, err := e.CreateTerrainMesh(hm, &glyph.TerrainOptions{Tint: func(_ float32, _ [3]float32) [3]float32 { return [3]float32{0.28, 0.34, 0.15} }})
	if err != nil {
		return err
	}
	g.heightfield, g.ground = hm, ground
	ent := e.Spawn()
	e.C.Transform.Set(ent, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: ground, Roughness: 1})
	e.C.NoCastShadow.Set(ent, &glyph.NoCastShadow{})
	for i, detail := range [][2]int{{24, 9}, {8, 4}, {4, 2}} {
		mesh, err := tree(r, detail[0], detail[1], g.indexed)
		if err != nil {
			return err
		}
		g.desc.Levels = append(g.desc.Levels, renderer.LODLevel{Mesh: mesh, MaxDistance: []float32{40, 75, 110}[i]})
		log.Printf("level %d: %d triangles", i, mesh.VertexCount/3)
	}
	if g.impostors {
		g.desc.Impostor, err = r.BakeImpostor(g.desc.Levels[0].Mesh, nil, 128)
		if err != nil {
			return err
		}
	}
	g.desc.Capacity = 3600
	g.desc.FadeWidth = g.fade
	g.desc.ShadowLevel = 1
	minX, minZ, maxX, maxZ := hm.Bounds()
	place := func(x, z, yaw, scale float32) {
		y, _ := hm.HeightAt(x, z)
		m := mgl32.Translate3D(x, y, z).Mul4(mgl32.HomogRotate3DY(yaw)).Mul4(mgl32.Scale3D(scale, scale, scale))
		g.placements = append(g.placements, renderer.MeshInstance{Model: [16]float32(m), Tint: [4]float32{0.85 + hash(uint32(len(g.placements)+133))*0.15, 1, 0.9, 0}})
	}
	place(minX+4, minZ+4, 0, 1)
	g.witness = mgl32.Vec3{g.placements[0].Model[12], g.placements[0].Model[13], g.placements[0].Model[14]}
	for i := 1; i < 3600; i++ {
		x := minX + (maxX-minX)*(0.11+0.78*hash(uint32(i*2)))
		z := minZ + (maxZ-minZ)*(0.11+0.78*hash(uint32(i*2+1)))
		place(x, z, hash(uint32(i+9000))*2*math.Pi, 0.8+hash(uint32(i+7000))*0.4)
	}
	if err = g.makeSet(e); err != nil {
		return err
	}
	e.SetDayCycleSpeed(0)
	e.SetTimeOfDay(0.34)
	e.SetFogDensity(0)
	g.baseline = r.ResourceCounts()
	return nil
}

func (g *game) makeSet(e *glyph.Engine) error {
	r := e.Renderer()
	var err error
	g.entity = e.Spawn()
	if g.levels == 1 {
		g.control, err = r.CreateInstanceSet(g.desc.Levels[0].Mesh, len(g.placements), g.placements)
		e.C.InstancedMesh.Set(g.entity, &glyph.InstancedMesh{Set: g.control})
	} else {
		g.set, err = r.CreateInstanceSetLOD(g.desc, g.placements)
		e.C.InstancedMesh.Set(g.entity, &glyph.InstancedMesh{LOD: g.set})
	}
	e.C.MeshRef.Set(g.entity, &glyph.MeshRef{Roughness: 1})
	e.C.DoubleSided.Set(g.entity, &glyph.DoubleSided{})
	return err
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	if e.Input().KeyPressed(input.KeyEscape) {
		e.Close()
	}
	g.time += dt
	g.printTime += dt
	var eye, target mgl32.Vec3
	switch g.pose {
	case "edge":
		eye = g.witness.Add(mgl32.Vec3{14, 3, 0})
		target = g.witness.Add(mgl32.Vec3{0, 3, 0})
	case "pop":
		// At 16.667 ms/frame, frames 60 and 61 straddle the 40-unit band.
		d := float32(40) + 0.03*(float32(e.FrameCount())-59.5)
		eye = g.witness.Add(mgl32.Vec3{float32(math.Sqrt(float64(d*d - 9))), 3, 0})
		target = g.witness.Add(mgl32.Vec3{0, 3, 0})
	case "far":
		eye = mgl32.Vec3{0, 30, 160}
		target = mgl32.Vec3{0, 5, 0}
	default:
		t := float64(g.time) * 0.13
		eye = mgl32.Vec3{float32(math.Sin(t)) * 95, 22 + float32(math.Sin(t*0.7))*7, 100 - float32(math.Sin(t*0.6))*65}
		target = mgl32.Vec3{float32(math.Sin(t+0.3)) * 35, 8, -25}
	}
	e.SetCamera(eye, target, mgl32.Vec3{0, 1, 0})
	if g.counts && g.printTime >= 1 {
		g.printTime = 0
		g.printCounts()
	}
	if g.replace && e.FrameCount() > 0 && e.FrameCount()%20 == 0 {
		before := e.Renderer().ResourceCounts()
		if before.LODSets != g.baseline.LODSets || before.ImpostorAtlases != g.baseline.ImpostorAtlases {
			panic("LOD replacement leaked resources")
		}
		old, oldAtlas, oldEntity := g.set, g.desc.Impostor, g.entity
		if g.impostors {
			a, err := e.Renderer().BakeImpostor(g.desc.Levels[0].Mesh, nil, 128)
			if err != nil {
				panic(err)
			}
			g.desc.Impostor = a
		}
		if err := g.makeSet(e); err != nil {
			panic(err)
		}
		e.Despawn(oldEntity)
		if g.swaps%2 == 1 {
			// Prepare the next scene before exchanging the exported Scene field.
			// GPU meshes and the new LOD set survive that game-owned swap.
			next := glyph.NewScene()
			next.SetTerrain(g.heightfield)
			groundEntity := next.Spawn()
			next.C.Transform.Set(groundEntity, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
			next.C.MeshRef.Set(groundEntity, &glyph.MeshRef{Mesh: g.ground, Roughness: 1})
			next.C.NoCastShadow.Set(groundEntity, &glyph.NoCastShadow{})
			g.entity = next.Spawn()
			next.C.InstancedMesh.Set(g.entity, &glyph.InstancedMesh{LOD: g.set})
			next.C.MeshRef.Set(g.entity, &glyph.MeshRef{Roughness: 1})
			next.C.DoubleSided.Set(g.entity, &glyph.DoubleSided{})
			next.SetDayCycleSpeed(0)
			next.SetTimeOfDay(0.34)
			e.Scene = next
			e.SetFogDensity(0)
			g.sceneSwaps++
		}
		before = e.Renderer().ResourceCounts()
		e.Renderer().DestroyInstanceSetLOD(old)
		e.Renderer().DestroyImpostorAtlas(oldAtlas)
		after := e.Renderer().ResourceCounts()
		if before.LODSets != after.LODSets || before.ImpostorAtlases != after.ImpostorAtlases {
			panic("LOD replacement freed resources in flight")
		}
		g.swaps++
	}
}

func (g *game) printCounts() {
	if g.control != nil {
		fmt.Printf("LOD counts=[%d] culled=0 total=%d\n", g.control.Count(), len(g.placements))
		return
	}
	drawn, culled := g.set.Counts()
	fmt.Printf("LOD counts=%v culled=%d total=%d\n", drawn, culled, len(g.placements))
}

func main() {
	frames := flag.Int("frames", 0, "render N frames then exit")
	indexed := flag.Bool("indexed", false, "use indexed meshes to exercise indexed indirect draws")
	gpu := flag.Bool("gpu", false, "select LOD on the GPU and issue indirect draws")
	shot := flag.String("screenshot", "", "last-frame PNG path")
	fade := flag.Float64("fade", 6, "transition width in world units")
	levels := flag.Int("levels", 3, "3 = LOD; 1 = full-detail single-set control")
	counts := flag.Bool("counts", false, "print bucket counts each second and at exit")
	pose := flag.String("pose", "fly", "fly, edge, pop or far camera path")
	impostors := flag.Bool("impostor", true, "draw the baked far level")
	replace := flag.Bool("replace", false, "replace the LOD set and atlas every 20 frames")
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	msaa := flag.Int("msaa", 4, "samples per pixel, 1 or 4")
	heightmap := flag.String("heightmap", "", "load the terrain example's .heightmap format")
	flag.Parse()
	if *levels != 1 && *levels != 3 {
		log.Fatal("-levels must be 1 or 3")
	}
	if *replace && *levels == 1 {
		log.Fatal("-replace requires LOD levels")
	}
	g := &game{fade: float32(*fade), levels: *levels, pose: *pose, counts: *counts, impostors: *impostors, replace: *replace, heightmap: *heightmap}
	g.desc.GPU = *gpu
	g.indexed = *indexed
	opts := []glyph.Option{glyph.WithTitle("GlyphEngine - 25 LOD Forest"), glyph.WithWindowSize(*width, *height), glyph.WithMSAA(*msaa), glyph.WithProjection(50, 0.1, 800)}
	if *frames > 0 {
		opts = append(opts, glyph.WithMaxFrames(*frames))
	}
	if *shot != "" {
		opts = append(opts, glyph.WithScreenshot(*shot))
	}
	e, err := glyph.New(g, opts...)
	if err != nil {
		log.Fatal(err)
	}
	defer e.Destroy()
	e.Run()
	if *counts {
		g.printCounts()
		st := e.Renderer().Stats()
		fmt.Printf("LOD stats draws=%d instances=%d triangles=%d\n", st.DrawCalls, st.Instances, st.Triangles)
	}
	if *replace {
		fmt.Printf("LOD replacements=%d resources=%+v\n", g.swaps, e.Renderer().ResourceCounts())
		fmt.Printf("LOD scene-swaps=%d\n", g.sceneSwaps)
	}
	log.Printf("rendered %d frames", e.FrameCount())
}
