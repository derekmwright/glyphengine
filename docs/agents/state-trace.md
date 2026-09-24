---
id: state-trace
title: Finding a render that differs run to run
summary: >
  Write one hashed line per frame-loop iteration, then diff two runs to find
  the first frame and the first subsystem that diverged -- instead of guessing
  which of a dozen plausible causes it was.
capability: meta
status: stable
since: v0.4.0
api:
  - renderer.StateTrace
  - cmd/tracefield
  - renderer.OpenStateTrace
  - renderer.Renderer.SetStateTrace
  - renderer.Hasher
  - renderer.NewHash
  - renderer.HashPOD
assets: none
run: task determinism
verified: 2026-09-24 # grassbake, grassatlas, grasspc, config and the prime provocation
---

# Finding a render that differs run to run

`GLYPHENGINE_FIXED_FRAME_TIME` is supposed to make two runs of the same build
produce the same bytes, and `task determinism` gates on it. When it does not,
the symptom is one wrong PNG and nothing else — no crash, no validation error,
no log line — and the list of things that could have done it is long.

Set `GLYPHENGINE_STATE_TRACE` to a path and each iteration of the frame loop
writes one line describing what that iteration did and what it fed the GPU.
Two such files diff cleanly, and the first differing line names the frame while
the first differing field names the subsystem.

```
cd examples
export GLYPHENGINE_FIXED_FRAME_TIME=16.667ms
GLYPHENGINE_STATE_TRACE=a.trace go run ./08-grass -frames 150 -screenshot a.png
GLYPHENGINE_STATE_TRACE=b.trace go run ./08-grass -frames 150 -screenshot b.png
diff a.trace b.trace | head
```

Diffing the whole file is right when you are hunting; it is wrong in a gate,
because `slot=` and `image=` name the frame-in-flight and the swapchain image
the acquire returned and both legitimately differ between two runs that drew
the same frame. To assert on one field, pull it out first:

```
go run ../cmd/tracefield -key draws -in a.trace -out a.draws
go run ../cmd/tracefield -key draws -in b.trace -out b.draws
diff a.draws b.draws
```

`tracefield` exits non-zero if the field is on no line, so a gate built on it
cannot pass by comparing two empty files. `task determinism` uses it on
`draws=` over thirteen scenes.

## Asserting about ONE run

`-constant` asks a different question from determinism: that a field holds the
same value on every frame of a single run. Two runs that both go wrong in the
same place diff clean, so `diff` cannot ask it at all.

```
go run ../cmd/tracefield -key draws -count -constant -in r.trace
go run ../cmd/tracefield -key draws -constant -skip 12 -in r.trace
```

`-count` keeps only the count from a `count/hash` field (`draws=8/1a2b…` →
`8`), which is a much weaker claim than the whole field and exactly the one
`task reload` wants: a level that vanishes for a frame while it is being
swapped for a freshly loaded copy is a frame with fewer draws in it, and
nothing else in this repo would notice. `-skip N` drops the first N lines,
for a steady state the run takes a few frames to reach — `22-level`'s camera
settles over about a dozen, and every draw's MVP moves with it. `-skip` that
would leave nothing to compare is an error rather than a pass.

A gate using `-constant` should prove it can fail first. `task reload` runs it
against `cam=`, which demonstrably moves over those same first frames.

It is an environment variable rather than an `Option` for the same reason
`GLYPHENGINE_FIXED_FRAME_TIME` and `GLYPHENGINE_VALIDATION` are: the run that
needs tracing is usually an example or a game you did not compile, and
recompiling it to add a flag is the step that does not happen.

## What a line says

```
loop=1 ticks=1 clock=45c6c6… cam=9d2328… sky=e290ea… draws=1/947469… drawsset=947469…
overlays=0/cbf29c… celestials=1/4317ee… msdf=1/f08f16… uioverlays=0
slot=0 w=1280 h=720 cloudframe=0 image=0 dynmesh=1/a6445a… dynmeshorder=e6dfaf…
cascades=71e68c… lights=0/e79af2… grass=124/82b4a2… outcome=present rendered=1
```

| Field | What it is |
|---|---|
| `loop` | Frame-loop iterations so far, **including ones that render nothing** |
| `rendered` | Frames actually presented — `WithMaxFrames` counts these |
| `ticks` | Fixed simulation ticks so far |
| `clock` | `elapsed`, interpolation `alpha`, `timeScale`, the accumulator |
| `cam` | Eye, centre, up, and the view / projection / view-projection matrices |
| `sky` | Time of day, sun direction and elevation, ambient, fog, shadow enable, and the scattering medium (`Scene.Volumetrics`) the fog doubles as |
| `draws`, `overlays`, `celestials`, `msdf` | Count and **order-dependent** hash of each draw list |
| `drawsset` etc. | The same per-draw hashes XORed, so order does not affect it |
| `uioverlays` | Count of UI overlay draws |
| `slot` | Frame-in-flight index this frame's buffers belong to |
| `image` | Swapchain image the acquire returned |
| `w`, `h` | Swapchain extent |
| `cloudframe` | The cloud history counter, which only advances on a full present |
| `particles`, `particlesbehind` | Staged instance count and hash, and how many are behind water |
| `dynmesh` | Dynamic-mesh count and order-independent content hash |
| `dynmeshorder` | The order the dynamic-mesh **map** was walked in |
| `cascades` | The shadow cascade view-projections |
| `lights` | Clustered light count and the whole binning -- the uploaded `GpuLight` array is hashed as bytes, so per-light fields like `Volumetric` are covered without listing them |
| `grass` | Grass tile draw count and the ordered (variant, range, instance count) sequence |
| `grasslod` | The live `GrassLOD`, which `SetGrassLOD` can move at any time |
| `grassbake` | Total instances scattered, and a hash of every variant's uploaded instance array and tile table — what the scatter **built**, as against what a frame drew |
| `grassatlas` | The baked impostor atlas, read back and hashed. `0` means the readback failed and the field says nothing |
| `grasspc` | The 256-byte push block the grass mesh draws were recorded against |
| `config` | The **negotiated** device and swapchain: MSAA sample count, swapchain format, image count and extent, depth format, frames in flight, and the GPU's name, driver version, vendor/device id and pipeline cache UUID |
| `post` | Exposure, tonemap curve and white point, and the four bloom knobs |
| `outcome` | What the iteration did — see below |

`post` and `grasslod` are there because a game — or a stray keypress under
`WithDebugKeys`, which toggles bloom and cycles the tonemap curve — can change
them mid-run. Without them, every other field would match while the whole frame
came out different, which is the worst kind of divergence to be handed.

### Four fields that never move inside one run

`grassbake`, `grassatlas`, `grasspc` and `config` are constant for the whole of
a normal run, and are written on every line anyway. That is not redundancy: the
trace is diffed **between** two runs, and a value that cannot move inside one
run can still be the value that differs between two.

Everything the trace carried before them was per frame, which quietly narrowed
what "the two traces are identical" could mean. It meant the simulation, the
draw list and the streamed buffers agreed. It did not mean the grass was
scattered the same way, that the impostor atlas baked the same way, that the
grass pass was pushed the same 256 bytes, or that the two processes were even
talking to the same driver:

- `grassbake` separates *built differently* from *drawn differently*. `grass=`
  records the tile draw sequence, so two scatters that disagree about where
  blades stand and agree about how many fall in each tile produce the identical
  field.
- `grassatlas` covers what a far tile actually is. Past
  `GrassLOD.ImpostorDistance` a grass pixel is the atlas and nothing else, and
  the atlas is baked at load from a pipeline whose first compile in a session
  may take a different path. It is hashed over the 8-bit readback, so the
  instrument's floor is 1/255 per channel, and it is only filled in when a
  trace is being written — the readback costs a device wait.
- `grasspc` is what was pushed, not what went into it. `sky=`, `lights=` and
  `grasslod=` each cover part of that block; none covers the block.
- `config` is the one that can differ for reasons outside the program. MSAA is
  halved until the device supports it, and grass dissolves through
  alpha-to-coverage rather than blending, so a run that negotiated a different
  sample count moves the grass and leaves everything else alone. Measured on
  `08-grass -timeofday 0.0 -frames 150`, 4x against 1x: 199621 of 921600
  pixels (21.66 %), max delta 47/255; 4x against 2x, 184861 pixels (20.06 %),
  max delta 38. With the grass culled away (`-grassdist 0.5`) the same 4x/1x
  pair moves 1907 pixels (0.21 %) at max delta 14 — so 99 % of it is the
  grass, and the sky, the terrain and the HUD are not in it. The driver
  identity is in there for the same reason: a driver update between two runs
  is exactly the kind of "first run of a session" difference that leaves every
  other field in this file agreeing.

None of them reproduced issue #40 on the machine they were added on; what they
change is that the next sighting is a diff rather than an argument.

The harness that measured that is `tools/firstrun-repro.sh <cycles> <label>
<example> [args]`: each cycle forces a real rebuild of the renderer package,
captures the example twice back to back with a trace beside each capture, and
compares the captures with `cmd/pngsame`; `REPRO_NOBUILD`, `REPRO_VALIDATION`,
`REPRO_FOREGROUND`, `REPRO_CLEARCACHE`, `REPRO_FRAMES`, `REPRO_RUNS` and
`REPRO_PRE` vary one thing at a time. Ten cycles of `08-grass -timeofday 0.0`
per variant and of `12-particles`, 75 cycles and 160 captures in all, gave
zero differing pixels and 72 byte-identical trace pairs on the machine above.
Diff `config=` first when it recurs, then `grassatlas=`, `grassbake=` and
`grasspc=`; if all four agree and the pixels differ, the divergence is below
the CPU and the next run is the same capture under the validation layer and
`task syncvalidate`.

`outcome` is the field that catches a frame which was simulated but never drawn:

| Value | Meaning |
|---|---|
| `present` | Recorded, submitted, presented |
| `skip-minimized` | Zero-sized framebuffer. The tick ran; nothing was drawn, and `frameCount` did not move |
| `skip-resized` | The swapchain was rebuilt at a new extent, so this frame — built for the old one — was dropped |
| `present-recreate` | Drawn and presented, and the swapchain rebuilt afterwards |
| `*-error` | A Vulkan call failed at that stage |

A line also carries `provoked=out-of-date` when
`GLYPHENGINE_PROVOKE_SKIP_FRAMES` named that frame, so a provoked run's trace
says so rather than looking like a spontaneous one.

An out-of-date acquire has no outcome of its own because it no longer costs the
frame: the swapchain is rebuilt and the frame is drawn into it, ending at
`present`. Only a rebuild that changed the extent gives `skip-resized`.

## Reading a diff

The pairing of fields is the point.

- `draws` differs, `drawsset` matches — the same geometry arrived in a
  different sequence. **This should no longer happen for an unchanged scene**,
  and `task determinism` gates on it: the draw list is assembled by an ECS
  query that walks a Go map, so it *arrives* in a different order on every
  frame of every run, but the sort it goes through has a total order, so what
  comes out is a function of the scene. If this field differs between two runs
  under a fixed clock, the sort has lost its tiebreak or something is feeding
  it a value that varies per process. That was issue #53, where the low bits of
  the sort key were a descriptor set address.
- `clock` differs — the frame loop ran a different number of times, or ticked a
  different number of times. Check `loop` against `rendered` and `ticks`.
- `cam` differs with `clock` identical — the camera moved under an identical
  clock: interpolation, a follow that read a different transform.
- `sim` fields all match and `grass`, `particles` or `dynmesh` differ — the
  simulation was identical and the renderer derived different GPU state from
  it. That is a much narrower bug than the other way round.
- `dynmeshorder` differs — the `r.dynamicMeshes` map was walked in a different
  order. Still expected, and still harmless: each entry's flush is independent,
  so the field records the walk rather than asserting anything about it. It is
  the one map order in the trace that was *not* fixed, because there is nothing
  to fix.
- `outcome` differs — one run skipped or rebuilt where the other did not. This
  is the one that needs no further hashing to be a finding.

- **Two traces identical, two captures not.** Everything the CPU handed the GPU
  matched. The difference is below this instrument: the driver, the GPU, or
  something the renderer derives inside the command recorder that no field
  covers. That is a finding, not a dead end — it rules out the whole simulation
  and every streamed buffer in one step.

Nothing in a line is a pointer or a Vulkan handle, deliberately. Both differ
between two runs of the same build for reasons that have nothing to do with the
image, and a trace whose every line differs reports nothing.

## What it costs when off

Nothing worth measuring. A disabled trace is a nil `*renderer.StateTrace`;
every method returns on a nil check, and every caller guards the hashing on the
same nil, so no hash is computed either.
`TestStateTraceDisabledAllocatesNothing` holds the allocation count at zero —
breaking it (formatting an argument before the nil check) takes it to 1
alloc/op and the test reports it. The `screenshots` set is byte-identical with
the instrument present and disabled.

With it on, every streamed buffer is hashed on the frame path once per frame.
That is a diagnostic cost, not a shipping one; do not leave it set.

## Provoking a divergence

Waiting for an intermittent bug is not a method. These variables inject, on a
frame you name, the things a run normally suffers by accident. A capture taken
under any of them must be byte-identical to one taken without, under a fixed
clock — anything else is a determinism bug whether or not it is the one that
was sighted.

| Variable | What it does |
|---|---|
| `GLYPHENGINE_PROVOKE_SKIP_FRAMES=3,40` | Makes `DrawFrame` take the out-of-date-acquire path on those loop frames |
| `GLYPHENGINE_PROVOKE_RECREATE_FRAMES=149` | Forces a swapchain rebuild after the present on those loop frames |
| `GLYPHENGINE_PROVOKE_DRAW_ORDER=reverse` | Reverses the draw list before it is sorted — a permutation the ECS map walk could legally have produced |
| `GLYPHENGINE_PROVOKE_STALL=20:40ms` | Sleeps inside the first N loop frames: a cold GPU, a compile finishing, another process on the machine |
| `GLYPHENGINE_PROVOKE_PRIME=0.5` | Clears the cloud history and the bloom chain to that grey instead of to black when they are created |

Frame numbers are 1-based and count every iteration, so they line up with the
state trace's `loop=` field.

What they found, measured on this repo before the fixes that followed
(1280x720, `GLYPHENGINE_FIXED_FRAME_TIME=16.667ms`):

| Provocation | Scene | Pixels changed | Max delta |
|---|---|---|---|
| `SKIP_FRAMES=3,40,97` | `08-grass -timeofday 0.0 -frames 150` | 55 (0.01 %) | 1 |
| `SKIP_FRAMES=149` | `08-grass -timeofday 0.28 -frames 150` | 12986 (1.41 %) | 2 |
| `RECREATE_FRAMES=40` | `08-grass -timeofday 0.0 -frames 150` | 1685 (0.18 %) | 1 |
| `RECREATE_FRAMES=149` | `08-grass -timeofday 0.28 -frames 150` | 257601 (27.95 %) | 104 |
| `RECREATE_FRAMES=89` | `09-water -frames 90` | 234723 (25.47 %) | 114 |
| `STALL=20:40ms` | `08-grass -timeofday 0.0 -frames 150` | none | — |
| `DRAW_ORDER=reverse` | `18-translucent`, `15-kitchen-sink -demo`, `09-water -plume -ghost -marker -submerged` | none | — |
| `PRIME=0.5` | `08-grass -timeofday 0.0`, at 1, 3 and 150 frames | none | — |
| `PRIME=0.5` | `12-particles`, at 3 and 90 frames | none | — |

Read that table as three findings. A wall-clock stall changes nothing, which is
what a fixed clock is for. And a frame the renderer did not draw, or a swapchain
rebuilt underneath one it did, changes the picture by an amount that depends
entirely on how late it happened: a rebuild on the last frame of `08-grass`
moved 28 % of the pixels.

Reversing the draw list was the third, and it has since been fixed rather than
merely recorded. It changed `draws=` and not the image, which said the map
walk's randomness was latent — and the same measurement said that two ordinary
runs of the same build differed the same way, in eight of the thirteen scenes
the determinism gate covers. Latent is not harmless: two blended draws at the
same depth would have picked an order per frame. The sort now has a total order
(`Engine.sortDraws`), so reversing the list produces the identical sequence and
not merely the identical picture. `TestReversedInputSortsToTheSameSequence`
asserts that without a GPU; the provocation stays in the gate as the end-to-end
half.
