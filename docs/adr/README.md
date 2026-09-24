# Architecture decision records

ADRs record significant choices, the constraints behind them, and their
consequences. Keep one decision per record, alongside the code it affects.
Start with the index below; use [the template](template.md) for a new record.

## Index

| ADR | Decision | Status | Recorded |
|---|---|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions in the repository | Accepted | 2026-09-22 |
| [0002](0002-keep-game-ownership-outside-the-engine.md) | Keep game ownership outside the engine | Accepted | 2026-09-22 |
| [0003](0003-isolate-examples-in-a-separate-module.md) | Isolate examples in a separate Go module | Accepted | 2026-09-22 |
| [0004](0004-ship-runtime-assets-in-the-module.md) | Ship runtime assets in the Go module | Accepted | 2026-09-22 |
| [0005](0005-schedule-the-frame-through-a-frame-graph.md) | Schedule the frame through a frame graph | Accepted | 2026-09-23 |
| [0006](0006-expose-application-graphics-nodes.md) | Expose application graphics nodes and sampled targets | Accepted | 2026-09-23 |
| [0007](0007-cpu-selected-instance-lod.md) | CPU-selected instance LOD, coverage and deferred buffers | Accepted | 2026-09-24 |
| [0008](0008-buffer-resources-and-gpu-draw-generation.md) | Buffer resources, storage buffers and GPU draw generation | Accepted | 2026-09-24 |

Records 0002–0004 document existing decisions retrospectively. Their recorded
date is when the ADR was written, not an assertion about when the original
decision was made. This is an initial set; backfill other decisions as their
subsystems are touched, using existing code and documentation as evidence.

## When to write one

Write an ADR when a choice affects multiple subsystems or constrains how games
use, extend, build, or ship the engine. Examples include engine/game ownership,
public API contracts, resource lifetime, concurrency, rendering conventions,
and dependency or asset distribution. A significant tradeoff that would be
costly to rediscover also belongs here.

Routine bug fixes, local refactors, and implementation details already explained
by a short code comment do not need an ADR. Link to an existing record when a
change simply implements its decision.

## Workflow

1. Read related records. Copy `template.md` to `NNNN-short-decision-title.md`,
   using the next unused four-digit number after the highest in this directory.
   Never reuse or renumber an existing record; resolve numbering collisions
   before merging a new record.
2. Set `Recorded` to the date the record is written. Describe the actual
   problem, decision, alternatives, and consequences. Link to code, capability
   docs, issues, or measurements that support it. For retrospective records,
   say so explicitly and distinguish known facts from unknown history.
3. Use `Proposed` while a choice is unresolved. Review it with the associated
   change and mark it `Accepted` when agreed. A retrospective record can start
   as `Accepted` when it documents an established decision supported by the
   repository. ADRs use the normal change review process, with no separate
   approval ceremony.
4. Add the record to this index in the same change. Update affected rules in
   `AGENTS.md` and usage instructions in `docs/agents/` when behavior changes.
5. Preserve accepted rationale. To reverse or materially change it, add a new
   ADR with a `Supersedes` link. When the replacement is accepted, mark the old
   record `Superseded` and add a `Superseded by` link back. Update both index
   entries. Status changes, links, and factual corrections may be edited in
   place; do not rewrite an old decision to make it appear to have always been
   the new one.

## Statuses

| Status | Meaning |
|---|---|
| Proposed | Under discussion; not a current architectural requirement |
| Accepted | Agreed and currently applicable |
| Rejected | Considered but not adopted; retained with the reason |
| Deprecated | No longer applicable, with an explanation and no direct replacement |
| Superseded | Replaced by another accepted ADR, linked from the record |

Retain rejected, deprecated, and superseded records in the index. Record the
date and reason for later status changes in the affected record.

ADRs explain **why**. [AGENTS.md](../../AGENTS.md) keeps the actionable rules,
and [capability docs](../agents/README.md) keep current APIs and working examples.
Link those sources instead of copying detailed implementation instructions.
