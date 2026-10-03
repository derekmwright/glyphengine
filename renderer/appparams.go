package renderer

import (
	"fmt"
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// AppParamBytes is the largest per-pass uniform block AppPassDesc.Params or
// AppComputeDesc.Params may ask for.
//
// 4096 is not a guess about what a pass needs; it is the size the renderer
// already proves every device it starts on can address. shaderParameterOffset
// refuses a device whose MaxUniformBufferRange is below ShaderParameterBytes,
// which is this same 4096, so a block of this size is reachable through one
// descriptor wherever the game's global block is. Vulkan's own guaranteed
// minimum is 16384, so the cap is conservative against the spec and exact
// against the check already in the startup path.
const AppParamBytes = 4096

// appParamsBinding is where a pass's private block lands in its own set.
//
// 12 for both pass kinds, past every binding either one can already declare: a
// graphics pass's four pass-input samplers end at 3 and a dispatch's storage
// buffers end at 11. One number for both kinds is one rule for a shader author
// to remember, and the gap in the graphics layout costs nothing -- Vulkan
// binding numbers need not be contiguous, and the pool charges per descriptor
// a layout declares rather than per binding index.
//
// A pass that declares Params therefore holds four uniform-buffer descriptors
// in its vertex and fragment stages: the texture set's unused one, the light
// set's engine block and application block, and this. Vulkan guarantees 12 per
// stage, so there is no device question to ask here -- the 3-per-stage floor
// shaderParameterOffset enforces is about the engine's own layouts, which this
// does not change.
const appParamsBinding = 12

// appParams is one pass's private uniform block: the bytes the application
// staged, plus a host-visible buffer per frame slot to copy them into.
//
// The staging copy is the whole mechanism. SetParams writes only staged, and
// the copy into a slot's mapped buffer happens in flushAppFrameBindings, after
// DrawFrame has waited on that slot's fence -- the same two-step the game's
// global block uses, for the same reason. Writing the mapped buffer from the
// setter would write memory a frame still in flight is reading, and the result
// is not a stale block but half of two.
type appParams struct {
	staged  []byte
	buffers [maxFramesInFlight]core1_0.Buffer
	memory  [maxFramesInFlight]core1_0.DeviceMemory
	mapped  [maxFramesInFlight][]byte
}

// validateAppParams enforces the declared size. 0 is the ordinary case and
// means the pass has no block at all.
func validateAppParams(size int, fail func(string, string) error) error {
	if size < 0 || size > AppParamBytes || size%16 != 0 {
		return fail("Params", fmt.Sprintf("size %d must be a multiple of 16, at most %d", size, AppParamBytes))
	}
	return nil
}

// SetParams copies data into this pass's own uniform block for subsequent
// DrawFrame calls. Call on the renderer/frame thread, as with every other pass
// method.
//
// It does not write in-flight GPU memory: DrawFrame copies into the frame
// slot's buffer after waiting for that slot's fence, so a frame still being
// read by the GPU never sees a half-written block. Data persists across frames
// and swapchain recreation until replaced; nil clears the whole block.
//
// Supply little-endian std140 bytes padded to a multiple of 16; the unused tail
// is zeroed. The caller owns field packing, exactly as with SetPushConstants
// and SetShaderParameters -- no Go struct layout or shader reflection is
// inferred. Oversized or unaligned input returns an error naming the pass and
// leaves the previous value intact. Allocates nothing.
func (p *AppPass) SetParams(data []byte) error {
	if p.params == nil {
		return fmt.Errorf("app pass %q: Params: the pass declares no uniform block", p.desc.Name)
	}
	if len(data) > len(p.params.staged) || len(data)%16 != 0 {
		return fmt.Errorf("app pass %q: Params: size %d must be a multiple of 16, at most the declared %d",
			p.desc.Name, len(data), len(p.params.staged))
	}
	clear(p.params.staged)
	copy(p.params.staged, data)
	return nil
}

// createAppParams gives p one host-visible uniform buffer per frame slot.
//
// p.params is attached before anything can fail, so release() frees whatever
// was made -- CreateAppPass and CreateAppCompute both unwind through it.
func (r *Renderer) createAppParams(p *AppPass, size int) error {
	a := &appParams{staged: make([]byte, size)}
	p.params = a
	for i := range a.buffers {
		buf, mem, err := r.createBuffer(size, core1_0.BufferUsageUniformBuffer,
			core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent)
		if err != nil {
			return fmt.Errorf("app pass %q: Params: %w", p.desc.Name, err)
		}
		a.buffers[i], a.memory[i] = buf, mem
		ptr, _, err := r.deviceDriver.MapMemory(mem, 0, size, 0)
		if err != nil {
			return fmt.Errorf("app pass %q: Params: map slot %d: %w", p.desc.Name, i, err)
		}
		a.mapped[i] = unsafe.Slice((*byte)(ptr), size)
		// A slot the application has not written yet reads as zeros rather than
		// as whatever the allocation happened to contain.
		clear(a.mapped[i])
	}
	return nil
}

// release unmaps and frees the per-slot buffers. Safe on a partially created
// block, which is how a failed CreateAppPass unwinds.
func (a *appParams) release(d core1_0.DeviceDriver) {
	if a == nil {
		return
	}
	for i := range a.buffers {
		if a.mapped[i] != nil {
			d.UnmapMemory(a.memory[i])
			a.mapped[i] = nil
		}
		if a.memory[i].Handle() != 0 {
			d.FreeMemory(a.memory[i], nil)
			a.memory[i] = core1_0.DeviceMemory{}
		}
		if a.buffers[i].Handle() != 0 {
			d.DestroyBuffer(a.buffers[i], nil)
			a.buffers[i] = core1_0.Buffer{}
		}
	}
}

// flushAppParams copies the staged block into the slot this frame will record.
// The other slot is not touched: it may still be in flight.
func (p *AppPass) flushAppParams(frame int) {
	if p.params != nil {
		copy(p.params.mapped[frame], p.params.staged)
	}
}

// writeAppParams points the just-waited frame slot's descriptor at that slot's
// buffer. Scratch slices survive the interface call, as in writeAppSampler, so
// this costs no allocation on the frame path.
func (r *Renderer) writeAppParams(p *AppPass, set core1_0.DescriptorSet, frame int) error {
	r.appBufferInfos[0] = core1_0.DescriptorBufferInfo{Buffer: p.params.buffers[frame], Range: len(p.params.staged)}
	r.appWrites[0] = core1_0.WriteDescriptorSet{DstSet: set, DstBinding: appParamsBinding,
		DescriptorType: core1_0.DescriptorTypeUniformBuffer, BufferInfo: r.appBufferInfos[:]}
	return r.deviceDriver.UpdateDescriptorSets(r.appWrites[:1], nil)
}
