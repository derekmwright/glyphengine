package yamlui

import (
	"fmt"
	"io/fs"
	"strconv"

	"gopkg.in/yaml.v3"
)

// WidgetDef is the YAML-parsed definition of a UI widget.
type WidgetDef struct {
	Widget  string  `yaml:"widget"`
	ID      string  `yaml:"id"`
	Width   float32 `yaml:"width"`
	Height  float32 `yaml:"height"`
	Flex    float32 `yaml:"flex"` // proportional sizing in horizontal/vertical layout
	Padding float32 `yaml:"padding"`
	Layout  string  `yaml:"layout"`
	Gap     float32 `yaml:"gap"`

	// Panel
	NineSlice string     `yaml:"nine_slice"`
	Color     [3]float32 `yaml:"color"`
	ColorKey  string     `yaml:"color_key"` // dynamic color via BindColor
	Opacity   float32    `yaml:"opacity"`

	// Label
	Text     string  `yaml:"text"`
	Font     string  `yaml:"font"` // font family (parsed, unused for now)
	FontSize float32 `yaml:"font_size"`
	Align    string  `yaml:"align"`
	Overlay  string  `yaml:"overlay"`

	// Progress bar
	Value   string     `yaml:"value"`
	Max     string     `yaml:"max"`
	FgColor [3]float32 `yaml:"fg_color"`
	BgColor [3]float32 `yaml:"bg_color"`

	// Progress bar nine-slice foreground
	FgNineSlice string `yaml:"fg_nine_slice"`

	// Dynamic foreground color (template key, resolved at build time)
	FgColorKey string `yaml:"fg_color_key"`

	// Button
	OnClick  string `yaml:"on_click"` // event name emitted on click
	Disabled string `yaml:"disabled"` // template: "true"/"false"

	// State is a template bool for a toggle widget on button and icon:
	// anything that does not resolve to "true"/"1" dims the widget's tint by
	// stateDim. It is separate from Disabled because an inactive toggle is
	// still clickable -- a cooldown that is running does not stop a game from
	// telling the player why.
	State string `yaml:"state"`

	// Scroll view
	ScrollDirection string `yaml:"scroll_direction"` // "vertical" (default)

	// Icon
	Sprite string `yaml:"sprite"` // icon sprite name for IconBuilder callback

	// Text input
	Placeholder      string     `yaml:"placeholder"`       // shown when empty + unfocused
	PlaceholderColor [3]float32 `yaml:"placeholder_color"` // explicit placeholder color
	Mask             string     `yaml:"mask"`              // mask character (e.g. "*" for passwords)

	// Visibility
	Visible string `yaml:"visible"` // template: "false"/"0" hides widget

	// Indicator is an optional driven overlay on panel, button and icon --
	// a cooldown sweep, a wipe or a tint. See indicator.go.
	Indicator *IndicatorDef `yaml:"indicator"`

	Children []WidgetDef `yaml:"children"`
}

// ParseFS reads and parses a YAML widget definition from fsys.
func ParseFS(fsys fs.FS, name string) (*WidgetDef, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	var def WidgetDef
	if err := yaml.Unmarshal(data, &def); err != nil {
		return nil, err
	}
	if err := validateDef(&def); err != nil {
		return nil, err
	}
	return &def, nil
}

// validateDef walks a parsed tree and rejects anything that would otherwise
// render as a quietly wrong shape.
//
// It runs at parse time rather than at first draw because the failures it
// catches are invisible at runtime: an indicator with a misspelled direction
// still draws something, and a game would have to notice by looking at a
// screenshot. Everything else in this package tolerates a missing binding, so
// this is the only place that says no.
func validateDef(def *WidgetDef) error {
	if def.Indicator != nil {
		switch def.Widget {
		case "panel", "button", "icon":
		default:
			return fmt.Errorf("widget %s: indicator is only supported on panel, button and icon, not %s",
				widgetLabel(def), def.Widget)
		}
		if err := def.Indicator.validate(widgetLabel(def)); err != nil {
			return err
		}
	}
	if def.State != "" {
		switch def.Widget {
		case "button", "icon":
		default:
			return fmt.Errorf("widget %s: state is only supported on button and icon, not %s",
				widgetLabel(def), def.Widget)
		}
	}
	for i := range def.Children {
		if err := validateDef(&def.Children[i]); err != nil {
			return err
		}
	}
	return nil
}

// widgetLabel names a widget in an error. The id is what a reader can search
// the YAML for; a widget without one is named by its type instead, which is
// still enough to find when the alternative is an error that names nothing.
func widgetLabel(def *WidgetDef) string {
	if def.ID != "" {
		return strconv.Quote(def.ID)
	}
	if def.Widget != "" {
		return "of type " + strconv.Quote(def.Widget) + " with no id"
	}
	return "with no id"
}
