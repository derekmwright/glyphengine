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
  - glyphengine.Engine.ResetTimings
  - glyphengine.CPUPhase
  - glyphengine.CPUTimings
  - renderer.GPUTimings
  - renderer.Pass
  - renderer.Renderer.MeanGPUTimings
  - renderer.Renderer.ResetGPUTimings
  - renderer.Renderer.Stats
  - renderer.RenderStats
assets: none
run: task bench
verified: 2026-08-02
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
cpu drawlist    0.016 ms      gpu particles   0.000 ms
cpu gpuwait     9.945 ms      gpu water       0.000 ms
cpu submit      0.093 ms      gpu overlay     0.197 ms
cpu record      0.209 ms      gpu FRAME       4.950 ms
cpu present     6.065 ms
cpu FRAME      16.512 ms
```

`=1` prints the human-readable block; `=tsv` prints one tab-separated line for
collecting runs. Neither needs the game to add a flag — the point, like
`GLYPHENGINE_VALIDATION`, is getting numbers out of a binary you did not build.

`task bench` runs a fixed scene set and prints the same data per scene.

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

`PassShadow` is the shadow *map render*, not shadow sampling. The PCF lookup
happens inside the grass, terrain and lit fragment shaders, so it lands in those
passes. A cheap `shadow` number does not mean shadows are cheap: switching the
5×5 PCF for four taps took 0.89 ms off a 6.50 ms frame, almost none of it from
the pass called `shadow`.

## Counters say why

```go
st := e.Renderer().Stats()   // DrawCalls, Instances, Triangles, GrassTiles*
```

Times say what costs; counts say why. A pass getting slower is either doing more
work or the same work slower, and the timer cannot tell those apart.

This is what made the grass work tractable. Grass was 3.95 ms and a trivial
fragment shader still cost 2.76 ms of it, so 70% was never shader maths — the
counters showed 49,214 blades and 16.6 million triangles, most of them sub-pixel
at distance. That pointed at drawing fewer blades rather than at a cheaper
shader.

Overdraw is deliberately absent. Measuring it needs a GPU query the engine does
not run, and a guessed number would be worse than none.

## No committed baseline

`task bench` does not compare against stored numbers. Frame cost depends on the
GPU, the driver, the display mode and what else is running, so a committed
baseline would be one machine's numbers rotting into a check that fails for
everyone but its author. Run it before and after a change on the same machine.

`task bench -- -json out.json` writes the results structured if you want to diff
two commits yourself. `-repeat N` keeps the fastest of N runs, on the grounds
that a slow run means something else interfered and there is no such thing as a
spuriously fast one.

## Availability

Timestamps need `timestampComputeAndGraphics` **and** a graphics queue with
non-zero `timestampValidBits`; a device can report the first without the second,
and checking only that is how this silently reports zeros. When either is
missing the timer is inert and `GPUTimings.Valid` is false.

`01-triangle` cannot be measured at all: it drives the renderer with its own
loop rather than `Engine.Run`, so there is no frame loop to instrument. It is
excluded from `task bench` for that reason.
