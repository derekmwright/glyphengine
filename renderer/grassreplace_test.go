package renderer

import "testing"

// TestReplaceGrassFirstCallDefersNothing is the base case InitGrass hits every
// run: nothing has been built yet, so there is nothing to retire. A no-op
// destroy queued here would mean a renderer that called InitGrass exactly
// once never sees Deferred return to 0, which is what examples/08-grass's
// -regrow loop (and task validate) checks at the top of every cycle.
func TestReplaceGrassFirstCallDefersNothing(t *testing.T) {
	r := &Renderer{}
	gs := &GrassSystem{}

	r.replaceGrass(gs, nil)

	if r.grass != gs {
		t.Fatalf("r.grass not installed: got %p, want %p", r.grass, gs)
	}
	if got := len(r.deferredDestroys); got != 0 {
		t.Errorf("first InitGrass queued %d deferred destroys, want 0 -- nothing to retire", got)
	}
}

// TestReplaceGrassDefersThePreviousGeneration is issue #87's core claim: a
// second InitGrass must not abandon what the first one built. The swap is
// immediate -- r.grass/r.grassImpostor name the new generation right away, so
// DrawFrame is never mid-swap -- but the retirement behind it has to wait out
// the frames in flight the same way DestroyModel's does, because a frame
// submitted just before this call can still be reading the previous atlas's
// descriptor set.
//
// BROKEN: made replaceGrass call prevImpostor.destroy(r) and
// prevGrass.Destroy(r.deviceDriver) directly instead of through
// r.DeferDestroy. FAILED with: "replaceGrass freed the previous generation
// immediately: 0 deferred destroys queued, want 1". Restored with
// `git checkout -- renderer/renderer.go`.
func TestReplaceGrassDefersThePreviousGeneration(t *testing.T) {
	r := &Renderer{}
	oldModel := &Model{owned: &modelResources{}}
	oldGrass := &GrassSystem{models: []*Model{oldModel}}
	oldImpostor := &grassImpostor{}
	r.grass, r.grassImpostor = oldGrass, oldImpostor

	newGrass := &GrassSystem{}
	r.replaceGrass(newGrass, nil)

	if r.grass != newGrass || r.grassImpostor != nil {
		t.Fatalf("the new generation was not installed immediately: grass=%p impostor=%v", r.grass, r.grassImpostor)
	}
	if got := len(r.deferredDestroys); got != 1 {
		t.Fatalf("replaceGrass freed the previous generation immediately: %d deferred destroys queued, want 1", got)
	}
	if got := r.deferredDestroys[0].framesLeft; got != maxFramesInFlight {
		t.Errorf("queued with framesLeft = %d, want %d -- one per frame slot whose fence has to be waited on", got, maxFramesInFlight)
	}
	// oldModel must still be fully owned: nothing has run yet.
	if oldModel.owned == nil {
		t.Fatalf("the previous model's ownership was cleared before its retirement ran")
	}

	// Flush past the frames in flight. The callback releases the old flora
	// model through DestroyModel, which nests its own DeferDestroy for the
	// resources themselves -- so the outer callback firing is what has to
	// clear oldModel.owned, not a device-level free (there is no device here).
	for i := 0; i < maxFramesInFlight; i++ {
		r.flushDeferredDestroys()
	}
	if oldModel.owned != nil {
		t.Errorf("the previous generation's flora model was not released via DestroyModel")
	}
	if got := len(r.deferredDestroys); got != 1 {
		t.Fatalf("DestroyModel's own nested deferral did not survive running from inside a flush (see TestDeferredDestroyQueuedFromInsideAFlushSurvives): %d queued, want 1", got)
	}
}

// TestReplaceGrassKeepsTheNewGenerationOnABakeFailure pins what happens when
// the atlas bake fails on a replacing InitGrass call (a variant count over
// grassImpostorMaxCells, say): the new GrassSystem still replaces the old one
// -- a game's flora is not held hostage by an impostor failure -- but the OLD
// atlas is retired along with it rather than kept, because it was baked from
// meshes that no longer describe what r.grass now draws. Keeping a mismatched
// atlas around would silhouette the wrong shapes, which is worse than having
// none (see InitGrass's comment on this).
func TestReplaceGrassKeepsTheNewGenerationOnABakeFailure(t *testing.T) {
	r := &Renderer{}
	oldImpostor := &grassImpostor{}
	r.grass, r.grassImpostor = &GrassSystem{}, oldImpostor

	newGrass := &GrassSystem{}
	r.replaceGrass(newGrass, nil) // imp == nil: what a failed bake returns

	if r.grass != newGrass {
		t.Fatalf("the new grass system was not installed on a bake failure")
	}
	if r.grassImpostor != nil {
		t.Errorf("r.grassImpostor = %v, want nil -- a failed bake must not leave a stale atlas live", r.grassImpostor)
	}
	if got := len(r.deferredDestroys); got != 1 {
		t.Fatalf("the previous atlas was not queued for retirement: %d deferred destroys, want 1", got)
	}
}
