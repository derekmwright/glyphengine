package renderer

import "github.com/go-gl/mathgl/mgl32"

const MaxJoints = 128

// Joint represents a single bone in a skeleton hierarchy.
type Joint struct {
	Name              string
	NodeIndex         int
	ParentIndex       int // -1 if root
	Children          []int
	InverseBindMatrix mgl32.Mat4
	LocalTransform    mgl32.Mat4
}

// Skeleton holds the joint hierarchy and index mappings for a skinned model.
type Skeleton struct {
	Joints      []Joint
	RootJoints  []int
	NodeToJoint map[int]int // maps glTF node index -> joint index

	// Lazily-built sampling caches (see ensureCache): rest-pose TRS per joint
	// and a parent-before-child traversal order replacing per-frame BFS.
	restT []mgl32.Vec3
	restR []mgl32.Quat
	restS []mgl32.Vec3
	order []int
}

// TRSProperty identifies which transform property an animation track targets.
type TRSProperty int

const (
	PropertyTranslation TRSProperty = iota
	PropertyRotation
	PropertyScale
)

// Interpolation defines how keyframes are interpolated.
type Interpolation int

const (
	InterpolationLinear Interpolation = iota
	InterpolationStep
	InterpolationCubicSpline
)

// AnimationTrack holds keyframe data for a single joint property.
type AnimationTrack struct {
	JointIndex    int
	Property      TRSProperty
	Interpolation Interpolation
	Timestamps    []float32
	Translations  []mgl32.Vec3
	Rotations     []mgl32.Quat
	Scales        []mgl32.Vec3
}

// AnimationClip holds a named animation with multiple tracks.
type AnimationClip struct {
	Name     string
	Duration float32
	Tracks   []AnimationTrack
}

// SkinnedModel extends Model with skeleton and animation data.
type SkinnedModel struct {
	Model
	Skeleton      *Skeleton
	Animations    []AnimationClip
	RootTransform mgl32.Mat4 // armature root transform (parent chain above joints)
}
