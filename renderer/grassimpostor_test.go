package renderer

import "testing"

// The push constant block is full, so the impostor pass borrows slots grass
// does not read. Borrowing one grass *does* read lights the billboards
// differently from the meshes they stand in for, and it is silent -- a valid
// float in a valid slot, no validation message, correct in daylight where the
// sun dominates and wrong at night where the ambient term is most of the light.
// That is exactly what shipped: the billboard size sat in ambient.xy and the
// far field glowed pale under a dark sky.
//
// Verified against the bug: moving any of the three to 52, 53 or 54 fails here.
func TestImpostorPushSlotsAvoidLighting(t *testing.T) {
	var pc [64]float32
	// Every field packLightingPC reads, non-zero, so "written" is visible.
	packLightingPC(&pc, SceneLighting{
		SunDir:        [3]float32{1, 1, 1},
		SunColor:      [3]float32{1, 1, 1},
		SunElevation:  1,
		PointPos:      [3]float32{1, 1, 1},
		PointRange:    1,
		PointColor:    [3]float32{1, 1, 1},
		Ambient:       [3]float32{1, 1, 1},
		CameraPos:     [3]float32{1, 1, 1},
		FogDensity:    1,
		FogHeight:     1,
		FogBaseHeight: 1,
		RealSunDir:    [3]float32{1, 1, 1},
	})
	// The grass pass adds two of its own on top: the wind clock, which
	// packLightingPC leaves as padding, and the LOD distances that grass.vert
	// culls and fades by, which it writes over the point light's position.
	pc[39] = 1
	pc[44], pc[45] = 1, 1
	// Slots 0..34 are the two matrices and the tint colour.
	for i := 0; i <= 34; i++ {
		pc[i] = 1
	}

	slots := map[string]int{
		"cell":   pcImpostorCell,
		"width":  pcImpostorWidth,
		"height": pcImpostorHeight,
	}
	for name, slot := range slots {
		if pc[slot] != 0 {
			t.Errorf("impostor %s is in push slot %d, which already carries scene data", name, slot)
		}
	}
	seen := map[int]string{}
	for name, slot := range slots {
		if other, dup := seen[slot]; dup {
			t.Errorf("impostor %s and %s share push slot %d", name, other, slot)
		}
		seen[slot] = name
	}
}

func tilesAt(dists ...float32) []tileDraw {
	out := make([]tileDraw, len(dists))
	for i, d := range dists {
		// FirstInstance identifies the tile, so a mix-up between variants is
		// visible in the assertion rather than just a wrong count.
		out[i] = tileDraw{dist2: d * d, tile: GrassTile{FirstInstance: int(d), Count: 1}}
	}
	return out
}

func TestSplitImpostorTilesSplitsAtTheDistance(t *testing.T) {
	visible := tilesAt(1, 10, 30, 60)

	mesh, scratch, variants := splitImpostorTiles(visible, 25*25, nil, nil, 0)

	if len(mesh) != 2 || mesh[0].tile.FirstInstance != 1 || mesh[1].tile.FirstInstance != 10 {
		t.Fatalf("near tiles = %v, want the two inside 25", mesh)
	}
	if len(variants) != 1 || variants[0].variant != 0 {
		t.Fatalf("variants = %v, want one entry for variant 0", variants)
	}
	far := scratch[variants[0].start:variants[0].end]
	if len(far) != 2 || far[0].tile.FirstInstance != 30 || far[1].tile.FirstInstance != 60 {
		t.Fatalf("far tiles = %v, want the two past 25", far)
	}
}

func TestSplitImpostorTilesRecordsNothingWhenAllTilesAreNear(t *testing.T) {
	visible := tilesAt(1, 10)

	mesh, scratch, variants := splitImpostorTiles(visible, 25*25, nil, nil, 0)

	if len(mesh) != 2 {
		t.Fatalf("near tiles = %d, want all 2", len(mesh))
	}
	// An empty range would still cost a vertex-buffer bind and a push constant
	// in the billboard pass.
	if len(variants) != 0 || len(scratch) != 0 {
		t.Fatalf("variants = %v, scratch = %v, want both empty", variants, scratch)
	}
}

// The draw loop sorts each variant's visible tiles into one shared scratch
// slice, resetting it per variant, and the billboard pass does not run until
// every variant has been through. Recording a subslice of that scratch means
// the next variant overwrites the tiles under it, and the earlier variants end
// up drawing the last variant's instance ranges against their own instance
// buffers -- which is silent: the tiles are in bounds, the layer sees nothing
// wrong, and the blades simply do not appear.
//
// Verified against the bug: with the previous `tiles: visible[split:]` this
// fails with variant 0's far tiles reading 300 and 600.
func TestSplitImpostorTilesSurvivesTheNextVariant(t *testing.T) {
	// One backing array, reused per variant exactly as the draw loop does.
	shared := make([]tileDraw, 0, 8)

	visible := append(shared[:0], tilesAt(1, 30, 60)...)
	_, scratch, variants := splitImpostorTiles(visible, 25*25, nil, nil, 0)

	visible = append(shared[:0], tilesAt(2, 300, 600)...)
	_, scratch, variants = splitImpostorTiles(visible, 25*25, scratch, variants, 1)

	if len(variants) != 2 {
		t.Fatalf("variants = %d, want 2", len(variants))
	}
	first := scratch[variants[0].start:variants[0].end]
	if len(first) != 2 || first[0].tile.FirstInstance != 30 || first[1].tile.FirstInstance != 60 {
		t.Errorf("variant 0 far tiles = %v, want 30 and 60 -- variant 1 overwrote them", first)
	}
	second := scratch[variants[1].start:variants[1].end]
	if len(second) != 2 || second[0].tile.FirstInstance != 300 || second[1].tile.FirstInstance != 600 {
		t.Errorf("variant 1 far tiles = %v, want 300 and 600", second)
	}
}
