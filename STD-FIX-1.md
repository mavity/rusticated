## Plan: Two-Tier Sysroot — sysroot crate in src

**TL;DR:** build.rs writes `--extern std=../target/debug/librusticated.rlib` into config.toml — a file that has never existed. Cargo hashes sysroot artifacts as `libstd-<hash>.rlib`. The fix: make src a proper Cargo crate named sysroot, which `rusticated` depends on. Cargo then builds sysroot before `rusticated`'s build.rs runs, so build.rs can scan `target/<profile>/deps/libsysroot-*.rlib` and write the real absolute path into config.toml.

---

### Phase 1 — Create `src/Cargo.toml`

1. **Create `src/Cargo.toml`** — declares src as the sysroot implementation crate:
   - `[package] name = "sysroot"` with `publish = false` and workspace-inherited fields
   - `[lib] name = "sysroot"`, `path = "lib.rs"` *(relative to src, hits existing lib.rs — no file move)*
   - `[features] threads = []` (same feature as root)
   - `[lints] workspace = true`
   - Receives all runtime dependencies currently in root Cargo.toml: `dlmalloc`, `spin`, `hashbrown`, `ahash`, `concurrent-queue`, and the wasm-gated `getrandom`

2. **Add `"src"` to `[workspace.members]`** in root Cargo.toml

### Phase 2 — Thin `rusticated` wrapper

3. **Create `src/std.rs`** — new file, lives alongside lib.rs, belongs to `rusticated`:
   ```rust
   #![no_std]
   pub use sysroot::*;
   ```

4. **Update root Cargo.toml**:
   - Set `[lib] path = "src/std.rs"` (overrides the default of lib.rs)
   - Add `sysroot = { path = "src" }` to `[dependencies]`
   - **Remove** dlmalloc, spin, hashbrown, ahash, concurrent-queue, getrandom (moved to `src/Cargo.toml`)
   - Keep `serde_json` under `[build-dependencies]`

### Phase 3 — Fix build.rs

5. **Add rlib discovery** in build.rs:
   - Read `PROFILE` env var (Cargo sets `"debug"` or `"release"`)
   - Scan `target_dir.join(&profile).join("deps")` for files matching `libsysroot-*.rlib` via `fs::read_dir`
   - If multiple exist (stale artifacts), pick the most recently modified via `metadata().modified()`
   - Panic with a descriptive message if none found
   - Emit `cargo:rerun-if-changed=<absolute path>`

6. **Fix the format string** in build.rs `config_toml`:
   - Old: `"--extern", "std=../target/debug/librusticated.rlib"`
   - New: `"--extern", "std={sysroot_rlib_path}"` (absolute path from step 5; backslashes already handled by existing `.replace('\\', "/")`)

---

### Relevant files

| File | Action |
|------|--------|
| `src/Cargo.toml` | **Create** |
| `src/std.rs` | **Create** (thin wrapper) |
| Cargo.toml | **Modify** — members, `[lib] path`, deps |
| build.rs | **Modify** — rlib discovery + extern path fix |
| lib.rs | **No change** |
| config.toml | **No change** |
| build.rs | **No change** — `build_sysroot`'s `librusticated→libstd` rename is dead code (`needs_sysroot = false`) |

### Verification

1. `cargo build -p sysroot` — standalone build must succeed
2. `cargo build -p rusticated` — build.rs must find `libsysroot-*.rlib` without panicking
3. Inspect config.toml — `--extern std=` must be an absolute path to a `libsysroot-<hash>.rlib` that actually exists
4. `cargo build -p mohabbat` — washmhost must compile under `aarch64-rusticated` without "cannot find crate for `std`"

### One gotcha to watch

`src/std.rs` uses `pub use sysroot::*` in a crate with workspace-level `missing_docs = "deny"` and `rustdoc::all = "deny"`. Re-exported items carry their source docs so it should be lint-clean — but if a lint fires, adding `#[doc(inline)]` before the `pub use` resolves it.

---

# Testing

`cargo build -p rusticated-demo` — must succeed and produce a working demo executable that prints "hello" and then times out after 5s, creating `rusticated_demo.txt` with a trailing newline.
`cargo build -p loch`
`cargo build -p mohabbat`