package renderer

import (
	"fmt"
	"math"
	"slices"
	"unsafe"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// ImpostorAtlas is an eight-view bake, at 45 degree intervals about local Y
// and 12 degrees above the horizon. Albedo and alpha come from the shared grass
// bake shaders; the billboard receives current lighting and shadows at draw time.
// The source mesh and texture are borrowed only during BakeImpostor.
type ImpostorAtlas struct {
	owner     *Renderer
	atlas     *grassImpostor
	texture   Texture
	center    [3]float32
	radius    float32
	size      int
	destroyed bool
}

// BakeImpostor runs synchronously on the renderer thread. Size is the tile edge
// in pixels; the atlas is 8*size by size. A nil texture samples white, with the
// mesh's vertex colours retained. The mesh must have finite, positive bounds.
func (r *Renderer) BakeImpostor(mesh *Mesh, texture *Texture, size int) (*ImpostorAtlas, error) {
	if mesh == nil || mesh.destroyed || !finiteLOD(mesh.BoundRadius) || mesh.BoundRadius <= 0 {
		return nil, fmt.Errorf("impostor: a live mesh with bounds is required")
	}
	for _, c := range mesh.BoundCenter {
		if !finiteLOD(c) {
			return nil, fmt.Errorf("impostor: nonfinite bounds")
		}
	}
	if size < 4 || size > 2048 {
		return nil, fmt.Errorf("impostor: tile size must be between 4 and 2048")
	}
	if texture != nil && texture.destroyed {
		return nil, fmt.Errorf("impostor: destroyed texture")
	}
	a := &ImpostorAtlas{owner: r, center: mesh.BoundCenter, radius: mesh.BoundRadius * 1.02, size: size,
		atlas: &grassImpostor{cells: 8, extent: core1_0.Extent2D{Width: 8 * size, Height: size}}}
	if err := r.allocateBakeAtlas(a.atlas, true); err != nil {
		return nil, err
	}
	if err := r.recordImpostorBake(a, mesh, texture); err != nil {
		a.destroy()
		return nil, err
	}
	a.texture.DescriptorSet = a.atlas.set
	r.impostorAtlases = append(r.impostorAtlases, a)
	return a, nil
}

// DestroyImpostorAtlas defers release over frames in flight. Remove every set
// referencing the atlas from the draw list first. Nil-safe and idempotent.
func (r *Renderer) DestroyImpostorAtlas(a *ImpostorAtlas) {
	if a == nil || a.destroyed {
		return
	}
	if a.owner != r {
		panic("impostor: destroy of foreign atlas")
	}
	a.destroyed = true
	r.DeferDestroy(func() {
		a.destroy()
		if i := slices.Index(r.impostorAtlases, a); i >= 0 {
			r.impostorAtlases = slices.Delete(r.impostorAtlases, i, i+1)
		}
	})
}

func (a *ImpostorAtlas) destroy() { a.atlas.destroy(a.owner) }

func (r *Renderer) recordImpostorBake(a *ImpostorAtlas, mesh *Mesh, texture *Texture) error {
	cmd, err := r.beginSingleTimeCommands()
	if err != nil {
		return err
	}
	imp := a.atlas
	if err = imp.target.begin(r.deviceDriver, r.cmdScratch.dynamic, cmd); err != nil {
		return err
	}
	r.deviceDriver.CmdBindPipeline(cmd, core1_0.PipelineBindPointGraphics, imp.pipeline)
	if texture == nil {
		texture = r.fallbackTexture
	}
	r.deviceDriver.CmdBindDescriptorSets(cmd, core1_0.PipelineBindPointGraphics, r.pipelineLayout, 0, []core1_0.DescriptorSet{texture.DescriptorSet}, nil)
	r.deviceDriver.CmdBindVertexBuffers(cmd, 0, []core1_0.Buffer{mesh.vertexBuffer}, []int{0})
	if mesh.IndexCount > 0 {
		r.deviceDriver.CmdBindIndexBuffer(cmd, mesh.indexBuffer, 0, mesh.indexType)
	}
	for i := 0; i < 8; i++ {
		r.deviceDriver.CmdSetViewport(cmd, core1_0.Viewport{X: float32(i * a.size), Width: float32(a.size), Height: float32(a.size), MinDepth: 0, MaxDepth: 1})
		r.deviceDriver.CmdSetScissor(cmd, core1_0.Rect2D{Offset: core1_0.Offset2D{X: i * a.size}, Extent: core1_0.Extent2D{Width: a.size, Height: a.size}})
		angle := float64(i) * math.Pi / 4
		const elevation = 12 * math.Pi / 180
		c := mgl32.Vec3(a.center)
		dir := mgl32.Vec3{float32(math.Sin(angle) * math.Cos(elevation)), float32(math.Sin(elevation)), float32(math.Cos(angle) * math.Cos(elevation))}
		view := mgl32.LookAtV(c.Add(dir.Mul(a.radius*2)), c, mgl32.Vec3{0, 1, 0})
		// RH view looks down -Z. Map z=-r..-3r to 1..0 (reverse-Z).
		proj := mgl32.Mat4{0: 1 / a.radius, 5: -1 / a.radius, 10: 1 / (2 * a.radius), 14: 1.5, 15: 1}
		vp := proj.Mul4(view)
		var pc [pushConstantSize / 4]float32
		copy(pc[:16], vp[:])
		pc[16], pc[21], pc[26], pc[31] = 1, 1, 1, 1
		pc[32], pc[33], pc[34] = 1, 1, 1
		r.deviceDriver.CmdPushConstants(cmd, r.pipelineLayout, core1_0.StageVertex|core1_0.StageFragment, 0, unsafe.Slice((*byte)(unsafe.Pointer(&pc[0])), pushConstantSize))
		if mesh.IndexCount > 0 {
			r.deviceDriver.CmdDrawIndexed(cmd, mesh.IndexCount, 1, 0, 0, 0)
		} else {
			r.deviceDriver.CmdDraw(cmd, mesh.VertexCount, 1, 0, 0)
		}
	}
	if err := imp.target.end(r.deviceDriver, r.cmdScratch.dynamic, cmd); err != nil {
		return err
	}
	return r.endSingleTimeCommands(cmd)
}
