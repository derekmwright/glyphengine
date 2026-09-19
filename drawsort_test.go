package glyphengine

import (
	"math/rand"
	"slices"
	"testing"

	"github.com/derekmwright/glyphengine/renderer"
)

// What the sort costs, and why it is not a slices.SortFunc over the draw list.
//
// Giving the sort a total order (issue #53) is not free the way a tiebreak
// sounds like it should be. Before it, every draw in a field of identical props
// compared EQUAL, and pdqsort's all-equal case walks the slice once and stops --
// it was fast because it was not sorting. Making the order total makes it sort,
// and a RenderObject is 224 bytes, which slices.SortFunc copies into every
// comparison and moves on every swap.
//
// Measured on a 7900XTX box, best of three at -benchtime 500x, with the
// copy-in baseline already subtracted:
//
//	                    900 draws   4000 draws
//	objects-partial         13 us        59 us   what it used to cost
//	objects-total          203 us      1095 us   the obvious fix, in place
//	keys (this engine)      38 us       306 us   sorted through drawOrder
//
// So the honest statement of the cost is 59 -> 306 us on a field of 4000
// individual draws, not 59 -> 1095. objects-partial is cheap because it was
// not ordering: in a field of props over eight textures most pairs compare
// equal and pdqsort stops early. On what the examples actually render -- 25
// draws in 21-streetlights, 6 in 16-materials -- all three are noise.
//
// Run: go test -run XXX -bench BenchmarkDrawSort -benchtime 500x
func BenchmarkDrawSort(b *testing.B) {
	for _, n := range []int{900, 4000} {
		src := benchDrawList(n)
		buf := make([]renderer.RenderObject, len(src))
		b.Run(benchName(n, "copy-only"), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				copy(buf, src)
			}
		})
		b.Run(benchName(n, "keys"), func(b *testing.B) {
			e := &Engine{}
			for i := 0; i < b.N; i++ {
				copy(buf, src)
				e.sortDraws(buf, benchEye)
			}
		})
		b.Run(benchName(n, "objects-total"), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				copy(buf, src)
				slices.SortFunc(buf, benchTotalOrder)
			}
		})
		b.Run(benchName(n, "objects-partial"), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				copy(buf, src)
				slices.SortFunc(buf, benchPartialOrder)
			}
		})
	}
}

func benchName(n int, what string) string {
	if n == 900 {
		return "900/" + what
	}
	return "4000/" + what
}

var benchEye = [3]float32{0, 0, 0}

// benchDrawList is a field of props over a handful of textures, handed over in
// no order at all -- which is what a map walk hands over.
func benchDrawList(n int) []renderer.RenderObject {
	rng := rand.New(rand.NewSource(7))
	mesh := &renderer.Mesh{BoundRadius: 1}
	texs := make([]*renderer.Texture, 8)
	for i := range texs {
		texs[i] = &renderer.Texture{}
	}
	out := make([]renderer.RenderObject, n)
	for i := range out {
		out[i] = renderer.RenderObject{
			Mesh:    mesh,
			Texture: texs[rng.Intn(len(texs))],
			Model:   identityModel,
			SortID:  uint64(i),
		}
		out[i].Model[12] = rng.Float32() * 100
	}
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// benchTotalOrder is the obvious fix -- the same comparison sortDraws makes,
// applied to the RenderObjects rather than to a key. It is here to be measured,
// not used.
func benchTotalOrder(a, b renderer.RenderObject) int {
	at, bt := a.IsTranslucent(), b.IsTranslucent()
	if at != bt {
		if at {
			return 1
		}
		return -1
	}
	if at {
		switch da, db := a.ViewDepth(benchEye), b.ViewDepth(benchEye); {
		case da > db:
			return -1
		case da < db:
			return 1
		}
	} else {
		switch ka, kb := a.SortKey(), b.SortKey(); {
		case ka < kb:
			return -1
		case ka > kb:
			return 1
		}
	}
	switch {
	case a.SortID < b.SortID:
		return -1
	case a.SortID > b.SortID:
		return 1
	}
	return 0
}

// benchPartialOrder is what the engine did before issue #53: no tiebreak, so
// draws sharing a variant and a resource compare equal and keep whatever order
// the map walk gave them. Measured to say what the fix cost, not to be used.
func benchPartialOrder(a, b renderer.RenderObject) int {
	at, bt := a.IsTranslucent(), b.IsTranslucent()
	if at != bt {
		if at {
			return 1
		}
		return -1
	}
	if at {
		switch da, db := a.ViewDepth(benchEye), b.ViewDepth(benchEye); {
		case da > db:
			return -1
		case da < db:
			return 1
		default:
			return 0
		}
	}
	switch ka, kb := a.SortKey(), b.SortKey(); {
	case ka < kb:
		return -1
	case ka > kb:
		return 1
	default:
		return 0
	}
}
