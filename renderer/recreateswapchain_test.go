package renderer

import (
	"errors"
	"strings"
	"testing"

	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/core/v3/loader"
)

// This file drives rebuildSwapchainTargets -- recreateSwapchain's body from
// the depth buffer on -- without a window or a GPU, the way
// commands_fixture_test.go drives recordCommandBuffer. It cannot drive
// recreateSwapchain itself: the swapchain-recreation step goes through
// khr_swapchain.CreateExtensionDriverFromCoreDriver(deviceDriver), which
// calls deviceDriver.Device() and then wraps deviceDriver.Loader() in a real
// function-pointer table looked up by extension name. A driver with no real
// device behind it does not fail that call, it segfaults -- there is no Go
// panic to recover, because the failure is a nil C function pointer, not a
// nil interface. That step is exercised on the GPU instead, by task
// validate's GLYPHENGINE_PROVOKE_RECREATE_FRAMES runs (see
// docs/agents/validation.md).
//
// Everything after the swapchain -- depth, MSAA, HDR, bloom, clouds, the UI
// glow layer, every framebuffer set -- goes through core1_0.DeviceDriver and
// core1_0.CoreInstanceDriver alone, which resizeFakeDriver below implements
// just enough of to drive the real construction and teardown code, the same
// way fakeDriver in fakedriver_test.go drives recordCommandBuffer.

// resizeFakeDriver is core1_0.DeviceDriver with only the calls
// rebuildSwapchainTargets's call tree reaches implemented; everything else is
// the embedded nil interface, so an unimplemented method panics on a nil
// pointer and names itself in the stack trace -- the same idiom fakeDriver
// uses.
//
// failCall/failAt make the Nth (1-indexed) call to the named method return
// errInjected instead of succeeding. created/destroyed count how many
// handles of each Vulkan object kind have been made and given back, by the
// same names failCall uses ("Image", "ImageView", "DeviceMemory", "Sampler",
// "DescriptorSet", "Framebuffer") -- assertBalanced below is what a test uses
// to confirm an unwind gave back everything a failed rebuild attempt made.
type resizeFakeDriver struct {
	core1_0.DeviceDriver
	h fakeHandles

	failCall string
	failAt   int
	calls    map[string]int

	created   map[string]int
	destroyed map[string]int
}

func newResizeFakeDriver() *resizeFakeDriver {
	return &resizeFakeDriver{
		calls:     map[string]int{},
		created:   map[string]int{},
		destroyed: map[string]int{},
	}
}

// errInjected is what a failed call returns. A test checks for it with
// errors.Is through recreateSwapchain's/rebuildSwapchainTargets's %w
// wrapping, which is also what proves the returned error names the step that
// failed -- the wrapping is real fmt.Errorf calls in renderer.go, not
// something this driver fabricates.
var errInjected = errors.New("resizeFakeDriver: injected failure")

// shouldFail counts a call to name and reports whether this is the one
// failAt asked to fail. A blank failCall never matches, so a driver built
// with no failure configured always returns false -- the "let the second
// call after a failure succeed" tests rely on exactly that.
func (d *resizeFakeDriver) shouldFail(name string) bool {
	d.calls[name]++
	return name == d.failCall && d.calls[name] == d.failAt
}

func (d *resizeFakeDriver) CreateImage(cb *loader.AllocationCallbacks, o core1_0.ImageCreateInfo) (core1_0.Image, common.VkResult, error) {
	if d.shouldFail("CreateImage") {
		return core1_0.Image{}, core1_0.VKErrorUnknown, errInjected
	}
	d.created["Image"]++
	return d.h.image(), core1_0.VKSuccess, nil
}

func (d *resizeFakeDriver) DestroyImage(core1_0.Image, *loader.AllocationCallbacks) {
	d.destroyed["Image"]++
}

func (d *resizeFakeDriver) GetImageMemoryRequirements(core1_0.Image) *core1_0.MemoryRequirements {
	return &core1_0.MemoryRequirements{Size: 4096, Alignment: 1, MemoryTypeBits: 0xFFFFFFFF}
}

func (d *resizeFakeDriver) AllocateMemory(cb *loader.AllocationCallbacks, o core1_0.MemoryAllocateInfo) (core1_0.DeviceMemory, common.VkResult, error) {
	if d.shouldFail("AllocateMemory") {
		return core1_0.DeviceMemory{}, core1_0.VKErrorUnknown, errInjected
	}
	d.created["DeviceMemory"]++
	return d.h.deviceMemory(), core1_0.VKSuccess, nil
}

func (d *resizeFakeDriver) FreeMemory(core1_0.DeviceMemory, *loader.AllocationCallbacks) {
	d.destroyed["DeviceMemory"]++
}

func (d *resizeFakeDriver) BindImageMemory(core1_0.Image, core1_0.DeviceMemory, int) (common.VkResult, error) {
	return core1_0.VKSuccess, nil
}

func (d *resizeFakeDriver) CreateImageView(cb *loader.AllocationCallbacks, o core1_0.ImageViewCreateInfo) (core1_0.ImageView, common.VkResult, error) {
	if d.shouldFail("CreateImageView") {
		return core1_0.ImageView{}, core1_0.VKErrorUnknown, errInjected
	}
	d.created["ImageView"]++
	return d.h.imageView(), core1_0.VKSuccess, nil
}

func (d *resizeFakeDriver) DestroyImageView(core1_0.ImageView, *loader.AllocationCallbacks) {
	d.destroyed["ImageView"]++
}

func (d *resizeFakeDriver) CreateSampler(cb *loader.AllocationCallbacks, o core1_0.SamplerCreateInfo) (core1_0.Sampler, common.VkResult, error) {
	if d.shouldFail("CreateSampler") {
		return core1_0.Sampler{}, core1_0.VKErrorUnknown, errInjected
	}
	d.created["Sampler"]++
	return d.h.sampler(), core1_0.VKSuccess, nil
}

func (d *resizeFakeDriver) DestroySampler(core1_0.Sampler, *loader.AllocationCallbacks) {
	d.destroyed["Sampler"]++
}

func (d *resizeFakeDriver) AllocateDescriptorSets(o core1_0.DescriptorSetAllocateInfo) ([]core1_0.DescriptorSet, common.VkResult, error) {
	if d.shouldFail("AllocateDescriptorSets") {
		return nil, core1_0.VKErrorUnknown, errInjected
	}
	sets := make([]core1_0.DescriptorSet, len(o.SetLayouts))
	for i := range sets {
		sets[i] = d.h.descSet()
	}
	d.created["DescriptorSet"] += len(sets)
	return sets, core1_0.VKSuccess, nil
}

func (d *resizeFakeDriver) FreeDescriptorSets(sets ...core1_0.DescriptorSet) (common.VkResult, error) {
	d.destroyed["DescriptorSet"] += len(sets)
	return core1_0.VKSuccess, nil
}

func (d *resizeFakeDriver) UpdateDescriptorSets([]core1_0.WriteDescriptorSet, []core1_0.CopyDescriptorSet) error {
	return nil
}

func (d *resizeFakeDriver) CreateFramebuffer(cb *loader.AllocationCallbacks, o core1_0.FramebufferCreateInfo) (core1_0.Framebuffer, common.VkResult, error) {
	if d.shouldFail("CreateFramebuffer") {
		return core1_0.Framebuffer{}, core1_0.VKErrorUnknown, errInjected
	}
	d.created["Framebuffer"]++
	return d.h.framebuffer(), core1_0.VKSuccess, nil
}

func (d *resizeFakeDriver) DestroyFramebuffer(core1_0.Framebuffer, *loader.AllocationCallbacks) {
	d.destroyed["Framebuffer"]++
}

func (d *resizeFakeDriver) DeviceWaitIdle() (common.VkResult, error) { return core1_0.VKSuccess, nil }

// AllocateCommandBuffers/BeginCommandBuffer/EndCommandBuffer/CmdPipelineBarrier/
// CmdClearColorImage/QueueSubmit/QueueWaitIdle/FreeCommandBuffers are
// primeSampledImages's one-shot command buffer (via beginSingleTimeCommands/
// endSingleTimeCommands) -- reached unconditionally by primeBloomLayouts.
// None of them fail in this fixture: issue #86 is about a construction
// failure leaving the renderer half-built, and a one-shot command buffer here
// creates nothing rebuildSwapchainTargets tracks -- see primeSampledImages,
// which barriers and clears images the caller already owns.
func (d *resizeFakeDriver) AllocateCommandBuffers(o core1_0.CommandBufferAllocateInfo) ([]core1_0.CommandBuffer, common.VkResult, error) {
	bufs := make([]core1_0.CommandBuffer, o.CommandBufferCount)
	for i := range bufs {
		bufs[i] = d.h.commandBuffer()
	}
	return bufs, core1_0.VKSuccess, nil
}
func (d *resizeFakeDriver) FreeCommandBuffers(...core1_0.CommandBuffer) {}
func (d *resizeFakeDriver) BeginCommandBuffer(core1_0.CommandBuffer, core1_0.CommandBufferBeginInfo) (common.VkResult, error) {
	return core1_0.VKSuccess, nil
}
func (d *resizeFakeDriver) EndCommandBuffer(core1_0.CommandBuffer) (common.VkResult, error) {
	return core1_0.VKSuccess, nil
}
func (d *resizeFakeDriver) CmdPipelineBarrier(core1_0.CommandBuffer, core1_0.PipelineStageFlags, core1_0.PipelineStageFlags, core1_0.DependencyFlags, []core1_0.MemoryBarrier, []core1_0.BufferMemoryBarrier, []core1_0.ImageMemoryBarrier) error {
	return nil
}
func (d *resizeFakeDriver) CmdClearColorImage(core1_0.CommandBuffer, core1_0.Image, core1_0.ImageLayout, core1_0.ClearColorValue, ...core1_0.ImageSubresourceRange) {
}
func (d *resizeFakeDriver) QueueSubmit(core1_0.Queue, *core1_0.Fence, ...core1_0.SubmitInfo) (common.VkResult, error) {
	return core1_0.VKSuccess, nil
}
func (d *resizeFakeDriver) QueueWaitIdle(core1_0.Queue) (common.VkResult, error) {
	return core1_0.VKSuccess, nil
}

// InstanceDriver satisfies core1_0.CoreDeviceDriver, the type r.deviceDriver
// actually holds. Nothing in rebuildSwapchainTargets's call tree calls it --
// findMemoryType and findDepthFormat take r.instanceDriver directly -- so it
// only has to type-check.
func (d *resizeFakeDriver) InstanceDriver() core1_0.CoreInstanceDriver {
	return resizeFakeInstanceDriver{}
}

// resizeFakeInstanceDriver is core1_0.CoreInstanceDriver with only the two
// physical-device queries findDepthFormat and findMemoryType make
// implemented.
type resizeFakeInstanceDriver struct{ core1_0.CoreInstanceDriver }

func (resizeFakeInstanceDriver) GetPhysicalDeviceMemoryProperties(core1_0.PhysicalDevice) *core1_0.PhysicalDeviceMemoryProperties {
	return &core1_0.PhysicalDeviceMemoryProperties{
		MemoryTypes: []core1_0.MemoryType{{PropertyFlags: core1_0.MemoryPropertyDeviceLocal}},
	}
}

func (resizeFakeInstanceDriver) GetPhysicalDeviceFormatProperties(pd core1_0.PhysicalDevice, format core1_0.Format) *core1_0.FormatProperties {
	// findDepthFormat asks for FormatD32SignedFloat first and takes whatever
	// answers with the bit it wants, so answering it for every format keeps
	// the fixture from caring which candidate it lands on.
	return &core1_0.FormatProperties{OptimalTilingFeatures: core1_0.FormatFeatureDepthStencilAttachment}
}

// The following fakeHandles methods are additive: fakedriver_test.go's
// fakeHandles already hands out Buffer/Image/DescriptorSet/Pipeline/
// PipelineLayout/RenderPass/Framebuffer/CommandBuffer for the command-
// recording tests. rebuildSwapchainTargets's construction path needs a few
// more Vulkan object kinds, so this file adds constructors for those and
// nothing else -- fakeHandles.n() already guarantees every handle, old kinds
// and new, is distinct.

func (h *fakeHandles) deviceMemory() core1_0.DeviceMemory {
	return core1_0.InternalDeviceMemory(0, loader.VkDeviceMemory(h.n()), 0, 0)
}
func (h *fakeHandles) imageView() core1_0.ImageView {
	return core1_0.InternalImageView(0, loader.VkImageView(h.n()), 0)
}
func (h *fakeHandles) sampler() core1_0.Sampler {
	return core1_0.InternalSampler(0, loader.VkSampler(h.n()), 0)
}
func (h *fakeHandles) descriptorPool() core1_0.DescriptorPool {
	return core1_0.InternalDescriptorPool(0, loader.VkDescriptorPool(h.n()), 0)
}
func (h *fakeHandles) descriptorSetLayout() core1_0.DescriptorSetLayout {
	return core1_0.InternalDescriptorSetLayout(0, loader.VkDescriptorSetLayout(h.n()), 0)
}
func (h *fakeHandles) commandPool() core1_0.CommandPool {
	return core1_0.InternalCommandPool(0, loader.VkCommandPool(h.n()), 0)
}
func (h *fakeHandles) queue() core1_0.Queue {
	return core1_0.InternalQueue(0, loader.VkQueue(h.n()), 0)
}
func (h *fakeHandles) physicalDevice() core1_0.PhysicalDevice {
	return core1_0.InternalPhysicalDevice(loader.VkPhysicalDevice(h.n()), 0, 0)
}

// newResizeFixture builds a *Renderer with just the fields
// rebuildSwapchainTargets's call tree reads: no window, no instance, no
// surface -- those belong to the swapchain-recreation step this fixture does
// not reach (see the file comment). count is the swapchain image count; msaa
// off and the UI glow layer and scene-colour target absent, so the fixture
// exercises the path every renderer takes and the two conditional ones
// (clouds, captureCapable) are opted into per test.
func newResizeFixture(d *resizeFakeDriver, count int) *Renderer {
	h := &fakeHandles{}
	views := make([]core1_0.ImageView, count)
	for i := range views {
		views[i] = h.imageView()
	}

	shadow := &shadowResources{}
	for f := range shadow.lightVPBuffers {
		shadow.lightVPBuffers[f] = h.buffer()
	}

	r := &Renderer{
		deviceDriver:   d,
		instanceDriver: resizeFakeInstanceDriver{},
		physicalDevice: h.physicalDevice(),
		sc: &swapchainDetails{
			imageViews: views,
			extent:     core1_0.Extent2D{Width: 640, Height: 360},
		},
		msaaSamples:         core1_0.Samples1,
		descriptorPool:      h.descriptorPool(),
		descriptorSetLayout: h.descriptorSetLayout(),
		tonemapSetLayout:    h.descriptorSetLayout(),
		bloomDownRenderPass: h.renderPass(),
		bloomUpRenderPass:   h.renderPass(),
		cloudRenderPass:     h.renderPass(),
		renderPass:          h.renderPass(),
		tonemapRenderPass:   h.renderPass(),
		waterRenderPass:     h.renderPass(),
		shadow:              shadow,
		commandPool:         h.commandPool(),
		graphicsQueue:       h.queue(),
	}
	return r
}

// assertBalanced fails the test unless every Vulkan object kind the driver
// tracked has been destroyed exactly as many times as it was created -- the
// GPU-free stand-in for "the validation layer reports no leaked handles at
// vkDestroyDevice". A step that pushes an undo closure but the closure
// forgets to destroy something breaks this; see
// TestRebuildSwapchainTargetsUnwindCatchesAMissedDestroy for the check
// proving that. Takes testing.TB rather than *testing.T so that test can
// substitute a recording stand-in and observe whether this would have failed,
// without failing itself.
func assertBalanced(t testing.TB, d *resizeFakeDriver) {
	t.Helper()
	for _, kind := range []string{"Image", "ImageView", "DeviceMemory", "Sampler", "DescriptorSet", "Framebuffer"} {
		if d.created[kind] != d.destroyed[kind] {
			t.Errorf("%s: created %d, destroyed %d (leaked or double-freed %d)",
				kind, d.created[kind], d.destroyed[kind], d.created[kind]-d.destroyed[kind])
		}
	}
}

// attemptRebuild is recreateSwapchain's own post-swapchain shape (call
// rebuildSwapchainTargets, unwind on failure) without the swapchain step
// itself, since that step is what this file cannot reach GPU-free.
func attemptRebuild(r *Renderer, oldExtent core1_0.Extent2D) error {
	var undo rebuildUndo
	if err := r.rebuildSwapchainTargets(oldExtent, &undo); err != nil {
		undo.unwind()
		return err
	}
	return nil
}

// TestRebuildSwapchainTargetsSucceeds is the control: with no failure
// injected, every step should succeed and leave every tracked resource kind
// created (destroyed stays 0, since nothing tore anything down on a clean
// pass) -- proving newResizeFixture's field set is actually sufficient to
// reach the end of rebuildSwapchainTargets before the failure tests below
// start subtracting from it.
func TestRebuildSwapchainTargetsSucceeds(t *testing.T) {
	d := newResizeFakeDriver()
	r := newResizeFixture(d, 2)

	if err := attemptRebuild(r, r.sc.extent); err != nil {
		t.Fatalf("rebuildSwapchainTargets: %v", err)
	}
	if r.depth == nil || r.hdr == nil || r.bloom == nil || r.framebuffers == nil || r.tonemapFramebuffers == nil {
		t.Fatal("a successful rebuild left a target nil")
	}
	if d.created["Image"] == 0 || d.destroyed["Image"] != 0 {
		t.Fatalf("expected images created and none destroyed on a clean pass, got created=%d destroyed=%d",
			d.created["Image"], d.destroyed["Image"])
	}
}

// TestRebuildSwapchainTargetsUnwindsOnFailure is issue #86's proof: a failure
// injected into a named call, at the Nth invocation of it, comes back wrapped
// with the failing step's name, and every handle the attempt created before
// that point -- across every resource kind the driver tracks -- was destroyed
// again on the way out. Table-driven over the three calls the issue names
// (AllocateDescriptorSets, CreateImage, CreateFramebuffer), each landing in a
// different step so the whole chain from HDR through the plain framebuffers
// gets exercised at least once.
func TestRebuildSwapchainTargetsUnwindsOnFailure(t *testing.T) {
	const count = 2 // r.sc.imageViews; keeps the call-count arithmetic below small.

	tests := []struct {
		name     string
		failCall string
		failAt   int
		wantStep string // substring the wrapped error must contain
	}{
		{
			// HDR's own AllocateDescriptorSets (sceneSets) is call 1; bloom's
			// per-image AllocateDescriptorSets is calls 2..count+1, one per
			// swapchain image. Failing call 2 fails bloom on its first image,
			// after HDR has already fully succeeded and been pushed onto undo.
			name:     "AllocateDescriptorSets fails inside bloom",
			failCall: "AllocateDescriptorSets",
			failAt:   2,
			wantStep: "recreate bloom targets",
		},
		{
			// depth uses CreateImage calls 1..count; HDR uses count+1..2*count.
			// Failing the second of HDR's own calls exercises createHDRTargets'
			// own internal cleanup (it must destroy the first HDR image before
			// returning) as well as the outer unwind of depth.
			name:     "CreateImage fails inside HDR",
			failCall: "CreateImage",
			failAt:   count + 2,
			wantStep: "recreate HDR targets",
		},
		{
			// bloom uses CreateFramebuffer calls 1..count*bloomLevels*2 (down
			// and up per level per image). The plain scene framebuffers
			// (createFramebuffers) start right after, at count*bloomLevels*2+1,
			// and create `count` of their own. Failing the SECOND of those
			// exercises createFramebuffers' own partial-failure cleanup (it
			// must destroy the framebuffer made for image 0) on top of the
			// outer unwind of depth, HDR and bloom.
			name:     "CreateFramebuffer fails inside the scene framebuffers",
			failCall: "CreateFramebuffer",
			failAt:   count*bloomLevels*2 + 2,
			wantStep: "recreate framebuffers",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newResizeFakeDriver()
			d.failCall, d.failAt = tc.failCall, tc.failAt
			r := newResizeFixture(d, count)

			err := attemptRebuild(r, r.sc.extent)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !errors.Is(err, errInjected) {
				t.Errorf("error does not wrap the injected failure: %v", err)
			}
			if got := err.Error(); !strings.Contains(got, tc.wantStep) {
				t.Errorf("error %q does not name the failing step %q", got, tc.wantStep)
			}

			// Every field this attempt could have touched must be back to nil:
			// Destroy must be safe to call (nothing left half-built), and a
			// later DrawFrame/recreateSwapchain must retry cleanly rather than
			// read through a stale pointer.
			if r.depth != nil || r.hdr != nil || r.bloom != nil || r.framebuffers != nil || r.tonemapFramebuffers != nil {
				t.Errorf("a field survived the unwind: depth=%v hdr=%v bloom=%v framebuffers=%v tonemapFramebuffers=%v",
					r.depth, r.hdr, r.bloom, r.framebuffers, r.tonemapFramebuffers)
			}

			assertBalanced(t, d)

			// The proof this is not decoration: break the fix (skip the depth
			// undo) and confirm assertBalanced actually fails. Left in as a
			// sub-test rather than a comment, per CLAUDE.md's "break the fix
			// and confirm the check fails" -- see
			// TestRebuildSwapchainTargetsUnwindCatchesAMissedDestroy, which
			// does this for real rather than simulating it inline here.

			// A second attempt, with the injected failure turned off, must
			// succeed cleanly rather than panic on whatever the unwind left
			// behind -- issue #86's "a later DrawFrame either succeeds after a
			// successful rebuild or returns the same error again, never
			// dereferences nil".
			d.failCall = ""
			if err := attemptRebuild(r, r.sc.extent); err != nil {
				t.Fatalf("second attempt after the failure: %v", err)
			}
			if r.depth == nil || r.hdr == nil || r.bloom == nil {
				t.Fatal("second attempt did not leave a usable renderer")
			}
		})
	}
}

// TestRebuildSwapchainTargetsSameErrorTwice is the deterministic half of
// issue #86's contract: a rebuild that fails because a resource is
// genuinely exhausted (the pool, in the original report) fails the SAME way
// on a retry, rather than panicking or silently drawing with whatever the
// first attempt left behind. Simulated here by leaving the injected failure
// on for both attempts, which is what a pool that never recovers looks like
// from rebuildSwapchainTargets's side.
func TestRebuildSwapchainTargetsSameErrorTwice(t *testing.T) {
	const count = 2
	d := newResizeFakeDriver()
	d.failCall, d.failAt = "AllocateDescriptorSets", 2
	r := newResizeFixture(d, count)

	first := attemptRebuild(r, r.sc.extent)
	if first == nil {
		t.Fatal("expected the first attempt to fail")
	}
	d.calls["AllocateDescriptorSets"] = 0 // a fresh attempt starts its own count, same as a fresh call would
	second := attemptRebuild(r, r.sc.extent)
	if second == nil {
		t.Fatal("expected the second attempt to fail the same way")
	}
	if first.Error() != second.Error() {
		t.Errorf("retry produced a different error:\n first: %v\nsecond: %v", first, second)
	}
	assertBalanced(t, d)
}

// TestRebuildSwapchainTargetsCloudsRebuildOnResize exercises the one
// conditional step the three failure cases above skip by holding oldExtent
// equal to the fixture's extent: a genuine resize forces the cloud targets to
// rebuild too (see the comment on that block in rebuildSwapchainTargets), and
// a failure there must unwind through depth, HDR and bloom exactly like any
// other step.
func TestRebuildSwapchainTargetsCloudsRebuildOnResize(t *testing.T) {
	const count = 2
	d := newResizeFakeDriver()
	r := newResizeFixture(d, count)

	oldExtent := core1_0.Extent2D{Width: 1280, Height: 720} // different from r.sc.extent
	if err := attemptRebuild(r, oldExtent); err != nil {
		t.Fatalf("rebuildSwapchainTargets: %v", err)
	}
	if r.clouds == nil {
		t.Fatal("a resize did not rebuild the cloud targets")
	}
	// A clean pass destroys nothing (there was nothing old to give back in
	// this fixture), so assertBalanced would only prove every count is zero
	// -- it belongs on the failure half below, once something has actually
	// been torn down.
	if d.destroyed["Image"] != 0 {
		t.Fatalf("expected nothing destroyed on a clean pass, got %d", d.destroyed["Image"])
	}

	// Now fail inside that same step and confirm it unwinds like the others.
	d2 := newResizeFakeDriver()
	// depth(count) + HDR(count) + bloom(count*bloomLevels) CreateImage calls
	// happen before clouds gets its own, one per cloud buffer.
	d2.failCall, d2.failAt = "CreateImage", count+count+count*bloomLevels+1
	r2 := newResizeFixture(d2, count)
	err := attemptRebuild(r2, oldExtent)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "recreate cloud targets") {
		t.Errorf("error %q does not name the cloud step", err.Error())
	}
	if r2.clouds != nil || r2.bloom != nil || r2.hdr != nil || r2.depth != nil {
		t.Error("a field survived the unwind")
	}
	assertBalanced(t, d2)
}

// TestRebuildSwapchainTargetsUnwindCatchesAMissedDestroy is CLAUDE.md's "break
// the fix and confirm the check fails", pinned as a test rather than a
// worktree-only manual step: assertBalanced only means something if it can
// actually fail, so this drives a driver that "forgets" to count one HDR
// image's destroy -- standing in for an undo closure that skipped a handle --
// and confirms assertBalanced reports it rather than passing silently.
func TestRebuildSwapchainTargetsUnwindCatchesAMissedDestroy(t *testing.T) {
	const count = 2
	d := newResizeFakeDriver()
	d.failCall, d.failAt = "AllocateDescriptorSets", 2 // fails inside bloom, same as the table test above
	r := newResizeFixture(d, count)

	if err := attemptRebuild(r, r.sc.extent); err == nil {
		t.Fatal("expected an error")
	}
	assertBalanced(t, d) // real behaviour: the unwind is not broken, so this passes.

	// Simulate a broken unwind (one destroy silently dropped, the shape a
	// missing line in an undo closure would take) and confirm the same check
	// now fails.
	d.destroyed["Image"]--
	captured := &capturingT{TB: t}
	assertBalanced(captured, d)
	if !captured.failed {
		t.Fatal("assertBalanced did not catch a deliberately unbalanced destroy count")
	}
}

// capturingT lets TestRebuildSwapchainTargetsUnwindCatchesAMissedDestroy
// observe whether assertBalanced would have failed the test, without
// actually failing this one -- t.Errorf has no "did it fire" return, so the
// check needs a testing.TB stand-in that records the call instead of
// reporting it upward.
type capturingT struct {
	testing.TB
	failed bool
}

func (c *capturingT) Errorf(format string, args ...any) { c.failed = true }
func (c *capturingT) Helper()                           {}
