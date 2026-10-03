---
id: profiling
title: Measuring frame cost
summary: >
  Find where a frame's time goes with per-pass GPU timestamps, per-phase CPU
  timing, and a benchmark task that runs a fixed scene set.
capability: meta
status: stable
since: v0.4.0
api:
  - glyphengine.Engine.CPUTimings
  - glyphengine.Engine.LogTimings
  - glyphengine.Engine.LogTimingsTSV
  - glyphengine.Engine.MeanGPUTimings
  - glyphengine.Engine.MeanPipelineStats
  - glyphengine.Engine.PipelineStats
  - glyphengine.Engine.LogPipelineStats
  - glyphengine.WithPipelineStatistics
  - glyphengine.Engine.ResetTimings
  - glyphengine.CPUPhase
  - glyphengine.CPUTimings
  - renderer.GPUTimings
  - renderer.Pass
  - renderer.PipelineStats
  - renderer.StatisticsBracketed
  - renderer.WithPipelineStatistics
  - renderer.Renderer.PipelineStats
  - renderer.Renderer.MeanPipelineStats
  - renderer.Renderer.ResetPipelineStats
  - renderer.Renderer.PipelineStatsSupported
  - renderer.ErrStatisticsNotEnabled
  - renderer.Capabilities.PipelineStatistics
  - renderer.Renderer.MeanGPUTimings
  - renderer.Renderer.ResetGPUTimings
  - renderer.Renderer.Stats
  - renderer.RenderStats
  - renderer.RenderStats.PrepassDraws
  - renderer.RenderStats.PrepassEstimate
  - renderer.RenderStats.PrepassCovered
  - renderer.RenderStats.PrepassActive
  - renderer.PassDepthPrepass
  - renderer.AppStats
  - renderer.AppStats.Pass
  - renderer.AppPassStats
assets: none
run: task bench
verified: 2026-10-03 # pipeline statistics per pass behind WithPipelineStatistics and GLYPHENGINE_PIPELINE_STATS, with the four brackets that cannot carry a query; helper lanes measured NOT counted on an RX 7900 XTX, so the counter bounds quad overshading from below only (#182); cpu drawsort split out of cpu drawlist; App beside the GPU timings; the depth prepass bracket and the shadow bracket's new end; the prepass estimate and decision in the stats block, and the threshold sweep scene
---

# Measuring frame cost

```
GLYPHENGINE_TIMING=1 go run ./08-grass -frames 200
```

```
cpu poll        0.104 ms      gpu shadow      0.055 ms
cpu update      0.010 ms      gpu terrain     0.000 ms
cpu tick        0.050 ms      gpu opaque      0.030 ms
cpu animate     0.000 ms      gpu grass       3.026 ms
cpu lateupdate  0.018 ms      gpu sky         1.642 ms
cpu drawlist    0.014 ms      gpu particles   0.000 ms
cpu drawsort    0.002 ms      gpu water       0.000 ms
cpu gpuwait     9.945 ms      gpu overlay     0.197 ms
cpu submit      0.093 ms      gpu FRAME       4.950 ms
cpu record      0.209 ms
cpu present     6.065 ms
cpu FRAME      16.512 ms
```

`drawlist` is building and culling; `drawsort` is ordering, split out because
what the sort costs is a question of its own and a column carrying the component
walk and the frustum tests cannot answer it. The two sum to what `drawlist`
alone used to report, so the phases still add up to the frame — any older
`cpu_drawlist` figure, including the ones quoted in `instancing.md` and
`models.md`, has to be read as `cpu_drawlist + cpu_drawsort`. On a field of 1024
individual opaque draws in one state group the sort is 0.13 to 0.19 ms a frame,
against 0.7 ms for the rest of the build; on what the examples render it is
microseconds.

`=1` prints the human-readable block; `=tsv` prints one tab-separated line for
collecting runs. Neither needs the game to add a flag — the point, like
`GLYPHENGINE_VALIDATION`, is getting numbers out of a binary you did not build.

`task bench` runs a fixed scene set and prints the same data per scene.

Three of its scenes are not single rows but paired arms, kept interleaved with
every sample retained because what they measure is smaller than the machine's own
drift over a run: `-scene patches` and `-scene stream` for submission and upload
paths, and `-scene overdraw` for hidden fragment work. That last one is a grazing
field of terrain patches under a five-map material where the measured screen-space
depth complexity is 3.28, against a control where the same field is seen from
overhead and it is 1.02. It exists because a mechanism that removes hidden
opaque fragments — the opt-in depth prepass, hierarchical-Z occlusion culling —
costs something unconditionally: the overlap arm says how much there is to win and
the control says what a scene with nothing hidden is charged for it. Each arm runs
with the prepass off, on and auto, interleaved within the arm, so six cells in
all. Read `gpu_total` first, because the prepass is a pass of its own, then
`gpu_prepass` against the fall in `gpu_opaque`, `prepass_estimate` to see what
auto decided on, and the reported depth complexity to confirm each arm is still
what it is named after.

`-scene prepasssweep` is the same example at nine camera placements between
estimate 1.15 and 3.13, prepass off and on interleaved inside each cell: it
measures where the prepass's net change in `gpu_total` crosses zero, which is what
set `DepthPrepassAuto`'s threshold of 2.02. Both bench resolutions have to be run
and they do not agree — the crossing is 1.849 at 1280x720 and 1.201 at 3840x2160,
and the ratio is not the pixel ratio, because the saving scales with the COVERED
pixels and not with the viewport's. `-cells eye:pitch:complexity,...` replaces the
ladder, which is how a crossing that falls outside it gets bracketed without
re-running the other eight. Each cell
checks its own measured complexity against the label it was launched with, so a
sweep whose layout has drifted fails rather than plotting the right numbers
against the wrong x. `28-overdraw -probe` prints a cell's complexity without
opening a window or touching the GPU, which is how the nine were chosen.

`docs/agents/game-loop.md` carries the measurements made on this scene and the
rules they were judged by.

## Read the two tables together

Printing one alone invites the wrong conclusion, because a slow frame with a
large `gpuwait` and a slow frame with `gpuwait` near zero have nothing in common
but their duration:

| shape | meaning | what to look at |
|---|---|---|
| large `gpuwait`/`present`, small `gpu FRAME` | vsync-paced, plenty of headroom | nothing is wrong |
| large `gpuwait`, large `gpu FRAME` | GPU-bound | the GPU table |
| `gpuwait` near zero, long frame | CPU-bound | the CPU table |

The engine's own overhead is about **0.5 ms of CPU per frame** across the
benchmark set, so on a 16.5 ms vsync budget almost everything else is waiting.

## Timestamps, not vsync-off benchmarking

GPU timings come from timestamp queries on the GPU's own clock, so they measure
GPU work whether or not presentation is blocking. Benchmarking therefore does
**not** need vsync off, which used to be the only way to see anything and which
pegs the card audibly.

Results are collected for the frame slot whose fence has just been waited on —
the one moment they are complete and not yet overwritten — so nothing stalls to
read them. They are a couple of frames old as a result; a fresher number would
need a pipeline flush, which would change what it was measuring.

## Always compare means

`MeanGPUTimings` and `CPUTimings` average every frame since the last
`ResetTimings`. Use them for any comparison. A single frame's reading moves by
several percent on unchanged work, and in a scene with a moving camera the last
frame is biased by whatever it happened to be looking at — grass measured 7.1 ms
on the final frame of a run whose mean was 5.06 ms. Averaging cut the
run-to-run spread from 0.32 ms to 0.05 ms.

Establish a noise floor before trusting a difference: run the same build twice
and diff. Anything smaller than that spread is not a result.

## Check the sum against the total

Both tables report a directly measured `FRAME` alongside the parts. The
comparison is load-bearing, not decoration.

For the GPU, the parts are separate query intervals and a **sum above the total
is impossible**. That is how the first version of the timer was caught: it wrote
the opening timestamp at `TopOfPipe` and the closing one at `BottomOfPipe`, so
each pass's interval began before its predecessor had drained, and the passes
summed to 13.2 ms inside a 7.8 ms frame.

For the CPU the phases run strictly in sequence, so a gap between sum and total
is unmeasured work — useful after editing the loop.

## What the passes actually mean

`PassSceneResolve` (`resolve`) and `PassWaterResolve` (`waterresolve`) bracket
`vkCmdEndRenderPass` and nothing else: ending a multisampled pass is where its
colour resolves, which is GPU time no draw caused. It used to be charged to
whichever pass closed last — `overlay` read 0.046 ms on `15-kitchen-sink`, which
sets no world-space overlays at all. Measured on this machine at 1280x720, 200
frames, three runs: `15-kitchen-sink -demo` overlay 0.000 ms, resolve 0.101 /
0.123 / 0.185 ms; `09-water` overlay 0.000, resolve 0.438 / 0.465 / 0.396 ms,
waterresolve 0.030 / 0.021 / 0.021 ms. On `09-water` the scene resolve is close
to a fifth of a 2.2 ms frame, and it spent a long time filed under "overlay".
Anyone comparing MSAA settings should read these two lines, not the passes
around them.

`PassShafts` (`shafts`) is the screen-space light shafts: one fullscreen
triangle of 48 texture taps, drawn inside the water render pass between the
water surface and the blended draws in front of it. It reads zero whenever the
shafts cannot contribute — sun below the horizon, behind the camera, or past the
screen-edge fade — and in those frames with no water the whole water pass is
skipped too, so `water`, `overwater` and `waterresolve` read zero with it.
Measured on this machine at 1280x720, MSAA 4x, 200 frames, five runs of each
pose interleaved with the others: 0.163 ms (0.144–0.169) with the sun in the
middle of the frame (`09-water -time 0.72 -yaw 1.771 -pitch -0.185 -pillars`),
0.095 ms (0.089–0.097) with it at the frame edge (`-yaw 2.4 -pitch 0.05`), and
0.000 with it off screen (`-yaw 2.70`). Those are far tighter than the
whole-frame numbers from the same runs, which spanned 2.97 to 4.60 ms — read
the pass, not the total.

`PassShadow` is the shadow *map render*, not shadow sampling. The PCF lookup
happens inside the grass, terrain and lit fragment shaders, so it lands in those
passes. A cheap `shadow` number does not mean shadows are cheap: switching the
5×5 PCF for four taps took 0.89 ms off a 6.50 ms frame, almost none of it from
the pass called `shadow`.

`shadow` is now exactly that, and it used to be slightly more. Its bracket closed
after the scene pass had already begun, which charged it for the frame-graph steps
between the shadow maps and the scene — application passes at `StageBeforeScene`,
and now the depth prepass. Those carry brackets of their own, so counting them
again here made the passes sum to more than the frame total, which is the one
arithmetic check this instrument has. The bracket now closes when the last cube
face does. On a scene with no pre-scene work the number does not move; on
`24-custom-passes` it drops by whatever its application passes cost.

`prepass` is the optional depth prepass (`WithDepthPrepass`), and it is zero on
every frame without it. Read it against the fall in `opaque`, and read `gpu_total`
rather than either, because the prepass is a second pass and a saving inside
`opaque` that it more than spends would look like a win from `opaque` alone.
`st.PrepassDraws` is how many draws it submitted; they are in `DrawCalls` as well,
the way the shadow cascades' draws are, so `DrawCalls` roughly doubling with the
option on is the second geometry submission and not a regression.

Three more counters come with `DepthPrepassAuto`. `PrepassEstimate` is the mean
screen-space depth complexity of the qualifying geometry, which is the number the
per-frame decision was taken on; `PrepassActive` is that decision;
`PrepassCovered` is the share of the viewport the qualifying bounds cover between
them, which is what distinguishes a high estimate belonging to a frame full of
stacked geometry from one belonging to a frame of sky with a small cluster in it.
All three are zero on a `DepthPrepassOff` renderer, which computes no estimate at
all — zero there means not measured, not measured as zero, and
`Capabilities.DepthPrepass` is what says which. The estimate's own CPU cost is
inside `cpu_record`, deliberately, beside the second submission's: the two costs
of the prepass are then one number. See `game-loop.md` for what the estimate sees
and what it does not.

## Counters say why

```go
st := e.Renderer().Stats()   // DrawCalls, Instances, Triangles, GrassTiles*
```

Times say what costs; counts say why. A pass getting slower is either doing more
work or the same work slower, and the timer cannot tell those apart.

That holds for the application's own passes too, and `st.App` is where they are:
the same three counters for application work only, plus `Dispatches`, and one
row per pass under the name its GPU timer reports. Read a row beside its timing
and a slow pass says which of the two it is; `st.DrawCalls - st.App.DrawCalls`
is the scene on its own, for watching engine-side regressions without the
application's effects in the number.

```go
for _, p := range st.App.Passes {   // graph order, same names as GPUTimings().App
    log.Printf("%-16s %d draws %d triangles %d dispatches", p.Name, p.DrawCalls, p.Triangles, p.Dispatches)
}
```

Application draws were missing from these counters entirely until 2026-10. A
pass submitting 2,359,296 triangles before the scene had a working timer and
moved nothing in `Stats()`, so there was no way to tell it from a cheap one. See
`docs/agents/render-targets.md` for the counting rule, including fullscreen
draws and dispatches.

This is what made the grass work tractable. Grass was 3.95 ms and a trivial
fragment shader still cost 2.76 ms of it, so 70% was never shader maths — the
counters showed 49,214 blades and 16.6 million triangles, most of them sub-pixel
at distance. That pointed at drawing fewer blades rather than at a cheaper
shader.

Overdraw is deliberately absent. Measuring it needs a GPU query the engine does
not run, and a guessed number would be worse than none.

## Counting fragments, not just timing them

```
GLYPHENGINE_PIPELINE_STATS=1 GLYPHENGINE_TIMING=1   go run ./28-overdraw -count 1024 -sky=false -prepass on -msaa 1 -frames 200
```

```
pipeline statistics over 198 frames (fragment invocations include helper lanes only if this device counts them -- see cmd/quadcheck)
stat opaque               77740 fragment invocations     1560752 primitives after clipping
```

At 1280x720 on an RX 7900 XTX. A pass with nothing in either counter is not
printed, and the prepass's own bracket is counted too -- it rasterises the same
geometry through a null fragment stage, so it reads its own invocations. 77,740 invocations over
a frame covering exactly 77,740 pixels is the depth prepass doing what it says:
one shading invocation per covered pixel, measured rather than inferred.

`WithPipelineStatistics` (or the environment variable, on a binary you did not
build) adds a pipeline-statistics query to each of the timer's brackets and
reports `FRAGMENT_SHADER_INVOCATIONS` and `CLIPPING_PRIMITIVES` per pass through
`PipelineStats`. Off by default and free when off: no pool, no reset, no query,
and not one extra driver call in a recorded frame.

The two counters are the numerator and the denominator of a shading-cost ratio.
Invocations over the samples the frame covers says how much of the fragment work
is real; primitives after clipping over the same denominator says how finely the
geometry is diced. `CLIPPING_PRIMITIVES` is **not** `RenderStats.Triangles` --
that one is a CPU count of what the vertex stage was asked for, before clipping
and before anything left the frustum, while this is what came out of the clipper.
Face culling happens after clipping, so a back-facing triangle is in this number
and shades nothing.

### What the fragment counter counts has to be established first

Forward shading runs in 2x2 quads: a triangle covering one pixel still occupies a
quad, and the three lanes outside it run as **helper** invocations so that
derivatives exist. Whether the counter includes them is implementation-defined,
and the two answers are not a detail -- a counter that excludes helpers reads
**1.0** on a field of pixel-sized triangles, which is exactly what a perfect
per-pixel shading model would read. It cannot measure quad overshading at all.

`cmd/quadcheck` (`task quads`) establishes it from geometry whose true quad factor
is known: one triangle covering the whole frame, where every quad is fully covered
and the ratio must be 1.0 whatever the counter includes, and a field of one-pixel
triangles spaced four pixels apart, where the ratio is 4.0 with helpers and 1.0
without. Each arm asserts its own covered-pixel count before reporting a ratio,
and the denominator comes from a frame readback rather than from the counter, so
the ratio is not a tautology.

**On an AMD Radeon RX 7900 XTX (2026-10-03) helper lanes are NOT counted.** A
field of 19,200 triangles each covering one pixel reads exactly 19,200 invocations
where a counter including helpers would read 76,800, and the full-screen arm reads
exactly 307,200 over a 640x480 frame. Both at 1x and 4x MSAA, both with the prepass
off and on; six arms, every one exact to the unit.

So on this device the counter measures **non-helper invocations: one per covered
pixel per primitive**. Two things follow, and the second is a limit worth knowing
before planning any work on it:

- The denominator is covered **pixels** at every sample count. The full-screen arm
  reads 1.0000 at 4x as well, so a fragment runs once per covered pixel per
  primitive and not once per sample -- the engine enables no `sampleShading`, and
  that arm is the measurement of it rather than this sentence.
- **Quad overshading cannot be measured with this counter here.** The helper lanes
  a near-pixel triangle shades are exactly what that question is about, and they
  are not in the number. What a ratio from this counter gives is a LOWER BOUND on
  invocations per covered sample. The route that would measure it is a
  `gl_HelperInvocation` atomic from a fragment stage, and the engine cannot run one
  today: a graphics application pass can bind no storage buffer and no storage
  image, both of which are exposed to compute only (`render-targets.md`). ADR 0013
  carries the measurement, the verdict and that gap.

**Verified to fail**, both ways the proof can be wrong. Asserting the pixel field
against 4.0 -- supposing helpers were counted -- reports `pixels: 1.0000
invocations per covered sample, which is neither 1.0 (helpers not counted) nor 4.0
(helpers counted)`. Submitting the full-screen triangle twice, so every pixel
shades twice, reports `fullscreen: 2.0000 invocations per covered sample, want 1.0
+- 0.02`.

The pixel field runs at **MSAA 1 only**, and that is measured rather than chosen:
at 4x the samples are not at the pixel centre -- the standard pattern puts all four
0.395 pixels away and the nearest sample of the next pixel at 0.605 -- so a
triangle covering one of its own samples and none of its neighbour's needs a
circumradius between 0.591 and 0.605, a 2.4 % window, and the arm would be
measuring the sample pattern. At the default 0.9 it was measured covering two
pixels per triangle, and the arm failed its own coverage assertion rather than
reporting a ratio. The 4x question that matters -- per pixel or per sample -- is
the full-screen arm's, and it answers it.

### What a ratio from it is worth

With the prepass on at MSAA 1 the ratio is **exactly 1.0000** on every scene
measured: the prepass delivers one shading invocation per covered pixel, to the
unit. With it off the same scenes read 1.86, so the prepass removes 46 % of the
shaded fragments. At 4x MSAA the ratio rises above 1.0 -- 1.34 at 0.18 triangles
per covered pixel, 1.94 at 2.2, 2.62 at 18.9 -- and that excess is **not** helper
lanes but a pixel straddling a primitive edge being shaded once per covering
primitive. It is zero at MSAA 1 on every arm, because one sample admits one
primitive. ADR 0013 has the table.

### Four brackets carry no statistics

`StatisticsBracketed` says which passes have a number, and four do not. The reason
is a Vulkan rule rather than a choice: a query is begun and ended by commands, and
one begun inside a render pass instance must end inside the same one.
`PassSceneResolve`, `PassWaterResolve` and `PassTonemap` each straddle a
`vkCmdEndRendering` on purpose -- that is what those brackets are for -- and
`PassWater` opens at the scene-copy node and closes inside the water pass. The
whole-frame bracket is out for a second reason: it encloses every other query, and
two queries of the same type cannot be active in one command buffer. So there is
no whole-frame total to check the per-pass sum against, which is a real loss -- the
timer's equivalent check is what caught its `TopOfPipe`/`BottomOfPipe` mismatch.

A zero on a bracketed pass is a pass that shaded nothing. A zero on an unbracketed
one is not measured, and `StatisticsBracketed` is the only thing that tells them
apart.

### Availability

The device needs `pipelineStatisticsQuery`, which `Capabilities.PipelineStatistics`
reports whether or not this build asked for statistics. The accessors distinguish
the three cases, beside a zero value every time:

```go
st, err := e.MeanPipelineStats()
switch {
case errors.Is(err, renderer.ErrStatisticsNotEnabled):
	// This build did not ask. A program bug, not a device one.
case errors.Is(err, renderer.ErrCapabilityUnavailable):
	// The device has no pipelineStatisticsQuery; measure on one that has.
case !st.Valid:
	// Asked, granted, and the first frames have not come back yet.
}
```

Counts are read back a frame late through the same slot discipline as the
timestamps -- after the slot's fence has signalled and before its queries are
reset -- so nothing stalls to collect them.

## No committed baseline

`task bench` does not compare against stored numbers. Frame cost depends on the
GPU, the driver, the display mode and what else is running, so a committed
baseline would be one machine's numbers rotting into a check that fails for
everyone but its author. Run it before and after a change on the same machine.

`task bench -- -json out.json` writes the results structured if you want to diff
two commits yourself. `-repeat N` keeps the fastest of N runs, on the grounds
that a slow run means something else interfered and there is no such thing as a
spuriously fast one.

## CPU allocations

Use the GPU-free benchmarks to isolate a CPU path from presentation and frame
pacing. `-benchmem` reports allocations and bytes per operation:

```sh
go test . -run '^$' -bench 'Benchmark(RaycastGrid|OverlapGridMiss|OverlapAABB|FindPath)$' -benchmem -count 3
go test ./renderer -run '^$' -bench 'Benchmark(RecordCommandBuffer|MSDFGeometry)$' -benchmem -count 3
go test ./renderer/lightcluster -run '^$' -bench BenchmarkBuild -benchmem -count 3
```

For allocation attribution, add `-memprofile alloc.prof` to one package's run,
then use `go tool pprof -alloc_objects alloc.prof` or `-alloc_space`. These are
CPU workload measurements, not whole-frame speedups. Compare the same fixtures
on the same machine, interleave before/after runs, and check real fixed-clock
renders after changing engine behavior.

The physics and pathfinding allocation guards were verified by restoring the
old implementations and watching them fail. A separate path fingerprint pins
the original routes and budget-exhaustion results, so fewer allocations cannot
pass by quietly doing less search work.

## Availability

Timestamps need `timestampComputeAndGraphics` **and** a graphics queue with
non-zero `timestampValidBits`; a device can report the first without the second,
and checking only that is how this silently reports zeros. When either is
missing the timer is inert and `GPUTimings.Valid` is false.

`01-triangle` cannot be measured at all: it drives the renderer with its own
loop rather than `Engine.Run`, so there is no frame loop to instrument. It is
excluded from `task bench` for that reason.
