package renderer

import (
	"math"
	"testing"
)

// The set's bound is the only thing standing between a field of props and the
// frustum, and it fails in a way no still frame shows: too small, and the whole
// colony vanishes when the camera looks slightly away from its centre. These
// need no device -- recomputeBounds is arithmetic over the instance matrices.

func translate(x, y, z float32) [16]float32 {
	return [16]float32{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, x, y, z, 1}
}

func scaled(s, x, y, z float32) [16]float32 {
	return [16]float32{s, 0, 0, 0, 0, s, 0, 0, 0, 0, s, 0, x, y, z, 1}
}

// contains reports whether every instance's own bounding sphere sits inside the
// set's. That is the property the frustum test depends on.
func contains(t *testing.T, s *InstanceSet, instances []MeshInstance) {
	t.Helper()
	mc := s.Mesh.BoundCenter
	for i := range instances {
		m := &instances[i].Model
		cx := m[0]*mc[0] + m[4]*mc[1] + m[8]*mc[2] + m[12]
		cy := m[1]*mc[0] + m[5]*mc[1] + m[9]*mc[2] + m[13]
		cz := m[2]*mc[0] + m[6]*mc[1] + m[10]*mc[2] + m[14]
		sx := float32(math.Sqrt(float64(m[0]*m[0] + m[1]*m[1] + m[2]*m[2])))
		r := s.Mesh.BoundRadius * sx

		dx, dy, dz := cx-s.boundCenter[0], cy-s.boundCenter[1], cz-s.boundCenter[2]
		d := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
		if d+r > s.boundRadius+1e-3 {
			t.Errorf("instance %d reaches %.3f from the set centre but the set radius is %.3f",
				i, d+r, s.boundRadius)
		}
	}
}

func TestInstanceSetBoundContainsEveryInstance(t *testing.T) {
	s := &InstanceSet{Mesh: &Mesh{BoundRadius: 1}}
	instances := []MeshInstance{
		{Model: translate(0, 0, 0)},
		{Model: translate(50, 0, 0)},
		{Model: translate(-50, 0, 30)},
		{Model: translate(0, 12, -80)},
	}
	s.recomputeBounds(instances)

	if s.boundRadius <= 0 {
		t.Fatal("radius is zero, so the containment check below proves nothing")
	}
	contains(t, s, instances)
}

// Scale has to reach the bound too. A set of props scaled up 10x inside a
// radius computed from the unscaled mesh is the quiet version of this bug.
func TestInstanceSetBoundAccountsForScale(t *testing.T) {
	s := &InstanceSet{Mesh: &Mesh{BoundRadius: 1}}
	instances := []MeshInstance{
		{Model: scaled(1, 0, 0, 0)},
		{Model: scaled(10, 40, 0, 0)},
	}
	s.recomputeBounds(instances)
	contains(t, s, instances)

	// The far instance's own sphere reaches 40 + 10 = 50 from the origin, and
	// the set centre sits at x=20, so nothing smaller than 30 can hold it.
	// Ignoring scale would give 21.
	if s.boundRadius < 30 {
		t.Errorf("radius %.3f is too small to hold a 10x-scaled instance; scale was not applied", s.boundRadius)
	}
}

func TestInstanceSetBoundIsEmptyWithNoInstances(t *testing.T) {
	s := &InstanceSet{Mesh: &Mesh{BoundRadius: 1}}
	s.recomputeBounds(nil)
	if s.boundRadius != 0 {
		t.Errorf("an empty set has radius %.3f, want 0 so the frustum test skips it", s.boundRadius)
	}
}

// A mat4 vertex attribute occupies four consecutive locations, which is the
// detail that makes the instanced layout non-obvious: the tint lands at 8, not
// 5. Getting it wrong gives geometry that draws, which is why this is worth
// pinning rather than eyeballing.
func TestInstanceAttributeLayout(t *testing.T) {
	attrs := instanceAttributeDescriptions()

	want := []struct {
		location uint32
		binding  int
		offset   int
	}{
		{4, 1, 0}, {5, 1, 16}, {6, 1, 32}, {7, 1, 48}, // model, four vec4 rows
		{8, 1, 64}, // tint
	}

	perVertex := len(vertexAttributeDescriptions())
	if len(attrs) != perVertex+len(want) {
		t.Fatalf("got %d attributes, want %d per-vertex plus %d per-instance",
			len(attrs), perVertex, len(want))
	}

	for i, w := range want {
		got := attrs[perVertex+i]
		if int(got.Location) != int(w.location) || got.Binding != w.binding || got.Offset != w.offset {
			t.Errorf("instance attribute %d: location %d binding %d offset %d, want %d/%d/%d",
				i, got.Location, got.Binding, got.Offset, w.location, w.binding, w.offset)
		}
	}

	// The stride has to cover the last attribute, or the driver reads the next
	// instance's model matrix as this one's tint.
	last := want[len(want)-1]
	if meshInstanceSize < last.offset+16 {
		t.Errorf("stride %d does not cover the tint at offset %d", meshInstanceSize, last.offset)
	}
	if got := instanceBindingDescription().Stride; got != meshInstanceSize {
		t.Errorf("binding stride %d does not match MeshInstance size %d", got, meshInstanceSize)
	}
}
