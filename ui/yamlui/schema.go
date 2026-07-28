package yamlui

import (
	"os"

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

	Children []WidgetDef `yaml:"children"`
}

// ParseFile reads and parses a YAML widget definition from a file.
func ParseFile(path string) (*WidgetDef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var def WidgetDef
	if err := yaml.Unmarshal(data, &def); err != nil {
		return nil, err
	}
	return &def, nil
}
