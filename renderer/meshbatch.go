package renderer

import (
	"fmt"
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// SetMeshRangeBatching opts into batching adjacent opaque, plain-lit ranges
// of one arena with identical material state. Other draws retain their paths.
// Bounds and transforms remain per range, including each shadow view. Drivers
// without multiDrawIndirect and drawIndirectFirstInstance use direct draws.
// Like other renderer mutation, call on the renderer thread between frames.
func (r *Renderer) SetMeshRangeBatching(enabled bool) { r.rangeBatching = enabled }

const meshBatchViews = 1 + ShadowCascades + 6

type meshBatchFrame struct {
	vertices, commands lodBuffer
	capacity           int
	groups             []*meshRangeBatch
	draws              []RenderObject
	multi              bool
	limit              int
}

func (f *meshBatchFrame) destroy(r *Renderer) { f.vertices.destroy(r); f.commands.destroy(r) }

type meshRangeBatch struct {
	set      InstanceSet
	sources  []RenderObject
	commands []uint32
	view     int
	multi    bool
	limit    int
}

func batchable(d *RenderObject) bool {
	return d.Mesh != nil && d.Mesh.owner != nil && !d.Mesh.destroyed && d.Instances == nil && d.InstancesLOD == nil && d.Joints == nil && d.TerrainMat == nil && d.Material == nil && d.Water == nil && !d.IsTranslucent()
}
func sameMeshBatch(a, b *RenderObject) bool {
	return batchable(b) && a.Mesh.owner == b.Mesh.owner && a.Texture == b.Texture && a.Color == b.Color && a.Roughness == b.Roughness && a.Metallic == b.Metallic && a.Emissive == b.Emissive && a.DoubleSided == b.DoubleSided && a.NoCastShadow == b.NoCastShadow
}

// Called only after this slot's fence. Buffer growth retires the old allocation;
// steady frames reuse both mapped buffers and Go scratch.
func (r *Renderer) prepareMeshBatches(draws []RenderObject, frame int) ([]RenderObject, error) {
	f := &r.rangeBatchFrames[frame]
	if len(draws) > f.capacity {
		features := r.instanceDriver.GetPhysicalDeviceFeatures(r.physicalDevice)
		props, err := r.instanceDriver.GetPhysicalDeviceProperties(r.physicalDevice)
		if err != nil {
			return nil, err
		}
		capacity := max(len(draws), f.capacity*2)
		var v, c lodBuffer
		if err = v.create(r, capacity*meshInstanceSize, core1_0.BufferUsageVertexBuffer, true); err != nil {
			v.destroy(r)
			return nil, fmt.Errorf("mesh batches: instances: %w", err)
		}
		if err = c.create(r, capacity*meshBatchViews*20, core1_0.BufferUsageIndirectBuffer, true); err != nil {
			c.destroy(r)
			v.destroy(r)
			return nil, fmt.Errorf("mesh batches: commands: %w", err)
		}
		oldV, oldC := f.vertices, f.commands
		if oldV.buffer.Handle() != 0 {
			r.DeferDestroy(func() { oldV.destroy(r); oldC.destroy(r) })
		}
		f.vertices, f.commands, f.capacity = v, c, capacity
		f.multi = features.MultiDrawIndirect && features.DrawIndirectFirstInstance
		f.limit = max(1, props.Limits.MaxDrawIndirectCount)
	}
	f.draws = f.draws[:0]
	group := 0
	for start := 0; start < len(draws); {
		end := start + 1
		if batchable(&draws[start]) {
			for end < len(draws) && sameMeshBatch(&draws[start], &draws[end]) {
				end++
			}
		}
		if end-start < 2 {
			f.draws = append(f.draws, draws[start])
			start = end
			continue
		}
		if group == len(f.groups) {
			f.groups = append(f.groups, &meshRangeBatch{})
		}
		b := f.groups[group]
		group++
		b.sources = draws[start:end]
		b.view = 0
		b.multi = f.multi
		b.limit = f.limit
		b.commands = unsafe.Slice((*uint32)(f.commands.mapped), f.capacity*meshBatchViews*5)[start*meshBatchViews*5 : end*meshBatchViews*5]
		b.set = InstanceSet{Mesh: draws[start].Mesh, batch: b, buffer: f.vertices.buffer, offset: start * meshInstanceSize, indirect: f.commands.buffer, indirectOffset: start * meshBatchViews * 20, count: end - start}
		instances := unsafe.Slice((*MeshInstance)(f.vertices.mapped), f.capacity)[start:end]
		for i, d := range b.sources {
			instances[i] = MeshInstance{Model: d.Model, Tint: [4]float32{1, 1, 1, 1}}
		}
		d := draws[start]
		d.Instances = &b.set
		d.ShadowOnly = false
		f.draws = append(f.draws, d)
		start = end
	}
	return f.draws, nil
}

func (b *meshRangeBatch) record(d core1_0.DeviceDriver, stats *RenderStats, cmd core1_0.CommandBuffer, frustum *Frustum) {
	if b.view >= meshBatchViews {
		panic("mesh batch: too many views in one frame")
	}
	base := b.view * len(b.sources) * 5
	b.view++
	words := b.commands[base:]
	n := 0
	for i, src := range b.sources {
		if frustum == nil {
			if src.ShadowOnly {
				continue
			}
		} else if x, y, z, r := src.worldBoundSphere(); r > 0 && !frustum.SphereInFrustum(x, y, z, r) {
			continue
		}
		m := src.Mesh
		words[n*5], words[n*5+1], words[n*5+2], words[n*5+3], words[n*5+4] = uint32(m.IndexCount), 1, m.firstIndex, uint32(m.vertexOffset), uint32(i)
		stats.Instances++
		stats.Triangles += m.IndexCount / 3
		n++
	}
	if n == 0 {
		return
	}
	d.CmdBindIndexBuffer(cmd, b.set.Mesh.indexBuffer, 0, b.set.Mesh.indexType)
	if b.multi {
		for first := 0; first < n; {
			count := min(n-first, b.limit)
			d.CmdDrawIndexedIndirect(cmd, b.set.indirect, b.set.indirectOffset+(base+first*5)*4, count, 20)
			stats.DrawCalls++
			first += count
		}
	} else {
		for i := 0; i < n; i++ {
			w := words[i*5:]
			d.CmdDrawIndexed(cmd, int(w[0]), 1, w[2], int(int32(w[3])), w[4])
			stats.DrawCalls++
		}
	}
}
