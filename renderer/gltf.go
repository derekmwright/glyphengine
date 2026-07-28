package renderer

import (
	"bytes"
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
)

// ModelMesh pairs a GPU mesh with an optional texture loaded from a glTF file.
type ModelMesh struct {
	Mesh        *Mesh
	Texture     *Texture
	Skinned     bool       // true if this mesh uses skinned vertices
	DoubleSided bool       // glTF material says render both faces
	BaseColor   [3]float32 // material base color factor (default white)
	Metallic    float32    // 0 = dielectric, 1 = metal (from glTF PBR)
	Roughness   float32    // 0 = mirror, 1 = matte (from glTF PBR)
}

// Model holds all meshes loaded from a single glTF/GLB file.
type Model struct {
	Meshes []ModelMesh
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
func (r *Renderer) LoadGLTF(fsys fs.FS, name string) (*Model, error) {
	doc, base, err := openGLTF(fsys, name)
	if err != nil {
		return nil, err
	}

	// Load all images referenced by the document
	textures, err := r.loadGLTFImages(doc, base)
	if err != nil {
		return nil, fmt.Errorf("load gltf images: %w", err)
	}

	var model Model

	for _, mesh := range doc.Meshes {
		for _, prim := range mesh.Primitives {
			if prim.Mode != gltf.PrimitiveTriangles {
				continue
			}

			vertices, indices, err := r.extractPrimitive(doc, prim)
			if err != nil {
				return nil, fmt.Errorf("extract primitive from mesh %q: %w", mesh.Name, err)
			}

			// Reverse winding order: glTF uses CCW, our engine uses CW
			for i := 0; i+2 < len(indices); i += 3 {
				indices[i+1], indices[i+2] = indices[i+2], indices[i+1]
			}

			// Create GPU mesh — choose uint16 vs uint32 based on vertex count
			var gpuMesh *Mesh
			if len(vertices) <= 65535 {
				idx16 := make([]uint16, len(indices))
				for i, v := range indices {
					idx16[i] = uint16(v)
				}
				gpuMesh, err = r.CreateIndexedMesh(vertices, idx16)
			} else {
				gpuMesh, err = r.CreateIndexedMesh32(vertices, indices)
			}
			if err != nil {
				return nil, fmt.Errorf("create mesh for %q: %w", mesh.Name, err)
			}

			// Resolve texture and PBR material from glTF
			var tex *Texture
			baseColor := [3]float32{1, 1, 1}
			var metallic, roughness float32
			var doubleSided bool
			roughness = 0.5
			if prim.Material != nil {
				tex = r.resolveBaseColorTexture(doc, textures, *prim.Material)
				baseColor, metallic, roughness = resolveMaterial(doc, int(*prim.Material))
				doubleSided = doc.Materials[*prim.Material].DoubleSided
			}

			model.Meshes = append(model.Meshes, ModelMesh{
				Mesh:        gpuMesh,
				Texture:     tex,
				DoubleSided: doubleSided,
				BaseColor:   baseColor,
				Metallic:    metallic,
				Roughness:   roughness,
			})
		}
	}

	return &model, nil
}

// extractPrimitive reads vertex attributes and indices from a glTF primitive.
func (r *Renderer) extractPrimitive(doc *gltf.Document, prim *gltf.Primitive) ([]Vertex, []uint32, error) {
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

// loadGLTFImages decodes all images in the document and uploads them as GPU textures.
// Returns a map from image index to Texture.
func (r *Renderer) loadGLTFImages(doc *gltf.Document, base fs.FS) (map[int]*Texture, error) {
	textures := make(map[int]*Texture)

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

		decoded, err := decodeImage(imgBytes)
		if err != nil {
			return nil, fmt.Errorf("decode image %d (%s): %w", i, img.Name, err)
		}

		tex, err := r.CreateTexture(decoded.pixels, decoded.width, decoded.height)
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

// decodeImage decodes PNG/JPEG bytes into RGBA pixel data.
func decodeImage(data []byte) (*decodedImage, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	// Convert to NRGBA
	nrgba := image.NewNRGBA(bounds)
	draw.Draw(nrgba, bounds, img, bounds.Min, draw.Src)

	return &decodedImage{
		pixels: nrgba.Pix,
		width:  w,
		height: h,
	}, nil
}

// resolveMaterial extracts PBR properties from a glTF material.
// Returns baseColor (default white), metallic (default 0), roughness (default 0.5).
func resolveMaterial(doc *gltf.Document, materialIdx int) (baseColor [3]float32, metallic, roughness float32) {
	baseColor = [3]float32{1, 1, 1}
	roughness = 0.5
	if materialIdx >= len(doc.Materials) {
		return
	}
	mat := doc.Materials[materialIdx]
	if mat.PBRMetallicRoughness == nil {
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

// resolveBaseColorTexture finds the base color texture for a material, if any.
func (r *Renderer) resolveBaseColorTexture(doc *gltf.Document, textures map[int]*Texture, materialIdx int) *Texture {
	if materialIdx >= len(doc.Materials) {
		return nil
	}
	mat := doc.Materials[materialIdx]
	if mat.PBRMetallicRoughness == nil {
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
func (r *Renderer) LoadGLTFSkinned(fsys fs.FS, name string) (*SkinnedModel, error) {
	doc, base, err := openGLTF(fsys, name)
	if err != nil {
		return nil, err
	}

	textures, err := r.loadGLTFImages(doc, base)
	if err != nil {
		return nil, fmt.Errorf("load gltf images: %w", err)
	}

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

	var model Model
	loadedMeshes := make(map[int]bool) // track which doc.Meshes indices we've loaded

	// Pass 1: Load skinned meshes from nodes with a Skin reference.
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

			vertices, indices, err := extractSkinnedPrimitive(doc, prim)
			if err != nil {
				return nil, fmt.Errorf("extract skinned primitive from mesh %q: %w", mesh.Name, err)
			}

			for i := 0; i+2 < len(indices); i += 3 {
				indices[i+1], indices[i+2] = indices[i+2], indices[i+1]
			}

			var gpuMesh *Mesh
			if len(vertices) <= 65535 {
				idx16 := make([]uint16, len(indices))
				for i, v := range indices {
					idx16[i] = uint16(v)
				}
				gpuMesh, err = r.CreateSkinnedIndexedMesh(vertices, idx16)
			} else {
				gpuMesh, err = r.CreateSkinnedIndexedMesh32(vertices, indices)
			}
			if err != nil {
				return nil, fmt.Errorf("create skinned mesh for %q: %w", mesh.Name, err)
			}

			var tex *Texture
			baseColor := [3]float32{1, 1, 1}
			var metallic, roughness float32
			roughness = 0.5
			if prim.Material != nil {
				tex = r.resolveBaseColorTexture(doc, textures, *prim.Material)
				baseColor, metallic, roughness = resolveMaterial(doc, int(*prim.Material))
			}

			model.Meshes = append(model.Meshes, ModelMesh{
				Mesh:      gpuMesh,
				Texture:   tex,
				Skinned:   true,
				BaseColor: baseColor,
				Metallic:  metallic,
				Roughness: roughness,
			})
		}
	}

	// Pass 2: Load non-skinned meshes from nodes without a Skin reference.
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

			vertices, indices, err := r.extractPrimitive(doc, prim)
			if err != nil {
				return nil, fmt.Errorf("extract static primitive from mesh %q: %w", mesh.Name, err)
			}

			for i := 0; i+2 < len(indices); i += 3 {
				indices[i+1], indices[i+2] = indices[i+2], indices[i+1]
			}

			var gpuMesh *Mesh
			if len(vertices) <= 65535 {
				idx16 := make([]uint16, len(indices))
				for i, v := range indices {
					idx16[i] = uint16(v)
				}
				gpuMesh, err = r.CreateIndexedMesh(vertices, idx16)
			} else {
				gpuMesh, err = r.CreateIndexedMesh32(vertices, indices)
			}
			if err != nil {
				return nil, fmt.Errorf("create static mesh for %q: %w", mesh.Name, err)
			}

			var tex *Texture
			baseColor := [3]float32{1, 1, 1}
			var metallic, roughness float32
			roughness = 0.5
			if prim.Material != nil {
				tex = r.resolveBaseColorTexture(doc, textures, *prim.Material)
				baseColor, metallic, roughness = resolveMaterial(doc, int(*prim.Material))
			}

			model.Meshes = append(model.Meshes, ModelMesh{
				Mesh:      gpuMesh,
				Texture:   tex,
				Skinned:   false,
				BaseColor: baseColor,
				Metallic:  metallic,
				Roughness: roughness,
			})
		}
	}

	// Compute root transform from the parent chain of the first skinned mesh
	// node, collecting transforms from nodes above the joint hierarchy (e.g.
	// an Armature node with scale 0.01 and a Z-up to Y-up rotation).
	rootTransform := computeArmatureRootTransform(doc, skeleton)

	log.Printf("Loaded skinned model: %d meshes, %d joints, %d animations",
		len(model.Meshes), len(skeleton.Joints), len(animations))

	return &SkinnedModel{
		Model:         model,
		Skeleton:      skeleton,
		Animations:    animations,
		RootTransform: rootTransform,
	}, nil
}

func extractSkinnedPrimitive(doc *gltf.Document, prim *gltf.Primitive) ([]SkinnedVertex, []uint32, error) {
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
