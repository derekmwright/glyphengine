package renderer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"io/fs"
	"log"
	"path"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/modeler"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// ModelMesh pairs a GPU mesh with an optional texture loaded from a glTF file.
type ModelMesh struct {
	// Name is the glTF material's name, or "" if the material had none or the
	// primitive had no material at all.
	//
	// It is how a game identifies a primitive when it has to drive one from
	// somewhere other than the renderer -- a charge strip that fills, a lamp
	// that changes with state. Everything else on this struct describes how the
	// primitive is *drawn*, which leaves appearance as the only handle, and
	// matching on a base colour needs a float tolerance to survive the
	// exporter's round trip. A tolerance is exactly what makes that fragile: it
	// is invisible in the art, it is not greppable, and a model re-exported
	// with a colour a thousandth off loads without complaint and simply never
	// lights up.
	Name string

	Mesh        *Mesh
	Texture     *Texture
	Skinned     bool       // true if this mesh uses skinned vertices
	DoubleSided bool       // glTF material says render both faces
	BaseColor   [3]float32 // material base color factor (default white)
	Metallic    float32    // 0 = dielectric, 1 = metal (from glTF PBR)
	Roughness   float32    // 0 = mirror, 1 = matte (from glTF PBR)

	// AlphaMode, AlphaCutoff and BaseAlpha surface glTF's alpha data as-is
	// (issue #68); the engine takes no action on any of them. AlphaMode is
	// AlphaModeOpaque and BaseAlpha is 1 when the primitive has no
	// material, the same defaults glTF itself uses for an absent material.
	//
	// BaseAlpha exists because BaseColor above stays [3]float32 -- widening
	// it to carry alpha would break every existing caller that already
	// treats it as three floats. See docs/agents/translucency.md for the
	// engine feature a game opts a BLEND mesh into, and
	// docs/agents/models.md for why AlphaModeMask is reported but not
	// honoured by any lit pipeline today.
	AlphaMode   AlphaMode
	AlphaCutoff float32 // meaningful only when AlphaMode is AlphaModeMask
	BaseAlpha   float32 // the base colour factor's alpha component

	// Material is non-nil when the glTF material carried a normal,
	// metallic-roughness, or occlusion map. Set it on MaterialRef.PBR to get
	// them; Texture alone ignores them and lights the surface as one uniform
	// material.
	//
	// Metallic and Roughness above stay meaningful either way — the maps
	// multiply them.
	Material *Material

	// Verts and Idx are the geometry this primitive was decoded from, in the
	// engine's winding. They are retained so a model can be treated as geometry
	// rather than only as a draw call: merged with its siblings, measured,
	// turned into a collider, or validated at load.
	//
	// Retained by default because the decode allocated them anyway and
	// discarding was the only reason they were unavailable. Model.ReleaseGeometry
	// drops them for a caller that has finished with them and would rather have
	// the memory.
	//
	// Empty on a skinned primitive. Those decode to SkinnedVertex, a different
	// layout carrying joint indices and weights, so there is nothing to put
	// here. Name is still set, and Model.Bounds reports that it cannot answer
	// rather than answering from an empty set.
	Verts []Vertex
	Idx   []uint32

	// Node is the index into Model.Nodes of the first node (in glTF node-index
	// order) that instances the doc mesh this primitive came from, or -1 if no
	// node does.
	//
	// A doc mesh can be referenced by several nodes (instancing) or by none.
	// This is "first" rather than "all" because a primitive is drawn once
	// regardless of instance count, and the one use for this field -- finding
	// the node a socket's mesh-space helper needs -- only needs one of them.
	// All primitives split from the same doc mesh share this value.
	Node int

	// DocMesh is the index into the source document's Meshes this primitive
	// was split from -- doc.Meshes[DocMesh], not Model.Meshes[DocMesh].
	//
	// Kept so Model.NodeMeshes can go the other direction from Node above: a
	// doc mesh instanced by several nodes (issue #66's forty identical lamp
	// posts) needs, for a GIVEN node, every ModelMesh that doc mesh split
	// into -- Node alone only ever names the first instancing node, never the
	// mesh's primitives themselves.
	DocMesh int
}

// materialName returns the name of the material a primitive references, or ""
// when it references none or the material is unnamed.
//
// Index rather than pointer because glTF stores the reference as an index into
// the document, and a nil one means "no material" rather than "material zero".
func materialName(doc *gltf.Document, material *int) string {
	if material == nil || *material < 0 || *material >= len(doc.Materials) {
		return ""
	}
	return doc.Materials[*material].Name
}

// Model holds all meshes loaded from a single glTF/GLB file.
type Model struct {
	Meshes []ModelMesh

	// Nodes is the glTF scene graph LoadGLTF walks to find the meshes, kept
	// rather than discarded so a game can find a point on the model that
	// carries no geometry at all -- an empty node an artist placed as a
	// socket, a muzzle, a doorway (issue #46).
	//
	// Nodes[i] is built from doc.Nodes[i]: the indices are kept equal on
	// purpose, so a game debugging a socket against Blender or another glTF
	// tool is looking at the same number the tooling shows.
	//
	// Space: World is in the glTF scene's space, which is the same space a
	// mesh's vertices are in only when that mesh's own instancing node chain
	// is identity -- LoadGLTF never applies a node's transform to the
	// vertices it decodes (see the "Node space" section of
	// docs/agents/models.md). Use Model.NodeInMeshSpace rather than a node's
	// World directly when placing something relative to a mesh.
	Nodes []ModelNode

	// Lights is every KHR_lights_punctual light the document carries,
	// attached to Nodes by ModelLight.Node. Nil when the document has none --
	// most models, since this is a level-authoring extension rather than
	// something a single prop needs. See docs/agents/lights.md for the units
	// caveat and the per-node placement pattern.
	Lights []ModelLight

	// owned is what this model's upload created on the GPU, recorded as it
	// created it so DestroyModel can release each resource exactly once (see
	// modelResources for why recording beats rediscovering). Nil on a Model
	// from ReadGLTF, which owns nothing, and set back to nil by DestroyModel,
	// which is what makes a second call a no-op.
	owned *modelResources
}

// ModelNode is one node in the glTF scene graph a model was loaded from.
//
// Translation, Rotation and Scale are the node's authored TRS fields exactly
// as glTF stored them (defaulted the way glTF defines, via
// TranslationOrDefault/RotationOrDefault/ScaleOrDefault), regardless of
// whether the node actually used TRS or a Matrix to author its transform.
// Local is the transform that matters: nodeLocalTransform's TRS-vs-Matrix
// rule decides it, the same rule LoadGLTFSkinned's joints and armature root
// already use, so a node authored with Matrix has a Local that reflects it
// while Translation/Rotation/Scale sit at their TRS defaults.
type ModelNode struct {
	Name string

	// Parent is the index into Model.Nodes of this node's parent, or -1 for
	// a root.
	Parent int

	Translation mgl32.Vec3
	Rotation    mgl32.Quat
	Scale       mgl32.Vec3

	// Local is this node's transform in its parent's space.
	Local mgl32.Mat4

	// World is Local composed down the parent chain, in the glTF scene's
	// space. See the space caveat on Model.Nodes before using this to place
	// anything relative to a mesh's vertices.
	World mgl32.Mat4

	// Mesh is the index into doc.Meshes this node instances, or -1 if the
	// node carries no mesh (an empty node, a light, a joint).
	Mesh int

	// Extras is the node's glTF `extras` as raw JSON, nil when the node has
	// none. This is glTF's own slot for application data -- a designer
	// tagging a node `{"collider": "box", "static": true}` in an editor --
	// and the engine deliberately does not look inside it (AGENTS.md rule
	// 14: unblock a path, do not ship an opinion). A game reads its own
	// vocabulary out with json.Unmarshal(node.Extras, &myTags).
	//
	// qmuntal/gltf decodes `extras` into `any` (there is no registered type
	// for it the way KHR_lights_punctual has one) -- extractNodes re-marshals
	// whatever came out, so this is always either nil or valid JSON, never
	// the decoded Go value glTF handed back.
	Extras json.RawMessage
}

// openGLTF opens a glTF or GLB document from fsys and returns it along with an
// fs.FS rooted at the document's own directory.
//
// glTF references external buffers and images by URI relative to itself, so a
// model at "models/rock.gltf" naming "rock.bin" means "models/rock.bin". The
// decoder resolves those against the FS it is given, which therefore has to be
// the subtree containing the document rather than the caller's root.
func openGLTF(fsys fs.FS, name string) (*gltf.Document, fs.FS, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, nil, fmt.Errorf("open gltf %q: %w", name, err)
	}
	defer f.Close()

	base := fsys
	if dir := path.Dir(name); dir != "." {
		base, err = fs.Sub(fsys, dir)
		if err != nil {
			return nil, nil, fmt.Errorf("gltf %q: resolve %q: %w", name, dir, err)
		}
	}

	doc := new(gltf.Document)
	if err := gltf.NewDecoderFS(f, base).Decode(doc); err != nil {
		return nil, nil, fmt.Errorf("decode gltf %q: %w", name, err)
	}
	return doc, base, nil
}

// LoadGLTF reads a glTF or GLB document from fsys and returns a Model with
// GPU-uploaded meshes and textures.
//
// It is ReadGLTF followed by an upload of what the read produced, and
// deliberately nothing else (issue #75). The decode -- node graph, lights,
// material factors, alpha, and every primitive's vertices and indices -- has
// exactly one implementation, shared with the GPU-free ReadGLTF, so a server
// or a tool reading a level cannot end up with different numbers from the
// client drawing it.
func (r *Renderer) LoadGLTF(fsys fs.FS, name string) (*Model, error) {
	rd, err := readGLTF(fsys, name)
	if err != nil {
		return nil, err
	}
	if err := r.uploadModel(rd); err != nil {
		return nil, err
	}
	warnUntransformedMeshNodes(name, rd.model.Nodes, rd.model.Meshes)
	return rd.model, nil
}

// uploadModel is LoadGLTF's upload half: it walks what the read produced and
// fills in the GPU handles the read left nil.
//
// Images are decoded HERE and nowhere else. That is the whole point of the
// split: a document's textures are the expensive part of opening it and the
// part a server, a navmesh baker or a level validator has no use for.
func (r *Renderer) uploadModel(rd *gltfRead) error {
	textures, err := r.uploadGLTFImages(rd.doc, rd.base)
	if err != nil {
		return fmt.Errorf("load gltf images: %w", err)
	}
	// Ownership is recorded from the upload's OWN maps, before anything can
	// be shared onto a ModelMesh -- see modelResources.
	owned := newModelResources(rd.doc, textures)
	rd.model.owned = owned

	materialCache := make(map[int]*Material)
	for i := range rd.model.Meshes {
		mm := &rd.model.Meshes[i]
		if err := r.uploadPrimitiveMesh(rd, i); err != nil {
			return err
		}
		owned.meshes = append(owned.meshes, mm.Mesh)
		if matIdx := rd.prims[i].material; matIdx >= 0 {
			mm.Texture = r.resolveBaseColorTexture(rd.doc, textures, matIdx)
			mm.Material, err = r.resolveMaterialMaps(rd.doc, textures, materialCache, matIdx)
			if err != nil {
				return err
			}
		}
	}
	owned.addMaterials(rd.doc, materialCache)
	return nil
}

// uploadPrimitiveMesh creates the GPU mesh for one primitive of a read,
// choosing uint16 or uint32 indices by vertex count the way this loader always
// has.
func (r *Renderer) uploadPrimitiveMesh(rd *gltfRead, i int) error {
	g := rd.prims[i]
	meshName := rd.doc.Meshes[rd.model.Meshes[i].DocMesh].Name

	var gpuMesh *Mesh
	var err error
	if g.skinned != nil {
		if len(g.skinned) <= 65535 {
			gpuMesh, err = r.CreateSkinnedIndexedMesh(g.skinned, narrowIndices(g.idx))
		} else {
			gpuMesh, err = r.CreateSkinnedIndexedMesh32(g.skinned, g.idx)
		}
		if err != nil {
			return fmt.Errorf("create skinned mesh for %q: %w", meshName, err)
		}
	} else {
		if len(g.verts) <= 65535 {
			gpuMesh, err = r.CreateIndexedMesh(g.verts, narrowIndices(g.idx))
		} else {
			gpuMesh, err = r.CreateIndexedMesh32(g.verts, g.idx)
		}
		if err != nil {
			return fmt.Errorf("create mesh for %q: %w", meshName, err)
		}
	}
	rd.model.Meshes[i].Mesh = gpuMesh
	return nil
}

// narrowIndices copies uint32 indices down to uint16 for a mesh small enough
// to use them.
func narrowIndices(idx []uint32) []uint16 {
	out := make([]uint16, len(idx))
	for i, v := range idx {
		out[i] = uint16(v)
	}
	return out
}

// warnUntransformedMeshNodes emits the one log line LoadGLTF and
// LoadGLTFSkinned each print when a mesh's instancing node carries a transform.
//
// This loader draws every primitive in mesh-local space -- it never applies a
// node's transform to the vertices it decoded. That is silently correct only
// when the node instancing a mesh has an identity transform. Making the
// exception visible costs one log line; finding it by hand cost someone a
// hand-measured constant that broke on re-export (issue #46).
//
// A level file (docs/agents/models.md's "Loading a level" section) is the
// case where this fires for EVERY mesh node on purpose: the per-node
// pattern places each mesh from Model.Nodes itself, which is the correct
// handling and makes this specific warning a false alarm there. cappedNodeList
// keeps the line readable regardless of how many nodes that is, and the
// message points at the pattern that makes the warning moot rather than
// just repeating that something is untransformed.
//
// It belongs to the UPLOAD half, not the read: it is a statement about how the
// geometry is about to be DRAWN. ReadGLTF's callers -- a server, a navmesh
// baker, a level validator -- place geometry themselves from Model.Nodes and
// would be reading a warning about a decision they never made.
func warnUntransformedMeshNodes(name string, nodes []ModelNode, meshes []ModelMesh) {
	if names := untransformedMeshNodes(nodes, meshes); len(names) > 0 {
		log.Printf("gltf %q: static geometry drawn without its node transform -- node(s) %s carry a transform LoadGLTF does not apply to their mesh's vertices; if this is a level file, place these with Model.Nodes per-node (see docs/agents/models.md#loading-a-level) rather than treating this as an error", name, cappedNodeList(names, untransformedNodeListCap))
	}
}

// extractPrimitive reads vertex attributes and indices from a glTF
// primitive. uv is the affine transform to bake into TEXCOORD_0 before it
// reaches the vertex array -- identityUV for a primitive whose material
// carries no KHR_texture_transform, which applyUVTransform turns into a
// no-op rather than wasted arithmetic (issue #69).
//
// Baking here rather than after the caller gets vertices back is safe even
// when several primitives split from the same doc mesh share one accessor:
// modeler.ReadTextureCoord is called with a nil destination buffer, and
// makeBufferOf's own rule ("len(buffer) < count" with buffer==nil) allocates
// a FRESH slice on every call rather than reusing one keyed by accessor --
// confirmed by reading modeler/read.go rather than assumed, per the issue's
// instruction to verify this claim before relying on it. Each primitive's uvs
// here is therefore its own copy, never aliased with a sibling primitive's.
//
// A free function rather than a method: it never touched the *Renderer it used
// to hang off, which is exactly the shape issue #75 was about -- a pure decode
// reachable only through the GPU path. Tests used to call it on a nil receiver
// to say so; now they just call it.
func extractPrimitive(doc *gltf.Document, prim *gltf.Primitive, uv uvAffine) ([]Vertex, []uint32, error) {
	// Positions (required)
	posIdx, ok := prim.Attributes[gltf.POSITION]
	if !ok {
		return nil, nil, fmt.Errorf("primitive missing POSITION attribute")
	}
	positions, err := modeler.ReadPosition(doc, doc.Accessors[posIdx], nil)
	if err != nil {
		return nil, nil, fmt.Errorf("read positions: %w", err)
	}

	vertexCount := len(positions)

	// Normals (optional, default +Y)
	var normals [][3]float32
	if idx, ok := prim.Attributes[gltf.NORMAL]; ok {
		normals, err = modeler.ReadNormal(doc, doc.Accessors[idx], nil)
		if err != nil {
			return nil, nil, fmt.Errorf("read normals: %w", err)
		}
	}

	// UVs (optional, default 0,0)
	var uvs [][2]float32
	if idx, ok := prim.Attributes[gltf.TEXCOORD_0]; ok {
		uvs, err = modeler.ReadTextureCoord(doc, doc.Accessors[idx], nil)
		if err != nil {
			return nil, nil, fmt.Errorf("read texcoords: %w", err)
		}
		applyUVTransform(uvs, uv)
	}

	// Vertex colors (optional, default white)
	var colors [][4]uint8
	if idx, ok := prim.Attributes[gltf.COLOR_0]; ok {
		colors, err = modeler.ReadColor(doc, doc.Accessors[idx], nil)
		if err != nil {
			return nil, nil, fmt.Errorf("read colors: %w", err)
		}
	}

	// Build vertex array
	vertices := make([]Vertex, vertexCount)
	for i := range vertices {
		vertices[i].Pos = positions[i]

		if normals != nil && i < len(normals) {
			vertices[i].Normal = normals[i]
		} else {
			vertices[i].Normal = [3]float32{0, 1, 0}
		}

		if uvs != nil && i < len(uvs) {
			vertices[i].UV = uvs[i]
		}

		if colors != nil && i < len(colors) {
			c := colors[i]
			vertices[i].Color = [3]float32{
				float32(c[0]) / 255.0,
				float32(c[1]) / 255.0,
				float32(c[2]) / 255.0,
			}
		} else {
			vertices[i].Color = [3]float32{1, 1, 1}
		}
	}

	// Indices (required for our loader)
	if prim.Indices == nil {
		return nil, nil, fmt.Errorf("primitive has no indices")
	}
	indices, err := modeler.ReadIndices(doc, doc.Accessors[*prim.Indices], nil)
	if err != nil {
		return nil, nil, fmt.Errorf("read indices: %w", err)
	}

	return vertices, indices, nil
}

// dataImageIndices returns the set of image indices the document uses as data
// rather than as colour: normal, metallic-roughness, and occlusion maps.
//
// This has to be decided before upload, because sRGB is a property of the image
// and not of the sampler. glTF only says which encoding an image wants
// indirectly, through the slot a material binds it to — so the materials have to
// be walked first. Uploading a normal map as sRGB is silent: it samples fine and
// every normal leans the same wrong way.
//
// An image bound to both a colour and a data slot is pathological and does not
// occur in practice; if it did, data wins here, which is the safer error — a
// slightly dark albedo rather than corrupt lighting.
func dataImageIndices(doc *gltf.Document) map[int]bool {
	data := make(map[int]bool)
	mark := func(texIdx int) {
		if texIdx < 0 || texIdx >= len(doc.Textures) {
			return
		}
		if src := doc.Textures[texIdx].Source; src != nil {
			data[int(*src)] = true
		}
	}
	for _, mat := range doc.Materials {
		if mat.NormalTexture != nil && mat.NormalTexture.Index != nil {
			mark(int(*mat.NormalTexture.Index))
		}
		if mat.OcclusionTexture != nil && mat.OcclusionTexture.Index != nil {
			mark(int(*mat.OcclusionTexture.Index))
		}
		if pbr := mat.PBRMetallicRoughness; pbr != nil && pbr.MetallicRoughnessTexture != nil {
			mark(pbr.MetallicRoughnessTexture.Index)
		}
	}
	return data
}

// decodeGLTFImages reads and decodes every image the document carries, keyed
// by image index.
//
// Split out from the upload (issue #75) so the expensive half of opening a
// glTF -- reading a 4K PNG off disk and turning it into pixels -- is a step
// LoadGLTF takes and ReadGLTF does not. It is also the step that FAILS on a
// document with corrupt image bytes, which is how TestReadGLTFDoesNotDecodeImages
// can prove the read skips it without a device: the read succeeds on bytes
// this rejects.
//
// A pure function of the document and its directory: no Renderer, no GPU.
func decodeGLTFImages(doc *gltf.Document, base fs.FS) (map[int]*decodedImage, error) {
	decoded := make(map[int]*decodedImage)
	for i, img := range doc.Images {
		var imgBytes []byte

		if img.BufferView != nil {
			// GLB-embedded image
			bv := doc.BufferViews[*img.BufferView]
			buf := doc.Buffers[bv.Buffer]
			imgBytes = buf.Data[bv.ByteOffset : bv.ByteOffset+bv.ByteLength]
		} else if img.IsEmbeddedResource() {
			// Base64-encoded data URI
			var err error
			imgBytes, err = img.MarshalData()
			if err != nil {
				return nil, fmt.Errorf("decode embedded image %d: %w", i, err)
			}
		} else if img.URI != "" {
			// External image, named relative to the glTF document.
			var err error
			imgBytes, err = fs.ReadFile(base, img.URI)
			if err != nil {
				return nil, fmt.Errorf("read external image %d (%s): %w", i, img.URI, err)
			}
		} else {
			continue
		}

		d, err := decodeImage(imgBytes)
		if err != nil {
			return nil, fmt.Errorf("decode image %d (%s): %w", i, img.Name, err)
		}
		decoded[i] = d
	}
	return decoded, nil
}

// uploadGLTFImages decodes all images in the document and uploads them as GPU
// textures. Returns a map from image index to Texture.
func (r *Renderer) uploadGLTFImages(doc *gltf.Document, base fs.FS) (map[int]*Texture, error) {
	decoded, err := decodeGLTFImages(doc, base)
	if err != nil {
		return nil, err
	}

	textures := make(map[int]*Texture)
	dataImages := dataImageIndices(doc)
	wrapModes, wrapConflicts := imageWrapModes(doc)

	// doc.Images order, not map order: an upload failure names an image index,
	// and the textures created before it have to be the same set on every run
	// for that to mean anything.
	for i, img := range doc.Images {
		d, ok := decoded[i]
		if !ok {
			continue
		}

		wrap, ok := wrapModes[i]
		if !ok {
			wrap = repeatWrap
		}
		if wrapConflicts[i] {
			log.Printf("gltf: image %d (%s) is referenced by glTF textures with different sampler wrap modes; uploading it once with the first and drawing every use of it that way", i, img.Name)
		}

		// Same srgb-vs-data choice CreateTexture/CreateDataTexture make, but
		// calling r.createTexture directly rather than going through either
		// public constructor: both hardcode repeat addressing, and a glTF
		// image's wrap mode is per-Texture (image+sampler pair) data this
		// loader now has to honour instead (issue #69).
		tex, err := r.createTexture(d.pixels, d.width, d.height, textureOptions{
			srgb:     !dataImages[i],
			filter:   core1_0.FilterLinear,
			addressU: wrap.u,
			addressV: wrap.v,
			mipmap:   true,
		})
		if err != nil {
			return nil, fmt.Errorf("upload texture %d (%s): %w", i, img.Name, err)
		}

		textures[i] = tex
	}

	return textures, nil
}

type decodedImage struct {
	pixels        []byte
	width, height int
}

// decodeImage decodes PNG/JPEG bytes into straight-alpha RGBA pixel data.
func decodeImage(data []byte) (*decodedImage, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	return &decodedImage{
		pixels: straightRGBA(img),
		width:  bounds.Dx(),
		height: bounds.Dy(),
	}, nil
}

// straightRGBA returns an image's pixels as tightly packed straight-alpha
// RGBA, borrowing the decoder's own buffer when it already is that.
//
// image/png hands back *image.RGBA for an RGB file and *image.NRGBA for an
// RGBA one, both already 4 bytes per pixel and row-major; a second full-size
// NRGBA plus a draw.Draw through the generic RGBA64At/SetRGBA64 path was
// 3.26 GB of a 3.9 GB total-alloc scene load and about 2 s of CPU across 60
// props with 2048x2048 sheets (issue #135). Premultiplied and straight alpha
// are the same bytes when every alpha is 255, which is what Opaque checks, so
// an RGB PNG's buffer is used as-is; anything else, a sub-image, a JPEG's
// YCbCr, a paletted PNG, an RGBA image with real alpha, takes the conversion
// it always took.
func straightRGBA(img image.Image) []byte {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	packed := bounds.Min == image.Point{} && w > 0 && h > 0
	switch m := img.(type) {
	case *image.NRGBA:
		if packed && m.Stride == 4*w && len(m.Pix) >= 4*w*h {
			return m.Pix[:4*w*h]
		}
	case *image.RGBA:
		if packed && m.Stride == 4*w && len(m.Pix) >= 4*w*h && m.Opaque() {
			return m.Pix[:4*w*h]
		}
	}
	nrgba := image.NewNRGBA(bounds)
	draw.Draw(nrgba, bounds, img, bounds.Min, draw.Src)
	return nrgba.Pix
}

// resolveMaterial extracts PBR properties from a glTF material.
// Returns baseColor (default white), metallic (default 0), roughness (default 0.5).
func resolveMaterial(doc *gltf.Document, materialIdx int) (baseColor [3]float32, metallic, roughness float32) {
	baseColor = [3]float32{1, 1, 1}
	roughness = 0.5
	// materialIdx < 0 means "no material at all", the same thing resolveAlpha
	// and resolveDoubleSided answer with glTF's defaults for. It reaches here
	// now that one decode path serves both loaders (issue #75); before, every
	// caller checked prim.Material != nil first and a negative index would
	// have indexed out of bounds.
	if materialIdx < 0 || materialIdx >= len(doc.Materials) {
		return
	}
	mat := doc.Materials[materialIdx]
	if mat == nil || mat.PBRMetallicRoughness == nil {
		return
	}
	pbr := mat.PBRMetallicRoughness
	if pbr.BaseColorFactor != nil {
		f := pbr.BaseColorFactor
		baseColor = [3]float32{float32(f[0]), float32(f[1]), float32(f[2])}
	}
	if pbr.MetallicFactor != nil {
		metallic = float32(*pbr.MetallicFactor)
	}
	if pbr.RoughnessFactor != nil {
		roughness = float32(*pbr.RoughnessFactor)
	}
	return
}

// textureFor resolves a glTF texture index to an uploaded Texture, or nil.
func textureFor(doc *gltf.Document, textures map[int]*Texture, texIdx int) *Texture {
	if texIdx < 0 || texIdx >= len(doc.Textures) {
		return nil
	}
	src := doc.Textures[texIdx].Source
	if src == nil {
		return nil
	}
	return textures[int(*src)]
}

// resolveMaterialMaps builds a Material for a glTF material that carries any of
// normal, metallic-roughness, or occlusion.
//
// Returns nil when the material has none of them, which keeps every model that
// only has a base colour on the plain textured path — same pipeline, same
// descriptor set, same pixels as before this existed.
//
// The cache is keyed by glTF material index because primitives share materials:
// a model with twenty primitives and two materials should allocate two
// descriptor sets, not twenty. See maxMaterials for the budget.
//
// Emissive textures are deliberately absent from dataImageIndices: emission is a
// colour, so it goes through the sRGB path like albedo.
func (r *Renderer) resolveMaterialMaps(doc *gltf.Document, textures map[int]*Texture, cache map[int]*Material, materialIdx int) (*Material, error) {
	if materialIdx < 0 || materialIdx >= len(doc.Materials) {
		return nil, nil
	}
	if m, ok := cache[materialIdx]; ok {
		return m, nil
	}
	mat := doc.Materials[materialIdx]

	var opts MaterialOptions
	if mat.NormalTexture != nil && mat.NormalTexture.Index != nil {
		opts.Normal = textureFor(doc, textures, int(*mat.NormalTexture.Index))
		if mat.NormalTexture.Scale != nil {
			opts.NormalScale = float32(*mat.NormalTexture.Scale)
		}
	}
	if mat.OcclusionTexture != nil && mat.OcclusionTexture.Index != nil {
		opts.Occlusion = textureFor(doc, textures, int(*mat.OcclusionTexture.Index))
		if mat.OcclusionTexture.Strength != nil {
			opts.OcclusionStrength = float32(*mat.OcclusionTexture.Strength)
		}
	}
	if pbr := mat.PBRMetallicRoughness; pbr != nil && pbr.MetallicRoughnessTexture != nil {
		opts.MetallicRoughness = textureFor(doc, textures, pbr.MetallicRoughnessTexture.Index)
	}
	if mat.EmissiveTexture != nil {
		opts.Emissive = textureFor(doc, textures, mat.EmissiveTexture.Index)
	}
	opts.EmissiveFactor = [3]float32{
		float32(mat.EmissiveFactor[0]),
		float32(mat.EmissiveFactor[1]),
		float32(mat.EmissiveFactor[2]),
	}
	opts.EmissiveStrength = emissiveStrength(mat)

	// A material with nothing but an emissive factor still needs the material
	// path -- the plain lit shader has nowhere to put emission.
	emits := opts.Emissive != nil || opts.EmissiveFactor != [3]float32{}
	if opts.Normal == nil && opts.Occlusion == nil && opts.MetallicRoughness == nil && !emits {
		cache[materialIdx] = nil
		return nil, nil
	}

	// glTF normal maps are green-up, which is what the shader assumes, so no
	// FlipGreen here. A model authored for DirectX needs the flag set by hand;
	// the format carries no way to say which convention was used.
	opts.Albedo = r.resolveBaseColorTexture(doc, textures, materialIdx)

	m, err := r.CreateMaterial(opts)
	if err != nil {
		return nil, fmt.Errorf("create material %d (%s): %w", materialIdx, mat.Name, err)
	}
	cache[materialIdx] = m
	return m, nil
}

// resolveBaseColorTexture finds the base color texture for a material, if any.
func (r *Renderer) resolveBaseColorTexture(doc *gltf.Document, textures map[int]*Texture, materialIdx int) *Texture {
	if materialIdx < 0 || materialIdx >= len(doc.Materials) {
		return nil
	}
	mat := doc.Materials[materialIdx]
	if mat == nil || mat.PBRMetallicRoughness == nil {
		return nil
	}
	pbr := mat.PBRMetallicRoughness
	if pbr.BaseColorTexture == nil {
		return nil
	}
	texIdx := pbr.BaseColorTexture.Index
	if texIdx >= len(doc.Textures) {
		return nil
	}
	gltfTex := doc.Textures[texIdx]
	if gltfTex.Source == nil {
		return nil
	}
	return textures[int(*gltfTex.Source)]
}

// LoadGLTFSkinned reads a glTF or GLB document from fsys with its skin and
// animation data.
//
// Like LoadGLTF, this is a read followed by an upload of what the read
// produced (issue #75): ReadGLTFSkinned decodes the skeleton, the animations,
// the armature root transform and every primitive, and the step below only
// turns that into GPU resources.
func (r *Renderer) LoadGLTFSkinned(fsys fs.FS, name string) (*SkinnedModel, error) {
	rd, err := readGLTFSkinned(fsys, name)
	if err != nil {
		return nil, err
	}
	if err := r.uploadSkinnedModel(rd); err != nil {
		return nil, err
	}

	// Pass 2's static primitives go through the same extractPrimitive as
	// LoadGLTF and are just as silently missing their node's transform (see
	// the identical check there). Skinned primitives are excluded: their
	// placement goes through the skeleton/RootTransform, not this trap.
	warnUntransformedMeshNodes(name, rd.model.Nodes, staticMeshes(rd.model.Meshes))

	log.Printf("Loaded skinned model: %d meshes, %d joints, %d animations",
		len(rd.model.Meshes), len(rd.skinned.Skeleton.Joints), len(rd.skinned.Animations))

	return rd.skinned, nil
}

// staticMeshes is the non-skinned tail of a skinned read's Model.Meshes.
//
// readGLTFSkinned appends every skinned primitive before every static one, so
// this is the slice the untransformed-node check is allowed to look at -- it
// used to be written as model.Meshes[staticStart:] inside the loader, which
// said the same thing while depending on a local variable set two loops
// earlier. Filtering on Skinned says it in terms of the data.
func staticMeshes(meshes []ModelMesh) []ModelMesh {
	for i := range meshes {
		if !meshes[i].Skinned {
			return meshes[i:]
		}
	}
	return nil
}

// uploadSkinnedModel is LoadGLTFSkinned's upload half.
//
// It differs from uploadModel in exactly one place, on purpose: it does not
// call resolveMaterialMaps, so ModelMesh.Material is nil on everything a
// skinned load returns. That is what this loader has always done -- the
// skinned pipelines take a plain base-colour Texture, and 06-skinned's
// character reaches the skinned MATERIAL pipeline through a Material the
// example builds itself. Folding the two loops together would start
// allocating descriptor sets for skinned models that nothing binds.
func (r *Renderer) uploadSkinnedModel(rd *gltfRead) error {
	textures, err := r.uploadGLTFImages(rd.doc, rd.base)
	if err != nil {
		return fmt.Errorf("load gltf images: %w", err)
	}
	owned := newModelResources(rd.doc, textures)
	rd.model.owned = owned

	for i := range rd.model.Meshes {
		mm := &rd.model.Meshes[i]
		if err := r.uploadPrimitiveMesh(rd, i); err != nil {
			return err
		}
		owned.meshes = append(owned.meshes, mm.Mesh)
		if matIdx := rd.prims[i].material; matIdx >= 0 {
			mm.Texture = r.resolveBaseColorTexture(rd.doc, textures, matIdx)
		}
	}
	return nil
}

// extractSkinnedPrimitive is extractPrimitive's skinned counterpart: a
// separate function rather than a shared code path (SkinnedVertex is a
// different layout), so it needs its own uv bake -- see extractPrimitive's
// doc comment for why baking per call is safe even when primitives share an
// accessor.
func extractSkinnedPrimitive(doc *gltf.Document, prim *gltf.Primitive, uv uvAffine) ([]SkinnedVertex, []uint32, error) {
	posIdx, ok := prim.Attributes[gltf.POSITION]
	if !ok {
		return nil, nil, fmt.Errorf("primitive missing POSITION attribute")
	}
	positions, err := modeler.ReadPosition(doc, doc.Accessors[posIdx], nil)
	if err != nil {
		return nil, nil, fmt.Errorf("read positions: %w", err)
	}

	vertexCount := len(positions)

	var normals [][3]float32
	if idx, ok := prim.Attributes[gltf.NORMAL]; ok {
		normals, err = modeler.ReadNormal(doc, doc.Accessors[idx], nil)
		if err != nil {
			return nil, nil, fmt.Errorf("read normals: %w", err)
		}
	}

	var uvs [][2]float32
	if idx, ok := prim.Attributes[gltf.TEXCOORD_0]; ok {
		uvs, err = modeler.ReadTextureCoord(doc, doc.Accessors[idx], nil)
		if err != nil {
			return nil, nil, fmt.Errorf("read texcoords: %w", err)
		}
		applyUVTransform(uvs, uv)
	}

	var colors [][4]uint8
	if idx, ok := prim.Attributes[gltf.COLOR_0]; ok {
		colors, err = modeler.ReadColor(doc, doc.Accessors[idx], nil)
		if err != nil {
			return nil, nil, fmt.Errorf("read colors: %w", err)
		}
	}

	var joints [][4]uint16
	if idx, ok := prim.Attributes[gltf.JOINTS_0]; ok {
		joints, err = modeler.ReadJoints(doc, doc.Accessors[idx], nil)
		if err != nil {
			return nil, nil, fmt.Errorf("read joints: %w", err)
		}
	}

	var weights [][4]float32
	if idx, ok := prim.Attributes[gltf.WEIGHTS_0]; ok {
		weights, err = modeler.ReadWeights(doc, doc.Accessors[idx], nil)
		if err != nil {
			return nil, nil, fmt.Errorf("read weights: %w", err)
		}
	}

	vertices := make([]SkinnedVertex, vertexCount)
	for i := range vertices {
		vertices[i].Pos = positions[i]

		if normals != nil && i < len(normals) {
			vertices[i].Normal = normals[i]
		} else {
			vertices[i].Normal = [3]float32{0, 1, 0}
		}

		if uvs != nil && i < len(uvs) {
			vertices[i].UV = uvs[i]
		}

		if colors != nil && i < len(colors) {
			c := colors[i]
			vertices[i].Color = [3]float32{
				float32(c[0]) / 255.0,
				float32(c[1]) / 255.0,
				float32(c[2]) / 255.0,
			}
		} else {
			vertices[i].Color = [3]float32{1, 1, 1}
		}

		if joints != nil && i < len(joints) {
			vertices[i].Joints = joints[i]
		}

		if weights != nil && i < len(weights) {
			vertices[i].Weights = weights[i]
		}
	}

	if prim.Indices == nil {
		return nil, nil, fmt.Errorf("primitive has no indices")
	}
	indices, err := modeler.ReadIndices(doc, doc.Accessors[*prim.Indices], nil)
	if err != nil {
		return nil, nil, fmt.Errorf("read indices: %w", err)
	}

	return vertices, indices, nil
}

func buildSkeleton(doc *gltf.Document, skin *gltf.Skin) (*Skeleton, error) {
	jointCount := len(skin.Joints)
	if jointCount == 0 {
		return nil, fmt.Errorf("skin has no joints")
	}
	if jointCount > MaxJoints {
		return nil, fmt.Errorf("skin has %d joints, max is %d", jointCount, MaxJoints)
	}

	// Read inverse bind matrices
	var invBindMats [][4][4]float32
	if skin.InverseBindMatrices != nil {
		var err error
		invBindMats, err = modeler.ReadInverseBindMatrices(doc, doc.Accessors[*skin.InverseBindMatrices], nil)
		if err != nil {
			return nil, fmt.Errorf("read inverse bind matrices: %w", err)
		}
	}

	// Build node->joint mapping
	nodeToJoint := make(map[int]int)
	for ji, nodeIdx := range skin.Joints {
		nodeToJoint[nodeIdx] = ji
	}

	joints := make([]Joint, jointCount)
	for ji, nodeIdx := range skin.Joints {
		node := doc.Nodes[nodeIdx]
		joints[ji] = Joint{
			Name:           node.Name,
			NodeIndex:      nodeIdx,
			ParentIndex:    -1,
			LocalTransform: nodeLocalTransform(node),
		}
		if ji < len(invBindMats) {
			joints[ji].InverseBindMatrix = convertMat4(invBindMats[ji])
		} else {
			joints[ji].InverseBindMatrix = mgl32.Ident4()
		}

		// Build children from node hierarchy
		for _, childIdx := range node.Children {
			if cji, ok := nodeToJoint[childIdx]; ok {
				joints[ji].Children = append(joints[ji].Children, cji)
			}
		}
	}

	// Set parent indices
	for ji := range joints {
		for _, childJI := range joints[ji].Children {
			joints[childJI].ParentIndex = ji
		}
	}

	// Find root joints
	var rootJoints []int
	for ji := range joints {
		if joints[ji].ParentIndex == -1 {
			rootJoints = append(rootJoints, ji)
		}
	}

	// If all inverse bind matrices are identity, compute them from the
	// bind-pose world transforms. Some exporters omit IBMs, leaving them
	// as identity, which is incorrect when joints have non-zero transforms.
	allIdentity := true
	ident := mgl32.Ident4()
	for ji := range joints {
		for k := 0; k < 16; k++ {
			diff := joints[ji].InverseBindMatrix[k] - ident[k]
			if diff > 0.001 || diff < -0.001 {
				allIdentity = false
				break
			}
		}
		if !allIdentity {
			break
		}
	}
	if allIdentity {
		worldBind := make([]mgl32.Mat4, jointCount)
		queue := make([]int, 0, jointCount)
		queue = append(queue, rootJoints...)
		for len(queue) > 0 {
			ji := queue[0]
			queue = queue[1:]
			if joints[ji].ParentIndex >= 0 {
				worldBind[ji] = worldBind[joints[ji].ParentIndex].Mul4(joints[ji].LocalTransform)
			} else {
				worldBind[ji] = joints[ji].LocalTransform
			}
			queue = append(queue, joints[ji].Children...)
		}
		for ji := range joints {
			joints[ji].InverseBindMatrix = worldBind[ji].Inv()
		}
		log.Printf("Computed inverse bind matrices from bind-pose world transforms (%d joints)", jointCount)
	}

	return &Skeleton{
		Joints:      joints,
		RootJoints:  rootJoints,
		NodeToJoint: nodeToJoint,
	}, nil
}

func loadAnimations(doc *gltf.Document, skeleton *Skeleton) ([]AnimationClip, error) {
	var clips []AnimationClip

	for _, anim := range doc.Animations {
		clip := AnimationClip{Name: anim.Name}

		for _, ch := range anim.Channels {
			if ch.Target.Node == nil {
				continue
			}
			nodeIdx := *ch.Target.Node

			jointIdx, ok := skeleton.NodeToJoint[nodeIdx]
			if !ok {
				continue // channel targets a non-joint node
			}

			sampler := anim.Samplers[ch.Sampler]

			// Read timestamps
			timestampsRaw, err := modeler.ReadAccessor(doc, doc.Accessors[sampler.Input], nil)
			if err != nil {
				return nil, fmt.Errorf("read animation timestamps: %w", err)
			}
			timestamps, ok := timestampsRaw.([]float32)
			if !ok {
				return nil, fmt.Errorf("unexpected timestamp type %T", timestampsRaw)
			}

			// Track duration
			if len(timestamps) > 0 {
				last := timestamps[len(timestamps)-1]
				if last > clip.Duration {
					clip.Duration = last
				}
			}

			interp := InterpolationLinear
			switch sampler.Interpolation {
			case gltf.InterpolationStep:
				interp = InterpolationStep
			case gltf.InterpolationCubicSpline:
				interp = InterpolationCubicSpline
			}

			track := AnimationTrack{
				JointIndex:    jointIdx,
				Interpolation: interp,
				Timestamps:    timestamps,
			}

			outputRaw, err := modeler.ReadAccessor(doc, doc.Accessors[sampler.Output], nil)
			if err != nil {
				return nil, fmt.Errorf("read animation output: %w", err)
			}

			switch ch.Target.Path {
			case gltf.TRSTranslation:
				track.Property = PropertyTranslation
				vals, ok := outputRaw.([][3]float32)
				if !ok {
					return nil, fmt.Errorf("unexpected translation type %T", outputRaw)
				}
				track.Translations = make([]mgl32.Vec3, len(vals))
				for i, v := range vals {
					track.Translations[i] = mgl32.Vec3{v[0], v[1], v[2]}
				}

			case gltf.TRSRotation:
				track.Property = PropertyRotation
				vals, ok := outputRaw.([][4]float32)
				if !ok {
					return nil, fmt.Errorf("unexpected rotation type %T", outputRaw)
				}
				track.Rotations = make([]mgl32.Quat, len(vals))
				for i, q := range vals {
					// glTF quat: (x,y,z,w) -> mathgl: Quat{W, Vec3{x,y,z}}
					track.Rotations[i] = mgl32.Quat{W: q[3], V: mgl32.Vec3{q[0], q[1], q[2]}}
				}

			case gltf.TRSScale:
				track.Property = PropertyScale
				vals, ok := outputRaw.([][3]float32)
				if !ok {
					return nil, fmt.Errorf("unexpected scale type %T", outputRaw)
				}
				track.Scales = make([]mgl32.Vec3, len(vals))
				for i, v := range vals {
					track.Scales[i] = mgl32.Vec3{v[0], v[1], v[2]}
				}

			default:
				continue // skip morph weights etc.
			}

			clip.Tracks = append(clip.Tracks, track)
		}

		if len(clip.Tracks) > 0 {
			clips = append(clips, clip)
		}
	}

	return clips, nil
}

// computeArmatureRootTransform walks up the parent chain from the first skinned
// mesh node, collecting transforms from non-joint ancestor nodes (e.g. an
// "Armature" node with scale and rotation). This allows models exported in
// centimeters with Z-up (common from Mixamo/Blender) to render correctly.
func computeArmatureRootTransform(doc *gltf.Document, skeleton *Skeleton) mgl32.Mat4 {
	// Build child->parent map for all nodes.
	parentOf := make(map[int]int)
	for pi, node := range doc.Nodes {
		for _, ci := range node.Children {
			parentOf[ci] = pi
		}
	}

	// Find the first node with a skin reference.
	meshNodeIdx := -1
	for ni, node := range doc.Nodes {
		if node.Skin != nil {
			meshNodeIdx = ni
			break
		}
	}
	if meshNodeIdx < 0 {
		return mgl32.Ident4()
	}

	// Walk up the parent chain from the mesh node, collecting transforms from
	// non-joint ancestors. Stop when we reach a node that has no parent.
	var chain []mgl32.Mat4
	cur := meshNodeIdx
	for {
		pi, hasParent := parentOf[cur]
		if !hasParent {
			break
		}
		// Only include transforms from nodes outside the joint hierarchy.
		if _, isJoint := skeleton.NodeToJoint[pi]; !isJoint {
			chain = append(chain, nodeLocalTransform(doc.Nodes[pi]))
		}
		cur = pi
	}

	if len(chain) == 0 {
		return mgl32.Ident4()
	}

	// Compose in root-first order (chain was collected child-to-root).
	result := chain[len(chain)-1]
	for i := len(chain) - 2; i >= 0; i-- {
		result = result.Mul4(chain[i])
	}
	return result
}

func nodeLocalTransform(node *gltf.Node) mgl32.Mat4 {
	// Prefer TRS when available — some exporters set Matrix to identity AND
	// store actual transforms in TRS fields. glTF spec says they're mutually
	// exclusive, but we handle both gracefully.
	hasTRS := node.Translation != [3]float64{} || node.Rotation != [4]float64{} || node.Scale != [3]float64{}
	if hasTRS {
		t := node.TranslationOrDefault()
		r := node.RotationOrDefault()
		s := node.ScaleOrDefault()

		trans := mgl32.Translate3D(float32(t[0]), float32(t[1]), float32(t[2]))
		// glTF quat: (x,y,z,w)
		rot := mgl32.Quat{W: float32(r[3]), V: mgl32.Vec3{float32(r[0]), float32(r[1]), float32(r[2])}}.Mat4()
		scale := mgl32.Scale3D(float32(s[0]), float32(s[1]), float32(s[2]))

		return trans.Mul4(rot).Mul4(scale)
	}

	// Fall back to matrix if specified (column-major [16]float64).
	if node.Matrix != [16]float64{} {
		var m mgl32.Mat4
		for i := 0; i < 16; i++ {
			m[i] = float32(node.Matrix[i])
		}
		return m
	}

	return mgl32.Ident4()
}

// marshalExtras re-encodes a node's decoded `extras` value as raw JSON, or
// returns nil when the node had none.
//
// gltf.Node.Extras is typed `any` and qmuntal/gltf has no registered decoder
// for it (unlike KHR_lights_punctual's node reference, which decodes to a
// concrete Go type -- see extractLights), so by the time it reaches here it
// is already whatever encoding/json's generic decode produced: typically
// map[string]any for a JSON object, but a game's extras are not required to
// be one. Re-marshaling rather than type-asserting to map[string]any keeps
// ModelNode.Extras a faithful copy of whatever the document actually said,
// which is the whole point of not interpreting it (AGENTS.md rule 14).
//
// The error path only fires for a value encoding/json's own decoder could
// not have produced in the first place (a channel, a func) and is treated
// the same as "no extras" rather than failing the whole load over one node's
// application data the engine was never going to read anyway.
func marshalExtras(extras any) json.RawMessage {
	if extras == nil {
		return nil
	}
	data, err := json.Marshal(extras)
	if err != nil {
		return nil
	}
	return json.RawMessage(data)
}

// extractNodes builds a Model.Nodes from a glTF document's node graph: name,
// authored TRS, the local and world transforms, parent index and the mesh a
// node instances, if any.
//
// A pure function of *gltf.Document so it can be tested without a Renderer or
// a GPU -- LoadGLTF needs one, node extraction does not.
//
// glTF from the wild is not always valid, so this tolerates what a
// hand-authored or buggy-exporter document can throw at it without hanging or
// panicking: a nil node in doc.Nodes, an out-of-range child index, an
// out-of-range mesh index, and a parent/child cycle (see resolveWorld below).
func extractNodes(doc *gltf.Document) []ModelNode {
	n := len(doc.Nodes)
	if n == 0 {
		return nil
	}
	nodes := make([]ModelNode, n)
	for i := range nodes {
		nodes[i].Parent = -1
		nodes[i].Mesh = -1
	}

	// Parent indices come from walking every node's Children rather than
	// from anything glTF stores on the child, so an out-of-range entry is
	// just skipped -- there's nothing on the child side to fail.
	for pi, gn := range doc.Nodes {
		if gn == nil {
			continue
		}
		for _, ci := range gn.Children {
			if ci < 0 || ci >= n {
				continue
			}
			if nodes[ci].Parent == -1 {
				nodes[ci].Parent = pi
			}
		}
	}

	for i, gn := range doc.Nodes {
		if gn == nil {
			// A nil entry in doc.Nodes is invalid glTF, but the index still
			// has to exist so every other node's Parent/Mesh reference stays
			// valid. Ident4 local/world and no mesh is the inert answer.
			nodes[i].Local = mgl32.Ident4()
			continue
		}
		nodes[i].Name = gn.Name
		t := gn.TranslationOrDefault()
		r := gn.RotationOrDefault()
		s := gn.ScaleOrDefault()
		nodes[i].Translation = mgl32.Vec3{float32(t[0]), float32(t[1]), float32(t[2])}
		// glTF quat: (x,y,z,w) -> mathgl: Quat{W, Vec3{x,y,z}}, same
		// convention loadAnimations and nodeLocalTransform already use.
		nodes[i].Rotation = mgl32.Quat{W: float32(r[3]), V: mgl32.Vec3{float32(r[0]), float32(r[1]), float32(r[2])}}
		nodes[i].Scale = mgl32.Vec3{float32(s[0]), float32(s[1]), float32(s[2])}
		nodes[i].Local = nodeLocalTransform(gn)
		if gn.Mesh != nil {
			if mi := *gn.Mesh; mi >= 0 && mi < len(doc.Meshes) {
				nodes[i].Mesh = mi
			}
		}
		nodes[i].Extras = marshalExtras(gn.Extras)
	}

	resolveWorld(nodes)
	return nodes
}

// resolveWorld fills World on every node by composing Local down the parent
// chain, memoized so a document with many siblings under one root does the
// composition once per node rather than once per leaf's depth.
//
// state distinguishes "already computed" from "on the path currently being
// resolved", and the latter is how a parent/child cycle is caught: glTF from
// the wild is not always valid, and a node that is its own ancestor has to
// resolve to *something* rather than recurse forever. Landing on the node's
// own Local is arbitrary but terminates, which is the only property invalid
// input needs to have here.
func resolveWorld(nodes []ModelNode) {
	const (
		unresolved = iota
		resolving
		resolved
	)
	state := make([]uint8, len(nodes))

	var resolve func(i int) mgl32.Mat4
	resolve = func(i int) mgl32.Mat4 {
		switch state[i] {
		case resolved:
			return nodes[i].World
		case resolving:
			return nodes[i].Local
		}
		state[i] = resolving
		w := nodes[i].Local
		if p := nodes[i].Parent; p >= 0 && p < len(nodes) {
			w = resolve(p).Mul4(nodes[i].Local)
		}
		nodes[i].World = w
		state[i] = resolved
		return w
	}
	for i := range nodes {
		resolve(i)
	}
}

// meshOwnerNodes returns, for each doc.Meshes index, the first node (in
// doc.Nodes order) that instances it via node.Mesh, or -1 if no node does.
//
// "First" rather than "all": a doc mesh can be instanced by several nodes,
// but a ModelMesh is drawn once regardless, and the one thing this index is
// for -- finding the node a mesh-space helper needs -- only needs one of
// them. Node-index order rather than scene-graph order because it is
// deterministic without requiring the document to declare a default scene,
// which LoadGLTF does not require either.
func meshOwnerNodes(doc *gltf.Document) []int {
	owner := make([]int, len(doc.Meshes))
	for i := range owner {
		owner[i] = -1
	}
	for ni, gn := range doc.Nodes {
		if gn == nil || gn.Mesh == nil {
			continue
		}
		mi := *gn.Mesh
		if mi < 0 || mi >= len(owner) {
			continue
		}
		if owner[mi] == -1 {
			owner[mi] = ni
		}
	}
	return owner
}

// isIdentityTransform reports whether m is the identity matrix, within a
// small epsilon for the float64 (glTF) -> float32 (mgl32) round trip.
func isIdentityTransform(m mgl32.Mat4) bool {
	const eps = 1e-6
	ident := mgl32.Ident4()
	for i := range m {
		d := m[i] - ident[i]
		if d > eps || d < -eps {
			return false
		}
	}
	return true
}

// untransformedMeshNodes returns the names of nodes that instance a mesh in
// meshes while carrying a non-identity World transform.
//
// LoadGLTF draws every primitive in mesh-local space (fact recorded in
// docs/agents/models.md): it never applies a node's transform to the
// vertices it decodes. That is invisible exactly when it does not matter --
// an identity instancing node -- and silently wrong otherwise, so this is
// the pure check behind the log line LoadGLTF and LoadGLTFSkinned each emit
// once per load. A mesh with Node == -1 (no instancing node at all) cannot
// be wrong this way and is skipped, and each offending node is named once
// even if several primitives share it.
func untransformedMeshNodes(nodes []ModelNode, meshes []ModelMesh) []string {
	var names []string
	seen := make(map[int]bool)
	for i := range meshes {
		ni := meshes[i].Node
		if ni < 0 || ni >= len(nodes) || seen[ni] {
			continue
		}
		seen[ni] = true
		if !isIdentityTransform(nodes[ni].World) {
			names = append(names, nodes[ni].Name)
		}
	}
	return names
}

// untransformedNodeListCap is how many names LoadGLTF/LoadGLTFSkinned's
// log line prints before falling back to "and N more". Sized so the line
// stays one screen-width-ish line for the #46 case (usually one or two
// nodes) while not turning into forty names for a level file, where every
// mesh node is expected to trip this check.
const untransformedNodeListCap = 5

// cappedNodeList formats names for the untransformed-node log line: the
// names themselves when there are few, or the first cap of them plus a count
// of the rest when there are not.
//
// Split out from the log line itself so the exact wording can be pinned by a
// test without rendering anything -- this is the piece the "cap the list of
// names" requirement in issue #66 is actually about, and the log line around
// it is not something a test can observe.
func cappedNodeList(names []string, limit int) string {
	if len(names) <= limit {
		return fmt.Sprintf("%v", names)
	}
	return fmt.Sprintf("%v and %d more", names[:limit], len(names)-limit)
}

// convertMat4 converts a [4][4]float32 from qmuntal/gltf to mgl32.Mat4.
// qmuntal/gltf decodes column-major glTF data into [row][col] layout,
// so we transpose to get mgl32's column-major [16]float32.
func convertMat4(m [4][4]float32) mgl32.Mat4 {
	return mgl32.Mat4{
		m[0][0], m[1][0], m[2][0], m[3][0], // column 0
		m[0][1], m[1][1], m[2][1], m[3][1], // column 1
		m[0][2], m[1][2], m[2][2], m[3][2], // column 2
		m[0][3], m[1][3], m[2][3], m[3][3], // column 3
	}
}

// emissiveStrength reads KHR_materials_emissive_strength, defaulting to 1.
//
// The extension is not registered with the glTF decoder, so it arrives as raw
// JSON rather than as a typed struct. Registering it would mean depending on an
// ext package that does not exist for this one; parsing the single float here is
// smaller than that, and every failure path returns the glTF default rather than
// an error, because a model with a malformed extension should still load looking
// like a model without it.
//
// This matters more than it looks: the extension exists precisely to let a
// material emit above 1, which is the range the HDR target was added to hold.
// Dropping it silently clamps every authored glow back to merely bright.
func emissiveStrength(mat *gltf.Material) float32 {
	raw, ok := mat.Extensions["KHR_materials_emissive_strength"]
	if !ok {
		return 1
	}
	msg, ok := raw.(json.RawMessage)
	if !ok {
		return 1
	}
	var ext struct {
		EmissiveStrength *float64 `json:"emissiveStrength"`
	}
	if err := json.Unmarshal(msg, &ext); err != nil || ext.EmissiveStrength == nil {
		return 1
	}
	if *ext.EmissiveStrength < 0 {
		return 1
	}
	return float32(*ext.EmissiveStrength)
}
