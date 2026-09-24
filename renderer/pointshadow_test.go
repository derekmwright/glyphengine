package renderer

import "testing"

// Before the point-shadow fix this submitted six single-instance draws, all
// at RenderObject.Model, instead of six draws of the three placements.
func TestPointShadowUsesInstanceTransforms(t *testing.T) {
	fx := buildFrame(1)
	h := &fakeHandles{next: 20000}
	m := fakeMesh(h, 0, 36, 0)
	s := fakeInstanceSet(h, m, 3, 0)
	fx.draws = []RenderObject{{Mesh: m, Instances: s, Color: [3]float32{1, 1, 1}}}
	fx.lighting.PointRange = 0
	if err := fx.record(&fakeDriver{}, 0); err != nil {
		t.Fatal(err)
	}
	off := fx.stats
	fx.lighting.PointRange = 10
	if err := fx.record(&fakeDriver{}, 0); err != nil {
		t.Fatal(err)
	}
	if draws, instances := fx.stats.DrawCalls-off.DrawCalls, fx.stats.Instances-off.Instances; draws != 6 || instances != 18 {
		t.Fatalf("point shadow added draws=%d instances=%d, want 6 and 18", draws, instances)
	}
}
