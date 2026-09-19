package main

import (
	"fmt"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/modeler"
)

// triangle is one world-space triangle, ready for rasterising: three
// vertices and the XZ bounding box used to bin it (see raster.go).
//
// Positions are float64 even though glTF (and the engine's own Vertex type)
// stores float32: the source precision does not improve, but composing a
// node's World matrix and then interpolating a height by barycentric
// coordinates in float64 keeps that composition's rounding error well under
// the epsilon two triangles sharing an edge need to agree within (see
// dedupeHeights).
type triangle struct {
	v0, v1, v2             [3]float64
	minX, maxX, minZ, maxZ float64
}

// loadMeshTriangles reads path's default scene, finds the node named
// nodeName (or the document's only mesh-bearing node if nodeName is ""),
// and returns every triangle of every TRIANGLES primitive that node's mesh
// carries, with the node's full World transform already applied.
//
// The node's transform matters as much as the mesh data: a terrain object
// translated and parented under a rotated empty (exactly the shape
// tools/blender/build_terrain_fixture.py builds on purpose, see
// docs/agents/terrain-heightmap.md) has vertex positions that are only
// correct in the FILE's local mesh space -- applying World is what puts
// them in the same world space the rest of the level, and this tool's
// output heightmap, live in.
func loadMeshTriangles(path, nodeName string) (tris []triangle, chosenNode string, err error) {
	doc, err := gltf.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open %q: %w", path, err)
	}

	nodes := buildNodes(doc)
	ni, err := findMeshNode(nodes, nodeName)
	if err != nil {
		return nil, "", err
	}
	nd := nodes[ni]

	mesh := doc.Meshes[nd.Mesh]
	for _, prim := range mesh.Primitives {
		// PrimitiveTriangles is the zero value (glTF's own default mode
		// when a primitive omits "mode"), so a nil-vs-zero Mode both mean
		// triangles; only an explicit non-triangle mode (points, lines,
		// strips/fans) is skipped.
		if prim.Mode != gltf.PrimitiveTriangles {
			continue
		}
		posIdx, ok := prim.Attributes[gltf.POSITION]
		if !ok {
			continue
		}
		positions, err := modeler.ReadPosition(doc, doc.Accessors[posIdx], nil)
		if err != nil {
			return nil, "", fmt.Errorf("read positions on node %q: %w", nd.Name, err)
		}

		var indices []uint32
		if prim.Indices != nil {
			indices, err = modeler.ReadIndices(doc, doc.Accessors[*prim.Indices], nil)
			if err != nil {
				return nil, "", fmt.Errorf("read indices on node %q: %w", nd.Name, err)
			}
		} else {
			// Non-indexed: glTF says the positions themselves are the
			// triangle list, three vertices at a time.
			indices = make([]uint32, len(positions))
			for i := range indices {
				indices[i] = uint32(i)
			}
		}

		for i := 0; i+2 < len(indices); i += 3 {
			v0 := transformPoint(nd.World, positions[indices[i]])
			v1 := transformPoint(nd.World, positions[indices[i+1]])
			v2 := transformPoint(nd.World, positions[indices[i+2]])
			tris = append(tris, newTriangle(v0, v1, v2))
		}
	}

	if len(tris) == 0 {
		return nil, nd.Name, fmt.Errorf("node %q has no triangle geometry", nd.Name)
	}
	return tris, nd.Name, nil
}

func transformPoint(m mgl32.Mat4, p [3]float32) [3]float64 {
	v := m.Mul4x1(mgl32.Vec4{p[0], p[1], p[2], 1})
	return [3]float64{float64(v[0]), float64(v[1]), float64(v[2])}
}

func newTriangle(v0, v1, v2 [3]float64) triangle {
	t := triangle{v0: v0, v1: v1, v2: v2}
	t.minX = min3(v0[0], v1[0], v2[0])
	t.maxX = max3(v0[0], v1[0], v2[0])
	t.minZ = min3(v0[2], v1[2], v2[2])
	t.maxZ = max3(v0[2], v1[2], v2[2])
	return t
}

func min3(a, b, c float64) float64 {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

func max3(a, b, c float64) float64 {
	if b > a {
		a = b
	}
	if c > a {
		a = c
	}
	return a
}

// meshBounds returns the XZ extent of tris, used as the default -bounds when
// none is given.
func meshBounds(tris []triangle) (minX, minZ, maxX, maxZ float64) {
	minX, minZ = tris[0].minX, tris[0].minZ
	maxX, maxZ = tris[0].maxX, tris[0].maxZ
	for _, t := range tris[1:] {
		if t.minX < minX {
			minX = t.minX
		}
		if t.minZ < minZ {
			minZ = t.minZ
		}
		if t.maxX > maxX {
			maxX = t.maxX
		}
		if t.maxZ > maxZ {
			maxZ = t.maxZ
		}
	}
	return
}
