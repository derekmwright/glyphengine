---
id: render-targets
title: Application render targets, graphics and compute passes
summary: Allocate floating-point images, schedule graphics and compute work, and sample outputs through fixed shader bindings.
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
  - renderer.AppComputeDesc
  - renderer.CreateAppCompute
  - renderer.DestroyAppCompute
  - renderer.AppCompute.SetEnabled
  - renderer.AppCompute.SetDispatch
  - renderer.AppCompute.SetPushConstants
  - renderer.SetShaderTexture
  - renderer.SetShaderTarget
  - renderer.SceneColor
  - renderer.SceneDepth
  - renderer.GPUTimings
example: examples/24-custom-passes
run: task example:24-custom-passes -- -timings
requires:
  - cgo
  - vulkan-runtime
assets: procedural
verified: 2026-09-23 # worked example and custompasses gate
---

# Application render targets, graphics and compute passes

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

Graphics and compute passes at a stage interleave in creation order. The scene
input availability in every row applies to both kinds. Compute always writes
its own storage targets; only graphics can load the HDR destination.

Graphics passes at a stage execute in creation order. A nil `Target` writes the engine
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

## Compute dispatches

```go
output, err := r.CreateRenderTarget(renderer.RenderTargetDesc{
    Name: "computed field", Format: renderer.TargetR32F, Scale: 1,
    Storage: true, History: true,
})
if err != nil { return err }
compute, err := r.CreateAppCompute(renderer.AppComputeDesc{
    Name: "update field", Stage: renderer.StageBeforeScene, Comp: computeSPV,
    Reads: []*renderer.Texture{target.Texture(), output.Texture()},
    Writes: []*renderer.RenderTarget{output}, Timed: true,
})
if err != nil { return err }
w, h := output.Extent()
compute.SetDispatch((w+7)/8, (h+7)/8, 1)
```

`RenderTargetDesc.Storage` adds storage-image usage without changing the resting
`ShaderReadOnlyOptimal` layout. Creation checks the device's optimal-tiling
storage format feature; an unsupported format returns an error naming that
format and target. `Writes` accepts up to four distinct, live targets, each with
`Storage`; a missing flag is an error naming the target. Reading an output in
the same pass requires `History`, with the sampled previous instance distinct
from the storage destination. Scene colour and depth can be sampled after the
scene but cannot be storage outputs. `Reads` follows the graphics input rules.

`SetDispatch` takes workgroup counts, initially zero. Any zero axis records no
compute commands; both timing edges still execute. `SetEnabled(false)` also
skips the node and its barriers. After a relative target resizes, update the
counts from `Extent()` and bounds-check the shader's global invocation IDs.
`SetPushConstants` uses the same 128 application bytes at offset 128 as graphics;
offsets 0–127 carry scene VP and an identity model. Compute's full push range
is visible to the compute stage.

Compute set 0 binds the unused fallback texture set, set 1 binds the shared
light set, and set 2 has four combined samplers at bindings 0–3 and four storage
images at 4–7 in `General`. The application bindings 6–10 in set 1 are also
visible to compute; engine bindings 0–5 retain their existing graphics-stage
visibility. Unused sampled inputs hold the white fallback. Declare only the
storage bindings provided in `Writes`.

```glsl
layout(set=2, binding=0) uniform sampler2D input0;            // Reads[0..3]
layout(set=2, binding=4, r32f) uniform image2D output0;       // Writes[0..3]; format qualifier must match the target's format
layout(push_constant) uniform ApplicationPush { layout(offset=128) vec4 data[8]; } pc;
layout(local_size_x = 8, local_size_y = 8) in;
```

Use `r16f`, `rg16f`, `rgba16f`, `r32f`, or `rgba32f` to match the target.
The device enables extended storage-image formats when supported. Compute
executes on the existing graphics queue. There is no second queue and no async
compute. The graph derives graphics-to-compute and compute-to-graphics barriers;
a dispatch runs without a render pass.

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
bindings 0â€“5 retain their engine layouts and stage visibility; use the engine
`lit.frag`/`lights.inc` layouts for their complete block declarations.

| Binding | Descriptor |
|---|---|
| 0 | `ShadowData` uniform buffer: cascades, night grade, palette, volumetrics; vertex and fragment |
| 1 | Directional shadow `sampler2DArrayShadow`; fragment |
| 2 | Point shadow `samplerCube`; fragment |
| 3 | Light storage buffer; fragment |
| 4 | Cluster grid storage buffer; fragment |
| 5 | Cluster index storage buffer; fragment |
| 6 | Application std140 uniform buffer, 4096 bytes; vertex, fragment and compute |
| 7â€“10 | Four application combined image samplers; vertex and fragment |

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

Destroy passes with `DestroyAppPass` or `DestroyAppCompute`, and targets with
`DestroyRenderTarget`.
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

`Timed` reserves one of 16 application timing entries shared by graphics and
compute. A seventeenth active
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

With `-compute`, a pre-scene dispatch reads the R16F pattern, applies a 3x3 box
blur and accumulates half the previous output in an R32F history target. The
custom lit shader samples that target. The edge probe must lie between the two
band interiors. Runs of at least 260 frames without churn/resize additionally
compare frames 2/60 (at least 4/255 visible change) and frames 200/260 (exact
convergence across the whole capture). `task determinism` repeats both compute and compute-plus-history
runs. Compute is included in churn and resize when the flag is set.

The graphics-plus-compute recording fixture pins 3339 calls against 3331 for
graphics alone: four compute commands, two incoming barrier groups, one layout
return barrier and one additional scene-input barrier. Both kinds together
still record zero allocations at 7, 97 and 511 engine draws.

## Walkthrough

[`24-custom-passes`](../../examples/24-custom-passes/main.go) builds a plain
plaza with three shadow-casting boxes. All effect policy lives in the example;
copy its shaders and these five steps into a game:

1. **Accumulate a light pattern.** `CreateRenderTarget` allocates a half-size
   `TargetR16F` image. `CreateAppPass` schedules a mesh pass at
   `StageBeforeScene` with `BlendAdditive`; `SetDraws` retains three quads whose
   models sweep in world XZ. Their soft edges and overlaps sum into the image.
2. **Smooth it over time.** A second `CreateRenderTarget` requests `TargetR32F`,
   `Storage: true`, and `History: true`. `CreateAppCompute` is created after the
   pattern at the same stage, reads the pattern and its own previous image,
   and writes an exponential average. Each `LateUpdate` uses `Extent()` and
   `SetDispatch((w+7)/8, (h+7)/8, 1)` and supplies the blend factor through
   `SetPushConstants`. The shader bounds-checks every invocation. A pending
   window resize also contributes to the dispatch bounds; history resets on
   recreation.
3. **Light the scene with it.** `renderer.DefaultShaders()` supplies the existing
   shader set; replace only `LitFrag` and pass it through `glyph.WithShaders`.
   `SetShaderTarget(0, smoothed)` binds the field at set 1, binding 7. The copied
   lit shader adds a world-XZ lookup and modulates only ground lighting,
   retaining the engine's shadows. Because it is a history target, scene
   sampling sees the previous write, one frame behind the current compute.
4. **Scatter and composite fog.** `SceneColor()` and `SceneDepth()` become
   `Reads` for a half-size `TargetRGBA16F` fullscreen `CreateAppPass` at
   `StageBeforeBloom`. Inverse VP reconstructs distance from reverse-Z; the
   light colour and scene colour supply the scattered light. A second
   `CreateAppPass` uses `Target: nil`, `Load: true`, and `BlendAdditive` to add
   that target to HDR. Its four low-resolution neighbours are weighted by
   similarity to full-resolution scene depth, retaining silhouettes. A shallow
   fog bank touches the horizon and leaves the upper sky clear.
5. **Measure the work.** Every descriptor sets `Timed: true`. With `-timings`,
   `ResetGPUTimings()` drops the first 30 frames and `MeanGPUTimings().App`
   prints the four named graphics/compute brackets when the run ends.

`-passes off` creates no application targets or passes and uses the ordinary
lit shader, giving the same scene as a control. `-frames` and `-screenshot`
work as in the other examples; `GLYPHENGINE_FIXED_FRAME_TIME=16.667ms` makes the
fixed-tick rectangle paths and temporal smoothing repeatable.

`task custompasses` captures frame 120 twice with passes on and once with them
off. It checks a visible ground change (at least 8/255 in 30000 pixels), a
visible fog band, unchanged upper sky, and byte-identical repeat captures. It
also requires silence from validation through three swapchain recreations.
The captures remain in `.task/custompasses/` for inspection. The renderer
releases all example resources at shutdown; effects removed earlier should
use `DestroyAppPass`/`DestroyAppCompute`, then `DestroyRenderTarget`.
