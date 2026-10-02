# 0011: One runtime-safe resource release contract

- Status: Accepted
- Recorded: 2026-10-02

## Context

Issue #153 reports that `Renderer.DestroyMesh`'s lifetime contract depended on
how the mesh was allocated and whether its upload had retired. Four paths:
an arena range delegated to the arena's deferred span retirement, a dynamic
mesh went through `DeferDestroy`, a mesh with a pending copy cancelled and
deferred, and a settled static mesh destroyed its Vulkan buffers inline.
`DestroyTexture` destroyed everything inline. The higher-level
`DestroyModel`, `DestroyMaterial` and `DestroyInstanceSet` already deferred.

Inline destruction is correct only when no submitted frame can still read the
resource, which is true at shutdown — `Renderer.Destroy` waits the device idle
— and false during a frame. Nothing in the API shows which of the four paths a
`*Mesh` is on, so an application could use the call correctly on a mesh whose
upload was pending and have the same call on the same mesh become a
use-after-free one frame later, when the upload settled. A terrain streamer
carried a branch on `UploadTicket.Ready()` to work around it.

The failure is also undetectable by the cheap signal this repo relies on. The
Vulkan validation layer reports a resource freed while the live draw list still
names it, and says nothing about one freed while only an already-submitted frame
references it — measured several times over, and recorded in
`docs/agents/models.md` and `docs/agents/instancing.md`. A second free is
swallowed by each resource's own `destroyed` flag before it reaches Vulkan, so
that is invisible too.

What must remain true: no device-wide idle on the runtime path (the stall is
what issue #95's asynchronous uploader exists to remove); the renderer's
shutdown sweep must still free what an application never released, exactly once;
and a released resource must stop being drawn by the engine's own paths at the
call rather than at the retirement.

## Decision

Every public release on the `Renderer` means two things, for every resource and
every allocation or upload state:

1. The resource stops being live at the call. Its `destroyed` flag is set, a
   shared glTF texture is evicted from the cache, a dynamic mesh is removed from
   the dynamic-mesh map, and a queued-but-unrecorded upload copy is cancelled
   with its staging returned.
2. Its Vulkan objects are destroyed by one callback on the existing
   `DeferDestroy` queue, which runs after `maxFramesInFlight` flushes — one per
   frame slot fence wait, which is every submission that could have been in
   flight at the call.

That covers `DestroyMesh`, `DestroyTexture`, `DestroyMaterial`,
`DestroyInstanceSet`, `DestroyModel` and `MeshArena.Free`. No device-wide idle
anywhere on that path, and no second public entry point to choose between.

Immediate destruction becomes internal: `destroyMeshNow`, `destroyTextureNow`
and `destroyMaterialNow`, valid in exactly two places — renderer shutdown, and
inside a deferred callback whose countdown has already covered the frames in
flight. The second is load-bearing rather than an optimisation: a release that
deferred inside `DestroyModel`'s own deferral would hold a released level's
geometry for two countdowns instead of one.

`ResourceCounts` reports a released-but-unretired object as live, across every
kind, because it is. Deregistration from `r.meshes`, `r.textures` and
`r.materials` moves into the retirement, which also makes the retirement and the
shutdown sweep mutually exclusive by construction.

## Alternatives

**Add `Retire`/`Release` methods beside the existing `Destroy*` ones**, as the
issue suggests. It leaves the hazardous call public and spelled the way every
existing caller spells it, so the migration is opt-in and a game that does not
read the new page keeps the bug. The engine has one runtime; a second contract
is the thing that caused this.

**Keep inline destruction and document the states.** Rejected because the state
is not observable from the API for a mesh whose ticket the caller did not retain,
and because the hazard is invisible to the validation layer: a game would find it
as corrupted geometry on hardware the author does not own.

**`DeviceWaitIdle` inside the release.** Correct and simple, and it reintroduces
exactly the per-resource queue stall the asynchronous upload path exists to
remove. Explicitly excluded by the issue.

**A typed retirement list instead of a closure per release.** Measured first:
200 releases a frame costs 200 allocations of 32 B, 6.4 KB a frame, with the
deferred queue's backing array reused between frames
(`TestReleasingAMeshCostsOneAllocationPerRelease`,
`BenchmarkReleaseStaticMesh`). A second countdown mechanism beside
`DeferDestroy` would also need the ordering between the two defined, because a
streamed copy's staging retirement and its destination's retirement currently
sit in one queue in that order, which is what keeps a recorded copy's
destination alive. Not worth 6.4 KB a frame; re-measure before revisiting.

## Consequences

- A game releases geometry and textures with one call at any point in a frame,
  with no ticket branch and no `DeferDestroy` wrapper. Wrapping one is still
  harmless, but costs `maxFramesInFlight` extra frames of residency.
- A released resource is resident for `maxFramesInFlight` more frames than it
  used to be on the settled-static and texture paths. A streaming game's peak
  footprint rises by roughly the release rate times the frames in flight, which
  is the same shape `ResourceCounts.PendingUploads` already had.
- `ResourceCounts.Meshes`, `.Textures`, `.Materials` and `.DescriptorSets` no
  longer drop at the call. An application asserting an immediate drop has to
  flush frames first. `examples/22-level`'s reload loop is unaffected: it
  releases through `DestroyModel`, which already deferred.
- The checks are counts and frames, not layer silence. `renderer/release_test.go`
  counts the fake driver's destroy calls per flush for every kind and every
  upload state; `task validate` runs `27-streaming -churn 200` in the sync and
  async modes, asserting both a ceiling on peak `Meshes` and a floor of 1 on
  minimum `Deferred`. The floor is the one that matters: with the inline free
  restored, the layer was silent in both validation modes AND the ceiling passed,
  because freeing at the call deregisters at the call and the count goes down
  rather than up. An upper bound on a resource count can only see a leak.
- Follow-up condition for revisiting: a profile showing the per-release closure
  or the extra residency mattering for a real streaming load. Both have numbers
  recorded above to compare against.

## References and evidence

- Issue #153, and the consumer branch it quotes.
- `renderer/mesh.go`, `renderer/texture.go`, `renderer/material.go`,
  `renderer/gltftexturecache.go`, `renderer/modeldestroy.go`,
  `renderer/renderer.go` (`Destroy`), `renderer/upload.go` (`cancelUpload`).
- [`docs/agents/models.md`](../agents/models.md#releasing-anything-at-runtime-one-contract)
  is the current instruction; [`game-loop.md`](../agents/game-loop.md) and
  [`material-maps.md`](../agents/material-maps.md) cross-reference it.
- ADR [0010](0010-shared-mesh-storage-and-range-submission.md) established the
  arena's deferred span retirement, which this generalises rather than changes.
- Checks run for this change: `renderer/release_test.go` (ten tests, each broken
  once and observed to fail — the messages are recorded next to them), `task ci`,
  and the GPU gates listed in `.task/release-report.md`.
