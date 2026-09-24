# 0005: Schedule the frame through a frame graph

- Status: Accepted
- Recorded: 2026-09-23
- Superseded in part: [0009](0009-execute-render-passes-with-dynamic-rendering.md), 2026-09-24, replaces the render-pass/cache and framebuffer executor decisions. The graph and ownership decisions remain accepted.

## Context

The renderer records a frame as one straight-line function
(`renderer/commands.go`, `recordCommandBuffer`). Every pass added since the
extraction was threaded through it by hand: image layout transitions live in
three separate sites, two render passes share a dependency struct
byte-for-byte so their pipelines stay compatible, and the GPU timer has to be
fed empty brackets for passes that did not run so query readback stays valid.
The order, the layouts and the barriers are knowledge that exists only in the
function body and its comments.

Issue #99 asks for application-owned render targets and application-scheduled
graphics and compute passes, with renderer-managed barriers and the ability
to sample a pass's output from later custom shaders. Rule 14 is satisfied
either way: a consuming game cannot reach pass scheduling from outside the
engine. The question was whether to add fixed insertion slots for application
passes beside the hand-ordered frame, or to make scheduling a first-class
structure that engine passes and application passes share.

Two facts constrain the choice. The device is created at Vulkan 1.0 and the
wrapper offers no dynamic rendering, so passes are `VkRenderPass` objects and
pipelines are only usable with a compatible one. And the engine can verify a
scheduling refactor cheaply: `TestRecordCommandBufferStreamIsUnchanged`
hashes every driver call and argument, and `task determinism` compares
rendered bytes under a fixed clock.

## Decision

Introduce `renderer/framegraph`, a backend-free compiler, and execute the
frame from the plan it builds.

- Nodes run in declaration order. The compiler validates and derives; it
  never reorders. Graphics, compute, transfer and legacy node kinds exist.
- Every resource has one resting layout derived from its uses. Attachments
  transition through the render pass's initial and final layouts and never
  through barriers; transfer and storage uses get explicit barriers only when
  the tracked layout or access differs. This rule reproduces the barriers the
  hand-written passes issue today, which is what makes the migration
  verifiable.
- Optional nodes must be layout-neutral so skipping one never changes what a
  later node sees.
- Migration is incremental. The clouds, shadow and scene block starts as one
  legacy node that declares what it leaves behind. The tail of the frame,
  from the scene-colour copy through tonemap, migrates first because that is
  where the explicit barriers live and where application passes insert. Each
  step must leave the command-stream hash unchanged, or justify every
  differing call.
- Stay at Vulkan 1.0 with render pass objects. The graph owns render pass
  caching and compatibility, so a later move to dynamic rendering is a change
  in one place.

Out of scope until a later record: memory aliasing between transient
resources, automatic reordering, and async compute on a second queue.

## Alternatives

- **Fixed insertion slots for application passes.** Smaller, and enough for
  the caustic and scattering passes in #99. Rejected because every engine
  pass would still be hand-ordered, compute passes would need hand-placed
  graphics-to-compute barriers, and the slots would be a second scheduling
  mechanism beside the first.
- **Migrate every engine pass before any application API.** A cleaner end
  state, but #99 would wait on the shadow and scene block, which is the
  largest and least reusable part of the frame and gains nothing from the
  graph today.
- **Build on dynamic rendering (Vulkan 1.3).** It removes render pass and
  framebuffer objects and their compatibility rules, and would simplify the
  graph substantially. Not possible now: the wrapper has no 1.3 package and
  no `khr_dynamic_rendering` extension package, and contributions to the
  wrapper are on hold pending its maintainer. Tracked below.
- **Raise the device to Vulkan 1.2 first.** Imageless framebuffers and
  depth-stencil resolve would help at the margins, but the graph does not
  need them, and the same features exist as extension packages usable at
  1.0.

## Consequences

- Application passes and engine passes are the same kind of node, with the
  same barrier derivation, timing and lifetime handling.
- Every migration step is gated by the command-stream hash and the
  determinism gate rather than by inspection.
- The graph must maintain a render pass cache keyed on the full description
  and expose Vulkan compatibility, because pipelines are still created
  against render pass objects.
- Requiring a device extension for dynamic rendering later would be a
  separate decision with its own record.

Follow-up to revisit this record: when a dynamic rendering binding becomes
available in the wrapper, or is written for it with the maintainer's consent,
move graphics nodes off render pass objects. The graph's executor is the only
place that changes.

## References and evidence

- Issue #99 (application-owned render targets and custom passes) and #94
  (application uniforms, the binding precedent this builds on)
- [Agent guide](../../AGENTS.md), rules 10, 12, 13 and 14
- `renderer/commands.go` `recordCommandBuffer`; `renderer/pipeline.go`
  `sceneEntryDependency` and the compatibility note above it
- `renderer/commands_alloc_test.go` `TestRecordCommandBufferStreamIsUnchanged`
- [Validation](../agents/validation.md) and `task determinism`
- Wrapper survey, 2026-09-23: `github.com/vkngwrapper/core/v3` is handwritten,
  its loader leaves 41 core 1.1/1.2 entry points unwired, its bundled header
  is v224 (contains every 1.3 type), and extension packages are
  self-contained. No checks were run for this record beyond reading the code.
