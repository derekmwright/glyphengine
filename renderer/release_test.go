package renderer

import (
	"testing"
)

// The one runtime release contract, driven on the fake device so it runs in
// `task ci` rather than only on a machine with a GPU. That is not a
// convenience: the validation layer does NOT report a resource freed while
// only an already-submitted frame still references it -- measured, and
// recorded in docs/agents/models.md -- so counting the driver's destroy calls
// per frame is the only check that can see this at all.
//
// Every test here drives frames through streamFrame (upload_test.go), which is
// DrawFrame's order: this slot's fence wait, the flush the wait makes safe,
// then claiming, recording and handing off the upload batch.

// releasedBuffers is how many buffer destroys the driver has seen, as a
// baseline a release can be measured against. Creation destroys staging
// buffers of its own, so an absolute count means nothing here.
func releasedBuffers(d *bufferTestDriver) int { return d.destroyed["Buffer"] }

// TestDestroyMeshKeepsBuffersUntilTheDrawingFrameRetires is the issue, as a
// count per frame.
//
// A static mesh whose upload has settled is the state the public call used to
// free immediately. The frames that drew it are submitted and still in flight
// at the moment of release, so the buffers have to survive until the fence of
// the LAST of them has been waited on -- which is the flush at
// maxFramesInFlight, because each flush follows one distinct slot's fence wait
// (see TestDeferDestroyWaitsForEveryFrameInFlight).
//
// The assertion is per flush, not "eventually": a release that frees one flush
// early is exactly the bug, and a check that only looked at the end would pass
// for both.
//
// BROKEN: put the immediate free back in DestroyMesh (destroy m's index and
// vertex buffers inline instead of `r.DeferDestroy(func() { r.retireMesh(...) })`).
// FAILED with: "2 buffers destroyed at the release itself; the frames that
// drew this mesh are still in flight". Restored with
// `git checkout -- renderer/mesh.go`.
func TestDestroyMeshKeepsBuffersUntilTheDrawingFrameRetires(t *testing.T) {
	r, d := bufferFixture()
	v, idx := arenaTriangle()
	m, err := r.CreateIndexedMesh32(v, idx)
	if err != nil {
		t.Fatal(err)
	}

	// Two frames draw it and are submitted. Both are in flight when the
	// release below happens; neither fence has been waited on since.
	for f := 0; f < maxFramesInFlight; f++ {
		streamFrame(r, d, f%maxFramesInFlight)
	}

	base := releasedBuffers(d)
	r.DestroyMesh(m)
	if got := releasedBuffers(d) - base; got != 0 {
		t.Fatalf("%d buffers destroyed at the release itself; the frames that drew this mesh are still in flight", got)
	}
	if !m.destroyed {
		t.Error("the mesh does not read as destroyed from the call, so nothing stops a new draw")
	}

	for flush := 1; flush <= maxFramesInFlight; flush++ {
		streamFrame(r, d, flush%maxFramesInFlight)
		got := releasedBuffers(d) - base
		switch {
		case flush < maxFramesInFlight && got != 0:
			t.Fatalf("%d buffers destroyed after %d flush(es); only %d of %d in-flight fences have been waited on",
				got, flush, flush, maxFramesInFlight)
		case flush == maxFramesInFlight && got != 2:
			t.Fatalf("%d buffers destroyed after %d flushes, want the vertex and index buffer", got, flush)
		}
	}

	// And exactly once: a second flush must not free them again.
	streamFrame(r, d, 0)
	if got := releasedBuffers(d) - base; got != 2 {
		t.Errorf("%d buffers destroyed in total, want 2", got)
	}
	r.flushAllDeferred()
	bufferBalance(t, d)
}

// TestDestroyMeshRetiresInEveryUploadState is the ergonomics half of issue
// #153: one public call, three upload states, and the application cannot see
// which state it is in.
//
//   - queued: the copy has not been recorded, so it is dropped and its staging
//     comes back at once. The destination was never read by anything, but it
//     still retires on the same countdown as everything else -- the point is
//     that the CALLER cannot tell these apart, so they must not behave
//     differently.
//   - recorded: the copy is in a command buffer that will run, so the
//     destination has to outlive it. Its retirement must not land before the
//     staging's, which retires on the batch's own fence.
//   - settled: the frames in flight.
//
// The staging-versus-destination ordering in the recorded case is what makes
// this more than three copies of the previous test: a destination freed while
// a queued copy still names it is a write to freed memory, which is the one
// shape of this bug the layer WOULD report, on somebody else's driver.
//
// BROKEN: the same immediate free as above. FAILED on all three states with
// "queued/recorded/settled: destination freed at the release, before the copy
// that writes it has run" -- including on the settled one, which is the state
// the old code was right about, because the state this test says it is in is
// also the state the fixture has to prove it reached. Restored with
// `git checkout -- renderer/mesh.go`.
func TestDestroyMeshRetiresInEveryUploadState(t *testing.T) {
	// framesBeforeRelease says how many frames run between the asynchronous
	// create and the release: none leaves the copy queued, one leaves it
	// recorded and in flight, and enough for the ticket leaves it settled.
	for _, tc := range []struct {
		state               string
		framesBeforeRelease int
		// stagingAtRelease is how many buffers the release itself destroys: the
		// staging of a copy that was only queued. A recorded copy's staging is
		// not the release's to free, and a settled one's went back with its
		// batch before the release was even made.
		stagingAtRelease int
		// totalAtRetire is every buffer destroyed from the release onwards, by
		// the flush the destination dies on -- which is how the destination
		// landing no earlier than the staging gets asserted rather than
		// assumed.
		totalAtRetire int
		wantReady     bool
	}{
		{"queued", 0, 1, 3, false},
		{"recorded", 1, 0, 3, false},
		{"settled", maxFramesInFlight + 1, 0, 2, true},
	} {
		t.Run(tc.state, func(t *testing.T) {
			r, d := bufferFixture()
			v, idx := arenaTriangle()
			m, ticket, err := r.CreateIndexedMesh32Async(v, idx)
			if err != nil {
				t.Fatal(err)
			}
			for f := 0; f < tc.framesBeforeRelease; f++ {
				streamFrame(r, d, f%maxFramesInFlight)
			}
			if ticket.Ready() != tc.wantReady {
				t.Fatalf("%s: ticket ready %v, want %v -- the fixture is not in the state it names", tc.state, ticket.Ready(), tc.wantReady)
			}

			base := releasedBuffers(d)
			r.DestroyMesh(m)

			staging := releasedBuffers(d) - base
			if staging >= 2 {
				t.Fatalf("%s: destination freed at the release, before the copy that writes it has run", tc.state)
			}
			if staging != tc.stagingAtRelease {
				t.Fatalf("%s: %d buffers destroyed at the release, want %d", tc.state, staging, tc.stagingAtRelease)
			}
			if r.uploadPending != 0 || m.upload != nil {
				t.Fatalf("%s: the mesh still holds an upload after release", tc.state)
			}

			// The destination buffers go, and go once, on the countdown the
			// release itself queued -- never earlier than the staging.
			frame := tc.framesBeforeRelease
			destinations, total := 0, 0
			for flush := 1; flush <= maxFramesInFlight && destinations == 0; flush++ {
				streamFrame(r, d, frame%maxFramesInFlight)
				frame++
				if got := releasedBuffers(d) - base - staging; got >= 2 {
					destinations, total = flush, releasedBuffers(d)-base
				}
			}
			if destinations != maxFramesInFlight {
				t.Fatalf("%s: destination buffers freed at flush %d, want %d", tc.state, destinations, maxFramesInFlight)
			}
			if total != tc.totalAtRetire {
				t.Fatalf("%s: %d buffers destroyed by the flush the destination dies on, want %d -- the staging has to have gone first",
					tc.state, total, tc.totalAtRetire)
			}
			r.flushAllDeferred()
			bufferBalance(t, d)
			if got := r.ResourceCounts().PendingUploads; got != 0 {
				t.Errorf("%s: PendingUploads %d after everything retired", tc.state, got)
			}
		})
	}
}

// TestDestroyMeshRetiresDynamicAndArenaOnTheSameCountdown holds the two kinds
// that already deferred to the contract the other two just joined, so the
// uniformity is a check rather than a claim.
//
// A dynamic mesh also has to stop being a destination for flushDynamicMeshes
// at the call: left in the map it would take two more frames of staged copies
// into buffers nothing will ever draw, and the last of those copies would be
// into memory the retirement is about to free.
//
// BROKEN: moved `delete(r.dynamicMeshes, m)` out of DestroyMesh into the
// deferred callback. FAILED with: "a released dynamic mesh is still in the
// dynamic map, so the next flush writes into buffers that are retiring".
// Restored with `git checkout -- renderer/mesh.go`.
func TestDestroyMeshRetiresDynamicAndArenaOnTheSameCountdown(t *testing.T) {
	t.Run("dynamic", func(t *testing.T) {
		r, d := bufferFixture()
		r.dynamicMeshes = make(map[*Mesh]*dynamicMesh)
		m, err := r.CreateDynamicIndexedMesh(8, 12)
		if err != nil {
			t.Fatal(err)
		}
		v, _ := arenaTriangle()
		if err := r.UpdateMeshData(m, v, []uint16{0, 1, 2}); err != nil {
			t.Fatal(err)
		}
		r.flushDynamicMeshes(0)

		base := releasedBuffers(d)
		r.DestroyMesh(m)
		if _, ok := r.dynamicMeshes[m]; ok {
			t.Fatal("a released dynamic mesh is still in the dynamic map, so the next flush writes into buffers that are retiring")
		}
		if got := releasedBuffers(d) - base; got != 0 {
			t.Fatalf("%d buffers destroyed at the release of a dynamic mesh", got)
		}
		for flush := 1; flush <= maxFramesInFlight; flush++ {
			r.flushDeferredDestroys()
			got := releasedBuffers(d) - base
			// Two buffers per frame in flight: a vertex set and an index set.
			want := 0
			if flush == maxFramesInFlight {
				want = 2 * maxFramesInFlight
			}
			if got != want {
				t.Fatalf("after %d flush(es): %d buffers destroyed, want %d", flush, got, want)
			}
		}
		r.flushAllDeferred()
		bufferBalance(t, d)
	})

	t.Run("arena range", func(t *testing.T) {
		r, d := bufferFixture()
		a := mustArena(t, r, true)
		v, idx := arenaTriangle()
		m, err := a.Alloc(v, idx)
		if err != nil {
			t.Fatal(err)
		}

		// A range owns no Vulkan objects: what has to wait out the frames in
		// flight is the span, so Stats is what moves rather than a destroy
		// count.
		r.DestroyMesh(m)
		if !m.destroyed {
			t.Error("a released range does not read as destroyed, so mesh-range batching would still take it")
		}
		for flush := 1; flush <= maxFramesInFlight; flush++ {
			if used, _, ranges := a.Stats(); flush == 1 && (used == 0 || ranges == 0) {
				t.Fatal("the arena gave the span back at the release itself")
			}
			r.flushDeferredDestroys()
		}
		if used, _, ranges := a.Stats(); used != 0 || ranges != 0 {
			t.Fatalf("after %d flushes the span is still held: used %d, ranges %d", maxFramesInFlight, used, ranges)
		}
		r.DestroyMeshArena(a)
		r.flushAllDeferred()
		bufferBalance(t, d)
	})
}

// TestRepeatReleaseIsANoOpOnEveryKind covers the thing a streaming game does
// by accident: two systems each decide the patch is gone.
//
// A second release must queue nothing and destroy nothing, on every kind,
// whether or not the first one has retired yet -- the half-retired window is
// new, so it is the one worth checking rather than only the settled state.
// A range belonging to another renderer still panics.
//
// BROKEN: dropped `|| m.destroyed` from releaseMesh's guard. FAILED on
// static, streamed and dynamic with "the second release queued 2 destroys,
// want 1"; the arena range survived it, because MeshArena.Free carries the
// same guard of its own. Restored with `git checkout -- renderer/mesh.go`.
func TestRepeatReleaseIsANoOpOnEveryKind(t *testing.T) {
	v, idx := arenaTriangle()

	kinds := map[string]func(t *testing.T, r *Renderer) *Mesh{
		"static": func(t *testing.T, r *Renderer) *Mesh {
			m, err := r.CreateIndexedMesh32(v, idx)
			if err != nil {
				t.Fatal(err)
			}
			return m
		},
		"streamed": func(t *testing.T, r *Renderer) *Mesh {
			m, _, err := r.CreateIndexedMesh32Async(v, idx)
			if err != nil {
				t.Fatal(err)
			}
			return m
		},
		"dynamic": func(t *testing.T, r *Renderer) *Mesh {
			r.dynamicMeshes = make(map[*Mesh]*dynamicMesh)
			m, err := r.CreateDynamicIndexedMesh(8, 12)
			if err != nil {
				t.Fatal(err)
			}
			return m
		},
		"arena range": func(t *testing.T, r *Renderer) *Mesh {
			m, err := mustArena(t, r, true).Alloc(v, idx)
			if err != nil {
				t.Fatal(err)
			}
			return m
		},
	}
	for name, make := range kinds {
		t.Run(name, func(t *testing.T) {
			r, d := bufferFixture()
			m := make(t, r)

			r.DestroyMesh(m)
			queued := len(r.deferredDestroys)
			r.DestroyMesh(m)
			if got := len(r.deferredDestroys); got != queued {
				t.Fatalf("%s: the second release queued %d destroys, want %d", name, got, queued)
			}

			// Again from inside the retiring window, and again after it.
			r.flushDeferredDestroys()
			r.DestroyMesh(m)
			if got := len(r.deferredDestroys); got != queued {
				t.Fatalf("%s: a release during retirement queued %d destroys, want %d", name, got, queued)
			}
			r.flushAllDeferred()
			base := releasedBuffers(d)
			r.DestroyMesh(m)
			r.flushAllDeferred()
			if got := releasedBuffers(d) - base; got != 0 {
				t.Fatalf("%s: a release after retirement destroyed %d more buffers", name, got)
			}
			if m.owner != nil {
				r.DestroyMeshArena(m.owner)
			}
			r.flushAllDeferred()
			bufferBalance(t, d)
		})
	}

	t.Run("foreign renderer", func(t *testing.T) {
		r, _ := bufferFixture()
		other, _ := bufferFixture()
		m, err := mustArena(t, other, true).Alloc(v, idx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if recover() == nil {
				t.Error("releasing another renderer's range did not panic")
			}
		}()
		r.DestroyMesh(m)
	})
}

// TestShutdownDrainsRetirementExactlyOnce is the shutdown half, and the reason
// the Now variants exist: Renderer.Destroy's sweep runs with the device idle,
// and a mesh or texture released in the LAST frame before it is still sitting
// in the deferred queue when it starts.
//
// Both paths have to reach the same objects exactly once. A release that
// retired twice would be a double free -- invisible, because the destroyed
// flag swallows it -- and one that retired never leaks a handle the layer
// reports at vkDestroyDevice.
//
// BROKEN: dropped the deregistration loop from retireMesh, so a released
// mesh's retirement left it in r.meshes for the shutdown sweep to find again.
// FAILED with: "the sweep found 6 meshes, want the 3 never released".
//
// Worth knowing what that break did NOT do, because it is why this asserts on
// what the sweep FOUND and not only on the balance: the balance check passed.
// The second free never reached Vulkan -- releaseMesh returns early on the
// destroyed flag -- so every count stayed level while the sweep was walking
// three meshes whose buffers were already gone. That is the same blind spot
// docs/agents/models.md records for a double-freed shared texture. Restored
// with `git checkout -- renderer/mesh.go`.
func TestShutdownDrainsRetirementExactlyOnce(t *testing.T) {
	r, d := textureFixture()
	v, idx := arenaTriangle()

	var released, kept []*Mesh
	for n := 0; n < 3; n++ {
		m, err := r.CreateIndexedMesh32(v, idx)
		if err != nil {
			t.Fatal(err)
		}
		released = append(released, m)
		m, err = r.CreateIndexedMesh32(v, idx)
		if err != nil {
			t.Fatal(err)
		}
		kept = append(kept, m)
	}
	tex, err := r.CreateTexture([]byte{1, 2, 3, 4}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	spare, err := r.CreateTexture([]byte{1, 2, 3, 4}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}

	// The last frame before shutdown: released, nothing flushed.
	for _, m := range released {
		r.DestroyMesh(m)
	}
	r.DestroyTexture(tex)
	if got := r.ResourceCounts().Deferred; got != len(released)+1 {
		t.Fatalf("%d retirements queued, want %d", got, len(released)+1)
	}

	// Renderer.Destroy's order, which is the thing under test: the queue
	// first, now that the device is idle, then the immediate sweep over
	// whatever the application never released.
	r.flushAllDeferred()
	textures, meshes := r.textures, r.meshes
	r.textures, r.meshes = nil, nil
	r.gltfTextureCache, r.gltfTextureShares = nil, nil
	for _, tx := range textures {
		r.destroyTextureNow(tx)
	}
	for _, m := range meshes {
		r.destroyMeshNow(m)
	}
	r.flushAllDeferred()

	if len(meshes) != len(kept) {
		t.Errorf("the sweep found %d meshes, want the %d never released", len(meshes), len(kept))
	}
	if len(textures) != 1 || textures[0] != spare {
		t.Errorf("the sweep found %v, want only the texture that was never released", textures)
	}
	textureBalance(t, d)

	// The meta-check: the balance above has to be able to fail, or it proves
	// nothing. One extra destroy of each kind is the double free this test is
	// really about.
	for _, kind := range []string{"Buffer", "Image", "Sampler", "ImageView", "DescriptorSet"} {
		d.destroyed[kind]++
		captured := &capturingT{TB: t}
		textureBalance(captured, d)
		d.destroyed[kind]--
		if !captured.failed {
			t.Fatalf("the balance check cannot see a second free of %s", kind)
		}
	}
}

// TestDestroyModelRetiresItsMeshesInOneCountdown is the nested-delay pin.
//
// DestroyModel defers its whole release, and the release then has to destroy
// IMMEDIATELY. Calling the public DestroyMesh/DestroyTexture/DestroyMaterial
// from in there would queue a second countdown inside the first, so a
// released level's buffers would live 2*maxFramesInFlight frames instead of
// maxFramesInFlight -- twice the geometry resident in a game that reloads on a
// tick, bought for no safety at all, because the outer callback already ran
// only after every frame that could have been in flight at the call retired.
//
// The frame the buffers actually die on is what this pins, and it is the same
// frame as before the release contract changed.
//
// BROKEN: put `r.DestroyMesh(mesh)` and `r.DestroyMaterial(mat)` back in
// DestroyModel's release loop in place of the Now variants. FAILED with: "a
// released model's buffers were destroyed at flush 4, want 2 -- a deferral
// inside a deferral". Restored with
// `git checkout -- renderer/modeldestroy.go`.
func TestDestroyModelRetiresItsMeshesInOneCountdown(t *testing.T) {
	r, d := bufferFixture()
	v, idx := arenaTriangle()
	mesh, err := r.CreateIndexedMesh32(v, idx)
	if err != nil {
		t.Fatal(err)
	}
	// A hand-built material rather than CreateMaterial: what is being measured
	// is when the uniform buffer dies, and the pipeline layouts a real one
	// needs are not what this fixture is for.
	uniform, memory, err := r.createBuffer(256, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	mat := &Material{uniform: uniform, uniformMemory: memory}
	r.materials = append(r.materials, mat)

	model := &Model{owned: &modelResources{meshes: []*Mesh{mesh}, materials: []*Material{mat}}}
	base := releasedBuffers(d)
	r.DestroyModel(model)

	destroyed := 0
	for flush := 1; flush <= 2*maxFramesInFlight; flush++ {
		r.flushDeferredDestroys()
		if destroyed == 0 && releasedBuffers(d)-base > 0 {
			destroyed = flush
		}
	}
	if destroyed != maxFramesInFlight {
		t.Fatalf("a released model's buffers were destroyed at flush %d, want %d -- a deferral inside a deferral",
			destroyed, maxFramesInFlight)
	}
	// All three of them, in that one countdown: the mesh's pair and the
	// material's uniform.
	if got := releasedBuffers(d) - base; got != 3 {
		t.Errorf("%d buffers destroyed, want the mesh's vertex and index buffers and the material's uniform", got)
	}
	bufferBalance(t, d)
}

// TestDestroyMaterialAndInstanceSetRetireInOneCountdown is the same pin for
// the two releases a game makes directly. Both deferred their own work before
// this change and still do; what must not have appeared underneath them is a
// second countdown.
//
// BROKEN: wrapped DestroyMaterial's retirement in a second DeferDestroy.
// FAILED with "material: freed at flush 4, want 2" and, in
// TestResourceCountsHoldAReleasedObjectUntilItRetires, "after 2 flushes:
// {... Materials:1 ... Deferred:1}, want every released kind at zero".
// The same wrap around DestroyInstanceSet's gave "instance set: freed at flush
// 4, want 2" plus the existing
// TestDestroyInstanceSetDeregistersOnlyWhenTheDeferredFreeRuns. Restored with
// `git checkout -- renderer/material.go renderer/instancedmesh.go`.
func TestDestroyMaterialAndInstanceSetRetireInOneCountdown(t *testing.T) {
	t.Run("material", func(t *testing.T) {
		r, d := bufferFixture()
		uniform, memory, err := r.createBuffer(256, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		mat := &Material{uniform: uniform, uniformMemory: memory}
		r.materials = append(r.materials, mat)

		base := releasedBuffers(d)
		r.DestroyMaterial(mat)
		if got := r.ResourceCounts().Materials; got != 1 {
			t.Errorf("Materials %d right after the release; it is still alive for %d frames", got, maxFramesInFlight)
		}
		at := flushUntilBuffers(r, d, base)
		if at != maxFramesInFlight {
			t.Fatalf("material: freed at flush %d, want %d", at, maxFramesInFlight)
		}
		if got := r.ResourceCounts().Materials; got != 0 {
			t.Errorf("Materials %d after retirement, want 0", got)
		}
		bufferBalance(t, d)
	})

	t.Run("instance set", func(t *testing.T) {
		r, d := bufferFixture()
		v, idx := arenaTriangle()
		mesh, err := r.CreateIndexedMesh32(v, idx)
		if err != nil {
			t.Fatal(err)
		}
		set, err := r.CreateInstanceSet(mesh, 4, []MeshInstance{{Model: [16]float32{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}, Tint: [4]float32{1, 1, 1, 1}}})
		if err != nil {
			t.Fatal(err)
		}
		base := releasedBuffers(d)
		r.DestroyInstanceSet(set)
		if got := r.ResourceCounts().InstanceSets; got != 1 {
			t.Errorf("InstanceSets %d right after the release, want 1", got)
		}
		at := flushUntilBuffers(r, d, base)
		if at != maxFramesInFlight {
			t.Fatalf("instance set: freed at flush %d, want %d", at, maxFramesInFlight)
		}
		r.DestroyMesh(mesh)
		r.flushAllDeferred()
		bufferBalance(t, d)
	})
}

// flushUntilBuffers reports the flush at which the driver first destroyed a
// buffer past base, or 0 within twice the countdown.
func flushUntilBuffers(r *Renderer, d *bufferTestDriver, base int) int {
	for flush := 1; flush <= 2*maxFramesInFlight; flush++ {
		r.flushDeferredDestroys()
		if releasedBuffers(d) > base {
			return flush
		}
	}
	return 0
}

// TestSharedTextureEvictsOnceAndRetiresOnce is the cache half.
//
// Two paths reach a shared texture's death and they must agree. The last share
// going through releaseGLTFTexture destroys once and evicts once. A direct
// DestroyTexture on a texture other models still hold is the caller's error,
// but it must still evict the cache entry THERE AND THEN -- a LoadGLTF between
// the call and the retirement would otherwise be handed a texture on its way
// out -- while destroying the image exactly once, on the countdown.
//
// BROKEN: moved r.forgetGLTFTexture(t) out of releaseTexture into
// retireTexture. FAILED with "cached/shares = 1/0 after the last share went,
// want 0/0" and "the cache still names a released texture, so the next load is
// handed it".
//
// BROKEN the other way: destroyed the texture inline instead of deferring
// (`r.retireTexture(t)` in place of the DeferDestroy). FAILED with "the last
// share destroyed the image at the call, with frames still in flight" and "a
// direct release destroyed the image at the call". Restored with
// `git checkout -- renderer/texture.go`.
func TestSharedTextureEvictsOnceAndRetiresOnce(t *testing.T) {
	key := func(path string) gltfTextureKey { return gltfTextureKey{path: path, srgb: true} }

	t.Run("last share", func(t *testing.T) {
		r, d := textureFixture()
		tex, err := r.CreateTexture([]byte{1, 2, 3, 4}, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		r.shareGLTFTexture(key("trim.png"), tex)
		if r.acquireGLTFTexture(key("trim.png")) != tex {
			t.Fatal("the cache does not hand back what it was given")
		}

		r.releaseGLTFTexture(tex) // two shares, one back
		if counts := r.ResourceCounts(); counts.TextureShares != 1 || counts.CachedTextures != 1 {
			t.Fatalf("shares/cached = %d/%d after giving one of two back, want 1/1", counts.TextureShares, counts.CachedTextures)
		}
		if d.destroyed["Image"] != 0 {
			t.Fatal("a share going back destroyed a texture another holder is still drawing with")
		}

		r.releaseGLTFTexture(tex) // the last one
		if counts := r.ResourceCounts(); counts.CachedTextures != 0 || counts.TextureShares != 0 {
			t.Fatalf("cached/shares = %d/%d after the last share went, want 0/0", counts.CachedTextures, counts.TextureShares)
		}
		if got := r.ResourceCounts().Textures; got != 1 {
			t.Errorf("Textures %d before the retirement ran; the image is genuinely still alive", got)
		}
		if d.destroyed["Image"] != 0 {
			t.Fatal("the last share destroyed the image at the call, with frames still in flight")
		}
		r.flushAllDeferred()
		if d.destroyed["Image"] != 1 {
			t.Fatalf("%d images destroyed after retirement, want exactly 1", d.destroyed["Image"])
		}
		if got := r.ResourceCounts().Textures; got != 0 {
			t.Errorf("Textures %d after retirement, want 0", got)
		}
		textureBalance(t, d)
	})

	t.Run("direct release of a shared texture", func(t *testing.T) {
		r, d := textureFixture()
		tex, err := r.CreateTexture([]byte{1, 2, 3, 4}, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		r.shareGLTFTexture(key("trim.png"), tex)
		r.acquireGLTFTexture(key("trim.png")) // a second model holds it

		r.DestroyTexture(tex)
		if r.acquireGLTFTexture(key("trim.png")) != nil {
			t.Fatal("the cache still names a released texture, so the next load is handed it")
		}
		if d.destroyed["Image"] != 0 {
			t.Fatal("a direct release destroyed the image at the call")
		}
		r.flushAllDeferred()
		if d.destroyed["Image"] != 1 {
			t.Fatalf("%d images destroyed, want exactly 1 even though two shares were outstanding", d.destroyed["Image"])
		}
		textureBalance(t, d)
	})
}

// TestResourceCountsHoldAReleasedObjectUntilItRetires states the convention
// once, across every kind, because a count that means "live" for one resource
// and "released" for another is worse than either.
//
// Counted until retired, which is what MeshRanges, InstanceSets and
// PendingUploads already meant and what Meshes, Textures and Materials now
// mean too. Reporting a release as gone the moment it is requested would be a
// lie with a use-after-free hiding behind it: for maxFramesInFlight more
// frames those objects are exactly as alive as they were.
//
// BROKEN: freed a mesh inline in DestroyMesh again. FAILED with "Meshes 0
// immediately after the release, want 1 until it retires" and "Deferred 3,
// want one retirement per released object". The same inline free in
// DestroyTexture added "Textures 0 immediately after the release" and
// "DescriptorSets 0 right after the release, want 1 -- the set goes back with
// the sampler it names".
func TestResourceCountsHoldAReleasedObjectUntilItRetires(t *testing.T) {
	r, d := textureFixture()
	v, idx := arenaTriangle()
	mesh, err := r.CreateIndexedMesh32(v, idx)
	if err != nil {
		t.Fatal(err)
	}
	tex, err := r.CreateTexture([]byte{1, 2, 3, 4}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	uniform, memory, err := r.createBuffer(256, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	mat := &Material{uniform: uniform, uniformMemory: memory}
	r.materials = append(r.materials, mat)
	a := mustArena(t, r, true)
	rng, err := a.Alloc(v, idx)
	if err != nil {
		t.Fatal(err)
	}

	before := r.ResourceCounts()
	if before.Meshes != 1 || before.Textures != 1 || before.Materials != 1 || before.MeshRanges != 1 {
		t.Fatalf("fixture is wrong: %+v", before)
	}

	r.DestroyMesh(mesh)
	r.DestroyTexture(tex)
	r.DestroyMaterial(mat)
	r.DestroyMesh(rng)

	after := r.ResourceCounts()
	if after.Meshes != 1 {
		t.Errorf("Meshes %d immediately after the release, want 1 until it retires", after.Meshes)
	}
	if after.Textures != 1 {
		t.Errorf("Textures %d immediately after the release, want 1 until it retires", after.Textures)
	}
	if after.Materials != 1 {
		t.Errorf("Materials %d immediately after the release, want 1 until it retires", after.Materials)
	}
	if after.MeshRanges != 1 {
		t.Errorf("MeshRanges %d immediately after the release, want 1 until it retires", after.MeshRanges)
	}
	if after.Deferred != 4 {
		t.Errorf("Deferred %d, want one retirement per released object", after.Deferred)
	}
	// Descriptor sets are the exception, and deliberately so: the set is freed
	// inside the retirement with the sampler it names, so the count moves with
	// the object rather than with the call.
	if after.DescriptorSets != before.DescriptorSets {
		t.Errorf("DescriptorSets %d right after the release, want %d -- the set goes back with the sampler it names",
			after.DescriptorSets, before.DescriptorSets)
	}

	for flush := 1; flush <= maxFramesInFlight; flush++ {
		r.flushDeferredDestroys()
	}
	final := r.ResourceCounts()
	if final.Meshes != 0 || final.Textures != 0 || final.Materials != 0 || final.MeshRanges != 0 || final.Deferred != 0 {
		t.Errorf("after %d flushes: %+v, want every released kind at zero", maxFramesInFlight, final)
	}
	if final.DescriptorSets != before.DescriptorSets-1 {
		t.Errorf("DescriptorSets %d after retirement, want %d", final.DescriptorSets, before.DescriptorSets-1)
	}
	r.DestroyMeshArena(a)
	r.flushAllDeferred()
	textureBalance(t, d)
}

// patchesPerFrame is the release rate the allocation checks below are sized
// for: issue #153 comes from a terrain streamer, and "hundreds of patches a
// frame" is the shape worth measuring rather than one release in isolation.
const patchesPerFrame = 200

// releaseLoop releases the same mesh patchesPerFrame times and then runs the
// two flushes that retire the batch, which is one frame of a streamer's
// steady state.
//
// The SAME mesh, with its destroyed flag reset: what is being counted is the
// release path, and a fresh mesh per release would fold two buffer creations
// and an r.meshes append into every measurement. Resetting the flag is not
// something any caller may do -- it is how this measures a release without
// measuring a creation.
func releaseLoop(r *Renderer, m *Mesh) func() {
	return func() {
		for i := 0; i < patchesPerFrame; i++ {
			m.destroyed = false
			r.DestroyMesh(m)
		}
		for f := 0; f < maxFramesInFlight; f++ {
			r.flushDeferredDestroys()
		}
	}
}

// TestReleasingAMeshCostsOneAllocationPerRelease is the allocation half of the
// contract, measured rather than assumed.
//
// A settled static mesh used to be freed inline and allocated nothing; it now
// queues a closure, and a streamer releasing hundreds of patches a frame pays
// one per patch. That one is the budget -- the deferred queue has always cost
// a closure per entry, which is what every other release here already paid --
// and anything above it would be this change quietly adding a per-patch cost
// to a per-patch operation.
//
// The deferred queue's backing array is reused across frames by
// flushDeferredDestroys (it compacts into the slice it detached), so a warm
// frame's only allocation is the closures. Measured at 200 releases a frame:
// 200 allocations, exactly 1 per release, 32 B each -- 6.4 KB a frame, which
// BenchmarkReleaseStaticMesh reports beside it. A typed retirement list would
// save that and cost a second countdown mechanism beside DeferDestroy, with
// the ordering between the two to get right: a staging buffer's retirement and
// its destination's land in ONE queue today, in that order, and that is what
// keeps a recorded copy's destination alive (see
// TestDestroyMeshRetiresInEveryUploadState). Not worth it for 6.4 KB a frame,
// and the number is here so the next person can re-measure rather than trust
// it.
//
// BROKEN: added a second DeferDestroy to DestroyMesh. FAILED with: "400
// allocations for 200 releases, want at most 200 -- one closure per release is
// the budget". A closure that captures NOTHING does not allocate at all, so
// the first attempt at this break -- `r.DeferDestroy(func() {})` -- left the
// count at 200 and the test green; the extra closure has to capture something
// to cost anything. Restored with `git checkout -- renderer/mesh.go`.
func TestReleasingAMeshCostsOneAllocationPerRelease(t *testing.T) {
	r, _ := bufferFixture()
	v, idx := arenaTriangle()
	m, err := r.CreateIndexedMesh32(v, idx)
	if err != nil {
		t.Fatal(err)
	}
	loop := releaseLoop(r, m)

	// Warm the queue's backing array and the driver's counter maps, so what is
	// measured is a steady-state frame and not the first one.
	loop()
	loop()

	got := int(testing.AllocsPerRun(5, loop))
	if got > patchesPerFrame {
		t.Errorf("%d allocations for %d releases, want at most %d -- one closure per release is the budget",
			got, patchesPerFrame, patchesPerFrame)
	}
	t.Logf("%d releases a frame allocate %d times, %.2f per release", patchesPerFrame, got, float64(got)/patchesPerFrame)
}

// BenchmarkReleaseStaticMesh is one streaming frame's worth of releases and the
// retirement behind them, for the cost in time rather than in allocations. The
// number it produced is recorded in docs/agents/models.md.
func BenchmarkReleaseStaticMesh(b *testing.B) {
	r, _ := bufferFixture()
	v, idx := arenaTriangle()
	m, err := r.CreateIndexedMesh32(v, idx)
	if err != nil {
		b.Fatal(err)
	}
	loop := releaseLoop(r, m)
	loop()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		loop()
	}
}
