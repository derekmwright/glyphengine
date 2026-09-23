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
)

type Use struct {
	Resource ResourceID
	Access   Access
	Clear    *Clear // Only ColorWrite or DepthWrite.
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
	Dependencies  []core1_0.SubpassDependency // Nil derives; non-nil replaces, even empty.
	// AttachmentOrder overrides the default colour/resolve/depth order. Existing
	// scene pipelines use colour/depth/resolve, and compatibility includes refs.
	// A non-nil override must list every attachment exactly once.
	AttachmentOrder []ResourceID
}

type Graph struct {
	images []ImageDesc
	nodes  []Node
}

func New() *Graph { return &Graph{} }

func (g *Graph) AddImage(d ImageDesc) ResourceID {
	id := ResourceID(len(g.images))
	g.images = append(g.images, d)
	return id
}

// AddNode copies declarations so reusing a caller's scratch cannot change a graph.
func (g *Graph) AddNode(n Node) NodeID {
	n.Optional = n.Optional || n.OptionalGroup != 0
	n.Uses = slices.Clone(n.Uses)
	n.Dependencies = slices.Clone(n.Dependencies)
	n.AttachmentOrder = slices.Clone(n.AttachmentOrder)
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
	Desc    ImageDesc
	Resting core1_0.ImageLayout
	Prime   bool // Allocation-time transition; imported images remain owner-managed.
}

type Step struct {
	Node       NodeID
	Barriers   []Barrier
	RenderPass *RenderPassDesc
}

type Barrier struct {
	Resource             ResourceID
	SrcStage, DstStage   core1_0.PipelineStageFlags
	SrcAccess, DstAccess core1_0.AccessFlags
	OldLayout, NewLayout core1_0.ImageLayout
}

type RenderPassDesc struct {
	// Attachments are colours in Use order, then resolves in colour order,
	// then depth, unless Node.AttachmentOrder overrides it. Framebuffers and
	// pipeline creation must use this same order.
	Attachments  []AttachmentDesc
	Color        []int
	Resolve      []int // Parallel to Color; -1 means unused.
	Depth        int   // -1 means unused.
	Dependencies []core1_0.SubpassDependency
	Extent       Extent
	Samples      core1_0.SampleCountFlags
	Clears       []Clear // Parallel to Attachments.
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
