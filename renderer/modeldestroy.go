package renderer

import "github.com/qmuntal/gltf"

// modelResources is the set of GPU resources ONE LoadGLTF or LoadGLTFSkinned
// call created, recorded as it created them.
//
// Recorded at upload rather than rediscovered by walking Model.Meshes, because
// walking gets it wrong in both directions. A Model SHARES textures and cached
// materials across its ModelMesh entries -- a level with one atlas and twenty
// primitives points twenty ModelMesh.Texture fields at one Texture -- so a
// caller that destroyed what each entry names would free the shared ones
// nineteen times over. And an image the document carries but no primitive's
// material references is uploaded all the same (loadGLTFImages has always
// walked doc.Images, not the materials), so it appears on no ModelMesh at all
// and a walk would leak it.
//
// The slices are built in document order -- doc.Images, doc.Materials, then
// primitive order -- so the destruction order is the same in every process,
// which is what makes a validation-layer run reproducible.
type modelResources struct {
	meshes    []*Mesh
	textures  []*Texture
	materials []*Material
}

// newModelResources records the textures an upload created, in doc.Images
// order. Meshes are appended by the upload loop as it goes and materials are
// taken from its cache at the end.
func newModelResources(doc *gltf.Document, textures map[int]*Texture) *modelResources {
	res := &modelResources{}
	for i := range doc.Images {
		if t, ok := textures[i]; ok && t != nil {
			res.textures = append(res.textures, t)
		}
	}
	return res
}

// addMaterials records the Materials an upload's cache created, in
// doc.Materials order.
//
// The cache is keyed by glTF material index and stores nil for a material that
// needed no Material object, so this both skips those and cannot record one
// twice: two primitives sharing a material share the one cache entry, which is
// the sharing that makes a naive per-ModelMesh free a double free.
func (res *modelResources) addMaterials(doc *gltf.Document, cache map[int]*Material) {
	for i := range doc.Materials {
		if m, ok := cache[i]; ok && m != nil {
			res.materials = append(res.materials, m)
		}
	}
}

// ResourceCounts is how many GPU resources the renderer is currently tracking
// for cleanup, plus how many destructions are still waiting out the frames in
// flight.
//
// This is not RenderStats: those counters are reset at the start of every
// frame and describe what one frame submitted. These describe what exists.
//
// It is exported because the check that DestroyModel actually released
// anything cannot otherwise be written from outside the package -- and a
// teardown test that cannot see the count is exactly the false green CLAUDE.md
// records ("a teardown test reported zero leaks because teardown never ran").
// examples/22-level's -reload loop asserts on it.
type ResourceCounts struct {
	// Meshes, Textures and Materials are the lengths of the renderer's own
	// tracking lists -- what Renderer.Destroy would still have to sweep if
	// the process shut down now.
	Meshes    int
	Textures  int
	Materials int

	// MeshArenas counts shared allocations; MeshRanges includes retiring ranges.
	MeshArenas int
	MeshRanges int

	// DescriptorSets is how many sets those resources hold from the
	// renderer's one descriptor pool: one per Texture, one per Material, one
	// per TerrainMaterial, maxFramesInFlight per JointBuffer, plus one for the
	// grass impostor atlas while InitGrass has built one (issue #87 -- see
	// replaceGrass, which is what returns it on a replacing call).
	//
	// It is not derivable from the numbers above, which is the whole reason it
	// is here. A set is the one thing a released model used to keep -- the
	// pool was created without VK_DESCRIPTOR_POOL_CREATE_FREE_DESCRIPTOR_SET_BIT,
	// so nothing could give one back, and the mesh, texture and material
	// counts returned to their baseline on every reload while the pool
	// drained anyway. It ran out on the 677th textured load, as an allocation
	// failure a long way from the cause (issue #82).
	//
	// The renderer's own pass sets -- shadow, HDR, bloom, clouds, the
	// scene-colour copy, the UI glow layer -- are not counted: they belong to
	// the renderer's lifetime and its resizes, not to anything an application
	// (or InitGrass, on its behalf) created. docs/agents/validation.md has the
	// full list of what the pool holds.
	DescriptorSets int

	// InstanceSets is the length of r.instanceSets: every InstanceSet handed
	// out by CreateInstanceSet that DestroyInstanceSet has not yet actually
	// freed. Like the three list lengths above and unlike DescriptorSets, it
	// IS derivable in principle -- but it is its own count for the same
	// reason those are counted at all: a number a caller can compare against
	// a baseline is what turns "did DestroyInstanceSet do anything" into an
	// assertion instead of a hunch. Kept in step with the list rather than
	// recomputed, because the list is the thing DestroyInstanceSet's
	// deferred callback actually edits.
	InstanceSets int
	// LODSets and ImpostorAtlases include resources awaiting deferred release.
	LODSets         int
	ImpostorAtlases int

	// Deferred is how many destruction callbacks are queued behind the frames
	// in flight. DestroyModel routes everything through that queue, so a
	// count taken immediately after it still includes the model: the
	// resources are genuinely still alive, and reporting them as gone would
	// be a lie with a use-after-free hiding behind it. DestroyInstanceSet
	// does the same for the same reason.
	Deferred int
}

// ResourceCounts returns the renderer's live GPU resource counts.
func (r *Renderer) ResourceCounts() ResourceCounts {
	ranges := 0
	for _, a := range r.meshArenas {
		ranges += len(a.live)
	}
	return ResourceCounts{
		Meshes:          len(r.meshes),
		MeshArenas:      len(r.meshArenas),
		MeshRanges:      ranges,
		Textures:        len(r.textures),
		Materials:       len(r.materials),
		DescriptorSets:  r.liveDescriptorSets,
		InstanceSets:    len(r.instanceSets),
		LODSets:         len(r.lodSets),
		ImpostorAtlases: len(r.impostorAtlases),
		Deferred:        len(r.deferredDestroys),
	}
}

// DestroyModel releases every GPU resource a LoadGLTF call created for m,
// exactly once each, after the frames currently in flight have finished with
// them.
//
// Three things it has to get right, each of which is a real failure it would
// otherwise have:
//
//   - **Sharing.** A Model's ModelMesh entries point at SHARED textures and
//     cached materials, so freeing what each entry names frees the shared ones
//     once per entry. The upload records what it created instead (see
//     modelResources), which also catches an image the document carries that
//     no material references -- uploaded, on no ModelMesh, and invisible to
//     any walk of the slice.
//
//   - **Frames in flight.** DestroyMesh and DestroyTexture free a static
//     resource IMMEDIATELY; only a dynamic mesh goes through DeferDestroy.
//     That is fine at shutdown, where Renderer.Destroy has already waited for
//     the device to go idle, and it is a use-after-free for a model a frame
//     still in flight is drawing -- which is the whole point of being able to
//     release one at runtime. So the entire release goes through DeferDestroy
//     rather than DestroyMesh's and DestroyTexture's immediacy being changed
//     under every other caller of them.
//
//   - **The renderer's own bookkeeping.** r.meshes, r.textures and r.materials
//     exist so Destroy can clean up after an application that did not, and the
//     Destroy* methods deregister from them as they go (read DestroyMesh's own
//     comment: without that, an explicit destroy is followed by a second free
//     at shutdown and the layer reports invalid handles). Routing through
//     those same methods is what keeps that right, rather than freeing the
//     Vulkan objects here and leaving three dangling pointers behind.
//
// After it returns, every ModelMesh's Mesh, Texture and Material is nil, so a
// stale draw fails on a nil pointer in Go instead of inside the driver.
// Nodes, Lights, Verts and Idx are untouched: they are CPU data the model
// never owned on the GPU, and a game that keeps using a destroyed level's
// collider geometry is doing something reasonable.
//
// Idempotent. A second call, or a call on a Model from ReadGLTF -- which owns
// no GPU resources at all -- does nothing rather than panicking.
//
// What a game must do FIRST: stop drawing it. Remove the MeshRef (and
// MaterialRef) components of every entity spawned from the model, or despawn
// those entities. See docs/agents/models.md for what happens if it does not.
func (r *Renderer) DestroyModel(m *Model) {
	if m == nil || m.owned == nil {
		return
	}
	res := m.owned
	m.owned = nil

	for i := range m.Meshes {
		m.Meshes[i].Mesh = nil
		m.Meshes[i].Texture = nil
		m.Meshes[i].Material = nil
	}

	// Materials before textures before meshes, the same order Renderer.Destroy
	// unwinds in and for the same reason: a material's descriptor set names
	// image views and samplers the textures own, and it has to stop doing so
	// first.
	r.DeferDestroy(func() {
		for _, mat := range res.materials {
			r.DestroyMaterial(mat)
		}
		for _, t := range res.textures {
			r.DestroyTexture(t)
		}
		for _, mesh := range res.meshes {
			r.DestroyMesh(mesh)
		}
	})
}

// DestroySkinnedModel releases what LoadGLTFSkinned created for m.
//
// The GPU side of a skinned model is its embedded Model and nothing else:
// Skeleton, Animations and RootTransform are plain CPU data, and the joint
// buffer a game animates it with comes from CreateJointBuffer, which the
// loader never calls -- so it belongs to whoever created it and goes back
// through DestroyJointBuffer. Leaving it alone here is deliberate: one model
// can be drawn by several entities with a joint buffer each.
func (r *Renderer) DestroySkinnedModel(m *SkinnedModel) {
	if m == nil {
		return
	}
	r.DestroyModel(&m.Model)
}
