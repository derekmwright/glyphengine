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
  - renderer.OpenStateTrace
  - renderer.Renderer.SetStateTrace
  - renderer.Hasher
  - renderer.NewHash
  - renderer.HashPOD
assets: none
run: task determinism
verified: 2026-09-18
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
| `sky` | Time of day, sun direction and elevation, ambient, fog, shadow enable |
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
| `lights` | Clustered light count and the whole binning |
| `grass` | Grass tile draw count and the ordered (variant, range, instance count) sequence |
| `outcome` | What the iteration did — see below |

`outcome` is the field that catches a frame which was simulated but never drawn:

| Value | Meaning |
|---|---|
| `present` | Recorded, submitted, presented; per-frame state advanced |
| `skip-minimized` | Zero-sized framebuffer. The tick ran; nothing was drawn, and `frameCount` did not move |
| `skip-acquire-out-of-date` | The acquire failed; the swapchain was rebuilt and this frame was never recorded |
| `present-recreate` | Drawn and presented, but the swapchain was rebuilt afterwards, so `cloudframe`, `prevVP` and the frame-in-flight slot did **not** advance |
| `*-error` | A Vulkan call failed at that stage |

## Reading a diff

The pairing of fields is the point.

- `draws` differs, `drawsset` matches — the same geometry arrived in a
  different sequence. The draw list is built by an ECS query that walks a Go
  map, so this happens on **every** frame of **every** run; what matters is
  whether it survives the sort into something the image depends on.
- `clock` differs — the frame loop ran a different number of times, or ticked a
  different number of times. Check `loop` against `rendered` and `ticks`.
- `cam` differs with `clock` identical — the camera moved under an identical
  clock: interpolation, a follow that read a different transform.
- `sim` fields all match and `grass`, `particles` or `dynmesh` differ — the
  simulation was identical and the renderer derived different GPU state from
  it. That is a much narrower bug than the other way round.
- `outcome` differs — one run skipped or rebuilt where the other did not. This
  is the one that needs no further hashing to be a finding.

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

Read that table as three findings. A wall-clock stall changes nothing, which is
what a fixed clock is for. Reversing the draw list changes `draws=` and not the
image, so the ECS map walk's randomness is latent rather than live — worth
knowing, not worth a fix. And a frame the renderer did not draw, or a swapchain
rebuilt underneath one it did, changes the picture by an amount that depends
entirely on how late it happened: a rebuild on the last frame of `08-grass`
moved 28 % of the pixels.
