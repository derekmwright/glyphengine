package renderer

import (
	"fmt"
	"math"
	"sort"
	"unsafe"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// JointBuffer holds joint matrices for one skinned entity, double-buffered per
// frame in flight so the CPU never writes a UBO the GPU may still be reading.
// Buffers are persistently mapped; new poses are staged CPU-side and flushed
// to the current frame's buffer inside DrawFrame, after its fence has signaled.
type JointBuffer struct {
	buffers        [maxFramesInFlight]core1_0.Buffer
	memories       [maxFramesInFlight]core1_0.DeviceMemory
	mapped         [maxFramesInFlight][]byte
	descriptorSets [maxFramesInFlight]core1_0.DescriptorSet

	staging     [MaxJoints]mgl32.Mat4
	stagedBytes int
	dirty       [maxFramesInFlight]bool
	jointCount  int
}

// CreateJointBuffer allocates per-frame host-visible UBOs for MaxJoints mat4
// matrices, persistently maps them, and creates descriptor sets bound to them.
// The buffer is registered with the renderer for staged uploads in DrawFrame.
func (r *Renderer) CreateJointBuffer() (*JointBuffer, error) {
	const bufSize = MaxJoints * 64 // 128 mat4s * 64 bytes each = 8192

	jb := &JointBuffer{}
	cleanup := func() {
		for i := 0; i < maxFramesInFlight; i++ {
			if jb.mapped[i] != nil {
				r.deviceDriver.UnmapMemory(jb.memories[i])
			}
			if jb.memories[i].Handle() != 0 {
				r.deviceDriver.FreeMemory(jb.memories[i], nil)
			}
			if jb.buffers[i].Handle() != 0 {
				r.deviceDriver.DestroyBuffer(jb.buffers[i], nil)
			}
		}
	}

	layouts := make([]core1_0.DescriptorSetLayout, maxFramesInFlight)
	for i := 0; i < maxFramesInFlight; i++ {
		buf, mem, err := r.createBuffer(bufSize,
			core1_0.BufferUsageUniformBuffer,
			core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent,
		)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("create joint UBO %d: %w", i, err)
		}
		jb.buffers[i] = buf
		jb.memories[i] = mem

		ptr, _, err := r.deviceDriver.MapMemory(mem, 0, bufSize, 0)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("map joint UBO %d: %w", i, err)
		}
		jb.mapped[i] = unsafe.Slice((*byte)(ptr), bufSize)
		layouts[i] = r.jointDescriptorSetLayout
	}

	sets, _, err := r.deviceDriver.AllocateDescriptorSets(core1_0.DescriptorSetAllocateInfo{
		DescriptorPool: r.descriptorPool,
		SetLayouts:     layouts,
	})
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("allocate joint descriptor sets: %w", err)
	}
	copy(jb.descriptorSets[:], sets)

	writes := make([]core1_0.WriteDescriptorSet, maxFramesInFlight)
	for i := 0; i < maxFramesInFlight; i++ {
		writes[i] = core1_0.WriteDescriptorSet{
			DstSet:         jb.descriptorSets[i],
			DstBinding:     0,
			DescriptorType: core1_0.DescriptorTypeUniformBuffer,
			BufferInfo: []core1_0.DescriptorBufferInfo{
				{
					Buffer: jb.buffers[i],
					Offset: 0,
					Range:  bufSize,
				},
			},
		}
	}
	if err := r.deviceDriver.UpdateDescriptorSets(writes, nil); err != nil {
		cleanup()
		return nil, fmt.Errorf("update joint descriptor sets: %w", err)
	}

	r.jointBuffers = append(r.jointBuffers, jb)
	return jb, nil
}

// DestroyJointBuffer unregisters the joint buffer and defers releasing its GPU
// resources until all in-flight frames have finished referencing them.
func (r *Renderer) DestroyJointBuffer(jb *JointBuffer) {
	for i, b := range r.jointBuffers {
		if b == jb {
			r.jointBuffers[i] = r.jointBuffers[len(r.jointBuffers)-1]
			r.jointBuffers = r.jointBuffers[:len(r.jointBuffers)-1]
			break
		}
	}
	r.DeferDestroy(func() {
		for i := 0; i < maxFramesInFlight; i++ {
			r.deviceDriver.UnmapMemory(jb.memories[i])
			r.deviceDriver.FreeMemory(jb.memories[i], nil)
			r.deviceDriver.DestroyBuffer(jb.buffers[i], nil)
		}
	})
}

// UploadJointMatrices stages joint matrices CPU-side; the data is copied to the
// current frame's UBO inside DrawFrame once that frame's fence has signaled.
func (r *Renderer) UploadJointMatrices(jb *JointBuffer, matrices []mgl32.Mat4) error {
	count := len(matrices)
	if count > MaxJoints {
		count = MaxJoints
	}
	copy(jb.staging[:count], matrices[:count])
	jb.stagedBytes = count * 64
	jb.jointCount = count
	for i := range jb.dirty {
		jb.dirty[i] = true
	}
	return nil
}

// flushJointUploads copies staged joint matrices into the given frame's UBOs.
// Called from DrawFrame after the frame's fence wait, so the GPU is guaranteed
// to be done reading these buffers.
func (r *Renderer) flushJointUploads(frame int) {
	for _, jb := range r.jointBuffers {
		if !jb.dirty[frame] || jb.stagedBytes == 0 {
			continue
		}
		src := unsafe.Slice((*byte)(unsafe.Pointer(&jb.staging[0])), jb.stagedBytes)
		copy(jb.mapped[frame][:jb.stagedBytes], src)
		jb.dirty[frame] = false
	}
}

// AnimScratch holds reusable buffers for animation sampling so the per-frame
// hot path allocates nothing. A single scratch may be shared across entities
// as long as sampling happens on one goroutine; it grows to fit the largest
// skeleton seen.
type AnimScratch struct {
	translations []mgl32.Vec3
	rotations    []mgl32.Quat
	scales       []mgl32.Vec3
	// Secondary TRS buffers, used to sample a second clip when cross-fading.
	translationsB []mgl32.Vec3
	rotationsB    []mgl32.Quat
	scalesB       []mgl32.Vec3
	locals        []mgl32.Mat4
	world         []mgl32.Mat4
	final         []mgl32.Mat4
}

// ensure grows the scratch buffers to hold at least n joints.
func (s *AnimScratch) ensure(n int) {
	if cap(s.translations) < n {
		s.translations = make([]mgl32.Vec3, n)
		s.rotations = make([]mgl32.Quat, n)
		s.scales = make([]mgl32.Vec3, n)
		s.translationsB = make([]mgl32.Vec3, n)
		s.rotationsB = make([]mgl32.Quat, n)
		s.scalesB = make([]mgl32.Vec3, n)
		s.locals = make([]mgl32.Mat4, n)
		s.world = make([]mgl32.Mat4, n)
		s.final = make([]mgl32.Mat4, n)
	}
	s.translations = s.translations[:n]
	s.rotations = s.rotations[:n]
	s.scales = s.scales[:n]
	s.translationsB = s.translationsB[:n]
	s.rotationsB = s.rotationsB[:n]
	s.scalesB = s.scalesB[:n]
	s.locals = s.locals[:n]
	s.world = s.world[:n]
	s.final = s.final[:n]
}

// ensureCache builds the skeleton's rest-pose TRS and parent-before-child
// traversal order on first use. The hierarchy never changes after load, so
// this runs once per skeleton instead of every frame.
func (sk *Skeleton) ensureCache() {
	if sk.order != nil {
		return
	}
	n := len(sk.Joints)
	sk.restT = make([]mgl32.Vec3, n)
	sk.restR = make([]mgl32.Quat, n)
	sk.restS = make([]mgl32.Vec3, n)
	for i, j := range sk.Joints {
		sk.restT[i], sk.restR[i], sk.restS[i] = decomposeTRS(j.LocalTransform)
	}

	sk.order = make([]int, 0, n)
	queue := append([]int(nil), sk.RootJoints...)
	for len(queue) > 0 {
		ji := queue[0]
		queue = queue[1:]
		sk.order = append(sk.order, ji)
		queue = append(queue, sk.Joints[ji].Children...)
	}
}

// sampleTRS fills the given per-joint translation/rotation/scale buffers by
// evaluating one clip at time t, starting from the skeleton's rest pose.
func sampleTRS(clip *AnimationClip, skeleton *Skeleton, t float32, outT, outS []mgl32.Vec3, outR []mgl32.Quat) {
	jointCount := len(skeleton.Joints)
	copy(outT, skeleton.restT)
	copy(outR, skeleton.restR)
	copy(outS, skeleton.restS)

	for i := range clip.Tracks {
		track := &clip.Tracks[i]
		ji := track.JointIndex
		if ji < 0 || ji >= jointCount {
			continue
		}
		switch track.Property {
		case PropertyTranslation:
			if len(track.Translations) > 0 {
				outT[ji] = sampleVec3Track(track, t)
			}
		case PropertyRotation:
			if len(track.Rotations) > 0 {
				outR[ji] = sampleQuatTrack(track, t)
			}
		case PropertyScale:
			if len(track.Scales) > 0 {
				outS[ji] = sampleVec3Track(track, t)
			}
		}
	}
}

// composeLocals recomposes T*R*S into local matrices.
func composeLocals(locals []mgl32.Mat4, tr, sc []mgl32.Vec3, ro []mgl32.Quat) {
	for i := range locals {
		t := mgl32.Translate3D(tr[i].X(), tr[i].Y(), tr[i].Z())
		r := ro[i].Mat4()
		s := mgl32.Scale3D(sc[i].X(), sc[i].Y(), sc[i].Z())
		locals[i] = t.Mul4(r).Mul4(s)
	}
}

// SampleAnimation evaluates an animation clip at the given time and returns
// per-joint local transforms in scratch.locals (valid until the next call
// with the same scratch).
func SampleAnimation(clip *AnimationClip, skeleton *Skeleton, t float32, scratch *AnimScratch) []mgl32.Mat4 {
	jointCount := len(skeleton.Joints)
	skeleton.ensureCache()
	scratch.ensure(jointCount)
	sampleTRS(clip, skeleton, t, scratch.translations, scratch.scales, scratch.rotations)
	composeLocals(scratch.locals, scratch.translations, scratch.scales, scratch.rotations)
	return scratch.locals
}

// SampleAnimationBlended cross-fades two clips: factor 0 = fully clipA@tA, 1 =
// fully clipB@tB. Translations/scales lerp, rotations nlerp (shortest-path),
// then the blended TRS is composed into scratch.locals.
func SampleAnimationBlended(clipA *AnimationClip, tA float32, clipB *AnimationClip, tB float32, factor float32, skeleton *Skeleton, scratch *AnimScratch) []mgl32.Mat4 {
	jointCount := len(skeleton.Joints)
	skeleton.ensureCache()
	scratch.ensure(jointCount)

	sampleTRS(clipA, skeleton, tA, scratch.translations, scratch.scales, scratch.rotations)
	sampleTRS(clipB, skeleton, tB, scratch.translationsB, scratch.scalesB, scratch.rotationsB)

	for i := 0; i < jointCount; i++ {
		scratch.translations[i] = lerpVec3(scratch.translations[i], scratch.translationsB[i], factor)
		scratch.scales[i] = lerpVec3(scratch.scales[i], scratch.scalesB[i], factor)
		scratch.rotations[i] = nlerpQuat(scratch.rotations[i], scratch.rotationsB[i], factor)
	}
	composeLocals(scratch.locals, scratch.translations, scratch.scales, scratch.rotations)
	return scratch.locals
}

// nlerpQuat is a normalized lerp between two quaternions, taking the
// shortest path. Cheaper than slerp and visually fine for short cross-fades.
func nlerpQuat(a, b mgl32.Quat, f float32) mgl32.Quat {
	if a.Dot(b) < 0 {
		b = b.Scale(-1)
	}
	r := mgl32.Quat{
		W: a.W + (b.W-a.W)*f,
		V: a.V.Add(b.V.Sub(a.V).Mul(f)),
	}
	return r.Normalize()
}

// ComputeJointMatrices walks the hierarchy and produces final joint matrices
// (world * inverseBindMatrix) ready for the shader, in scratch.final (valid
// until the next call with the same scratch). rootTransform is applied to
// root joints to account for armature-level transforms (e.g. scale/rotation
// from exporters that use centimeters or Z-up coordinates).
func ComputeJointMatrices(skeleton *Skeleton, localTransforms []mgl32.Mat4, rootTransform mgl32.Mat4, scratch *AnimScratch) []mgl32.Mat4 {
	jointCount := len(skeleton.Joints)
	skeleton.ensureCache()
	scratch.ensure(jointCount)
	world := scratch.world
	final := scratch.final

	// order is parent-before-child, so world transforms resolve in one pass.
	for _, ji := range skeleton.order {
		parent := skeleton.Joints[ji].ParentIndex
		if parent >= 0 {
			world[ji] = world[parent].Mul4(localTransforms[ji])
		} else {
			world[ji] = rootTransform.Mul4(localTransforms[ji])
		}
		final[ji] = world[ji].Mul4(skeleton.Joints[ji].InverseBindMatrix)
	}

	return final
}

func sampleVec3Track(track *AnimationTrack, t float32) mgl32.Vec3 {
	timestamps := track.Timestamps
	if len(timestamps) == 0 {
		return mgl32.Vec3{}
	}

	var vals []mgl32.Vec3
	switch track.Property {
	case PropertyTranslation:
		vals = track.Translations
	case PropertyScale:
		vals = track.Scales
	default:
		return mgl32.Vec3{}
	}

	if len(vals) == 0 {
		return mgl32.Vec3{}
	}

	// Clamp
	if t <= timestamps[0] {
		return vals[0]
	}
	if t >= timestamps[len(timestamps)-1] {
		return vals[len(vals)-1]
	}

	i := findKeyframe(timestamps, t)
	if track.Interpolation == InterpolationStep {
		return vals[i]
	}

	// Linear interpolation
	dt := timestamps[i+1] - timestamps[i]
	if dt <= 0 {
		return vals[i]
	}
	f := (t - timestamps[i]) / dt
	return lerpVec3(vals[i], vals[i+1], f)
}

func sampleQuatTrack(track *AnimationTrack, t float32) mgl32.Quat {
	timestamps := track.Timestamps
	vals := track.Rotations
	if len(timestamps) == 0 || len(vals) == 0 {
		return mgl32.QuatIdent()
	}

	if t <= timestamps[0] {
		return vals[0]
	}
	if t >= timestamps[len(timestamps)-1] {
		return vals[len(vals)-1]
	}

	i := findKeyframe(timestamps, t)
	if track.Interpolation == InterpolationStep {
		return vals[i]
	}

	dt := timestamps[i+1] - timestamps[i]
	if dt <= 0 {
		return vals[i]
	}
	f := (t - timestamps[i]) / dt
	return mgl32.QuatSlerp(vals[i], vals[i+1], f)
}

// findKeyframe returns the index of the keyframe just before time t.
func findKeyframe(timestamps []float32, t float32) int {
	n := len(timestamps)
	i := sort.Search(n, func(i int) bool { return timestamps[i] > t })
	if i > 0 {
		i--
	}
	if i >= n-1 {
		i = n - 2
	}
	return i
}

func decomposeTRS(m mgl32.Mat4) (mgl32.Vec3, mgl32.Quat, mgl32.Vec3) {
	// Translation from column 3
	trans := mgl32.Vec3{m[12], m[13], m[14]}

	// Scale from column lengths
	sx := mgl32.Vec3{m[0], m[1], m[2]}.Len()
	sy := mgl32.Vec3{m[4], m[5], m[6]}.Len()
	sz := mgl32.Vec3{m[8], m[9], m[10]}.Len()
	scale := mgl32.Vec3{sx, sy, sz}

	// Normalized rotation matrix
	var rot mgl32.Mat4
	if sx > 0 {
		rot[0] = m[0] / sx
		rot[1] = m[1] / sx
		rot[2] = m[2] / sx
	}
	if sy > 0 {
		rot[4] = m[4] / sy
		rot[5] = m[5] / sy
		rot[6] = m[6] / sy
	}
	if sz > 0 {
		rot[8] = m[8] / sz
		rot[9] = m[9] / sz
		rot[10] = m[10] / sz
	}
	rot[15] = 1

	q := mat4ToQuat(rot)
	return trans, q, scale
}

// mat4ToQuat extracts a quaternion from a pure rotation matrix.
func mat4ToQuat(m mgl32.Mat4) mgl32.Quat {
	// Shepperd's method
	trace := m[0] + m[5] + m[10]
	var q mgl32.Quat

	if trace > 0 {
		s := float32(math.Sqrt(float64(trace+1))) * 2
		q.W = 0.25 * s
		q.V = mgl32.Vec3{
			(m[6] - m[9]) / s,
			(m[8] - m[2]) / s,
			(m[1] - m[4]) / s,
		}
	} else if m[0] > m[5] && m[0] > m[10] {
		s := float32(math.Sqrt(float64(1+m[0]-m[5]-m[10]))) * 2
		q.W = (m[6] - m[9]) / s
		q.V = mgl32.Vec3{
			0.25 * s,
			(m[4] + m[1]) / s,
			(m[8] + m[2]) / s,
		}
	} else if m[5] > m[10] {
		s := float32(math.Sqrt(float64(1+m[5]-m[0]-m[10]))) * 2
		q.W = (m[8] - m[2]) / s
		q.V = mgl32.Vec3{
			(m[4] + m[1]) / s,
			0.25 * s,
			(m[9] + m[6]) / s,
		}
	} else {
		s := float32(math.Sqrt(float64(1+m[10]-m[0]-m[5]))) * 2
		q.W = (m[1] - m[4]) / s
		q.V = mgl32.Vec3{
			(m[8] + m[2]) / s,
			(m[9] + m[6]) / s,
			0.25 * s,
		}
	}
	return q.Normalize()
}

func lerpVec3(a, b mgl32.Vec3, t float32) mgl32.Vec3 {
	return mgl32.Vec3{
		a[0] + (b[0]-a[0])*t,
		a[1] + (b[1]-a[1])*t,
		a[2] + (b[2]-a[2])*t,
	}
}
