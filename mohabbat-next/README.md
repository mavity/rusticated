# Mohabbat Next Architecture

This directory contains an isolated implementation of the Mohabbat redesign.
It does not modify or integrate with the existing `mohabbat/` implementation.

## Components

```text
pal/
  abi/                    # Runtime-to-PAL contract
  windows/                # Windows PAL (Zig freestanding)
  unix/                   # Linux and macOS PALs (Zig freestanding)
brot/
  unix/                   # Native Zig launchers
  windows-host/           # AnyCPU CLR host and Zstandard dependency
runtime/                  # Movable zwasm runtime slices (Zig freestanding)
build/                    # Go build and assembly orchestration
```

The Go builder assembles an application-specific vegetable. Each vegetable
contains the platform launchers, OS/architecture-specific PAL and runtime
components, and the target application's `brain.wasm` in one shared compressed
pool. The Windows host reads the pool through the `MHBTTAIL` trailer; Unix
launchers extract and run the matching Brot component.

The host enters the runtime slice with the embedded application WASM module.
There is no default guest, hello fallback, external-WASM override, or
test-specific command mode. Application arguments are forwarded to the guest.

## Build and run

From the repository root, provide an application directory as the target:

```text
go run ./mohabbat-next/build demo-go
release/dist/demo-go.exe
```

The target must be an application under the repository root. By default the
builder writes `release/dist/<target>.exe`; `-o <path>` selects another output
path but does not remove the required target. Building requires Go, Zig
`0.17.1`, LLVM linker and inspection tools (`ld.lld`, `llvm-objcopy`,
`llvm-readobj`, `llvm-nm`, `llvm-objdump`), .NET, and the .NET Framework
compiler.

The Windows executable requires a 64-bit CLR. Its application manifest
advertises AMD64 and ARM64 support, and the CLR selects the matching embedded
components.
