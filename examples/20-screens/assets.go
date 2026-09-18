package main

import "embed"

// Committed alongside the example so it runs with no setup, the same way
// 13-ui's font is.
//
//go:embed assets
var assetsFS embed.FS
