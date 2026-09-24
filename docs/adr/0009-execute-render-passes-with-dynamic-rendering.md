# 0009: Execute render passes with dynamic rendering

- Status: Accepted
- Recorded: 2026-09-24
- Supersedes: the render-pass compatibility/cache and framebuffer executor portions of [0005](0005-schedule-the-frame-through-a-frame-graph.md); its ordered graph, ownership and timing decisions remain accepted.

## Context

The ordered graph already describes attachment formats, load/store operations,
resolves, extents and resource uses. Maintaining Vulkan render-pass objects,
compatibility keys, subpass dependencies and per-node framebuffers duplicated
that information and tied pipeline compatibility to synchronization policy.
The wrapper supplies `VK_KHR_dynamic_rendering` in extensions v3.3.2.
[Issue #120](https://github.com/derekmwright/glyphengine/issues/120) chose to
require it and remove the legacy executor.

## Decision

Require the extension and its `dynamicRendering` feature. Query features through
`VK_KHR_get_physical_device_properties2` and fail startup with the selected
driver and missing capability named. There is no render-pass fallback.

Keep the Vulkan 1.0 instance API request and explicitly enable the extension
dependency closure: instance properties2; device dynamic_rendering,
depth_stencil_resolve, create_renderpass2, multiview and maintenance2, alongside
swapchain. The renderpass2 extension is a dependency only; the engine creates
no render-pass objects. Retain conditional portability enumeration/subset
handling. This preserves the existing portability path without silently
requiring Vulkan 1.3. MoltenVK must advertise the same required capabilities;
this change has been exercised on Windows, not a Mac.

Every graphics pipeline uses a null RenderPass, subpass zero and a
`PipelineRenderingCreateInfo` chain with its color/depth formats and view mask
zero. Graph, scene, cloud, shadow, impostor-bake and triangle rendering use
`CmdBeginRendering`/`CmdEndRendering`. Inline MSAA resolve remains average.
Reverse-Z, load/store behavior, clears, draws, shader interfaces and pass timing
keep their existing meaning. SceneResolve and WaterResolve bracket only
CmdEndRendering; following barriers are outside those intervals.

The compiler emits explicit attachment entry barriers in `Step.Barriers` and
exit barriers in `Step.AfterBarriers`. Exit barriers restore resting layouts
and expose writes to declared consumers, including vertex/compute and
next-frame readers. Optional nodes/groups remain layout-neutral. Legacy nodes
still own hand-recorded bodies, now with explicit barriers. Buffer scheduling
and history instance selection keep their existing contracts.

Retain the public `RenderPassDesc`, `AttachmentDesc` and `Step.RenderPass`
names as attachment metadata. Remove `Compatible`, `RenderPassKey`, `Key`,
`Node.Dependencies` and `AttachmentOrder`. The renderer stores Go-side rendering
bindings instead of Vulkan framebuffers. Resize and graph rebuild replace
those bindings; descriptor/image lifetime and deferred destruction remain with
their existing owners.

## Alternatives

A dual executor would retain the compatibility/cache and lifetime code this
change removes. Requiring Vulkan 1.3 would avoid explicitly enabling extension
dependencies but impose a broader device/API requirement than this feature
needs. Neither is used.

## Consequences

Devices without the required extension or feature now fail clearly at startup.
No renderer path allocates or destroys VkRenderPass or VkFramebuffer. Pipeline
formats and sample counts must still match the bound attachments. Explicit
barriers make synchronization visible in the compiled plan and add driver
calls, so zero-allocation recording, timing boundaries and performance remain
regression gates.

## References and evidence

- [Frame graph](../agents/frame-graph.md), [render targets](../agents/render-targets.md).
- [Vulkan extension dependencies](https://docs.vulkan.org/refpages/latest/refpages/source/VK_KHR_dynamic_rendering.html).
- `TestMigrationDrawStreams` pins eight draw-side streams measured before this
  migration; full-stream hashes separately include rendering and barriers.
- `TestDynamicRenderingRequired`, `TestDynamicWaterBindings`,
  `TestDynamicDepthTransitionsCoverFormat`, `TestUILayerAttachmentBarriers`,
  and `TestResizeCreatesNoRenderPassesOrFramebuffers` cover the new contracts.
- On Windows / RX 7900 XTX, all 22 documentation captures remained byte-identical;
  core/synchronization validation and the existing visual gates passed. The
  two-image UI-glow resize fixture creates 0 render passes / 0 framebuffers,
  against 5 / 44 with an empty graph cache before this change.
- Six full 30-scene benchmark runs were interleaved without code changes.
  The first branch run had GPU timing spikes that did not recur in either
  subsequent branch run. The final A/B/A/B quartet averaged GPU 1.400 -> 1.367
  ms and CPU work excluding presentation/fence wait 1.125 -> 1.080 ms across
  scenes: flat to better within run variation, not a claimed speedup.
  Every run used a 16.667 ms fixed clock; all draw/instance/triangle/grass counts
  agreed. Recording still allocates zero in the command-recorder fixtures.
