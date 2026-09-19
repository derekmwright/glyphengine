package renderer

import (
	"slices"
	"sync"
	"testing"
)

// The sort key exists to keep draws that bind the same thing next to each
// other, so the recorder binds a pipeline and a descriptor set once per group
// instead of once per draw. Its low bits used to be the descriptor set's
// address, which grouped correctly and ordered the groups by wherever the
// driver had allocated -- different in every process (issue #53).
//
// Replacing an address with a creation id is only safe if it groups exactly as
// well. These hold that: the key is equal for draws binding the same resource
// and different otherwise, which is the whole of what grouping needs.

func TestResourceIDsAreUniqueAndOrdered(t *testing.T) {
	a, b, c := newResourceID(), newResourceID(), newResourceID()
	if a == 0 {
		t.Error("ids start at 0, which is the id of a draw that binds nothing: untextured draws would group with textured ones")
	}
	if !(a < b && b < c) {
		t.Errorf("ids are not handed out in order: %d, %d, %d", a, b, c)
	}
}

// Nothing stops a game creating textures off the frame thread, and two
// resources sharing an id would merge two groups in the sort -- a silent
// correctness loss that only shows up as extra binds. Run with -race.
func TestResourceIDsAreUniqueUnderConcurrentCreation(t *testing.T) {
	const workers, each = 8, 200

	var wg sync.WaitGroup
	got := make([][]uint32, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			ids := make([]uint32, each)
			for i := range ids {
				ids[i] = newResourceID()
			}
			got[w] = ids
		}(w)
	}
	wg.Wait()

	seen := make(map[uint32]bool, workers*each)
	for _, ids := range got {
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("id %d was handed out twice", id)
			}
			seen[id] = true
		}
	}
}

func TestSortKeyGroupsByResourceNotByPointer(t *testing.T) {
	// Two textures that differ in nothing the key can see except their id, and
	// two Go values holding the same id. A key that still read something
	// per-object -- an address, a handle -- would tell the last pair apart.
	one := &Texture{id: 7}
	alsoOne := &Texture{id: 7}
	two := &Texture{id: 8}

	if a, b := (&RenderObject{Texture: one}).SortKey(), (&RenderObject{Texture: alsoOne}).SortKey(); a != b {
		t.Errorf("two draws on the same resource id got different keys (%016x, %016x): the key still reads the object, not the scene", a, b)
	}
	if a, b := (&RenderObject{Texture: one}).SortKey(), (&RenderObject{Texture: two}).SortKey(); a == b {
		t.Errorf("two different resources share key %016x: they would be merged into one group", a)
	}
	if untextured, textured := (&RenderObject{}).SortKey(), (&RenderObject{Texture: one}).SortKey(); untextured >= textured {
		t.Errorf("an untextured draw (%016x) no longer sorts ahead of a textured one (%016x)", untextured, textured)
	}
}

func TestSortKeySeparatesPipelineVariants(t *testing.T) {
	tex := &Texture{id: 3}
	mat := &Material{id: 3}

	keys := map[string]uint64{
		"plain":          (&RenderObject{Texture: tex}).SortKey(),
		"double":         (&RenderObject{Texture: tex, DoubleSided: true}).SortKey(),
		"skinned":        (&RenderObject{Texture: tex, Joints: &JointBuffer{}}).SortKey(),
		"material":       (&RenderObject{Material: mat}).SortKey(),
		"skinnedmateria": (&RenderObject{Material: mat, Joints: &JointBuffer{}}).SortKey(),
	}
	// The ids are equal on purpose: the variant bits, not the id, are what has
	// to keep these five apart, and equal ids are the case that proves it.
	seen := map[uint64]string{}
	for name, k := range keys {
		if other, dup := seen[k]; dup {
			t.Errorf("%s and %s share key %016x: two pipelines would be interleaved", name, other, k)
		}
		seen[k] = name
	}
}

// The property the key is FOR: sorting by it leaves exactly one contiguous run
// per (variant, resource), so the recorder binds each one once. This counts the
// runs, which is the number of binds plus one, and fails if the sort ever
// leaves a group split in two.
func TestSortingByKeyLeavesOneRunPerResource(t *testing.T) {
	var textures []*Texture
	for i := 0; i < 6; i++ {
		textures = append(textures, &Texture{id: newResourceID()})
	}

	// Ten draws per texture, deliberately interleaved on the way in: the order
	// a map walk hands them over is arbitrary, and this is the arbitrary order
	// standing in for it.
	var draws []RenderObject
	for i := 0; i < 10; i++ {
		for j, tex := range textures {
			draws = append(draws, RenderObject{
				Texture:     tex,
				DoubleSided: j%2 == 1,
				SortID:      uint64(i*len(textures) + j),
			})
		}
	}

	slices.SortFunc(draws, func(a, b RenderObject) int {
		switch ka, kb := a.SortKey(), b.SortKey(); {
		case ka < kb:
			return -1
		case ka > kb:
			return 1
		}
		switch {
		case a.SortID < b.SortID:
			return -1
		case a.SortID > b.SortID:
			return 1
		}
		return 0
	})

	runs := 1
	for i := 1; i < len(draws); i++ {
		if draws[i].SortKey() != draws[i-1].SortKey() {
			runs++
		}
	}
	if runs != len(textures) {
		t.Errorf("sorted %d draws over %d resources into %d runs, want %d: the sort is no longer batching by state",
			len(draws), len(textures), runs, len(textures))
	}
}
