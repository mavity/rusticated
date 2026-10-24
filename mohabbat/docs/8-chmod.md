# GNU-Compatible Linux `chmod` Subset

**Status:** Target specification for an existing implementation with documented failures. No chmod remediation has been implemented against this specification yet.

## 1. Purpose

Implement a defined, testable subset of GNU coreutils `chmod` semantics in Kabibi's Linux guest, validate it through a Windows-built vegetable running on WSL, and keep umask state in the guest process.

Windows-specific permission behavior is deliberately deferred until Linux behavior is implemented and verified.

## 2. Observed Baseline

The Windows-built Kabibi vegetable was copied to WSL's Linux filesystem and executed there. The WSL guest reports `aarch64`.

Correctly invoked through `kabibi.bat -c`, the current implementation:

- Changed a file from `0644` to `0600`.
- Applied `u+x` to `0600`, resulting in `0700`.
- Applied `-R 0700` to a directory and its child.
- Rejected `04755` because the parser limits octal modes to `0777`.
- Rejected `-v` and `-c` as invalid mode operands.

The Rusticated Go syscall implementation of `Umask` currently always returns `0` and stores no state. `path_chmod` itself is host-backed and changes the mode when called in WSL; tests must inspect the resulting mode rather than infer success from the exit code.

## 3. Acceptance Criteria

1. Support the mode expressions and options explicitly listed in this document on Linux.
2. Apply mutable, guest-local umask state when GNU rules require it.
3. Match GNU coreutils 9.4 symlink behavior as specified in section 7. This is a compatibility commitment, not an optional traversal policy.
4. Return errors when a requested operation cannot be honored; do not report false success.
5. Verify command behavior and resulting modes in the Rusticated guest on WSL's Linux filesystem.

This is a **defined GNU-compatible subset**, benchmarked against GNU coreutils 9.4 (the version available in the WSL validation environment), not a claim of complete or current coreutils parity. The supported and intentionally excluded behavior is listed below. Other options and mode expressions must be rejected rather than silently accepted.

## 4. Mode-Expression Scope

### 4.1 Octal modes

Accept permission and special bits in the range `0000` through `7777`. Convert POSIX setuid, setgid, and sticky bits to the corresponding Go `os.FileMode` flags before calling `os.Chmod`; do not pass the raw POSIX octal value as a Go `FileMode`.

### 4.2 Symbolic modes

Parse GNU symbolic modes, including comma-separated clauses and multiple operator/permission groups within a clause:

```text
[who] operator permissions... [, ...]
```

For example, `og+rX-w` contains multiple groups within one clause. Support:

- `who`: `u`, `g`, `o`, `a`; omitted `who` means all classes subject to the current umask.
- Operators: `+`, `-`, `=`.
- Permissions: zero or more of `r`, `w`, `x`, conditional execute `X`, set-ID `s`, or sticky `t`; alternatively, one copy source `u`, `g`, or `o`.
- Sequential evaluation: each clause observes the mode produced by preceding clauses.

`X` adds execute/search only for directories or when an execute bit was already set before the operation. A permission-copy clause uses the source class's current bits at that point in the expression.

When `who` is omitted, GNU defaults to all classes while protecting permission bits selected by the current umask; explicit `a` does not apply that protection. `=` still replaces permissions rather than preserving every masked bit. Verified GNU coreutils 9.4 examples with umask `0022`:

| Starting mode | Expression | Result | Meaning |
|---:|---|---:|---|
| `0444` | `+w` | `0644` | Omitted `who` protects group/other write from being added. |
| `0444` | `a+w` | `0666` | Explicit `a` is not filtered by umask. |
| `0666` | `-w` | `0466` | Masked group/other write bits are not removed. |
| `0777` | `=rw` | `0644` | Replace mode with requested permissions filtered by umask. |
| `0777` | `=` | `0000` | Empty permissions with `=` clear permissions. |
| `0600` | `g=u` | `0660` | Explicit class selection copies user bits to group. |
| `0700` | `u=rw,g=u,o=g` | `0666` | Comma clauses apply in order and copy current source-class bits. |

The `0666` / `-w` case changes the file to `0466` but GNU coreutils 9.4 returns failure and emits `new permissions are r--rw-rw-, not r--r--r--`. Preserve the nonzero status and emit an explanatory diagnostic for this case; the diagnostic need not match byte-for-byte because it may be localized.

Empty permission lists are valid only with `=`. Symbolic special-bit rules are:

- `u+s` sets setuid; `g+s` sets setgid; omitted `who` or `a+s` sets both.
- `+t` or `a+t` sets sticky; `o+t` is equivalent to `+t`.
- `o+s`, `u+t`, and `g+t` have no effect.

Umask protects ordinary `rwx` permissions, not special bits. Numeric special bits map to the corresponding Go `os.FileMode` flags.

Malformed clauses and invalid octal values must produce clear errors without changing any target.

## 5. Guest Umask State

The umask used by chmod is **guest process state**, not host process state. No host umask ABI is needed for chmod.

1. A new Rusticated Go guest starts with umask `0022`, independent of the host OS and host process umask.
2. `syscall.Umask(mask)` atomically replaces the guest mask, restricted to `0777`, and returns the previous mask.
3. A guest-local `GetUmask()` getter returns the current mask to chmod without changing it.
4. Umask updates and chmod reads observe the same process state. The host process-global umask is never modified.
5. This scope covers chmod interpretation only; it does **not** claim umask filtering for newly created files.

The umask state belongs to the Rusticated Go guest process and is shared by shell built-ins and reentrant commands in that guest. It is initialized once per guest instance, not once per chmod invocation. Use an atomic state value so concurrent reads and updates are well-defined. No new host ABI import/export is part of this work.

## 6. Command-Line Behavior

Implement and test this Linux subset:

- `-R`, `--recursive`
- `-v`, `--verbose`
- `-c`, `--changes`
- `-f`, `--silent`, `--quiet`
- `--reference=RFILE` and `--reference RFILE`
- `--preserve-root` (default) and `--no-preserve-root`
- `--` to end option parsing
- `--help`, `--version`

`-c` reports only files whose modes changed; `-v` reports every processed file. When both occur, the last of `-c` or `-v` wins, as in GNU coreutils 9.4. `-f` suppresses ordinary error diagnostics but does not turn a failed operation into success. Continue processing other operands after an operand error, then return failure overall.

The Linux subset follows command-line symlinks by default. GNU coreutils 9.4 `chmod` in the WSL validation environment rejects `-H`, `-L`, `-P`, `-h`, `--dereference`, and `--no-dereference`; none are supported. Rusticated's path-chmod operation also has no no-follow mode. This specification does not silently reinterpret an unknown option as a mode.

Other intentionally unsupported features include GNU `--from=MODE` and any option or mode-expression extension not listed above. Explicitly test that these fail without modifying a target.

## 7. Recursive and Symlink Semantics

**Compatibility commitment:** For the supported Linux subset, Kabibi `chmod` will match the symlink behavior observed with GNU coreutils 9.4:

- A command-line symlink is followed. With `-R`, a command-line symlink to a directory is traversed.
- A symlink to a directory encountered below a command-line directory operand is not traversed.
- There are no `-H`, `-L`, or `-P` traversal controls in this `chmod` version; reject them as invalid options.

Nested symlinks encountered during recursive descent must not have their referents changed. Following a command-line symlink can change a target outside the operand's lexical subtree; this compatibility behavior does not provide a physical-containment boundary. `--preserve-root` protects recursive operation on `/`, and `--no-preserve-root` disables that guard.

Use `Lstat`/directory-entry type information during recursive traversal so a nested symlink is not accidentally followed by a later `Stat`/`Chmod` call. Test command-line symlink operands separately from symlinks encountered below a directory operand, including a command-line link whose target is outside the operand's lexical location. Report traversal errors and avoid repeated traversal of the same directory.

## 8. Test Plan

### 8.1 Mode and command tests

Add focused tests for:

- Octal permission and special bits, including setuid, setgid, and sticky.
- Symbolic `rwxXst`, copied permissions, combined operation groups, comma-separated clauses, omitted `who`, umask behavior for `+`, `-`, and `=`, and sequential application; assert both the resulting mode and the nonzero status for the documented `0666` / `-w` case.
- Invalid syntax and boundary values.
- `Umask` replacement/return values, initial `0022`, concurrent reads/updates, and chmod observing shell-updated guest state.
- `-v`, `-c`, `-f`, `--reference`, root protection, help/version, `--`, and explicit rejection of unsupported options, including `-H`, `-L`, `-P`, `-h`, `--dereference`, and `--no-dereference`.
- Recursive traversal through a command-line symlink to a directory, and no traversal through a nested directory symlink.
- Command-line symlink-to-file behavior and recursive symlink entries; verify that a nested symlink's referent is unchanged.
- Multiple operands where an error occurs between successful changes.

### 8.2 Rusticated integration on WSL

Build the vegetable on Windows, copy it into WSL's Linux filesystem (not `/mnt/c`), and run `chmod` with `-c` through `kabibi.bat -c`. After each operation, use WSL `stat` to assert exact numeric modes. Include octal, symbolic, special-bit, recursive, omitted-`who`/umask, error, and reporting cases.

Set the WSL parent shell's umask to a value other than `0022` before launching a fresh vegetable; confirm the guest starts at `0022`. Then set the guest mask using the shell's `umask` built-in and verify omitted-`who` and explicit-`a` examples against the table above. Confirm the WSL parent shell's umask is unchanged after the vegetable exits.

Run focused `u-root/cmds/core/chmod` package tests and a Kabibi shell integration test that checks both output and file modes. Test `path_chmod` host mode/error propagation directly if shared host code changes; unrelated timestamp tests and architecture cross-compiles are not chmod validation.

### 8.3 Checklist

Keep `kabibi/docs/5-uroot.md` unchecked until the Linux vegetable tests verify resulting modes and the promised options. Record Windows permission limitations separately rather than treating Linux success as proof of Windows parity.

## 9. Separate Follow-Up and Non-Goals

- Propagating guest umask to permissions used when creating files. The current `path_open` route discards the requested permission and the Go host opens with `0666`; changing that is a separate filesystem-create contract and test plan.
- Defining Windows ACL-to-POSIX mode mapping or claiming that Windows supports Unix permission semantics.
- Claiming complete GNU coreutils parity beyond the mode forms and options listed here.
- Marking the command complete based solely on native unit tests or exit status.

## 10. Remediation Plan

The existing implementation has the failures described in section 2. Address them in these six work areas; completion means the corresponding tests pass, not merely that code was changed.

1. **Octal modes and special bits:** Extend octal parsing through `07777`. Translate POSIX setuid, setgid, and sticky bits to Go `os.FileMode` flags before calling `os.Chmod`. Add tests asserting exact resulting permission and special bits.
2. **Symbolic modes:** Replace the current limited parser with parsing and application for the symbolic grammar in section 4, including comma clauses, multiple operation groups, `X`, copied permissions, special bits, empty `=`, and omitted-`who` semantics. Reject malformed expressions before changing any target; test each supported form and boundary.
3. **Guest umask:** Replace the stateless Rusticated Go `syscall.Umask` stub with guest-local state initialized to `0022`, an atomic setter that returns the previous value, and a getter used by chmod. Test initial state, updates, concurrent access, and visibility from shell umask to chmod. Do not add a host umask ABI or change host umask.
4. **Options, reporting, and errors:** Implement the options in section 6, including `-v`, `-c`, `-f`, reference mode, and root protection. Report changed/unchanged paths according to the selected reporting mode; suppress diagnostics for `-f` without converting failures to success. Continue across operands, return failure if any operand fails, and emit each diagnostic once. Test exit status, stdout, stderr, option ordering, and multiple operands.
5. **GNU symlink compatibility:** Replace the current recursive handling that combines `filepath.Walk` with a later `os.Stat` of each path. Follow command-line symlinks, and when `-R` is given, traverse a command-line symlink to a directory. Do not follow or chmod the referents of symlinks encountered below a directory during recursion. Do not add `-H`, `-L`, or `-P`; they are not supported by the GNU `chmod` 9.4 target. Test top-level and nested symlinks separately, including the documented outside-target behavior, traversal errors, and root protection.
6. **End-to-end verification:** Expand `u-root/cmds/core/chmod` tests and Kabibi shell integration tests to assert exact modes, special bits, output, and exit codes. Build the vegetable on Windows, run it from WSL's Linux filesystem, and verify modes with numeric `stat`. Update `kabibi/docs/5-uroot.md` only after these checks pass. Keep Windows permission semantics as a separate follow-up.

## 11. References

- [GNU coreutils 9.4: chmod invocation](https://www.gnu.org/software/coreutils/manual/html_node/chmod-invocation.html)
- [GNU coreutils: Symbolic Modes](https://www.gnu.org/software/coreutils/manual/html_node/Symbolic-Modes.html)
- [GNU coreutils: The Umask and Protection](https://www.gnu.org/software/coreutils/manual/html_node/Umask-and-Protection.html)
- [GNU coreutils: Traversing symlinks](https://www.gnu.org/software/coreutils/manual/html_node/Traversing-symlinks.html)
