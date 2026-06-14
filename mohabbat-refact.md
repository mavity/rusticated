We will now need to refactor mohabbat-go to become the central building utility superseding prebuild, mohabbat'Rust and even washmhost-go/debug-run.go. I'll describe the architecture of the refactoring, then produce the diffs and your job will be to apply this refactoring.

## Mohabbat-Go Refactoring: From Single Purpose to Four-Mode Build Tool

### What Changed

Three separate build tools — prebuild (Rust), mohabbat (Rust+WASM vegetable assembler), and debug-run.go (Go) — were unified into a single mohabbat-go project that serves all build needs of the rusticated project.

### File Structure

**New files created:**

* `prebuild.go` (662 lines) — Port of main.rs into Go, build-tagged `//go:build !wasip1`
* `refill.go` (177 lines) — Juice bottle refill: splice a new payload into an existing vegetable

**Heavily restructured:**

* `main.go` (865 lines) — Grew from a simple assembler into a four-mode CLI dispatcher with full build pipeline

**Total:** ~1,700 lines of Go across 3 files.

### The Four Modes

| Mode | Invocation | What it does |
| --- | --- | --- |
| 1 | `mohab.bat` (no args) | Full build: prebuild target specs + sysroot + Go overlay $\rightarrow$ cross-compile brot + washmhost-go for 4 platforms $\rightarrow$ build brain WASM $\rightarrow$ assemble vegetable |
| 2 | `mohab.bat <project> -o out.bat` (inside vegetable) | Juice bottle refill — decompress pool from self, splice new payload, recompress, write output |
| 3 | `mohab.bat <project> -o out.bat` (native) | Fresh assembly with arbitrary payload — builds brot/washmhost if needed, assembles new vegetable |
| 4 | `mohab.bat <project> -r [args...]` | Build project to WASM $\rightarrow$ run immediately under washmhost-go (dev inner loop) |

Mode detection is based on presence of `input`, `-r` flag, and whether we're running inside a vegetable (`MOHABBAT_VEGETABLE_PATH` env var).

### prebuild.go — The Rust-to-Go Port

Ported the entire main.rs pipeline:

* **Target spec generation:** 5 rusticated target triples (aarch64/x86_64 $\times$ linux/windows + wasm32), writing JSON specs to rusticated-spec
* **Sysroot build:** `cargo build` of the custom std library for each target
* **Go overlay generation:** Writes overlay.json mapping 14 GOROOT wasip1 source files to their rusticated replacements in overlay-go
* **GOROOT resolution:** Priority chain — go.mod version $\rightarrow$ `$HOME/sdk/go{ver}` $\rightarrow$ `go{ver} env GOROOT` $\rightarrow$ `go env GOROOT`
* **Brain WASM build:** Compiles mohabbat-go itself as WASM (`-buildmode=c-shared`), then post-processes the binary
* `postProcessWasm`: Proper WASM section parser with LEB128 encoding — renames the `_initialize` export to `run` (which is what the washmhost event loop calls)

### refill.go — Vegetable Surgery

When mohabbat-go runs as the brain inside a vegetable, it can repackage itself with a different payload without rebuilding everything from scratch:

1. Read the vegetable file, locate MOHABBAT magic signature
2. Parse the meta struct (pool offset/length, washmhost offset/length, payload offset/length)
3. Decompress the brotli pool
4. Splice: keep washmhost bytes, replace payload bytes
5. Recompress the pool
6. Patch all meta occurrences (each brot loader has its own copy)
7. Write output

### Technical Challenges

#### 1. GOROOT Contamination (the big one)

When mohabbat-go is launched via `go -C mohabbat-go run .`, the Go toolchain sets `GOROOT` to the **module cache** path:

```text
GOROOT=/Users/.../go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.4.darwin-arm64

```

But `overlay.json` maps files from the **SDK** path:

```text
/Users/.../sdk/go1.26.4/src/runtime/os_wasip1.go -> overlay-go/runtime/os_rusticated.go

```

When the subprocess `go build -overlay overlay.json` inherits the wrong GOROOT, the overlay paths **silently don't match** — the Go compiler just uses the standard wasip1 files, producing WASM that imports `wasi_snapshot_preview1` instead of `env`. The washmhost only registers `env`, so instantiation fails.

Fix: explicitly set `GOROOT` to the resolved SDK path via `upsertEnv` in both `buildGoProjectWasm` and `buildBrainWasm`.

#### 2. CWD vs Workspace Path Resolution

`go -C mohabbat-go run .` changes the working directory to mohabbat-go. When the user passes demo-go as a project path, `filepath.Abs("demo-go")` resolves to `content/mohab-go/demo-go` — which doesn't exist.

Fix: `buildProjectToWasm` falls back to `filepath.Join(ws, projectDir)` when the path doesn't exist relative to CWD. Workspace root is found by walking up from the executable/CWD looking for sysroot.toml.

#### 3. GOOS/GOARCH Leakage in `-r` Mode

The WASM build step sets `GOOS=wasip1 GOARCH=wasm`. When washmhost-go is subsequently launched via `go run .`, those env vars could leak and cause washmhost-go itself to be compiled as WASM instead of native. Fix: explicitly set `GOOS=runtime.GOOS` and `GOARCH=runtime.GOARCH` on the washmhost subprocess (though the real fix was GOROOT — this was belt-and-suspenders).

#### 4. Unified Codebase & Rusticated WASM ABI

A central covenant of this refactor is the **rejection of build-tag gating** for core logic. An initial attempt to split `main.go` into `native.go` and `wasm_main.go` was abandoned. 

The **Rusticated WASM ABI** is a high-speed, rich interface that replicates almost the entire platform richness of the native Go runtime. Specifically:
*   **No WASI Sandbox limitations**: The brain does **NOT** run in standard `wasi_snapshot_preview1` mode. It runs in the **Rusticated WASM ABI mode**, which provides direct, async access to the host's I/O, process management, and networking.
*   **Unified logic**: Because the Rusticated Platform has `process_spawn` / `process_wait` in its ABI, standard Go `os/exec` calls work identically in WASM. This allows the building, refilling, and execution logic in `main.go` to remain 100% unified without `#ifdef`-style gating.
*   **Exception**: Only `prebuild.go` carries `//go:build !wasip1` because orchestrating the native host compiler toolchain (Cargo, rustup) only makes sense on the host.

#### 5. `upsertEnv` Instead of `append`

`os.Environ()` may already contain `GOOS`, `GOARCH`, or `GOROOT` from the parent process. Using `append(os.Environ(), "GOOS=wasip1")` adds a **duplicate** — and which one wins is implementation-defined. `upsertEnv` removes any existing key before appending, ensuring deterministic behavior.


# Testing & Unconditional Failure

In the **Rusticated Platform**, an implementation without exhaustive verification is considered **unconditionally failed**. This is because subtle failures—such as `GOROOT` contamination, incorrect LEB128 encoding, or path resolution mismatches—produce artifacts that look correct but fail silently at runtime. 

Testing must cover every transition between Native and Vegetable states.

### Core Testing Covenants

Verifying the work requires successful completion of the following six targets:

1.  **The Bootstrap Covenant**: Running `go -C mohabbat-go run .` natively must successfully build the native launchers, the Go brain, and assemble a functional `mohab.bat`.
2.  **Native Project Packaging (Rust & Go)**: The native tool must successfully package both Rust projects (e.g., `demo`) and Go projects (e.g., `demo-go`) into functional vegetables via `<project> -o <project.bat>`.
3.  **The Native Inner Loop**: `go -C mohabbat-go run . <project> -r` must build and immediately execute payloads under `washmhost-go`, verifying native launcher behavior and terminal handover.
4.  **Vegetable Self-Hosting**: The generated `mohab.bat` MUST be able to rebuild itself. This verifies the WASM brain's ability to orchestrate the host toolchain via the Rusticated WASM ABI.
5.  **Vegetable Project Packaging (Refilling)**: `mohab.bat` must perform surgical "refills" (Mode 2) for both Rust and Go projects (`mohab.bat <project> -o out.bat`), verifying metalayout patching and Brotli pool manipulation.
6.  **The Vegetable Inner Loop**: `mohab.bat <project> -r` must successfully build and run projects from within the vegetable environment.

### Performance & Size Constraints

Vegetables must remain efficient. The previous state (Commit `b448c978a849a132c07d80750519ad602de1a9d0`) is the baseline.

*   **mohab.bat**: Expansion is allowed up to 2-3MB (Max total size: **10MB**) to account for Go infrastructure.
*   **Other Vegetables**: (e.g., `demo.bat`, `demo-go.bat`) must stay within 1MB of their original size (Max total size: **6MB**).
*   **Settings**: Use maximum Brotli compression (Quality 11) and the tightest compilation flags (`-s -w` for Go, `--release` for Rust). **DO NOT** modify `brot` Rust source; it is tuned for minimal size.

## Comprehensive Testing Plan for Unified Mohabbat-Go

In the **Rusticated Platform**, testing is not just a final step; it is the definition of completion. Because we are operating with a custom `sysroot` and a Go `wasip1` overlay, any drift in environment variables (like `GOROOT` contamination) or path resolution errors will cause the build system to silently fail by producing "standard" binaries instead of "rusticated" ones. Such a tool is **UNCONDITIONALLY FAILED** because it compromises the entire ABI contract. 

This plan systematically exercises every transition between Native and Vegetable states, ensuring that `mohabbat-go` can build itself, package others, and run the development inner loop without friction.

### Plan: Unified Mohabbat-Go Validation Suite

This validation suite covers the four operational modes across both Native and Vegetable execution environments.

**Steps**

#### Phase 1: Native Environment & Bootstrap (Mode 1)
1. **Cleanup**: Remove `mohab.bat` and `target/mohabbat-go-build` to ensure a clean slate.
2. **Bootstrap Entry**: Run `go -C mohabbat-go run .` with no arguments.
   - *Dependency*: Relies on `sysroot.toml` being present in workspace root.
   - *Verification*: Full pipeline must finish successfully. A new `mohab.bat` must be assembles.

#### Phase 2: Native Project Packaging & Running (Modes 3 & 4)
1. **Rust Packaging**: `go -C mohabbat-go run . demo -o demo.bat`
2. **Go Packaging**: `go -C mohabbat-go run . demo-go -o demo-go.bat`
   - *Verification*: Inspect `demo-go.wasm` (via `strings` or parser) to ensure it imports from `env`, NOT `wasi_snapshot_preview1`.
3. **Rust Inner Loop**: `go -C mohabbat-go run . demo -r`
4. **Go Inner Loop**: `go -C mohabbat-go run . demo-go -r`

#### Phase 3: Vegetable Self-Hosting (Mode 1 & 2)
1. **Self-Rebuild**: Run `./mohab.bat` (no args).
   - *Verification*: The vegetable executes its internal `mohabbat-go` brain, which must successfully rebuild a fresh `mohab.bat`.
2. **Rust Refill**: `./mohab.bat demo -o demo-refilled.bat`
   - *Verification*: Triggers "Juice Bottle Refill" logic. The tool must splice the new payload without rebuilding native launchers.
3. **Go Refill**: `./mohab.bat demo-go -o demo-go-refilled.bat`

#### Phase 4: Vegetable Project Running (Mode 4 inside Veg)
1. **Rust Dev Run**: `./mohab.bat demo -r`
2. **Go Dev Run**: `./mohab.bat demo-go -r`
   - *Verification*: Ensure proper terminal handover for `stdin`/`stdout`.

**Relevant files**
- [mohabbat-go/main.go](mohabbat-go/main.go) — CLI dispatcher and Mode 1/3/4 logic.
- [mohabbat-go/prebuild.go](mohabbat-go/prebuild.go) — `GOROOT` resolution and `overlay.json` generation.
- [mohabbat-go/refill.go](mohabbat-go/refill.go) — Mode 2 surgical payload replacement.

**Verification**
1. **Artifact Integrity**: Check that all generated `.bat` files are executable and contain valid Zone A headers.
2. **ABI Compliance**: Generated WASM files must strictly use the `env` namespace.
3. **Exit Codes**: Every test case must return `0`.

**Decisions & Blockage Management**
- **GOROOT Contamination**: If the Go build fails to find runtime files, the plan will fallback to investigating the SDK path resolution in [prebuild.go](mohabbat-go/prebuild.go#L284).
- **Path Ambiguity**: Tests will be run from both the workspace root and the `mohabbat-go` directory to verify workspace walking logic.
- **Dependency Missing**: If `brot` (Rust) or `washmhost-go` are missing from the cache, Mode 3/4 will block until a full Mode 1 build is performed.

**Further Considerations**
1. Would you like me to include a specific test case for the `demo-go/trivial` sub-project to verify deep directory resolution?
2. Should I add a "Smoke Test" step that executes one of the packaged vegetables (e.g., `demo.bat`) to verify end-to-end execution on the host?


**IMPORTANT NOTE:** Apart from functional test conditions, the produced vegetables need to be as compact and efficient as they were before the refactor. The only exception allowed for nontrivial expansion of the vegetable size is mohab.bat itself, which is moving to contain substantially more logic, plus migrating from original Rust brain to Go brain, which necessitates Go infrastructure. However the expansion of mohab.bat is expected to take 2-3Mb at most, and the other vegetables (demo.bat, demo-go.bat) should remain within 1Mb of their original size.

Note that all the vegetables before the refactoring were under 6Mb, most of them below 5.5Mb and some even below 5Mb. The new vegetables should be under 6Mb, and the new mohab.bat should be under 10Mb.

The key to achieving the size constraint is to use tightest compilation flags for the Go and Rust compilers, and to use brotli maximum compression. If you need study the current mohabbat Rust brain for the flags and compression settings, you can find them in the original mohabbat Rust source code. Also take a note of the original mohabbat-go that also builds brot and washmhost-go and packs them into highest brotli compression. The new mohabbat-go should do the same, and it should be able to build itself and pack itself into a vegetable with the same size constraints. The commit for the original code before the refactoring is here: b448c978a849a132c07d80750519ad602de1a9d0

Also pay attention to NOT change brot, as it is very tightly and carefully tuned for size: using no-std no-main build and it took a while to make it work perfectly like that.

