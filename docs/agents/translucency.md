---
id: translucency
title: Draw translucent world geometry
summary: >
  Put a Translucent component on an entity and it is drawn blended over the
  scene, back to front, after everything opaque and without casting a shadow.
capability: rendering
status: stable
since: v0.5.0
api:
  - glyphengine.Translucent
  - renderer.RenderObject.Alpha
  - renderer.RenderObject.IsTranslucent
  - renderer.RenderObject.ViewDepth
example: examples/18-translucent
run: task example:18-translucent
requires:
  - cgo
  - vulkan-runtime
assets: none
verified: 2026-09-18
---

# Draw translucent world geometry

```go
ghost := e.Spawn()
e.C.Transform.Set(ghost, &glyph.Transform{Position: at, Scale: mgl32.Vec3{1, 1, 1}})
e.C.MeshRef.Set(ghost, &glyph.MeshRef{Mesh: dome, Roughness: 0.55})
e.C.Color.Set(ghost, &glyph.Color{R: 0.45, G: 0.85, B: 1.0})
e.C.Translucent.Set(ghost, &glyph.Translucent{Alpha: 0.35})
```

That is the whole API. The entity keeps the lighting, fog and shadow-receiving
of the opaque path — it is the same `lit.vert` and `lit.frag` — and gains
blending, a back-to-front sort, and an exemption from casting shadows.

`Alpha` is animatable. Run it to 0 and the engine stops issuing the draw
entirely; run it to 1 and the draw goes back through the opaque pipeline. Both
ends work without the game special-casing them, so a fade is a single lerp.

## Why a component and not a field on MeshRef

`MeshRef`'s zero value has to stay opaque. An `Alpha float32` there would read
as fully transparent on every `MeshRef` in every existing game unless zero were
special-cased to mean one — and "0 means opaque, except when it means invisible"
is the kind of quiet surprise that costs an afternoon. A component makes the
opt-in explicit and leaves the zero value alone.

## Ordering is the feature

Blending is not commutative. Two overlapping translucent objects composited in
the wrong order produce the wrong colour, and nothing about that fails loudly:
the frame renders, the validation layer stays silent, and the image is wrong in
a way only a person looking at it notices.

`Engine.buildDrawList` therefore sorts translucent draws after all opaque ones,
and within themselves by distance from the eye, farthest first. It happens every
frame because the answer depends on where the camera is, not on what the scene
contains. `18-translucent` has three overlapping panes at different depths for
exactly this reason — orbit past them and the order has to invert.

**A game driving `renderer.DrawFrame` directly sorts its own.** The renderer
records the translucent subset in the order it is given, the same way the opaque
path relies on the caller having sorted by `SortKey` for its batching.

## Where it lands in the frame

After the sky and before particles. Both ends matter:

- **After the sky.** The sky is a fullscreen triangle drawn last on purpose, so
  it only shades pixels nothing else landed on. Translucent geometry writes no
  depth, so drawn before the sky it would be painted straight over wherever it
  overhangs the horizon — a ghost on a ridgeline would lose its top half.
- **Before particles.** Neither writes depth, so the order between them is paint
  order and nothing else. Additive effects reading as in front of a translucent
  surface is the less surprising of the two.

Depth is still *tested*, so a ghost behind a hill stays behind it. It is not
*written*, so two translucent surfaces do not depth-fight over which one exists,
and the opaque depth every later pass reads stays the opaque depth.

### Which render pass, when the frame has water

Not writing depth is also what made these draws vanish behind water. The water
surface is drawn last, in a second pass, because refraction samples the finished
frame — and at a pixel where a ghost stood in front of the lake the depth buffer
held the lake *bed*, so the water passed the depth test and painted over the
ghost (issue #45).

So in a frame that contains water this group splits in two, on the surface:

- a translucent draw **behind** the water — its bound centre on the opposite
  side of some water surface's still plane from the eye — is recorded in the
  scene pass exactly as described above, before the refraction copy, so the
  water refracts and absorbs it;
- one **in front** is recorded in the water pass, after the surface.

Both halves keep the back-to-front order: the recorder filters the already
sorted list rather than sorting each half, and a subsequence of a sorted
sequence is sorted. The order relative to particles is also unchanged — they
split on the same plane and follow the translucent draws in each half.

Everything in [`water.md`](water.md) about what that rule approximates applies
here, and one line of it is worth repeating because it is a `Translucent` mesh
that will meet it first: **the classification reads one point, the draw's bound
centre.** A pane standing half in the water goes wholly to one side.

`renderer/waterorder.go` decides, once per frame, for every blended draw.
A frame with no water skips all of it and records exactly what it always did.

## Shadows

Translucent entities do not cast. A placement preview that threw a solid shadow
would read as a real building, which defeats the point.

The decision is made in `buildDrawList`, where the draw is built, by setting
`NoCastShadow` — so it is visible in the draw list rather than discovered in a
shader. The shadow pass also skips anything `IsTranslucent`, which is the guard
for a game driving the renderer directly.

If you want a ghost that *does* cast, that is not reachable today; use an opaque
entity with `NoCastShadow` cleared.

## Composing with other components

- **Emissive** composes. A full-bright translucent object is the hologram
  variant of the same ghost, so alpha applies on the emissive early-out path too
  rather than one flag winning. Hold `E` in `18-translucent`.
- **DoubleSided** composes on the static path, via a second blended pipeline.
  Without it, a glass box would cull its back faces and leave nothing where the
  far wall should be. It does nothing on a skinned draw; see above.
- **Hidden** still wins; it is checked first.
- **InstancedMesh** does not. There is no blended instanced pipeline, so a set
  stays opaque rather than silently losing its placements. See
  [`instancing.md`](instancing.md).

## Skinned meshes

A skinned mesh fades the same way, through its own blended pipeline:

```go
e.C.Translucent.Set(character, &glyph.Translucent{Alpha: 0.4})
```

Respawns, cloaked enemies, a dissolve, a preview of an animated unit — all of
them are a character rather than a prop, and all of them were locked out until
this existed.

**There is no double-sided skinned variant, deliberately.** The opaque skinned
path has none either, so adding one here would make a translucent character more
capable than a solid one. `DoubleSided` therefore does not select a pipeline on
a skinned draw, and the flat-shading signal that goes with it is suppressed.

`task validate` covers it as `06-skinned -demo -plain -fade 0.4`, because no
default run reaches the pipeline.

## Failure mode: the paths with no blended variant

Terrain, water and material (`MaterialRef.PBR`) draws each go through a pipeline
of their own, and none has a blended twin.

**An entity that carries `Translucent` alongside any of those stays opaque.** It
is not rerouted, because rerouting would silently drop the splat blend, the
refraction, or the normal and occlusion maps — a frame that renders and is
quietly wrong, which is worse than an object that is not as see-through as asked
for.

The case worth knowing about is **a skinned mesh that also carries a
`MaterialRef.PBR`**: it goes through the skinned *material* pipeline, which has
no blended twin, so it stays opaque even though both of its halves look
supported. `examples/06-skinned` is exactly this — its character gets a
generated normal map — which is why the `-fade` flag there needs `-plain`
alongside it.

`renderer.RenderObject.IsTranslucent` is the single place that decides, so the
draw-list build and the recorder cannot drift apart. They have to agree exactly:
a draw the opaque loop skips and the blended loop also skips simply vanishes.

Translucent PBR is the obvious remaining one — a pipeline and a branch, unwritten
because nothing has needed it.

## Not done

Order-independent transparency, depth peeling, per-pixel sorting. Back-to-front
per object is what this genre ships with and it is all a placement ghost needs.

Alpha-tested cutout foliage is a different feature wearing a similar name: that
is a `discard` in the fragment shader, wants no blending and no sorting, and
`lit.frag` already does it at `texSample.a < 0.5`.

## What it costs

Two pipelines at startup, and a pass of its own in the frame timings so the cost
is attributable rather than folded into `opaque`. Measured with `task bench` on
`18-translucent` — four opaque domes, one ghost and three double-sided panes,
200 frames:

```
gpu opaque        0.023 ms
gpu translucent   0.005 ms
```

Four blended draws against five opaque ones, so the per-draw cost is in the same
neighbourhood; there is nothing expensive about the pipeline itself. What would
cost is the overdraw, since translucent surfaces do not occlude each other and a
stack of them shades every pixel once per layer.

The sort is over the translucent subset only, and `ViewDepth` is recomputed per
comparison rather than cached in a parallel slice. That is fine for the handful
of ghosts, indicators and panes a frame has; precompute it if that ever stops
being true.
