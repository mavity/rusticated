# Linux no-CRT plan

This document lists the concrete tasks required to remove Linux CRT dependency and restore a clean custom `rusticated` Linux build.

## 1. Fix the Linux target spec

- Ensure `aarch64-rusticated-linux-gnu.json` is a true custom target, not a normal GNU target.
- Use `rust-lld` as the linker and add explicit no-CRT linker flags.
- Prevent automatic linking of `-lc`, `-lgcc_s`, `-lm`, `-lrt`, and `-lpthread`.
- Ensure the target JSON does not bring in libc startup objects or default C runtime objects.

## 2. Provide a minimal Linux entrypoint

- Do not rely on `crt1.o`, `crti.o`, `crtn.o`, or other libc startup files.
- Implement a small Linux `_start` or custom startup path in the runtime.
- The entrypoint should set up arguments and call `lang_start`/`main` directly.

## 3. Replace the Unix allocator path

- Remove the Linux allocator implementation that uses libc `malloc`/`free`.
- Use `dlmalloc` as the global allocator facade.
- Have `dlmalloc` acquire memory from Linux with raw OS primitives (`mmap`/`munmap` or direct syscalls).
- This removes libc heap dependency from the allocator.

## 4. Remove explicit libc and gcc_s linkage

- Remove `#[link(name = "c")]` for Linux builds from `src/lib.rs`.
- Remove explicit `#[link(name = "gcc_s")]` from Linux-specific code.
- If unwind support is not needed, avoid `libgcc_s` entirely.
- Prefer no-CRT alternatives and minimal platform bindings.

## 5. Audit and replace libc-style wrappers

- Search Linux-specific code for `extern "C"` libc APIs.
- Replace libc wrappers with raw Linux syscalls where possible.
- Prioritize `env`, `process`, `tty`, `fs`, and startup/runtime layers.
- Keep the Linux runtime path minimal and self-contained.

## 6. Validate build configuration and generated sysroot

- Verify `sysroot.toml` and `target/rusticated-spec/config.toml` do not introduce libc or system C libs.
- Ensure Cargo is using the generated Linux target JSON.
- Confirm the custom target and rustflags are aligned.

## 7. Test the Linux demo build

- Run `cargo run -p prebuild`.
- Run `cargo build -p demo --config sysroot.toml`.
- Confirm the linker no longer requests CRT libraries like `-lc` or `-lgcc_s`.
- Confirm the resulting Linux executable starts without libc startup objects.

## Notes

- The main blockers are target/linker configuration and startup path.
- The allocator rewrite is important, but linker flags and entrypoint are the highest priority.
- Keep Linux no-CRT work separate from Windows target behavior.
- `dlmalloc` is already present in `Cargo.toml`; the remaining work is implementation and cleanup.
