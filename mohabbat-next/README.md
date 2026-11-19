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

The production host (`Program.cs`) enters the runtime slice with the embedded
application WASM module and forwards application arguments to the guest. For
standalone WASM execution (`-r` and `-t`), `go run ./mohabbat-next/build -runner`
assembles a separate payload-free runner (`Runner.cs`) containing only the
Windows PAL and `zwasm` runtime slices, which accepts `<module.wasm> [args...]`.

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

`go run ./release --test-all` delegates to the same launcher before building
release vegetables.

It runs native Go suites, Windows x64-emulated Go suites on Windows ARM64,
Go suites compiled for Rusticated WASM and executed via `go tool test2json` on
both the payload-free Mohabbat-Next runner (`wasm-mohabbat`) and the Node
host (`wasm-node`), Rust tests for `wasm32-rusticated-unknown-unknown` using
the testing-enabled Rusticated sysroot (`rusticated-test`) and the payload-free
runner, native Zig unit suites via `zig_test_runner.zig`, and Node TAP suites in
`mohabbat/washmhost/js/node` and `kabibi/vscode`. `-skip-native`,
`-skip-x64-emulated`, `-skip-wasm`, `-skip-rust`, `-skip-zig`, and `-skip-node`
select subsets; `-skip-build` reuses `target/test-host/MohabbatNextTestHost.exe`.
The inactivity timeout defaults to three minutes and the per-suite timeout
defaults to thirty minutes. Override them with `-stdio-idle-timeout` and
`-suite-timeout`.

Each invocation creates `target/test-runs/<run-id>/` with streamed
`events.jsonl`, a final `summary.json` (including per-runner/target counts),
`manifest.json` (host, filters, toolchain probes, and aggregate counts), and
per-suite stdout/stderr logs. Filter by a project path or package suffix with
`-p`; comma-separated filters are supported. Unavailable runner/target
combinations are emitted as `disabled` events with probe and symptom. The
launcher exits unsuccessfully if the selection executes no test cases or any
runner fails.

Go WASM suites are not silently excluded: unsupported or failing packages
remain visible as failures. Linux/WSL execution is deferred.
