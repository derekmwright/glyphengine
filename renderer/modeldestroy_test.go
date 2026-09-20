package renderer

import (
	"testing"

	"github.com/qmuntal/gltf"
)

// TestModelResourcesRecordsEachResourceOnce is the dedupe claim on its own,
// without a device: a Model SHARES textures and cached materials across its
// ModelMesh entries, so the set DestroyModel frees has to come from what the
// upload CREATED, not from what the entries point at.
//
// The fixture below is the shape that breaks a naive walk: three primitives,
// two of them on one material, one image bound by that material and a second
// image the document carries that no material references at all -- uploaded
// anyway (loadGLTFImages has always walked doc.Images), on no ModelMesh, and
// leaked by anything that rediscovers ownership from the slice.
//
// BROKEN: made newModelResources record each texture TWICE (`append(...,  t,
// t)`), which is what a walk of Model.Meshes does to a shared one. FAILED
// with "textures = [0x319aa90bc1b0 0x319aa90bc1b0 0x319aa90bc240
// 0x319aa90bc240], want exactly the two uploaded ones ... in doc.Images
// order" and "recorded 4 textures for a model whose 3 primitives share 1,
// want 2". Restored with `git checkout -- renderer/modeldestroy.go`.
//
// Worth knowing what that break does NOT do, because it is the reason this
// test exists at all: the same break, run through
// `examples/22-level -reload 20` under the validation layer, produced ZERO
// messages and passed every count assertion the loop makes. DestroyTexture's
// own `if t == nil || t.destroyed` guard turns the second free into a no-op
// before it ever reaches Vulkan, so the layer has nothing to see. A double
// free of a model's shared texture is caught here or nowhere.
func TestModelResourcesRecordsEachResourceOnce(t *testing.T) {
	doc := &gltf.Document{
		Images:    []*gltf.Image{{Name: "atlas"}, {Name: "unreferenced"}},
		Materials: []*gltf.Material{{Name: "Walls"}, {Name: "Plain"}, {Name: "Glass"}},
	}
	atlas, spare := &Texture{}, &Texture{}
	walls, glass := &Material{}, &Material{}

	// What an upload's own maps look like: one Texture per IMAGE (not per
	// primitive), and a material cache keyed by material index with nil for
	// the material that needed no Material object.
	textures := map[int]*Texture{0: atlas, 1: spare}
	cache := map[int]*Material{0: walls, 1: nil, 2: glass}

	res := newModelResources(doc, textures)
	res.addMaterials(doc, cache)

	if len(res.textures) != 2 || res.textures[0] != atlas || res.textures[1] != spare {
		t.Errorf("textures = %v, want exactly the two uploaded ones (%v, %v) in doc.Images order", res.textures, atlas, spare)
	}
	if len(res.materials) != 2 || res.materials[0] != walls || res.materials[1] != glass {
		t.Errorf("materials = %v, want exactly the two created ones (%v, %v) -- the nil cache entry is a material that needed none", res.materials, walls, glass)
	}

	// The sharing that makes a per-ModelMesh walk a double free, stated as
	// the thing this avoids: three primitives, two pointing at one material
	// and all three at one texture, and still one entry each above.
	shared := []ModelMesh{
		{Name: "Walls", Texture: atlas, Material: walls},
		{Name: "Walls", Texture: atlas, Material: walls},
		{Name: "Glass", Texture: atlas, Material: glass},
	}
	frees := 0
	for i := range shared {
		if shared[i].Texture == atlas {
			frees++
		}
	}
	if frees != 3 {
		t.Fatalf("fixture is wrong: %d primitives share the atlas, want 3", frees)
	}
	if len(res.textures) != 1+1 {
		t.Errorf("recorded %d textures for a model whose 3 primitives share 1, want 2 (the shared one and the unreferenced one)", len(res.textures))
	}
}

// TestDestroyModelOnAReadModelIsANoOp covers the case a level validator or a
// dedicated server hits by accident: a Model from ReadGLTF owns nothing, so
// handing it to DestroyModel must do nothing rather than panic on the nil
// handles it is full of.
//
// It needs no device precisely because nothing should be freed -- which is the
// assertion.
//
// BROKEN: removed the `|| m.owned == nil` guard from DestroyModel. FAILED
// with: "DestroyModel on a read model queued 1 deferred destroy, want 0".
func TestDestroyModelOnAReadModelIsANoOp(t *testing.T) {
	r := &Renderer{}
	model, err := ReadGLTF(dirFSOnly(t, "testdata/blender"), "level.glb")
	if err != nil {
		t.Fatalf("ReadGLTF: %v", err)
	}

	r.DestroyModel(model)
	if got := len(r.deferredDestroys); got != 0 {
		t.Errorf("DestroyModel on a read model queued %d deferred destroy, want 0", got)
	}
	if len(model.Nodes) == 0 || len(model.Meshes) == 0 {
		t.Error("DestroyModel emptied a read model's CPU data")
	}

	// And the two nil cases around it.
	r.DestroyModel(nil)
	r.DestroySkinnedModel(nil)
	if got := len(r.deferredDestroys); got != 0 {
		t.Errorf("DestroyModel/DestroySkinnedModel on nil queued %d deferred destroy, want 0", got)
	}
}

// TestDestroyModelIsIdempotentAndNilsHandles pins the two things a caller can
// observe without a device: the second call does nothing, and the ModelMesh
// handles are nil afterwards so a stale draw fails in Go rather than in the
// driver.
//
// owned is deliberately EMPTY here -- the resources a real model owns would
// need a device to free, and this is about the bookkeeping around them. The
// live-resource half is proved in examples/22-level's -reload loop, which
// counts them for real under the validation layer.
//
// BROKEN: removed `m.owned = nil` from DestroyModel. FAILED with: "the second
// DestroyModel queued another destroy: 2 deferred, want 1".
func TestDestroyModelIsIdempotentAndNilsHandles(t *testing.T) {
	r := &Renderer{}
	m := &Model{
		Meshes: []ModelMesh{
			{Name: "Walls", Mesh: &Mesh{}, Texture: &Texture{}, Material: &Material{}},
			{Name: "Glass", Mesh: &Mesh{}, Texture: &Texture{}},
		},
		Nodes: []ModelNode{{Name: "Building"}},
		owned: &modelResources{},
	}

	r.DestroyModel(m)
	if got := len(r.deferredDestroys); got != 1 {
		t.Fatalf("DestroyModel queued %d deferred destroys, want 1", got)
	}
	for i := range m.Meshes {
		if m.Meshes[i].Mesh != nil || m.Meshes[i].Texture != nil || m.Meshes[i].Material != nil {
			t.Errorf("primitive %d still holds a GPU handle after DestroyModel", i)
		}
	}
	if len(m.Nodes) != 1 {
		t.Error("DestroyModel dropped Nodes; they are CPU data the model never owned on the GPU")
	}

	r.DestroyModel(m)
	if got := len(r.deferredDestroys); got != 1 {
		t.Errorf("the second DestroyModel queued another destroy: %d deferred, want 1", got)
	}

	// The queued callback is what actually frees; nothing may have run yet,
	// because the frames that were in flight when DestroyModel was called are
	// still reading those resources.
	for i := 0; i < maxFramesInFlight; i++ {
		r.flushDeferredDestroys()
	}
	if got := len(r.deferredDestroys); got != 0 {
		t.Errorf("%d destroys still queued after %d flushes, want 0", got, maxFramesInFlight)
	}
}

// TestDestroyModelDefersRatherThanFreeingNow is the frames-in-flight property
// stated as a test rather than as a comment.
//
// DestroyMesh and DestroyTexture free a static resource IMMEDIATELY -- read
// them -- which is correct at shutdown, where the device is already idle, and
// a use-after-free for a model a submitted frame is still drawing. So what
// DestroyModel must NOT do is call them straight away.
//
// BROKEN: made DestroyModel run its release closure inline instead of passing
// it to DeferDestroy. FAILED with: "DestroyModel freed immediately: 0 deferred
// destroys queued, want 1".
func TestDestroyModelDefersRatherThanFreeingNow(t *testing.T) {
	r := &Renderer{}
	m := &Model{owned: &modelResources{}}
	r.DestroyModel(m)

	if got := len(r.deferredDestroys); got != 1 {
		t.Fatalf("DestroyModel freed immediately: %d deferred destroys queued, want 1", got)
	}
	if got := r.deferredDestroys[0].framesLeft; got != maxFramesInFlight {
		t.Errorf("queued with framesLeft = %d, want %d -- one per frame slot whose fence has to be waited on", got, maxFramesInFlight)
	}
}

// TestResourceCountsReportsTheTrackingLists keeps ResourceCounts honest about
// what it is: the renderer's own cleanup lists, the count of descriptor sets
// those resources hold, and the pending-destroy queue -- which is what the
// reload loop compares against a baseline.
//
// The descriptor set count is deliberately NOT one per texture plus one per
// material here. It is its own number because it is kept by hand (Vulkan
// reports nothing about a pool's remaining capacity) and because the list
// lengths are exactly what stayed level while the pool drained, before issue
// #82: a fixture where it could be derived from the others would not notice a
// ResourceCounts that derived it.
//
// BROKEN: made ResourceCounts report len(r.meshes) for Textures too. FAILED
// with: "counts = {2 2 1 1}, want {2 3 1 1}".
//
// BROKEN: made ResourceCounts report len(r.textures) for DescriptorSets --
// the derivation this fixture exists to rule out. FAILED with: "counts = {2 3
// 1 3 1}, want {2 3 1 7 1}".
func TestResourceCountsReportsTheTrackingLists(t *testing.T) {
	r := &Renderer{
		meshes:             []*Mesh{{}, {}},
		textures:           []*Texture{{}, {}, {}},
		materials:          []*Material{{}},
		instanceSets:       []*InstanceSet{{}, {}, {}, {}},
		liveDescriptorSets: 7,
	}
	r.DeferDestroy(func() {})

	want := ResourceCounts{Meshes: 2, Textures: 3, Materials: 1, DescriptorSets: 7, InstanceSets: 4, Deferred: 1}
	if got := r.ResourceCounts(); got != want {
		t.Errorf("counts = %v, want %v", got, want)
	}
}
