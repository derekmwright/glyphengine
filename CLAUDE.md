# CLAUDE.md

Read [`AGENTS.md`](AGENTS.md) first. It is the tool-neutral guide — repository
layout, the rules that are expensive to violate, and how to verify a change —
and it is kept current. This file only adds what is specific to working here
with Claude Code.

## Before changing anything

Find the capability page in [`docs/agents/`](docs/agents) that covers what you
are touching. Each carries YAML frontmatter (`id`, `capability`, `api`,
`example`, `run`), so match on `api` for the identifier you are about to change
and read that one page rather than the whole subsystem. They document failure
modes, especially the silent ones.

If you change an API, update its page and bump `verified`. A page that lies is
worse than no page — an agent will trust it.

## Verify with the real thing

```
task ci        # lint, build, test, race
task smoke     # renders real frames of every example
task validate  # every example under the Vulkan validation layer, must be silent
```

`task ci` is the minimum. Run `task validate` for anything touching the
renderer: Vulkan misuse is usually invisible until it corrupts a frame on
hardware you do not own, and the layer is the only cheap signal.

## Make checks prove something

This engine has repeatedly produced green results that meant nothing:

- A prediction-replay test passed because the "previous transform" aliased the
  live one, so nothing was being compared.
- A teardown test reported zero leaks because teardown never ran, so the
  validation layer had nothing to inspect.
- A smoothness metric showed no improvement because the test body was
  accelerating, which inflates variance regardless of what you are measuring.

When adding a check for a bug, **break the fix and confirm the check fails.**
If it still passes, the check is decoration. Several tests here have comments
recording exactly that verification.

## Do not inherit a comment's conclusion

A comment that says it prevents an artifact is telling you what someone
believed, not what is true. Two in `grass.frag` were confidently wrong -- one
blamed a normal source the shader never computes -- and reading them as settled
sent this session chasing Fresnel and normals for hours while the real cause
sat in the albedo. `AGENTS.md` rules 12 and 13 are the standing version of
this. Turn the claim off, measure it, and either write the number next to it or
delete it.

Renders are only comparable under `GLYPHENGINE_FIXED_FRAME_TIME`; without it
two runs of the same build differ by as much as the change you are measuring,
and single-run before/after readings are coin tosses. `task determinism` is the
gate.

## Measure, do not assume

Claims about frame pacing, CPU cost, and timing have been wrong here more often
than not — the software frame cap was assumed to be doing the pacing when vsync
already was, and it cost 0.72 CPU cores for nothing. Numbers in commit messages
and docs come from actual measurement. Keep it that way.

## House style

- Comments explain **why**, not what. The engine is full of non-obvious
  decisions (reverse-Z, entity-hash partitioning, the `Static` tag) and those
  are what comments are for.
- Match surrounding code. This is a port; consistency beats personal taste.
- Never add AI attribution to commits or PRs — no `Co-Authored-By`, no
  generation notices. See `AGENTS.md` rule 11.
