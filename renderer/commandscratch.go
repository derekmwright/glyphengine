package renderer

import (
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// commandScratch is the long-lived storage recordCommandBuffer and its
// callees fill and hand to the driver, so recording a frame's few hundred
// draws stops paying for a fresh descriptor-set slice, vertex-buffer slice,
// clear-value slice, barrier slice or push-constant array on every one of
// them. It lives on the Renderer and is threaded through as one pointer --
// recordCommandBuffer already took far too many parameters to give each of
// these its own name in the list.
//
// Every method here hands the driver a slice or array that lives inside this
// struct rather than one built fresh at the call site, which is safe because
// vkngwrapper's device driver copies every argument slice into a per-call
// cgoparam arena before the real vkCmd* call returns -- see
// CmdBindVertexBuffers, CmdBindDescriptorSets, CmdPushConstants and friends in
// vkngwrapper/core's internal/impl1_0/command_buffer.go, which cgoparam.Malloc
// a C array, populate it from the Go slice, issue the call, and only then
// defer cgoparam.ReturnAlloc. Nothing the driver is handed outlives the call
// that received it, so overwriting a field the instant its call returns is
// safe.
//
// NOT SAFE FOR CONCURRENT OR RE-ENTRANT USE, and not meant to be: recording
// one command buffer is one goroutine working strictly sequentially, front to
// back. Every helper below fills a field and immediately issues the driver
// call that reads it, before doing anything else that could refill the same
// field -- fill and consume are never separated by another call anywhere in
// this recorder. That is the whole safety argument, and it is also the rule
// for anyone adding to this recorder: a caller that needs to hold a filled
// field across a call to something else that also touches this scratch (an
// outer loop assembling descriptor sets, then calling a helper that assembles
// its own before the outer bind runs) must not reuse these fields -- it needs
// its own storage, or the two uses will corrupt each other exactly the way
// TestRecordCommandBufferStreamIsUnchanged is built to catch.
//
// pc is reset to zero before every fill rather than only overwritten, even at
// call sites that happen to write every one of its 64 floats. Several do not
// -- the instanced path never writes the model columns because the shader
// takes the model from a per-instance attribute, and relied on `var pc [64]float32`
// starting at zero for those slots to read as an identity matrix's zero
// off-diagonal. A shared array does not get that for free; resetting it here
// is what keeps every push-constant upload byte-identical to what a fresh
// stack array would have contained, which is what a game's push constants
// have always been.
type commandScratch struct {
	pc [pushConstantSize / 4]float32

	// shadowPC is the depth-only pass's smaller block: light-space MVP plus
	// model, 128 bytes. Every shadow draw fills all 32 floats itself, so this
	// needs no reset.
	shadowPC [32]float32

	// descSets is sized for the widest bind this recorder makes: a skinned
	// material draw's set 0 (material) + set 1 (joints) + set 2 (shadow).
	descSets [3]core1_0.DescriptorSet
	// vertexBufs is sized for the widest bind: a mesh plus its instance buffer.
	vertexBufs [2]core1_0.Buffer

	viewport1 [1]core1_0.Viewport
	scissor1  [1]core1_0.Rect2D

	// clearValues is sized for the main pass's widest case: colour, depth, and
	// the MSAA resolve attachment Vulkan requires a (Loadop DontCare, but
	// still counted) clear value for.
	clearValues [3]core1_0.ClearValue

	// colorClear backs the main pass's colour clear. core1_0.ClearValue is an
	// INTERFACE, so passing a core1_0.ClearValueFloat VALUE to beginRenderPass
	// boxes it -- unlike DescriptorSet/Buffer, which are concrete structs, a
	// value stored in an interface needs its own heap home unless the
	// interface holds a pointer instead. The sky colour changes every frame
	// (day/night), so this box cannot be folded to a compile-time constant the
	// way the shadow pass's fixed {1.0, 0} clear can. Kept as one persistent
	// value and passed by pointer (*ClearValueFloat also satisfies ClearValue,
	// via Go's automatic pointer method set), so only its contents are
	// overwritten each frame and the interface never reboxes. Verified: with a
	// fresh core1_0.ClearValueFloat{...} passed by value here instead,
	// TestRecordCommandBufferAllocsAreConstant reports 1 alloc/op instead of 0.
	colorClear core1_0.ClearValueFloat
	// barriers is allocated once from the plan's widest barrier group.
	barriers []core1_0.ImageMemoryBarrier

	imageCopy [1]core1_0.ImageCopy

	// cubeCasters is the point-light shadow pass's pre-culled caster list,
	// rebuilt once per frame (not per face) and sized by draw count -- the one
	// remaining per-frame allocation this scratch does not turn into a fixed
	// array, because its length is data-dependent rather than bounded by a
	// small constant like a descriptor-set bind is. Grows on demand and stays
	// grown, the same way lightcluster's Builder does.
	cubeCasters []cubeCaster
}

// cubeCaster is a shadow caster that survived the point light's range test,
// with its world bound carried along so the six face loops only have to test
// a frustum, not recompute worldBoundSphere. Package-scoped rather than local
// to recordCommandBuffer so commandScratch can retain a slice of it.
type cubeCaster struct {
	idx            int
	cx, cy, cz, cr float32
}

// vertexBufferOffsets is always zero: every vertex or instance buffer this
// recorder binds is bound from its start, so there has never been a per-call
// offset to plumb through. A package-level zeroed array, resliced per call,
// is simpler than giving commandScratch a field that never changes value.
var vertexBufferOffsets = [2]int{}

// resetPC zeros the push-constant block. See the doc comment on
// commandScratch for why this runs before every fill rather than trusting
// each call site to overwrite every float it does not use.
func (s *commandScratch) resetPC() { s.pc = [pushConstantSize / 4]float32{} }

// pushConstants uploads the full 256-byte block from pc.
func (s *commandScratch) pushConstants(d core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, layout core1_0.PipelineLayout, stages core1_0.ShaderStageFlags) {
	d.CmdPushConstants(cmdBuf, layout, stages, 0, unsafe.Slice((*byte)(unsafe.Pointer(&s.pc[0])), pushConstantSize))
}

// pushShadowConstants uploads the depth-only pass's 128-byte block from
// shadowPC, always to the vertex stage -- shadow.frag does not exist.
func (s *commandScratch) pushShadowConstants(d core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, layout core1_0.PipelineLayout) {
	d.CmdPushConstants(cmdBuf, layout, core1_0.StageVertex, 0, unsafe.Slice((*byte)(unsafe.Pointer(&s.shadowPC[0])), 128))
}

// setViewport and setScissor exist because CmdSetViewport/CmdSetScissor take
// their argument as ...Viewport/...Rect2D. Every call site here passes a
// single bare value, and spreading one into a variadic parameter of an
// INTERFACE method allocates a fresh one-element backing array at the call
// site every time -- escape analysis cannot prove core1_0.DeviceDriver does
// not retain it, so it has to assume the worst. Spreading an existing slice
// instead (s.viewport1[:]...) passes that slice's backing array directly, no
// allocation, because the array already exists.
func (s *commandScratch) setViewport(d core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, v core1_0.Viewport) {
	s.viewport1[0] = v
	d.CmdSetViewport(cmdBuf, s.viewport1[:]...)
}

func (s *commandScratch) setScissor(d core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, r core1_0.Rect2D) {
	s.scissor1[0] = r
	d.CmdSetScissor(cmdBuf, s.scissor1[:]...)
}

// bindDescriptorSets binds up to len(s.descSets) sets in one call. sets is
// itself declared variadic, but bindDescriptorSets is a concrete method, not
// an interface one, so the compiler can see the whole body and prove the
// implicit slice never escapes it -- unlike the interface call above, this one
// costs nothing even though it looks the same shape.
func (s *commandScratch) bindDescriptorSets(d core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, bindPoint core1_0.PipelineBindPoint, layout core1_0.PipelineLayout, firstSet int, sets ...core1_0.DescriptorSet) {
	n := copy(s.descSets[:], sets)
	d.CmdBindDescriptorSets(cmdBuf, bindPoint, layout, firstSet, s.descSets[:n], nil)
}

// bindVertexBuffers binds up to len(s.vertexBufs) buffers, all at offset 0.
func (s *commandScratch) bindVertexBuffers(d core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, firstBinding int, buffers ...core1_0.Buffer) {
	n := copy(s.vertexBufs[:], buffers)
	d.CmdBindVertexBuffers(cmdBuf, firstBinding, s.vertexBufs[:n], vertexBufferOffsets[:n])
}

// beginRenderPass begins a render pass with up to len(s.clearValues) clear
// values, held in scratch rather than a per-call slice literal.
func (s *commandScratch) beginRenderPass(d core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, contents core1_0.SubpassContents, renderPass core1_0.RenderPass, fb core1_0.Framebuffer, area core1_0.Rect2D, clears ...core1_0.ClearValue) error {
	n := copy(s.clearValues[:], clears)
	return d.CmdBeginRenderPass(cmdBuf, contents, core1_0.RenderPassBeginInfo{
		RenderPass:  renderPass,
		Framebuffer: fb,
		RenderArea:  area,
		ClearValues: s.clearValues[:n],
	})
}

// pipelineBarrier issues an image-memory-only barrier (the only kind this
// recorder ever needs) with up to len(s.barriers) barriers.
func (s *commandScratch) pipelineBarrier(d core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, src, dst core1_0.PipelineStageFlags, barriers ...core1_0.ImageMemoryBarrier) error {
	n := copy(s.barriers[:], barriers)
	return d.CmdPipelineBarrier(cmdBuf, src, dst, 0, nil, nil, s.barriers[:n])
}

// copyImage issues a single-region image copy, which is the only shape
// the graph's copy node needs -- the whole HDR image, once.
func (s *commandScratch) copyImage(d core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, srcImage core1_0.Image, srcLayout core1_0.ImageLayout, dstImage core1_0.Image, dstLayout core1_0.ImageLayout, region core1_0.ImageCopy) error {
	s.imageCopy[0] = region
	return d.CmdCopyImage(cmdBuf, srcImage, srcLayout, dstImage, dstLayout, s.imageCopy[:]...)
}
