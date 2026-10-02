// Package shaderinclude holds this module's gate on the engine's exported GLSL
// include set.
//
// It ships nothing. An x package that writes GLSL compiles it against
// shaders/include at build time rather than vendoring a copy, and the thing that
// has to keep working is that mechanism: write the embedded set to a directory,
// run glslc with -I against it, and get SPIR-V out. Nothing in Go notices when
// that breaks -- a renamed function in lighting.inc, a new uniform the include
// expects the shader to declare, a fragment dropped from the embed pattern -- so
// the only way the x module finds out before a draw call is to actually run a
// compile in a test.
//
// It is deliberately test-only and deliberately in internal/: once x/water
// exists it will carry a compile of its own real shaders, and this package is
// then the minimal case that still fails loudly if the include export itself
// regresses, independently of any package's shader.
package shaderinclude
