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
| `color` | [3]float | [0,0,0] | RGB tint applied to nine-slice (linear space) |
| `color_key` | string | — | Dynamic color binding key (overrides `color` if bound) |
| `opacity` | float | 1.0 | Alpha for nine-slice rendering (0.0 = fully transparent) |
| `bg_color` | [3]float | — | Flat solid-color quad fallback when no `nine_slice` is set |
| `visible` | string | — | Template. `"false"` or `"0"` hides widget and all children |
| `overlay` | string | — | ID of sibling — positions this widget at the same Y as that sibling (vertical layout only) |
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
| `color` | [3]float | [0,0,0] | Text color (linear space) |
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
| `visible` | string | — | Visibility template |

### scroll_view

Scrollable vertical container. Handles mouse wheel input and renders a scrollbar thumb.

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `id` | string | **required** | Used for scroll state tracking |
| `flex` | float | — | Proportional sizing (typically `flex: 1` to fill remaining space) |
| `nine_slice` | string | — | Optional background |
| `color` | [3]float | [0,0,0] | Background tint |
| `opacity` | float | 1.0 | Background alpha |
| `padding` | float | 0 | Inner padding |
| `gap` | float | 0 | Spacing between children |
| `scroll_direction` | string | `"vertical"` | Only `"vertical"` is supported |
| `children` | list | — | Scrollable child widgets |
| `visible` | string | — | Visibility template |

Children outside the visible area are culled. Scroll thumb appears automatically when content exceeds view height.

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

Colors are linear-space RGB floats. The GPU applies sRGB gamma on output.

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
- **Scroll state is preserved.** `scroll_view` tracks scroll position by its own ID, so replacing children doesn't reset the scroll offset.
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

11. **CSS `overflow: scroll` → `scroll_view`**. Wrap scrollable content in a scroll_view widget.

12. **Dynamic values → `{binding}` templates**. Replace `{props.value}` or `${variable}` with `{binding_key}`.

13. **Conditional rendering → `visible`**. Replace `{condition && <Component/>}` with `visible: "{condition_binding}"`.

14. **No margin**. YamlUI has no margin property. Use `gap` on the parent or a spacer panel.

15. **No border-radius**. Rounded corners come from the nine-slice texture, not a CSS property.

16. **Colors are linear-space RGB**. CSS hex colors must be converted. To convert: `linear = (sRGB / 255) ^ 2.2`. For quick approximation: divide hex by 255, then square the result.
