---
id: skeletal-animation
title: Play skeletal animation clips
summary: >
  Attach a skinned model, start clips by name with automatic cross-fade, and
  let the engine sample and upload joint matrices once per frame.
capability: animation
status: stable
since: v0.2.0
api:
  - glyphengine.AnimationState
  - glyphengine.SkeletonRef
  - glyphengine.FindClip
  - glyphengine.FindClipAny
  - glyphengine.AnimationState.PlayLoop
  - glyphengine.AnimationState.PlayOnce
  - glyphengine.AnimationState.Stop
  - glyphengine.AnimationState.Finished
  - glyphengine.Scene.PlayClip
  - glyphengine.Engine.TickAnimations
  - glyphengine.DefaultBlendDuration
  - renderer.Renderer.LoadGLTFSkinned
  - renderer.Renderer.CreateJointBuffer
requires:
  - cgo
  - vulkan-runtime
assets: user-supplied
verified: 2026-07-28
---

# Play skeletal animation clips

```go
model, err := e.Renderer().LoadGLTFSkinned(assetsFS, "models/character.glb")
if err != nil {
	return err
}
joints, err := e.Renderer().CreateJointBuffer()
if err != nil {
	return err
}

ent := e.Spawn()
e.C.Transform.Set(ent, &glyphengine.Transform{Scale: mgl32.Vec3{1, 1, 1}})
e.C.MeshRef.Set(ent, &glyphengine.MeshRef{Mesh: model.Mesh})
e.C.SkeletonRef.Set(ent, &glyphengine.SkeletonRef{
	Model:       model,
	JointBuffer: joints,
	Skinned:     true,
})
e.C.AnimationState.Set(ent, &glyphengine.AnimationState{})

e.PlayClip(ent, "idle", true)
```

`Engine.Run` calls `TickAnimations` once per rendered frame, which advances
time, samples the pose, blends if a cross-fade is active, and uploads the joint
matrices. You never call it yourself.

## Starting clips

`Scene.PlayClip(entity, name, loop)` is the convenient form. For per-frame
control, work with the component directly:

```go
anim, _ := scene.C.AnimationState.Get(ent)
skel, _ := scene.C.SkeletonRef.Get(ent)

// Playback speed scaled so the feet track the ground instead of sliding.
anim.PlayLoop(glyphengine.FindClip(skel.Model, "walk"), speed/walkClipNominalSpeed)
```

- `PlayLoop(clip, speed)` loops. Time resets **only on an actual clip change**,
  so calling it every frame with the same clip does not stutter.
- `PlayOnce(clip)` plays once at normal speed; `Playing` goes false at the end.
- Both ignore a negative clip index, so `FindClip`'s `-1` miss is safe to pass
  straight through — a missing clip animates nothing rather than panicking.
- `FindClipAny(model, "sprint", "run", "walk")` handles models that spell a
  clip differently, falling back in order.

Clip lookup is case-insensitive but does a linear scan of the model's clip
list. Resolve indices once at load time if you switch clips every frame.

## Cross-fading

Every clip change cross-fades from the outgoing pose over
`DefaultBlendDuration` (0.15s). Set `AnimationState.BlendDuration` to override
it for the next change — a snappy hit reaction wants less, a settle into idle
wants more.

The outgoing clip keeps playing during the blend and clamps at its end, so a
transition out of a non-looping clip does not freeze on its last frame.

## The engine ships no locomotion controller — deliberately

`AnimationState` is playback only: which clip, where in it, how fast. Choosing
the clip is game policy — an idle/walk/run speed ladder, a jump state machine, a
death override, a hit reaction — and it lives in a game-side system.

That split is not arbitrary. A locomotion controller has to read game
components (health, stance, stun state) and hardcode clip-name strings and
speed thresholds that only make sense for one game. Baking those into the
engine puts game concepts in the engine and locks every consumer into one
game's animation set.

A minimal game-side controller:

```go
scene.AddSystem(func(s *glyphengine.Scene, dt float32) {
	ecs.Query3(s.C.AnimationState, s.C.SkeletonRef, s.C.Velocity,
		func(e ecs.Entity, anim *glyphengine.AnimationState, skel *glyphengine.SkeletonRef, vel *glyphengine.Velocity) {
			if skel.Model == nil {
				return
			}
			// Your priority order goes here: death > jump > locomotion.
			if cc, ok := s.C.CharacterController.Get(e); ok && !cc.Grounded {
				anim.PlayLoop(glyphengine.FindClip(skel.Model, "jump"), 1.0)
				return
			}
			hSpeed := mgl32.Vec2{vel.Vec[0], vel.Vec[2]}.Len()
			switch {
			case hSpeed < 0.5:
				anim.PlayLoop(glyphengine.FindClip(skel.Model, "idle"), 1.0)
			case hSpeed < 6:
				anim.PlayLoop(glyphengine.FindClip(skel.Model, "walk"), hSpeed/2.4)
			default:
				anim.PlayLoop(glyphengine.FindClipAny(skel.Model, "run", "walk"), hSpeed/5.5)
			}
		})
})
```

`CharacterController.Grounded` is the hook for airborne states — you do not
need to infer them from vertical velocity.

## Sampled per frame, not per tick

Animation advances by the real frame delta in `TickAnimations`, not by the
fixed tick delta, so poses stay smooth above and below the tick rate. The delta
is clamped to 0.25s so a long pause does not lurch a pose forward.

## Failure modes

- **Character renders in bind pose.** `SkeletonRef.Skinned` is false, or
  `JointBuffer` is nil, or `AnimationState.Playing` is false. All three are
  required, and `TickAnimations` silently skips the entity otherwise.
- **Nothing animates and no error appears.** `FindClip` returned `-1` because
  the clip name does not exist in that model. Log the result while wiring it up.
- **Animation plays at the wrong speed.** `PlayLoop`'s `speed` is a multiplier,
  not a target velocity. Divide the actual ground speed by the speed the clip
  was authored for.
- **A looping clip restarts every frame.** You called `PlayOnce` in a per-frame
  update. `PlayOnce` always resets time; `PlayLoop` only resets on a clip
  change.
