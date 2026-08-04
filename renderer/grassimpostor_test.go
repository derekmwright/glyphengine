package renderer

import "testing"

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
