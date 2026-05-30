# MOHABBAT — Go pivot

This document supersedes the Rust-specific portions of [MOHABBAT.md](MOHABBAT.md).
The file format, the polyglot trick, the vocabulary, and the WASM payload all
stay. The native infrastructure pivots from Rust to Go.

Read this document as authoritative. Where it disagrees with
[MOHABBAT.md](MOHABBAT.md), this one wins.

---

## 1. Why Go

The Rust implementation works and the architecture is sound. We pivot the
native layer for three concrete reasons:

1. **Cross-compilation.** Go produces six target binaries with
   `GOOS=X GOARCH=Y go build`. No sysroot, no toolchain matrix, no cargo
   target installation. We need this because mohab.bat ships six brot
   binaries and six washmhost binaries on every build.

2. **Async is the default.** Goroutines and the Go scheduler give us the
   completion-driven I/O model that washmhost needs (drive WASM, fulfil
   pending host operations, repeat) without standing up a custom executor,
   a custom std, or a custom sysroot. The rusticated stack exists today
   because Rust does not give us this for free — Go does.

3. **wazero is a pure-Go WASM runtime.** It is one `go get` away. It is
   designed for embedding, supports WASI out of the box, and lets us
   register host modules with normal Go callbacks. There is no JIT, no
   AOT compilation step, no Cranelift dependency, no `.cwasm` artefact.

The WASM payload — including the kabibi TUI and the mohabbat brain itself —
stays Rust against the rusticated ABI. The rusticated stack is the right
tool inside WASM. It is overkill outside.

---

## 2. What stays, what changes

| Component | Stays | Changes |
|---|---|---|
| Vegetable file format (Zone A / B / C) | yes | no |
| Polyglot `.bat` + sh header | yes | no |
| `MohabbatMeta` layout and `.mohabbat_meta` section | yes | section magic and offsets identical |
| Brot's responsibilities (decompress, load host, run) | yes | reimplemented in Go |
| Washmhost's responsibilities (run WASM, expose ABI) | yes | reimplemented in Go on wazero |
| WASM brain (the builder) | yes | stays Rust against rusticated ABI |
| Kabibi (the TUI app) | yes | stays Rust against rusticated ABI |
| Six target matrix | yes | no |
| Wasmtime | — | replaced by wazero |
| Custom rusticated sysroot for native layer | — | removed; native layer is Go |
| `.cwasm` AOT pipeline | — | removed; wazero takes `.wasm` directly |
| Mode A vs Mode B CLI in brain | — | collapsed into a single positional argument |

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
- **Modern Six** — the target matrix:
  - `linux/amd64`
  - `linux/arm64`
  - `windows/amd64`
  - `windows/arm64`
  - `darwin/amd64`
  - `darwin/arm64`

We use Go's `GOOS/GOARCH` notation in the Go pivot. Mapping to Rust
triples (for cross-referencing the brain build) is straightforward.

---

## 4. Vegetable file layout (unchanged)

```
[Zone A: polyglot script header   ]   text, executable by sh + cmd
[Zone B: brot table               ]   six native binaries, back to back
[Zone C: brotli pool              ]   one brotli stream: hosts + payload
EOF
```

Zone A and Zone B behave exactly as documented in [MOHABBAT.md §2](MOHABBAT.md).
The polyglot header detects OS and CPU, slices out the right brot, writes
it to a temp file, executes it, propagates exit code. Nothing here changes
in the Go pivot.

Zone C is one brotli stream containing, in fixed order: washmhost-1,
washmhost-2, …, washmhost-6, payload. Skipped slots have zero length and
are absent from the stream.

---

## 5. Brot (Go reimplementation)

Source lives in `brot-go/`. One Go module. Six builds via
`GOOS/GOARCH`.

### Responsibilities

In order:

1. Discover its own path. Brot was extracted to a temp file by the Zone A
   header; its own image contains the brotli pool at its tail. Use
   `os.Executable()` — works correctly on all three OSes.
2. Open self, seek to `file_size - POOL_LEN`, read the brotli pool.
   `POOL_LEN` and all other offsets come from the `MohabbatMeta` struct
   embedded at a known section/offset in the binary.
3. Decompress the pool with a pure-Go brotli decoder
   (`github.com/andybalholm/brotli`). One allocation, one decode.
4. Slice out the washmhost for this exact target (offset+length from
   `MohabbatMeta`) and the payload (offset+length from `MohabbatMeta`).
5. Load the washmhost into the current process as an in-process library.
   See §7 for the per-OS mechanism.
6. Invoke washmhost's exported entry point. Pass: a pointer to the
   payload bytes, payload length, and the brot's own `argv` (so the WASM
   guest sees the user's command-line arguments).
7. Wait for washmhost's entry to return. Propagate exit code.

### Constraints

- **CGO_ENABLED=0 on Linux and macOS.** No C toolchain required to build
  brot for those platforms. Cross-compilation is pure Go.
- **CGO_ENABLED=1 on Windows only**, and only for the reflective PE
  loader. See §7. The Windows brot build needs a mingw cross-compiler in
  CI. This is the one place we accept the CGO cost.
- **Binary size target: under 4 MB per brot.** Strip with
  `-ldflags="-s -w"`, compress with `upx` only if it stays compatible
  with our polyglot tail-reading. Brotli decoder is the dominant cost;
  Go runtime itself is around 2 MB.
- **No standard library cruft.** No `net/http`, no `encoding/json` in
  brot. The loader is mechanical — read bytes, decompress, load, jump.

### MohabbatMeta embedding

Same struct, same magic, same layout as Rust:

```go
type MohabbatMeta struct {
    Magic            [8]byte  // "MOHABBAT"
    PoolLen          uint64
    WashmhostOffset  uint64
    WashmhostLen     uint64
    PayloadOffset    uint64
    PayloadLen       uint64
    Reserved         uint64
}
```

Embedded as a package-level `var` in a known location:

```go
//go:linkname mohabbatMeta mohabbat.Meta
var mohabbatMeta = MohabbatMeta{Magic: [8]byte{'M','O','H','A','B','B','A','T'}}
```

The patcher (see §9) scans the brot binary for the `MOHABBAT` magic byte
sequence and rewrites the six u64 fields in place. Same approach as the
Rust version. Go places initialized globals in `.data` / `.rodata`
sections; the patcher does not care about section names, only about the
magic.

The Go source must contain the magic exactly once. Enforce by build-time
check in the patcher: if scan finds more than one occurrence, fail.

---

## 6. Washmhost (Go reimplementation on wazero)

Source lives in `washmhost-go/`. One Go module. Six builds via
`GOOS/GOARCH`. Built as a `-buildmode=c-shared` library (DLL on Windows,
`.so` on Linux, `.dylib` on macOS) for brot to load.

### Responsibilities

1. Receive payload pointer + length + argv from brot.
2. Set up a wazero runtime. Use the interpreter mode (no JIT compilation
   on the fly; this is a CLI tool, not a long-running server, and
   interpreter startup is faster).
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
   rusticated, `_start` for WASI).
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
6. Return exit code to brot.

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

A Go c-shared library with wazero is roughly 12–18 MB unstripped. With
`-ldflags="-s -w"`, around 8–12 MB. Brotli at quality 11 compresses
this to roughly 3–5 MB per host, ~25 MB for all six in the pool
post-compression. Acceptable.

---

## 7. In-process host loading

Brot must load washmhost into its own address space and call its
exported entry point. We do not spawn a child process — that defeats
the single-process design and adds startup latency.

### Linux — `memfd_create` + dlopen

```
fd = memfd_create("washmhost", 0)
write(fd, washmhostBytes)
handle = dlopen("/proc/self/fd/<fd>", RTLD_NOW)
runPayload = dlsym(handle, "run_payload")
runPayload(payloadPtr, payloadLen, argv, argc)
```

True fileless. The shared object never has a filesystem entry. `dlopen`
accepts the `/proc/self/fd/N` path because the kernel resolves it
through the open file descriptor. Available since Linux 3.17 (2014);
universally available on every supported distro.

Pure Go via `golang.org/x/sys/unix`. No CGO.

### macOS — temp file + immediate unlink

```
path = <temp>/mohabbat-washmhost-<pid>-<rand>.dylib
write(path, washmhostBytes)
handle = dlopen(path, RTLD_NOW)
unlink(path)            // dyld holds its own vnode reference
runPayload = dlsym(handle, "run_payload")
runPayload(payloadPtr, payloadLen, argv, argc)
```

`NSCreateObjectFileImageFromMemory` was rewritten on modern macOS to
write to a hidden temp file under `$TMPDIR` and call standard dlopen.
We do the equivalent ourselves and gain control over when the file goes
away.

The directory entry vanishes immediately after `dlopen` returns. dyld
holds the inode through its mapping. The file is unreachable from the
filesystem from microseconds after we wrote it. On process exit, the
inode refcount drops to zero and the kernel reclaims the storage. No
cleanup needed on crash.

Reflective Mach-O loading is wrong on Apple Silicon — chained fixups
and pointer authentication codes make manual relocation either
impossible or platform-specific in fragile ways. The temp+unlink path
is the right ceiling.

Pure Go via `syscall.Dlopen` / `syscall.Dlsym`. No CGO.

### Windows — reflective PE loader

Windows offers no equivalent to `memfd_create`. The kernel requires
`NtCreateSection` with `SEC_IMAGE` to operate on a real file handle
backed by a filesystem volume.

We have two choices:

1. **Temp file with `FILE_FLAG_DELETE_ON_CLOSE | FILE_SHARE_DELETE`.**
   Acceptable. The file exists on disk for the lifetime of the loaded
   DLL but is invisible to other processes (delete-pending) and the
   kernel guarantees deletion on the last handle close, even on crash.
   Pure Go via `golang.org/x/sys/windows`.

2. **Reflective PE loading.** The MemoryModule pattern. Parse PE
   headers, allocate with `VirtualAlloc`, copy sections, apply base
   relocations, resolve imports via `GetProcAddress` against system
   DLLs, flip page protections, call `DllMain(DLL_PROCESS_ATTACH)`,
   call the export. Truly in-memory. No file on disk ever.

**We choose reflective loading on Windows.** The reasoning:

- The Go runtime in a c-shared DLL initialises itself in `DllMain`. A
  correct reflective loader that calls `DllMain(DLL_PROCESS_ATTACH)`
  initialises the Go runtime correctly. Modern Go (1.21+) does not
  rely on Windows TLS for goroutine tracking, which sidesteps the
  exact problem that made reflective loading painful with Rust+wasmtime.
- It is the only option that achieves true fileless execution on
  Windows. Mohabbat is a tool that runs untrusted user WASM; not
  dropping native files to disk is a meaningful posture.
- Battle-tested code exists. **MemoryModule** by Joachim Bauch is the
  reference C implementation. Go ports exist; we will base ours on the
  pure-Go port at `github.com/Binject/universal/blob/master/memorymodule_windows.go`
  or reimplement directly using `golang.org/x/sys/windows`. Pure Go is
  preferred; CGO with MemoryModule.c is an acceptable fallback only if
  the pure-Go approach hits a wall.

The Windows brot becomes the most complex piece of native code in the
whole project. That is acceptable. The Linux and macOS brots are
trivial wrappers around system loaders.

### Entry point signature

Identical across platforms:

```c
// C-style signature exported by washmhost
int run_payload(const uint8_t* payload, size_t payload_len,
                int argc, const char** argv);
```

Returns the exit code that brot will propagate to the OS.

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
7. Write Zone A + patched Zone B + new pool to the output path.
8. `chmod +x` on POSIX.

The brain uses the rusticated ABI for all of this: file I/O (async), the
brotli encoder (Rust crate compiled into the brain WASM), child process
spawn (for invoking cargo or go).

---

## 9. Build pipeline (Go side)

Source lives in `mohabbat-go/`. This is the orchestrator that produces
`mohab.bat` from a clean checkout. Go program, runs natively on the
developer's machine. Replaces `mohabbat/build.rs`.

```
mohabbat-go/
├── main.go        entry point
├── slots.go       per-target build of brot-go and washmhost-go
├── brain.go       cargo build of the Rust brain WASM
├── stitch.go      assemble Zone A + Zone B + Zone C
├── patch.go       find MOHABBAT magic, write meta
└── pool.go        brotli encoding of the pool
```

### Pipeline

1. **Probe targets.** For each of the Modern Six, decide whether we can
   build it on this host. Pure Go targets: always yes. Windows target
   needs mingw cross-compiler in PATH (only for the reflective loader's
   minimal C glue, if we go that route). macOS targets: cross-compile
   from any host — Go does not need an SDK for pure Go binaries.
2. **Build brot-go × 6.** For each Available target:
   ```
   GOOS=<os> GOARCH=<arch> CGO_ENABLED=0 \
   go build -ldflags="-s -w" -o build/brot-<os>-<arch> ./brot-go
   ```
   Windows is the exception: `CGO_ENABLED=1` with the cross C compiler.
3. **Build washmhost-go × 6.** For each Available target:
   ```
   GOOS=<os> GOARCH=<arch> CGO_ENABLED=0 \
   go build -buildmode=c-shared -ldflags="-s -w" \
     -o build/washmhost-<os>-<arch>.<ext> ./washmhost-go
   ```
4. **Build the brain.** Invoke cargo:
   ```
   cargo build --release \
     --target wasm32-rusticated-unknown-unknown \
     -p mohabbat
   ```
   Pick up `target/.../release/mohabbat.wasm`.
5. **Pool.** Concatenate the six washmhosts in fixed order, then the
   brain. Brotli quality 11. Record offsets and lengths.
6. **Patch brots.** For each brot binary, scan for `MOHABBAT` magic
   (must occur exactly once), write the six u64 fields after it.
7. **Stitch.** Emit Zone A from the frozen template with the six
   (offset, length) pairs filled in. Concatenate Zone A + Zone B
   (patched brots) + Zone C (compressed pool).
8. **Write** `mohab.bat` at the repo root. `chmod +x` on POSIX.

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
├── brot-go/                      NEW: native loader (Go)
│   ├── go.mod
│   ├── main.go
│   ├── meta.go                   MohabbatMeta + magic
│   ├── pool.go                   brotli decode + slicing
│   ├── load_linux.go             memfd_create + dlopen
│   ├── load_darwin.go            temp + dlopen + unlink
│   └── load_windows.go           reflective PE loader
│
├── washmhost-go/                 NEW: WASM host (Go on wazero)
│   ├── go.mod
│   ├── main.go                   exported run_payload entry
│   ├── runtime.go                wazero setup, module detection
│   ├── env_impl.go               rusticated ABI bindings
│   ├── env_impl_linux.go         platform-specific ABI implementations
│   ├── env_impl_darwin.go
│   ├── env_impl_windows.go
│   └── poll.go                   completion-driven I/O loop
│
├── mohabbat-go/                  NEW: native build orchestrator (Go)
│   ├── go.mod
│   ├── main.go                   pipeline driver
│   ├── slots.go                  per-target builds
│   ├── brain.go                  cargo invocation for brain
│   ├── pool.go                   brotli encoding
│   ├── patch.go                  MohabbatMeta patching
│   └── stitch.go                 Zone A / B / C assembly
│
├── brot/                         OLD: kept until Go pivot ships, then deleted
├── washmhost/                    OLD: kept until Go pivot ships, then deleted
│
└── mohab.bat                     produced artefact, committed periodically
```

Note the `-go` suffix convention. We use it so the Go and Rust
implementations sort adjacently in directory listings, making side-by-side
review and parity checking easy. Any future second-language port follows
the same convention (`brot-zig`, `washmhost-rust`, etc.).

The old `brot/` and `washmhost/` directories stay through the transition
so we can run the existing Rust pipeline while bringing up the Go one.
They are deleted in one commit once the Go pipeline produces a working
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

1. Stand up `brot-go/` with the loader for one platform first
   (Linux, easiest). Verify it can load a `.so` from memfd and call an
   exported function.
2. Stand up `washmhost-go/` with wazero. Get the existing rusticated
   ABI bindings ported. Test by hand-loading a kabibi WASM and verifying
   the TUI runs.
3. Wire brot-go to washmhost-go. End-to-end on Linux.
4. Stand up `mohabbat-go/`. Produce a working `mohab.bat` on Linux only,
   five slots empty.
5. Port the loader to macOS (temp + unlink). Add the macOS slot.
6. Reflective PE loader for Windows. Add the Windows slots.
7. Cross-compile from one host to all six. Validate by running each
   target binary under emulation (QEMU for arm64 Linux; actual hardware
   or VMs for Windows and macOS slots in CI).
8. Delete `brot/` and `washmhost/`. Update `mohab.bat` from the new
   pipeline. This is the cut-over commit.

Each step lands as its own commit. No big-bang switch.

---

## 13. What we explicitly are not doing

- **TinyGo.** TinyGo is a different runtime with different semantics
  (no goroutines on WASM, limited reflection, partial stdlib). The
  WASM brain stays Rust; if we ever ship a Go-based builder WASM in
  the future, it goes through standard `GOOS=wasip1 GOARCH=wasm`.
- **CGO on Linux or macOS.** Pure Go only. The whole point of the
  pivot is escaping toolchain matrices. The Windows reflective loader
  is the one exception, and we will try pure Go first.
- **Wasmtime in Go.** wasmtime-go uses CGO and a pre-built native
  library. That brings back exactly the cross-compilation pain we are
  escaping. wazero is the right call.
- **Bundling Rust or Go toolchains.** mohab.bat does not ship a
  compiler. If the user passes a Rust project, they need cargo. If
  they pass a Go project, they need go. We document this.
- **Backwards compatibility with the old Rust brot/washmhost.** The
  vegetable file format is stable. The internal native components are
  not. A `mohab.bat` produced by the Rust pipeline and a `mohab.bat`
  produced by the Go pipeline are interchangeable from the user's
  perspective; internally they share nothing.
