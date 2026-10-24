# GNU-Compatible `chmod` and Umask Integration

**Status:** Proposal for review. The `chmod` implementation work is paused; this document records scope and an implementation plan.

## 1. Purpose

Bring Kabibi's `chmod` command closer to GNU coreutils on Linux, validate it through a Windows-built vegetable running on WSL, and make symbolic modes that omit `who` honor the guest process's current umask.

Windows-specific permission behavior is deliberately deferred until Linux behavior is implemented and verified.

## 2. Observed Baseline

The Windows-built Kabibi vegetable was copied to WSL's Linux filesystem and executed there. The WSL guest reports `aarch64`.

Correctly invoked through `kabibi.bat -c`, the current implementation:

- Changed a file from `0644` to `0600`.
- Applied `u+x` to `0600`, resulting in `0700`.
- Applied `-R 0700` to a directory and its child.
- Rejected `04755` because the parser limits octal modes to `0777`.
- Rejected `-v` and `-c` as invalid mode operands.

The Rusticated Go syscall implementation of `Umask` currently returns `0`; there is no ABI operation to retrieve or update the guest's umask. A passing command exit code alone does not establish that a requested mode took effect; tests must inspect the resulting mode.

## 3. Goals

1. Support GNU/POSIX octal and symbolic chmod modes on Linux, including special bits.
2. Implement the documented reporting, error, reference, and recursive options.
3. Preserve GNU recursive symlink traversal rules and avoid changing targets outside a requested tree.
4. Apply the current guest umask when symbolic mode clauses omit `who`.
5. Return errors when the requested operation cannot be honored; do not report false success.
6. Validate both the command package and the actual Rusticated guest running on WSL's Linux filesystem.

## 4. Mode-Expression Scope

### 4.1 Octal modes

Accept permission and special bits in the range `0000` through `7777`. Convert POSIX setuid, setgid, and sticky bits to the corresponding Go `os.FileMode` flags before calling `os.Chmod`; do not pass the raw POSIX octal value as a Go `FileMode`.

### 4.2 Symbolic modes

Parse comma-separated clauses in sequence:

```text
[who] operator permissions
```

Support:

- `who`: `u`, `g`, `o`, `a`; omitted `who` means all classes subject to the current umask.
- Operators: `+`, `-`, `=`.
- Permissions: `r`, `w`, `x`, conditional execute `X`, set-ID `s`, sticky `t`, and permission copying from `u`, `g`, or `o`.
- Sequential evaluation: each clause observes the mode produced by preceding clauses.

`X` adds execute/search only for directories or when an execute bit is already set. Omitted `who` uses the umask to filter affected classes; explicit `a` does not. Empty permission lists and class-specific `=` behavior must follow GNU/POSIX rules.

Malformed clauses and invalid octal values must produce clear errors without changing any target.

## 5. Command-Line Behavior

Implement and test the GNU chmod options relevant to the command:

- `-R`, `--recursive`
- `-v`, `--verbose`
- `-c`, `--changes`
- `-f`, `--silent`, `--quiet`
- `--reference=RFILE`
- `-h`, `--no-dereference`
- Recursive traversal controls `-H`, `-L`, and `-P`
- `--preserve-root` and `--no-preserve-root`
- `--` to end option parsing

`-c` reports only files whose modes changed; `-v` reports every processed file; `-f` suppresses ordinary diagnostics. When multiple operands fail, continue processing the remaining operands and return failure overall.

Where a platform cannot change a symlink's own mode, `-h` must report the platform limitation rather than silently change the target.

## 6. Recursive and Symlink Safety

Use an explicit traversal policy. The default recursive policy must not descend through symlinks encountered below the operand. `-H` follows command-line symlinks to directories; `-L` follows symlinked directories throughout the walk; `-P` does not follow them.

Do not use a path-following metadata lookup to decide that a walked symlink is an ordinary file. Ensure recursive mode changes cannot escape the requested tree through a symlink. Define and test behavior for command-line symlink operands separately from symlinks discovered during traversal.

## 7. Umask ABI Requirement

GNU symbolic modes that omit `who` depend on the current process umask. The current Rusticated `syscall.Umask` stub returns zero, so a WASM guest cannot currently observe or maintain this state.

Before implementing omitted-`who` semantics, define an ABI contract that:

1. Exposes the effective umask to the guest chmod implementation.
2. Specifies whether the guest can update its umask, so shell-level umask changes and later chmod commands remain consistent.
3. Keeps guest state scoped to the running guest and does not mutate the host process's global umask.
4. Defines behavior for Go/Linux, Node, browser, and Windows hosts. A backend without POSIX umask semantics must use an explicit documented virtual value or return an explicit unsupported result; it must not silently claim a different host mask.
5. Uses the existing ABI operation/completion conventions where host work is required.

The implementation contract for umask initialization and mutation is a design decision to settle before adding imports or modifying the `chmod` parser. A read-only host query alone is insufficient if the guest supports changing umask during a session.

## 8. Test Plan

### 8.1 `u-root` package tests

Add focused tests for:

- Octal permission and special bits, including setuid, setgid, and sticky.
- Symbolic `rwxXst`, copied permissions, comma-separated clauses, omitted `who`, umask masking, and sequential application.
- Invalid syntax and boundary values.
- `-v`, `-c`, `-f`, `--reference`, and `--` behavior.
- Recursive traversal modes, command-line symlink operands, nested symlinks, and prevention of changes outside the selected tree.
- Multiple operands where an error occurs between successful changes.

### 8.2 Rusticated integration on WSL

Build the vegetable on Windows, copy it into WSL's Linux filesystem (not `/mnt/c`), and run `chmod` with `-c` through `kabibi.bat -c`. After each operation, use WSL `stat` to assert exact numeric modes. Include octal, symbolic, special-bit, recursive, omitted-`who`/umask, error, and reporting cases.

Run the focused Go host timestamp tests and cross-compile the host for Linux/ARM64 as part of packaging validation. The Linux/ARM32 host build also must remain compilable.

### 8.3 Checklist

Keep `kabibi/docs/5-uroot.md` unchecked until the Linux vegetable tests verify resulting modes and the promised options. Record Windows permission limitations separately rather than treating Linux success as proof of Windows parity.

## 9. Out of Scope for This Pass

- Defining Windows ACL-to-POSIX mode mapping or claiming that Windows supports Unix permission semantics.
- Changing unrelated filesystem ABI operations.
- Marking the command complete based solely on native unit tests or exit status.
