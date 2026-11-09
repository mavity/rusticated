# Mohabbat Next

This directory is an isolated implementation of the first Mohabbat redesign
delivery. It does not modify or integrate with the existing `mohabbat/`
implementation.

## Windows and Unix probe components

The Windows path ships one AnyCPU CLR executable,
`probe/hosts/windows/out/MohabbatProbe.exe`. At launch, the CLR identifies the
native processor architecture, decompresses the matching components from
embedded Zstandard resources, and transfers control to the platform
abstraction.

The executable contains four independently compiled movable binaries:

| Architecture | Windows platform abstraction | OS-blind hello-world payload |
| --- | --- | --- |
| x86-64 | `probe/hosts/windows/.build/platform/platform-windows-x86_64.bin` | `probe/payload/out/probe-x86_64.bin` |
| AArch64 | `probe/hosts/windows/.build/platform/platform-windows-aarch64.bin` | `probe/payload/out/probe-aarch64.bin` |

The platform abstraction receives a narrow ABI from the CLR: `LoadLibrary`,
`GetProcAddress`, executable-memory allocation/free, and the matching hello
payload pointer and size. It resolves Windows APIs, populates the canonical
47-field `PalAbiTable` at payload offset `-0x20000`, maps the hello payload into
executable memory, and invokes it. `read`, `write`, and `process_exit` are
implemented for the probe, with `process_exit` using a callback that matches
the table's target ABI; every other function slot is a valid typed
unsupported stub, rather than a null function pointer. The hello payload owns
the banner, input loop, line trimming, echoing, and empty-line exit behavior;
it contains no OS-specific calls.

Probe stdio uses the ABI's asynchronous `Overlapped` contract. The payload
stores its phase, buffers, and pending `Overlapped` in persistent globals, then
returns to the platform after submitting each read or write. Windows performs
the blocking standard-handle operation on a worker thread and waits for it
before re-entering the payload; Unix does the same with a pthread. The payload
consumes completion errors and byte counts, including short writes, before
continuing. It never polls the completion structure or performs blocking
stdio itself.

The two payloads are shared by all future OS platform abstractions for their
respective instruction sets.

The hello payload and Windows platform abstraction are freestanding `no_std`
Rust components built for `x86_64-unknown-none` and `aarch64-unknown-none`,
using `core` rather than the Rusticated WASM sysroot. The shared
[`probe/pal_abi.rs`](probe/pal_abi.rs) definition fixes the table layout and
calling convention: Windows x64 on x86-64 and C on AArch64; the AArch64 build
reserves x18.

The final deliverable is one MZ/PE `MohabbatProbe.exe`. Its DOS stub begins
with a shell-valid assignment and dispatches POSIX execution to Bash; the
appended bootstrap is read through process substitution so the original
terminal input remains available to the probe. The bootstrap selects Linux
x86-64/AArch64, Termux/Android AArch64, or macOS x86-64/AArch64, extracts the
matching Brot executable to a private `mktemp` path prefixed with `🐚︎`, and
launches it. Brot unlinks its executable path at startup, then reads the
Zstandard-compressed platform abstraction and ISA-matched hello payload from
the bundle, decompresses them, and transfers control through the narrow host
ABI. The Unix abstractions are freestanding Rust binaries; the Brot runner is
a native Rust executable. The assembler renders component offsets and appends
the Unix assets and Bash bootstrap after the Windows PE image.

The temporary-file path is the baseline, not a memory-only guarantee: the
selected temporary filesystem may be disk-backed, may use swap, or may be
mounted `noexec`. In that case launch fails explicitly. Unlinking removes the
name but does not erase the file's backing pages. Linux `memfd` and other
in-memory launch mechanisms remain optional paths, not prerequisites.

The Go builder is the canonical build path. It compiles the flat payloads,
platform abstractions, and Brot launchers for all supported targets, validates
them, builds the Windows CLR host, and assembles the single executable. No
per-target asset collection is required.

## Build and run

From the repository root, run:

```text
go run ./mohabbat-next/build
release/dist/MohabbatProbe.exe
```

Use `go run ./mohabbat-next/build -o <path>` to select another output, or
`go run ./mohabbat-next/build -windows-host-only` to build only the Windows
host. Build tools and intermediates are kept under `mohabbat-next/.build/`.
Building requires nightly Rust with `rust-src`, Cargo, LLVM linker and
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
python3 mohabbat-next/probe/acceptance/test_probe.py bash release/dist/MohabbatProbe.exe
```

The Go build tests also assemble a synthetic PE fixture and verify its DOS
stub/Bash handoff. The unified test runner exercises the actual assembled
bundle on the platforms available to the host.

Windows acceptance and Linux AArch64 end-to-end acceptance passed in the
current environment. The Linux x86-64, Android, and macOS Brot runners and
native runtimes still require validation on those target environments.
