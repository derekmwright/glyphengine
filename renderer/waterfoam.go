package renderer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"unsafe"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// WaterFoamDesc is the grid a water surface's whitecap foam is kept over: the
// same grid the surface mesh was built on, one texel per vertex.
//
// One texel per vertex because the Jacobian foam is marked from is evaluated
// per vertex in the water vertex stage, so it carries no detail finer than the
// grid: a finer target would store interpolation, and a coarser one would
// blur a crest across its neighbours before it ever reached the screen. For
// 09-water's 200-division lake that is 201 x 201 half floats, 79 KiB.
type WaterFoamDesc struct {
	// Origin is the rest position, in the mesh's own space, of grid point
	// (0, 0), and Step the spacing between grid points along X and Z.
	Origin, Step [2]float32
	// Width and Height are the grid's point counts along X and Z.
	Width, Height int
	// Depth is the still-water depth under every grid point, Width*Height of
	// them in rows along X: the depths the mesh's vertices carry, which is
	// what shoals the waves the foam is computed from exactly as the vertex
	// stage shoals them.
	Depth []float32
}

// WaterFoam is one water surface's persistent foam: an R16F target the size of
// its grid, the grid's depths, and a compute dispatch that advances the target
// once per frame the surface is drawn. Create it with CreateWaterFoam; it is
// released with its mesh or by DestroyWaterFoam.
//
// Use only on the renderer thread.
type WaterFoam struct {
	r             *Renderer
	mesh          *Mesh
	width, height int
	spacing       float32

	image      *appImages
	grid       core1_0.Buffer
	gridMemory core1_0.DeviceMemory
	set        core1_0.DescriptorSet

	// Per-frame state, set by prepareWaterFoam before the graph runs. graph is
	// the frame graph that listed this target this frame: a target created
	// since that graph was built has no node in it, and must not be stepped as
	// though it had been dispatched.
	graph   *frameGraph
	active  bool
	stepped bool
	last    float32
	push    [8]float32

	destroyed bool
}

// waterFoamShared is what every foamed surface shares: one descriptor set
// layout, the compute pipeline that steps a target and the graphics pipeline
// that draws a surface with its foam. Created on the first CreateWaterFoam, so
// a renderer that never foams anything never builds them.
type waterFoamShared struct {
	setLayout                     core1_0.DescriptorSetLayout
	computeLayout, graphicsLayout core1_0.PipelineLayout
	compute, graphics             core1_0.Pipeline
}

// The foam target's format. R16F because coverage is a value in [0, 1] whose
// per-frame decay at 60 Hz and the default three-second Decay is 0.0056, about
// eleven half-precision steps even just under full coverage, so with the
// compute stage rounding each store explicitly (quantize in water_foam.comp) a
// fade keeps its rate. Half R32F's memory, and linear filtering of R16F is
// required of every Vulkan device where R32F's is not. Writing it from a shader
// takes shaderStorageImageExtendedFormats, which createLogicalDevice enables
// whenever the device has it; CreateWaterFoam reports a device without it as an
// error naming the format.
const waterFoamFormat = core1_0.FormatR16SignedFloat

// Water foam push slots beside pcWaterSubsurfaceColor; the same reasoning,
// and TestWaterPushSlotsAreUnreadByWater holds both.
const (
	pcWaterFoamThreshold = 47 // pointPos.w
	pcWaterFoamColor     = 48 // pointColor.rgb
)

// WaterFoam returns the foam target CreateWaterFoam attached to this mesh, or
// nil.
func (m *Mesh) WaterFoam() *WaterFoam { return m.waterFoam }

// Size returns the target's texel counts, which are the grid's point counts.
func (f *WaterFoam) Size() (width, height int) { return f.width, f.height }

// CreateWaterFoam attaches a persistent foam target to a water surface mesh.
//
// A surface with one is drawn by the foam pipeline whenever its
// WaterParams.FoamDecay is above zero, and its target is stepped once in each
// frame it is drawn by an engine compute node placed before the scene-colour
// copy. A surface without one costs exactly what it cost before foam existed:
// no target, no node, no second pipeline.
func (r *Renderer) CreateWaterFoam(m *Mesh, d WaterFoamDesc) (_ *WaterFoam, err error) {
	switch {
	case m == nil || m.destroyed:
		return nil, errors.New("water foam: needs a live mesh")
	case m.waterFoam != nil:
		return nil, errors.New("water foam: the mesh already has a foam target")
	case d.Width < 2 || d.Height < 2:
		return nil, fmt.Errorf("water foam: grid %dx%d must be at least 2x2", d.Width, d.Height)
	case len(d.Depth) != d.Width*d.Height:
		return nil, fmt.Errorf("water foam: %d depths for a %dx%d grid", len(d.Depth), d.Width, d.Height)
	case !(d.Step[0] > 0) || !(d.Step[1] > 0):
		return nil, fmt.Errorf("water foam: grid step %v must be positive", d.Step)
	}
	props := r.instanceDriver.GetPhysicalDeviceFormatProperties(r.physicalDevice, waterFoamFormat)
	need := core1_0.FormatFeatureStorageImage | core1_0.FormatFeatureSampledImageFilterLinear
	if !r.caps.storageImageExtendedFormats || props.OptimalTilingFeatures&need != need {
		return nil, fmt.Errorf("water foam: this device cannot write and filter %v storage images", waterFoamFormat)
	}
	if err = r.ensureWaterFoamPipelines(); err != nil {
		return nil, fmt.Errorf("water foam: %w", err)
	}

	f := &WaterFoam{r: r, mesh: m, width: d.Width, height: d.Height, spacing: max(d.Step[0], d.Step[1])}
	defer func() {
		if err != nil {
			f.release()
		}
	}()
	f.image, err = r.newAppImages(waterFoamFormat, core1_0.ImageAspectColor,
		core1_0.Extent2D{Width: d.Width, Height: d.Height}, 1,
		appImageOptions{sampled: true, storage: true, filter: FilterLinear, wrap: WrapClampToEdge})
	if err != nil {
		return nil, fmt.Errorf("water foam: target: %w", err)
	}
	// The header the compute and fragment stages index the grid by, then the
	// depths, as std430 lays the block out: a vec4 and a float array.
	data := make([]byte, 16+4*len(d.Depth))
	for i, v := range [4]float32{d.Origin[0], d.Origin[1], d.Step[0], d.Step[1]} {
		binary.LittleEndian.PutUint32(data[4*i:], math.Float32bits(v))
	}
	for i, v := range d.Depth {
		binary.LittleEndian.PutUint32(data[16+4*i:], math.Float32bits(v))
	}
	f.grid, f.gridMemory, err = r.createDeviceLocalBuffer(data, core1_0.BufferUsageStorageBuffer)
	if err != nil {
		return nil, fmt.Errorf("water foam: grid: %w", err)
	}
	sets, _, err := r.deviceDriver.AllocateDescriptorSets(core1_0.DescriptorSetAllocateInfo{
		DescriptorPool: r.descriptorPool, SetLayouts: []core1_0.DescriptorSetLayout{r.waterFoam.setLayout}})
	if err != nil {
		return nil, fmt.Errorf("water foam: descriptor set: %w", err)
	}
	f.set = sets[0]
	// Both image bindings name the one view: the fragment stage samples it in
	// the layout it rests in and the compute stage writes it in General, and a
	// pipeline only ever touches the binding its own stage declares.
	view := f.image.views[0]
	if err = r.deviceDriver.UpdateDescriptorSets([]core1_0.WriteDescriptorSet{
		{DstSet: f.set, DstBinding: 0, DescriptorType: core1_0.DescriptorTypeCombinedImageSampler,
			ImageInfo: []core1_0.DescriptorImageInfo{{Sampler: f.image.sampler, ImageView: view, ImageLayout: core1_0.ImageLayoutShaderReadOnlyOptimal}}},
		{DstSet: f.set, DstBinding: 1, DescriptorType: core1_0.DescriptorTypeStorageBuffer,
			BufferInfo: []core1_0.DescriptorBufferInfo{{Buffer: f.grid, Range: len(data)}}},
		{DstSet: f.set, DstBinding: 2, DescriptorType: core1_0.DescriptorTypeStorageImage,
			ImageInfo: []core1_0.DescriptorImageInfo{{ImageView: view, ImageLayout: core1_0.ImageLayoutGeneral}}},
	}, nil); err != nil {
		return nil, fmt.Errorf("water foam: descriptors: %w", err)
	}

	m.waterFoam = f
	r.waterFoams = append(r.waterFoams, f)
	r.graphDirty = true
	return f, nil
}

// DestroyWaterFoam detaches the target from its mesh at once and releases it
// once the frames in flight are done with it, through the same deferred queue
// every other GPU object here retires through. The mesh draws without foam from
// the next frame. Destroying the mesh does this too.
func (r *Renderer) DestroyWaterFoam(f *WaterFoam) {
	if f == nil || f.r != r || f.destroyed {
		return
	}
	f.destroyed = true
	f.active = false
	if f.mesh != nil && f.mesh.waterFoam == f {
		f.mesh.waterFoam = nil
	}
	r.waterFoams = slices.DeleteFunc(r.waterFoams, func(x *WaterFoam) bool { return x == f })
	// The next frame rebuilds the graph without this target's node before it
	// records anything, and the countdown outlasts the frames that still have it.
	r.graphDirty = true
	r.DeferDestroy(f.release)
}

// release destroys what one target owns, in the order its descriptor set
// requires: the set names the view and the buffer.
func (f *WaterFoam) release() {
	d := f.r.deviceDriver
	if f.set.Handle() != 0 {
		freeSets(d, []core1_0.DescriptorSet{f.set})
		f.set = core1_0.DescriptorSet{}
	}
	if f.grid.Handle() != 0 {
		d.DestroyBuffer(f.grid, nil)
		d.FreeMemory(f.gridMemory, nil)
		f.grid, f.gridMemory = core1_0.Buffer{}, core1_0.DeviceMemory{}
	}
	f.image.destroy(d)
	f.image = nil
}

// ensureWaterFoamPipelines builds the shared layout and both pipelines on first
// use. A failure leaves none of them behind, so the next attempt starts clean.
func (r *Renderer) ensureWaterFoamPipelines() (err error) {
	s := &r.waterFoam
	if s.graphics.Handle() != 0 {
		return nil
	}
	defer func() {
		if err != nil {
			r.destroyWaterFoamPipelines()
		}
	}()
	s.setLayout, _, err = r.deviceDriver.CreateDescriptorSetLayout(nil, core1_0.DescriptorSetLayoutCreateInfo{
		Bindings: []core1_0.DescriptorSetLayoutBinding{
			{Binding: 0, DescriptorType: core1_0.DescriptorTypeCombinedImageSampler, DescriptorCount: 1, StageFlags: core1_0.StageFragment},
			{Binding: 1, DescriptorType: core1_0.DescriptorTypeStorageBuffer, DescriptorCount: 1, StageFlags: core1_0.StageFragment | core1_0.StageCompute},
			{Binding: 2, DescriptorType: core1_0.DescriptorTypeStorageImage, DescriptorCount: 1, StageFlags: core1_0.StageCompute},
		}})
	if err != nil {
		return fmt.Errorf("set layout: %w", err)
	}
	s.compute, s.computeLayout, err = createAppComputePipeline(r.deviceDriver, r.shaders.WaterFoamComp, s.setLayout)
	if err != nil {
		return fmt.Errorf("compute pipeline: %w", err)
	}
	// Sets 0 and 1 are the lit layout's, so the foam pipeline binds the scene
	// copy and the light set exactly as the plain water pipeline does; set 2 is
	// the foam.
	s.graphicsLayout, _, err = r.deviceDriver.CreatePipelineLayout(nil, core1_0.PipelineLayoutCreateInfo{
		SetLayouts:         []core1_0.DescriptorSetLayout{r.descriptorSetLayout, r.shadow.descriptorSetLayout, s.setLayout},
		PushConstantRanges: []core1_0.PushConstantRange{{StageFlags: core1_0.StageVertex | core1_0.StageFragment, Size: pushConstantSize}},
	})
	if err != nil {
		return fmt.Errorf("pipeline layout: %w", err)
	}
	s.graphics, err = createWaterPipeline(r.deviceDriver, r.shaders.WaterVert, r.shaders.WaterFoamFrag, r.waterFormats, s.graphicsLayout, r.sc.extent, r.msaaSamples)
	if err != nil {
		return fmt.Errorf("graphics pipeline: %w", err)
	}
	return nil
}

func (r *Renderer) destroyWaterFoamPipelines() {
	s := &r.waterFoam
	d := r.deviceDriver
	if s.graphics.Handle() != 0 {
		d.DestroyPipeline(s.graphics, nil)
	}
	if s.graphicsLayout.Handle() != 0 {
		d.DestroyPipelineLayout(s.graphicsLayout, nil)
	}
	if s.compute.Handle() != 0 {
		d.DestroyPipeline(s.compute, nil)
	}
	if s.computeLayout.Handle() != 0 {
		d.DestroyPipelineLayout(s.computeLayout, nil)
	}
	if s.setLayout.Handle() != 0 {
		d.DestroyDescriptorSetLayout(s.setLayout, nil)
	}
	*s = waterFoamShared{}
}

// destroyWaterFoamResources is shutdown's half: every target still alive, then
// the shared pipelines. Registered on New's init stack beside the application
// resources, which these are made the same way as.
func (r *Renderer) destroyWaterFoamResources() {
	foams := r.waterFoams
	r.waterFoams = nil
	for _, f := range foams {
		f.destroyed = true
		if f.mesh != nil && f.mesh.waterFoam == f {
			f.mesh.waterFoam = nil
		}
		f.release()
	}
	r.destroyWaterFoamPipelines()
}

// appendWaterFoamGraph declares every live foam target and the node that steps
// it, ahead of the scene-colour copy: a compute write the water pass's fragment
// stage then samples, which is the edge the graph exists to derive.
//
// Each dispatch and its trailing return to the sampled layout form one optional
// group, like an application dispatch, so a surface not drawn this frame skips
// both and leaves its target where it was -- foam on water nobody is looking at
// neither grows nor fades, and catches up by the elapsed time when it is seen
// again.
func (r *Renderer) appendWaterFoamGraph(f *frameGraph, g *framegraph.Graph, group int, add func(framegraph.Node, graphNode)) {
	f.foams, f.foamImages = nil, nil
	for i, wf := range r.waterFoams {
		id := g.AddImage(framegraph.ImageDesc{Name: fmt.Sprintf("water foam %d", i), Format: waterFoamFormat,
			Extent: framegraph.Extent{Fixed: [2]uint32{uint32(wf.width), uint32(wf.height)}}, Samples: core1_0.Samples1,
			Aspect: core1_0.ImageAspectColor, Persistent: true})
		f.foams = append(f.foams, wf)
		f.foamImages = append(f.foamImages, id)
		enabled := func(*graphFrame) bool { return wf.active }
		add(framegraph.Node{Name: "water foam", Kind: framegraph.Compute, OptionalGroup: group + i,
			Uses: []framegraph.Use{{Resource: id, Access: framegraph.StorageReadWrite}}},
			graphNode{name: "water foam", record: wf.record, enabled: enabled, begin: -1, end: -1, resolve: -1})
		add(framegraph.Node{Name: "water foam sampled", Kind: framegraph.Transfer, OptionalGroup: group + i,
			Uses: []framegraph.Use{{Resource: id, Access: framegraph.SampledRead,
				Stages: core1_0.PipelineStageFragmentShader | core1_0.PipelineStageComputeShader}}},
			graphNode{name: "water foam sampled", enabled: enabled, begin: -1, end: -1, resolve: -1})
	}
}

// prepareWaterFoam decides which targets step this frame and with what: every
// target this graph lists whose surface is drawn with a decay above zero, once
// however many draws share its mesh.
//
// The decay is the elapsed shader time since the target last stepped, so under
// a fixed clock the target is a function of the frame count, and a surface
// that was out of view comes back faded by however long it was gone.
func prepareWaterFoam(graph *frameGraph, draws []RenderObject, time float32) {
	if len(graph.foams) == 0 {
		return
	}
	for _, f := range graph.foams {
		f.active = false
		f.graph = graph
	}
	for i := range draws {
		d := &draws[i]
		if d.Water == nil || d.ShadowOnly || d.Mesh == nil || !(d.Water.FoamDecay > 0) {
			continue
		}
		f := d.Mesh.waterFoam
		if f == nil || f.destroyed || f.active || f.graph != graph {
			continue
		}
		var decay float32
		if f.stepped {
			decay = max(time-f.last, 0) / d.Water.FoamDecay
		}
		f.active, f.stepped, f.last = true, true, time
		f.push = [8]float32{time, d.Water.Amplitude, d.Water.WaveLength, f.spacing, d.Water.FoamThreshold, decay}
	}
}

// foamFor is the target a water draw is shaded with, or nil for the plain
// water pipeline.
func foamFor(d *RenderObject) *WaterFoam {
	if d.Mesh == nil || !(d.Water.FoamDecay > 0) {
		return nil
	}
	if f := d.Mesh.waterFoam; f != nil && f.active {
		return f
	}
	return nil
}

func (f *WaterFoam) record(c *graphFrame) {
	s, d := c.scratch, c.driver
	shared := &f.r.waterFoam
	d.CmdBindPipeline(c.cmd, core1_0.PipelineBindPointCompute, shared.compute)
	s.bindDescriptorSets(d, c.cmd, core1_0.PipelineBindPointCompute, shared.computeLayout, 0, f.set)
	s.resetPC()
	copy(s.pc[:], f.push[:])
	s.pushConstants(d, c.cmd, shared.computeLayout, core1_0.StageCompute)
	d.CmdDispatch(c.cmd, (f.width+7)/8, (f.height+7)/8, 1)
}

// Coverage reads the target back: foam coverage from 0 to 1 for every grid
// point, in rows along X, as the last submitted frame left it.
//
// It waits for the device to go idle and submits a copy of its own, so it is
// for checks and tools, not for a frame loop -- `task waterfx` compares it
// against the CPU's prediction of the same wave function.
func (f *WaterFoam) Coverage() ([]float32, error) {
	if f.destroyed || f.image == nil {
		return nil, errors.New("water foam: target destroyed")
	}
	r := f.r
	size := f.width * f.height * 2
	if _, err := r.deviceDriver.DeviceWaitIdle(); err != nil {
		return nil, fmt.Errorf("water foam: wait idle: %w", err)
	}
	buf, mem, err := r.createBuffer(size, core1_0.BufferUsageTransferDst, core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent)
	if err != nil {
		return nil, fmt.Errorf("water foam: readback buffer: %w", err)
	}
	defer func() {
		r.deviceDriver.DestroyBuffer(buf, nil)
		r.deviceDriver.FreeMemory(mem, nil)
	}()
	cmd, err := r.beginSingleTimeCommands()
	if err != nil {
		return nil, err
	}
	img := f.image.images[0]
	sub := core1_0.ImageSubresourceRange{AspectMask: core1_0.ImageAspectColor, LevelCount: 1, LayerCount: 1}
	barrier := func(src, dst core1_0.PipelineStageFlags, from, to core1_0.ImageLayout, srcAccess, dstAccess core1_0.AccessFlags) error {
		return r.deviceDriver.CmdPipelineBarrier(cmd, src, dst, 0, nil, nil, []core1_0.ImageMemoryBarrier{{
			OldLayout: from, NewLayout: to, SrcQueueFamilyIndex: -1, DstQueueFamilyIndex: -1,
			Image: img, SubresourceRange: sub, SrcAccessMask: srcAccess, DstAccessMask: dstAccess}})
	}
	// The target rests sampled between frames; the device is idle, so the only
	// work to order against is this copy.
	err = barrier(core1_0.PipelineStageAllCommands, core1_0.PipelineStageTransfer,
		core1_0.ImageLayoutShaderReadOnlyOptimal, core1_0.ImageLayoutTransferSrcOptimal, core1_0.AccessShaderWrite, core1_0.AccessTransferRead)
	if err == nil {
		err = r.deviceDriver.CmdCopyImageToBuffer(cmd, img, core1_0.ImageLayoutTransferSrcOptimal, buf, core1_0.BufferImageCopy{
			ImageSubresource: core1_0.ImageSubresourceLayers{AspectMask: core1_0.ImageAspectColor, LayerCount: 1},
			ImageExtent:      core1_0.Extent3D{Width: f.width, Height: f.height, Depth: 1}})
	}
	if err == nil {
		err = barrier(core1_0.PipelineStageTransfer, core1_0.PipelineStageAllCommands,
			core1_0.ImageLayoutTransferSrcOptimal, core1_0.ImageLayoutShaderReadOnlyOptimal, core1_0.AccessTransferRead, core1_0.AccessShaderRead)
	}
	if err != nil {
		r.deviceDriver.FreeCommandBuffers(cmd)
		return nil, fmt.Errorf("water foam: record readback: %w", err)
	}
	if err = r.endSingleTimeCommands(cmd); err != nil {
		return nil, fmt.Errorf("water foam: submit readback: %w", err)
	}
	ptr, _, err := r.deviceDriver.MapMemory(mem, 0, size, 0)
	if err != nil {
		return nil, fmt.Errorf("water foam: map readback: %w", err)
	}
	defer r.deviceDriver.UnmapMemory(mem)
	raw := unsafe.Slice((*byte)(ptr), size)
	out := make([]float32, f.width*f.height)
	for i := range out {
		out[i] = halfToFloat(binary.LittleEndian.Uint16(raw[2*i:]))
	}
	return out, nil
}

// halfToFloat widens an IEEE 754 binary16 value.
func halfToFloat(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exp := uint32(h>>10) & 0x1f
	frac := uint32(h) & 0x3ff
	switch {
	case exp == 0 && frac == 0:
		return math.Float32frombits(sign)
	case exp == 0:
		// Subnormal: normalise into float32's range.
		e := uint32(127 - 15 + 1)
		for frac&0x400 == 0 {
			frac <<= 1
			e--
		}
		return math.Float32frombits(sign | e<<23 | (frac&0x3ff)<<13)
	case exp == 0x1f:
		return math.Float32frombits(sign | 0xff<<23 | frac<<13)
	default:
		return math.Float32frombits(sign | (exp+127-15)<<23 | frac<<13)
	}
}
