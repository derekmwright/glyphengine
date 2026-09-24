// Package framegraph compiles ordered image and buffer uses without a device.
// It does not record commands, reorder nodes, alias memory, or choose instances.
//
// A resource rests in ShaderReadOnlyOptimal if sampled, General if used for
// storage, or its attachment layout otherwise; presented imports rest in
// PresentSrc. Persistent images start there and need a one-time transition.
// Imports start in their declared InitialLayout. Other images start Undefined
// every frame, so their contents cannot be read before a guaranteed write.
// Buffers have no layout or priming transition. Persistent/imported buffers
// begin with a conservative preceding-submission memory scope. Buffer hazards
// use pipeline barriers, including graphics vertex and indirect reads before
// dynamic-rendering entry. Buffer and image barriers share stage-pair grouping.
// Priming establishes a layout, not valid history pixels: the owner must also
// initialize any history contents its shaders read.
//
// Every graphics attachment enters its attachment layout through Step.Barriers.
// Step.AfterBarriers exposes attachment writes and restores resting layouts after
// CmdEndRendering, including inline resolves. Declared consumer stages include
// vertex, fragment, compute and next-frame readers. Legacy nodes own their own
// explicit transitions and declare the layouts they leave behind.
//
// Each maximal adjacent run of Step.Barriers with identical SrcStage and
// DstStage is one pipeline-barrier call, retaining resource order. The same
// grouping applies to Step.AfterBarriers and Plan.FinalBarriers. An executor can size its scratch from
// these runs at build time and walk the plan without maps or frame allocations.
// A barrier from Undefined has no source work: its source access is zero and its
// source stage is TopOfPipe when alone. Adjacent undefined barriers with matching
// destination stages adopt the preceding defined barrier's source stage, or the
// following one's when they lead the run. Defined source masks and order stay
// unchanged; runs containing only undefined barriers retain TopOfPipe.
// FinalBarriers restore resting layouts after the last node when necessary.
//
// Optional means the entire step, including barriers, may be skipped. Nonzero
// Node.OptionalGroup makes every member Optional; members must be contiguous and
// execute or skip together. Every touched resource must have the same layout at
// group entry and exit. Ungrouped Optional nodes are checked individually. Writes
// inside a group feed its later members, but cannot initialize transient contents
// for reads after the group. The merged synchronization state covers both paths.
// A group-private copy target can be primed in its resting layout and explicitly
// discarded by TransferDst while still retaining that layout when the group skips.
package framegraph
