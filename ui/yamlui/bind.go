package yamlui

import (
	"strconv"
	"strings"
)

// resolve replaces all {key} placeholders in template with values from bindings.
func resolve(template string, bindings map[string]string) string {
	if !strings.Contains(template, "{") {
		return template
	}
	result := template
	for k, v := range bindings {
		result = strings.ReplaceAll(result, "{"+k+"}", v)
	}
	return result
}

// resolveFloat resolves a template string to a float32.
// If the template is a pure placeholder like "{hp}", looks up the binding and parses it.
// If it's a literal number string, parses directly.
func resolveFloat(template string, bindings map[string]string) float32 {
	s := resolve(template, bindings)
	f, _ := strconv.ParseFloat(s, 32)
	return float32(f)
}

// resolveBool resolves a template string to a bool.
// Returns true if the resolved value is "true" or "1".
func resolveBool(template string, bindings map[string]string) bool {
	s := resolve(template, bindings)
	return s == "true" || s == "1"
}
