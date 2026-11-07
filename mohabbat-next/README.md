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
payload pointer and size. It resolves Windows APIs, builds the higher-level
stdio/exit dispatch table, maps the hello payload into executable memory, and
invokes it. The hello payload owns the banner, input loop, line trimming,
echoing, and empty-line exit behavior; it contains no OS-specific calls.

The two payloads are shared by all future OS platform abstractions for their
respective instruction sets.

The hello payload and Windows platform abstraction are freestanding `no_std`
Rust components built for `x86_64-unknown-none` and `aarch64-unknown-none`,
using `core` rather than the Rusticated WASM sysroot. Their C-compatible
dispatch uses the Windows x64 calling convention on x86-64 and the C ABI on
AArch64; the AArch64 build reserves x18.

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

Unix builds use Cargo and LLVM directly; there is no CMake or C POSIX host.
Run `bash probe/hosts/unix/build.sh` on each native target: Linux x86-64,
Linux AArch64, Termux/Android AArch64, and macOS x86-64/AArch64. Each run
publishes its Brot runner and flat platform component into
`probe/hosts/unix/out/`; use the same Rust nightly for all runs. The shared
hello payloads are also produced there. After collecting all five Brot
runners and the flat components into that directory, run the Windows build
script below to assemble the single executable. Android builds need the
Android Rust target and a compatible linker. The build fails explicitly if a
required target asset is missing.

## Build and run

On Windows, after collecting the Unix target assets, run:

```powershell
.\probe\hosts\windows\build.ps1
.\probe\hosts\windows\out\MohabbatProbe.exe
```

The Windows build creates the single cross-platform EXE in
`probe/hosts/windows/out/`. `-WindowsHostOnly` builds an intermediate used
only for Windows-specific acceptance; it is not the distributable.
Build tools and intermediates are kept separately under `.build/`. The
build-time component packer uses the pinned `ZstdSharp.Port` 0.8.8 package;
its managed runtime dependencies are embedded in the EXE. Building requires
nightly Rust with `rust-src` for `-Z build-std=core`, Cargo, LLVM linker and
inspection tools, .NET, and the .NET Framework compiler.

The EXE requires a 64-bit Windows CLR. Its application manifest advertises
AMD64 and ARM64 support, and the CLR selects the matching embedded binaries.

## Acceptance

Run the Windows acceptance script after building:

```powershell
.\probe\acceptance\test-windows.ps1
```

It verifies decompression of all four embedded components, interactive input
and echo, EOF handling, and rejection of an overlong input line. The suite
should be run on both native Windows ARM64 and AMD64 machines. In the current
development environment, the AnyCPU executable passed on Windows ARM64, and
the x86-64 path passed under Windows ARM64 emulation using a temporary test
launcher. Native AMD64 execution remains to be verified on an AMD64 Windows
host.

The flat payload and platform binaries are checked during the build for
relocations, TLS, forbidden system-call/trap instructions, and size limits.

Run the Unix interactive acceptance on a target with:

```bash
python3 probe/acceptance/test_probe.py bash probe/hosts/windows/out/MohabbatProbe.exe
```

`probe/acceptance/test-polyglot-assembler.ps1` tests the MZ/PE assembly and
Bash trampoline with synthetic Unix assets. Its
`-UseRealLinuxAarch64Assets` mode runs the actual Windows and Linux AArch64
paths, substituting the Linux runner only for untested targets in a temporary
test bundle; that fixture is not a distributable.

To keep that validated test bundle instead of deleting it with the temporary
test directory:

```powershell
.\probe\acceptance\test-polyglot-assembler.ps1 `
    -UseRealLinuxAarch64Assets `
    -OutputPath .\probe\hosts\windows\.build\MohabbatProbe-Windows-Linux-AArch64-fixture.exe
```

The retained fixture has only been validated on Windows and Linux AArch64;
the substituted runner slots do not support the untested targets.

Windows acceptance and Linux AArch64 end-to-end acceptance passed in the
current environment. The Linux x86-64, Android, and macOS Brot runners and
native runtimes still require validation on those target environments.
