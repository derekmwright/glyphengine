---
id: agent-docs-schema
title: Capability doc schema
summary: How the machine-readable capability docs in this directory are structured.
capability: meta
status: stable
since: v0.1.0
api: []
requires: []
assets: none
verified: 2026-07-28
---

# Capability docs

Each file in `docs/agents/` documents **one thing an agent might be asked to
do** with the engine, and carries YAML frontmatter so a harness can index the
directory without parsing prose.

The goal is that an agent can answer "how do I do X with this engine?" by
matching on frontmatter, then reading one focused page — rather than reading the
whole codebase.

## Frontmatter schema

```yaml
---
id: render-triangle          # stable kebab-case slug, unique, never renamed
title: Render a triangle     # human-readable, one line
summary: >                   # one sentence; what a harness shows in a list
  Verify the Vulkan stack end to end with no assets.
capability: rendering        # top-level grouping (see below)
status: stable               # stable | experimental | planned | deprecated
since: v0.1.0                # engine version the API landed in
api:                         # exact Go identifiers this page covers
  - window.New
  - renderer.Renderer.DrawTriangle
example: examples/01-triangle # path to runnable code, or omit
run: task example:01-triangle # exact command to see it work, or omit
requires:                    # environment prerequisites beyond a Go toolchain
  - cgo
  - vulkan-runtime
assets: none                 # none | procedural | bundled | user-supplied
verified: 2026-07-28         # date the page was last checked against the code
---
```

### Field notes

- **`id`** is a permanent identifier. Change `title` freely; never change `id`.
- **`capability`** is the primary index key. Values in use: `rendering`,
  `windowing`, `input`, `ecs`, `physics`, `navigation`, `terrain`, `animation`,
  `water`, `lighting`, `environment`, `meta`. Reserved for pages not yet
  written: `audio`, `ui`, `assets`.

  Keep this list matching what the pages actually declare. It drifted once
  already — `water`, `lighting` and `environment` were in use for a while
  without appearing here, which makes the index look authoritative while being
  incomplete.
- **`status: planned`** pages are allowed and encouraged — they tell an agent
  that something does *not* exist yet, which prevents it from inventing an API.
  A planned page must not show code that cannot run.
- **`api`** entries use Go notation: `package.Function` or
  `package.Type.Method`. These are the join keys — an agent that finds
  `renderer.Renderer.DrawTriangle` in code can find the page explaining it.
- **`verified`** is a staleness signal. Anything older than the last API break
  should be treated as suspect.

## Body conventions

Write for an agent that will copy code out of the page:

1. **Lead with working code.** Complete and compilable, imports included — not
   a fragment with `...` in it.
2. **State the failure mode.** If getting it wrong produces a black screen
   rather than an error, say so explicitly. Silent failures are the expensive
   ones.
3. **Link the runnable example** rather than duplicating it, so there is one
   source of truth that CI actually compiles.
4. **Do not document aspirations.** If it does not work today, mark it
   `status: planned` and say what is missing.

## Adding a page

1. Copy the frontmatter block above; pick a new `id`.
2. Write the body.
3. Add a runnable example under `examples/` if the capability is demonstrable.
4. Verify the code you pasted actually compiles — `task build`.
5. Set `verified` to today.
