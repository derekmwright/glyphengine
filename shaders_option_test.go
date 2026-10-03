package glyphengine

import (
	"bytes"
	"testing"

	"github.com/derekmwright/glyphengine/renderer"
)

// The bug this guards against is not a wrong value, it is a dropped one: an
// option that exists on Engine, is documented, and never reaches renderer.New.
// Engine.WithShaders was missing for exactly that long, and nothing would have
// noticed a refactor that silently stopped appending it either.
//
// renderer.Option is a plain func(*Renderer), so the options can be applied to
// a Renderer and read back without a device, a window or a frame.
func applyRendererOptions(opts []renderer.Option) *renderer.Renderer {
	var r renderer.Renderer
	for _, o := range opts {
		o(&r)
	}
	return &r
}

func TestWithShadersReachesTheRenderer(t *testing.T) {
	custom := renderer.DefaultShaders()
	custom.SkyFrag = []byte("not spir-v, and not the embedded sky either")

	var cfg config
	WithShaders(custom)(&cfg)

	got := applyRendererOptions(cfg.rendererOptions()).Shaders()
	if !bytes.Equal(got.SkyFrag, custom.SkyFrag) {
		t.Fatalf("SkyFrag did not reach the renderer:\n got %q\nwant %q", got.SkyFrag, custom.SkyFrag)
	}

	// This used to compare against the embedded default as well, to prove the
	// option had been APPLIED rather than that nothing had happened. That
	// comparison cannot say anything any more: SkyFrag is the sky slot and the
	// engine embeds nothing for it, so renderer.DefaultShaders().SkyFrag is nil
	// and the comparison is nil against a payload however the code behaves.
	// A dropped append leaves nil here, which the assertion above reports --
	// verified by dropping the append in config.rendererOptions: it fails with
	// `got ""`. The stage-carried-through half is
	// TestWithShadersKeepsTheStagesItWasNotGiven, which uses stages that do have
	// defaults.
	//
	// What is worth asserting is the premise, because the reasoning above goes
	// stale the moment it stops holding.
	if renderer.DefaultShaders().SkyFrag != nil {
		t.Error("the engine embeds a sky dome again; the sky slot is gone and the note above is wrong")
	}
}

func TestWithoutWithShadersTheRendererIsUntouched(t *testing.T) {
	var cfg config

	// No shader option, so nothing should set r.shaders at all -- New fills the
	// embedded defaults in later. A non-nil field here would mean the engine is
	// pushing a set of its own, which would quietly outrank the renderer's
	// own defaulting.
	//
	// LitFrag rather than SkyFrag, deliberately. SkyFrag is nil in the engine's
	// own defaults now, so asserting it is nil here would pass whether or not the
	// engine pushed a set -- which is the shape of check this repository has
	// shipped green before.
	//
	// Verified to fail: giving config.rendererOptions an `else` branch that appends
	// renderer.WithShaders(renderer.DefaultShaders()) reports `an engine built with
	// no shader option still set LitFrag (46604 bytes)`. With SkyFrag as the subject
	// it would have reported nothing, which is why the subject changed.
	if got := applyRendererOptions(cfg.rendererOptions()).Shaders(); got.LitFrag != nil {
		t.Fatalf("an engine built with no shader option still set LitFrag (%d bytes)", len(got.LitFrag))
	}
}

// Overriding one stage must not drop the other twenty-odd. This is the property
// the ShaderSet doc promises and the reason a game can swap a single shader.
func TestWithShadersKeepsTheStagesItWasNotGiven(t *testing.T) {
	custom := renderer.DefaultShaders()
	custom.SkyFrag = []byte("replacement")

	var cfg config
	WithShaders(custom)(&cfg)
	got := applyRendererOptions(cfg.rendererOptions()).Shaders()

	def := renderer.DefaultShaders()
	if !bytes.Equal(got.LitFrag, def.LitFrag) {
		t.Error("LitFrag was not carried through")
	}
	if !bytes.Equal(got.TonemapFrag, def.TonemapFrag) {
		t.Error("TonemapFrag was not carried through")
	}
	if len(got.LitFrag) == 0 {
		t.Error("LitFrag is empty, so the comparison above proves nothing")
	}
}
