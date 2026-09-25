package renderer

import (
	"fmt"
	"io/fs"

	"github.com/qmuntal/gltf"
)

// ReadGLTF reads a glTF or GLB document from fsys and returns the same *Model
// LoadGLTF would, with every GPU handle left nil.
//
// This exists because the things that most need a level's DATA need its pixels
// least (issue #75): a dedicated server wants the same colliders and spawn
// points its clients have, from the same file, on a machine with no GPU; a
// navmesh baker or a CI level validator is a command-line tool; and a game's
// own level-loading tests should not need a device to run. Before this, the
// only door was LoadGLTF, which needs a Vulkan device because it uploads as it
// goes -- so none of them could open the file at all.
//
// What a read Model carries: Nodes (with Extras), Lights, and one ModelMesh
// per triangle primitive with Name, BaseColor, Metallic, Roughness,
// DoubleSided, AlphaMode, AlphaCutoff, BaseAlpha, Node, DocMesh, and
// Verts/Idx -- byte for byte what LoadGLTF retains, including UVs with
// KHR_texture_transform already baked in and the winding already reversed
// into the engine's.
//
// What it does not: Mesh, Texture and Material are nil, and nothing in the
// document's images is read or decoded (see uploadGLTFImages -- a server does
// not want to spend a 4K PNG decode learning where the doors are). A Model
// from here is also not something DestroyModel has anything to do: it owns no
// GPU resources, so destroying it is a no-op rather than an error.
//
// Model.Bounds, Node, NodeInMeshSpace, NodeMeshes, LightWorldPosDir and
// ReleaseGeometry are pure and all work on what this returns;
// Renderer.CombineModel does not, because it uploads.
func ReadGLTF(fsys fs.FS, name string) (*Model, error) {
	rd, err := readGLTF(fsys, name)
	if err != nil {
		return nil, err
	}
	return rd.model, nil
}

// ReadGLTFSkinned is ReadGLTF for a document with a skin: the same GPU-free
// read, returning the Skeleton, the AnimationClips and the armature
// RootTransform alongside the Model.
//
// It exists for the same callers ReadGLTF does and splits just as cleanly --
// buildSkeleton, loadAnimations and computeArmatureRootTransform were already
// pure functions of the document, reachable only through the GPU path. A
// server that resolves hits against a character's bones needs exactly those
// three and no pixels.
//
// One honest cost, because it is not free and pretending otherwise would be
// the drift this refactor exists to prevent: the read decodes each skinned
// primitive's vertices even here, where they cannot be returned.
// ModelMesh.Verts is []Vertex and a skinned primitive decodes to
// SkinnedVertex, a different layout, so there is nowhere to put them -- the
// same reason LoadGLTFSkinned leaves Verts empty (docs/agents/models.md).
// Skipping the decode in the read-only case would mean two decode paths and a
// flag to choose between them, which is precisely what issue #75 asked not to
// build. The static primitives in the same file DO come back with their
// Verts, which is what a server placing a skinned character's props wants.
func ReadGLTFSkinned(fsys fs.FS, name string) (*SkinnedModel, error) {
	rd, err := readGLTFSkinned(fsys, name)
	if err != nil {
		return nil, err
	}
	return rd.skinned, nil
}

// gltfRead is one decoded glTF document: the Model a GPU-free read returns,
// plus everything only an upload needs. LoadGLTF and LoadGLTFSkinned are
// "read, then upload" over this, so there is exactly one decode path and the
// GPU-free read cannot drift from the one the renderer actually draws.
type gltfRead struct {
	model *Model
	// skinned is non-nil only for readGLTFSkinned, and its embedded Model is
	// the same one model points at.
	skinned *SkinnedModel

	// doc and base are what the upload half still needs and the read half is
	// finished with: the document (for materials and image references) and an
	// fs.FS rooted at the document's own directory (for external images).
	doc  *gltf.Document
	base fs.FS

	// fsys is the filesystem the CALLER named, kept alongside base because the
	// shared-image cache keys on it (issue #136) and base will not do: fs.Sub
	// hands back a fresh *subFS on every call, so two documents in one
	// directory have equal fsys and unequal base. Reading images still goes
	// through base -- this is identity, not a second way to open a file.
	fsys fs.FS

	// name is the document's name as the caller gave it, for log lines and
	// error messages that quote it, and for the directory the cache key's
	// image path is resolved against.
	name string

	// prims is one entry per model.Meshes entry, in the same order, holding
	// what the upload needs and a Model has nowhere to keep.
	prims []primitiveGeometry
}

// primitiveGeometry is the per-primitive staging an upload reads and a Model
// does not carry.
//
// material is the glTF material INDEX: ModelMesh keeps the material's name,
// which is what a game identifies a primitive by, and the index is what
// resolveBaseColorTexture and resolveMaterialMaps need. -1 means the
// primitive references no material at all, which glTF allows.
//
// skinned holds a skinned primitive's vertices, which have no home on
// ModelMesh (see ReadGLTFSkinned). verts and idx are the same slices
// ModelMesh.Verts and .Idx point at for a static primitive -- kept here too so
// the upload does not depend on a caller not having called ReleaseGeometry in
// between, which is free (a slice header) and removes an ordering rule nobody
// would remember.
type primitiveGeometry struct {
	material int
	verts    []Vertex
	skinned  []SkinnedVertex
	idx      []uint32
}

// readGLTF is LoadGLTF's decode half: everything that is a pure function of
// the document, in the order LoadGLTF has always walked it (doc.Meshes, then
// each mesh's triangle primitives), so Model.Meshes comes out in the same
// order and the draw list behind it does not move.
func readGLTF(fsys fs.FS, name string) (*gltfRead, error) {
	rd, err := openRead(fsys, name)
	if err != nil {
		return nil, err
	}

	uvCache := make(map[int]uvAffine)
	meshOwners := meshOwnerNodes(rd.doc)

	for meshIdx, mesh := range rd.doc.Meshes {
		for _, prim := range mesh.Primitives {
			if prim.Mode != gltf.PrimitiveTriangles {
				continue
			}
			if err := rd.readPrimitive(prim, meshIdx, mesh.Name, meshOwners[meshIdx], uvCache, false); err != nil {
				return nil, err
			}
		}
	}
	return rd, nil
}

// readGLTFSkinned is LoadGLTFSkinned's decode half.
//
// It walks doc.Nodes in two passes rather than doc.Meshes, which is NOT a
// stylistic difference from readGLTF: the skin reference that decides whether
// a primitive decodes to Vertex or SkinnedVertex lives on the node, not on the
// mesh, and the resulting Model.Meshes order (every skinned primitive, then
// every static one) is the order the animation path and every existing caller
// already sees. Unifying the two traversals would reorder Model.Meshes for
// every skinned model, which reorders the draw list.
func readGLTFSkinned(fsys fs.FS, name string) (*gltfRead, error) {
	rd, err := openRead(fsys, name)
	if err != nil {
		return nil, err
	}
	doc := rd.doc

	if len(doc.Skins) == 0 {
		return nil, fmt.Errorf("gltf has no skins")
	}
	skeleton, err := buildSkeleton(doc, doc.Skins[0])
	if err != nil {
		return nil, fmt.Errorf("build skeleton: %w", err)
	}
	animations, err := loadAnimations(doc, skeleton)
	if err != nil {
		return nil, fmt.Errorf("load animations: %w", err)
	}

	uvCache := make(map[int]uvAffine) // shared across both passes, keyed by materialIdx
	meshOwners := meshOwnerNodes(doc)
	loadedMeshes := make(map[int]bool)

	// Pass 1: meshes instanced by a node that carries a skin.
	for _, node := range doc.Nodes {
		if node.Skin == nil || node.Mesh == nil {
			continue
		}
		meshIdx := *node.Mesh
		loadedMeshes[meshIdx] = true
		mesh := doc.Meshes[meshIdx]
		for _, prim := range mesh.Primitives {
			if prim.Mode != gltf.PrimitiveTriangles {
				continue
			}
			if err := rd.readPrimitive(prim, meshIdx, mesh.Name, meshOwners[meshIdx], uvCache, true); err != nil {
				return nil, err
			}
		}
	}

	// Pass 2: everything else, decoded exactly as readGLTF decodes it.
	for _, node := range doc.Nodes {
		if node.Mesh == nil || node.Skin != nil {
			continue
		}
		meshIdx := *node.Mesh
		if loadedMeshes[meshIdx] {
			continue
		}
		loadedMeshes[meshIdx] = true
		mesh := doc.Meshes[meshIdx]
		for _, prim := range mesh.Primitives {
			if prim.Mode != gltf.PrimitiveTriangles {
				continue
			}
			if err := rd.readPrimitive(prim, meshIdx, mesh.Name, meshOwners[meshIdx], uvCache, false); err != nil {
				return nil, err
			}
		}
	}

	rd.skinned = &SkinnedModel{
		Model:         *rd.model,
		Skeleton:      skeleton,
		Animations:    animations,
		RootTransform: computeArmatureRootTransform(doc, skeleton),
	}
	// SkinnedModel embeds Model by value, so the copy above is the one every
	// caller (and the upload half, which fills in GPU handles) has to work
	// through from here. Repointing rd.model at it rather than leaving the
	// original behind is what keeps "read, then upload" filling in the Model
	// the caller actually gets.
	rd.model = &rd.skinned.Model
	return rd, nil
}

// openRead decodes the document and fills in everything that does not depend
// on a single primitive: the node graph and the punctual lights, both already
// pure functions of the document.
func openRead(fsys fs.FS, name string) (*gltfRead, error) {
	doc, base, err := openGLTF(fsys, name)
	if err != nil {
		return nil, err
	}
	rd := &gltfRead{model: new(Model), doc: doc, base: base, fsys: fsys, name: name}
	rd.model.Nodes = extractNodes(doc)
	rd.model.Lights = extractLights(doc, rd.model.Nodes)
	return rd, nil
}

// readPrimitive decodes one triangle primitive and appends its ModelMesh and
// staging geometry.
//
// meshName is the DOC mesh's name, used only in the error message, which is
// the one place the doc mesh's own name appears -- ModelMesh.Name is the
// material's.
func (rd *gltfRead) readPrimitive(prim *gltf.Primitive, meshIdx int, meshName string, owner int, uvCache map[int]uvAffine, skinned bool) error {
	matIdx := -1
	if prim.Material != nil {
		matIdx = int(*prim.Material)
	}
	uv := resolveUVTransform(rd.name, rd.doc, uvCache, matIdx)

	g := primitiveGeometry{material: matIdx}
	var err error
	if skinned {
		g.skinned, g.idx, err = extractSkinnedPrimitive(rd.doc, prim, uv)
		if err != nil {
			return fmt.Errorf("extract skinned primitive from mesh %q: %w", meshName, err)
		}
	} else {
		g.verts, g.idx, err = extractPrimitive(rd.doc, prim, uv)
		if err != nil {
			return fmt.Errorf("extract primitive from mesh %q: %w", meshName, err)
		}
	}
	reverseWinding(g.idx)

	baseColor, metallic, roughness := resolveMaterial(rd.doc, matIdx)
	alphaMode, alphaCutoff, baseAlpha := resolveAlpha(rd.doc, matIdx)

	mm := ModelMesh{
		Name:        materialName(rd.doc, prim.Material),
		Skinned:     skinned,
		DoubleSided: resolveDoubleSided(rd.doc, matIdx),
		BaseColor:   baseColor,
		Metallic:    metallic,
		Roughness:   roughness,
		AlphaMode:   alphaMode,
		AlphaCutoff: alphaCutoff,
		BaseAlpha:   baseAlpha,
		Node:        owner,
		DocMesh:     meshIdx,
	}
	// Skinned primitives carry the name but not Verts: they decode to
	// SkinnedVertex, which is a different layout, so there is nothing to put
	// in a []Vertex. Model.Bounds and CombineModel therefore report "cannot
	// answer" for a purely skinned model rather than answering from an empty
	// set.
	if !skinned {
		mm.Verts, mm.Idx = g.verts, g.idx
	}

	rd.model.Meshes = append(rd.model.Meshes, mm)
	rd.prims = append(rd.prims, g)
	return nil
}

// reverseWinding flips every triangle's second and third index in place: glTF
// is counter-clockwise, this engine is clockwise.
func reverseWinding(indices []uint32) {
	for i := 0; i+2 < len(indices); i += 3 {
		indices[i+1], indices[i+2] = indices[i+2], indices[i+1]
	}
}

// resolveDoubleSided reads a glTF material's doubleSided flag, with the same
// out-of-range tolerance resolveMaterial and resolveAlpha already have for a
// primitive that references no material or a broken index -- glTF's own
// default for an absent material is single-sided.
//
// Read in the decode half rather than at upload because it is data about the
// surface, not about the GPU, and a level validator reading with no device
// wants it: Blender writes doubleSided: true unless Backface Culling is
// ticked, so it is the flag that most often differs from what an artist
// expected (docs/agents/blender-pipeline.md).
func resolveDoubleSided(doc *gltf.Document, materialIdx int) bool {
	if materialIdx < 0 || materialIdx >= len(doc.Materials) {
		return false
	}
	mat := doc.Materials[materialIdx]
	if mat == nil {
		return false
	}
	return mat.DoubleSided
}
