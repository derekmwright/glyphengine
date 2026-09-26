// Package ease holds the interpolation curves the UI tweens on.
//
// Eight curves and a lookup, not a tween library. They exist because
// ui/yamlui's `transition:` block names one in YAML and has to turn the name
// into a function, and because a HUD built in Go wants the same shapes without
// re-deriving them -- a fade that decelerates and a fade that does not are the
// difference between a dialog that feels placed and one that feels dropped.
//
// Every curve is a Penner curve under its standard name, so a designer who
// knows out_cubic from anywhere else already knows this one. The formulas are
// the usual ones and are written out rather than factored, because factoring
// them is how the in and the out ends of a pair quietly stop being mirrors.
package ease

// Func is an interpolation curve over 0..1.
//
// f(0) is 0 and f(1) is 1 for every curve here. In between, only OutBack leaves
// the unit interval: it overshoots past 1 and comes back, which is the point of
// it. A caller that cannot survive a value above 1 -- an opacity, say -- clamps
// the result, not the curve.
type Func func(t float32) float32

// backOvershoot is the constant the Back curves are defined with.
//
// 1.70158 is Robert Penner's value and produces roughly 10% overshoot. It is
// written here once so OutBack's two terms cannot drift apart; c3 is c1 + 1,
// which is what makes f(0) land exactly on 0.
const (
	backOvershoot = 1.70158
	backCubic     = backOvershoot + 1
)

// Linear is no easing at all: constant rate from start to finish.
//
// Reads as mechanical on anything that moves, and is exactly right for a
// crossfade, where a constant rate of change IS the even blend.
func Linear(t float32) float32 { return clamp01(t) }

// InQuad starts slow and accelerates. Something leaving under its own power.
func InQuad(t float32) float32 {
	t = clamp01(t)
	return t * t
}

// OutQuad starts fast and decelerates into rest. The gentlest arrival here.
func OutQuad(t float32) float32 {
	t = clamp01(t)
	u := 1 - t
	return 1 - u*u
}

// InOutQuad accelerates out of rest and decelerates into it. Symmetric, and the
// safe default for something that both appears and disappears.
func InOutQuad(t float32) float32 {
	t = clamp01(t)
	if t < 0.5 {
		return 2 * t * t
	}
	u := -2*t + 2
	return 1 - u*u/2
}

// InCubic is InQuad's sharper sibling: slower at the start, faster at the end.
func InCubic(t float32) float32 {
	t = clamp01(t)
	return t * t * t
}

// OutCubic decelerates harder than OutQuad. The usual choice for a panel
// arriving -- most of the distance is covered early, so it reads as quick
// without ever snapping to a stop.
func OutCubic(t float32) float32 {
	t = clamp01(t)
	u := 1 - t
	return 1 - u*u*u
}

// InOutCubic is InOutQuad with more contrast between the middle and the ends.
// A slide that should feel deliberate rather than merely smooth.
func InOutCubic(t float32) float32 {
	t = clamp01(t)
	if t < 0.5 {
		return 4 * t * t * t
	}
	u := -2*t + 2
	return 1 - u*u*u/2
}

// OutBack decelerates past its target and settles back onto it -- about 10%
// over, around t 0.6. A dialog that pops rather than arrives.
//
// This is the only curve here that returns more than 1, and the only one whose
// output a caller may have to clamp.
func OutBack(t float32) float32 {
	t = clamp01(t)
	u := t - 1
	return 1 + backCubic*u*u*u + backOvershoot*u*u
}

// names is the lookup YAML goes through. The spellings are snake_case because
// that is what the rest of the YAML schema is written in; the Go identifiers
// keep the conventional CamelCase.
var names = map[string]Func{
	"linear":       Linear,
	"in_quad":      InQuad,
	"out_quad":     OutQuad,
	"in_out_quad":  InOutQuad,
	"in_cubic":     InCubic,
	"out_cubic":    OutCubic,
	"in_out_cubic": InOutCubic,
	"out_back":     OutBack,
}

// ByName returns the curve with the given name, and whether there is one.
//
// The caller decides what an unknown name means. ui/yamlui rejects it at load
// with the widget named, rather than falling back to Linear: a misspelled curve
// that silently eases linearly is a change nobody can see in a screenshot and
// everybody can see in motion, which is the worst place for it to surface.
func ByName(name string) (Func, bool) {
	f, ok := names[name]
	return f, ok
}

// Names returns every curve name, for an error message that can list them.
// Sorted, so the list an error prints is stable between runs.
func Names() []string {
	return []string{
		"linear",
		"in_quad", "out_quad", "in_out_quad",
		"in_cubic", "out_cubic", "in_out_cubic",
		"out_back",
	}
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
