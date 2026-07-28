package glyphengine

import (
	"math"
	"strings"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/ecs"
	"github.com/derekmwright/glyphengine/renderer"
)

// DefaultBlendDuration is the cross-fade time in seconds applied when a clip
// changes and AnimationState.BlendDuration is zero.
const DefaultBlendDuration = float32(0.15)

// AnimationState holds skeletal animation playback state for one entity.
//
// This is playback only — which clip, where in it, how fast. Choosing the clip
// is game policy (an idle/walk/run speed ladder, a jump state machine, a death
// override) and belongs in a game-side system that calls PlayLoop and
// PlayOnce. The engine deliberately ships no locomotion controller.
type AnimationState struct {
	Clip    int     // index into SkeletonRef.Model.Animations
	Time    float32 // seconds into the clip
	Speed   float32 // playback rate multiplier
	Loop    bool
	Playing bool

	// BlendDuration is the cross-fade time in seconds used on the next clip
	// change. Zero means DefaultBlendDuration.
	BlendDuration float32

	// Cross-fade: while blendTime < blendDur, the sampled pose is a blend from
	// (prevClip @ prevTime) into (Clip @ Time). Set on clip change.
	prevClip  int
	prevTime  float32
	blendTime float32
	blendDur  float32
}

// SkeletonRef links an entity to a skinned model and its GPU joint buffer.
type SkeletonRef struct {
	Model       *renderer.SkinnedModel
	JointBuffer *renderer.JointBuffer
	Skinned     bool
}

// FindClip returns the index of the first animation clip whose name matches
// name, case-insensitively, or -1 if there is none.
func FindClip(model *renderer.SkinnedModel, name string) int {
	if model == nil {
		return -1
	}
	for i, clip := range model.Animations {
		if strings.EqualFold(clip.Name, name) {
			return i
		}
	}
	return -1
}

// FindClipAny returns the index of the first clip matching any of names, tried
// in order, or -1 if none match. Useful for models that spell a clip
// differently: FindClipAny(m, "sprint", "run", "walk").
func FindClipAny(model *renderer.SkinnedModel, names ...string) int {
	for _, n := range names {
		if i := FindClip(model, n); i >= 0 {
			return i
		}
	}
	return -1
}

// startBlend captures the current pose as the source of a cross-fade into the
// clip that is about to be set.
func (a *AnimationState) startBlend() {
	dur := a.BlendDuration
	if dur <= 0 {
		dur = DefaultBlendDuration
	}
	a.prevClip = a.Clip
	a.prevTime = a.Time
	a.blendTime = 0
	a.blendDur = dur
}

// PlayLoop plays clip on a loop at the given playback speed, cross-fading from
// the current pose. Playback time is reset only on an actual clip change, so
// calling it every frame with the same clip does not stutter. A negative clip
// index is ignored, which makes FindClip's -1 miss safe to pass straight in.
func (a *AnimationState) PlayLoop(clip int, speed float32) {
	if clip < 0 {
		return
	}
	if a.Clip != clip {
		a.startBlend()
		a.Clip = clip
		a.Time = 0
	}
	a.Speed = speed
	a.Loop = true
	a.Playing = true
}

// PlayOnce plays clip from the start at normal speed without looping,
// cross-fading from the current pose on a clip change. Playing goes false when
// the clip reaches its end. A negative clip index is ignored.
func (a *AnimationState) PlayOnce(clip int) {
	if clip < 0 {
		return
	}
	if a.Clip != clip {
		a.startBlend()
	}
	a.Clip = clip
	a.Time = 0
	a.Speed = 1.0
	a.Loop = false
	a.Playing = true
}

// Stop freezes playback at the current pose.
func (a *AnimationState) Stop() { a.Playing = false }

// Finished reports whether a non-looping clip has played to its end.
func (a *AnimationState) Finished() bool { return !a.Playing && !a.Loop }

// PlayClip looks up a clip by name on the entity's skinned model and starts it.
// It reports false if the entity has no AnimationState and SkeletonRef, or if
// the model has no clip by that name.
func (s *Scene) PlayClip(entity ecs.Entity, name string, loop bool) bool {
	anim, ok := s.C.AnimationState.Get(entity)
	if !ok {
		return false
	}
	skel, ok := s.C.SkeletonRef.Get(entity)
	if !ok {
		return false
	}
	clip := FindClip(skel.Model, name)
	if clip < 0 {
		return false
	}
	if loop {
		anim.PlayLoop(clip, 1.0)
	} else {
		anim.PlayOnce(clip)
	}
	return true
}

// TickAnimations advances and uploads skeletal animations for every animated
// entity. dt is the real frame delta: animation is sampled once per rendered
// frame rather than per simulation tick, so poses stay smooth independently of
// the tick rate.
func (e *Engine) TickAnimations(dt float32) {
	ecs.Query2(e.C.AnimationState, e.C.SkeletonRef, func(entity ecs.Entity, anim *AnimationState, skel *SkeletonRef) {
		if !anim.Playing || skel.Model == nil || skel.Model.Skeleton == nil || skel.JointBuffer == nil {
			return
		}

		clips := skel.Model.Animations
		if anim.Clip < 0 || anim.Clip >= len(clips) {
			return
		}
		clip := &clips[anim.Clip]

		// Advance time.
		anim.Time += dt * anim.Speed
		if clip.Duration > 0 {
			if anim.Loop {
				anim.Time = float32(math.Mod(float64(anim.Time), float64(clip.Duration)))
				if anim.Time < 0 {
					anim.Time += clip.Duration
				}
			} else if anim.Time > clip.Duration {
				anim.Time = clip.Duration
				anim.Playing = false
			}
		}

		// Sample — cross-fading from the previous clip while a blend is active
		// — and upload. Scratch is reused across entities; single-threaded.
		var locals []mgl32.Mat4
		if anim.blendDur > 0 && anim.prevClip >= 0 && anim.prevClip < len(clips) && anim.prevClip != anim.Clip {
			anim.blendTime += dt
			f := anim.blendTime / anim.blendDur
			if f >= 1 {
				f = 1
				anim.blendDur = 0 // blend complete
			}
			prev := &clips[anim.prevClip]
			anim.prevTime += dt // outgoing clip keeps playing; clamps past its end
			if prev.Duration > 0 && anim.prevTime > prev.Duration {
				anim.prevTime = prev.Duration
			}
			locals = renderer.SampleAnimationBlended(prev, anim.prevTime, clip, anim.Time, f, skel.Model.Skeleton, &e.animScratch)
		} else {
			anim.blendDur = 0
			locals = renderer.SampleAnimation(clip, skel.Model.Skeleton, anim.Time, &e.animScratch)
		}
		finals := renderer.ComputeJointMatrices(skel.Model.Skeleton, locals, skel.Model.RootTransform, &e.animScratch)
		e.renderer.UploadJointMatrices(skel.JointBuffer, finals)
	})
}
