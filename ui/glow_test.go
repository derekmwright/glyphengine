package ui

import "testing"

// TestLabelGlowReachesTheTextLine: the widget is what a game sets, the TextLine
// is what the renderer reads, and a field that exists on both and is copied by
// neither Build path is a glow that was asked for and silently never drawn --
// the state Sky.LightShafts was in for seven weeks.
//
// Verified to fail: with `Glow: l.Glow` dropped from BuildAt the first case
// reports a glow of 0, and dropped from Build the second does.
func TestLabelGlowReachesTheTextLine(t *testing.T) {
	l := &Label{Text: "REACTOR CRITICAL", FontSize: 14, Color: [3]float32{1, 0.6, 0.2}, Glow: 2.5}

	// containerW 0 is left-aligned, which is the path that never measures the
	// text and so needs no font.
	lines := l.BuildAt(10, 20, 14, 0, nil)
	if len(lines) != 1 || lines[0].Glow != 2.5 {
		t.Errorf("BuildAt produced %+v, want one line with a glow of 2.5", lines)
	}

	l.Glow = 0
	if lines := l.BuildAt(10, 20, 14, 0, nil); len(lines) != 1 || lines[0].Glow != 0 {
		t.Errorf("a label that asks for no glow produced %+v", lines)
	}
}
