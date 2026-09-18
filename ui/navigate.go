package ui

import "github.com/derekmwright/glyphengine/input"

// Navigable is a widget a keyboard or gamepad can move a highlight onto and
// activate. Button implements it.
//
// It is separate from Clickable because the two answer different questions.
// Clickable asks "is the pointer on you"; Navigable asks "are you somewhere the
// highlight can rest", which a disabled item is not even though it is still
// there to be clicked on and ignored.
type Navigable interface {
	Widget
	Contains(mx, my float32) bool

	// SetHighlighted is the only thing that drives the widget's highlight. The
	// manager calls it on every registered widget each frame, so a widget must
	// not also set its own from the pointer -- see UIManager.RegisterNavigable.
	SetHighlighted(bool)

	// Activate is what Enter, Space or a click does.
	Activate()

	// Enabled reports whether the highlight may rest here. A disabled item is
	// skipped by traversal and cannot be activated.
	Enabled() bool
}

// RegisterNavigable adds a widget to the keyboard traversal order, in the order
// registered.
//
// The manager then owns that widget's highlight, including from the pointer.
// **Do not also call Button.UpdateHover on a registered widget**: it recomputes
// the highlight from the mouse position on its own, so the two would fight and
// the arrow keys would appear to do nothing while the mouse sat still.
//
// That single ownership is the point rather than an implementation detail. A
// menu where the pointer and the keyboard each keep their own highlight is a
// menu where the mouse says one thing and Enter does another.
func (m *UIManager) RegisterNavigable(n Navigable) {
	m.navigables = append(m.navigables, n)
	if m.hot < 0 {
		// Land the highlight on something that can actually be chosen, rather
		// than on index 0 whatever it happens to be.
		m.hot = m.nextEnabled(-1, +1)
	}
}

// ClearNavigables empties the traversal order, for a menu that is rebuilt or
// swapped for another screen.
func (m *UIManager) ClearNavigables() {
	for _, n := range m.navigables {
		n.SetHighlighted(false)
	}
	m.navigables = nil
	m.hot = -1
}

// Highlighted returns the widget the highlight is resting on, or nil.
func (m *UIManager) Highlighted() Navigable {
	if m.hot < 0 || m.hot >= len(m.navigables) {
		return nil
	}
	return m.navigables[m.hot]
}

// FocusNext moves the highlight to the next enabled widget, wrapping at the
// end.
func (m *UIManager) FocusNext() { m.moveHighlight(+1) }

// FocusPrev moves the highlight to the previous enabled widget, wrapping at the
// start.
func (m *UIManager) FocusPrev() { m.moveHighlight(-1) }

// ActivateHighlighted activates the highlighted widget, if there is one and it
// is enabled.
func (m *UIManager) ActivateHighlighted() {
	n := m.Highlighted()
	if n == nil || !n.Enabled() || !n.Visible() {
		return
	}
	n.Activate()
}

func (m *UIManager) moveHighlight(step int) {
	if next := m.nextEnabled(m.hot, step); next >= 0 {
		m.hot = next
	}
}

// nextEnabled returns the index of the next enabled, visible widget from,
// exclusive, in the direction step. It returns -1 when there is none.
//
// The scan is bounded by the list length rather than looping until it finds
// something. A menu whose items are all disabled -- a save slot screen with no
// saves, a shop with nothing affordable -- would otherwise spin forever on the
// first arrow key, which is the classic way to write this wrong.
func (m *UIManager) nextEnabled(from, step int) int {
	n := len(m.navigables)
	if n == 0 {
		return -1
	}
	idx := from
	for i := 0; i < n; i++ {
		idx += step
		// Wrap at both ends, so a menu is a ring rather than a line with two
		// dead stops.
		if idx < 0 {
			idx = n - 1
		} else if idx >= n {
			idx = 0
		}
		if w := m.navigables[idx]; w.Visible() && w.Enabled() {
			return idx
		}
	}
	return -1
}

// handleNavigation moves the highlight from the keyboard and from pointer
// motion, then pushes it to the widgets.
//
// Called from HandleInput; split out so the traversal can be driven by a test
// without a window, and so a game binding a gamepad can call FocusNext and
// friends directly.
func (m *UIManager) handleNavigation(inp *input.Input, mouseMoved bool) {
	if len(m.navigables) == 0 {
		return
	}

	// The pointer claims the highlight only when it actually moves. A mouse
	// left sitting over one item would otherwise drag the highlight back every
	// frame and the arrow keys would look broken.
	if mouseMoved {
		for i, n := range m.navigables {
			if n.Visible() && n.Enabled() && n.Contains(m.mouseX, m.mouseY) {
				m.hot = i
				break
			}
		}
	}

	switch {
	case inp.KeyPressed(input.KeyUp), inp.KeyPressed(input.KeyW):
		m.FocusPrev()
		m.keyConsumed = true
	case inp.KeyPressed(input.KeyDown), inp.KeyPressed(input.KeyS):
		m.FocusNext()
		m.keyConsumed = true
	}

	// Enter and Space both activate, which is what every menu does and what a
	// player will try without thinking about it.
	if inp.KeyPressed(input.KeyEnter) || inp.KeyPressed(input.KeySpace) {
		m.ActivateHighlighted()
		m.keyConsumed = true
	}

	m.syncHighlight()
}

// syncHighlight pushes the highlight to every registered widget.
//
// Run every frame rather than only on change, so a widget that was rebuilt or
// re-registered still reflects it, and so exactly one is ever lit -- the others
// are actively cleared rather than left to remember.
func (m *UIManager) syncHighlight() {
	for i, n := range m.navigables {
		n.SetHighlighted(i == m.hot)
	}
}
