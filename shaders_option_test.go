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

	// Break the fix and the check fails: dropping the append in
	// config.rendererOptions leaves SkyFrag nil here, which is what the
	// assertion above catches. Verified by doing it.
	if bytes.Equal(got.SkyFrag, renderer.DefaultShaders().SkyFrag) {
		t.Fatal("got the embedded default back, so the option was not applied")
	}
}

func TestWithoutWithShadersTheRendererIsUntouched(t *testing.T) {
	var cfg config

	// No shader option, so nothing should set r.shaders at all -- New fills the
	// embedded defaults in later. A non-nil field here would mean the engine is
	// pushing a set of its own, which would quietly outrank the renderer's
	// own defaulting.
	if got := applyRendererOptions(cfg.rendererOptions()).Shaders(); got.SkyFrag != nil {
		t.Fatalf("an engine built with no shader option still set SkyFrag (%d bytes)", len(got.SkyFrag))
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
