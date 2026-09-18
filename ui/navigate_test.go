package ui

import "testing"

// The parts of menu traversal that every reimplementation gets subtly
// different: skipping disabled items, wrapping at both ends, opening on
// something that can actually be chosen, and not spinning forever when nothing
// can be.
//
// These drive UIManager's own traversal rather than a copy of it, so deleting
// the logic fails them.

// navItem is a Navigable with no rendering attached, so traversal can be tested
// without a renderer, a font or a window.
type navItem struct {
	name        string
	disabled    bool
	hidden      bool
	rect        Rect
	highlighted bool
	activations int
}

func (n *navItem) Bounds() Rect                 { return n.rect }
func (n *navItem) Visible() bool                { return !n.hidden }
func (n *navItem) Contains(mx, my float32) bool { return n.rect.Contains(mx, my) }
func (n *navItem) SetHighlighted(v bool)        { n.highlighted = v }
func (n *navItem) Activate()                    { n.activations++ }
func (n *navItem) Enabled() bool                { return !n.disabled && !n.hidden }

func menu(items ...*navItem) *UIManager {
	m := NewUIManager()
	for _, it := range items {
		m.RegisterNavigable(it)
	}
	return m
}

func item(name string) *navItem { return &navItem{name: name} }

// highlightedName is what the player would see highlighted.
func highlightedName(m *UIManager) string {
	n, _ := m.Highlighted().(*navItem)
	if n == nil {
		return "<none>"
	}
	return n.name
}

func TestTraversalOpensOnSomethingChoosable(t *testing.T) {
	// A menu whose first item is unavailable — "Continue" with no save.
	first := item("continue")
	first.disabled = true
	m := menu(first, item("new"), item("quit"))

	if got := highlightedName(m); got != "new" {
		t.Errorf("menu opened on %q, want the first enabled item", got)
	}
}

func TestTraversalSkipsDisabled(t *testing.T) {
	a, b, c := item("a"), item("b"), item("c")
	b.disabled = true
	m := menu(a, b, c)

	m.FocusNext()
	if got := highlightedName(m); got != "c" {
		t.Errorf("next from a landed on %q, want c — the disabled item was not skipped", got)
	}
	m.FocusPrev()
	if got := highlightedName(m); got != "a" {
		t.Errorf("prev from c landed on %q, want a", got)
	}
}

func TestTraversalSkipsHidden(t *testing.T) {
	a, b, c := item("a"), item("b"), item("c")
	b.hidden = true
	m := menu(a, b, c)

	m.FocusNext()
	if got := highlightedName(m); got != "c" {
		t.Errorf("next from a landed on %q, want c — the hidden item was not skipped", got)
	}
}

// A menu is a ring, not a line with two dead stops.
func TestTraversalWrapsAtBothEnds(t *testing.T) {
	m := menu(item("a"), item("b"), item("c"))

	m.FocusPrev()
	if got := highlightedName(m); got != "c" {
		t.Errorf("prev from the first item landed on %q, want c", got)
	}
	m.FocusNext()
	if got := highlightedName(m); got != "a" {
		t.Errorf("next from the last item landed on %q, want a", got)
	}
}

// The classic way to write this wrong: scan forward until something enabled
// turns up. With nothing enabled it never turns up.
//
// A save-slot screen with no saves and a shop with nothing affordable are both
// this, and a hang on the first arrow key is not a subtle failure.
func TestTraversalTerminatesWhenNothingIsEnabled(t *testing.T) {
	a, b := item("a"), item("b")
	a.disabled, b.disabled = true, true
	m := menu(a, b)

	// If the scan were unbounded these would never return, and the test binary
	// would be killed by its own timeout rather than reporting a failure -- a
	// hang is the failure signal here.
	m.FocusNext()
	m.FocusPrev()

	if m.Highlighted() != nil {
		t.Error("highlight rests on a disabled item")
	}
}

func TestActivateRunsTheHighlightedItem(t *testing.T) {
	a, b := item("a"), item("b")
	m := menu(a, b)

	m.FocusNext()
	m.ActivateHighlighted()

	if b.activations != 1 {
		t.Errorf("highlighted item ran %d times, want 1", b.activations)
	}
	if a.activations != 0 {
		t.Errorf("a non-highlighted item ran %d times", a.activations)
	}
}

// Traversal skips disabled items, so the highlight should never be on one — but
// a caller can disable the item under the highlight after the fact, and
// activating it then would fire something the player can see is unavailable.
func TestActivateIgnoresAnItemDisabledUnderTheHighlight(t *testing.T) {
	a := item("a")
	m := menu(a, item("b"))

	a.disabled = true
	m.ActivateHighlighted()

	if a.activations != 0 {
		t.Errorf("a disabled item was activated %d times", a.activations)
	}
}

// One highlight, not two. The manager pushes it to every widget each frame, so
// exactly one is lit and the rest are cleared.
func TestExactlyOneItemIsHighlighted(t *testing.T) {
	a, b, c := item("a"), item("b"), item("c")
	m := menu(a, b, c)
	m.FocusNext()
	m.syncHighlight()

	lit := 0
	for _, it := range []*navItem{a, b, c} {
		if it.highlighted {
			lit++
		}
	}
	if lit != 1 {
		t.Errorf("%d items are highlighted, want exactly 1", lit)
	}
	if !b.highlighted {
		t.Error("the highlighted item is not the one traversal moved to")
	}
}

func TestClearNavigablesDropsTheHighlight(t *testing.T) {
	a := item("a")
	m := menu(a, item("b"))
	m.syncHighlight()

	m.ClearNavigables()

	if a.highlighted {
		t.Error("clearing left an item highlighted")
	}
	if m.Highlighted() != nil {
		t.Error("clearing left a highlight")
	}
}
