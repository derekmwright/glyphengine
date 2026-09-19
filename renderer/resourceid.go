package renderer

import "sync/atomic"

// Resources that a draw binds at set 0 carry a small id, handed out in
// creation order, so the draw list can group by them without sorting on an
// address.
//
// RenderObject.SortKey used to put the descriptor set's Vulkan handle in its
// low bits. That grouped correctly — two draws binding the same set landed
// together — but the value is an address, so which group came first was
// decided by where the driver happened to allocate, and that is different in
// every process. Ties between equal keys then resolved differently run to run,
// and the recorded draw sequence was a coin toss even with an identical scene
// (issue #53).
//
// An id fixes that without giving anything up. Grouping only needs the key to
// be EQUAL for draws that bind the same thing and different otherwise; which
// group sorts first is arbitrary either way, so the number of pipeline and
// descriptor binds is unchanged by construction — the partition is identical,
// only the order of the parts moves. What changes is that the order is now the
// same on every frame of every run of the same program.
//
// Creation order, not content: two textures loaded from the same file get two
// ids, as they already got two descriptor sets. The guarantee is that one
// program run twice creates its resources in the same order and therefore
// sorts its draws the same way, which is what a determinism gate can check.
// A game that creates resources in an order that varies between runs — a
// streamer racing on goroutines, say — has a different scene each run and no
// sort key can hide that.
//
// Ids start at 1. Zero is the id of a draw that binds nothing at set 0, which
// keeps untextured draws sorting ahead of textured ones exactly as a zero
// handle did.
var nextResourceID atomic.Uint32

// newResourceID returns the next id. Atomic because nothing stops a game
// creating textures off the frame thread, and two resources sharing an id
// would silently merge two groups in the sort.
func newResourceID() uint32 { return nextResourceID.Add(1) }
