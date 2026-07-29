package glyphengine

import (
	"fmt"

	"github.com/derekmwright/glyphengine/renderer"
)

// WaterOptions configures a water surface built over a heightmap.
//
// The zero value is not useful; use DefaultWaterOptions and adjust.
type WaterOptions struct {
	// Level is the world Y of the still surface. Terrain below it is
	// underwater, terrain above it is dry land and gets no surface built over
	// it at all.
	Level float32

	// Resolution is the number of grid divisions along each axis. Waves are
	// displaced per vertex, so this bounds the shortest wavelength the surface
	// can actually show: too coarse and the Gerstner sum turns into visible
	// faceting. 160 is comfortable for a lake a couple of hundred units wide.
	Resolution int

	// ShallowColor and DeepColor are the water's own colour at the surface and
	// at AbsorptionDepth. Real water absorbs red first, so a deep colour that
	// is bluer and darker than the shallow one reads correctly.
	ShallowColor [3]float32
	DeepColor    [3]float32

	// AbsorptionDepth is how far light travels through the water before it is
	// fully absorbed, in world units. It sets both how quickly the colour
	// reaches DeepColor and how quickly the bottom stops being visible, which
	// is why it is one number rather than two: in real water they are the same
	// physical process.
	AbsorptionDepth float32

	// WaveAmplitude is the height of the largest wave component, in world
	// units. Waves are scaled down in shallow water so they vanish at the
	// shoreline rather than cutting through it.
	WaveAmplitude float32

	// WaveLength is the wavelength of the largest wave component, in world
	// units. Three shorter components ride on top of it.
	WaveLength float32

	// RefractStrength scales how far the surface displaces the view of the
	// lake bed. Zero disables refraction entirely and falls back to ordinary
	// alpha blending, which costs nothing but shows an undistorted bottom.
	RefractStrength float32
}

// DefaultWaterOptions is a calm freshwater lake.
func DefaultWaterOptions(level float32) WaterOptions {
	return WaterOptions{
		Level:           level,
		Resolution:      160,
		ShallowColor:    [3]float32{0.30, 0.52, 0.52},
		DeepColor:       [3]float32{0.02, 0.10, 0.17},
		AbsorptionDepth: 6.0,
		WaveAmplitude:   0.10,
		WaveLength:      7.0,
		RefractStrength: 1.0,
	}
}

// WaterMesh builds a surface grid covering the submerged parts of a heightmap.
//
// Quads whose four corners are all above Level are dropped, so the mesh
// follows the shape of the lake instead of covering the whole map. That is
// worth doing for more than the triangle count: a surface stretched over dry
// land would z-fight with the ground it sits inside.
//
// Each vertex carries the still-water depth beneath it, sampled from the
// heightmap. Baking it at build time means the shader gets the depth of the
// real lake bed without a depth buffer read, which is what lets the shore fade,
// the colour absorption, and the wave shoaling all work in the first pass.
func WaterMesh(h *Heightmap, opts WaterOptions) ([]renderer.Vertex, []uint32, error) {
	if h == nil {
		return nil, nil, fmt.Errorf("glyphengine: WaterMesh: nil heightmap")
	}
	if opts.Resolution < 2 {
		return nil, nil, fmt.Errorf("glyphengine: WaterMesh: resolution %d must be at least 2", opts.Resolution)
	}
	if opts.AbsorptionDepth <= 0 {
		return nil, nil, fmt.Errorf("glyphengine: WaterMesh: absorption depth must be positive, got %g", opts.AbsorptionDepth)
	}

	minX, minZ, maxX, maxZ := h.Bounds()
	n := opts.Resolution
	stepX := (maxX - minX) / float32(n)
	stepZ := (maxZ - minZ) / float32(n)

	// Depth at every grid point, so the quad test and the vertex data agree.
	depth := make([]float32, (n+1)*(n+1))
	for iz := 0; iz <= n; iz++ {
		for ix := 0; ix <= n; ix++ {
			x := minX + float32(ix)*stepX
			z := minZ + float32(iz)*stepZ
			ground, ok := h.HeightAt(x, z)
			if !ok {
				// Off the heightmap: treat as deep, so a lake that runs to the
				// edge of the map does not develop a false shoreline there.
				depth[iz*(n+1)+ix] = opts.AbsorptionDepth
				continue
			}
			if d := opts.Level - ground; d > 0 {
				depth[iz*(n+1)+ix] = d
			}
		}
	}

	// Emit only vertices that some kept quad actually uses, so the buffer does
	// not carry the dry parts of the grid.
	index := make([]int32, (n+1)*(n+1))
	for i := range index {
		index[i] = -1
	}
	var (
		vertices []renderer.Vertex
		indices  []uint32
	)

	emit := func(ix, iz int) uint32 {
		gi := iz*(n+1) + ix
		if index[gi] >= 0 {
			return uint32(index[gi])
		}
		x := minX + float32(ix)*stepX
		z := minZ + float32(iz)*stepZ
		index[gi] = int32(len(vertices))
		vertices = append(vertices, renderer.Vertex{
			Pos:   [3]float32{x, opts.Level, z},
			Color: opts.DeepColor,
			// The surface normal comes from the wave derivatives, so this
			// attribute is free and carries the shallow colour instead. See
			// water.vert, which documents the same trade from the other side.
			Normal: opts.ShallowColor,
			UV:     [2]float32{depth[gi], 0},
		})
		return uint32(index[gi])
	}

	for iz := 0; iz < n; iz++ {
		for ix := 0; ix < n; ix++ {
			d00 := depth[iz*(n+1)+ix]
			d10 := depth[iz*(n+1)+ix+1]
			d01 := depth[(iz+1)*(n+1)+ix]
			d11 := depth[(iz+1)*(n+1)+ix+1]

			// Keep a quad if any corner is underwater. Keeping the partly-dry
			// ones is deliberate: they are the shoreline, and their dry corners
			// have depth 0, which is exactly what fades the surface out there.
			if d00 <= 0 && d10 <= 0 && d01 <= 0 && d11 <= 0 {
				continue
			}

			v00 := emit(ix, iz)
			v10 := emit(ix+1, iz)
			v01 := emit(ix, iz+1)
			v11 := emit(ix+1, iz+1)

			indices = append(indices,
				v00, v01, v10,
				v10, v01, v11,
			)
		}
	}

	if len(indices) == 0 {
		return nil, nil, fmt.Errorf("glyphengine: WaterMesh: level %g is above all terrain, nothing to draw", opts.Level)
	}
	return vertices, indices, nil
}

// CreateWaterMesh builds a water surface over the heightmap and uploads it.
//
// Give the returned mesh to an entity with a Water component:
//
//	mesh, err := e.CreateWaterMesh(hm, opts)
//	ent := e.Spawn()
//	e.C.Transform.Set(ent, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
//	e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: mesh})
//	e.C.Water.Set(ent, &glyph.Water{Options: opts})
//	e.C.Static.Set(ent, &glyph.Static{})
func (e *Engine) CreateWaterMesh(h *Heightmap, opts WaterOptions) (*renderer.Mesh, error) {
	vertices, indices, err := WaterMesh(h, opts)
	if err != nil {
		return nil, err
	}
	return e.renderer.CreateIndexedMesh32(vertices, indices)
}
