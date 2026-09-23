# 0003: Isolate examples in a separate Go module

- Status: Accepted
- Recorded: 2026-09-22

Retrospective record of the existing module layout. The original decision date
is not recorded here; the module files and their comments document this choice.

## Context

Consumers need the engine library without downloading the example programs and
their assets. Contributors need those same examples in a clone, building
against their local engine edits.

## Decision

Keep the engine at the repository root and the examples in a separate Go
module under `examples/`. Join both with `go.work` for local development.

Never add a `replace` directive to the root `go.mod`: replacements in a
dependency are ignored by consuming modules, which would make local builds use
different dependency code than consumers receive.

The examples may replace the engine module with `../` because they are
unpublished main programs. That replacement also supports building examples
from a clone without workspace mode.

## Alternatives

- A single module would include example code and assets in the engine module's
  distribution.
- Separate repositories would separate downloads, but make examples and engine
  changes harder to develop and review together.
- A root-level replacement would solve a local dependency problem without
  solving it for users of the library.

## Consequences

- `go get` consumers receive the engine module; a clone includes the examples.
- Contributors maintain two module manifests and verify that both build.
- Engine dependencies must resolve correctly for consumers without relying on
  the workspace or a root-level replacement.

## References and evidence

These files establish the current two-module layout; no module manifests are
changed by this ADR.

- [Engine module](../../go.mod)
- [Examples module and local replacement rationale](../../examples/go.mod)
- [Workspace and layout rationale](../../go.work)
- [Build tasks for the engine and examples](../../Taskfile.yml)
- [Agent guide](../../AGENTS.md), rule 1
