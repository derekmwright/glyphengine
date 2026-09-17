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
example: examples/13-ui
run: task hud
verified: 2026-09-17
---

# Where screen-space overlays are drawn

The engine has three overlay channels and they do not all go to the same place.

| Channel | Set by | Recorded in | Coordinates |
| --- | --- | --- | --- |
| UI panels | `SetUIOverlays` | tonemap pass, after the resolve | screen space |
| MSDF text | `SetMSDFOverlays`, `Debugf` | tonemap pass, after the resolve | screen space |
| Unlit overlays | `SetOverlays` | scene pass, with the geometry | world space |

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
- **They do not bloom.** Bright text does not feed the glare chain, so it does
  not glow into the scene behind it.
- **They are not multisampled.** See the cost below.

## What the world-space channel gets

`SetOverlays` takes `RenderObject`s with a caller-supplied MVP, and is for
things positioned in the world — health bars over a unit, a cooldown ring on the
ground. It stays in the scene pass, so it keeps the scene's MSAA and sits at the
right point in the frame relative to the geometry around it. It is still drawn
with no depth test and no culling, so it is always on top of the geometry; what
it is *not* is screen space.

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
in the scene pass and goes through the tonemap, so its colour is an HDR value
like any other piece of geometry, not a display value. The two channels mean
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
