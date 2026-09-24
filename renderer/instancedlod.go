package renderer

import (
	"fmt"
	"math"
	"slices"
	"time"
	"unsafe"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// LODLevel is a distance band, nearest first. MaxDistance is exclusive.
type LODLevel struct {
	Mesh        *Mesh
	MaxDistance float32
}

// InstanceSetLODDesc describes fixed-capacity, CPU-selected distance levels.
// FadeWidth is the full width of a transition centred on each MaxDistance.
// ShadowLevel selects the mesh bucket that casts shadows; -1 disables casting.
// Impostors never cast. Meshes and the atlas are borrowed, not owned by the set.
type InstanceSetLODDesc struct {
	Levels      []LODLevel
	Impostor    *ImpostorAtlas
	Capacity    int
	FadeWidth   float32
	ShadowLevel int
}

// InstanceSetLOD retains placements and per-frame GPU buckets. Use it only on
// the renderer thread, and stop drawing it before destroying it.
type InstanceSetLOD struct {
	owner                                  *Renderer
	levels                                 []LODLevel
	impostor                               *ImpostorAtlas
	placements                             []MeshInstance
	buckets                                []lodBucket
	counts                                 []int
	capacity, dropped, culled, shadowLevel int
	fadeWidth, radius                      float32
	destroyed                              bool
	prepared                               uint64
}

type lodBucket struct {
	instances []MeshInstance
	frames    [maxFramesInFlight]InstanceSet
	lo, hi    [3]float32
}

// Counts returns a read-only view of the last prepared frame's bucket counts,
// including the impostor when present. Culled includes placements dropped at
// capacity, outside the frustum, or beyond the last distance without an atlas.
// Cross-fading placements count in both adjacent buckets. Copy drawn to retain it.
func (s *InstanceSetLOD) Counts() (drawn []int, culled int) {
	if s == nil {
		return nil, 0
	}
	return s.counts, s.culled
}

func validateLOD(d InstanceSetLODDesc) error {
	if d.Capacity <= 0 || d.Capacity > int(^uint(0)>>1)/meshInstanceSize {
		return fmt.Errorf("LOD: invalid capacity %d", d.Capacity)
	}
	if len(d.Levels) == 0 {
		return fmt.Errorf("LOD: at least one mesh level is required")
	}
	if d.ShadowLevel < -1 || d.ShadowLevel >= len(d.Levels) {
		return fmt.Errorf("LOD: invalid shadow level %d", d.ShadowLevel)
	}
	if !finiteLOD(d.FadeWidth) || d.FadeWidth < 0 {
		return fmt.Errorf("LOD: invalid fade width")
	}
	var last float32
	for i, l := range d.Levels {
		if l.Mesh == nil || l.Mesh.destroyed {
			return fmt.Errorf("LOD: level %d has no live mesh", i)
		}
		if !finiteLOD(l.MaxDistance) || l.MaxDistance <= last {
			return fmt.Errorf("LOD: distances must be finite, positive and ascending")
		}
		if d.FadeWidth > l.MaxDistance-last {
			return fmt.Errorf("LOD: fade width overlaps distance bands")
		}
		last = l.MaxDistance
	}
	if d.Impostor != nil && d.Impostor.destroyed {
		return fmt.Errorf("LOD: atlas is destroyed")
	}
	return nil
}

func finiteLOD(x float32) bool { return !math.IsNaN(float64(x)) && !math.IsInf(float64(x), 0) }

func newLOD(d InstanceSetLODDesc) *InstanceSetLOD {
	n := len(d.Levels)
	if d.Impostor != nil {
		n++
	}
	s := &InstanceSetLOD{levels: slices.Clone(d.Levels), impostor: d.Impostor, capacity: d.Capacity,
		fadeWidth: d.FadeWidth, shadowLevel: d.ShadowLevel, placements: make([]MeshInstance, 0, d.Capacity),
		buckets: make([]lodBucket, n), counts: make([]int, n)}
	// One conservative radius for all levels. Bounds are centred at the model
	// translation by this API; authors must enclose the mesh about that origin.
	for _, l := range d.Levels {
		if l.Mesh.BoundRadius <= 0 {
			s.radius = 0
			break
		}
		s.radius = max(s.radius, l.Mesh.BoundRadius)
	}
	for i := range s.buckets {
		s.buckets[i].instances = make([]MeshInstance, 0, d.Capacity)
	}
	return s
}

// CreateInstanceSetLOD allocates a buffer per level per frame in flight.
// Placements beyond Capacity are dropped and included in Counts' culled total.
func (r *Renderer) CreateInstanceSetLOD(d InstanceSetLODDesc, instances []MeshInstance) (*InstanceSetLOD, error) {
	if err := validateLOD(d); err != nil {
		return nil, err
	}
	if d.Impostor != nil && d.Impostor.owner != r {
		return nil, fmt.Errorf("LOD: atlas belongs to another renderer")
	}
	s := newLOD(d)
	s.owner = r
	for i := range s.buckets {
		for f := range s.buckets[i].frames {
			b := &s.buckets[i].frames[f]
			b.capacity, b.lod, b.lodLevel = d.Capacity, s, i
			if i < len(s.levels) {
				b.Mesh = s.levels[i].Mesh
			}
			var err error
			b.buffer, b.memory, err = r.createBuffer(d.Capacity*meshInstanceSize, core1_0.BufferUsageVertexBuffer, core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent)
			if err != nil {
				s.destroy()
				return nil, fmt.Errorf("LOD buffer: %w", err)
			}
			b.mapped, _, err = r.deviceDriver.MapMemory(b.memory, 0, d.Capacity*meshInstanceSize, 0)
			if err != nil {
				s.destroy()
				return nil, fmt.Errorf("LOD map: %w", err)
			}
		}
	}
	r.lodSets = append(r.lodSets, s)
	r.UpdateInstanceSetLOD(s, instances)
	return s, nil
}

// UpdateInstanceSetLOD copies the full placement list. Tint.w is overwritten
// with renderer-owned coverage when the per-level buffers are filled.
func (r *Renderer) UpdateInstanceSetLOD(s *InstanceSetLOD, instances []MeshInstance) {
	if s == nil {
		return
	}
	if s.destroyed || s.owner != r {
		panic("LOD: update of destroyed or foreign set")
	}
	n := min(len(instances), s.capacity)
	s.placements = append(s.placements[:0], instances[:n]...)
	s.dropped = len(instances) - n
}

// DestroyInstanceSetLOD is nil-safe and idempotent. GPU storage is released
// only after all frames which could reference it have retired.
func (r *Renderer) DestroyInstanceSetLOD(s *InstanceSetLOD) {
	if s == nil || s.destroyed {
		return
	}
	if s.owner != r {
		panic("LOD: destroy of foreign set")
	}
	s.destroyed = true
	r.DeferDestroy(func() {
		s.destroy()
		if i := slices.Index(r.lodSets, s); i >= 0 {
			r.lodSets = slices.Delete(r.lodSets, i, i+1)
		}
	})
}

func (s *InstanceSetLOD) destroy() {
	for i := range s.buckets {
		for f := range s.buckets[i].frames {
			s.buckets[i].frames[f].destroy(s.owner.deviceDriver)
		}
	}
}

func instanceScale(m *[16]float32) float32 {
	return float32(math.Sqrt(float64(max(m[0]*m[0]+m[1]*m[1]+m[2]*m[2], m[4]*m[4]+m[5]*m[5]+m[6]*m[6], m[8]*m[8]+m[9]*m[9]+m[10]*m[10]))))
}

func (s *InstanceSetLOD) bucket(frustum Frustum, eye [3]float32) {
	for i := range s.buckets {
		s.buckets[i].instances = s.buckets[i].instances[:0]
	}
	s.culled = s.dropped
	for _, p := range s.placements {
		m := &p.Model
		x, y, z := m[12], m[13], m[14]
		radius := s.radius * instanceScale(m)
		if s.radius > 0 && !frustum.SphereInFrustum(x, y, z, radius) {
			s.culled++
			continue
		}
		dx, dy, dz := x-eye[0], y-eye[1], z-eye[2]
		d := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
		level := 0
		for level < len(s.levels) && d >= s.levels[level].MaxDistance {
			level++
		}
		// The last boundary also fades to empty when no impostor exists.
		boundary := -1
		if s.fadeWidth > 0 {
			if level < len(s.levels) && d >= s.levels[level].MaxDistance-s.fadeWidth/2 {
				boundary = level
			} else if level > 0 && d < s.levels[level-1].MaxDistance+s.fadeWidth/2 {
				boundary = level - 1
			}
		}
		if boundary >= 0 {
			t := (d - (s.levels[boundary].MaxDistance - s.fadeWidth/2)) / s.fadeWidth
			s.put(boundary, p, 1-t, radius)
			s.put(boundary+1, p, t, radius)
		} else if level < len(s.buckets) {
			s.put(level, p, 1, radius)
		} else {
			s.culled++
		}
	}
	for i := range s.buckets {
		s.counts[i] = len(s.buckets[i].instances)
	}
}

func (s *InstanceSetLOD) put(level int, p MeshInstance, fade, radius float32) {
	if level >= len(s.buckets) || fade <= 0 {
		return
	}
	p.Tint[3] = fade
	b := &s.buckets[level]
	for a := range b.lo {
		lo, hi := p.Model[12+a]-radius, p.Model[12+a]+radius
		if len(b.instances) == 0 {
			b.lo[a], b.hi[a] = lo, hi
		} else {
			b.lo[a] = min(b.lo[a], lo)
			b.hi[a] = max(b.hi[a], hi)
		}
	}
	b.instances = append(b.instances, p)
}

func (s *InstanceSetLOD) upload(frame int) {
	for i := range s.buckets {
		bucket := &s.buckets[i]
		b := &bucket.frames[frame]
		b.count = len(bucket.instances)
		copy(unsafe.Slice((*MeshInstance)(b.mapped), b.capacity), bucket.instances)
		// The bucket's sphere encloses the sphere AABB accumulated during
		// selection. No second walk over placements is needed for shadow culling.
		b.boundRadius = 0
		if b.count > 0 && s.radius > 0 {
			var diagonal float32
			for a := range b.boundCenter {
				b.boundCenter[a] = (bucket.lo[a] + bucket.hi[a]) / 2
				half := (bucket.hi[a] - bucket.lo[a]) / 2
				diagonal += half * half
			}
			b.boundRadius = float32(math.Sqrt(float64(diagonal)))
		}
	}
}

// prepareLOD runs after the slot's fence, independently of application passes.
// The expanded draw list is retained; ordinary instance sets are passed through.
func (r *Renderer) prepareLOD(draws []RenderObject, lighting SceneLighting, frame int) []RenderObject {
	r.lodGeneration++
	r.lastLODCull, r.lastLODUpload = 0, 0
	// Reserve for every band, even when the first frame sees only one. Later
	// camera movement must not allocate merely because another band survives.
	required := len(draws)
	for i := range draws {
		if s := draws[i].InstancesLOD; s != nil {
			required += len(s.buckets) - 1
		}
	}
	if cap(r.lodDraws) < required {
		r.lodDraws = make([]RenderObject, 0, required)
	}
	r.lodDraws = r.lodDraws[:0]
	frustum := ExtractFrustum(mgl32.Mat4(lighting.VP))
	for _, d := range draws {
		s := d.InstancesLOD
		if s == nil {
			r.lodDraws = append(r.lodDraws, d)
			continue
		}
		if d.Instances != nil {
			panic("RenderObject: Instances and InstancesLOD are mutually exclusive")
		}
		if s.destroyed || s.owner != r {
			panic("LOD: draw of destroyed or foreign set")
		}
		if s.impostor != nil && s.impostor.destroyed {
			panic("LOD: draw of destroyed atlas")
		}
		if s.prepared != r.lodGeneration {
			start := time.Now()
			s.bucket(frustum, lighting.CameraPos)
			r.lastLODCull += time.Since(start)
			start = time.Now()
			s.upload(frame)
			r.lastLODUpload += time.Since(start)
			s.prepared = r.lodGeneration
		}
		for i := range s.buckets {
			b := &s.buckets[i].frames[frame]
			if b.count == 0 {
				continue
			}
			out := d
			out.InstancesLOD = nil
			out.Instances = b
			out.Mesh = b.Mesh
			out.NoCastShadow = d.NoCastShadow || i != s.shadowLevel
			r.lodDraws = append(r.lodDraws, out)
		}
	}
	return r.lodDraws
}

// LastLODWork reports CPU culling/bucketing and upload time for the last frame.
func (r *Renderer) LastLODWork() (cull, upload time.Duration) { return r.lastLODCull, r.lastLODUpload }
