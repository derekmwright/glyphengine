package glyphengine

import (
	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/ecs"
	"github.com/derekmwright/glyphengine/renderer"
)

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

// MaterialRef links an entity to a GPU texture for textured rendering.
// Terrain, when set, routes the entity through the terrain splat pipeline
// (multi-texture blend) instead of the single-texture lit pipeline.
type MaterialRef struct {
	Texture *renderer.Texture
	Terrain *renderer.TerrainMaterial
}

// Emissive is a tag component that bypasses lighting (always full-bright).
type Emissive struct{}

// DoubleSided is a tag component that disables backface culling for this entity.
type DoubleSided struct{}

// Highlighted is a tag component that additively brightens an entity's color.
type Highlighted struct{}

// Hidden is a tag component that prevents an entity from being rendered.
type Hidden struct{}

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
	MaterialRef    *ecs.Store[MaterialRef]
	Color          *ecs.Store[Color]

	// Render flags
	Hidden       *ecs.Store[Hidden]
	Highlighted  *ecs.Store[Highlighted]
	DoubleSided  *ecs.Store[DoubleSided]
	Emissive     *ecs.Store[Emissive]
	NoCastShadow *ecs.Store[NoCastShadow]
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
		MaterialRef:         ecs.NewStore[MaterialRef](w),
		Color:               ecs.NewStore[Color](w),
		Hidden:              ecs.NewStore[Hidden](w),
		Highlighted:         ecs.NewStore[Highlighted](w),
		DoubleSided:         ecs.NewStore[DoubleSided](w),
		Emissive:            ecs.NewStore[Emissive](w),
		NoCastShadow:        ecs.NewStore[NoCastShadow](w),
	}
}
