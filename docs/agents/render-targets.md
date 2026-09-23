---
id: render-targets
title: Application render targets and graphics passes
summary: Allocate floating-point images, schedule graphics work, and sample outputs through fixed shader bindings.
capability: rendering
status: experimental
api:
  - renderer.RenderTargetDesc
  - renderer.CreateRenderTarget
  - renderer.DestroyRenderTarget
  - renderer.RenderTarget.Texture
  - renderer.RenderTarget.Extent
  - renderer.AppPassDesc
  - renderer.CreateAppPass
  - renderer.DestroyAppPass
  - renderer.AppPass.SetEnabled
  - renderer.AppPass.SetDraws
  - renderer.AppPass.SetPushConstants
  - renderer.SetShaderTexture
  - renderer.SetShaderTarget
  - renderer.SceneColor
  - renderer.SceneDepth
  - renderer.GPUTimings
example: cmd/apppasscheck
run: go run ./cmd/apppasscheck -frames 300 -churn -provoke-recreate -validate
requires:
  - cgo
  - vulkan-runtime
assets: procedural
verified: 2026-09-23
---

# Application render targets and graphics passes

Call these APIs on the renderer thread, normally from `Game.Init` or
`Game.Update`. The game supplies shaders and draws; the renderer owns images,
pass scheduling, synchronization, descriptor updates, resize and GPU lifetime.

```go
target, err := r.CreateRenderTarget(renderer.RenderTargetDesc{
    Name: "application field", Format: renderer.TargetR16F, Scale: 0.5,
})
if err != nil { return err }
pass, err := r.CreateAppPass(renderer.AppPassDesc{
    Name: "write field", Stage: renderer.StageBeforeScene, Target: target,
    Fullscreen: true, Vert: fullscreenSPV, Frag: fieldSPV, Timed: true,
})
if err != nil { return err }
if err := r.SetShaderTarget(0, target); err != nil { return err }
// Custom scene shaders now sample the field at light-set binding 7.
```

Formats are `TargetR16F`, `TargetRG16F`, `TargetRGBA16F`, `TargetR32F`, and
`TargetRGBA32F`. Both fixed `Width` and `Height` must be nonzero; otherwise
`Scale` must be positive and finite. A fixed extent ignores `Scale`.
`Depth: true` adds a single-sample depth attachment, cleared to reverse-Z zero
when its pass clears and retained when the pass loads. Newly allocated target
contents, including history, start at zero.

| Stage | Available inputs and destination |
|---|---|
| `StageBeforeScene` | After clouds and shadows, before scene geometry. Earlier application outputs and history; own targets only. |
| `StageAfterScene` | Resolved HDR and scene depth, before water copies the scene. Own targets only. |
| `StageBeforeBloom` | The completed HDR scene including water; scene depth and earlier outputs. Own target or loaded HDR. |
| `StageBeforeTonemap` | After scene bloom and the UI glow chain. Earlier outputs, HDR and scene depth. Own target or loaded HDR; changes here do not feed this frame's bloom. |

Passes at a stage execute in creation order. A nil `Target` writes the engine
HDR scene, requires `Load: true`, and is restricted to the last two stages.
`BlendAdditive` adds source and destination. `BlendAlpha` uses premultiplied
source-over. `BlendNone` replaces covered pixels. `DepthTest` requires a
target with depth, or nil `Target`; in the latter case it loads scene depth
without writing it. With MSAA, HDR passes load the scene's multisample colour
and resolve it back to HDR.

`Reads` supplies up to four `*Texture` inputs: `target.Texture()`,
`r.SceneColor()`, `r.SceneDepth()`, or an ordinary loaded image. Nil and
destroyed textures are rejected. Scene colour can only be read after the
scene and with an own destination target. Scene depth can only be read after
the scene. Ordinary textures need no graph resource declaration. Sampling and
writing the same target is rejected unless it has `History`. A slot naming
the current destination must likewise not be sampled by that pass unless the
target has history; shader source determines which global slots it uses.

`r.SceneColor()` returns a borrowed, stable texture for resolved HDR colour.
Its image, view and existing descriptor set follow the acquired swapchain
image. `r.SceneDepth()` enables a graphics node immediately after the scene
and returns a borrowed, stable texture for its output. Pass either texture
in `Reads` at the binding your shader expects. Allocation occurs at the next `DrawFrame`; do
not inspect its GPU descriptor before then. The node remains enabled for the
renderer lifetime. It stores the scene's reverse-Z depth in R32F: 1 is near,
0 is far/background. With MSAA it takes the maximum sample. It captures
opaque scene depth before water, whose pipeline does not write depth.

## Fixed shader layouts

As with `ShaderSet`, these are fixed layouts, not reflected material layouts.
SPIR-V must obey the bindings, push offsets, vertex inputs and output formats.
Vulkan pipeline errors include the pass name. Replacing shaders does not
negotiate a different interface.

Set 0 binding 0 is the mesh draw's texture, or the white fallback when absent
or destroyed. Fullscreen passes use the fallback. It reuses the ordinary
texture layout and is visible to the fragment stage; its unused environment
UBO at binding 1 must not be read. Set 2 has four pass input samplers, visible
to vertex and fragment stages. Unused bindings hold the white fallback.

```glsl
layout(set=0, binding=0) uniform sampler2D drawTexture;
layout(set=2, binding=0) uniform sampler2D input0; // Reads[0]
layout(set=2, binding=1) uniform sampler2D input1; // Reads[1]
layout(set=2, binding=2) uniform sampler2D input2; // Reads[2]
layout(set=2, binding=3) uniform sampler2D input3; // Reads[3]
```

Set 1 is the shared shadow/light set. It is also set 1 for custom sky, static
lit, terrain and water shaders, and set 2 for skinned lit shaders. Existing
bindings 0–5 retain their engine layouts and stage visibility; use the engine
`lit.frag`/`lights.inc` layouts for their complete block declarations.

| Binding | Descriptor |
|---|---|
| 0 | `ShadowData` uniform buffer: cascades, night grade, palette, volumetrics; vertex and fragment |
| 1 | Directional shadow `sampler2DArrayShadow`; fragment |
| 2 | Point shadow `samplerCube`; fragment |
| 3 | Light storage buffer; fragment |
| 4 | Cluster grid storage buffer; fragment |
| 5 | Cluster index storage buffer; fragment |
| 6 | Application std140 uniform buffer, 4096 bytes; vertex and fragment |
| 7–10 | Four application combined image samplers; vertex and fragment |

```glsl
// Application-pass set 1; change set to 2 for a skinned lit shader.
layout(set=1, binding=0, std140) uniform ShadowData {
    mat4 cascadeVP[2];
    vec4 nightGrade;
    vec4 skyPalette[6];
    vec4 volumetric;
} shadow;
layout(set=1, binding=1) uniform sampler2DArrayShadow shadowMap;
layout(set=1, binding=2) uniform samplerCube pointShadowMap;
struct GpuLight { vec4 posRange; vec4 color; vec4 dirCone; vec4 params; };
layout(set=1, binding=3, std430) readonly buffer LightBuffer {
    uvec4 grid; vec4 zParams; vec4 screen; uvec4 flags; GpuLight lights[];
} lb;
struct Cell { uint offset; uint count; };
layout(set=1, binding=4, std430) readonly buffer ClusterGrid { Cell cells[]; } cg;
layout(set=1, binding=5, std430) readonly buffer LightIndices { uint indices[]; } li;
layout(set=1, binding=6, std140) uniform ApplicationParameters {
    vec4 values[256];
} application;
layout(set=1, binding=7) uniform sampler2D applicationTexture0;
layout(set=1, binding=8) uniform sampler2D applicationTexture1;
layout(set=1, binding=9) uniform sampler2D applicationTexture2;
layout(set=1, binding=10) uniform sampler2D applicationTexture3;

layout(push_constant) uniform ApplicationPush {
    layout(offset=0) mat4 viewProjection; // engine SceneLighting.VP
    layout(offset=64) mat4 model;         // RenderObject.Model; fullscreen identity
    layout(offset=128) vec4 data[8];      // application-owned 128 bytes
} pc;
```

`SetPushConstants` copies little-endian bytes, at most 128, padded to a multiple
of 16. It zeroes the unused tail; nil clears the application block. Invalid
sizes return an error without changing previous data. The full 256-byte push
range is visible to vertex and fragment stages.

Fullscreen shaders use `gl_VertexIndex` to generate a triangle and declare no
location vertex inputs. Mesh shaders use `Vertex`: locations 0 position
(`vec3`), 1 colour (`vec3`), 2 normal (`vec3`), 3 UV (`vec2`). A shader can use
a subset. Creation rejects a `Fullscreen` flag inconsistent with these inputs.

For mesh passes, `SetDraws` retains the supplied `[]RenderObject`; keep it alive
through `DrawFrame`. Only `Mesh`, `Model` and `Texture` are used. `MVP` is
ignored because the engine supplies VP separately. `Texture` always supplies
set 0 binding 0 using its existing descriptor set; pass inputs at set 2 stay
independent. A mesh pass with no draws still begins
and ends its render pass, including a requested clear. Fullscreen passes draw
one triangle; supplying mesh draws to one is an error at `DrawFrame`.

## History, resize and lifetime

`History` allocates two colour instances and alternates by frame-in-flight
index, independently of swapchain image acquisition. During a frame,
`Texture()` and every sampled reference select the previous frame's write,
while the attachment selects the other instance. Both start at zero. Disabling
a pass skips its whole graph node; frame indices continue to advance.

`*RenderTarget` and its `Texture()` pointer remain valid through resize.
Relative targets are reallocated on swapchain recreation and their history
starts over. Fixed-size images survive. Descriptors and framebuffers are
rebuilt against replacement views. Use `Extent()` for the current dimensions.

`SetShaderTexture` and `SetShaderTarget` record intent. They update only the
frame slot whose fence was waited on. Nil restores the fallback. Sampling an
unbound slot returns the fallback, not uninitialized data. Pass inputs follow
the same fence rule. A renderer resize waits for the GPU before replacing all
affected descriptors.

Destroy passes with `DestroyAppPass` and targets with `DestroyRenderTarget`.
Destroying a target also destroys passes writing it; other readers and slots
fall back to white. GPU destruction is deferred across frames in flight.
Descriptor sets retire before their owned image views. Undestroyed resources
are returned by `Renderer.Destroy`. The renderer owns scene colour and depth;
these borrowed views cannot be destroyed independently.

Creation/destruction marks the graph dirty. After the next frame fence wait,
the renderer compiles new declarations, uses cached render passes and builds
replacement framebuffers. Old framebuffers retire through `DeferDestroy`;
cached render passes survive until renderer destruction. Pipeline construction
uses the same compiled description immediately at `CreateAppPass`.

`Timed` reserves one of 16 application timing entries. A seventeenth active
timed pass is rejected. `GPUTimings.App` contains `{Name, Ms}` in creation
order, with both timestamp edges written even for disabled passes. Existing
`Pass` values and `GPUTimings.Pass` retain their meaning. Timing slices are
renderer-owned views; copy them if retaining them across frames.

Recording uses retained command scratch and allocates zero bytes per frame.
Creating resources and rebuilding the graph are setup work and can allocate.
Draws reuse existing texture sets; only the pass input set is allocated per
frame slot. Changing a draw count does not allocate descriptor sets.

The GPU check compares a pattern-modulated terrain quad plus a half-resolution
HDR/depth filter and additive composite against disabled passes. It requires
nonzero terrain pixels with at least 4/255 contrast. `-history` exercises
self-reading history; `-churn -provoke-recreate` replaces resources every 30
frames while resizing. `task determinism` repeats both ordinary and history
runs. The recording fixture pins 3331 calls with application passes against
3299 without them, and reports zero recording allocations at 7, 97 and 511
engine draws.
