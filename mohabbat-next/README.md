# Mohabbat Next Architecture

This directory is an isolated implementation of the first Mohabbat redesign
delivery. It does not modify or integrate with the existing `mohabbat/`
implementation.

## Components

The source tree is organized by architectural role:

```text
pal/
  abi/guest.rs            # payload/runtime-to-PAL contract
  windows/                # Windows PAL
  unix/                   # Linux and macOS PAL
brot/
  unix/                   # native Brot launchers
  windows-host/           # CLR host and component packer
payload/                  # OS-independent raw-entry payloads
runtime/                  # movable Cranelift runtime
build/                    # Go build and assembly orchestration
test/                     # unit and cross-platform acceptance tests
```

The test runner writes its assembled Windows acceptance artifact to
`brot/windows-host/out/MohabbatNext.exe`; the default builder output is
`release/dist/MohabbatNext.exe`. On Windows, the AnyCPU CLR host identifies
the native processor architecture, decompresses the matching components from
embedded Zstandard resources, and transfers control to the platform
abstraction.

The Go builder compiles separate freestanding PAL and payload binaries for
x86-64 and AArch64. The PAL receives a narrow ABI from the host, resolves
platform APIs, populates the canonical `PalAbiTable` at payload offset
`-0x20000`, maps the hello payload into executable memory, and invokes it.
`read`, `write`, and `process_exit` are implemented for the payload; every
other function slot is a valid typed unsupported stub, rather than a null
function pointer. The hello payload owns the banner, input loop, line trimming,
echoing, and empty-line exit behavior; it contains no OS-specific calls.

The ABI definition in [`pal/abi/guest.rs`](pal/abi/guest.rs) fixes the table
layout and calling convention: Windows x64 on x86-64 and C on AArch64; the
AArch64 build reserves x18.

Payload stdio uses the ABI's asynchronous `Overlapped` contract. The payload
stores its phase, buffers, and pending `Overlapped` in persistent globals,
then returns to the PAL after submitting each read or write. Windows and Unix
perform blocking standard-handle operations on worker threads and re-enter the
payload after completion. The payload consumes completion errors and byte
counts, including short writes, before continuing. It never polls the
completion structure or performs blocking stdio itself.

The Unix path builds platform-specific Brot launchers. The final deliverable
is one MZ/PE `MohabbatNext.exe`; its DOS stub begins with a shell-valid
assignment and dispatches POSIX execution to Bash. The appended bootstrap is
read through process substitution so the original terminal input remains
available. It selects Linux x86-64/AArch64, Termux/Android AArch64, or macOS
x86-64/AArch64, extracts the matching Brot executable to a private `mktemp`
path prefixed with `🐚︎`, and launches it. Brot unlinks its executable path at
startup, then reads the Zstandard-compressed platform abstraction and
ISA-matched hello payload from the bundle, decompresses them, and transfers
control through the narrow host ABI.

The temporary-file path is the baseline, not a memory-only guarantee: the
selected temporary filesystem may be disk-backed, may use swap, or may be
mounted `noexec`. In that case launch fails explicitly. Unlinking removes the
name but does not erase the file's backing pages. Linux `memfd` and other
in-memory launch mechanisms remain optional paths, not prerequisites.

The Go builder is the canonical build path. It compiles the payloads, PALs,
runtime, and Brot launchers for supported targets, validates them, builds the
Windows CLR host, and assembles the single executable. Build intermediates
are kept under `mohabbat-next/.build/`.

## Build and run

From the repository root, run:

```text
go run ./mohabbat-next/build
release/dist/MohabbatNext.exe
```

Use `go run ./mohabbat-next/build -o <path>` to select another output, or
`go run ./mohabbat-next/build -windows-host-only` to build only the Windows
host. Building requires nightly Rust with `rust-src`, Cargo, LLVM linker and
inspection tools, .NET, and the .NET Framework compiler.

The EXE requires a 64-bit Windows CLR. Its application manifest advertises
AMD64 and ARM64 support, and the CLR selects the matching embedded binaries.

## Acceptance

The unified Go test runner builds fresh artifacts and runs package tests plus
the available Windows, Unix, and architecture-specific acceptance suites:

```text
go run ./mohabbat-next/test
```

Unavailable target environments are recorded as disabled tests. The build
checks flat binaries for relocations, TLS, forbidden system-call/trap
instructions, and size limits.

Run the Unix interactive acceptance on a target with:

```bash
python3 mohabbat-next/test/acceptance/test_bundle.py bash release/dist/MohabbatNext.exe
```

The Go build tests also assemble a synthetic PE fixture and verify its DOS
stub/Bash handoff. The unified test runner exercises the actual assembled
bundle on the platforms available to the host.

Windows acceptance and Linux AArch64 end-to-end acceptance passed in the
previous validation environment. Linux x86-64, Android, and macOS Brot
launchers and native runtimes still require validation on those target
environments.
