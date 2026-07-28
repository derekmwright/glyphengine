package glyphengine

import (
	"errors"

	"github.com/derekmwright/glyphengine/renderer"
)

// SplatTiles is how many times a terrain mesh's UVs repeat across its full
// extent. The terrain splat shader divides fragUV by this to recover the
// 0–1 top-down coordinate for the splat weight map, so terrain geometry built
// for that pipeline must use this tiling factor. TerrainMesh uses it by
// default.
const SplatTiles = float32(10)

// TerrainTint colors a terrain vertex from its world height and surface
// normal — the usual grass-in-the-valleys, rock-on-the-cliffs rule. Return
// linear RGB in 0–1.
type TerrainTint func(height float32, normal [3]float32) [3]float32

// TerrainOptions configures TerrainMesh. The zero value is valid.
type TerrainOptions struct {
	// UVTiles is how many times UVs repeat across the terrain. Zero means
	// SplatTiles, which is what the terrain splat pipeline expects.
	UVTiles float32

	// Tint colors each vertex. Nil means flat white, which the lit pipeline
	// renders as untinted.
	Tint TerrainTint
}

// TerrainMesh builds renderable geometry from a heightmap: one vertex per grid
// point, with normals from the heightmap's central differences so lighting is
// smooth across cell boundaries.
//
// It returns 32-bit indices because terrain grids routinely exceed 65,536
// vertices — a 256×256 heightmap is already 65,536 exactly. Feed the result to
// Renderer.CreateIndexedMesh32.
func TerrainMesh(h *Heightmap, opts *TerrainOptions) ([]renderer.Vertex, []uint32, error) {
	if h == nil {
		return nil, nil, errors.New("terrain mesh: nil heightmap")
	}
	if h.GridW < 2 || h.GridH < 2 {
		return nil, nil, errors.New("terrain mesh: heightmap must be at least 2x2")
	}

	var o TerrainOptions
	if opts != nil {
		o = *opts
	}
	if o.UVTiles <= 0 {
		o.UVTiles = SplatTiles
	}

	stepX := h.WorldW / float32(h.GridW-1)
	stepZ := h.WorldD / float32(h.GridH-1)

	vertices := make([]renderer.Vertex, 0, h.GridW*h.GridH)
	for iz := 0; iz < h.GridH; iz++ {
		for ix := 0; ix < h.GridW; ix++ {
			x := h.OriginX + float32(ix)*stepX
			z := h.OriginZ + float32(iz)*stepZ
			y := h.Heights[iz*h.GridW+ix]

			n := h.NormalAt(x, z)
			color := [3]float32{1, 1, 1}
			if o.Tint != nil {
				color = o.Tint(y, n)
			}

			vertices = append(vertices, renderer.Vertex{
				Pos:    [3]float32{x, y, z},
				Color:  color,
				Normal: n,
				UV: [2]float32{
					float32(ix) / float32(h.GridW-1) * o.UVTiles,
					float32(iz) / float32(h.GridH-1) * o.UVTiles,
				},
			})
		}
	}

	// Winding matches Renderer.CreatePlane: the engine's projection flips Y,
	// so a quad wound counter-clockwise when viewed from +Y arrives at the
	// rasterizer clockwise, which is the pipeline's front face.
	indices := make([]uint32, 0, (h.GridW-1)*(h.GridH-1)*6)
	for iz := 0; iz < h.GridH-1; iz++ {
		for ix := 0; ix < h.GridW-1; ix++ {
			v00 := uint32(iz*h.GridW + ix)
			v10 := v00 + 1
			v01 := uint32((iz+1)*h.GridW + ix)
			v11 := v01 + 1
			indices = append(indices, v00, v10, v11, v11, v01, v00)
		}
	}

	return vertices, indices, nil
}

// CreateTerrainMesh builds terrain geometry from a heightmap and uploads it to
// the GPU. The returned mesh is owned by the caller — pass it to a MeshRef and
// release it with Renderer.DestroyMesh.
func (e *Engine) CreateTerrainMesh(h *Heightmap, opts *TerrainOptions) (*renderer.Mesh, error) {
	vertices, indices, err := TerrainMesh(h, opts)
	if err != nil {
		return nil, err
	}
	return e.renderer.CreateIndexedMesh32(vertices, indices)
}
