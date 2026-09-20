// Command gen writes examples/22-level/assets/level.glb: a small settlement
// authored directly against qmuntal/gltf rather than exported from an
// editor, so the committed asset and the numbers this package's tests check
// it against can never drift apart.
//
// It exists to give examples/22-level something to load that exercises every
// piece issue #66 added: node `extras` (mixed types -- a bool, a string and
// a number, on the buildings), KHR_lights_punctual (a shared spot light
// definition referenced by four lamp posts, and one point light), several
// nodes instancing ONE doc mesh with different TRS including a non-uniform
// scale plus a rotation, and an empty "spawn" node carrying extras rather
// than a name a game would have to know in advance.
//
// The light intensities, cone angles and node rotation are not made up --
// they are the numbers a real Blender 5.0.1 glTF export actually produces
// (measured headlessly, +Y up, default SPEC lighting mode), so this file
// doubles as a record of what a level built in the reference world-building
// pipeline looks like on the wire:
//
//   - A 1000 W Blender SPOT exports as intensity 54351.4 (candela) with
//     spot_size 1.2 / blend 0.3 -> outerConeAngle 0.6, innerConeAngle 0.42.
//   - A 100 W Blender POINT exports as intensity 5435.1 (candela); this file
//     uses a 500 W-equivalent point light (5435.1 * 5 = 27175.5) so the
//     plaza gets a light bright enough to see by. Scale is linear in both
//     directions in Blender's exporter, which is how that multiplication is
//     legitimate rather than invented.
//   - Blender's exporter never writes "range" -- every light below is
//     unbounded (glTF's own default), which is the NORMAL case for a
//     Blender-authored level, not a corner case.
//   - Each light sits on its own node carrying rotation
//     (-0.7071068, 0, 0, 0.7071068) -- minus 90 degrees about X -- which is
//     what points glTF's local -Z light axis at world -Y (straight down) once
//     Blender's own Z-up-to-Y-up axis conversion is folded in. A level built
//     by hand should reproduce this node shape, not assume "down" and skip
//     the rotation.
//   - Building 1's transform (translation, rotation, non-uniform scale) is
//     copied verbatim from a real Alt-D linked-duplicate export, for the same
//     reason: TestLevelGLBLightsAndTransforms checks this file's decoded
//     bytes against numbers computed by hand from a transform someone
//     actually measured, not one tuned to look nice.
//
// Run it from this directory: go run ./gen
//
// -big writes a SECOND, unrelated document -- buildBigDoc, not
// buildSettlementDoc -- for measuring examples/22-level -instanced
// (docs/agents/instancing.md): hundreds of buildings sharing one doc mesh
// and hundreds of lamp posts sharing another, nothing else. It never touches
// the committed level.glb; always give it its own -out pointed outside the
// repository:
//
//	go run ./gen -big -out /scratch/biglevel.glb -buildings 100 -lamps 400
package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"os"

	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/modeler"
)

// khrLightsPayload/khrLightRefPayload/spotPayload are the WRITE side of
// KHR_lights_punctual. github.com/qmuntal/gltf/ext/lightspunctual only
// implements decoding (it registers an Unmarshal in its own init(), which is
// what renderer.extractLights depends on) -- nothing in the module marshals
// TO the extension's wire shape, so this is that missing half, kept in sync
// with the identical structs in renderer/gltflights_test.go by
// TestLevelGLBLightsAndTransforms actually decoding this file with the real
// glTF decoder rather than by inspection.
type khrLightsPayload struct {
	Lights []gltfLightPayload `json:"lights"`
}

type khrLightRefPayload struct {
	Light int `json:"light"`
}

type gltfLightPayload struct {
	Type      string       `json:"type"`
	Name      string       `json:"name,omitempty"`
	Color     *[3]float64  `json:"color,omitempty"`
	Intensity *float64     `json:"intensity,omitempty"`
	Spot      *spotPayload `json:"spot,omitempty"`
	// Range is deliberately never set anywhere in this file -- see the
	// package comment: a real Blender export never writes it either.
}

type spotPayload struct {
	InnerConeAngle float64  `json:"innerConeAngle,omitempty"`
	OuterConeAngle *float64 `json:"outerConeAngle,omitempty"`
}

// Measured Blender 5.0.1 KHR_lights_punctual export values (see the package
// comment). Named here rather than inlined so
// TestLevelGLBLightsAndTransforms's expectations and this file's authored
// values are visibly the same numbers rather than two copies that happen to
// agree today.
const (
	spotIntensityCd  = 54351.4 // 1000 W Blender spot
	spotOuterConeRad = 0.6
	spotInnerConeRad = 0.42

	pointIntensityCd = 5435.1 * 5 // a 500 W-equivalent Blender point light

	// lightNodeRotX is every Blender light node's own rotation in this file:
	// -90 degrees about X, quaternion (x,y,z,w). It is what sends glTF's
	// local -Z light axis to world -Y (straight down) for a light node with
	// no other rotation above it in its parent chain -- see the package
	// comment and TestLevelGLBLightsAndTransforms.
)

var lightNodeRotX = [4]float64{-0.7071068, 0, 0, 0.7071068}

// identQuat is glTF's default rotation.
var identQuat = [4]float64{0, 0, 0, 1}

func main() {
	out := flag.String("out", "assets/level.glb", "output path for the generated GLB")
	big := flag.Bool("big", false, "write a large synthetic level (many repeated props) instead of the small settlement -- for measuring examples/22-level -instanced (docs/agents/instancing.md), NOT the committed default; always pass -out to a scratch path outside the repo when using this")
	buildings := flag.Int("buildings", 100, "-big only: number of buildings, all sharing one doc mesh")
	lamps := flag.Int("lamps", 400, "-big only: number of lamp posts, all sharing a second doc mesh")
	flag.Parse()

	var doc *gltf.Document
	if *big {
		doc = buildBigDoc(*buildings, *lamps)
	} else {
		doc = buildSettlementDoc()
	}

	if err := os.MkdirAll(dirOf(*out), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	if err := gltf.SaveBinary(doc, *out); err != nil {
		log.Fatalf("save %s: %v", *out, err)
	}
	log.Printf("wrote %s: %d nodes, %d meshes", *out, len(doc.Nodes), len(doc.Meshes))
}

// buildSettlementDoc builds the small hand-authored settlement this package
// exists to write to the committed examples/22-level/assets/level.glb --
// unchanged by issue #71, just pulled out of main so -big can build a
// different document without disturbing it. Every number here is unchanged
// from before this split; see the package comment for what each one is
// measured against.
func buildSettlementDoc() *gltf.Document {
	doc := &gltf.Document{
		Asset:              gltf.Asset{Version: "2.0", Generator: "glyphengine examples/22-level/gen"},
		ExtensionsUsed:     []string{"KHR_lights_punctual"},
		ExtensionsRequired: []string{"KHR_lights_punctual"},
	}

	groundMesh := boxMesh(doc, "GroundSlab", [3]float32{-15, -0.3, -15}, [3]float32{15, 0, 15})
	buildingMesh := boxMesh(doc, "Building", [3]float32{-0.5, 0, -0.5}, [3]float32{0.5, 1, 0.5})
	lampMesh := boxMesh(doc, "LampPost", [3]float32{-0.06, 0, -0.06}, [3]float32{0.06, 3, 0.06})

	doc.Materials = []*gltf.Material{
		pbrMaterial("Ground", [3]float64{0.25, 0.28, 0.22}, 0.9, 0),
		pbrMaterial("Stone", [3]float64{0.55, 0.50, 0.42}, 0.8, 0),
		pbrMaterial("DarkMetal", [3]float64{0.15, 0.15, 0.16}, 0.4, 0.6),
	}
	for _, m := range doc.Meshes[groundMesh].Primitives {
		m.Material = gltf.Index(0)
	}
	for _, m := range doc.Meshes[buildingMesh].Primitives {
		m.Material = gltf.Index(1)
	}
	for _, m := range doc.Meshes[lampMesh].Primitives {
		m.Material = gltf.Index(2)
	}

	// KHR_lights_punctual's document-level array. Index 0 is shared by all
	// four lamp posts -- a linked light DATA block, the same way Building 1
	// below is a linked MESH -- and index 1 is the one point light.
	doc.Extensions = gltf.Extensions{
		"KHR_lights_punctual": khrLightsPayload{Lights: []gltfLightPayload{
			{ // 0: the lamp posts' shared warm spot
				Type:      "spot",
				Name:      "LampSpot",
				Color:     &[3]float64{1.0, 0.70, 0.40},
				Intensity: gltf.Float(spotIntensityCd),
				Spot: &spotPayload{
					InnerConeAngle: spotInnerConeRad,
					OuterConeAngle: gltf.Float(spotOuterConeRad),
				},
			},
			{ // 1: the plaza's point light, a cooler white for contrast
				Type:      "point",
				Name:      "PlazaLantern",
				Color:     &[3]float64{0.85, 0.90, 1.0},
				Intensity: gltf.Float(pointIntensityCd),
			},
		}},
	}

	var roots []int

	// Ground -- identity transform, so this one node does NOT trip the
	// untransformed-mesh-node warning; every mesh node below it does, on
	// purpose (see docs/agents/models.md's "Loading a level" section). Still
	// carries "static"/"collider" extras like the buildings, so the level
	// has a walkable floor rather than every collider belonging to something
	// standing on empty air.
	roots = append(roots, addNode(doc, &gltf.Node{
		Name:   "Ground",
		Mesh:   gltf.Index(groundMesh),
		Extras: map[string]any{"static": true, "collider": "box"},
	}))

	// Buildings: four nodes instancing the SAME doc mesh with different TRS,
	// including a non-uniform scale and a rotation -- exactly the case
	// Model.NodeMeshes exists for, and ModelMesh.Node ("first owner only")
	// gets wrong. Building 1's transform is not tuned; it is the measured
	// Alt-D linked-duplicate export from the issue, copied verbatim, which
	// is also why it floats -- that translation was captured from a real
	// file, not chosen to sit flush with this scene's ground.
	roots = append(roots, addNode(doc, &gltf.Node{
		Name: "Building0", Mesh: gltf.Index(buildingMesh),
		Translation: [3]float64{-6, 0, -2}, Rotation: identQuat, Scale: [3]float64{3, 4, 3},
		Extras: map[string]any{"static": true, "collider": "box", "floors": 2},
	}))
	roots = append(roots, addNode(doc, &gltf.Node{
		Name: "Building1", Mesh: gltf.Index(buildingMesh),
		Translation: [3]float64{6, 1, -2},
		Rotation:    [4]float64{0, 0.2955, 0, 0.9553}, // measured Alt-D duplicate, verbatim
		Scale:       [3]float64{1, 1.5, 2},
		Extras:      map[string]any{"static": true, "collider": "box", "floors": 1},
	}))
	roots = append(roots, addNode(doc, &gltf.Node{
		Name: "Building2", Mesh: gltf.Index(buildingMesh),
		Translation: [3]float64{-6, 0, 6}, Rotation: quatY(15 * math.Pi / 180), Scale: [3]float64{2.5, 3, 2.5},
		Extras: map[string]any{"static": true, "collider": "box", "floors": 3},
	}))
	roots = append(roots, addNode(doc, &gltf.Node{
		Name: "Building3", Mesh: gltf.Index(buildingMesh),
		Translation: [3]float64{0, 0, 8}, Rotation: quatY(60 * math.Pi / 180), Scale: [3]float64{4, 2.5, 3},
		Extras: map[string]any{"static": true, "collider": "box", "floors": 4},
	}))

	// Lamp posts: four more instances of a second shared doc mesh, each with
	// a child "Light" node carrying the light's own placement -- the pole's
	// node is NOT the light's node, the same way an artist's fixture socket
	// in docs/agents/models.md is a node in its own right rather than a
	// property of the mesh it sits near.
	lampPositions := [4][2]float32{
		{10, 10}, {10, -10}, {-10, 10}, {-10, -10},
	}
	for i, p := range lampPositions {
		postIdx := addNode(doc, &gltf.Node{
			Name: nameN("LampPost", i), Mesh: gltf.Index(lampMesh),
			Translation: [3]float64{float64(p[0]), 0, float64(p[1])},
			Extras:      map[string]any{"static": true, "collider": "box"},
		})
		roots = append(roots, postIdx)

		lightIdx := addNode(doc, &gltf.Node{
			Name:        nameN("LampPost", i) + "_Light",
			Translation: [3]float64{0, 3, 0},
			Rotation:    lightNodeRotX,
			Extensions:  gltf.Extensions{"KHR_lights_punctual": khrLightRefPayload{Light: 0}},
		})
		doc.Nodes[postIdx].Children = append(doc.Nodes[postIdx].Children, lightIdx)
	}

	// The plaza's point light, on its own node -- same Blender-style
	// rotation as every other light node even though a point light has no
	// direction, because that is what the reference exporter actually
	// produces (see the package comment).
	roots = append(roots, addNode(doc, &gltf.Node{
		Name:        "PlazaLantern",
		Translation: [3]float64{0, 4, 0},
		Rotation:    lightNodeRotX,
		Extensions:  gltf.Extensions{"KHR_lights_punctual": khrLightRefPayload{Light: 1}},
	}))

	// Spawn: an empty node identified by ITS OWN extras rather than by a
	// name the example would have to know in advance -- the same pattern
	// the buildings' "static"/"collider" extras demonstrate, applied to
	// picking the camera's start point instead of a collider.
	roots = append(roots, addNode(doc, &gltf.Node{
		Name:        "Spawn",
		Translation: [3]float64{0, 0, -13},
		Extras:      map[string]any{"spawn": "player"},
	}))

	doc.Scene = gltf.Index(0)
	doc.Scenes = []*gltf.Scene{{Name: "Level", Nodes: roots}}
	return doc
}

// buildBigDoc writes a synthetic level for measuring examples/22-level
// -instanced (docs/agents/instancing.md): buildingCount buildings sharing
// ONE doc mesh and lampCount lamp posts sharing a second, every one tagged
// {"static": true, "collider": "box"} -- the exact shape -instanced's rule
// (examples/22-level/main.go's instancedGroupCandidate) merges into a single
// InstanceSet apiece, so the measurement is against a level big enough for
// the draw-call saving to be worth reading, not the eight shared props the
// committed settlement has.
//
// No lights and no spawn marker -- this exists to be loaded with -level and
// timed, not to be a scene anyone plays in, and every light in
// buildSettlementDoc's version is already measured on its own page
// (docs/agents/lights.md). Reusing boxMesh/pbrMaterial/addNode keeps this
// geometrically identical in kind to the committed level -- same box
// primitives, same material shape -- so the measurement is about instancing,
// not about drawing a different sort of mesh.
func buildBigDoc(buildingCount, lampCount int) *gltf.Document {
	doc := &gltf.Document{
		Asset: gltf.Asset{Version: "2.0", Generator: "glyphengine examples/22-level/gen -big"},
	}

	// Big enough to sit under every building and lamp post the grid below
	// places, with room to spare -- see the spacing chosen there. Both grids
	// are centred on the origin and sized to roughly the same footprint
	// (buildingSpan/lampSpan below) rather than pushed kilometres apart, so
	// the whole level fits inside the engine's default 500-unit far plane
	// (app.go's cfg.far) from a camera distance that still frames it -- a
	// scene built for measuring draw calls is worthless if half of it never
	// clears the frustum to be drawn at all.
	buildingSpacing, lampSpacing := 6.0, 2.5
	bSide := int(math.Ceil(math.Sqrt(float64(buildingCount))))
	lSide := int(math.Ceil(math.Sqrt(float64(lampCount))))
	buildingSpan := float64(bSide) * buildingSpacing
	lampSpan := float64(lSide) * lampSpacing
	span := float32(math.Max(buildingSpan, lampSpan)/2 + 10)
	groundMesh := boxMesh(doc, "GroundSlab", [3]float32{-span, -0.3, -span}, [3]float32{span, 0, span})
	buildingMesh := boxMesh(doc, "Building", [3]float32{-0.5, 0, -0.5}, [3]float32{0.5, 1, 0.5})
	lampMesh := boxMesh(doc, "LampPost", [3]float32{-0.06, 0, -0.06}, [3]float32{0.06, 3, 0.06})

	doc.Materials = []*gltf.Material{
		pbrMaterial("Ground", [3]float64{0.25, 0.28, 0.22}, 0.9, 0),
		pbrMaterial("Stone", [3]float64{0.55, 0.50, 0.42}, 0.8, 0),
		pbrMaterial("DarkMetal", [3]float64{0.15, 0.15, 0.16}, 0.4, 0.6),
	}
	for _, m := range doc.Meshes[groundMesh].Primitives {
		m.Material = gltf.Index(0)
	}
	for _, m := range doc.Meshes[buildingMesh].Primitives {
		m.Material = gltf.Index(1)
	}
	for _, m := range doc.Meshes[lampMesh].Primitives {
		m.Material = gltf.Index(2)
	}

	var roots []int
	roots = append(roots, addNode(doc, &gltf.Node{
		Name: "Ground", Mesh: gltf.Index(groundMesh),
		Extras: map[string]any{"static": true, "collider": "box"},
	}))

	// Buildings on a grid centred on the origin, height varied by position so
	// a few hundred of them read as a skyline rather than a carpet of
	// identical dots -- spawnInstancedGroup draws every one of these through
	// ONE InstanceSet when -instanced is on, so the placements have to
	// actually differ (position, and here height) or the measurement would
	// be drawing one prop's transform N times, which proves nothing about a
	// level whose props are not all identical.
	bOffset := buildingSpan / 2
	for i := 0; i < buildingCount; i++ {
		gx := float64(i%bSide)*buildingSpacing - bOffset
		gz := float64(i/bSide)*buildingSpacing - bOffset
		roots = append(roots, addNode(doc, &gltf.Node{
			Name: fmt.Sprintf("Building%d", i), Mesh: gltf.Index(buildingMesh),
			Translation: [3]float64{gx, 0, gz}, Rotation: identQuat,
			Scale:  [3]float64{3, 3 + float64(i%4), 3},
			Extras: map[string]any{"static": true, "collider": "box"},
		}))
	}

	// Lamp posts on their own grid, also centred on the origin -- deliberately
	// overlapping the buildings' footprint rather than pushed to one side.
	// This scene exists to be timed with -level, not screenshotted for its
	// own sake (docs/agents/instancing.md's numbers come from RenderStats,
	// not from looking at it), and keeping both groups over the same ground
	// slab is what lets a single moderate camera distance see all 500 props
	// at once within the far plane.
	lOffset := lampSpan / 2
	for i := 0; i < lampCount; i++ {
		gx := float64(i%lSide)*lampSpacing - lOffset
		gz := float64(i/lSide)*lampSpacing - lOffset
		roots = append(roots, addNode(doc, &gltf.Node{
			Name: fmt.Sprintf("LampPost%d", i), Mesh: gltf.Index(lampMesh),
			Translation: [3]float64{gx, 0, gz},
			Extras:      map[string]any{"static": true, "collider": "box"},
		}))
	}

	doc.Scene = gltf.Index(0)
	doc.Scenes = []*gltf.Scene{{Name: "BigLevel", Nodes: roots}}
	return doc
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}

func nameN(base string, i int) string {
	const digits = "0123456789"
	if i < 10 {
		return base + string(digits[i])
	}
	return base + string(digits[i/10]) + string(digits[i%10])
}

// addNode appends n to doc.Nodes and returns its index.
func addNode(doc *gltf.Document, n *gltf.Node) int {
	doc.Nodes = append(doc.Nodes, n)
	return len(doc.Nodes) - 1
}

// quatY returns the glTF quaternion (x,y,z,w) for a right-handed rotation of
// angle radians about +Y -- the same convention nodeLocalTransform (and
// mgl32.Quat) use, so a node built with this reproduces exactly what an
// artist rotating an object about Y in an editor would export.
func quatY(angle float64) [4]float64 {
	return [4]float64{0, math.Sin(angle / 2), 0, math.Cos(angle / 2)}
}

// pbrMaterial builds a plain, unlit-texture material: a base colour factor
// only, which is all this file needs since the level carries no textures
// (issue #66 does not ask for any).
func pbrMaterial(name string, color [3]float64, roughness, metallic float64) *gltf.Material {
	return &gltf.Material{
		Name: name,
		PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
			BaseColorFactor: &[4]float64{color[0], color[1], color[2], 1},
			RoughnessFactor: gltf.Float(roughness),
			MetallicFactor:  gltf.Float(metallic),
		},
	}
}

// boxMesh appends a one-primitive box mesh spanning [min, max] in local
// space -- 24 vertices (four per face, so every face keeps a flat normal
// rather than sharing smoothed corner normals) and 36 indices, wound
// counter-clockwise as seen from outside each face, which is glTF's own
// convention (LoadGLTF reverses winding to the engine's on the way in). It
// returns the new mesh's index into doc.Meshes.
func boxMesh(doc *gltf.Document, name string, min, max [3]float32) int {
	type face struct {
		normal [3]float32
		verts  [4][3]float32
	}
	x0, y0, z0 := min[0], min[1], min[2]
	x1, y1, z1 := max[0], max[1], max[2]
	faces := [6]face{
		{[3]float32{1, 0, 0}, [4][3]float32{{x1, y0, z0}, {x1, y1, z0}, {x1, y1, z1}, {x1, y0, z1}}},
		{[3]float32{-1, 0, 0}, [4][3]float32{{x0, y0, z0}, {x0, y0, z1}, {x0, y1, z1}, {x0, y1, z0}}},
		{[3]float32{0, 1, 0}, [4][3]float32{{x0, y1, z0}, {x0, y1, z1}, {x1, y1, z1}, {x1, y1, z0}}},
		{[3]float32{0, -1, 0}, [4][3]float32{{x0, y0, z0}, {x1, y0, z0}, {x1, y0, z1}, {x0, y0, z1}}},
		{[3]float32{0, 0, 1}, [4][3]float32{{x0, y0, z1}, {x1, y0, z1}, {x1, y1, z1}, {x0, y1, z1}}},
		{[3]float32{0, 0, -1}, [4][3]float32{{x0, y0, z0}, {x0, y1, z0}, {x1, y1, z0}, {x1, y0, z0}}},
	}

	positions := make([][3]float32, 0, 24)
	normals := make([][3]float32, 0, 24)
	indices := make([]uint16, 0, 36)
	for _, f := range faces {
		base := uint16(len(positions))
		for _, v := range f.verts {
			positions = append(positions, v)
			normals = append(normals, f.normal)
		}
		indices = append(indices,
			base+0, base+1, base+2,
			base+0, base+2, base+3,
		)
	}

	posIdx := modeler.WritePosition(doc, positions)
	normIdx := modeler.WriteNormal(doc, normals)
	idxIdx := modeler.WriteIndices(doc, indices)

	doc.Meshes = append(doc.Meshes, &gltf.Mesh{
		Name: name,
		Primitives: []*gltf.Primitive{{
			Attributes: gltf.PrimitiveAttributes{gltf.POSITION: posIdx, gltf.NORMAL: normIdx},
			Indices:    gltf.Index(idxIdx),
			Mode:       gltf.PrimitiveTriangles,
		}},
	})
	return len(doc.Meshes) - 1
}
