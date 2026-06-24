//go:build ignore

// cshared_darwin.go: obsolete since brot switched from dlopen/dylib to
// spawning washmhost as a standalone process on macOS.  The run_payload CGO
// export is no longer called.  File kept for reference only.

package main
