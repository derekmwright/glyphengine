package renderer

import "testing"

// The deferred-destroy queue is plain bookkeeping on the Renderer struct --
// DeferDestroy appends, flushDeferredDestroys ticks and runs, flushAllDeferred
// drains -- and none of the three touch the device. So a zero-value &Renderer{}
// drives all of it, and these run in `task ci` rather than only on a machine
// with a GPU. That matters: this is the mechanism DestroyModel's safety against
// frames in flight rests on entirely.

// TestDeferDestroyWaitsForEveryFrameInFlight pins the countdown against what it
// has to mean.
//
// DrawFrame waits on frame slot f's fence and THEN calls
// flushDeferredDestroys, cycling f through maxFramesInFlight slots. So after k
// flushes, k distinct slots' fences have been waited on since the defer was
// queued, and only at k == maxFramesInFlight has every submission that could
// have been in flight at that moment completed. Running the callback earlier
// frees a resource the GPU is still reading; later only wastes memory.
//
// BROKEN: changed DeferDestroy's framesLeft from maxFramesInFlight to 1.
// FAILED with: "callback ran after 1 flush(es); it must wait for 2".
// Restored with `git checkout -- renderer/renderer.go`.
func TestDeferDestroyWaitsForEveryFrameInFlight(t *testing.T) {
	r := &Renderer{}
	ran := 0
	r.DeferDestroy(func() { ran++ })

	for i := 1; i <= maxFramesInFlight; i++ {
		r.flushDeferredDestroys()
		switch {
		case i < maxFramesInFlight && ran != 0:
			t.Fatalf("callback ran after %d flush(es); it must wait for %d", i, maxFramesInFlight)
		case i == maxFramesInFlight && ran != 1:
			t.Fatalf("callback ran %d times after %d flushes, want exactly 1", ran, maxFramesInFlight)
		}
	}

	r.flushDeferredDestroys()
	if ran != 1 {
		t.Errorf("callback ran %d times in total, want 1", ran)
	}
	if got := len(r.deferredDestroys); got != 0 {
		t.Errorf("%d entries left queued after the callback ran, want 0", got)
	}
}

// TestDeferredDestroyQueuedFromInsideAFlushSurvives is a bug this issue found
// rather than a property it added.
//
// flushDeferredDestroys used to compact into the SAME slice it was iterating
// and then truncate it to the surviving count -- so anything a callback queued
// while it ran landed past that count and was silently discarded. Discarded,
// not deferred: the resource is never freed, and DestroyMaterial has already
// removed it from r.materials, so Renderer.Destroy's shutdown sweep cannot
// find it either. It surfaces as a leaked VkBuffer at vkDestroyDevice and
// nowhere before.
//
// Nothing queued a destroy from inside a destroy before DestroyModel, which is
// why this sat harmless: DestroyModel's deferred callback calls
// DestroyMaterial, which defers the material's uniform buffer in turn.
//
// MEASURED, not assumed: with the old body restored the test below failed with
// "a destroy queued from inside a flush never ran (queue length 0)" -- the
// entry was gone, not merely delayed.
//
// BROKEN: restored the old body of flushDeferredDestroys (compact in place
// over `for i := range r.deferredDestroys`, then
// `r.deferredDestroys = r.deferredDestroys[:n]`). FAILED with: "a destroy
// queued from inside a flush never ran (queue length 0)". Restored with
// `git checkout -- renderer/renderer.go`.
func TestDeferredDestroyQueuedFromInsideAFlushSurvives(t *testing.T) {
	r := &Renderer{}
	outer, inner := 0, 0
	r.DeferDestroy(func() {
		outer++
		r.DeferDestroy(func() { inner++ })
	})

	// Enough flushes for the outer callback and then the one it queued.
	for i := 0; i < 2*maxFramesInFlight+1; i++ {
		r.flushDeferredDestroys()
	}
	if outer != 1 {
		t.Fatalf("outer callback ran %d times, want 1", outer)
	}
	if inner != 1 {
		t.Fatalf("a destroy queued from inside a flush never ran (queue length %d)", len(r.deferredDestroys))
	}

	// And the nested one must have waited its own full countdown rather than
	// being run inside the flush that queued it.
	r2 := &Renderer{}
	nested := 0
	r2.DeferDestroy(func() { r2.DeferDestroy(func() { nested++ }) })
	for i := 0; i < maxFramesInFlight; i++ {
		r2.flushDeferredDestroys()
	}
	if nested != 0 {
		t.Errorf("the nested destroy ran after %d flushes; it was queued on the last of them and owes its own %d", maxFramesInFlight, maxFramesInFlight)
	}
}

// TestFlushAllDeferredDrainsWhatItQueues covers the shutdown path's version of
// the same hazard. Renderer.Destroy calls flushAllDeferred with the device
// idle, and DestroyModel's callback queues more work while it runs; a drain
// that ran one pass would leave that behind for the validation layer to report
// at vkDestroyDevice.
//
// Destroy happens to call flushAllDeferred twice, which covered one level of
// nesting by accident. Draining until the queue is empty covers it on purpose.
//
// BROKEN: made flushAllDeferred run a single pass (`for _, dd := range
// r.deferredDestroys { dd.fn() }; r.deferredDestroys = nil`). FAILED with:
// "flushAllDeferred left 0 destroy queued and 1 of 2 nested callbacks run" --
// note the ZERO: the single pass had already set the queue to nil, so the
// nested destroy was not waiting anywhere, it was gone. That is the shape of
// the leak, and it is why this asserts on the callback count and not only on
// the queue being empty.
// Restored with `git checkout -- renderer/renderer.go`.
func TestFlushAllDeferredDrainsWhatItQueues(t *testing.T) {
	r := &Renderer{}
	depth := 0
	var chain func()
	chain = func() {
		depth++
		if depth < 2 {
			r.DeferDestroy(chain)
		}
	}
	r.DeferDestroy(chain)

	r.flushAllDeferred()
	if depth != 2 || len(r.deferredDestroys) != 0 {
		t.Fatalf("flushAllDeferred left %d destroy queued and %d of 2 nested callbacks run", len(r.deferredDestroys), depth)
	}
}
