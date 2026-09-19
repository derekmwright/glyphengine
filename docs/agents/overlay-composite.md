---
id: overlay-composite
title: Where screen-space overlays are drawn
summary: >
  UI panels and MSDF text are composited onto the swapchain after the tonemap,
  not into the scene target with the geometry. That is what keeps a HUD out of
  water refraction, scene exposure and bloom.
capability: rendering
status: stable
since: v0.5.0
api:
  - renderer.PanelFill
  - renderer.NineSlice
  - glyphengine.Engine.SetUIOverlays
  - glyphengine.Engine.SetMSDFOverlays
  - glyphengine.Engine.SetOverlays
  - glyphengine.Engine.Debugf
  - glyphengine.WithUIGlow
  - renderer.WithUIGlowLayer
  - renderer.Renderer.SetUIGlow
  - renderer.Renderer.UIGlow
  - renderer.Renderer.SetUIExposure
  - renderer.Renderer.UIExposure
  - renderer.Renderer.UIGlowLayer
  - renderer.UIRenderObject.Glow
  - renderer.TextLine.Glow
  - renderer.PanelLayer.Glow
  - renderer.ShaderSet.UIResolveFrag
  - renderer.PassUILayer
  - renderer.PassUIGlow
  - shaders.UIResolveFragSpv
example: examples/13-ui
run: task hud
verified: 2026-09-19
---

# Where screen-space overlays are drawn

The engine has three overlay channels and they do not all go to the same place.

| Channel | Set by | Recorded in | Coordinates |
| --- | --- | --- | --- |
| UI panels | `SetUIOverlays` | tonemap pass, after the resolve | screen space |
| MSDF text | `SetMSDFOverlays`, `Debugf` | tonemap pass, after the resolve | screen space |
| Unlit overlays | `SetOverlays` | scene pass, with the geometry — or the water pass when the frame has water | world space |

Nothing about the API changes with the split. It matters when you are reasoning
about what a colour means, or about why something in the scene is or is not
affecting your HUD.

## What the two screen-space channels get

They are composited onto the swapchain image once the scene is finished and
resolved, which means:

- **Water cannot touch them.** `recordWaterPass` copies the HDR scene to refract
  through. The HUD is not in that image any more, so it cannot end up inside the
  refraction and cannot be drawn over by the surface.
- **Their colours are literal.** A UI colour is an sRGB value that reaches the
  display as written, and it does not move with `SetTonemap`'s exposure or
  curve — a white label is the same white at midday and at dusk. See
  [what a UI colour means](#what-a-ui-colour-means) for why the first half of
  that took a second change to become true.
- **They do not feed the scene's bloom.** Bright text does not reach the glare
  chain, so it does not glow into the scene behind it. It can have a glare chain
  of its OWN, over a layer of its own, without ever reaching the scene's — see
  [giving the UI its own HDR layer](#giving-the-ui-its-own-hdr-layer).
- **They are not multisampled.** See the cost below.

## What the world-space channel gets

`SetOverlays` takes `RenderObject`s with a caller-supplied MVP, and is for
things positioned in the world — health bars over a unit, a cooldown ring on the
ground. It stays in the scene render pass, so it keeps the scene's MSAA and sits
at the right point in the frame relative to the geometry around it. It is still
drawn with no depth test and no culling, so it is always on top of the geometry;
what it is *not* is screen space.

**In a frame that contains water it moves to the water pass instead**, after the
surface, and the pass it lands in is the only thing that changes. It had to: it
is recorded before the refraction copy otherwise, which put it *inside* the
refraction and then let the surface paint over it — the same shape of bug that
moved the two screen-space channels out, one pass later in the frame. "Always on
top" has to include on top of the lake. See
[`water.md`](water.md#blended-draws-split-on-the-surface).

If you are drawing a HUD, use the other two.

## The failure this prevents

Everything above used to be recorded in the scene pass together, which put the
HUD in the HDR target before three passes that are meant to operate on the scene
ran across it. Water was the one that showed, and it was not subtle:

```go
// examples/09-water/main.go, in Update
for i := 0; i < 26; i++ {
    e.Debugf("%s", hudLine)
}
```

```
go run ./09-water -frames 90 -hud 26 -screenshot out.png
```

Lines 0 to 14 came out crisp. Line 15 lost a slice of its ink, line 16 was cut
off mid-glyph exactly at the waterline, lines 17 to 21 were gone completely, and
lines 22 to 25 came back as faint ghosts warped along the wave pattern — the
refraction sampling a copy of the scene that had the text in it. The MSDF
pipeline writes no depth (`DepthTestEnable: false, DepthWriteEnable: false`), so
nothing stopped the water drawing over it.

Measured per line as recovered glyph coverage, against the same frame with the
HUD off:

| line | before | after |
| --- | --- | --- |
| 00–14 | 99.8–100.3% of median | 99.9–100.1% |
| 15 | 92.7% | 100.0% |
| 16 | 16.4% | 100.0% |
| 17–21 | 0.0–0.8% | 100.0% |
| 22–25 | 2.3–15.3% | 100.0% |

## The check

```
task hud
```

It renders `09-water` twice per configuration, with the HUD and without, at the
same fixed frame clock, and differences the two. That is the part that makes it
work: white text over bright sky and the same text over dark water produce
completely different pixels, so no single-frame threshold can measure both.
Differencing cancels the background out and lets the blend be inverted exactly
for coverage, which does not depend on what was behind it. `cmd/hudcheck` has
the arithmetic.

Two configurations run:

- the stock scene, where the text block crosses the waterline;
- pitched down at the water with bloom on at a threshold white text clears,
  which is what exercises the second half — before the move, this pair reported
  25 of 26 lines lost and 74268 scene pixels perturbed by the mere presence of
  the HUD.

Each configuration runs twice, once on each path the screen-space channels can
take: straight onto the swapchain, and through the UI glow layer. "The HUD
survives water, bloom and the tonemap" is a statement about a path, so a second
path is a second thing to say it about.

The gate is not vacuous and that was checked rather than assumed: `hudcheck`
refuses to pass when the median line carries no ink, because "every line is
equally absent" is otherwise a green result, and both configurations were run
against the pre-move build to watch them fail.

`task smoke` and `task validate` both stayed green through the original bug and
would stay green through its return. Neither looks at whether text is readable,
and the failure needs water and text in the same frame, which no example had.

## What a UI colour means

`[3]float32{0.05, 0.06, 0.08}` is sRGB. It reaches the display as
`{0.05, 0.06, 0.08}`, which is the near-black a HUD author picking those numbers
means.

That was not true when the channels first moved. The swapchain is
`B8G8R8A8_SRGB`, so the hardware applies the linear-to-sRGB encode on write, and
a shader passing a game's colour straight through was declaring it *linear*.
`0.05` chosen as "nearly black" arrived at `0.248` — a mid slate. To get `0.05`
on screen you had to write `0.0039`. The error was largest exactly in the dark
values a HUD is built from, and invisible until someone compared the colour they
picked against the pixel they got.

`ui.frag` and `msdf.frag` now decode with `srgbToLinear` (`shaders/srgb.inc`)
before writing, so what a game writes is what it sees.

**Texels do not go through it.** A colour texture is created as
`R8G8B8A8_SRGB` (see `textureOptions.srgb`), so the sampler has already decoded
it; decoding again would darken every texture the UI draws. Only the colour that
came from the game — vertex colour times tint — is converted.

**The world-space `overlays` channel is unchanged and still linear.** It renders
inside the scene render pass — or the water one, which shares its attachments —
and goes through the tonemap, so its colour is an HDR value like any other piece
of geometry, not a display value. The two channels mean
different things by a colour because they are composited at different points,
which is the same split this page is about.

What that change cost, captured either side of it back to back under a fixed
frame clock:

| example | pixels differing | max delta | where |
| --- | --- | --- | --- |
| `13-ui` | 46807 (5.1%) | 72/255 | the HUD panel and the caption, 135 rows |
| `17-input` | 164736 (17.9%) | 73/255 | the bottom readout bar |

The 3D scene does not move in either — every differing row is a row with UI in
it. `13-ui` was left alone: its constants were already the values someone would
pick meaning "nearly black" and "dark red", and it now draws them. `17-input`'s
greys were re-picked, because they had been chosen by eye against the lifted
output and landed too close together once they were taken literally.

## Edges are antialiased in the shader

The swapchain is single-sampled, so there is no MSAA coverage to smooth a panel
edge — and none to be had, because MSAA is resolved into the HDR target long
before this pass runs. Every engine that composites its UI after tone mapping is
in the same position, and they all compute coverage analytically instead. Text
already did: MSDF antialiases in the fragment shader and never depended on the
rasterizer.

`ui.frag` does the same for quads. `min(uv, 1 - uv)` is the distance to the
nearest *outer* edge, `fwidth` converts it to pixels, and the result multiplies
alpha.

Two details carry the whole thing:

- **The distance is in UV, not in the quad's own space.** That is what makes it
  correct for a nine-slice. The nine quads tile the atlas, so the panel's outer
  boundary is exactly where UV reaches 0 or 1, while the interior seams sit at
  `inset/texSize` and keep a positive distance. A formulation based on each
  quad's own extent would antialias the seams too and draw a visible line down
  the middle of every panel where two quads abut.
- **Quads are grown half a pixel outward,** with UVs extended to match so 0 and
  1 stay on the requested edge. The rasterizer only generates fragments whose
  centre is inside the geometry, so without that skirt the distance never goes
  negative and only the inner half of the ramp exists — a softer edge biased
  half a pixel inward rather than a hard step. Emitters that have not been
  updated still get that half, which is why this degrades gracefully.

A zero-width or zero-height quad is dropped rather than grown. An empty progress
bar asks for exactly that, and a skirt around nothing is a one-pixel sliver
where the bar is supposed to be empty.

### What it measures

`13-ui`'s health bar fills on a sine, so each frame puts its fill edge at a
different sub-pixel position. Collecting every luminance strictly between the
bar's background and its fill, across twelve captures under a fixed frame clock:

| | distinct intermediate levels |
| --- | --- |
| before | 2 |
| after | 10 |

The two before are the bar's own gradient, not coverage — the edge itself is a
hard step from 29 to 97 with nothing in between. Ten after is continuous
coverage, and it is worth noting 4x MSAA could produce at most three, since five
coverage levels leaves three strictly between the endpoints. **This is a better
edge than the one the overlay move cost, not a restoration of it.**

## Choosing a panel interior

Panel mode splits a nine-slice by the texture's alpha: opaque border,
transparent middle. The interior defaults to the border tint at `0.2` and `0.7`
of its opacity, which is one look and was the only one:

```go
ns := renderer.NewNineSlice(tex, 48, 16)
ns.Fill = &renderer.PanelFill{
    Color:   [3]float32{0.04, 0.05, 0.07}, // sRGB, like every UI colour
    Opacity: 1.0,
}
```

`Fill` is a pointer because both ends of the opacity range are meaningful: `0`
leaves just the frame, and `1` is what a modal dialog or a title card needs.
Neither can double as "unset", so nil is the signal and the shader branches on a
negative opacity. `packUIFill` is the one place that encodes it, and
`TestPanelFillUnsetSignalsDerive` fails if the sentinel goes missing — the
symptom otherwise is a panel whose background vanished, which points at the
wrong file.

## What it costs

MSAA is resolved into the HDR target long before the tonemap runs, so the
swapchain attachment is single-sampled and these pipelines are built at
`Samples1`. Measured by capturing each example either side of the move under a
fixed frame clock:

| example | pixels differing | above 1/255 | max delta |
| --- | --- | --- | --- |
| `10-text` | 2801 | 0 | 1/255 |
| `15-kitchen-sink` | 1250 | 0 | 1/255 |
| `13-ui` | 6031 | 30 | 47/255 |
| `17-input` | 20396 | 136 | 23/255 |

Text does not move: every difference in `10-text` is one 8-bit rounding step
from blending against an 8-bit destination instead of a half-float one. MSDF
antialiases in the fragment shader and never used the rasterizer's coverage.

**UI panel edges do move.** The 30 pixels in `13-ui` are two border columns of a
single panel; the 136 in `17-input` are the outlines of the small button rects.
A panel edge that lands between pixels used to be smoothed by MSAA coverage and
is now hard. Antialiasing a panel edge belongs in `ui.frag`, the way the glyph
edge already belongs to the distance field; until it is there, put panels on
integer pixel boundaries if the edge matters.

GPU cost is its own pass so the resolve's number stays meaningful. On
`15-kitchen-sink`, 240 frames:

```
gpu composite   0.0020 ms
gpu tonemap     0.0120 ms
```

Nesting the two would have been the easy mistake — passes that contain each
other sum to more than the frame they sit in, which is the exact mismatch that
caught `gputimer.go`'s first version. `recordTonemap` writes both intervals
itself so they stay adjacent.

## Giving the UI its own HDR layer

Everything above says a UI colour is a literal sRGB value, and it stays true.
What it also means is that a UI colour cannot exceed 1 and nothing in a HUD can
bloom, because the swapchain is `B8G8R8A8_SRGB` and 8 bits per channel cannot
represent a highlight. That closes off a look: a glowing button, a warning that
pulses, a sci-fi HUD that bleeds light onto the panel behind it.

**Tonemapping the UI with the scene is not the answer.** That is the bug the
move fixed, in a nicer suit: with any exposure that tracks the time of day a
white label is a different white at midday than at dusk, which is not what a UI
colour means, and on HDR output the HUD brightness wanders while the guidance is
to pin it to a stable paper white.

So the UI can have an HDR target of its own, with a bloom chain over that target
alone and a resolve with a **fixed** exposure that composites it onto the
swapchain. Off by default.

```go
e, err := glyph.New(&game{}, glyph.WithUIGlow())   // allocates the layer
...
r := e.Renderer()
r.SetUIGlow(0.7, 1.2, 0.2, 1.0)  // strength, threshold, knee, radius
r.SetUIExposure(1.0)             // fixed, and not SetTonemap's
```

An element asks to emit through one field on whichever channel it belongs to:

```go
renderer.UIRenderObject{..., Glow: 2.0}                  // panels and textures
renderer.TextLine{Text: "REACTOR CRITICAL", Glow: 2.2}   // MSDF text
renderer.PanelLayer{..., Glow: 1.0}                      // a Panel's layer
```

`Glow` is a **linear** multiple of the element's own colour: 0 is none, 1
doubles it, 3 quadruples it. It is deliberately not "write 1.4 into `Color`" —
`Color` is an sRGB display value and sRGB has no meaning above 1, so
`srgbToLinear` would be evaluating its curve outside the domain it is defined
on. A multiple of the element's own colour also keeps its hue, which is what a
game means by "make the warning glow": a brighter red, not a red with white
added.

`Glow` does nothing without the layer, and that is enforced rather than merely
likely — the recorder does not push a panel's emission when there is no layer,
and `msdf.frag` gates the per-vertex one on the same flag. Honouring it against
an 8-bit target would clamp, turning a mid-grey panel that asked for glow into a
white one, which is a different colour rather than no glow. Checked: `13-ui` in
its layer-less `direct` mode with every demo element asking for the emission it
asks for in `on` renders a byte-identical frame to the same mode with none of
them asking.

**An element that emits hard loses its hue in the middle**, and that is the
display rather than a bug. `13-ui`'s button is teal at `Glow: 2.0`, so its green
channel reaches 2.06 in linear and its red 0.16; the composite clamps both and
the core comes out near white with a cyan halo around it, exactly the way the
sun disc clips in the scene. Whether that reads as "lit" or as "blown out" is a
look, and it is the game's to tune: a lower `Glow`, a less saturated colour, or
a lower `SetUIGlow` threshold so a gentler emission still reaches the chain.

`Renderer.UIGlowLayer` reports whether the layer exists, because every other
knob is silent without it. `Renderer.UIGlow` and `Renderer.UIExposure` read the
settings back, for a harness that wants to switch the look off and put the same
numbers back rather than guessing them.

### What is in the layer, and what is not

| | scene | UI glow layer |
| --- | --- | --- |
| target | `R16G16B16A16_SFLOAT`, one per swapchain image | the same |
| bloom | `SetBloom` | `SetUIGlow` — a second instance of the same chain |
| exposure and curve | `SetTonemap`, and the day/night cycle moves it | `SetUIExposure`, fixed, default 1 |
| what feeds it | geometry | `SetUIOverlays` and `SetMSDFOverlays` only |
| resolve | `tonemap.frag`, writes every pixel | `uiresolve.frag`, blends over what that wrote |

The two never meet. The scene's bloom chain reads the scene target, which has
never contained UI on either path; the UI's chain reads the UI layer, which
contains nothing else. Measured, on `13-ui` at 1280x720 under a fixed clock:

- with the scene's bloom turned up until it moves 69 % of the frame
  (`-bloom 0.7 -bloomthreshold 0.5`), the UI's own contribution — the glowing
  frame minus the non-glowing one, in linear light — changes by at most 0.0097
  against a peak contribution of 0.87, and that residue is 8-bit quantisation:
  the contribution is recovered by subtracting two 8-bit images, and two
  quantisation steps at the sRGB values involved is 0.007 to 0.014;
- between midnight and noon (`-time 0.0` against `-time 0.5`) it changes by at
  most 0.0095 against a peak of 0.91;
- between an identity tonemap and ACES at exposure 0.45, at most 0.0092;
- with the scene's bloom on, the glow perturbs exactly the same 67577 pixels at
  exactly the same maximum of 191 as with it off — byte for byte the same
  difference, so the UI is not feeding the scene's chain.

### The composite replaces the direct draw

With the layer on, the tonemap pass's composite step draws **one fullscreen
triangle of the finished layer** instead of N UI quads. It does not stack on
them: drawing both would put the UI on screen twice, once blended into a float
layer and once straight onto the swapchain, which reads as the HUD having gained
contrast rather than as a double draw.

A frame with no UI at all skips the layer pass, its chain, and the composite, so
it presents exactly what a layer-off frame presents. A layer that was never
written must not be composited over the scene.

### Premultiplied alpha, or every antialiased edge gets a dark halo

This is the part that bites, and the symptom does not point at it.

The UI blends `SrcAlpha / OneMinusSrcAlpha` onto an opaque tonemapped scene,
which is correct and needs no thought. Render into a transparent layer and that
stops working: overlapping panels double-count alpha, and edges fringe where
they blended against a cleared-to-zero target.

So the layer clears to `(0,0,0,0)`, `ui.frag` and `msdf.frag` scale by their own
coverage before writing, the blend inside the layer is `One / OneMinusSrcAlpha`,
and so is the composite onto the swapchain. `overlayBlend` is the one place the
pair is written down, in the two forms the two destinations need. Note that the
**alpha** factors are `One / OneMinusSrcAlpha` in both and always were — that
half of premultiplied alpha was already right here before there was a layer to
need it.

Getting it wrong is one line. Measured by doing it, with the shaders
premultiplying and the layer's blend left at `SrcAlpha`: `13-ui` through the
layer differed from the direct path on 33745 pixels at a maximum of 55/255,
every one of them a glyph edge or a panel border. At 8x magnification the
glyph stems read thinner and the amber-to-slate ramp turns muddy — it looks like
the antialiasing broke, not like a blend mode did.

### Where the emission rides

The 256-byte push block is full for the lit pipelines and nowhere near full for
these two. `ui.vert` declares `mvp`, `model` and `tint`; `ui.frag` adds `params`
(only `.x`, the texture-mode flag) and `fill`; `msdf.frag` declared no push
block at all. So everything from offset 176 up was free, and `vec4 glow` sits
there: `.x` is the emission multiplier and `.y` is 1 when the draws are going
into the layer. `msdf.frag` declares the four members before it purely to land
on that offset, the way `sky.frag` declares the members it ignores.

**Text cannot use it.** `MSDFText` builds one mesh and one draw from every line
it is given, so a per-draw value could not light one line and leave the next
alone. Its emission rides in the vertex position's **Z**, which was always zero
on a screen-space ortho pipeline that neither tests nor writes depth, and which
is the only per-vertex float not already taken — `inNormal` is alpha,
screenPxRange and the bold bias, and `inColor` and `inUV` are the colour and the
atlas lookup.

`msdf.vert` therefore drops Z from `gl_Position`, and it has to: the overlay
projection is `Ortho(-1, 1)`, so a glow of 2 would put the glyph at clip z = -2
and Vulkan would clip the quad away. Glyphs would vanish exactly when a game
asked them to glow, which reads as a text bug rather than a clip-space one. With
Z zero — every glyph built before this existed — the third column contributed
exactly the zero vector, so dropping it changes no existing pixel.

### What it costs

Memory, at 1280x720 with three swapchain images:

| | per image | total |
| --- | --- | --- |
| the layer, `R16G16B16A16_SFLOAT` at full resolution | 7.03 MiB | 21.09 MiB |
| its five-level bloom chain (640x360 down to 40x22) | 2.34 MiB | 7.02 MiB |
| | | **28.12 MiB** |

GPU time, on a Radeon RX 7900 XTX at 1280x720, as the mean and range of five
interleaved 200-frame runs of `13-ui` (`task bench -scene ui`, `ui-layer`,
`ui-glow`):

| | `uilayer` | `uiglow` | `composite` | `tonemap` | frame total |
| --- | --- | --- | --- | --- | --- |
| layer off | — | — | 0.002 | 0.011 (0.011–0.012) | 0.966 (0.946–0.987) |
| layer on, `SetUIGlow(0, …)` | 0.017 | — | 0.007 | 0.010 | 0.995 (0.993–0.998) |
| layer on, nothing emitting | 0.018 (0.017–0.018) | 0.063 (0.062–0.065) | 0.007 | 0.009 | 1.060 (1.053–1.066) |
| layer on, three elements emitting | 0.018 (0.017–0.019) | 0.064 (0.062–0.065) | 0.007 | 0.009 | 1.059 (1.044–1.071) |

Three things worth reading off that table:

- **The glow itself is free; the chain is not.** Whether anything actually
  clears the threshold changes nothing — the last two rows are the same within
  noise. `recordBloom` runs whenever the strength is positive, because the
  recorder cannot tell whether anything will clear it: text carries its emission
  per vertex, where the recorder cannot see it. A game that wants the layer
  without the glow should set the strength to 0, which skips recording the chain
  entirely and costs 0.029 ms rather than 0.094.
- **`composite` goes UP, from 0.002 to 0.007 ms**, even though it draws one
  triangle instead of nine quads. A fullscreen blended triangle touches 921600
  pixels; nine small quads do not.
- **`tonemap` goes DOWN, from 0.011 to 0.009 ms**, which nothing in the change
  explains — the pass is identical. It is most likely where the GPU happens to
  place the bubble between two adjacent brackets. Recorded rather than explained
  away.

The three passes do not sum to the frame delta (0.017 + 0.063 + 0.005 = 0.085
against a measured 0.094), and that gap is the usual one: the GPU overlaps work
across pass boundaries and a bubble between two passes belongs to neither. See
`gputimer.go`.

**Off is free**, and that is checked rather than intended.
`TestRecordCommandBufferStreamIsUnchanged` pins the recorder's driver-call hash
and passes UNMODIFIED with the layer off; a second pinned hash covers the
layer-on stream, and a third test compares the call counts — 3299 with the layer
off against 3379 with it on, so a layer that was never entered would read the
same as one that was. On the GPU side, `13-ui`, `17-input`, `10-text`,
`09-water -hud 26`, `15-kitchen-sink -demo` and `20-screens` are all byte
identical to the build before this landed, under `GLYPHENGINE_FIXED_FRAME_TIME`.

### The check

```
task uiglow
```

It renders `13-ui` three ways at the same fixed clock — the same geometry drawn
straight onto the swapchain (`-glow direct`), through the layer with nothing
emitting (`-glow layer`), and through the layer with three elements emitting
(`-glow on`) — and differences two pairs.

**`direct` against `layer` is the layer's neutrality.** Its resolve is identity
for everything at or below 1, so the only difference allowed is one 8-bit
rounding step from blending in a float layer and encoding once instead of
blending into an 8-bit sRGB target repeatedly. Measured: 5009 pixels of 921600
differ, **none of them by more than 1/255**, and every one is a blended UI pixel
— the HUD panel, the bars, the labels, the caption and the fps readout. The 3D
scene does not move at all. Cropped to 8x, panel edges, glyph edges and two
overlapping translucent panels are indistinguishable between the two.

**`layer` against `on` is the glow**, over four regions, because no one of them
carries the statement:

| region | what it is | measured | limit |
| --- | --- | --- | --- |
| `lit` | an element that emits | +49.75 mean luma | floor 25 |
| `near` | a NON-emitting label 10 px away | +9.31 | floor 3 |
| `far` | the original HUD panel and bars | +0.00 | ceiling 0.5 |
| `scene` | sky and terrain, no UI over it | +0.00 | ceiling 0 |

`near` is the one that matters most: glow that crosses *between* elements is the
only part of this a game could not already reach from outside the engine.
World-space UI already blooms (`Emissive` plus `Translucent` above the
threshold), and `WithShaders` can replace `UIFrag` outright for a halo on one
element. Neither can make a button light the panel next to it.

`lit` also carries a floor on its own background brightness, the way `hudcheck`'s
median ink does: a capture where the element was never drawn would otherwise
report four zeroes and fail for a reason pointing at the wrong file.

The gate was proved by breaking the real code three ways and watching it fail;
the Taskfile comment records what each one printed. The one worth knowing is the
third — the glow applied per pass rather than per object leaves the neutrality
arm green and *raises* both `lit` and `near`, so those two alone would have
shipped it. `far` is the only arm that fires, at +43.68 against a ceiling of
0.50.

`task hud` runs both paths too, once each. Nothing in `09-water` asks to emit, so
the layer is supposed to change no colour there at all, and it does not: median
recovered ink 2392.6 against 2392.6 on the stock scene and 2392.5 against 2392.5
with the camera pitched at the water and the scene's bloom low enough to catch
white text, with zero bleed outside the HUD's rows in every arm. No threshold was
loosened to get there.

## Failure mode: a pipeline against the wrong render pass

A pipeline is tied to the render pass it was created against, and its sample
count has to match that pass's attachments. The overlay pipelines are therefore
built after `tonemapRenderPass` exists, not with the other pipelines in
`renderer.New`. Building them against `r.renderPass` at `r.msaaSamples` and
then binding them inside the tonemap pass is a validation error at *draw* time
rather than at creation, which was checked by doing it:

```
VUID-vkCmdDrawIndexed-renderPass-02684: RenderPasses incompatible between
active render pass and pipeline state object. Attachment 0 is not compatible
with 0: They have different formats.
```

Three more follow it, for samples, for the depth attachment the tonemap pass
does not have, and for the subpass dependency. Nothing else notices — the frame
still presents and the example still exits zero. `task validate` is the only
cheap signal.

The UI glow layer doubles the number of ways to get this wrong, which is why it
builds its own copies of both overlay pipelines rather than reusing the tonemap
pass's: the layer is `R16G16B16A16_SFLOAT` and the swapchain is
`B8G8R8A8_SRGB`, so a pipeline built for one and bound inside the other is
exactly the incompatibility above. `task validate` runs `13-ui -glow layer`,
`13-ui -glow on`, `09-water -hud 26 -uiglow` and a swapchain rebuild mid-flight
with the layer on, which is also where a resolve descriptor set still naming a
freed image view after a resize would surface and nowhere else.
