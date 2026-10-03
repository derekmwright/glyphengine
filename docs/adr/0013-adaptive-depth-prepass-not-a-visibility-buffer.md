# 0013: Shade once per pixel through an adaptive depth prepass, not a visibility buffer

- Status: Accepted
- Recorded: 2026-10-03

## Context

UniverseBuild shades every opaque fragment with a custom `LitFrag` that
integrates the atmosphere per fragment: one texture read, then a 16-step view
march with up to four shadow samples per step and a 6-step sun-transmittance
march. Its own measurements (issue #152: RX 7900 XTX, 3840x2054, 1.33 M
triangles) put the opaque pass at 10.09 ms of a 12.58 ms frame, falling to
3.35 ms at half resolution with the same triangle count. The cost is per
shaded fragment, and overlapping terrain patches shade the same pixel more
than once.

Three mechanisms for reducing that have been built here, measured against a
rule written before each run, and removed by the same clause of that rule,
which required the control scene — one where nothing is hidden — to pay less
than its own within-mode scatter:

| mechanism | saving where work was hidden | unconditional cost on the control | record |
|---|---|---|---|
| front-to-back opaque ordering (#152) | small, eaten by state-change locality | about 1.0 ms | game-loop.md, PR 163 |
| depth prepass (#158) | 61 % of the lit pass, 46 % of `gpu_total` at 4K | 0.555 ms at 4K, 0.925 ms at 720p | game-loop.md, PR 172 |
| hierarchical-Z occlusion (#154) | 26 to 34 % of `gpu_total` | 0.055 to 0.188 ms | lod-instancing.md, PR 174 |

The prepass record is the one that matters for this decision. It delivered
exactly what a visibility buffer promises — each pixel's material evaluated
once, hidden fragments rejected before shading — on a scene of depth
complexity 3.28 at 3840x2160: the opaque pass fell from 7.594 to 2.929 ms and
the frame from 8.156 to 4.350 ms, with the prepass itself costing 0.816 ms. It
was removed because that 0.8 ms is paid by every scene, including the one
with nothing hidden. The record closes by naming the missing piece: a cheap
online estimate of depth complexity, so that the control pays nothing because
the prepass never runs there.

Issue #155 proposes a visibility buffer instead: rasterise opaque geometry
into a target holding a draw id and a triangle id per sample, then one
full-screen pass reconstructs each pixel's attributes and runs the material
once. It is a second shading model beside forward. Materials become a
full-screen stage indexed by id; a custom `LitFrag` needs a second entry point
whose inputs are reconstructed rather than interpolated, and grass,
alpha-tested, skinned and translucent draws stay forward with the existing
contract. Every game that customises shading would carry both. The showcase
runs 4x MSAA, so the id target is multisampled and the resolve shades once per
distinct id per pixel, up to four at edges, or gives up anti-aliased
silhouettes on the geometry that goes through it.

What must remain true: the engine ships mechanism, not opinion (AGENTS.md
rule 14, ADR 0012); a game that does nothing new renders what it rendered
before; the frame is scheduled through the frame graph (ADR 0005, 0009);
repeatable renders stay byte-identical under the fixed clock.

## Decision

**No visibility buffer.** The engine addresses overdraw on expensive
materials by shading once per pixel through the depth prepass, which comes
back from its archive with the piece its record said was missing: an online
estimate of depth complexity that decides, per frame, whether the prepass
runs at all.

The reasons, in the order they weigh:

1. **The saving is already delivered, and measured.** A prepass is the same
   shade-once guarantee for opaque geometry that cannot discard, at the cost
   of submitting that geometry a second time through two depth-only vertex
   stages. 61 % of the lit pass on the overlap scene is the number a
   visibility buffer would have to beat, and it would have to beat it by
   enough to pay for a second shading model.
2. **A visibility buffer has an unconditional cost too, and it falls on the
   same control.** The id target is written for every rasterised sample, the
   resolve is a full-screen pass that fetches three vertices and reconstructs
   barycentrics and derivatives per pixel before the material runs, and under
   MSAA it does so per distinct id. None of that is free on a scene with
   nothing hidden. It is not measured here, and it does not need to be for
   this decision: a mechanism whose fixed cost is a full-screen reconstruction
   pass is not the shape that passes the clause three mechanisms failed, and
   the adaptive prepass is.
3. **The one cost a prepass cannot remove is quad overshading.** Forward
   shading runs the fragment stage in 2x2 quads, so a triangle covering few
   pixels shades helper lanes that a visibility-buffer resolve never would.
   That is the Nanite-scale argument, and it is real at near-pixel triangles.
   The showcase's 1.33 M triangles over 7.9 M pixels, shadows included, is not
   that regime, and the factor has not been measured on this engine. It is
   the condition that reopens this decision, declared now: **if the measured
   shading invocations per covered sample on the showcase-shaped scene exceed
   1.5 with the prepass on**, the visibility buffer is reconsidered against
   that number, because that is the part of the cost a prepass leaves on the
   table and a resolve pass removes.
4. **A second shading model is a contract every customising game carries.**
   The showcase's `LitFrag` would need a resolve-stage twin; so would every
   `x` package that ships a lit material. The prepass changes no shader
   contract: a custom `LitFrag` is unaware of it.

**The adaptive prepass**, as the contract future changes respect:

- `WithDepthPrepass(mode)` on `renderer` and `glyphengine`, with
  `DepthPrepassOff`, `DepthPrepassOn` and `DepthPrepassAuto`, default `Off`;
  reported through `Capabilities.DepthPrepass`. Opt-in, because this record
  is evidence that the mechanism is worth having, not that every game's
  frame should change; whether `Auto` becomes the default is a later
  decision taken on consumer measurements.
- The estimate is the mean screen-space depth complexity of the geometry the
  prepass would touch: over the draws the prepass predicate qualifies, each
  draw's projected bound area clipped to the viewport, summed, divided by the
  area at least one of them covers. It is the same quantity
  `examples/28-overdraw` has always measured offline, and the engine measures it
  by the same method -- a coverage grid, 192 cells per axis, whose union is a
  bitmask and whose numerator is the product of two cell spans -- so no
  per-cell work happens for the sum. CPU microseconds, no GPU readback, no frame
  of latency. `Auto` enables the prepass when the estimate crosses a threshold
  and disables it when it falls below a lower one, so a scene sitting on the
  threshold does not toggle frame to frame.
- **Over the covered area and not over the viewport**, which this record's draft
  had the other way round and measurement corrected. Dividing by the viewport
  needs no grid at all and is O(1) per draw, which is why it was built first.
  Measured on the two arms this whole investigation rests on, in CPU arithmetic
  with `28-overdraw -probe`: the overlap arm reads **0.742** and the control
  **0.576** -- the arm where the prepass saves 46 % of the frame at 4K and the
  arm where it costs 0.555 ms for nothing, 29 % apart. Over the covered area they
  are 3.28 and 1.02, a factor of 3.2. A grazing camera's field covers 23 % of the
  screen, so dividing by the whole viewport divides out most of the signal, and no
  threshold placed in a 29 % gap would survive a different scene.
- The bound is each mesh's own object-space box through its `MVP`, and the box is
  new: `Mesh` carried only a bounding sphere, and a sphere is the wrong bound for
  an AREA. Substituting the sphere's box for the mesh's in the estimate reads
  2.393 against 1.029 on an overhead camera over this geometry and 13.196 against
  3.571 on a grazing one. The alternative the draft named -- the projected radius
  the engine's own frustum cull already derives -- was not taken because that cull
  lives a module away, in a layer a renderer-only program does not run, and this
  decision has to be reachable from the renderer alone, beside the predicate that
  selects the draws it sums over.
- The threshold is **measured, not chosen**: a sweep of depth complexity on
  the overdraw baseline finds where the prepass's net `gpu_total` change
  crosses zero at both bench resolutions, and the threshold sits above that
  crossing by the sweep's own scatter. It is **2.02** with a hysteresis band of
  **0.18**, from crossings at estimate 1.849 at 1280x720 and 1.201 at 3840x2160;
  the sweep is in the evidence section and beside the constant.
- **One threshold, not one per resolution.** `excess = C / viewportPixels` is the
  model the mechanism suggests and the measurement refutes it: C = 782,438 at 720p
  and 1,667,174 at 4K, a factor of 2.13, and the 4K-derived C predicts a 720p
  crossing 5.6x that cell's scatter away from the measured one. The break-even
  depends on the share of the viewport the geometry COVERS as well as on the pixel
  count, because the hidden fragments are `covered x pixels x (complexity - 1)`.
  A coverage-aware threshold is therefore the open refinement, not a resolution-
  aware one, and `RenderStats.PrepassCovered` exists so it can be measured.
- The estimate is per draw, so hidden work inside one draw is invisible to
  it. A single self-overlapping mesh reads as its own bound area over its own
  bound area, which is 1.0. That is a known limitation, recorded on the page, and
  the scene it misses is not the showcase's, whose terrain is many patches.
- An instance set contributes its whole-set bound ONCE, because a set keeps one
  bounding sphere over every placement. A thousand trees filling the screen read
  as one screenful at complexity 1.0 rather than as the overlap they are, so
  `Auto` declines on a dense instanced forest it would have helped. Under-counting
  is the conservative direction -- it costs a saving, not a control -- and it is
  recorded rather than fixed here. Summing per placement would need the per-
  instance transforms back on the CPU, which sets do not keep.
- The ratio says how STACKED the geometry is, not how much of it there is, so a
  frame of sky with one small self-occluding cluster reads as high as a frame
  filled with the same cluster. The prepass's cost scales with the geometry it
  resubmits and its saving with the hidden fragments, and both are small in that
  frame, so the decision is not wrong so much as uncalibrated for it. The covered
  share of the viewport is reported beside the ratio (`RenderStats.PrepassCovered`)
  so a caller can tell the two frames apart; a second threshold on it is the
  refinement if a real scene needs one.
- The decision rule for shipping it, written before the measurement: on
  `examples/28-overdraw` at 1280x720 and 3840x2160, `Auto` on the overlap arm
  recovers at least 90 % of what `On` recovers in `gpu_total`; `Auto` on the
  control arm changes `gpu_total`, `cpu_record` and `cpu_total` by less than
  that cell's within-mode scatter, paired by trial; the estimate reads above
  the threshold on every overlap frame and below it on every control frame;
  `task determinism` captures with `Auto` on are byte-identical; the
  prepass-on and prepass-off frames of the equivalence gate are identical to
  the pixel on every example it covers; the validation and synchronisation
  layers are silent. Otherwise it is removed again and the numbers recorded.

## Alternatives

- **The visibility buffer (#155) now.** Rejected above. Its benefit over a
  prepass is quad overshading, which is unmeasured and declared as the reopen
  condition; its costs are a second shading model and a fixed full-screen
  resolve on every scene.
- **The prepass always on, or on by a game-set flag only.** This is the
  mechanism that was removed: the rule already allowed for it being off by
  default and said that is not what the control measures.
- **A GPU-side estimate**, such as occlusion queries or a fragment counter
  from the previous frame. Exact where the bound sum is a bound, and it sees
  hidden work inside one draw. It costs a query per frame and arrives a frame
  late, so a cut in the scene is one frame of the wrong mode. Not chosen
  first because the CPU estimate is cheap and the showcase's geometry is
  the per-draw case; it is the refinement if the bound estimate proves too
  loose on a real scene. "Costs nothing" is what the draft of this record said,
  and it is not quite true now that the estimate carries a coverage grid: the
  grid is 4.5 KiB, zero allocations, at most 576 word-ORs per draw and the whole
  of it inside `cpu_record`, which is where the second geometry submission's CPU
  price already lands. The two costs of the prepass are therefore one number, and
  the rule below reads it.
- **Hierarchical-Z culling combined with the prepass.** Hi-Z rejects draws,
  the prepass rejects fragments; they are complementary and hi-Z's archive
  is intact. Its 0.055 to 0.188 ms would need the same online gate. A later
  step, if a scene shows draw-level occlusion the prepass leaves on the
  table.
- **Lowering the material's cost instead** (the showcase is moving its
  atmosphere integration to lower resolution). That is the game's lever and
  it composes with this one; the engine's part is to make sure each pixel
  pays that cost once.

## Consequences

- The 49-file prepass archive returns with its four recorded traps already
  solved: the spliced frame-graph node with `FinalLayout` declared, the
  depth-only vertex twins of `lit.vert` rather than `shadow.vert`, the
  push-constant stage mask, and the node-versus-declaration index.
- A custom `LitFrag` is unaffected. The qualifying predicate is one function
  read by both the prepass recorder and the main pass's pipeline choice, and
  a draw it excludes renders as it does today.
- `Auto` adds a per-frame CPU estimate over the qualifying draws, bounded by
  the draw count and the grid rather than by the scene, and measured as part of
  the rule. It is skipped entirely on a `DepthPrepassOff` renderer, which is still
  free in every sense: no estimate, no shader module, no pipeline, no node, and
  the same driver calls a frame records without the option.
- `Mesh` gains an object-space axis-aligned box (`BoundMin`, `BoundMax`) beside
  its bounding sphere, from the same single walk over the vertices. Public, and
  useful beyond this: a sphere is a loose bound for anything flat.
- `renderer.DepthPrepassThreshold()` is exported, because the estimate in the
  stats block cannot be interpreted without it -- and because a gate checking
  that `Auto` decided correctly on a given scene would otherwise hard-code a
  number the sweep is expected to move, which is how a gate stops testing the
  thing it names.
- The frame graph declares the prepass node for every frame of an `Auto`
  renderer, not only the frames it runs on. The scene pass loads depth whenever
  the prepass exists, so something has to clear it, and a plan that varied frame
  to frame would have to be recompiled frame to frame. A declined frame is a node
  that clears depth, submits nothing, and leaves the main pass on the ordinary
  `Greater` variants.
- The reopen condition for the visibility buffer needs a measurement the
  engine could not take when this record was written: shading invocations per
  covered sample. The instrument now exists -- pipeline-statistics queries on
  the same brackets `GPUTimings` records, behind `WithPipelineStatistics` and the
  device's `pipelineStatisticsQuery` feature (#182, `docs/agents/profiling.md`) --
  and it does not settle the question on its own, because whether the counter
  includes a quad's helper lanes is implementation-defined. `cmd/quadcheck`
  establishes that from geometry whose quad factor is known, and the measurement
  below is not evidence until it has. If helpers turn out not to be counted on a
  machine, the counter cannot see quad overshading at all and the
  `gl_HelperInvocation` route is the only one left; that one needs a writable
  storage buffer in a graphics pass, which `AppPassDesc` does not offer today.
- #155 closes with this record; #158 reopens as the adaptive prepass; #154
  stays closed with its archive, referenced above.

## References and evidence

- Issues #152, #154, #155, #158; PRs 163, 172, 174 (the three evaluation
  records); ADR 0005, 0006, 0009, 0012.
- `docs/agents/game-loop.md`: "The draw list and its order", "Opaque order
  inside a group was measured, and left alone", "A depth prepass was measured
  and removed" (the table, the verdict, the rebuild recipe).
- `docs/agents/lod-instancing.md`: the hierarchical-Z record.
- `examples/28-overdraw` (hidden fragments, measured depth complexity),
  `examples/29-ridge` (hidden draws), `task bench -- -scene overdraw`.
- Existing evidence: every number in the Context table is from those records.
- Checks run for this change: the sweep, the threshold, and the decision rule's
  cells are recorded below when the measurement is made; nothing is recorded
  here before it is.
- Validation still needed: the HELPER-LANE half of the quad-overshading factor.
  The rest is measured below, and the instrument cannot see that half on this
  machine; see the verdict and the gap it names.

### Evidence recorded with the implementation

Measured on this machine: AMD Radeon RX 7900 XTX, `examples/28-overdraw`, 200
frames per sample under `GLYPHENGINE_FIXED_FRAME_TIME=16.667ms`, three trials per
cell, arms interleaved inside each cell with every sample retained, nothing else
on the GPU. Background GPU 3D utilisation was sampled every two seconds for the
whole session and every run was matched to its own wall-clock bracket: baseline
6.2 to 6.6 %, maximum during any run 7.7 %, discard limit 11.6 %. **One run was
discarded and re-run** -- a 4K sweep cell that overlapped a compile on this
machine, which took 17 s against the usual 11; its replacement read -3.644 ms
against the discarded -3.615, inside scatter.

#### The threshold sweep

Nine camera placements on the grazing layout, chosen with `28-overdraw -probe`
(which needs no GPU), prepass off and on interleaved inside each cell.
`gpu_total` net change, in milliseconds:

| estimate | covered | 1280x720 net | scatter | 3840x2160 net | scatter |
|---:|---:|---:|---|---:|---|
| 3.12 | 0.22 | -0.218 | 0.127 | -3.698 | 0.475 |
| 2.81 | 0.285 | -0.584 | 0.096 | -3.644 | 0.414 |
| 2.40 | 0.465 | -0.360 | 0.223 | -1.017 | 0.223 |
| 2.18 | 0.541 | -0.459 | 0.152 | -0.502 | 0.210 |
| 1.92 | 0.646 | -0.130 | 0.314 | -0.446 | 0.165 |
| 1.71 | 0.771 | **+0.277** | 0.331 | -1.233 | 0.168 |
| 1.52 | 0.982 | +0.454 | 0.524 | -1.163 | 0.304 |
| 1.36 | 0.948 | +0.633 | 0.637 | -0.528 | 0.227 |
| 1.15 | 0.456 | +1.529 | 0.544 | **+0.143** | 0.592 |

Crossings: **1.849** at 1280x720, bracketed by 1.706 (+0.277) and 1.916 (-0.130);
**1.201** at 3840x2160, bracketed by 1.158 (+0.143) and 1.359 (-0.528). The
threshold is the higher crossing plus that bracket's scatter expressed on the
estimate axis (0.331 ms against a -1.938 ms slope per unit = 0.171), so 2.02.

The 4K column is **not monotone** in the estimate, and that is the single most
useful thing the sweep found: the saving grows again at 1.71 and 1.52, where
coverage is 0.77 and 0.98, after shrinking at 1.92 where it is 0.65. Coverage and
complexity both vary along this ladder, which is what refutes `C/pixels` and what
a future coverage gate would act on.

#### The decision rule, applied

`gpu_total`, six cells, three trials, paired by trial:

| resolution | arm | off | On | Auto | On recovers | Auto recovers |
|---|---|---|---|---|---:|---:|
| 1280x720 | overlap | 2.116 | 1.888 | 1.900 | +0.228 | +0.216 (94.7 %) |
| 1280x720 | control | 7.375 | 8.804 | 7.160 | -1.431 | +0.215 |
| 3840x2160 | overlap | 7.578 | 4.095 | 4.274 | +3.483 | +3.304 (94.9 %) |
| 3840x2160 | control | 9.827 | 10.186 | 9.739 | -0.360 | +0.088 |

1. **Pass** -- 94.7 % and 94.9 % against a 90 % bar, paired deltas agreeing trial
   for trial.
2. **Pass**, and this is the clause the three earlier mechanisms failed. Auto moves
   the control's `gpu_total` by -0.215 ms against 0.689 scatter and -0.088 against
   0.311; `cpu_record` by +0.538 against 0.944 and +0.177 against 0.761;
   `cpu_total` by +0.093 against 0.111 and -0.013 against 0.066. `gpu_prepass` on
   the Auto control arm is 0.006 ms, the inactive node's depth clear.
3. **Pass** -- 199 frames sampled per run, `PrepassEstimate` min equal to max in
   every one; overlap 3.117 and 3.129 above 2.02 with 199/199 frames active,
   control 1.022 and 1.014 below the 1.84 off edge with 0/199.
4. **Pass** -- `task determinism` capture pairs identical for `08-grass -prepass
   on`, `24-custom-passes -prepass on` and `28-overdraw -prepass auto`, the last
   with its environment hash and draw sequence identical as well; the gate's own
   controls fired.
5. **Pass** -- `task prepass`, all six examples byte-identical off and on at 1, 6,
   2, 1, 4 and 5 prepass draws, the same counts the removal record reports; the
   withheld-draws control moved pixels on all six; both Auto arms of
   `28-overdraw` identical to prepass-off.
6. **Pass** -- `task validate` (102 runs, all eight prepass arms) and
   `task syncvalidate` silent. `task screenshots` changes no image.

**Verdict: ship.** All six clauses pass. `Off` stays the default; whether `Auto`
becomes one is the later decision this record already reserved.

#### What passing does not mean

- **The fixed threshold leaves measured saving on the table at 4K.** Break-even
  there is 1.201 and the threshold is 2.02, so the measured cells at 1.359, 1.518,
  1.710 and 1.922 forgo 0.528, 1.163, 1.233 and 0.446 ms -- up to 10.6 % of an
  11.6 ms frame. The rule has no clause about this; it is the largest known cost of
  the design and the reason a coverage-aware threshold is the next measurement.
- **The estimate is not free.** 94 ns per qualifying draw: 96 us over this field's
  1024 draws, 328 us in a pessimistic shape. Inside clause two's scatter, but
  `cpu_record` rose on all six paired control trials, which these records treat as
  real rather than noise. The draft of this ADR said a CPU estimate "costs
  nothing"; it does not.
- **The quad-overshading reopen condition was unmeasured when this was written**,
  and the section below is what came of measuring it: a lower bound of 1.335 to
  1.351 on the showcase-shaped scene against a threshold of 1.5, and a counter that
  cannot see the helper lanes the condition is actually about. The decision still
  rests where it did -- on a prepass delivering the shade-once guarantee without a
  second shading model, which that section now measures exactly at 1.0000 -- and
  not on a comparison against a visibility buffer.

#### Corrections this record made to its own draft

1. The estimate normalises by the **covered area**, not the viewport. Measured with
   `28-overdraw -probe`: the viewport form reads 0.742 on the overlap arm and 0.576
   on the control -- 29 % apart, where the covered form puts them 3.2x apart. Auto
   built to the draft could not have told the two arms apart.
2. The bound is each mesh's object-space **box**, not the frustum cull's projected
   radius: the cull is a module away in a layer a renderer-only program never runs.
   `Mesh` gained `BoundMin`/`BoundMax` because a bounding sphere's box reads 2.393
   against 1.029 on an overhead camera over this geometry.
3. `excess = C / viewportPixels` is refuted, as above.
4. A CPU estimate is cheap, not free.

#### Quad overshading, against the reopen condition

Measured on this machine: AMD Radeon RX 7900 XTX, 200 frames per sample under
`GLYPHENGINE_FIXED_FRAME_TIME=16.667ms`, three trials per cell, arms interleaved
inside each trial, nothing else on the GPU. Background GPU 3D utilisation was
sampled before and after every trial: baseline 6.5 to 8.1 %, 6.5 to 7.1 % during
the runs, discard limit 13.1 %. **No run was discarded.** The counters are exact
rather than sampled, and all three trials of every cell below returned
bit-identical counts, so the ratios carry no scatter at all; `gpu_opaque` does,
and its spread is in the report.

##### What the counter counts here, and what that costs

`task quads` establishes it from geometry whose quad factor is known in advance,
six arms, every one exact:

| arm | MSAA | prepass | triangles | covered px | invocations | per covered px |
|---|---:|---|---:|---:|---:|---:|
| full-screen triangle | 1 | off | 1 | 307,200 | 307,200 | **1.0000** |
| full-screen triangle | 4 | off | 1 | 307,200 | 307,200 | **1.0000** |
| one-pixel triangles | 1 | off | 19,200 | 19,200 | 19,200 | **1.0000** |
| full-screen triangle | 1 | on | 1 | 307,200 | 307,200 | 1.0000 |
| full-screen triangle | 4 | on | 1 | 307,200 | 307,200 | 1.0000 |
| one-pixel triangles | 1 | on | 19,200 | 19,200 | 19,200 | 1.0000 |

A field of 19,200 triangles each covering one pixel reads exactly one invocation
each, where a counter including helper lanes would read four.
**`FRAGMENT_SHADER_INVOCATIONS` excludes helper invocations on this device.**
Three consequences, and the third decides what this section can and cannot say:

1. **The denominator is covered pixels, at every sample count.** The full-screen
   arm reads 1.0000 at 4x as well as at 1x, so a fragment runs once per covered
   pixel per primitive and not once per sample — the engine enables no sample
   shading, and this is the measurement rather than the assumption.
2. **The counter is exact.** 307,200 is 640x480 and 19,200 is the triangle count,
   to the unit.
3. **It cannot see quad overshading.** Helper lanes are precisely what clause 3 of
   this decision is about, and they are not counted here. What is counted is the
   non-helper multiplicity: a **lower bound** on invocations per covered sample,
   and not the component a visibility-buffer resolve removes — under MSAA a
   resolve shades once per distinct id per pixel too, up to four at edges, as this
   record says above. So **the reopen condition is undecidable by this
   instrument**, and the table below is a lower bound and a regime, not the
   condition's number.

The second route this record named — a `gl_HelperInvocation` counter — is not
built here, and it is blocked on an engine gap rather than on effort:
`AppPassDesc` has no `Buffers` field, so a graphics pass cannot write a storage
buffer or a storage image at all; those are exposed to compute only
(`docs/agents/render-targets.md`). That gap is the follow-up.

##### The three scenes

Invocations per covered pixel, and triangles after clipping per covered pixel,
with the prepass off and on. `-sky=false` on every arm, so the frame keeps its
flat clear colour and the pixel count is a count of geometry.

| scene | res | MSAA | tri/covered px | inv/covered px, prepass off | prepass on | gpu_opaque off (ms) | on (ms) |
|---|---|---:|---:|---:|---:|---:|---:|
| 28-overdraw overlap | 1280x720 | 1 | 20.08 | 1.8588 | **1.0000** | 1.252 | 0.904 |
| 28-overdraw overlap | 1280x720 | 4 | 18.88 | 5.6259 | **2.6215** | 3.112 | 1.450 |
| 28-overdraw overlap | 3840x2160 | 1 | 2.28 | 1.8602 | **1.0000** | 4.566 | 1.964 |
| 28-overdraw overlap | 3840x2160 | 4 | 2.23 | 4.4261 | **1.9429** | 8.441 | 3.822 |
| 28-overdraw at showcase density | 1280x720 | 1 | 0.183 | 1.8629 | **1.0000** | 0.475 | 0.388 |
| 28-overdraw at showcase density | 1280x720 | 4 | 0.173 | 2.8702 | **1.3353** | 0.597 | 0.416 |
| 28-overdraw at showcase density | 3840x2160 | 1 | 0.182 | 1.8576 | **1.0000** | 2.240 | 1.223 |
| 28-overdraw at showcase density | 3840x2160 | 4 | 0.178 | 2.9350 | **1.3507** | 3.700 | 1.488 |
| 29-ridge (upper bound) | 1280x720 | 1 | 0.479 | 25.7309 | n/a | 0.661 | n/a |
| 29-ridge (upper bound) | 1280x720 | 4 | 0.479 | 26.5186 | n/a | 0.766 | n/a |
| 29-ridge (upper bound) | 3840x2160 | 1 | 0.055 | 21.1669 | n/a | 4.029 | n/a |
| 29-ridge (upper bound) | 3840x2160 | 4 | 0.055 | 21.0886 | n/a | 4.066 | n/a |

**29-ridge is an upper bound and nothing else.** The prepass declines every draw
in it — an instanced LOD set and a double-sided draw are both excluded by
`depthPrepassQualifies`, and the terrain has its own pipeline — so nothing removes
its hidden fragments, and its 21 to 27 is shaded depth complexity rather than any
kind of quad factor. The forest's LOD fade and impostors discard, which defeats
early-Z, so every overlapping layer shades. It is in the table because the
condition names the scene, and it bounds nothing from below.

**The showcase-shaped arm is the one the condition is about.** UniverseBuild is
1.33 M triangles over 3840x2054, which is 0.169 triangles per viewport pixel;
`28-overdraw -density 0.17` reaches **0.173 to 0.183** per covered pixel, within
8 % of it, at `-grid 4` (1280x720) and `-grid 10` (3840x2160). Reaching it meant
**coarsening** the field, not subdividing it: the overdraw baseline's own
tessellation is 20.08 triangles per covered pixel at 720p against this arm's 0.173
on the same camera, **116 times** the density -- worth knowing about every number
ever measured on that scene.

##### What the numbers say

- **At MSAA 1 with the prepass on, the ratio is exactly 1.0000 on every arm.**
  77,740 invocations over 77,740 covered pixels; 682,247 over 682,247. The prepass
  delivers the shade-once guarantee exactly, measured in fragments rather than
  inferred from time. That is new evidence for this decision's first reason, and
  it is the strongest form the claim can take.
- **The prepass removes 46 % of shaded fragments at MSAA 1** (1.86 to 1.00) and
  53 to 56 % at MSAA 4 (5.63 to 2.62, 4.43 to 1.94), against the 61 % of lit-pass
  TIME the removal record measured. Two different quantities that agree in size.
- **The measured excess over 1.0 is entirely an MSAA effect, and it scales with
  triangle density.** At MSAA 1 it is exactly zero on every arm, because one
  sample per pixel admits one primitive. At MSAA 4 a pixel straddling a primitive
  edge is shaded once per covering primitive, and the excess runs 0.34 at 0.18
  triangles per covered pixel, 0.94 at 2.2 and 1.62 at 18.9. It is not quad
  overshading, and a visibility-buffer resolve under MSAA pays it too.
- **The bound-area estimate over-states real depth complexity by about 1.8x.**
  `PrepassEstimate` reads 3.28 on the overlap arm where the shaded fragments per
  covered pixel are 1.86. Both numbers are right about their own quantity — one is
  a ratio of projected bound areas, the other a count of fragments — and the
  threshold is calibrated against the first. Worth knowing before anyone reads the
  estimate as a fragment count.

##### Verdict

**Undecidable by this instrument, and not met by the lower bound.**

- The lower bound on invocations per covered sample on the showcase-shaped scene,
  with the prepass on and at the showcase's 4x MSAA, is **1.335 at 1280x720 and
  1.351 at 3840x2160** — below the 1.5 the condition names. At MSAA 1 it is
  exactly 1.000.
- A literal reading of the condition — "measured shading invocations per covered
  sample exceed 1.5" — is therefore **not** satisfied on the showcase-shaped
  scene, and #155 does not reopen on this evidence.
- The condition's intent is the helper-lane component, and **this instrument
  cannot measure it on this device**. The lower bound leaves 0.165 of head-room
  before 1.5, and at 5.6 covered pixels per triangle the unmeasured component is
  not obviously smaller than that — a triangle of that size touches four to six
  quads by arithmetic, which is an argument and not a measurement, and this record
  does not treat it as one. So "not met" is a statement about the lower bound and
  not a finding that quad overshading is small.
- **The decision stands, and its evidence is not complete.** It rests where it
  did: on a prepass that delivers the shade-once guarantee — now measured exactly,
  at 1.0000 — without a second shading model. The condition is retired from
  "unmeasured" to "unmeasurable with what the engine can read", and what it needs
  is the storage-buffer gap above.
- On the overdraw baseline's own tessellation the lower bound DOES exceed 1.5 at
  MSAA 4 (2.62 at 720p, 1.94 at 4K). That scene is 12 to 116 times the showcase's
  triangle density, so it is not the scene the condition names, and the excess
  there is the MSAA edge multiplicity a resolve pass would also pay. It is in the
  record because it is the shape the condition was worried about, measured.

#### Validation still needed

- The helper-lane component of quad overshading, which is the half of the reopen
  condition this machine's counter cannot see. It needs a `gl_HelperInvocation`
  atomic from a fragment stage, and that needs a storage buffer or storage image
  bindable by a GRAPHICS application pass -- `AppPassDesc` has no `Buffers` field,
  and both are exposed to compute only. Until then the condition has a measured
  lower bound of 1.335 to 1.351 on the showcase-shaped scene and no upper one.
- A coverage-aware threshold, against the non-monotone 4K column above.
- Consumer measurements, before `Auto` could become the default.
