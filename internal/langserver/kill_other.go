//go:build !linux

package langserver

// killOrphanOnPort is a no-op on non-Linux platforms. Windows / macOS dev
// boxes don't host the production LS pool anyway, and the /proc scan trick
// only works on Linux. If someone's dev env has a stray LS on :42100 they
// can kill it by hand — not worth wiring a cross-platform implementation.
func killOrphanOnPort(_ int) {}
