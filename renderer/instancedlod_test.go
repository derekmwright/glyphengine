package renderer

import (
	"math"
	"slices"
	"testing"
	"unsafe"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/vkngwrapper/core/v3/core1_0"
)

func lodPlacement(x, y, z float32) MeshInstance {
	return MeshInstance{Model: [16]float32(mgl32.Translate3D(x, y, z)), Tint: [4]float32{0.2, 0.4, 0.8, 99}}
}

func lodTestSet(n int, atlas bool) *InstanceSetLOD {
	m := &Mesh{BoundRadius: 1}
	d := InstanceSetLODDesc{Levels: []LODLevel{{m, 10}, {m, 20}, {m, 30}}, Capacity: n, FadeWidth: 4, ShadowLevel: 1}
	if atlas {
		d.Impostor = &ImpostorAtlas{}
	}
	return newLOD(d)
}

func TestLODBoundariesAndCoverage(t *testing.T) {
	s := lodTestSet(1, true)
	for _, tc := range []struct {
		distance float32
		counts   []int
		fade     []float32
	}{
		{0, []int{1, 0, 0, 0}, []float32{1, 0, 0, 0}},
		{8, []int{1, 0, 0, 0}, []float32{1, 0, 0, 0}},
		{9, []int{1, 1, 0, 0}, []float32{.75, .25, 0, 0}},
		{10, []int{1, 1, 0, 0}, []float32{.5, .5, 0, 0}},
		{11, []int{1, 1, 0, 0}, []float32{.25, .75, 0, 0}},
		{12, []int{0, 1, 0, 0}, []float32{0, 1, 0, 0}},
		{20, []int{0, 1, 1, 0}, []float32{0, .5, .5, 0}},
		{30, []int{0, 0, 1, 1}, []float32{0, 0, .5, .5}},
		{100, []int{0, 0, 0, 1}, []float32{0, 0, 0, 1}},
	} {
		s.placements = append(s.placements[:0], lodPlacement(tc.distance, 0, 0))
		s.bucket(Frustum{}, [3]float32{})
		counts, culled := s.Counts()
		if !slices.Equal(counts, tc.counts) || culled != 0 {
			t.Fatalf("distance %g: counts %v culled %d", tc.distance, counts, culled)
		}
		for i, b := range s.buckets {
			if len(b.instances) > 0 {
				if got := b.instances[0].Tint; got != [4]float32{.2, .4, .8, tc.fade[i]} {
					t.Errorf("distance %g level %d tint=%v", tc.distance, i, got)
				}
			}
		}
		if s.placements[0].Tint[3] != 99 {
			t.Fatal("selection mutated caller placement")
		}
	}
	s.fadeWidth = 0
	s.placements = []MeshInstance{lodPlacement(10, 0, 0)}
	s.bucket(Frustum{}, [3]float32{})
	if !slices.Equal(s.counts, []int{0, 1, 0, 0}) {
		t.Fatal("MaxDistance must be exclusive", s.counts)
	}
}

func TestLODCullScaleCapacityAndNoBounds(t *testing.T) {
	r := &Renderer{}
	s := lodTestSet(4, false)
	s.owner = r
	s.fadeWidth = 0
	// Only x>=0 is bounded. Translation, not mesh BoundCenter, is the centre.
	f := Frustum{Planes: [6][4]float32{{1, 0, 0, 0}}}
	p := []MeshInstance{lodPlacement(3, 0, 0), lodPlacement(-2, 0, 0), lodPlacement(-2, 0, 0), lodPlacement(40, 0, 0), lodPlacement(1, 0, 0)}
	p[2].Model[5] = 3 // largest scale on Y still expands the sphere along X
	r.UpdateInstanceSetLOD(s, p)
	p[0].Model[12] = 999 // copied by Update, not aliased
	s.bucket(f, [3]float32{})
	if !slices.Equal(s.counts, []int{2, 0, 0}) || s.culled != 3 {
		t.Fatalf("counts=%v culled=%d want [2 0 0],3", s.counts, s.culled)
	}
	s.radius = 0
	s.bucket(f, [3]float32{})
	if !slices.Equal(s.counts, []int{3, 0, 0}) || s.culled != 2 {
		t.Fatal("missing bounds must skip frustum culling", s.counts, s.culled)
	}
	r.UpdateInstanceSetLOD(s, nil)
	s.bucket(f, [3]float32{})
	if s.culled != 0 || !slices.Equal(s.counts, []int{0, 0, 0}) {
		t.Fatal("empty update did not clear counts")
	}
}

func TestLODDescriptorValidation(t *testing.T) {
	m := &Mesh{BoundRadius: 1}
	valid := InstanceSetLODDesc{Levels: []LODLevel{{m, 10}, {m, 20}}, Capacity: 2, FadeWidth: 6, ShadowLevel: -1}
	if err := validateLOD(valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*InstanceSetLODDesc){
		func(d *InstanceSetLODDesc) { d.Capacity = 0 }, func(d *InstanceSetLODDesc) { d.Levels = nil },
		func(d *InstanceSetLODDesc) { d.Levels[0].Mesh = nil }, func(d *InstanceSetLODDesc) { d.Levels[1].MaxDistance = 10 },
		func(d *InstanceSetLODDesc) { d.Levels[0].MaxDistance = float32(math.NaN()) },
		func(d *InstanceSetLODDesc) { d.FadeWidth = -1 }, func(d *InstanceSetLODDesc) { d.FadeWidth = 11 },
		func(d *InstanceSetLODDesc) { d.ShadowLevel = 2 }, func(d *InstanceSetLODDesc) { d.Impostor = &ImpostorAtlas{destroyed: true} },
	} {
		d := valid
		d.Levels = slices.Clone(valid.Levels)
		mutate(&d)
		if validateLOD(d) == nil {
			t.Errorf("accepted invalid descriptor %+v", d)
		}
	}
}

// withLOD adds all mesh buckets and an impostor to the existing recorder fixture,
// without renumbering any of the handles in its historical golden stream.
func withLOD(fx *frame, n int) (*Renderer, []RenderObject) {
	h := &fakeHandles{next: 10000}
	r := &Renderer{}
	for i := range r.lodPipelines {
		r.lodPipelines[i] = h.pipeline()
	}
	s := lodTestSet(n, true)
	s.fadeWidth = 0
	s.owner = r
	s.impostor.owner = r
	s.impostor.radius = 1
	s.impostor.size = 128
	s.impostor.texture = *fakeTexture(h)
	for i := range s.levels {
		s.levels[i].Mesh = fakeMesh(h, 12, 36, 1)
	}
	for i := 0; i < n; i++ {
		s.placements = append(s.placements, lodPlacement(float32((i%4)*10), 0, 0))
	}
	for i := range s.buckets {
		for f := range s.buckets[i].frames {
			b := &s.buckets[i].frames[f]
			b.capacity = n
			b.buffer = h.buffer()
			b.lod = s
			b.lodLevel = i
			data := make([]MeshInstance, n)
			b.mapped = unsafe.Pointer(&data[0])
			if i < len(s.levels) {
				b.Mesh = s.levels[i].Mesh
			}
		}
	}
	r.lodSets = []*InstanceSetLOD{s}
	input := append(slices.Clone(fx.draws), RenderObject{InstancesLOD: s, Color: [3]float32{1, 1, 1}})
	return r, input
}

func TestLODFrameSlotsAndAllocations(t *testing.T) {
	for _, n := range []int{40, 160} {
		fx := buildFrame(n)
		r, input := withLOD(fx, n)
		d := &fakeDriver{}
		for f := 0; f < maxFramesInFlight; f++ {
			fx.draws = r.prepareLOD(input, fx.lighting, f)
			if err := fx.record(d, f); err != nil {
				t.Fatal(err)
			}
		}
		s := r.lodSets[0]
		if !slices.Equal(s.counts, []int{n / 4, n / 4, n / 4, n / 4}) {
			t.Fatalf("fixture culled a LOD path: %v", s.counts)
		}
		if allocs := testing.AllocsPerRun(20, func() {
			fx.draws = r.prepareLOD(input, fx.lighting, 0)
			if err := fx.record(d, 0); err != nil {
				panic(err)
			}
		}); allocs != 0 {
			t.Fatalf("%d placements: %g allocations", n, allocs)
		}
		before := *(*MeshInstance)(s.buckets[0].frames[1].mapped)
		s.placements[0].Model[12] = 1
		r.prepareLOD(input, fx.lighting, 0)
		if after := *(*MeshInstance)(s.buckets[0].frames[1].mapped); after != before {
			t.Fatal("upload overwrote the other frame's in-flight buffer")
		}
		if after := *(*MeshInstance)(s.buckets[0].frames[0].mapped); after == before {
			t.Fatal("upload did not reach the current frame")
		}
	}
}

func TestLODShadowSelectionAndPointDraws(t *testing.T) {
	fx := buildFrame(40)
	r, input := withLOD(fx, 4)
	draws := r.prepareLOD(input[len(input)-1:], fx.lighting, 0)
	var stats RenderStats
	recordInstancedShadow(&fakeDriver{}, &stats, fx.cmdBuf, fx.shadow.instancedPipeline, fx.shadow.pipelineLayout, core1_0.Viewport{}, core1_0.Rect2D{}, draws, [16]float32{}, Frustum{}, &fx.scratch)
	if stats.DrawCalls != 1 || stats.Instances != 1 || stats.Triangles != 12 {
		t.Fatalf("selected shadow bucket: %+v", stats)
	}
	for i := range draws {
		draws[i].NoCastShadow = true
	}
	stats = RenderStats{}
	recordInstancedShadow(&fakeDriver{}, &stats, fx.cmdBuf, fx.shadow.instancedPipeline, fx.shadow.pipelineLayout, core1_0.Viewport{}, core1_0.Rect2D{}, draws, [16]float32{}, Frustum{}, &fx.scratch)
	if stats.DrawCalls != 0 {
		t.Fatal("NoCastShadow ignored")
	}
}

func TestLODDestroyDeferredAndStaleDraw(t *testing.T) {
	r := &Renderer{}
	s := newLOD(InstanceSetLODDesc{Levels: []LODLevel{{&Mesh{}, 10}}, Capacity: 1})
	s.owner = r
	r.lodSets = []*InstanceSetLOD{s}
	r.DestroyInstanceSetLOD(s)
	r.DestroyInstanceSetLOD(s)
	r.DestroyInstanceSetLOD(nil)
	if r.ResourceCounts().LODSets != 1 || len(r.deferredDestroys) != 1 {
		t.Fatal("destroy must retain resources until the deferred callback")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("stale draw did not panic")
			}
		}()
		r.prepareLOD([]RenderObject{{InstancesLOD: s}}, SceneLighting{}, 0)
	}()
	r.flushAllDeferred()
	if len(r.lodSets) != 0 {
		t.Fatal("destroy failed to deregister")
	}
}
