# MOHABBAT — Go pivot (hybrid)

This document supersedes the Rust-specific portions of [MOHABBAT.md](MOHABBAT.md).
The file format, the polyglot trick, the vocabulary, and the WASM payload all
stay. The native infrastructure becomes a **hybrid**:

- **brot stays Rust.** It is a tiny native stub. It needs no async, no WASM
  runtime, no large dependencies. It needs precise OS control (pipes,
  `execve`, reflective PE loading, instruction patching on Windows). Rust
  is the right tool for that and the existing brot codebase is already
  there.
- **washmhost is rewritten in Go** as a **plain executable** (not a
  c-shared library), embedding wazero, exposing the rusticated ABI to the
  WASM guest.
- brot launches washmhost in a per-platform way (see §7) and hands it the
  WASM payload through an inherited fd or named pipe (see §7).

Read this document as authoritative. Where it disagrees with
[MOHABBAT.md](MOHABBAT.md), this one wins.

---

## 1. Why Go (for washmhost) and Rust (for brot)

The Rust implementation works and the architecture is sound. We pivot
**washmhost** to Go and keep **brot** in Rust. Each language goes where
it is the right tool:

### Why Go for washmhost

1. **Async is the default.** Goroutines and the Go scheduler give us
   the completion-driven I/O model that washmhost needs (drive WASM,
   fulfil pending host operations, repeat) without standing up a custom
   executor, a custom std, or a custom sysroot. The rusticated stack
   exists today because Rust does not give us this for free — Go does.

2. **wazero is a pure-Go WASM runtime.** It is one `go get` away. It is
   designed for embedding, supports WASI out of the box, and lets us
   register host modules with normal Go callbacks. There is no JIT, no
   AOT compilation step, no Cranelift dependency, no `.cwasm` artefact.

3. **Plain cross-compilation.** `GOOS=X GOARCH=Y CGO_ENABLED=0 go
   build` produces all five washmhost targets from any one host. No
   sysroot, no toolchain matrix.

### Why Rust for brot

1. **Brot is tiny, OS-mechanical, and has no async or runtime needs.**
   Five-page operations: read meta, decompress pool, set up a pipe,
   `fork`+`execve` (POSIX) or reflectively map a PE and patch
   instructions (Windows). Every piece is a thin wrapper over OS
   primitives. Go's runtime, GC, and goroutine machinery buy nothing
   here and inflate binary size for no benefit.

2. **The Windows reflective loader needs surgical bytecode patching**
   (PEB-read instructions in washmhost's `.text`). Rust crates
   `iced-x86` and `bad64` give us robust disassemble-and-rewrite
   primitives. Equivalent Go libraries are weaker.

3. **The existing brot is already Rust** and works. Reimplementing it
   in Go was tried; it bought nothing and lost size and control.

The WASM payload — kabibi and the mohabbat brain — stays Rust against
the rusticated ABI. The rusticated stack is the right tool inside WASM.
It is overkill outside.

The boundary between brot and washmhost is one environment variable,
`MOHABBAT_WASM_FD` (§6). No shared C ABI, no FFI, no header file. Each
side speaks its own language end-to-end.

---

## 2. What stays, what changes

| Component | Stays | Changes |
|---|---|---|
| Vegetable file format (Zone A / B / C) | yes | no |
| Polyglot `.bat` + sh header | yes | no |
| `MohabbatMeta` layout and `.mohabbat_meta` section | yes | section magic and offsets identical |
| Brot's responsibilities (decompress, launch host, propagate exit) | yes | **stays Rust**; in-process DLL loading replaced by per-platform launch (§7) |
| Washmhost's responsibilities (run WASM, expose ABI) | yes | **reimplemented in Go on wazero, as a plain executable** |
| Brot ↔ washmhost handoff | — | **new**: env var `MOHABBAT_WASM_FD` (fd number on POSIX, pipe path on Windows) |
| WASM brain (the builder) | yes | stays Rust against rusticated ABI |
| Kabibi (the TUI app) | yes | stays Rust against rusticated ABI |
| Five target matrix (Intel Mac dropped) | yes | updated |
| Wasmtime | — | replaced by wazero |
| Custom rusticated sysroot for native layer | — | removed for washmhost (Go); brot still uses minimal Rust std |
| `.cwasm` AOT pipeline | — | removed; wazero takes `.wasm` directly |
| Mode A vs Mode B CLI in brain | — | collapsed into a single positional argument |
| `-buildmode=c-shared` for washmhost | — | **abandoned**: requires a C compiler even with `CGO_ENABLED=0`, defeats cross-compilation. Plain `go build` instead. |

---

## 3. Vocabulary (unchanged)

- **vegetable** — any polyglot file produced by this pipeline. Extension
  `.bat`.
- **mohabbat** — the self-hosting vegetable that is also a builder.
  Filename `mohab.bat`.
- **brot** — small native loader stub. One per target triple.
- **washmhost** — the WASM host. Embeds wazero, exposes the rusticated
  ABI to the guest WASM, runs it. One per target triple.
- **brain** — the WASM payload inside `mohab.bat`. Builder logic.
- **payload** — the WASM module inside any vegetable. For mohabbat it is
  the brain; for other vegetables it is the user's WASM.
- **pool** — the brotli-compressed concatenation of all washmhosts plus
  the payload.
- **Modern Five** — the target matrix:
  - `linux/amd64`
  - `linux/arm64`
  - `windows/amd64`
  - `windows/arm64`
  - `darwin/arm64`

  Intel Mac (`darwin/amd64`) is not supported, and 32-bit x86/ARM CPUs either.

We use Go's `GOOS/GOARCH` notation in the Go pivot. Mapping to Rust
triples (for cross-referencing the brain build) is straightforward.

---

## 4. Vegetable file layout (unchanged)

```
[Zone A: polyglot script header   ]   text, executable by sh + cmd
[Zone B: brot table               ]   five native binaries, back to back
[Zone C: brotli pool              ]   one brotli stream: hosts + payload
EOF
```

Zone A detects OS and CPU, then extracts **exclusively the byte-perfect
slice of the target `brot` from Zone B** using strict offset and
fixed-length extraction boundaries — no tail-carving or slicing to EOF.
The extraction command is of the form `dd skip=<OFFSET> count=<LEN>` (or
equivalent), so the dropped file contains exactly the compiled brot bytes
and nothing trailing it. This keeps the pre-baked `rcodesign` signature
valid on macOS: the file Gatekeeper sees matches the hash baked in at
build time.

Because the extracted brot no longer carries the pool in its own tail,
Zone A passes the path of the parent vegetable to brot by setting the
environment variable `MOHABBAT_VEGETABLE_PATH` before executing the
extracted stub. Brot reads this variable to locate and open the pool.
This is a structural contract between Zone A and brot, not an optional
convenience.

Zone C is one brotli stream containing, in fixed order: washmhost-1,
washmhost-2, …, washmhost-5, payload. Skipped slots have zero length and
are absent from the stream.

---

## 5. Brot (Rust)

Source lives in `brot/`. One Cargo crate. Five builds via
`--target <triple>`. The existing Rust brot is the starting point;
the in-process DLL loading code is removed and replaced with the
per-platform launch mechanism in §7.

### Responsibilities

In order:

1. **Discover parent vegetable.** Brot was extracted as a clean,
   bounded slice by Zone A. Its own image does not carry the pool —
   appended data would corrupt its code signature. Instead, brot reads
   the `MOHABBAT_VEGETABLE_PATH` environment variable that Zone A set
   before `exec`-ing the stub. This is the path to the `.bat` container
   the user originally invoked. Do not fall back to `std::env::current_exe()`;
   that path leads only to the isolated temp stub.
2. **Open the parent vegetable.** Open the file at `MOHABBAT_VEGETABLE_PATH`,
   seek to `file_size - POOL_LEN`, and read the brotli pool from there.
   `POOL_LEN` and all other offsets come from the `MohabbatMeta` struct
   embedded in brot's own binary (the patcher wrote them there at build
   time).
3. Decompress the pool with the `brotli` crate. One allocation, one decode.
4. Slice out the washmhost for this exact target (offset+length from
   `MohabbatMeta`) and the WASM payload (offset+length from `MohabbatMeta`).
5. **Set up the WASM handoff channel** (per-platform; see §7). Set
   `MOHABBAT_WASM_FD` in the environment that washmhost will inherit.
6. **Launch washmhost** (per-platform; see §7). On POSIX this is
   `fork` + `execve` of an in-memory or temp-file image; on Windows it is
   a reflective PE loader running washmhost in brot's own process.
7. Propagate washmhost's exit code as brot's own exit code.

### Constraints

- **Pure Rust.** No CGO equivalents. The Rust standard library and a
  handful of crates (`brotli`, `libc`, `windows-sys`, optionally
  `iced-x86` / `bad64` for the Windows loader) cover everything brot
  needs.
- **Binary size target: under 1 MB per brot, stripped.** Rust gives us
  this for free. Brotli decoder is the dominant cost.
- **No async runtime.** Brot has no concurrency requirements except a
  single helper thread on Windows for the named-pipe writer (§7).
- **No `std::process::Command` shell-out.** Washmhost is launched
  directly via raw `execve` (POSIX) or in-process reflective load
  (Windows). No intermediate shell, no PATH lookup, no quoting concerns.

### MohabbatMeta embedding

Same struct, same magic, same layout as before:

```rust
#[repr(C)]
pub struct MohabbatMeta {
    pub magic:             [u8; 8], // b"MOHABBAT"
    pub pool_len:          u64,
    pub washmhost_offset:  u64,
    pub washmhost_len:     u64,
    pub payload_offset:    u64,
    pub payload_len:       u64,
    pub reserved:          u64,
}

#[no_mangle]
#[link_section = ".mohabbat_meta"]
pub static MOHABBAT_META: MohabbatMeta = MohabbatMeta {
    magic: *b"MOHABBAT",
    pool_len: 0, washmhost_offset: 0, washmhost_len: 0,
    payload_offset: 0, payload_len: 0, reserved: 0,
};
```

The patcher (see §9) scans the brot binary for the `MOHABBAT` magic byte
sequence and rewrites the six u64 fields in place. The Rust source must
contain the magic exactly once. Enforce by build-time check in the
patcher: if scan finds more than one occurrence, fail.

---

## 6. Washmhost (Go executable on wazero)

Source lives in `washmhost-go/`. One Go module. Five builds via
`GOOS/GOARCH`. Built as a **plain executable** with `go build`
(`CGO_ENABLED=0`, no `-buildmode=c-shared`).

The `c-shared` mode was investigated and rejected: the Go toolchain
refuses to produce a c-shared library without a C compiler available,
even with `CGO_ENABLED=0`. That breaks the single-host cross-compilation
promise. A plain executable cross-compiles cleanly to all five targets
from any one host.

### Responsibilities

1. **Read the WASM payload.** On startup, look up `MOHABBAT_WASM_FD` in
   the environment. The value is either:
   - a **decimal integer** — treat as an inherited fd; wrap with
     `os.NewFile(uintptr(n), "wasm")`. POSIX uses this.
   - a **path string** (anything not parsing as a decimal integer) —
     treat as a path; open with `os.Open`. Windows uses this with a
     named-pipe path like `\\.\pipe\mohabbat-wasm-<pid>`. Standalone
     testing uses this with a regular file path.

   In all cases, `io.ReadAll` on the resulting `*os.File` produces the
   WASM bytes. No platform-specific code in washmhost; the same five
   lines work everywhere:

   ```go
   ref := os.Getenv("MOHABBAT_WASM_FD")
   var r io.Reader
   if n, err := strconv.ParseUint(ref, 10, 64); err == nil {
       r = os.NewFile(uintptr(n), "wasm")
   } else {
       f, _ := os.Open(ref); defer f.Close(); r = f
   }
   wasm, _ := io.ReadAll(r)
   ```

2. Set up a wazero runtime. Use **compiler mode**
   (`wazero.NewRuntimeConfigCompiler()`). wazero's single-pass JIT reads
   WASM bytecode and emits machine code directly — no IR, no loop
   unrolling, sub-second compilation. The result runs 10–20× faster than
   the interpreter during the intensive TUI, file-manager, and shell
   processing cycles that kabibi performs. The extra 50–100 ms at startup
   is imperceptible to a human running a terminal command and is already
   buried inside the brotli decompression time.
3. Detect the WASM module's import flavour. Two recognised flavours:
   - **rusticated** — imports from module name `"env"`. The native side
     registers the rusticated ABI: file I/O, process spawn, TTY, time,
     random, network, terminal size events.
   - **WASI** — imports from `"wasi_snapshot_preview1"`. wazero has
     first-class WASI support; instantiate with
     `wasi_snapshot_preview1.Instantiate(ctx, r)`. No custom shim
     required. This is what `GOOS=wasip1 GOARCH=wasm go build` produces.

   If a module imports from both, prefer rusticated and warn. If from
   neither, fail with a clear error listing the imports actually
   requested.
4. Instantiate the module. Call the exported entry point (`run` for
   rusticated, `_start` for WASI). `os.Args` is forwarded into the WASM
   guest as its argv.
5. Drive the completion loop. For rusticated modules, this is the
   `run` / `is_done` / `poll_completions` cycle currently in
   [washmhost/src/lib.rs](washmhost/src/lib.rs). Translated to Go:

   ```go
   for {
       if err := runFn.Call(ctx); err != nil { break }
       if done, _ := isDoneFn.Call(ctx); done[0] == 1 { break }
       pollCompletions(ctx, mem)
   }
   ```

   For WASI modules, `_start` runs to completion; no polling loop.
6. Call `os.Exit` with the appropriate exit code. Brot is either the
   parent (POSIX, reaping via `wait`) or the same process (Windows,
   reflective load), and propagates the code to the OS.

### Standalone smoke testing

Because the entire payload-delivery contract is one env var, washmhost
runs without brot for testing:

```sh
MOHABBAT_WASM_FD=./kabibi.wasm ./washmhost-linux-amd64
```

```powershell
$env:MOHABBAT_WASM_FD = "C:\path\to\kabibi.wasm"
.\washmhost-windows-amd64.exe
```

The value parses as a path (non-numeric), washmhost opens it as a file,
reads the WASM, runs it. ABI development happens entirely against this
mode, with no brot involved.

### ABI bindings (rusticated flavour)

The rusticated ABI is a fixed set of host functions registered under
module name `"env"`. The Go implementation mirrors the existing Rust
implementation in [washmhost/src/env_impl.rs](washmhost/src/env_impl.rs)
function for function:

```go
builder := r.NewHostModuleBuilder("env")

builder.NewFunctionBuilder().
    WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
        stack[0] = uint64(time.Now().UnixNano())
    }), nil, []api.ValueType{api.ValueTypeI64}).
    Export("get_time")

// ... one Export per ABI function
```

Each async ABI function (file ops, network, child processes) launches a
goroutine that performs the operation, then writes the overlapped
completion struct into WASM linear memory when done. The WASM side polls
the overlapped flag. The completion-struct layout is fixed and identical
to today's Rust implementation (24 bytes: flags, error, continued,
result_ext — see `write_overlapped` in `env_impl.rs`).

This is the place where Go earns its keep: every "async" Rust function
in env_impl.rs becomes a `go func(){...}()` in Go. No executor, no waker
plumbing, no select abstraction. Go's scheduler runs the goroutine; the
goroutine writes the completion when ready.

### Size

A plain Go executable embedding wazero is roughly 12–18 MB unstripped.
With `-ldflags="-s -w"`, around 8–12 MB. Brotli at quality 11 compresses
this to roughly 3–5 MB per host, ~20 MB for all five in the pool
post-compression. Acceptable.

---

## 7. Launching washmhost

Brot launches washmhost in a per-platform way. POSIX uses `fork` +
`execve` of an image written to a kernel-only file (Linux) or a
self-deleting temp file (macOS). Windows has no `fork`/`execve` and no
`memfd_create`, so brot uses a reflective PE loader and runs washmhost
in its own process.

The WASM payload reaches washmhost through `MOHABBAT_WASM_FD` (§6).
The mechanism differs per platform but the washmhost-side contract does
not.

### Linux — `memfd_create` + fork/execve, payload via anonymous pipe

```text
washmhost_fd = memfd_create("washmhost", MFD_CLOEXEC)
write(washmhost_fd, washmhost_bytes)

(pipe_r, pipe_w) = pipe2(O_CLOEXEC)
fcntl(pipe_r, F_SETFD, 0)         // clear CLOEXEC on the read end
setenv("MOHABBAT_WASM_FD", pipe_r.to_string())

pid = fork()
if pid == 0:
    close(pipe_w)
    execve("/proc/self/fd/<washmhost_fd>", argv, envp)
else:
    close(pipe_r)
    write_all(pipe_w, wasm_payload_bytes)
    close(pipe_w)
    status = waitpid(pid)
    exit(status)
```

Properties:

- **Fileless.** Washmhost never touches the filesystem. `memfd_create`
  produces a kernel-backed fd; `execve("/proc/self/fd/N", ...)` runs it
  directly. Linux 3.17+ (universal on supported distros).
- **No pipe buffer ceiling.** The parent feeds the pipe while the child
  drains it. WASM payload size is unbounded by `PIPE_BUF`.
- **No CLOEXEC trap.** The read end has CLOEXEC cleared explicitly so
  it survives `execve`; the write end keeps CLOEXEC (closed
  automatically in the child) and is closed by hand in the parent after
  writing.
- **Parent exits with child's status**, so the shell sees the right
  exit code.

### macOS — temp file + fork/execve, payload via anonymous pipe

```text
path = mkstemp("/tmp/mohabbat-washmhost-XXXXXX")
write(path, washmhost_bytes)
fchmod(path, 0o700)

(pipe_r, pipe_w) = pipe2(O_CLOEXEC)
fcntl(pipe_r, F_SETFD, 0)
setenv("MOHABBAT_WASM_FD", pipe_r.to_string())

pid = fork()
if pid == 0:
    close(pipe_w)
    execve(path, argv, envp)         // Gatekeeper checks the ad-hoc signature
else:
    close(pipe_r)
    write_all(pipe_w, wasm_payload_bytes)
    close(pipe_w)
    status = waitpid(pid)
    exit(status)
```

The temp file is unavoidable: macOS has no `memfd_create`, and
`execve` requires a real file path. To minimise visibility, washmhost
removes its own image as the first act of `main()`:

```go
_ = os.Remove(os.Args[0])
```

The kernel mapping survives the unlink. By the time control returns
from `os.Remove`, the file is invisible to the filesystem; on process
exit the inode is reclaimed.

The brot stub and washmhost are both signed ad-hoc with `rcodesign` at
build time (see §9). Gatekeeper checks the signature on `execve`; with
the ad-hoc signature baked in, it passes.

Reflective Mach-O loading was considered and rejected: chained fixups
and pointer-authentication codes on Apple Silicon make manual
relocation brittle.

### Windows — reflective PE loader (in-process), payload via named pipe

Windows has neither `fork`/`execve` nor `memfd_create`. The kernel
requires a real on-disk file handle to map an EXE. To stay fileless we
load washmhost's EXE bytes into brot's own address space using a Rust
reflective PE loader, then jump to its entry point.

#### Loader steps

1. Parse PE headers (`IMAGE_DOS_HEADER`, `IMAGE_NT_HEADERS`).
2. `VirtualAlloc` a contiguous region of `SizeOfImage` bytes (RW).
3. Copy each section to its `VirtualAddress` within the region.
4. Walk the base relocation directory (`IMAGE_DIRECTORY_ENTRY_BASERELOC`)
   and patch every absolute pointer by `(alloc_base - preferred_base)`.
5. Walk the import directory (`IMAGE_DIRECTORY_ENTRY_IMPORT`); resolve
   each import via `LoadLibraryA` + `GetProcAddress`, write the
   resolved address into the IAT slot.
6. `VirtualProtect` each section to its final permissions: `.text` →
   RX, `.rdata` → R, `.data` → RW, `.pdata` → R.
7. **Hot-patch the PEB-reading instructions** (see below).
8. **Register exception unwind data** with `RtlAddFunctionTable` using
   the `.pdata` section. Without this, any hardware fault inside
   washmhost (including ordinary Go runtime stack growth probes) is not
   matched by the OS unwinder and terminates the process.
9. `FlushInstructionCache(GetCurrentProcess(), base, SizeOfImage)`.
10. Jump to `base + AddressOfEntryPoint` (Go runtime `_rt0` → `main`).

#### PEB hot-patching

The Go runtime reads the Process Environment Block during initialisation
to locate the module list, image base, environment, and command line.
On x64 the sequence is:

```asm
mov rax, gs:[0x60]    ; rax = TEB.ProcessEnvironmentBlock
```

On ARM64:

```asm
mrs x0, tpidr_el0     ; x0 = TEB
ldr x1, [x0, #0x60]   ; x1 = PEB
```

Both read the **real** PEB of brot's process, not a fictional one for
washmhost. With real Windows-loaded images this is harmless because the
PEB.Ldr module list contains every loaded image. With our reflective
load, washmhost is not in that list, and Go gets the wrong image base
and environment.

Fix: scan washmhost's `.text` for these instruction sequences and
rewrite them to load from a brot-controlled `FakePeb` struct.

- **x64:** use `iced-x86` to disassemble `.text`. For each
  `MOV RAX/R*, GS:[0x60]` instruction, overwrite with a RIP-relative
  load from a 16-byte code cave near the function:
  `MOV reg, [rip + offset_to_FakePeb_ptr]`.
- **ARM64:** use `bad64` to find the `MRS Xn, TPIDR_EL0` + adjacent
  `LDR Xm, [Xn, #0x60]` pair and replace with a PC-relative `ADRP` +
  `LDR` to the same `FakePeb` pointer.

`FakePeb` is constructed by brot before the jump:

```rust
struct FakePeb {
    image_base:        *const u8,   // mapped washmhost base
    ldr:               *const PebLdrData,
    process_parameters:*const RtlUserProcessParameters,
    // ...minimum fields the Go runtime reads
}
```

`FakePeb.image_base` points to the `VirtualAlloc`-ed washmhost region.
`process_parameters` carries an `Environment` block containing
`MOHABBAT_WASM_FD` set to the named-pipe path.

The code cave can be appended to the end of the `.text` mapping (one
extra page of RX memory allocated alongside the image).

#### WASM payload via named pipe

There is no fork, so no fd inheritance. Brot creates a named pipe in
the kernel Object Manager namespace (`\\.\pipe\...` — no disk, no
filesystem entry, available at any privilege level) and exports the
path via `MOHABBAT_WASM_FD`:

```rust
let pipe_name = format!(r"\\.\pipe\mohabbat-wasm-{}", process::id());
let pipe = CreateNamedPipeW(
    &pipe_name, PIPE_ACCESS_OUTBOUND,
    PIPE_TYPE_BYTE | PIPE_WAIT, 1,
    /*out*/ 64 * 1024, /*in*/ 0, 0, ptr::null_mut(),
);
fake_peb_env.insert("MOHABBAT_WASM_FD", &pipe_name);

let wasm = wasm_bytes.clone();
let pipe_handle = pipe; // SendWrapper for HANDLE
std::thread::spawn(move || {
    ConnectNamedPipe(pipe_handle, ptr::null_mut());  // blocks until client connects
    let mut written = 0u32;
    WriteFile(pipe_handle, wasm.as_ptr(), wasm.len() as u32, &mut written, ptr::null_mut());
    CloseHandle(pipe_handle);
});

// main thread: reflective load, patch, jump to entry point
```

The writer thread is structurally required: `ConnectNamedPipe` blocks
until washmhost (running on the main thread inside the reflective
image) calls `CreateFileW` on the pipe path. Brot's main thread cannot
do both. Overlapped I/O, IOCP, and APCs do not help — every variant
still needs a thread to observe the completion while the main thread
is buried inside washmhost's synchronous execution.

The Windows brot is by far the most complex piece of native code in
the project. The Linux and macOS brots are thin wrappers around the OS
loader. That asymmetry is fine — complexity lives in one place and does
not leak into washmhost.

### Brot → washmhost handoff contract

| Platform | Launch mechanism | `MOHABBAT_WASM_FD` value | Backing |
|---|---|---|---|
| Linux | `memfd_create` + fork + `execve("/proc/self/fd/N", ...)` | decimal fd number | anonymous pipe, parent writes after fork |
| macOS | temp file + fork + `execve(path, ...)`; washmhost self-removes | decimal fd number | anonymous pipe, parent writes after fork |
| Windows | reflective PE load + PEB patch + jump to entry | `\\.\pipe\mohabbat-wasm-<pid>` | named pipe, writer thread |
| Standalone test | brot uninvolved | path to a `.wasm` file | regular file on disk |

In every case washmhost runs the same `MOHABBAT_WASM_FD`-reading
prologue from §6.

---

## 8. mohab.bat CLI

The whole CLI is one positional argument:

```
mohab.bat <input>
```

where `<input>` is one of:

| Input | Detection | Behaviour |
|---|---|---|
| Directory containing `Cargo.toml` | file exists | Build as Rust against rusticated wasm32 target |
| Directory containing `go.mod` | file exists | Build as Go to `GOOS=wasip1 GOARCH=wasm` |
| Directory containing exactly one `*.wasm` | only file matching `*.wasm` | Use that WASM directly |
| Path to `Cargo.toml` | explicit | Build as Rust, no ambiguity |
| Path to `go.mod` | explicit | Build as Go, no ambiguity |
| Path to `*.wasm` | explicit | Use directly, no build |

Ambiguous cases (a directory with both `Cargo.toml` and `go.mod`, or
multiple `.wasm` files, or a directory with none of the above) are
rejected with an explicit error message telling the user to pass the
specific file path.

Output is always written as `<input-stem>.bat` next to the input. The
input-stem of a directory is the directory name. The input-stem of a
file is the file basename without extension.

No flags. No subcommands. No `--output`. No `--entry`. If you want a
specific output filename, rename the result yourself. The brain is
small enough that we can revisit this if real user demand for flags
emerges, but the starting position is one argument in, one file out.

### Build commands the brain runs

- Rust: `cargo build --release --target wasm32-rusticated-unknown-unknown`
- Go: `GOOS=wasip1 GOARCH=wasm go build -o <tempdir>/payload.wasm ./...`

For Rust, the brain locates the produced `.wasm` by parsing `cargo
metadata` output (JSON, single line, easy). For Go, the output path is
explicit (`-o`).

The brain forwards build tool stdout/stderr to the user's terminal so
they see what is happening. Build failures abort with the build tool's
exit code.

### Brain runs in WASM

The brain reads its container with the host file API: open `argv[0]`,
read the whole file, parse `MohabbatMeta` at the known offset (search
for the `MOHABBAT` magic — same approach as the patcher). Then:

1. Decompress the pool.
2. Replace the payload slice with the new WASM bytes.
3. Brotli-encode the new pool (quality 11).
4. Compute new `MohabbatMeta` values.
5. Read mohabbat's own Zone A + Zone B (everything before the pool).
6. Patch each brot in Zone B with the new `MohabbatMeta`.
7. **Rewrite the Zone A template with fixed-length brot extraction
   bounds.** Each platform's extraction command in the script must
   specify both the byte offset *and* the exact byte count of that
   platform's brot slice — never `offset to EOF`. The brain computes
   exact `(offset, length)` pairs from the Zone B layout and substitutes
   them into the script template (e.g., replacing `{{LINUX_AMD64_OFF}}`
   and `{{LINUX_AMD64_LEN}}` placeholders). This ensures the output
   vegetable repeats the clean-slice process on every execution, keeping
   code-signing intact down the chain.
8. Write Zone A + patched Zone B + new pool to the output path.
9. `chmod +x` on POSIX.

The brain uses the rusticated ABI for all of this: file I/O (async), the
brotli encoder (Rust crate compiled into the brain WASM), child process
spawn (for invoking cargo or go).

---

## 9. Build pipeline

Source lives in `mohabbat-go/`. This is the orchestrator that produces
`mohab.bat` from a clean checkout. Go program, runs natively on the
developer's machine. Replaces `mohabbat/build.rs`.

```
mohabbat-go/
├── main.go        entry point
├── slots.go       per-target build of brot (Rust) and washmhost (Go)
├── brain.go       cargo build of the Rust brain WASM
├── stitch.go      assemble Zone A + Zone B + Zone C
├── patch.go       find MOHABBAT magic, write meta
└── pool.go        brotli encoding of the pool
```

### Pipeline

1. **Probe targets.** All five targets are always available. Go
   cross-compiles cleanly with `CGO_ENABLED=0`. Rust cross-compiles via
   pre-built targets that `rustup` installs on demand. No C compiler,
   no SDK, no special toolchain required on the build host.
2. **Build brot × 5 (Rust).** For each target:
   ```
   cargo build --release --target <triple> -p brot
   ```
   Triples: `x86_64-unknown-linux-gnu`, `aarch64-unknown-linux-gnu`,
   `aarch64-apple-darwin`, `x86_64-pc-windows-msvc`,
   `aarch64-pc-windows-msvc`. Strip with `strip` / `llvm-strip`.
3. **Build washmhost × 5 (Go).** For each target:
   ```
   GOOS=<os> GOARCH=<arch> CGO_ENABLED=0 \
   go build -ldflags="-s -w" \
     -o build/washmhost-<os>-<arch>[.exe] ./washmhost-go
   ```
   Plain executable. No `-buildmode=c-shared`.
4. **Sign the macOS binaries.** Gatekeeper rejects any unsigned Mach-O
   the moment it is executed. Two binaries need signing on macOS:
   - `brot-aarch64-apple-darwin` — the native loader stub. Zone A
     extracts it to a temp file and `exec`s it.
   - `washmhost-darwin-arm64` — the WASM host executable. Brot writes
     it to a temp file and `execve`s it (§7).

   Sign both with `rcodesign` (pure-Rust, runs on Linux/Windows, no
   Xcode required) using an ad-hoc signature:
   ```
   rcodesign sign --ad-hoc build/brot-aarch64-apple-darwin
   rcodesign sign --ad-hoc build/washmhost-darwin-arm64
   ```
   Both signatures are baked in before Zone B and Zone C are assembled.
   No runtime signing, no entitlements, no developer identity required
   from the end user.
5. **Build the brain.** Invoke cargo:
   ```
   cargo build --release \
     --target wasm32-rusticated-unknown-unknown \
     -p mohabbat
   ```
   Pick up `target/.../release/mohabbat.wasm`.
6. **Pool.** Concatenate the five washmhosts in fixed order, then the
   brain. Brotli quality 11. Record offsets and lengths.
7. **Patch brots.** For each brot binary, scan for `MOHABBAT` magic
   (must occur exactly once), write the six u64 fields after it.
8. **Stitch.** Emit Zone A from the frozen template with the five
   (offset, length) pairs filled in. Concatenate Zone A + Zone B
   (patched brots) + Zone C (compressed pool).
9. **Write** `mohab.bat` at the repo root. `chmod +x` on POSIX.

### Bootstrapping

On a clean checkout: `cd mohabbat-go && go run .` produces `mohab.bat`.

Subsequent vegetable production uses `mohab.bat` directly. The brain
inside it handles new WASM payloads. We do not need to re-run
`mohabbat-go` until a brot or washmhost source changes.

### Seeding (for distribution)

We will commit a `mohab.bat` to the repository periodically (or attach
it to releases) so users can run it without cargo or Go installed. This
matches the §8 strategy in [MOHABBAT.md](MOHABBAT.md). The Go pivot
does not change this story.

---

## 10. Repository layout (after pivot)

```
rusticated-treego/
├── Cargo.toml                    workspace (Rust crates)
├── src/                          rusticated library (custom std for WASM)
├── kabibi/                       TUI app (Rust → WASM)
├── mohabbat/                     brain crate (Rust → WASM)
│   └── src/main.rs               the builder logic
│
├── brot/                         ACTIVE: native loader stub (Rust)
│   ├── Cargo.toml
│   └── src/
│       ├── main.rs               entry, meta lookup, pool decode, dispatch
│       ├── meta.rs               MohabbatMeta + magic
│       ├── pool.rs               brotli decode + slicing
│       ├── launch_linux.rs       memfd_create + pipe + fork + execve
│       ├── launch_macos.rs       temp file + pipe + fork + execve
│       └── launch_windows.rs     reflective PE loader + PEB patch + named pipe
│
├── washmhost-go/                 NEW: WASM host (Go on wazero, plain executable)
│   ├── go.mod
│   ├── main.go                   reads MOHABBAT_WASM_FD, runs wazero
│   ├── runtime.go                wazero setup, module detection
│   ├── env_impl.go               rusticated ABI bindings
│   └── poll.go                   completion-driven I/O loop
│
├── mohabbat-go/                  NEW: native build orchestrator (Go)
│   ├── go.mod
│   ├── main.go                   pipeline driver
│   ├── slots.go                  per-target builds (cargo for brot, go for washmhost)
│   ├── brain.go                  cargo invocation for brain
│   ├── pool.go                   brotli encoding
│   ├── patch.go                  MohabbatMeta patching
│   └── stitch.go                 Zone A / B / C assembly
│
├── washmhost/                    OLD: Rust host, kept until washmhost-go ships, then deleted
│
└── mohab.bat                     produced artefact, committed periodically
```

Note the `-go` suffix on `washmhost-go/` and `mohabbat-go/`: it marks
the Go-language components and lets a future second-language port live
beside them without collision (`washmhost-zig`, etc.). `brot/` keeps
its unsuffixed name because the Rust implementation is the canonical
one going forward — no language pivot is planned there.

The old `washmhost/` directory stays through the transition so we can
run the existing pipeline while bringing up `washmhost-go`. It is
deleted in one commit once the Go pipeline produces a working
`mohab.bat` end to end.

---

## 11. What kabibi gets out of this

Kabibi is the demonstration consumer. It is already substantially built
out: ratatui TUI, two-panel file manager, shell prompt, AI chat panel,
async stdin + resize event loop, runs as `wasm32-rusticated-unknown-unknown`.

Once the Go pivot lands, kabibi is built as:

```
mohab.bat ./kabibi
```

Produces `kabibi.bat`. One file. Runs on Windows, Linux, macOS, x64 and
ARM64. Double-clickable on Windows. `./kabibi.bat` on Unix.

Kabibi sticks with the rusticated ABI because it needs raw TTY control,
resize events, and async stdin — things WASI does not expose. User apps
that only need stdio + filesystem can use plain `GOOS=wasip1 go build`
and let wazero's WASI implementation handle the rest.

---

## 12. Order of work

1. Stand up `washmhost-go/` with wazero as a plain executable. Port the
   existing rusticated ABI bindings. Verify standalone with
   `MOHABBAT_WASM_FD=./kabibi.wasm ./washmhost-linux-amd64` — no brot in
   the loop.
2. Refresh `brot/` (Rust) for the Linux launch path: `memfd_create` +
   pipe + fork + `execve`. End-to-end: brot launches washmhost-go,
   washmhost-go runs kabibi.
3. Stand up `mohabbat-go/`. Produce a working `mohab.bat` on Linux
   only, four slots empty.
4. Add the macOS launch path to `brot/` (temp file + pipe + fork +
   execve + washmhost self-removes its image). Sign with `rcodesign`.
   Add the macOS slot.
5. Build the Windows reflective PE loader in `brot/`: PE parsing,
   section mapping, relocations, IAT resolution, PEB instruction
   hot-patching (iced-x86 for x64, bad64 for arm64),
   `RtlAddFunctionTable`, named pipe + writer thread. Add the Windows
   slots.
6. Cross-compile from one host to all five. Validate by running each
   target binary under emulation (QEMU for arm64 Linux; actual hardware
   or VMs for Windows and macOS slots in CI).
7. Delete the old `washmhost/` Rust crate. Update `mohab.bat` from the
   new pipeline. This is the cut-over commit.

Each step lands as its own commit. No big-bang switch.

---

## 13. What we explicitly are not doing

- **TinyGo.** TinyGo is a different runtime with different semantics
  (no goroutines on WASM, limited reflection, partial stdlib). The
  WASM brain stays Rust; if we ever ship a Go-based builder WASM in
  the future, it goes through standard `GOOS=wasip1 GOARCH=wasm`.
- **CGO in washmhost.** `CGO_ENABLED=0` is absolute for the Go side.
  This is precisely why washmhost is a plain executable rather than a
  `c-shared` library: the Go toolchain unconditionally requires a C
  compiler for `c-shared`, even with `CGO_ENABLED=0` set. Plain `go
  build` cross-compiles cleanly from any single host.
- **`go build -buildmode=c-shared` for washmhost.** Investigated and
  abandoned for the reason above. The IPC overhead of a separate
  washmhost process is dominated by `fork` and pipe-write latency,
  both negligible compared to brotli decompression and wazero compile
  time. Not worth fighting the toolchain.
- **Reflective Mach-O loading on macOS.** Apple Silicon chained fixups
  and pointer authentication codes make manual relocation either
  impossible or fragile. Temp file + `execve` + immediate self-remove
  is the correct ceiling.
- **Reflective ELF loading on Linux.** `memfd_create` +
  `execve("/proc/self/fd/N", ...)` is the supported, fileless path. No
  need to reimplement the dynamic linker.
- **Wasmtime in Go.** wasmtime-go uses CGO and a pre-built native
  library. That brings back exactly the cross-compilation pain we are
  escaping. wazero is the right call.
- **The wazero interpreter.** `wazero.NewRuntimeConfigInterpreter()` is
  not appropriate for kabibi or any sustained-computation payload. The
  TUI, file manager, and shell parser need throughput. wazero's
  single-pass compiler (`NewRuntimeConfigCompiler()`) translates WASM
  bytecode directly to machine code in well under a second with no
  heavy IR or loop-unrolling passes. The startup cost is absorbed into
  the brotli decompression time. Use the compiler.
- **Intel Mac (`darwin/amd64`).** Apple completed the Intel-to-Silicon
  transition and subsequent macOS releases dropped Intel support. There
  is no Intel Mac CLI demographic left to serve. Dropping the slot
  removes 16 % from the binary table and simplifies the matrix to five.
- **Runtime code-signing on macOS.** Signatures go on during the
  `mohabbat-go` build step, using `rcodesign`, before either the brot
  stub or the washmhost executable is stitched into the output. Both
  Mach-O binaries must be signed: `brot-aarch64-apple-darwin`
  (Gatekeeper checks it on `execve`) and `washmhost-darwin-arm64`
  (Gatekeeper checks it on `execve`). Attempting to sign at runtime
  requires entitlements, notarisation, or a developer identity — none
  of which we can or should impose on the end user.
- **Bundling Rust or Go toolchains.** mohab.bat does not ship a
  compiler. If the user passes a Rust project, they need cargo. If
  they pass a Go project, they need go. We document this.
- **Backwards compatibility with the previous all-Rust
  brot/washmhost.** The vegetable file format is stable. The internal
  native components are not. A `mohab.bat` produced by the old
  all-Rust pipeline and a `mohab.bat` produced by the hybrid pipeline
  are interchangeable from the user's perspective; internally they
  share nothing.
