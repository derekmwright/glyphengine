package glyphengine

import (
	"encoding/json"
	"fmt"
	"log"

	"golang.org/x/image/font/gofont/gomono"

	"github.com/derekmwright/glyphengine/msdf"
	"github.com/derekmwright/glyphengine/renderer"
)

// Debugf queues a line of text for the top-left corner of this frame.
//
//	e.Debugf("ToD %.4f", e.Scene.TimeOfDay())
//	e.Debugf("sun %+.4f", e.Scene.Environment().SunElevation)
//
// Lines are cleared every frame, so calling it from Update or LateUpdate shows
// the current value and calling it from nowhere shows nothing. Order is call
// order, top to bottom.
//
// It exists because the alternative is what this repo kept doing instead:
// screenshotting a frame that looks wrong and then guessing which time of day,
// sun elevation or setting produced it. A number in the corner turns "the glow
// lingers too long" into "the glow is still warm at sun -0.14", which is a bug
// report that can be acted on.
//
// The font is Go Mono, generated on first use rather than loaded, so this needs
// no asset and no setup. Generation costs a few hundred milliseconds once; if
// that matters in a shipping build, do not call Debugf in one.
//
// It draws over everything, in a channel of its own, so it neither disturbs nor
// is disturbed by SetMSDFOverlays.
func (e *Engine) Debugf(format string, args ...any) {
	e.debugLines = append(e.debugLines, fmt.Sprintf(format, args...))
}

// debugOverlay builds this frame's debug text, or returns nothing when Debugf
// was not called. The font is built on the first line ever queued.
func (e *Engine) debugOverlay() []renderer.RenderObject {
	if len(e.debugLines) == 0 {
		return nil
	}
	if e.debugText == nil {
		if err := e.initDebugFont(); err != nil {
			// A debug overlay must never take the frame down with it.
			log.Printf("debug text unavailable: %v", err)
			e.debugLines = e.debugLines[:0]
			return nil
		}
	}

	w, h := e.renderer.Extent()
	sw, sh := float32(w), float32(h)

	lines := make([]renderer.TextLine, len(e.debugLines))
	for i, s := range e.debugLines {
		lines[i] = renderer.TextLine{
			Text:  s,
			X:     18,
			Y:     18 + float32(i)*24,
			Scale: 20,
			Color: [3]float32{1, 1, 1},
		}
	}
	e.debugLines = e.debugLines[:0]

	e.debugText.SetText(e.renderer, lines, sw, sh)
	return []renderer.RenderObject{e.debugText.RenderObject(sw, sh, 48)}
}

func (e *Engine) initDebugFont() error {
	// Go Mono: monospaced, so a value changing between frames does not shift
	// the ones after it, and BSD-licensed and already in the module graph.
	atlas, err := msdf.Generate(gomono.TTF, msdf.Options{})
	if err != nil {
		return fmt.Errorf("generate debug font: %w", err)
	}
	meta, err := json.Marshal(atlas.Meta)
	if err != nil {
		return fmt.Errorf("debug font metadata: %w", err)
	}
	font, err := renderer.NewFontFromAtlas(e.renderer, atlas.Image, meta)
	if err != nil {
		return err
	}
	text, err := renderer.NewMSDFText(e.renderer, font)
	if err != nil {
		return err
	}
	e.debugFont, e.debugText = font, text
	return nil
}
