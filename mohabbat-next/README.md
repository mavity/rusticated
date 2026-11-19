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
Application arguments are forwarded to the guest. The Windows test launcher
can select a test WASM module with the host-only `--wasm <path> -- <args>`
options; normal application builds continue to use the embedded module.

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

## Test launcher

From the repository root, run the unified test launcher:

```text
go run ./mohabbat-next/test
go run ./mohabbat-next/test -p kabibi
```

It runs native Go suites, eligible Go suites compiled for Rusticated WASM and
executed by the Windows Mohabbat-Next host, Rust workspace tests for the
Rusticated WASM target, and native Zig unit suites available on the current
host. `-skip-native`, `-skip-wasm`, `-skip-rust`, and `-skip-zig` select
subsets; `-skip-build` reuses `target/test-host/MohabbatNextTestHost.exe`.
The inactivity timeout defaults to three minutes and the per-suite timeout
defaults to thirty minutes. Override them with `-stdio-idle-timeout` and
`-suite-timeout`.

Each invocation creates `target/test-runs/<run-id>/` with streamed
`events.jsonl`, a final `summary.json`, and per-suite stdout/stderr logs.
Filter by a project path or package suffix with `-p`; comma-separated filters
are supported. The launcher exits unsuccessfully if the selection executes no
test cases or any runner fails.

Rusticated-WASM tests explicitly exclude Go custom-sysroot packages that cannot
be tested as standalone modules, skip cases requiring host Go/Cargo toolchains,
and skip cases that exceed the guest's memory, stack, or inactivity budgets.
Those cases remain enabled in native Go runs.

The current custom Rust target cannot build Cargo's standard unit-test
harness: its sysroot lacks Rust's `test` crate, and compiling the Rusticated
`std` crate with `cfg(test)` also produces duplicate `panic_impl` lang items.
The launcher records these as test-run failures rather than treating a
compile-only result as a pass. A Rusticated-specific test harness, including
dedicated native Rusticated ABI coverage, is still needed. Linux/WSL test-host
support is also not implemented yet.
