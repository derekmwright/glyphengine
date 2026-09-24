package renderer

import (
	"errors"
	"fmt"
	"testing"
	"unsafe"

	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/core/v3/loader"
)

type bufferTestInstance struct{ resizeFakeInstanceDriver }

func (bufferTestInstance) GetPhysicalDeviceProperties(core1_0.PhysicalDevice) (*core1_0.PhysicalDeviceProperties, error) {
	return &core1_0.PhysicalDeviceProperties{Limits: &core1_0.PhysicalDeviceLimits{MaxStorageBufferRange: 1 << 27, MaxComputeWorkGroupCount: [3]int{65535, 65535, 65535}}}, nil
}
func (bufferTestInstance) GetPhysicalDeviceMemoryProperties(core1_0.PhysicalDevice) *core1_0.PhysicalDeviceMemoryProperties {
	return &core1_0.PhysicalDeviceMemoryProperties{MemoryTypes: []core1_0.MemoryType{{PropertyFlags: core1_0.MemoryPropertyDeviceLocal | core1_0.MemoryPropertyHostVisible | core1_0.MemoryPropertyHostCoherent}}}
}

type bufferTestDriver struct {
	*resizeFakeDriver
	copies      int
	mapped      [][]byte
	descriptors []core1_0.DescriptorBufferInfo
}

func (d *bufferTestDriver) CreateBuffer(_ *loader.AllocationCallbacks, info core1_0.BufferCreateInfo) (core1_0.Buffer, common.VkResult, error) {
	if d.shouldFail("CreateBuffer") {
		return core1_0.Buffer{}, core1_0.VKErrorUnknown, errInjected
	}
	d.created["Buffer"]++
	return d.h.buffer(), core1_0.VKSuccess, nil
}
func (d *bufferTestDriver) DestroyBuffer(core1_0.Buffer, *loader.AllocationCallbacks) {
	d.destroyed["Buffer"]++
}
func (d *bufferTestDriver) GetBufferMemoryRequirements(core1_0.Buffer) *core1_0.MemoryRequirements {
	return &core1_0.MemoryRequirements{Size: 1 << 20, MemoryTypeBits: 1}
}
func (d *bufferTestDriver) BindBufferMemory(core1_0.Buffer, core1_0.DeviceMemory, int) (common.VkResult, error) {
	if d.shouldFail("BindBufferMemory") {
		return core1_0.VKErrorUnknown, errInjected
	}
	return core1_0.VKSuccess, nil
}
func (d *bufferTestDriver) MapMemory(_ core1_0.DeviceMemory, _, size int, _ core1_0.MemoryMapFlags) (unsafe.Pointer, common.VkResult, error) {
	if d.shouldFail("MapMemory") {
		return nil, core1_0.VKErrorUnknown, errInjected
	}
	b := make([]byte, size)
	d.mapped = append(d.mapped, b)
	return unsafe.Pointer(&b[0]), core1_0.VKSuccess, nil
}
func (d *bufferTestDriver) UnmapMemory(core1_0.DeviceMemory) {}
func (d *bufferTestDriver) CmdCopyBuffer(core1_0.CommandBuffer, core1_0.Buffer, core1_0.Buffer, ...core1_0.BufferCopy) error {
	if d.shouldFail("CmdCopyBuffer") {
		return errInjected
	}
	d.copies++
	return nil
}

func (d *bufferTestDriver) AllocateCommandBuffers(core1_0.CommandBufferAllocateInfo) ([]core1_0.CommandBuffer, common.VkResult, error) {
	if d.shouldFail("AllocateCommandBuffers") {
		return nil, core1_0.VKErrorUnknown, errInjected
	}
	d.created["CommandBuffer"]++
	return []core1_0.CommandBuffer{d.h.commandBuffer()}, core1_0.VKSuccess, nil
}
func (d *bufferTestDriver) FreeCommandBuffers(buffers ...core1_0.CommandBuffer) {
	d.destroyed["CommandBuffer"] += len(buffers)
}
func (d *bufferTestDriver) BeginCommandBuffer(core1_0.CommandBuffer, core1_0.CommandBufferBeginInfo) (common.VkResult, error) {
	if d.shouldFail("BeginCommandBuffer") {
		return core1_0.VKErrorUnknown, errInjected
	}
	return core1_0.VKSuccess, nil
}
func (d *bufferTestDriver) EndCommandBuffer(core1_0.CommandBuffer) (common.VkResult, error) {
	if d.shouldFail("EndCommandBuffer") {
		return core1_0.VKErrorUnknown, errInjected
	}
	return core1_0.VKSuccess, nil
}
func (d *bufferTestDriver) CmdPipelineBarrier(core1_0.CommandBuffer, core1_0.PipelineStageFlags, core1_0.PipelineStageFlags, core1_0.DependencyFlags, []core1_0.MemoryBarrier, []core1_0.BufferMemoryBarrier, []core1_0.ImageMemoryBarrier) error {
	if d.shouldFail("CmdPipelineBarrier") {
		return errInjected
	}
	return nil
}
func (d *bufferTestDriver) UpdateDescriptorSets(writes []core1_0.WriteDescriptorSet, _ []core1_0.CopyDescriptorSet) error {
	for _, w := range writes {
		d.descriptors = append(d.descriptors, w.BufferInfo...)
	}
	return nil
}

func bufferFixture() (*Renderer, *bufferTestDriver) {
	d := &bufferTestDriver{resizeFakeDriver: newResizeFakeDriver()}
	r := newResizeFixture(d.resizeFakeDriver, 3)
	r.deviceDriver = d
	r.instanceDriver = bufferTestInstance{}
	return r, d
}
func bufferBalance(t *testing.T, d *bufferTestDriver) {
	t.Helper()
	for kind, n := range d.created {
		if n != d.destroyed[kind] {
			t.Errorf("%s: created %d destroyed %d", kind, n, d.destroyed[kind])
		}
	}
}

func TestStorageBufferLifetimeAndBindings(t *testing.T) {
	r, d := bufferFixture()
	b, err := r.CreateStorageBuffer(StorageBufferDesc{Name: "history", Size: 64, History: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.buffers) != 2 || d.copies != 2 {
		t.Fatal("history not initialized twice")
	}
	if err = r.UploadStorageBuffer(b, []byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	if d.copies != 4 {
		t.Fatal("prefix upload missed history instance")
	}
	if r.UploadStorageBuffer(b, make([]byte, 65)) == nil {
		t.Fatal("oversized upload accepted")
	}
	c := &AppCompute{desc: AppComputeDesc{Buffers: []*StorageBuffer{b}}}
	for f := range 2 {
		if err = r.flushComputeOutputs(c, core1_0.DescriptorSet{}, f); err != nil {
			t.Fatal(err)
		}
	}
	for i, v := range d.descriptors {
		if v.Buffer != b.buffers[i] || v.Range != 64 {
			t.Fatal("wrong frame buffer binding")
		}
	}
	p := &AppPass{r: r, compute: c}
	c.pass = p
	r.appPasses = []*AppPass{p}
	r.DestroyStorageBuffer(b)
	r.DestroyStorageBuffer(b)
	if !p.destroyed || len(r.appPasses) != 0 || len(b.buffers) != 2 {
		t.Fatal("buffer users must detach before deferred release")
	}
	if r.UploadStorageBuffer(b, nil) == nil {
		t.Fatal("destroyed upload accepted")
	}
	r.flushAllDeferred()
	r.destroyAppResources()
	bufferBalance(t, d)
}

func TestStorageBufferValidation(t *testing.T) {
	r, d := bufferFixture()
	for _, size := range []int{0, -1, 1 << 28} {
		if _, err := r.CreateStorageBuffer(StorageBufferDesc{Size: size}); err == nil {
			t.Fatalf("accepted size %d", size)
		}
	}
	if d.created["Buffer"] != 0 {
		t.Fatal("invalid descriptor allocated GPU memory")
	}
	b := &StorageBuffer{r: r, desc: StorageBufferDesc{Name: "valid", Size: 64}}
	desc := AppComputeDesc{Stage: StageBeforeScene, Comp: computeCode(t), Buffers: []*StorageBuffer{b}}
	if err := r.validateAppCompute(desc); err != nil {
		t.Fatal(err)
	}
	for _, bs := range [][]*StorageBuffer{{nil}, {b, b}, {b, b, b, b, b}, {{r: &Renderer{}}}, {{r: r, destroyed: true}}} {
		desc.Buffers = bs
		if err := r.validateAppCompute(desc); err == nil {
			t.Fatal("accepted invalid buffers")
		}
	}
	if r.UploadStorageBuffer(nil, nil) == nil || r.UploadStorageBuffer(&StorageBuffer{r: &Renderer{}}, nil) == nil {
		t.Fatal("accepted foreign/nil upload")
	}
}

// Verified break: omitting storage-buffer memory release reports four memory
// allocations but only two frees before initialization was batched; the current
// batched form reports three allocations and one free. Partial cases fail too.
func TestStorageBufferAndGPULODCreationUnwind(t *testing.T) {
	for _, gpu := range []bool{false, true} {
		create := func(r *Renderer) error {
			if !gpu {
				_, err := r.CreateStorageBuffer(StorageBufferDesc{Size: 80, History: true})
				return err
			}
			_, err := r.CreateInstanceSetLOD(InstanceSetLODDesc{GPU: true, Levels: []LODLevel{{Mesh: &Mesh{VertexCount: 3}, MaxDistance: 10}}, Capacity: 4}, nil)
			return err
		}
		cleanup := func(r *Renderer) {
			for _, s := range r.lodSets {
				s.destroy()
			}
			r.destroyAppResources()
		}
		r, control := bufferFixture()
		if err := create(r); err != nil {
			t.Fatal(err)
		}
		cleanup(r)
		bufferBalance(t, control)
		for _, call := range []string{"CreateBuffer", "AllocateMemory", "BindBufferMemory", "MapMemory", "CreateDescriptorSetLayout", "CreateShaderModule", "CreatePipelineLayout", "CreateComputePipelines", "AllocateDescriptorSets", "AllocateCommandBuffers", "BeginCommandBuffer", "CmdPipelineBarrier", "CmdCopyBuffer", "EndCommandBuffer"} {
			for at := 1; at <= control.calls[call]; at++ {
				t.Run(fmt.Sprintf("GPU=%v/%s/%d", gpu, call, at), func(t *testing.T) {
					r, d := bufferFixture()
					d.failCall, d.failAt = call, at
					err := create(r)
					if !errors.Is(err, errInjected) {
						t.Fatalf("error %v", err)
					}
					cleanup(r)
					bufferBalance(t, d)
				})
			}
		}
	}
}
