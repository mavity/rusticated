# Mohabbat Next

This directory is an isolated implementation of the first Mohabbat redesign
delivery. It does not modify or integrate with the existing `mohabbat/`
implementation.

## Windows-first delivery

The Windows stage ships one AnyCPU CLR executable,
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
respective instruction sets. Linux and macOS runners are not implemented in
this Windows-first stage.

## Build and run

On Windows, run:

```powershell
.\probe\hosts\windows\build.ps1
.\probe\hosts\windows\out\MohabbatProbe.exe
```

The build creates the single deliverable EXE in `probe/hosts/windows/out/`.
Build tools and intermediates are kept separately under `.build/`. The
build-time component packer uses the pinned `ZstdSharp.Port` 0.8.8 package;
its managed runtime dependencies are embedded in the EXE.

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
