# 0004: Ship runtime assets in the Go module

- Status: Accepted
- Recorded: 2026-09-22

Retrospective record of the existing shader and asset distribution policy.
The original decision date is not recorded here; the agent guide and shader
build configuration already explain the constraint.

## Context

The engine embeds compiled SPIR-V shaders with `go:embed`. A consumer building
the Go module needs the actual shader bytes present without first running the
repository's authoring tools. Git LFS pointer files cannot satisfy that
requirement because the Go module proxy does not run LFS smudge.

## Decision

Commit compiled `shaders/*.spv` alongside their GLSL sources and embed those
committed files. When changing GLSL, including shared shader includes, run
`task shaders` and include the regenerated SPIR-V in the same change.

Do not use Git LFS. Files required by embedding or asset loaders must be real
content in the repository and module distribution. `glslc` is an authoring
dependency, not a prerequisite for consumers who only build the engine.

## Alternatives

- Compile shaders during each consumer build. This would require the shader
  compiler and an extra generation step before Go can embed the results.
- Store shader binaries or required assets through Git LFS. Module consumers
  would receive pointer stubs where the engine expects real content.
- Fetch required shader assets at runtime. That would introduce a distribution
  and availability requirement the embedded defaults currently avoid.

## Consequences

- Consumers receive the engine's default shaders as part of the module.
- Authors must keep source and compiled artifacts synchronized, and binary
  changes add repository history that cannot be reviewed like GLSL text.
- Building the engine still requires its documented CGo and Vulkan
  prerequisites; embedding shaders only removes the shader compiler requirement.

## References and evidence

These are existing implementation and verification entry points. Shader
compilation and runtime checks were not rerun for this documentation change.

- [Embedded shader declarations](../../shaders/shaders.go)
- [Shader compilation and `shaders:verify` tasks](../../Taskfile.yml)
- [Binary file attributes](../../.gitattributes)
- [Agent guide](../../AGENTS.md), rules 2 and 3
- [Consumer prerequisites](../../README.md#prerequisites)
