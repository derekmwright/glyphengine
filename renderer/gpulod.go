package renderer

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"
	"unsafe"

	"github.com/derekmwright/glyphengine/shaders"
	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// Buckets occupy disjoint Capacity-sized ranges of a device-local arena. This
// keeps descriptor usage at four storage buffers, within Vulkan 1.0's minimum.
type gpuLOD struct {
	placements                                   lodBuffer
	output, commands, readback, uniform, scratch [maxFramesInFlight]lodBuffer
	setLayout                                    core1_0.DescriptorSetLayout
	sets                                         []core1_0.DescriptorSet
	pipeline                                     core1_0.Pipeline
	layout                                       core1_0.PipelineLayout
	pc                                           [64]float32
	lastFrame                                    int
	readValid                                    [maxFramesInFlight]bool
	copy                                         [1]core1_0.BufferCopy
}

type lodBuffer struct {
	buffer core1_0.Buffer
	memory core1_0.DeviceMemory
	mapped unsafe.Pointer
	size   int
}

func (b *lodBuffer) create(r *Renderer, size int, usage core1_0.BufferUsageFlags, host bool) error {
	b.size = size
	props := core1_0.MemoryPropertyDeviceLocal
	if host {
		props = core1_0.MemoryPropertyHostVisible | core1_0.MemoryPropertyHostCoherent
	}
	var err error
	b.buffer, b.memory, err = r.createBuffer(size, usage, props)
	if err == nil && host {
		b.mapped, _, err = r.deviceDriver.MapMemory(b.memory, 0, size, 0)
	}
	return err
}
func (b *lodBuffer) destroy(r *Renderer) {
	if b.mapped != nil {
		r.deviceDriver.UnmapMemory(b.memory)
	}
	if b.buffer.Handle() != 0 {
		r.deviceDriver.DestroyBuffer(b.buffer, nil)
	}
	if b.memory.Handle() != 0 {
		r.deviceDriver.FreeMemory(b.memory, nil)
	}
	*b = lodBuffer{}
}

func (s *InstanceSetLOD) createGPU() error {
	r := s.owner
	g := &gpuLOD{}
	s.gpu = g
	props, err := r.instanceDriver.GetPhysicalDeviceProperties(r.physicalDevice)
	if err != nil {
		return err
	}
	if s.capacity > props.Limits.MaxStorageBufferRange/(len(s.buckets)*meshInstanceSize) {
		return fmt.Errorf("LOD: GPU buckets exceed MaxStorageBufferRange")
	}
	if (s.capacity+63)/64 > props.Limits.MaxComputeWorkGroupCount[0] {
		return fmt.Errorf("LOD: capacity exceeds compute dispatch limit")
	}
	if err = g.placements.create(r, s.capacity*meshInstanceSize, core1_0.BufferUsageStorageBuffer|core1_0.BufferUsageTransferDst, false); err != nil {
		return err
	}
	bindings := make([]core1_0.DescriptorSetLayoutBinding, 5)
	for i := range bindings {
		kind := core1_0.DescriptorTypeStorageBuffer
		if i == 3 {
			kind = core1_0.DescriptorTypeUniformBuffer
		}
		bindings[i] = core1_0.DescriptorSetLayoutBinding{Binding: i, DescriptorCount: 1, DescriptorType: kind, StageFlags: core1_0.StageCompute}
	}
	g.setLayout, _, err = r.deviceDriver.CreateDescriptorSetLayout(nil, core1_0.DescriptorSetLayoutCreateInfo{Bindings: bindings})
	if err != nil {
		return err
	}
	g.pipeline, g.layout, err = createAppComputePipeline(r.deviceDriver, shaders.LODSelectCompSpv, g.setLayout)
	if err != nil {
		return err
	}
	g.sets, _, err = r.deviceDriver.AllocateDescriptorSets(core1_0.DescriptorSetAllocateInfo{DescriptorPool: r.descriptorPool, SetLayouts: []core1_0.DescriptorSetLayout{g.setLayout, g.setLayout}})
	if err != nil {
		return err
	}
	r.liveDescriptorSets += len(g.sets)
	for f := range maxFramesInFlight {
		if err = g.output[f].create(r, s.capacity*meshInstanceSize*len(s.buckets), core1_0.BufferUsageStorageBuffer|core1_0.BufferUsageVertexBuffer, false); err != nil {
			return err
		}
		if err = g.commands[f].create(r, (len(s.buckets)*5+1)*4, core1_0.BufferUsageStorageBuffer|core1_0.BufferUsageIndirectBuffer|core1_0.BufferUsageTransferSrc, false); err != nil {
			return err
		}
		if err = g.readback[f].create(r, g.commands[f].size, core1_0.BufferUsageTransferDst, true); err != nil {
			return err
		}
		if err = g.uniform[f].create(r, 128, core1_0.BufferUsageUniformBuffer, true); err != nil {
			return err
		}
		if err = g.scratch[f].create(r, (s.capacity*6+((s.capacity+63)/64)*9)*4, core1_0.BufferUsageStorageBuffer, false); err != nil {
			return err
		}
		for i, b := range []*lodBuffer{&g.placements, &g.output[f], &g.commands[f], &g.uniform[f], &g.scratch[f]} {
			if err = r.deviceDriver.UpdateDescriptorSets([]core1_0.WriteDescriptorSet{{DstSet: g.sets[f], DstBinding: i, DescriptorType: bindings[i].DescriptorType, BufferInfo: []core1_0.DescriptorBufferInfo{{Buffer: b.buffer, Range: b.size}}}}, nil); err != nil {
				return err
			}
		}
		for i := range s.buckets {
			b := &s.buckets[i].frames[f]
			*b = InstanceSet{lod: s, lodLevel: i, capacity: s.capacity, buffer: g.output[f].buffer, offset: i * s.capacity * meshInstanceSize, indirect: g.commands[f].buffer, indirectOffset: i * 20}
			if i < len(s.levels) {
				b.Mesh = s.levels[i].Mesh
			}
		}
	}
	g.copy[0] = core1_0.BufferCopy{Size: g.commands[0].size}
	return nil
}

func (g *gpuLOD) destroy(r *Renderer) {
	r.freeDescriptorSets(g.sets...)
	g.sets = nil
	if g.pipeline.Handle() != 0 {
		r.deviceDriver.DestroyPipeline(g.pipeline, nil)
	}
	if g.layout.Handle() != 0 {
		r.deviceDriver.DestroyPipelineLayout(g.layout, nil)
	}
	if g.setLayout.Handle() != 0 {
		r.deviceDriver.DestroyDescriptorSetLayout(g.setLayout, nil)
	}
	g.placements.destroy(r)
	for f := range maxFramesInFlight {
		g.output[f].destroy(r)
		g.commands[f].destroy(r)
		g.readback[f].destroy(r)
		g.uniform[f].destroy(r)
		g.scratch[f].destroy(r)
	}
}

// Fixed-clock 25-lod-forest, 3600 placements, RX 7900 XTX, 1280x720,
// task lod CPU/GPU/CPU/GPU (200 frames each, 2026-09-24): CPU selection plus
// upload 0.282 -> 0.0365 ms; GPU selection 0.035 ms. Only bucket parameters
// and retired counts are touched here; placement work stays in the shader.
func (s *InstanceSetLOD) prepareGPU(frustum Frustum, eye [3]float32, frame int) {
	g := s.gpu
	s.readGPUCounts(frame)
	for i := range frustum.Planes {
		copy(g.pc[i*4:], frustum.Planes[i][:])
	}
	copy(g.pc[24:], eye[:])
	g.pc[27] = s.radius
	g.pc[28] = s.fadeWidth
	g.pc[29] = math.Float32frombits(uint32(len(s.levels)))
	g.pc[30] = math.Float32frombits(uint32(len(s.buckets)))
	g.pc[31] = math.Float32frombits(uint32(len(s.placements)))
	g.pc[32] = math.Float32frombits(uint32(s.capacity))
	g.pc[34] = math.Float32frombits(uint32(s.dropped))
	params := unsafe.Slice((*byte)(g.uniform[frame].mapped), 128)
	clear(params)
	for i, l := range s.levels {
		binary.LittleEndian.PutUint32(params[i*4:], math.Float32bits(l.MaxDistance))
	}
	for i := range s.buckets {
		n := 6
		if i < len(s.levels) {
			m := s.levels[i].Mesh
			binary.LittleEndian.PutUint32(params[64+i*4:], m.firstIndex)
			binary.LittleEndian.PutUint32(params[96+i*4:], uint32(m.vertexOffset))
			n = m.VertexCount
			if m.IndexCount > 0 {
				n = m.IndexCount
			}
		}
		binary.LittleEndian.PutUint32(params[32+i*4:], uint32(n))
		s.buckets[i].frames[frame].count = s.counts[i]
	}
}

func (s *InstanceSetLOD) gpuActive(*graphFrame) bool {
	return !s.destroyed && s.prepared == s.owner.lodGeneration
}
func (s *InstanceSetLOD) dispatchGPU(c *graphFrame, mode uint32) {
	start := time.Now()
	defer func() { s.owner.lastLODCull += time.Since(start) }()
	g := s.gpu
	c.driver.CmdBindPipeline(c.cmd, core1_0.PipelineBindPointCompute, g.pipeline)
	c.scratch.bindDescriptorSets(c.driver, c.cmd, core1_0.PipelineBindPointCompute, g.layout, 0, g.sets[c.frame])
	c.scratch.pc = g.pc
	n := max(1, (len(s.placements)+63)/64)
	c.scratch.pc[33] = math.Float32frombits(mode)
	if mode == 1 {
		n = 1
	}
	c.scratch.pushConstants(c.driver, c.cmd, g.layout, core1_0.StageCompute)
	c.driver.CmdDispatch(c.cmd, n, 1, 1)
}

func (s *InstanceSetLOD) readGPUCounts(frame int) {
	g := s.gpu
	if !g.readValid[frame] {
		return
	}
	counts := unsafe.Slice((*uint32)(g.readback[frame].mapped), g.readback[frame].size/4)
	for i := range s.counts {
		s.counts[i] = int(counts[i*5+1])
	}
	s.culled = int(counts[len(s.buckets)*5])
}
func (s *InstanceSetLOD) latestGPUCounts() {
	g := s.gpu
	if !s.destroyed && g.readValid[g.lastFrame] {
		if _, err := s.owner.deviceDriver.WaitForFences(true, common.NoTimeout, s.owner.sync.inFlight[g.lastFrame]); err != nil {
			panic(err)
		}
		s.readGPUCounts(g.lastFrame)
	}
}
