# 0001: Record architecture decisions in the repository

- Status: Accepted
- Recorded: 2026-09-22

## Context

GlyphEngine is growing beyond its initial extraction from a game. Architectural
constraints already appear in `AGENTS.md`, capability docs, and code comments,
but there is no dedicated record of decisions and their tradeoffs. Current
instructions alone do not preserve the reasoning when a decision changes.

## Decision

Keep numbered Markdown ADRs in `docs/adr/`, with an index and a reusable
template. Record significant architectural choices with their context,
decision, alternatives, consequences, and supporting references. Include the
record in the same change as the architecture it establishes.

Use the statuses and workflow in [the ADR guide](README.md). Preserve accepted
decisions as history; replace them through a new record with reciprocal links.
Backfill existing decisions when useful, clearly identifying retrospective
records and avoiding invented dates or deliberations.

## Alternatives

- Continue using only comments and current guides. These remain useful for
  local rationale and instructions but provide no indexed decision history.
- Keep decisions only in issues or pull requests. Discussion is useful evidence,
  but a repository record makes the outcome available in a checkout beside the
  implementation.
- Require an ADR for every change. That would bury significant decisions among
  routine fixes and add upkeep without preserving useful architectural context.

## Consequences

- Contributors can find the rationale and prior choices before changing a
  contract, including decisions that have been replaced or rejected.
- Architectural changes carry a small documentation cost: keep the record,
  index, and current instructions consistent.
- Plain Markdown needs no new tooling. Completeness and consistency depend on
  normal change review.
- The initial records are a starting point, not a claim to cover every existing
  architectural decision.

## References and evidence

- [ADR workflow and index](README.md)
- [Record template](template.md)
- [Agent guide](../../AGENTS.md)
- [Capability documentation conventions](../agents/README.md)
