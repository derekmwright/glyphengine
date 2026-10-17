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
	// can actually show. 160 is comfortable for a lake a couple of hundred
	// units wide.
	//
	// It interacts with WaveLength, and the interaction is worth knowing about.
	// The shortest of the five wave components is WaveLength * 0.26, and a
	// wavelength needs at least two vertices across it to be a wave at all
	// rather than a beat pattern the grid invented. Below that the shader fades
	// the component out rather than rendering it, so asking for short waves on a
	// coarse grid gets smooth water rather than choppy water — never faceted
	// water, which is what it used to get.
	//
	// The rule, given a surface w world units wide:
	//
	//	WaveLength * 0.26 >= 2 * w / Resolution
	//
	// The defaults sit just below that, so the finest component is faded at
	// Resolution 160 over a 200-unit lake. Raise Resolution to about 256 to get
	// it as a real wave.
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

	// WaveAmplitude is the surface's maximum crest height above still water, in
	// world units — the five components' amplitudes sum to it. Waves are scaled
	// down in shallow water so they vanish at the shoreline rather than cutting
	// through it.
	//
	// Amplitude tall relative to WaveLength is self-limiting: a Gerstner surface
	// stops being a function once its crests would fold over, and the shader
	// clamps steepness to keep it just short of that. Past roughly
	// WaveLength / 11 the crests stop getting sharper and only get taller.
	WaveAmplitude float32

	// WaveLength is the wavelength of the longest wave component, in world
	// units. Four shorter ones ride on top of it, down to 0.26 of it. See
	// Resolution for the shortest wavelength a given grid can carry.
	WaveLength float32

	// WaveNoise is how much of WaveAmplitude arrives as drifting fractal noise
	// rather than as one of the five wave components, from 0 to about 1.
	//
	// A sum of sinusoids is exactly periodic, so without this the surface tiles
	// at a glancing angle — the same crest pattern marching away from you to the
	// horizon. The noise gives the field variation no sum of sinusoids can:
	// patches of chop, stretches of calm, and crests that do not all agree.
	//
	// Zero disables it and restores the pure Gerstner sum. Its octaves are
	// bounded by the grid exactly as the wave components are, so raising it on a
	// coarse mesh makes the water lumpier, never faceted.
	WaveNoise float32

	// RefractStrength scales how far the surface displaces the view of the
	// lake bed. Zero disables refraction entirely and falls back to ordinary
	// alpha blending, which costs nothing but shows an undistorted bottom.
	RefractStrength float32

	// Subsurface is light let through a backlit crest. The zero value is no
	// term at all, and a surface without it renders byte for byte what it
	// rendered before the option existed. DefaultWaterSubsurface is the
	// measured starting point.
	Subsurface WaterSubsurface

	// Foam is whitecap foam on crests that are close to breaking. The zero
	// value is no foam and costs nothing: CreateWaterMesh makes no target and
	// the frame has no dispatch for it. Unlike the other options it has to be
	// decided when the mesh is created, because that is when the target is.
	// DefaultWaterFoam is the measured starting point.
	Foam WaterFoam
}

// WaterFoam is whitecap foam: marked where the wave sum squeezes the surface
// together hard enough to break, kept in a persistent target over the surface
// grid, and faded out over Decay seconds, so a crest leaves foam behind it
// rather than carrying a highlight along.
//
// Where the surface is squeezed is the Jacobian of the Gerstner sum's
// horizontal displacement, evaluated by the same shader code that displaces
// the vertices and builds their normal: one on calm water, falling toward zero
// under a sharp crest. WaterOptions.SurfaceAt reports it at any point.
//
// The target is one R16F texel per grid vertex, stepped by one compute
// dispatch per surface per frame the surface is drawn, and the foam is blended
// over the water as a matte layer lit by the sun and the ambient. See
// docs/agents/water.md for what it costs and how the defaults were measured.
type WaterFoam struct {
	// Threshold is the Jacobian below which a crest makes foam; new foam
	// ramps from none at the threshold to full 0.1 below it.
	Threshold float32
	// Decay is how many seconds full foam takes to fade to nothing, in
	// shader time (Engine.Elapsed). Zero, the default, is no foam at all.
	Decay float32
	// Color is the foam's albedo, lit by the sun and the ambient.
	Color [3]float32
}

// DefaultWaterFoam is the measured starting point for WaterOptions.Foam.
// DefaultWaterOptions leaves foam off: whether the engine's lake has
// whitecaps by default is a decision for someone looking at it.
func DefaultWaterFoam() WaterFoam {
	return WaterFoam{
		Threshold: 0.8,
		Decay:     3.0,
		Color:     [3]float32{0.83, 0.88, 0.88},
	}
}

// WaterSubsurface is the glow of a thin wave crest with the sun low behind it.
//
// The fragment stage adds Color * Strength, times the sun's colour and the
// shadow the surface is already in, times three factors that say where the glow
// belongs: how far the crest rose above still water as a fraction of
// WaveAmplitude, how nearly the sun is behind the crest from the eye, and how
// edge-on the eye sees it. It is added under the Fresnel reflection, so a crest
// seen at a glancing angle shows the sky rather than the glow, as it would.
//
// It reads the sun and nothing else: a lamp behind a wave does not light it
// through. See docs/agents/water.md.
type WaterSubsurface struct {
	// Color is the water's colour in transmission. Light through a crest has
	// crossed centimetres of water rather than metres, so it is the shallow
	// colour rather than the deep one.
	Color [3]float32
	// Strength scales the whole term. Zero, the default, is no term.
	Strength float32
}

// DefaultWaterSubsurface is the measured starting point for
// WaterOptions.Subsurface. DefaultWaterOptions leaves it off: whether the
// engine's lake glows by default is a decision for someone looking at it.
func DefaultWaterSubsurface() WaterSubsurface {
	return WaterSubsurface{
		// DefaultWaterOptions' ShallowColor, for the reason on Color.
		Color:    [3]float32{0.30, 0.52, 0.52},
		Strength: 1.0,
	}
}

// DefaultWaterOptions is a calm freshwater lake.
func DefaultWaterOptions(level float32) WaterOptions {
	return WaterOptions{
		Level:           level,
		Resolution:      160,
		ShallowColor:    [3]float32{0.30, 0.52, 0.52},
		DeepColor:       [3]float32{0.02, 0.10, 0.17},
		AbsorptionDepth: 6.0,
		// A lake, not an ocean: calm enough that the wave sum reads as ripples
		// rather than as swell, which is also what keeps its periodicity from
		// announcing itself.
		WaveAmplitude:   0.13,
		WaveLength:      7.0,
		WaveNoise:       0.6,
		RefractStrength: 1.0,
	}
}

// params is what the water shader reads of these options, per frame; the rest
// are baked into the mesh.
func (o WaterOptions) params() renderer.WaterParams {
	return renderer.WaterParams{
		Amplitude:       o.WaveAmplitude,
		WaveLength:      o.WaveLength,
		AbsorptionDepth: o.AbsorptionDepth,
		RefractStrength: o.RefractStrength,
		WaveNoise:       o.WaveNoise,

		SubsurfaceColor:    o.Subsurface.Color,
		SubsurfaceStrength: o.Subsurface.Strength,

		FoamThreshold: o.Foam.Threshold,
		FoamDecay:     o.Foam.Decay,
		FoamColor:     o.Foam.Color,
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
	grid, err := NewWaterGrid(h, opts)
	if err != nil {
		return nil, nil, err
	}
	vertices, indices, err := grid.mesh(opts)
	return vertices, indices, err
}

// WaterGrid is the still grid a water surface is built on: the rest position
// of every vertex and the depth of still water under it. WaterMesh emits the
// underwater part of it as a mesh; the foam target is laid out over all of it,
// one texel per point; and WaterOptions.SurfaceAt evaluates the waves at any of
// its points as the vertex stage displaces the vertex there.
type WaterGrid struct {
	// Origin is the rest X and Z of point (0, 0), Step the spacing along X and
	// Z, and Width and Height the point counts -- Resolution+1 each way.
	Origin, Step  [2]float32
	Width, Height int
	// Depth is the still-water depth under each point, in rows along X; zero
	// over dry land.
	Depth []float32
}

// NewWaterGrid lays out the grid WaterMesh builds over a heightmap.
func NewWaterGrid(h *Heightmap, opts WaterOptions) (WaterGrid, error) {
	if h == nil {
		return WaterGrid{}, fmt.Errorf("glyphengine: WaterMesh: nil heightmap")
	}
	if opts.Resolution < 2 {
		return WaterGrid{}, fmt.Errorf("glyphengine: WaterMesh: resolution %d must be at least 2", opts.Resolution)
	}
	if opts.AbsorptionDepth <= 0 {
		return WaterGrid{}, fmt.Errorf("glyphengine: WaterMesh: absorption depth must be positive, got %g", opts.AbsorptionDepth)
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
	return WaterGrid{Origin: [2]float32{minX, minZ}, Step: [2]float32{stepX, stepZ}, Width: n + 1, Height: n + 1, Depth: depth}, nil
}

// Spacing is the coarser of the two steps, which is what the water shader
// judges which wave components the grid can carry by; see water.vert. It is
// the spacing SurfaceAt wants for a point of this grid.
func (g WaterGrid) Spacing() float32 { return max32(g.Step[0], g.Step[1]) }

// Point returns the rest X and Z of grid point (ix, iz) and the depth under it.
func (g WaterGrid) Point(ix, iz int) (x, z, depth float32) {
	return g.Origin[0] + float32(ix)*g.Step[0], g.Origin[1] + float32(iz)*g.Step[1], g.Depth[iz*g.Width+ix]
}

func (g WaterGrid) mesh(opts WaterOptions) ([]renderer.Vertex, []uint32, error) {
	n := g.Width - 1
	depth := g.Depth
	minX, minZ := g.Origin[0], g.Origin[1]
	stepX, stepZ := g.Step[0], g.Step[1]

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

	// The coarser of the two steps, so a non-square grid is judged by its worst
	// direction. The shader needs it to know which wave components this mesh is
	// too coarse to represent; see water.vert.
	spacing := g.Spacing()

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
			UV:     [2]float32{depth[gi], spacing},
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
//
// With opts.Foam.Decay above zero the mesh also gets its foam target, laid over
// the same grid; the target is released with the mesh.
func (e *Engine) CreateWaterMesh(h *Heightmap, opts WaterOptions) (*renderer.Mesh, error) {
	grid, err := NewWaterGrid(h, opts)
	if err != nil {
		return nil, err
	}
	vertices, indices, err := grid.mesh(opts)
	if err != nil {
		return nil, err
	}
	mesh, err := e.renderer.CreateIndexedMesh32(vertices, indices)
	if err != nil || !(opts.Foam.Decay > 0) {
		return mesh, err
	}
	if _, err := e.renderer.CreateWaterFoam(mesh, renderer.WaterFoamDesc{
		Origin: grid.Origin, Step: grid.Step, Width: grid.Width, Height: grid.Height, Depth: grid.Depth,
	}); err != nil {
		e.renderer.DestroyMesh(mesh)
		return nil, fmt.Errorf("glyphengine: CreateWaterMesh: %w", err)
	}
	return mesh, nil
}
