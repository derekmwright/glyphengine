# YamlUI Specification

YamlUI is a declarative layout system for game UI. Panels are defined as YAML files, parsed into widget trees, and rendered each frame with live data bindings.

## File Structure

Each `.yaml` file defines a single root widget. The root must be a `panel` with explicit `width` and `height` (reference pixels, scaled at runtime).

```yaml
widget: panel
id: my_window
width: 620
height: 520
nine_slice: panel_dark
color: [1, 1, 1]
layout: vertical
gap: 0
padding: 0
children:
  - widget: label
    text: "Hello"
    font_size: 16
    color: [0.8, 0.8, 0.8]
```

## Coordinate System

All sizes (`width`, `height`, `padding`, `gap`, `font_size`) are in **reference pixels**. At runtime a uniform `scale` factor is applied. Coordinates are screen-space, Y-down.

## Widget Types

### panel

Container widget. Arranges children in a layout. Optionally renders a nine-slice background or flat color.

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `id` | string | — | Unique identifier for bindings and lookup |
| `width` | float | — | Fixed width in ref px. In horizontal layout, sized by this or `flex` |
| `height` | float | — | Fixed height in ref px. In vertical layout, sized by this or `flex` |
| `flex` | float | — | Proportional share of remaining space after fixed siblings |
| `padding` | float | 0 | Uniform inset on all 4 sides |
| `layout` | string | `"vertical"` | `"vertical"` or `"horizontal"` |
| `gap` | float | 0 | Spacing between children |
| `nine_slice` | string | — | Nine-slice texture name (from AssetProvider). Supports `{binding}` templates |
| `color` | [3]float | [0,0,0] | RGB tint applied to nine-slice (sRGB, 0..1) |
| `color_key` | string | — | Dynamic color binding key (overrides `color` if bound) |
| `opacity` | float | 1.0 | Alpha for nine-slice rendering (0.0 = fully transparent) |
| `bg_color` | [3]float | — | Flat solid-color quad fallback when no `nine_slice` is set |
| `visible` | string | — | Template. `"false"` or `"0"` hides widget and all children |
| `overlay` | string | — | ID of sibling — positions this widget at the same Y as that sibling (vertical layout only) |
| `indicator` | block | — | Driven overlay effect. See [Indicator](#indicator) |
| `transition` | block | — | Fade, scale and slide as `visible` flips. Allowed on every widget type. See [Transition](#transition) |
| `children` | list | — | Child widget definitions |

**Rendering priority:** If `nine_slice` is set, renders a nine-slice panel. If only `bg_color` is set, renders a flat colored rectangle. If neither, the panel is invisible (layout-only container).

### label

Text display widget. Vertically centered within its rect.

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `id` | string | — | Unique identifier |
| `text` | string | — | Display text. Supports `{binding}` templates |
| `font` | string | — | Font family hint (`"display"` or `"body"`). Parsed but currently unused — single MSDF atlas |
| `font_size` | float | — | Text size in ref px. **Required** for the label to render |
| `color` | [3]float | [0,0,0] | Text color (sRGB, 0..1) |
| `color_key` | string | — | Dynamic color binding key (overrides `color`) |
| `align` | string | `"left"` | `"left"`, `"center"`, or `"right"` |
| `nine_slice` | string | — | Optional background nine-slice behind the text |
| `width` | float | — | Fixed width (for horizontal layout sizing) |
| `height` | float | — | Fixed height |
| `flex` | float | — | Proportional sizing |
| `visible` | string | — | Visibility template |

### progress_bar

Horizontal fill bar with background and foreground layers.

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `id` | string | — | Unique identifier |
| `height` | float | — | Bar height in ref px |
| `value` | string | — | Current value. Template: `"{hp}"` |
| `max` | string | — | Maximum value. Template: `"{max_hp}"` |
| `bg_color` | [3]float | — | Background color (flat quad). Used when no `nine_slice` |
| `fg_color` | [3]float | — | Foreground fill color (flat quad). Used when no `fg_nine_slice` |
| `fg_color_key` | string | — | Dynamic foreground color binding (overrides `fg_color`) |
| `nine_slice` | string | — | Background nine-slice texture name |
| `fg_nine_slice` | string | — | Foreground fill nine-slice texture name |
| `visible` | string | — | Visibility template |

Fill width = `(value / max) * bar_width`, clamped to [0, 1].

### Flat quads reach the colour you asked for

`bg_color` on a `panel`, and `bg_color`/`fg_color` on a `progress_bar` with no
nine-slice, do not go through `PanelFn`. They are appended to the vertex stream
`BuildAt` returns, which the game uploads as one mesh — the fourth and fifth
return values, not the first. The `scroll_view` thumb is on the same path.

Because that stream is one mesh and one draw, a quad inside a `scroll_view` is
**trimmed to the clip as it is emitted** rather than scissored: the corners move
and the UVs move with them, so the coverage ramp still lands on the edges that
survived and the cut edge is hard. A quad outside every `scroll_view` is emitted
exactly as it always was.

Those colours are **sRGB**, like every other UI colour in the engine: what you
write is what reaches the display. `bg_color: [0.05, 0.06, 0.08]` renders as
(13, 15, 20) of 255, which is `0.05 × 255` and so on, within rounding.
`shaders/ui.frag` decodes them with `srgbToLinear` and the swapchain re-encodes
on the way out, so the round trip is the identity. This page used to
call them linear in four places; measured, they are not, and the palette table
below has always given each float next to the sRGB hex it equals.

Each quad is emitted **half a pixel larger than its rect on every side**, with
UVs running from just below 0 to just above 1 so that UV 0 and 1 still land on
the rect that was asked for. `ui.frag` turns the distance to UV 0 or 1 into
coverage; that is where a flat panel's antialiased edge comes from, since the
swapchain is single-sampled and the UI is composited after the tonemap. A
zero-width or zero-height quad is dropped rather than grown, so an empty
progress bar is empty rather than a one-pixel sliver.

The emitter had none of that until issue #144. It wrote no UV at all, so all
four vertices carried (0, 0), `edgeCoverage` saw a distance of 0 with `fwidth`
clamped to `1e-8`, and coverage came out at its `+ 0.5` clamp for every
fragment: **every flat quad composited at half alpha, edge to edge**, whatever
`opacity` asked for. A nearly black panel on a nearly white one measured
(147, 158, 172) where the YAML asked for (13, 15, 20). It is (13, 15, 20) now.
Nine-slice panels and text were never affected — their UVs already spanned
0..1 — which is why the only symptom was a flat panel that looked washed out,
and why nothing in `task smoke`, `task validate`, `task determinism` or
`task indicator` could see it. `task flatquad` is the gate that now does.

### button

Interactive widget with nine-slice background and optional centered label. Emits click events.

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `id` | string | **required** | Used for hit testing and event identification |
| `width` | float | — | Fixed width |
| `height` | float | — | Fixed height |
| `nine_slice` | string | — | Background nine-slice. Supports `{binding}` templates |
| `color` | [3]float | [0,0,0] | Nine-slice tint color |
| `color_key` | string | — | Dynamic color binding |
| `opacity` | float | 1.0 | Nine-slice alpha |
| `text` | string | — | Centered label text. Supports templates |
| `font_size` | float | 14 | Label font size |
| `fg_color` | [3]float | — | Label text color |
| `on_click` | string | — | Event value emitted on click |
| `disabled` | string | — | Template. `"true"` or `"1"` disables interaction |
| `state` | string | — | Template bool. Anything but `"true"`/`"1"` multiplies the tint by 0.45 |
| `indicator` | block | — | Driven overlay effect. See [Indicator](#indicator) |
| `children` | list | — | Child widgets (e.g., nested `icon`) |
| `flex` | float | — | Proportional sizing |
| `visible` | string | — | Visibility template |

**Button states:** Normal renders as-is. Hover multiplies color × 1.3. Pressed × 0.6. Disabled × 0.4.

### icon

Renders a sprite texture via the IconBuilder callback.

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `sprite` | string | — | Sprite name. Supports `{binding}` templates |
| `width` | float | — | Icon width |
| `height` | float | — | Icon height |
| `color` | [3]float | [0,0,0] | Tint color |
| `color_key` | string | — | Dynamic color binding |
| `opacity` | float | 1.0 | Alpha |
| `state` | string | — | Template bool. Anything but `"true"`/`"1"` multiplies the sprite tint by 0.45 |
| `indicator` | block | — | Driven overlay effect. See [Indicator](#indicator) |
| `visible` | string | — | Visibility template |

### scroll_view

A container that clips what it holds to its own rect and lets the player move
the content inside it with the wheel, a drag, or the thumb.

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `id` | string | **required** | Used for scroll state tracking |
| `flex` | float | — | Proportional sizing (typically `flex: 1` to fill remaining space) |
| `nine_slice` | string | — | Optional background |
| `color` | [3]float | [0,0,0] | Background tint |
| `opacity` | float | 1.0 | Background alpha |
| `padding` | float | 0 | Inner padding; the view rect is the widget's rect less this |
| `layout` | string | `"vertical"` | How the children are laid out, as on any container |
| `gap` | float | 0 | Spacing between children |
| `scroll_direction` | string | `"vertical"` | `"vertical"`, `"horizontal"` or `"both"` |
| `scrollbar` | string | `"auto"` | `"auto"`, `"always"` or `"never"` |
| `drag` | bool | `true` | Whether the content follows the pointer |
| `scroll_ease` | string | `"out_cubic"` | Curve focus arrives on, or `"none"` to snap |
| `children` | list | — | Scrollable child widgets |
| `visible` | string | — | Visibility template |

Any value these fields cannot resolve is a **load error** rather than a silent
default: `scroll_direction: horizonal` would otherwise be a list that refuses to
move sideways, which looks exactly like a layout mistake. The four scroll fields
are also rejected on a widget that is not a `scroll_view`.

`scroll_direction: horizontal` almost always wants `layout: horizontal` with it.
The two are separate because they answer different questions — one is how the
children are placed, the other is which way the player may move them.

#### Content size comes from the laid-out rects

How far the content reaches is measured from the children's **resolved rects
after layout**, not from their declared heights. A child sized by `flex:` has no
declared height at all, and a child with its own vertical layout has one that
says nothing about what is inside it; summing declared heights got both wrong,
so a list stopped short of its last row or scrolled past its end into blank
space.

The measurement descends into containers and **stops at a nested
`scroll_view`**: that one's overflow is its own business, and counting it would
make the outer list scroll by the inner list's length.

#### Clipping

Every descendant is clipped to the view rect — panels, nine-slices, icons,
indicator fans, transition shapes, flat `bg_color` quads and text lines alike. A
row crossing an edge stops at the edge; before this it drew in full, over
whatever the container was sitting on.

Nested views **intersect**: the inner clip is the overlap of the two rects, not
the inner rect. `task scroll` reads pixels out of exactly that case.

Text is clipped by trimming the glyph quads and carrying their atlas UVs with
them, which is why a row sliding under the top edge loses the tops of its
letters rather than the whole line at once. That is not a style choice — a text
overlay is one mesh and one draw covering every line it was given, so a scissor
there could only clip all of them or none. `docs/agents/overlay-composite.md`
has the mechanism and its measured cost.

A widget outside every `scroll_view` carries no clip at all and draws exactly
the way it always did.

#### Input

- **Wheel** while the pointer is over the widget. It drives the vertical axis,
  except on a view that only scrolls sideways, where it drives that one.
- **Content drag** with the left button, when `drag` is true. The content
  follows the pointer: dragging down reveals what is above, the way every touch
  surface works.
- **Thumb drag**, when `scrollbar` is `auto` or `always` and there is somewhere
  to scroll. One pixel of thumb is `maxScroll / (track - thumb)` pixels of
  content, so the thumb stays under the finger.

A drag only becomes a drag once the pointer has moved **4 reference pixels**
from where the button went down. Under that, the press is still a click: a list
of rows that are buttons has to be both clickable and draggable with the same
button, and a hand on a mouse moves a pixel or two during any click. Once a drag
crosses that dead zone it **cancels every press in flight**, so the row you
started on does not fire when you let go.

A view inside a widget playing its out transition takes no input at all, the
same rule a button follows.

#### Scroll into view

`SetFocus(id)` on a widget inside a `scroll_view` scrolls the **minimum
distance** that brings it into sight, and nothing at all if it is already there.
Nested views are handled innermost first, so an outer view aims at where the
inner one leaves the row rather than at where it used to be.

The move is eased with `out_cubic` over 0.15 s on the tree's unscaled clock —
the one `SetTime` is fed — so a list on a pause menu still scrolls at
`SetTimeScale(0)`. `scroll_ease: none` snaps instead, and a tree that was never
given a clock snaps too, for the same reason a transition without one cuts: an
eased scroll driven by a delta that is always zero would stop at its start, and
the row focus just moved to would never appear.

It runs only on the frame focus **changes**. A view that re-aimed every build
could never be scrolled away from with the wheel.

`examples/20-screens -scroll` is the whole keyboard path: the arrow keys pick a
row and the game says nothing but `SetFocus("row7")`.

#### Scroll state

Kept on the tree, keyed by widget id, so it survives `SetChildren` — a filtered
or refilled list stays where the player left it. It is **clamped on every
build** against the content that is actually there, so a list that got shorter
comes back rather than staying scrolled past its own end.

`SetScroll(id, x, y)` and `ScrollOffset(id)` save and restore a position across
a screen change. `SetContentClipping(false)` turns clipping off; it is a
diagnostic and the control arm of `task scroll`, not something a game wants on.

Two `scroll_view`s with no `id` share one offset, which is why the id is
required.

### text_input

Single-line text entry with placeholder, focus, and keyboard handling.

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `id` | string | **required** | Used for focus tracking and event identification |
| `font_size` | float | 14 | Input text size |
| `color` | [3]float | [0,0,0] | Input text color (also used for nine-slice tint) |
| `fg_color` | [3]float | — | Text color when typing |
| `placeholder` | string | — | Placeholder text when empty and unfocused. Supports templates |
| `placeholder_color` | [3]float | — | Explicit placeholder color (default: fg_color × 0.5) |
| `nine_slice` | string | — | Background nine-slice |
| `opacity` | float | 1.0 | Background alpha |
| `padding` | float | 0 | Left padding for text |
| `flex` | float | — | Proportional sizing |
| `visible` | string | — | Visibility template |

Click focuses the input. Enter emits a `"submit"` event with the typed text. Escape clears and defocuses.

## Indicator

An optional block on `panel`, `button` and `icon` that draws a **driven overlay
effect** over the widget: a cooldown sweep, a wipe, or a tint, bound to a value
the game updates every frame.

```yaml
- widget: icon
  id: slot_1
  sprite: "{ability_1_icon}"
  width: 64
  height: 64
  state: "{ability_1_active}"      # template bool; false dims the sprite
  indicator:
    type: sweep                    # roll | sweep | tint
    direction: clockwise           # sweep: clockwise | counterclockwise
    start: -90                     # sweep: degrees; -90 is twelve o'clock
    shape: square                  # sweep: square (default) | circle
    fill: remaining                # remaining (default) | elapsed
    value: "{ability_1_cooldown}"  # template number
    max: "{ability_1_cooldown_max}"
    color: "#000000"
    opacity: { start: 0.6, end: 0.0 }
```

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `type` | string | **required** | `roll`, `sweep` or `tint` |
| `direction` | string | — | `roll`: `up`, `down`, `left`, `right`, or a number of degrees. **Required.** `sweep`: `clockwise` (default) or `counterclockwise`. Not used by `tint` |
| `start` | float | -90 | `sweep` only. Where the fan starts, in degrees. -90 is twelve o'clock |
| `shape` | string | `square` | `sweep` only. `square` clips the fan to the widget's own edge, `circle` to the circle inscribed in it |
| `fill` | string | `remaining` | `remaining` covers `frac` (a cooldown that unwinds), `elapsed` covers `1 - frac` (a bar that fills in). Not used by `tint` |
| `value` | string | — | Current value. Template: `"{cooldown}"` |
| `max` | string | — | Maximum value. Template: `"{cooldown_max}"` |
| `color` | string | `#000000` | Overlay colour, `#rrggbb`. The same space a `color:` triple is in — each channel is the hex digit pair over 255 |
| `opacity` | block | opaque | `{ start, end, ease }`. `start` is the alpha at `frac` 1, `end` the alpha at `frac` 0, and `ease` shapes the ramp between them by [`ui/ease`](../ease) name |

### Semantics

`frac = clamp(value / max, 0, 1)`.

- **`frac` 0 draws nothing at all.** A finished cooldown leaves the widget
  exactly as a YAML with no `indicator:` block would — byte for byte on the
  frame, which `task indicator` checks.
- **`max <= 0` draws nothing either**, rather than dividing. That is the state a
  game is in on its first frame, before it has bound anything.
- **`roll`** covers `frac` of the widget rect as a wipe starting at the edge the
  direction points *from*: `down` at 0.5 covers the **top** half, and the
  covered band's lower boundary rises as the value falls. A number rotates the
  wipe axis — 0 is `right`, 90 is `down`, 180 is `left`, 270 is `up`, and 45 is
  a diagonal cut from the top-left corner. The boundary sits at `frac` of the
  rect's extent *projected onto the axis*, which is what makes a rotated axis a
  rotation of the same wipe rather than a different shape.
- **`sweep`** covers `frac` of a full turn as a fan from the widget's centre,
  starting at `start` and unwinding in `direction`. With the default
  `shape: square` the fan's rim is on the widget's own perimeter, so the corners
  darken too; `shape: circle` clips it to the inscribed circle instead, for a
  round icon or a ring. A quarter turn from -90 clockwise is exactly the
  top-right quadrant, corner included.
- **`tint`** multiplies the widget's colour by `color` at the interpolated
  alpha and adds **no geometry**: at alpha 0 the widget is untouched, at 1 it is
  fully multiplied, and in between it crossfades.
- **`opacity`** interpolates on `frac`, not on coverage, so a cooldown fades out
  as it expires. Omit the block for a fully opaque overlay. An `ease` shapes
  that interpolation — `ease: out_cubic` on a `{start: 0.6, end: 0}` ramp loses
  most of the alpha early and then lingers, which is how a sweep stops shouting
  at the player for the last half second of a long cooldown. The endpoints are
  untouched by any curve, so "frac 0 draws nothing" still holds. The same eight
  names the [`transition:`](#the-curves) block uses.

### Draw order

Within one widget: **widget, sprite, indicator, text**. Everything the indicator
draws goes into the same render-object stream as the widget's own nine-slice and
its sprite, immediately after them, so it covers the artwork; labels are in the
text stream, which the renderer composites after every UI panel, so they stay
readable over it.

### `state`

`state` is a separate statement from the indicator and from `disabled`:

- `disabled` means the widget cannot be used, and multiplies the tint by 0.4.
- `state` means a toggle is off, and multiplies it by 0.45. The widget is still
  clickable.
- The indicator draws regardless of either. An ability that is switched off
  still shows its cooldown.

### Errors

The indicator block is validated at **load**, and the errors name the widget —
by its `id` where it has one, by its type where it does not. This is the only
part of the package that rejects anything; everything else tolerates a missing
binding. It is strict because a typo here renders a plausible frame rather than
a failure:

```
yamlui: load hud.yaml: widget "slot_1": indicator: unknown key "colour"
yamlui: load hud.yaml: widget "slot_1": indicator: unknown type "spin" (want roll, sweep or tint)
yamlui: load hud.yaml: widget "slot_1": indicator: unknown direction "clockwise" for type roll (want up, down, left, right or a number of degrees)
yamlui: load hud.yaml: widget "slot_1": indicator: shape is only used by type sweep, not roll
yamlui: load hud.yaml: widget "slot_1": indicator: color "#12345" is not a #rrggbb colour
```

An `indicator:` on any widget other than `panel`, `button` or `icon`, and a
`state:` on anything but `button` or `icon`, are rejected the same way.

### What the host has to supply

An indicator is geometry with an alpha, and `renderer.Vertex` has no alpha
channel — the value can only ride on a render object's `Opacity`. So the widget
tree needs a third builder callback beside `PanelFn` and `IconFn`:

```go
tree.ShapeFn = func(verts []renderer.Vertex, idxs []uint16, opacity float32) []renderer.UIRenderObject {
    mesh := pool.take(verts, idxs)          // the host owns the mesh
    return []renderer.UIRenderObject{{
        RenderObject: renderer.RenderObject{Mesh: mesh, MVP: proj},
        Opacity:      opacity,
    }}
}
```

Without `ShapeFn` an `indicator:` draws nothing, exactly as a `sprite:` draws
nothing without `IconFn`. `type: tint` is the exception — it changes the
widget's own colour and needs no builder.

`examples/13-ui` has a working pair of callbacks and a mesh pool behind its
`-yamlui` flag; `go run ./13-ui -yamlui cooldown` runs the whole block against a
clock.

### Building one in Go

`SetChildren` takes `WidgetDef` structs rather than YAML, so it does not run the
unmarshaller and therefore applies none of the defaults above and performs none
of the validation. Two consequences for an action bar assembled in Go:

- Set `Start` explicitly for a sweep. The zero value is three o'clock, not
  twelve.
- An all-zero `Opacity` is read as fully opaque, because an indicator that is
  invisible at every value is never what anyone means.

Everything else — the direction words, the colour, the fill — is resolved from
the strings on every draw, so a block built in Go behaves exactly like the same
block written in YAML.

## Transition

An optional block on **any** widget that fades, scales and slides it as its
`visible` binding flips. There is no `play()`, no trigger and no timeline: a
game already has a bool for "is the dialog open", and the whole point is that it
does not need a second one.

```yaml
- widget: panel
  id: confirm_dialog
  visible: "{confirm_open}"
  transition:
    duration: 0.2       # seconds, on the UNSCALED clock. Required
    ease: out_cubic     # any curve below; default linear
    opacity: true       # fade from 0; default true
    scale: 0.9          # grow from this factor to 1 about the anchor; default 1
    offset_y: -12       # slide from this many pixels to 0; offset_x likewise
    out: in_quad        # a different curve on the way out; default the in curve played back
```

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `duration` | float | **required** | Seconds. Must be positive |
| `ease` | string | `linear` | The curve the in-transition plays. See the set below |
| `opacity` | bool | `true` | Fade from fully transparent. `false` for a slide or a scale with no fade |
| `scale` | float | `1` | The factor the widget grows from, about its anchor. Must be positive |
| `offset_x` | float | `0` | Reference pixels the widget slides from, multiplied by the tree's scale like every other length here |
| `offset_y` | float | `0` | |
| `out` | string | — | A separate curve for the out. Without it the out is the in curve read at a descending progress: the same shape, played back |

### The curves

`linear`, `in_quad`, `out_quad`, `in_out_quad`, `in_cubic`, `out_cubic`,
`in_out_cubic`, `out_back` — the standard Penner names, exported from
[`ui/ease`](../ease) so a HUD built in Go can use the same shapes.

| Curve | How it reads |
|---|---|
| `linear` | No easing. Mechanical on anything that moves, and exactly right for a crossfade, where a constant rate of change *is* the even blend |
| `in_quad` | Starts slow, accelerates. Something leaving under its own power |
| `out_quad` | Starts fast, decelerates into rest. The gentlest arrival here |
| `in_out_quad` | Symmetric. The safe default for something that both appears and disappears |
| `in_cubic` | `in_quad` sharper: slower at the start, faster at the end |
| `out_cubic` | Decelerates harder than `out_quad`. The usual choice for a panel arriving — quick without ever snapping to a stop |
| `in_out_cubic` | `in_out_quad` with more contrast between the middle and the ends. Deliberate rather than merely smooth |
| `out_back` | Overshoots about 10% past its target around t 0.6 and settles back. A dialog that pops rather than arrives |

`out_back` is the only one that leaves 0..1. The overshoot reaches the scale and
the offset, which is the point of it; the opacity is clamped, because there is
nothing above fully opaque.

### Semantics

- **Driven by `visible`.** True starts the in, false starts the out. The widget
  is skipped only once the out **finishes**, not when `visible` flips.
- **Flipping mid-transition reverses from the current progress**, not from the
  start. A dialog dismissed halfway through its arrival takes half as long to
  leave, and re-opening it from there picks up where it was.
- **A widget already visible at the first build starts at rest.** A HUD does not
  fade in on every level load.
- **Nothing here touches layout.** The resolved rect and the resolved opacity
  are transformed at draw time, after layout has run, so siblings do not reflow
  while a dialog slides in. The one exception is documented below.
- **Input is refused from the first frame `visible` is false**, not when the
  fade ends — so the click that dismissed a dialog cannot also hit what was
  underneath it. During the in the widget takes clicks normally, and hit testing
  uses the **transformed** rect: a button sliding in is clickable where it is
  drawn. The refusal covers the whole subtree, and the hover highlight with it.
- **`scale` is about the widget's anchor**, which is its rect centre. This
  schema has no anchor field, and a modal growing from its middle is what
  `scale` means everywhere it is offered. The whole subtree scales as one, so a
  dialog's buttons arrive with the dialog rather than sliding around inside it.
- **Transitions nest.** A child with its own block multiplies its opacity by its
  parent's and transforms inside the parent's transform.
- **Per-widget state lives on the node.** `Load` and `SetChildren` build new
  nodes, so a tree rebuilt from YAML starts at rest rather than resuming a fade
  that belonged to the list it replaced.

#### The one place a transition meets layout

A widget playing its **out** is invisible by binding but still on screen, so it
keeps its layout box until the out finishes — without one its rect is the zero
rect and it fades out at the top-left corner, a pixel wide. Siblings therefore
close up when the out ends rather than on the frame `visible` flipped. A widget
with no `transition:` block is unaffected: hiding it still reflows immediately.

### The clock

Transitions run on a clock the host feeds in, beside the input:

```go
tree.SetInput(state)
tree.SetTime(e.UnscaledElapsed())   // NOT e.Elapsed()
panels, verts, idxs, text := tree.BuildAt(r, assets, x, y, scale, sw, sh)
```

`Engine.UnscaledElapsed` and not `Engine.Elapsed`, and this is the part that is
easy to get wrong because it works in every test: `Elapsed` is scaled by
`SetTimeScale`, a game pauses by setting the scale to 0, and a modal is very
nearly always opened while the game is paused. A fade wired to `Elapsed` never
starts in the one situation it exists for. `task transition` is the gate for
exactly that, and it captures every frame at `SetTimeScale(0)`.

The value is a running total rather than a delta, so a host that skips a frame
loses no time and one that hands the same reading twice advances nothing. Under
`GLYPHENGINE_FIXED_FRAME_TIME` it advances by the fixed delta, which is what
makes a capture of a fade land on the same point every run.

**A tree that is never given a clock plays no transitions at all**: every widget
cuts to hidden or to rest the moment `visible` flips, exactly as if the block
were absent. That is deliberate. A delta that was always zero would leave a
widget that just became visible stuck at the start of its fade forever, and "my
dialog never opens" is a far harder failure to read than "my dialog does not
fade".

### What the host has to supply

A fading **flat `bg_color` panel** needs `ShapeFn`, for the same reason an
`indicator:` does: `renderer.Vertex` has no alpha channel, so a quad that is
half faded cannot ride the shared vertex stream. While it is fading it is handed
to `ShapeFn` as its own render object, and it goes back into the vertex stream
the moment the transition settles. Two consequences:

- Mid-transition the widget composites **over** its siblings' flat quads rather
  than under them, because it is in the panel stream. For a dialog arriving over
  a HUD that is where it belongs.
- Without `ShapeFn` it still scales and slides but does not fade. It falls back
  to the vertex stream rather than being dropped — unlike an `indicator:`, which
  draws nothing — because a dialog that is briefly opaque is a smaller surprise
  than one that is missing.

Nine-slice panels, sprites and labels need nothing extra: their opacity already
has somewhere to live, on `UIRenderObject.Opacity` and `TextLine.Alpha`.

### Past the end, nothing is different

Once the in has finished, a transitioning widget is on exactly the draw path a
widget with no block is on — the same render objects, the same vertex stream,
the same text, with `TextLine.Alpha` back to the 0 that means opaque. Adding a
`transition:` to a HUD does not change a screenshot of it at rest.
`task transition` checks that pixel for pixel against the same YAML with the
block deleted, and `TestFramePastTheEndMatchesNoTransition` checks it render
object for render object.

### Errors

Validated at **load**, naming the widget, the same way the indicator block is:

```
yamlui: load hud.yaml: widget "confirm_dialog": transition: unknown key "offset"
yamlui: load hud.yaml: widget "confirm_dialog": transition: duration is required and must be positive, got 0
yamlui: load hud.yaml: widget "confirm_dialog": transition: unknown ease "out_bounce" (want linear, in_quad, out_quad, in_out_quad, in_cubic, out_cubic, in_out_cubic, out_back)
yamlui: load hud.yaml: widget "confirm_dialog": transition: unknown out "elastic" (want linear, in_quad, ...)
yamlui: load hud.yaml: widget "confirm_dialog": transition: scale must be positive, got 0
```

### Building one in Go

`SetChildren` takes `WidgetDef` structs rather than YAML, so it does not run the
unmarshaller and therefore applies none of the defaults above and performs none
of the validation:

- Set `Opacity: true` explicitly. The zero value is `false`, which is a
  transition that moves without fading.
- An all-zero `Scale` is read as 1 (no scale), because a widget scaled from
  nothing and held there is never what anyone means. Written in YAML, `scale: 0`
  is rejected at load instead, so the two paths cannot disagree about a number
  someone actually typed.

`examples/13-ui` drives the whole block behind `-yamlui dialog`:
`go run ./13-ui -yamlui dialog` pauses the scene and opens a confirm dialog on
the space bar, with an `out_back` pop on the bar inside it. A still frame cannot
tell an overshoot from a slow arrival, so that one exists to be looked at.

## Layout Algorithm

### Vertical (default)

Children are stacked top-to-bottom within the parent's padded area.

1. **Fixed children**: `height > 0` — uses that height.
2. **Flex children**: `flex > 0` — shares remaining space proportionally after fixed children and gaps.
3. **Natural height**: No `height` or `flex` — computed recursively from children's heights + padding + gaps. A leaf widget (no children) uses its `font_size` as natural height.

### Horizontal

Children are placed left-to-right within the parent's padded area.

1. **Fixed children**: `width > 0` — uses that width.
2. **Flex children**: `flex > 0` — shares remaining space proportionally.
3. **Neither**: gets 0 width.

In horizontal layout, child height defaults to the parent's inner height unless the child has an explicit `height`.

### Overlay

A child with `overlay: "sibling_id"` is positioned at the same Y as the referenced sibling (vertical layout only). It does **not** consume space in the layout flow and does not affect gap calculations.

## Template Bindings

Text and some properties support `{key}` placeholder syntax. At render time, all `{key}` occurrences are replaced with bound string values.

`{{key}}` resolves too, and means the same thing. It is accepted because the
doubled form is common in other UI dialects and because getting it wrong is
silent: replacing the single-brace form inside a doubled one leaves `{5}`
behind, which is not a number, not a bool and not an error either. `{key}` is
the house spelling.

```yaml
text: "{char_name}"           # Replaced with bound value of "char_name"
text: "{hp} / {max_hp}"       # Multiple placeholders in one string
nine_slice: "{slot_head_9s}"  # Dynamic nine-slice name
value: "{char_hp}"            # Resolved to float for progress_bar
disabled: "{craft_disabled}"  # Resolved to bool ("true"/"1" = disabled)
visible: "{show_panel}"       # "false"/"0" hides widget
```

### Binding Types

| Go Method | YAML Usage | Description |
|-----------|-----------|-------------|
| `Bind(key, value)` | `{key}` in text/nine_slice/visible/disabled | String replacement |
| `BindFloat(key, v)` | `{key}` in value/max | Float for progress bars |
| `BindInt(key, v)` | `{key}` in text | Integer display |
| `BindColor(key, color)` | via `color_key`/`fg_color_key` | Dynamic [3]float32 color |

`value`, `max` and `state` on an indicator resolve through the same three:
`BindFloat` for the two numbers, `Bind` with `"true"`/`"false"` for the state.

### Color Bindings

`color_key` and `fg_color_key` reference colors set via `BindColor()`. If a `color_key` is set and a matching color binding exists, it overrides the static `color` property. This enables runtime color changes (e.g., stat colors that change when equipment bonuses apply).

## Events

Interactive widgets emit `UIEvent` structs collected via `DrainEvents()`.

| Widget | Event Kind | Value |
|--------|-----------|-------|
| `button` | `"click"` | The `on_click` string |
| `text_input` | `"submit"` | The typed text content |

## Dynamic Children

`SetChildren(parentID, defs)` replaces all children of a node at runtime. Use this for dynamic lists (e.g., recipe rows in a scroll_view, inventory slot grids). Old children are removed from the index; new children are built and indexed.

## Available Nine-Slice Textures

These are the standard nine-slice assets in the stone theme:

| Name | Usage |
|------|-------|
| `panel_dark` | Primary window background |
| `panel_mid` | Slightly lighter panel (toolbar areas) |
| `panel_raised` | Elevated panel (headers, cards) |
| `panel_inset` | Sunken/recessed area |
| `slot_empty` | Empty equipment/inventory slot |
| `slot_equipped` | Slot containing an item |
| `slot_selected` | Currently selected slot |
| `slot_common` | Common rarity slot border |
| `slot_uncommon` | Uncommon rarity slot border |
| `slot_rare` | Rare rarity slot border |
| `slot_epic` | Epic rarity slot border |
| `slot_legendary` | Legendary rarity slot border |
| `slot_mythic` | Mythic rarity slot border |
| `btn_gold` | Primary action button |
| `btn_dark` | Secondary/minor button |
| `tab_active` | Active tab |
| `tab_inactive` | Inactive tab |
| `input_field` | Text input background |
| `divider_gold` | Gold accent line (top/bottom of windows) |
| `divider_subtle` | Faint gradient section separator |
| `border_dark` | 1px structural border between sections |
| `bar_hp_bg` / `bar_hp_fg` | HP bar nine-slice (bg/fg) |
| `bar_mana_bg` / `bar_mana_fg` | Mana bar nine-slice |
| `bar_end_bg` / `bar_end_fg` | Endurance bar nine-slice |
| `bar_xp_bg` / `bar_xp_fg` | XP bar nine-slice |

## Color Palette (Stone Theme)

Colors are sRGB floats in 0..1 — the hex column is the same number times 255.
`ui.frag` decodes them and the swapchain re-encodes, so what you write is what
reaches the display.

| Name | Value | Hex (sRGB) | Usage |
|------|-------|------------|-------|
| gold | [0.784, 0.651, 0.306] | #C8A64E | Headers, accents, section labels |
| gold_dim | [0.541, 0.447, 0.204] | #8A7234 | Stat abbreviations, section headings |
| parchment | [0.816, 0.808, 0.784] | #D0CEC8 | Primary body text |
| parch_dim | [0.541, 0.533, 0.502] | #8A8880 | Secondary/label text |
| green | [0.290, 0.620, 0.247] | #4A9E3F | HP bar fill |
| blue | [0.227, 0.416, 0.722] | #3A6AB8 | Mana bar fill, guild names |
| yellow | [0.722, 0.643, 0.227] | #B8A43A | Endurance bar fill |
| bg | [0.086, 0.086, 0.094] | #161618 | Deepest background |

## Structural Patterns

### Window Frame

Standard window structure with gold accents and header:

```yaml
widget: panel
width: 620
height: 520
nine_slice: panel_dark
color: [1, 1, 1]
layout: vertical
gap: 0
children:
  # Gold accent top
  - widget: panel
    height: 3
    nine_slice: divider_gold
    color: [1, 1, 1]

  # Header
  - widget: panel
    height: 80
    padding: 14
    layout: horizontal
    gap: 10
    children: [...]

  # Header/body separator
  - widget: panel
    height: 1
    nine_slice: border_dark
    color: [1, 1, 1]

  # Body
  - widget: panel
    flex: 1
    layout: horizontal
    children: [...]

  # Gold accent bottom
  - widget: panel
    height: 2
    nine_slice: divider_gold
    color: [1, 1, 1]
    opacity: 0.5
```

### Multi-Column Layout

Use horizontal layout with fixed-width columns and 1px separators:

```yaml
- widget: panel
  flex: 1
  layout: horizontal
  gap: 0
  children:
    - widget: panel
      width: 210
      padding: 20
      layout: vertical
      children: [...]

    # Vertical separator
    - widget: panel
      width: 1
      nine_slice: border_dark
      color: [1, 1, 1]

    - widget: panel
      width: 260
      padding: 20
      layout: vertical
      children: [...]
```

### Spacer

An empty flex panel pushes siblings apart:

```yaml
- widget: panel
  flex: 1
```

### Labeled Progress Bar

```yaml
- widget: panel
  layout: vertical
  gap: 3
  children:
    - widget: panel
      layout: horizontal
      children:
        - widget: label
          text: "HP"
          font_size: 10
          color: [0.541, 0.533, 0.502]
          width: 40
        - widget: label
          text: "{hp} / {max_hp}"
          font_size: 11
          color: [0.541, 0.533, 0.502]
          flex: 1
          align: right
    - widget: progress_bar
      height: 8
      value: "{hp}"
      max: "{max_hp}"
      fg_color: [0.290, 0.620, 0.247]
      bg_color: [0.102, 0.149, 0.094]
```

### Equipment Slot Grid

```yaml
- widget: panel
  layout: horizontal
  gap: 10
  children:
    - widget: button
      id: slot_head
      width: 46
      height: 46
      nine_slice: "{slot_head_9s}"
      color: [0.5, 0.5, 0.5]
      color_key: slot_head_color
      on_click: select_slot_head
      children:
        - widget: icon
          sprite: "{slot_head_icon}"
          width: 22
          height: 22
          color: [0.8, 0.8, 0.8]
          color_key: slot_head_icon_color
```

### Tab Bar

```yaml
- widget: panel
  layout: horizontal
  gap: 0
  height: 30
  children:
    - widget: button
      id: tab_all
      text: "ALL"
      font_size: 10
      flex: 1
      nine_slice: tab_active
      color: [0.784, 0.651, 0.306]
      on_click: filter_all
    - widget: button
      id: tab_weapons
      text: "WEAPONS"
      font_size: 10
      flex: 1
      nine_slice: tab_inactive
      color: [0.541, 0.533, 0.502]
      on_click: filter_weapons
```

## SetChildren Usage Patterns

`SetChildren(parentID, defs)` replaces all children of a named node at runtime. This is how dynamic, data-driven lists are populated — the YAML defines the container, Go code generates the rows.

### How It Works

1. Define an empty (or placeholder) container in YAML with a unique `id`.
2. In Go, build a `[]yamlui.WidgetDef` slice representing the dynamic rows.
3. Call `tree.SetChildren("container_id", defs)` before `BuildAt()`.

Old children are removed from the node index. New children are built, indexed, and participate in layout normally.

### Recipe List in a Scroll View

**YAML** — define the scroll container, leave it empty:

```yaml
- widget: scroll_view
    id: recipe_scroll
    flex: 1
    scroll_direction: vertical
    gap: 2
    # children populated by SetChildren
```

**Go** — generate a row for each recipe:

```go
var rows []yamlui.WidgetDef
for i, recipe := range recipes {
    id := fmt.Sprintf("recipe_%d", i)
    rows = append(rows, yamlui.WidgetDef{
        Widget:    "button",
        ID:        id,
        Height:    48,
        NineSlice: "panel_raised",
        Color:     [3]float32{1, 1, 1},
        OnClick:   fmt.Sprintf("select_recipe_%d", i),
        Layout:    "horizontal",
        Gap:       10,
        Padding:   8,
        Children: []yamlui.WidgetDef{
            {
                Widget: "icon",
                Sprite: recipe.Icon,
                Width:  32, Height: 32,
                Color: [3]float32{0.8, 0.8, 0.8},
            },
            {
                Widget:   "label",
                Text:     recipe.Name,
                FontSize: 14,
                Color:    [3]float32{0.816, 0.808, 0.784},
                Flex:     1,
            },
        },
    })
}
tree.SetChildren("recipe_scroll", rows)
```

### Material Requirements List

Same pattern — vertical list of rows with quantity badges:

```go
var mats []yamlui.WidgetDef
for _, mat := range selectedRecipe.Materials {
    owned := inventory.Count(mat.ItemID)
    have := owned >= mat.Qty
    textColor := [3]float32{0.722, 0.353, 0.353} // red
    if have {
        textColor = [3]float32{0.416, 0.722, 0.353} // green
    }
    mats = append(mats, yamlui.WidgetDef{
        Widget:  "panel",
        Height:  36,
        Layout:  "horizontal",
        Gap:     10,
        Padding: 4,
        Children: []yamlui.WidgetDef{
            {Widget: "icon", Sprite: mat.Icon, Width: 24, Height: 24, Color: [3]float32{0.8, 0.8, 0.8}},
            {Widget: "label", Text: mat.Name, FontSize: 13, Color: [3]float32{0.816, 0.808, 0.784}, Flex: 1},
            {Widget: "label", Text: fmt.Sprintf("%d / %d", owned, mat.Qty), FontSize: 13, Color: textColor, Align: "right"},
        },
    })
}
tree.SetChildren("materials_scroll", mats)
```

### Updating Tab Active States

SetChildren can also swap out static widgets. For tab bars, rebuild the tab row with the active tab's nine-slice changed:

```go
tabs := []struct{ ID, Label, Filter string }{
    {"tab_all", "ALL", "filter_all"},
    {"tab_weapons", "WEAPONS", "filter_weapons"},
    {"tab_armor", "ARMOR", "filter_armor"},
}
var defs []yamlui.WidgetDef
for _, tab := range tabs {
    ns := "tab_inactive"
    color := [3]float32{0.541, 0.533, 0.502}
    if tab.Filter == activeFilter {
        ns = "tab_active"
        color = [3]float32{0.784, 0.651, 0.306}
    }
    defs = append(defs, yamlui.WidgetDef{
        Widget: "button", ID: tab.ID, Text: tab.Label,
        FontSize: 10, Flex: 1,
        NineSlice: ns, Color: color,
        OnClick: tab.Filter,
    })
}
tree.SetChildren("category_tabs", defs)
```

### Guidelines

- **Call SetChildren every frame** (or when data changes) before `BuildAt()`. The tree does not diff — it rebuilds the subtree each time.
- **Give dynamic children unique IDs** if they need to emit events or be referenced. Use indexed IDs like `"recipe_0"`, `"recipe_1"`.
- **Keep row definitions simple.** Each row is a `WidgetDef` struct built in Go. Deeply nested rows are valid but harder to maintain.
- **Scroll state is preserved.** `scroll_view` tracks scroll position by its own ID, so replacing children doesn't reset the scroll offset. It is clamped against the new content, so a list that got shorter does not stay scrolled past its end.
- **Performance.** SetChildren rebuilds the node subtree and re-indexes. For lists under ~100 items this is negligible. For very large lists, only populate the visible portion.

## JSX-to-YamlUI Conversion Guide

When converting from JSX/React mockups to YamlUI:

1. **`<div>` → `panel`**. A div with flex-direction becomes `layout: horizontal` or `layout: vertical`.

2. **CSS flex → `flex` / `width` / `height`**. `flex: 1` maps directly. `width: 200px` → `width: 200`. Percentage widths must be converted to fixed or flex.

3. **`<span>` / `<p>` / `<h1>` → `label`**. Set `font_size` to approximate the heading level. Use `font: display` for headings, `font: body` for body text (these are hints only).

4. **`<button>` → `button`**. The `onClick` handler name becomes `on_click`. Disabled state uses `disabled: "{binding}"`.

5. **`<input>` → `text_input`**. Only single-line text. `placeholder` maps directly.

6. **`<img>` → `icon`**. The `src` becomes `sprite`.

7. **CSS `padding` → `padding`**. YamlUI only supports uniform padding (single value for all sides).

8. **CSS `gap` → `gap`**. Maps directly for flex containers.

9. **CSS `border` → `nine_slice: border_dark` with `height: 1` or `width: 1`**. Borders are separate panel children, not a property on the parent.

10. **CSS `background-color` → `bg_color` or `nine_slice`**. Use `bg_color` for flat colors. Use `nine_slice` for textured/rounded backgrounds.

11. **CSS `overflow: scroll` → `scroll_view`**. Wrap scrollable content in a scroll_view widget. `overflow: hidden` is the same widget with `scrollbar: never` and `drag: false`: the clip is unconditional, so a view nothing can scroll still clips.

12. **Dynamic values → `{binding}` templates**. Replace `{props.value}` or `${variable}` with `{binding_key}`.

13. **Conditional rendering → `visible`**. Replace `{condition && <Component/>}` with `visible: "{condition_binding}"`.

14. **No margin**. YamlUI has no margin property. Use `gap` on the parent or a spacer panel.

15. **No border-radius**. Rounded corners come from the nine-slice texture, not a CSS property.

16. **Colors are sRGB floats**. A CSS hex color converts by dividing each byte by 255 and nothing else: `#C8A64E` is `[0.784, 0.651, 0.306]`. Do not apply a gamma — the shader decodes and the swapchain re-encodes, and squaring the value here darkens every colour in the file.
