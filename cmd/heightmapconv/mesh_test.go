package main

import (
	"path/filepath"
	"testing"

	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/modeler"

	glyph "github.com/derekmwright/glyphengine"
)

// TestLoadMeshTrianglesPutsAnAsymmetricTerrainInWorldSpace is the in-memory
// half of the issue's orientation proof: a 3x3 grid mesh with a ramp along
// one axis and a single tall spike at one known corner, parented under a
// node that translates AND rotates 90 degrees about Y -- exactly the shape
// tools/blender/build_terrain_fixture.py builds for real (see
// docs/agents/terrain-heightmap.md), except this version never leaves Go,
// so it pins the rasteriser and node-transform code against numbers
// computed independently by hand, before the real Blender fixture (a much
// more expensive, much less debuggable thing to get wrong first) enters the
// picture at all.
//
// Local mesh: vertices at (lx, h(lx,lz), lz) for lx,lz in {0,1,2}, h = lx
// (a ramp along local X) except the corner (lx=2, lz=2), which is a spike
// at h=20.
//
// Parent transform: rotate 90 degrees about Y, then translate (100, 0, 50).
// Hand-derived independently of buildNodes (see TestNodeWorldThroughParentChain
// for the same independent derivation): for a point (x, y, z),
// world = (z + 100, y, 50 - x). Applying that to the grid gives, in WORLD
// space: world X runs with local Z, world Z runs BACKWARDS with local X --
// so the rasteriser output grid's (ix, iz), built by OriginX/OriginZ
// ascending, ends up as ix = lz, iz = 2 - lx. The spike (lx=2, lz=2) lands
// at grid (ix=2, iz=0), world (102, 20, 48).
func TestLoadMeshTrianglesPutsAnAsymmetricTerrainInWorldSpace(t *testing.T) {
	height := func(lx, lz int) float64 {
		if lx == 2 && lz == 2 {
			return 20 // the spike, at the far corner from the origin
		}
		return float64(lx) // the ramp
	}

	var positions [][3]float32
	index := func(lx, lz int) uint32 { return uint32(lz*3 + lx) }
	for lz := 0; lz < 3; lz++ {
		for lx := 0; lx < 3; lx++ {
			positions = append(positions, [3]float32{float32(lx), float32(height(lx, lz)), float32(lz)})
		}
	}
	var indices []uint32
	for lz := 0; lz < 2; lz++ {
		for lx := 0; lx < 2; lx++ {
			v00, v10 := index(lx, lz), index(lx+1, lz)
			v01, v11 := index(lx, lz+1), index(lx+1, lz+1)
			indices = append(indices, v00, v10, v11, v00, v11, v01)
		}
	}

	doc := &gltf.Document{
		Asset: gltf.Asset{Version: "2.0"},
		Nodes: []*gltf.Node{
			{Name: "TerrainParent", Translation: [3]float64{100, 0, 50}, Rotation: quatY(1.5707963267948966), Children: []int{1}},
			{Name: "Terrain", Mesh: gltf.Index(0)},
		},
		Meshes: []*gltf.Mesh{{
			Name: "Terrain",
			Primitives: []*gltf.Primitive{{
				Indices: gltf.Index(0),
				Mode:    gltf.PrimitiveTriangles,
			}},
		}},
		Scene:  gltf.Index(0),
		Scenes: []*gltf.Scene{{Nodes: []int{0}}},
	}
	posIdx := modeler.WritePosition(doc, positions)
	idxIdx := modeler.WriteIndices(doc, indices)
	doc.Meshes[0].Primitives[0].Attributes = gltf.PrimitiveAttributes{gltf.POSITION: posIdx}
	doc.Meshes[0].Primitives[0].Indices = gltf.Index(idxIdx)

	path := filepath.Join(t.TempDir(), "terrain.glb")
	if err := gltf.SaveBinary(doc, path); err != nil {
		t.Fatalf("SaveBinary: %v", err)
	}

	tris, nodeName, err := loadMeshTriangles(path, "")
	if err != nil {
		t.Fatalf("loadMeshTriangles: %v", err)
	}
	if nodeName != "Terrain" {
		t.Errorf("chose node %q, want Terrain", nodeName)
	}

	minX, minZ, maxX, maxZ := meshBounds(tris)
	const wantMinX, wantMinZ, wantMaxX, wantMaxZ float64 = 100, 48, 102, 50
	if minX != wantMinX || minZ != wantMinZ || maxX != wantMaxX || maxZ != wantMaxZ {
		t.Fatalf("mesh bounds = %g,%g..%g,%g, want %g,%g..%g,%g", minX, minZ, maxX, maxZ, wantMinX, wantMinZ, wantMaxX, wantMaxZ)
	}

	res := rasterizeMesh(tris, 3, 3, minX, minZ, maxX, maxZ)
	if len(res.holes) != 0 || len(res.overhangs) != 0 {
		t.Fatalf("holes=%v overhangs=%v, want none", res.holes, res.overhangs)
	}

	// grid(ix,iz) = h(lx=2-iz, lz=ix), derived in the doc comment above.
	want := [3][3]float32{
		// iz=0            iz=1  iz=2
		{2, 1, 0},  // ix=0: lz=0, lx=2,1,0
		{2, 1, 0},  // ix=1: lz=1, lx=2,1,0
		{20, 1, 0}, // ix=2: lz=2, lx=2(SPIKE),1,0
	}
	for ix := 0; ix < 3; ix++ {
		for iz := 0; iz < 3; iz++ {
			got := res.heights[iz*3+ix]
			if got != want[ix][iz] {
				t.Errorf("heights[ix=%d,iz=%d] = %g, want %g", ix, iz, got, want[ix][iz])
			}
		}
	}

	h, err := glyph.NewHeightmap(3, 3, float32(maxX-minX), float32(maxZ-minZ), float32(minX), float32(minZ), res.heights)
	if err != nil {
		t.Fatalf("NewHeightmap: %v", err)
	}
	// HeightAt through the public API, at points that need no interpolation
	// to predict: the exact MIN corner and the centre grid point both land
	// on a single grid sample with fraction 0. The two MAX-edge corners
	// (102,48) and (100,50) are deliberately not probed here: HeightAt's
	// own gx/gz >= GridW-1 bound is exclusive on the top edge (heightmap.go),
	// an existing, unrelated boundary quirk this issue does not touch --
	// res.heights above already pinned those two corners' rasterised values
	// directly.
	atCases := []struct {
		x, z float32
		want float32
	}{
		{100, 48, 2}, // lx=2 (ramp top), lz=0 -- the MIN corner
		{101, 49, 1}, // centre grid point: lz=1, lx=1
	}
	for _, c := range atCases {
		got, ok := h.HeightAt(c.x, c.z)
		if !ok {
			t.Errorf("HeightAt(%g,%g) out of bounds, want a hit", c.x, c.z)
			continue
		}
		if got != c.want {
			t.Errorf("HeightAt(%g,%g) = %g, want %g", c.x, c.z, got, c.want)
		}
	}
}

// TestLoadMeshTrianglesErrorsWithNoMatchingNode covers the -node flag
// pointing at a name the document does not have, or does not carry a mesh.
func TestLoadMeshTrianglesErrorsWithNoMatchingNode(t *testing.T) {
	doc := &gltf.Document{
		Asset:  gltf.Asset{Version: "2.0"},
		Nodes:  []*gltf.Node{{Name: "Empty"}},
		Scene:  gltf.Index(0),
		Scenes: []*gltf.Scene{{Nodes: []int{0}}},
	}
	path := filepath.Join(t.TempDir(), "empty.glb")
	if err := gltf.SaveBinary(doc, path); err != nil {
		t.Fatalf("SaveBinary: %v", err)
	}
	if _, _, err := loadMeshTriangles(path, "Nope"); err == nil {
		t.Error("loadMeshTriangles with an unknown -node succeeded, want an error")
	}
	if _, _, err := loadMeshTriangles(path, ""); err == nil {
		t.Error("loadMeshTriangles on a document with no mesh-bearing node succeeded, want an error")
	}
}
