# 0007: Select instance LOD on the renderer CPU

- Status: Accepted
- Recorded: 2026-09-24

## Context

Issue #114 requires independently culled placements, distance-selected meshes,
smooth transitions and far billboards. A game cannot reach the renderer's
instance vertex bindings or pipeline coverage state from outside the library.
The existing `InstanceSet` intentionally has one bound and one mesh per set.

## Decision

Expose `InstanceSetLOD` beside `InstanceSet`. Games own placements, meshes,
distance bands, fade width, shadow selection and any wind shader. The renderer
owns frustum selection, per-band scratch, coverage and GPU buffers. This is
independent of application frame-graph passes.

Prepare the buckets after the current frame fence, with separate persistently
mapped buffers for every frame in flight. Updates copy CPU placements. Sets
borrow meshes and atlases; explicit destruction defers storage release until
submitted frames have retired. Resource counts expose deferred lifetime.

Use complementary ordered coverage and an MSAA alpha-to-coverage variant,
with dedicated `LitLODVert`/`LitLODFrag` stages. Keep ordinary lit and instanced
shader interfaces and bytes unchanged; share lighting through `lighting.inc`.
Reuse grass bake resources
and fragment lighting for an eight-view atlas, adding depth to the general bake.
Billboards receive current lighting. Only the configured mesh bucket casts
shadows; impostors do not. Selection uses the main camera's frustum, so it is
not an independent off-camera shadow-caster selection mechanism.

## Alternatives

- Replacing `InstanceSet` would impose per-frame selection/upload costs on
  games that already partition small groups effectively.
- GPU-driven selection is a separate capability (#115). It must not be a
  prerequisite for ordinary host-visible instance buffers.
- A forest or vegetation subsystem would impose game content and placement
  policy. The example owns all tree geometry and terrain scatter.
- Immediate buffer reuse or destruction could overwrite data still in flight.
- Two plain alpha-to-coverage fades overlap sample masks. Ordered opposite
  thresholds preserve complementary pixel coverage through a transition.

## Consequences

There is CPU work and fixed per-band capacity storage in exchange for less
off-camera and distant vertex work. Counts include transition duplicates and
capacity drops. Bounds are translation-centred, with largest-axis scale; authors
must enclose all levels about their placement origin. Billboards approximate
orientation and nonuniform scaling.

Only the LOD shader interface has coverage at fragment location 5 and
specialization constant 0. Custom LOD shaders must forward it to support fades;
existing custom lit and terrain shaders are unaffected.

## References and evidence

- [Engine/game ownership](0002-keep-game-ownership-outside-the-engine.md)
- [LOD API, measured cost and gate](../agents/lod-instancing.md)
- [Implementation](../../renderer/instancedlod.go)
- [Allocation, selection and lifetime tests](../../renderer/instancedlod_test.go)
- [GPU gate](../../cmd/lodcheck/main.go)

This builds on the separate point-light instance-shadow fix, whose regression
test requires 18 instances over six cube faces instead of six. LOD leaves all
of that fix's original-scene command-stream hashes unchanged.
