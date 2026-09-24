package glyphengine

import (
	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/ecs"
	"github.com/derekmwright/glyphengine/renderer"
)

// Entity is a handle to a thing in the world.
//
// Aliased from the ecs package so a game that only touches the engine's own
// API does not have to import ecs for a single type. It is the same type, so
// the two are interchangeable wherever both are in scope.
type Entity = ecs.Entity

// Transform holds position, rotation, and scale for an entity.
type Transform struct {
	Position mgl32.Vec3
	Rotation mgl32.Vec3 // Euler angles in radians
	Scale    mgl32.Vec3
}

// ModelMatrix computes the model matrix from the transform.
func (t *Transform) ModelMatrix() mgl32.Mat4 {
	m := mgl32.Ident4()
	m = m.Mul4(mgl32.Translate3D(t.Position.X(), t.Position.Y(), t.Position.Z()))
	m = m.Mul4(mgl32.HomogRotate3DY(t.Rotation.Y()))
	m = m.Mul4(mgl32.HomogRotate3DX(t.Rotation.X()))
	m = m.Mul4(mgl32.HomogRotate3DZ(t.Rotation.Z()))
	m = m.Mul4(mgl32.Scale3D(t.Scale.X(), t.Scale.Y(), t.Scale.Z()))
	return m
}

// PrevTransform holds an entity's Transform as it was at the start of the
// current tick, so rendering can interpolate between ticks instead of showing
// simulation steps.
//
// Written by the engine at the top of Scene.Tick when Scene.Interpolate is on.
// Games do not set this; they call Scene.ClearInterpolation after a teleport.
type PrevTransform Transform

// Velocity holds linear velocity in world units per second.
type Velocity struct {
	Vec mgl32.Vec3
}

// MeshRef links an entity to a GPU mesh resource.
type MeshRef struct {
	Mesh      *renderer.Mesh
	Metallic  float32 // 0 = dielectric, 1 = metal (from glTF PBR)
	Roughness float32 // 0 = mirror, 1 = matte (from glTF PBR)
}

// Color holds an RGB tint for an entity.
type Color struct {
	R, G, B float32
}

// MaterialRef links an entity to the GPU resources that describe its surface.
// Exactly one of the three is used, in the order they are listed here.
type MaterialRef struct {
	// PBR routes the entity through the material pipeline: albedo plus normal,
	// metallic-roughness, and occlusion maps. Build one with
	// Renderer.CreateMaterial. It takes precedence over Texture, whose job the
	// material's own albedo slot does.
	//
	// MeshRef.Metallic and .Roughness still apply — the maps multiply them, so
	// they keep working as per-object factors.
	//
	// Ignored on skinned meshes; see renderer.RenderObject.Material.
	PBR *renderer.Material

	// Texture is a single albedo map, lit as one uniform material.
	Texture *renderer.Texture

	// Terrain routes the entity through the terrain splat pipeline
	// (multi-texture blend) instead of the single-texture lit pipeline.
	Terrain *renderer.TerrainMaterial
}

// Water routes an entity's mesh through the water pipeline: Gerstner wave
// displacement, a Fresnel-weighted sky reflection, and refraction of whatever
// was drawn behind it. Build the mesh with Engine.CreateWaterMesh, and pass the
// same options here so the shader agrees with the geometry.
type Water struct {
	Options WaterOptions
}

// Emissive is a tag component that bypasses lighting (always full-bright).
type Emissive struct{}

// DoubleSided is a tag component that disables backface culling for this entity.
type DoubleSided struct{}

// Highlighted is a tag component that additively brightens an entity's color.
type Highlighted struct{}

// Hidden is a tag component that prevents an entity from being rendered.
type Hidden struct{}

// InstancedMesh draws one mesh at many placements in a single draw call.
//
// It is for the case a builder-style game hits early: 900 identical habitat
// domes are 900 draw calls of a 200-triangle mesh on the ordinary MeshRef path
// — no GPU cost worth the name, and all of the cost in CPU-side command
// recording. One InstancedMesh is one CmdDrawIndexed, one push-constant upload
// and one frustum test, however many placements it holds.
//
//	set, err := e.Renderer().CreateInstanceSet(domeMesh, 1000, placements)
//	colony := e.Spawn()
//	e.C.InstancedMesh.Set(colony, &glyph.InstancedMesh{Set: set})
//
// The entity needs no Transform: every placement carries its own model matrix,
// which is what separates this from merging props into one mesh per chunk. Add
// a placement with Renderer.UpdateInstanceSet rather than rebuilding geometry.
//
// Color on the entity tints the whole set; MeshInstance.Tint tints one
// placement. Emissive, DoubleSided and NoCastShadow all apply to the set as a
// whole. Translucent does not — there is no blended instanced pipeline, and the
// set stays opaque rather than silently losing its placements.
//
// Set culls as one group; LOD culls each placement and selects distance levels.
// See docs/agents/instancing.md and docs/agents/lod-instancing.md for the costs.
type InstancedMesh struct {
	Set *renderer.InstanceSet
	LOD *renderer.InstanceSetLOD // mutually exclusive with Set; per-placement culling and distance levels
}

// Translucent draws an entity blended over the scene instead of opaque.
//
// It is a component rather than a field on MeshRef because MeshRef's zero value
// has to stay opaque. An Alpha field there would make every existing MeshRef
// read as fully transparent unless zero were special-cased to mean one, and
// "0 means opaque except when it means invisible" is the kind of quiet
// surprise that costs an afternoon.
//
//	ghost := e.Spawn()
//	e.C.Transform.Set(ghost, &glyph.Transform{Position: at, Scale: one})
//	e.C.MeshRef.Set(ghost, &glyph.MeshRef{Mesh: dome})
//	e.C.Translucent.Set(ghost, &glyph.Translucent{Alpha: 0.4})
//
// Translucent entities are drawn after everything opaque, back to front, and do
// not cast shadows — a placement preview that threw a solid shadow would read as
// a real building. They compose with Emissive and DoubleSided.
//
// Not supported on entities that also carry a MaterialRef.PBR or a skinned
// SkeletonRef: those route through their own pipelines, which have no blended
// variant, so the engine leaves such an entity opaque rather than silently
// dropping its maps or its skinning. See docs/agents/translucency.md.
type Translucent struct {
	// Alpha is opacity from 0 to 1. At or below 0 the entity is not drawn at
	// all; at or above 1 it is drawn opaque, through the ordinary lit path.
	Alpha float32
}

// NoCastShadow is a tag component that excludes an entity from the shadow pass.
// The entity still receives shadows but does not cast them.
type NoCastShadow struct{}

// Static is a tag component marking an entity as world geometry that never
// moves — rocks, walls, buildings. Static entities go into Scene.StaticGrid,
// which is rebuilt on demand rather than every tick.
//
// The tag is load-bearing, not just an optimization: the parallel movement
// phase runs convex-hull narrow-phase tests against *live* Transforms while
// AABB queries read a frozen snapshot, which is only sound because hull
// entities do not move during that phase. Attaching ConvexHullCollider to an
// entity that moves breaks that invariant.
type Static struct{}

// Components holds the engine's typed component stores. Access via Scene.C
// (e.g. scene.C.Transform.Get(entity)).
//
// This is deliberately only what the engine itself reads: spatial and physics
// queries, draw-list building, and animation sampling. Game-specific stores
// belong in a struct the game owns — the engine never sees them, so the
// engine/game boundary is enforced by the compiler rather than by convention.
type Components struct {
	// Physics & spatial
	Transform           *ecs.Store[Transform]
	PrevTransform       *ecs.Store[PrevTransform]
	Velocity            *ecs.Store[Velocity]
	Collider            *ecs.Store[Collider]
	ConvexHullCollider  *ecs.Store[ConvexHullCollider]
	CharacterController *ecs.Store[CharacterController]
	Static              *ecs.Store[Static]

	// Animation & rendering
	AnimationState *ecs.Store[AnimationState]
	SkeletonRef    *ecs.Store[SkeletonRef]
	MeshRef        *ecs.Store[MeshRef]
	Water          *ecs.Store[Water]
	MaterialRef    *ecs.Store[MaterialRef]
	Color          *ecs.Store[Color]

	// Render flags
	Hidden        *ecs.Store[Hidden]
	Highlighted   *ecs.Store[Highlighted]
	DoubleSided   *ecs.Store[DoubleSided]
	Emissive      *ecs.Store[Emissive]
	Translucent   *ecs.Store[Translucent]
	InstancedMesh *ecs.Store[InstancedMesh]
	NoCastShadow  *ecs.Store[NoCastShadow]
}

// NewComponents creates the engine component stores and registers them with
// the given World. Call once per World, before spawning any entities.
func NewComponents(w *ecs.World) *Components {
	return &Components{
		Transform:           ecs.NewStore[Transform](w),
		PrevTransform:       ecs.NewStore[PrevTransform](w),
		Velocity:            ecs.NewStore[Velocity](w),
		Collider:            ecs.NewStore[Collider](w),
		ConvexHullCollider:  ecs.NewStore[ConvexHullCollider](w),
		CharacterController: ecs.NewStore[CharacterController](w),
		Static:              ecs.NewStore[Static](w),
		AnimationState:      ecs.NewStore[AnimationState](w),
		SkeletonRef:         ecs.NewStore[SkeletonRef](w),
		MeshRef:             ecs.NewStore[MeshRef](w),
		Water:               ecs.NewStore[Water](w),
		MaterialRef:         ecs.NewStore[MaterialRef](w),
		Color:               ecs.NewStore[Color](w),
		Hidden:              ecs.NewStore[Hidden](w),
		Highlighted:         ecs.NewStore[Highlighted](w),
		DoubleSided:         ecs.NewStore[DoubleSided](w),
		Emissive:            ecs.NewStore[Emissive](w),
		Translucent:         ecs.NewStore[Translucent](w),
		InstancedMesh:       ecs.NewStore[InstancedMesh](w),
		NoCastShadow:        ecs.NewStore[NoCastShadow](w),
	}
}
