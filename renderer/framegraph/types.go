package framegraph

import (
	"math"
	"slices"

	"github.com/vkngwrapper/core/v3/core1_0"
)

type ResourceID int
type NodeID int

// ImageLayoutPresentSrc is VK_IMAGE_LAYOUT_PRESENT_SRC_KHR. Keeping its value
// here avoids importing an extension package just to describe an imported image.
const ImageLayoutPresentSrc core1_0.ImageLayout = 1000001002

// Extent is swapchain-relative unless both Fixed dimensions are nonzero.
type Extent struct {
	Scale   float32
	RoundUp bool
	Fixed   [2]uint32
}

// Size applies bloom's floor or clouds' ceiling, clamping each dimension to 1.
// Build validates extents before an executor uses them.
func (e Extent) Size(width, height uint32) [2]uint32 {
	if e.Fixed != [2]uint32{} {
		return e.Fixed
	}
	out := [2]uint32{width, height}
	for i, v := range out {
		scaled := float64(v) * float64(e.Scale)
		if e.RoundUp {
			scaled = math.Ceil(scaled)
		}
		out[i] = uint32(max(1, min(scaled, math.MaxUint32)))
	}
	return out
}

type ImageDesc struct {
	Name          string
	Format        core1_0.Format
	Extent        Extent
	Samples       core1_0.SampleCountFlags
	Aspect        core1_0.ImageAspectFlags
	Layers        uint32 // Zero means one; arrays must be imported.
	Instances     int    // Zero means one; the executor chooses the physical copy.
	Persistent    bool
	Imported      bool
	InitialLayout core1_0.ImageLayout // Imports only; contents exist unless Undefined.
	Usage         core1_0.ImageUsageFlags
}

type Access int

// BufferDesc describes one logical buffer. Persistent and imported contents
// must be initialized by the owner before any graph reads them.
type BufferDesc struct {
	Name                 string
	Size                 int
	Persistent, Imported bool
	Usage                core1_0.BufferUsageFlags
}

const (
	SampledRead Access = iota + 1
	StorageRead
	StorageWrite
	StorageReadWrite
	ColorWrite
	ColorLoadWrite
	DepthWrite
	DepthLoadWrite
	TransferSrc
	TransferDst
	Present
	IndirectRead
	VertexRead
	// DepthSampledRead is a sampled read of a depth image that rests in
	// DepthStencilReadOnlyOptimal rather than ShaderReadOnlyOptimal. Both
	// layouts are legal for sampling depth, and the renderer's shadow maps use
	// the depth one: it is what their descriptors name and what their
	// attachment passes leave them in, so nothing transitions between the
	// cascade write and the shader read. Declaring such a read as SampledRead
	// would derive a barrier naming a layout the image is not in.
	DepthSampledRead
)

type Use struct {
	// FinalLayout declares the layout an attachment pass leaves instead of the
	// resource's resting layout. Undefined keeps the resting layout.
	//
	// On a Legacy node it describes what a hand-recorded pass really does: the
	// scene leaves depth in attachment layout even when later nodes sample it.
	//
	// On a Graphics node it REPLACES the exit transition the compiler would
	// otherwise derive, so naming the attachment's own layout means "leave it an
	// attachment and do not restore resting". That is what the depth prepass
	// needs: its depth rests in the sampled layout whenever an application pass
	// reads scene depth, and bouncing it out to that layout and straight back in
	// for the scene pass is two barriers that do nothing, the second of which is
	// a write-after-write against the prepass's own depth writes with no stage in
	// its source scope that covers them.
	//
	// Requires an attachment access.
	FinalLayout core1_0.ImageLayout
	Resource    ResourceID
	Access      Access
	Clear       *Clear // Only ColorWrite or DepthWrite.
	// HasResolve disambiguates an omitted resolve from a resolve to resource 0.
	// ColorLoadWrite also supports resolves: water reloads multisample colour.
	ResolveTo  ResourceID
	HasResolve bool
	Stages     core1_0.PipelineStageFlags
	// Discard permits an Undefined old/initial layout even when tracked
	// contents exist, as in the renderer's primed bloom downsample targets.
	// ColorWrite, DepthWrite and TransferDst accept it. A discarded transfer
	// destination must be fully overwritten. Resolve targets always discard.
	Discard bool
	// Rewrites declares that this write replaces the resource's contents for
	// the rest of the frame, so later nodes read what it produced. Reads of the
	// same resource at earlier nodes are then an ordering mistake rather than a
	// deliberate look at what the previous frame left, and Build reports them
	// naming the reader. Requires a write access.
	//
	// It exists for a resource whose contents a Legacy node replaces every
	// frame while its layout never changes -- the shadow cascades, which the
	// renderer records by hand. Nothing distinguishes that case from an import
	// read as it stands, so without this flag a dispatch scheduled ahead of the
	// cascade passes would compile and silently sample the previous frame.
	Rewrites bool
}

type Clear struct {
	Color   [4]float32
	Depth   float32
	Stencil uint32
}

type NodeKind int

const (
	Graphics NodeKind = iota + 1
	Compute
	Transfer
	Legacy // Declares the state left by an opaque recorder; no commands derived.
)

type Node struct {
	Name     string
	Kind     NodeKind
	Uses     []Use
	Optional bool
	// OptionalGroup makes the node Optional. Nonzero groups must be contiguous
	// and execute or skip as a unit; layout neutrality is checked at the group's
	// boundaries. Zero retains the per-node Optional behavior.
	OptionalGroup int
	Timed         bool
}

type Graph struct {
	images  []ImageDesc
	buffers map[ResourceID]BufferDesc
	nodes   []Node
}

func New() *Graph { return &Graph{} }

func (g *Graph) AddImage(d ImageDesc) ResourceID {
	id := ResourceID(len(g.images))
	g.images = append(g.images, d)
	return id
}

func (g *Graph) AddBuffer(d BufferDesc) ResourceID {
	id := ResourceID(len(g.images))
	// The common identity table retains names and lifetime flags for validation.
	g.images = append(g.images, ImageDesc{Name: d.Name, Persistent: d.Persistent, Imported: d.Imported})
	if g.buffers == nil {
		g.buffers = make(map[ResourceID]BufferDesc)
	}
	g.buffers[id] = d
	return id
}

// AddNode copies declarations so reusing a caller's scratch cannot change a graph.
func (g *Graph) AddNode(n Node) NodeID {
	n.Optional = n.Optional || n.OptionalGroup != 0
	n.Uses = slices.Clone(n.Uses)
	for i := range n.Uses {
		if n.Uses[i].Clear != nil {
			c := *n.Uses[i].Clear
			n.Uses[i].Clear = &c
		}
	}
	id := NodeID(len(g.nodes))
	g.nodes = append(g.nodes, n)
	return id
}

type Plan struct {
	Steps         []Step
	Resources     []ResourceInfo
	Timed         []NodeID
	FinalBarriers []Barrier
}

type ResourceInfo struct {
	Buffer     bool
	BufferDesc BufferDesc
	Desc       ImageDesc
	Resting    core1_0.ImageLayout
	Prime      bool // Allocation-time transition; imported images remain owner-managed.
}

type Step struct {
	Node          NodeID
	Barriers      []Barrier
	AfterBarriers []Barrier
	RenderPass    *RenderPassDesc
}

type Barrier struct {
	Buffer               bool
	Offset, Size         int
	Resource             ResourceID
	SrcStage, DstStage   core1_0.PipelineStageFlags
	SrcAccess, DstAccess core1_0.AccessFlags
	OldLayout, NewLayout core1_0.ImageLayout
}

type RenderPassDesc struct {
	// Attachments are colours in Use order, then resolves in colour order,
	// then depth. Colour references define pipeline output locations.
	Attachments []AttachmentDesc
	Color       []int
	Resolve     []int // Parallel to Color; -1 means unused.
	Depth       int   // -1 means unused.
	Extent      Extent
	Samples     core1_0.SampleCountFlags
	Clears      []Clear // Parallel to Attachments.
}

type AttachmentDesc struct {
	Resource       ResourceID
	Format         core1_0.Format
	Samples        core1_0.SampleCountFlags
	LoadOp         core1_0.AttachmentLoadOp
	StoreOp        core1_0.AttachmentStoreOp
	StencilLoadOp  core1_0.AttachmentLoadOp
	StencilStoreOp core1_0.AttachmentStoreOp
	InitialLayout  core1_0.ImageLayout
	FinalLayout    core1_0.ImageLayout
	SubpassLayout  core1_0.ImageLayout
}
