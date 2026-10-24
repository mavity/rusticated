# GNU-Compatible Linux `chmod` Subset

**Status:** Revised proposal. Implementation is paused pending review.

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

## 3. Goals

1. Implement the mode expressions and options explicitly listed in this document on Linux.
2. Apply a mutable, guest-local umask when GNU rules require it.
3. Match GNU's documented symlink traversal choices; do not add a separate containment rule that changes `-H` or `-L` behavior.
4. Return errors when a requested operation cannot be honored; do not report false success.
5. Validate command behavior and resulting modes in the Rusticated guest on WSL's Linux filesystem.

This is a **defined GNU-compatible subset**, not a claim of complete coreutils parity. Unsupported GNU options and mode extensions must be listed and rejected explicitly rather than silently accepted.

## 4. Mode-Expression Scope

### 4.1 Octal modes

Accept permission and special bits in the range `0000` through `7777`. Convert POSIX setuid, setgid, and sticky bits to the corresponding Go `os.FileMode` flags before calling `os.Chmod`; do not pass the raw POSIX octal value as a Go `FileMode`.

### 4.2 Symbolic modes

Parse GNU symbolic modes, including comma-separated clauses and multiple operation/permission groups within a clause:

```text
[who] operator permissions [, ...]
```

For example, `og+rX-w` contains multiple groups within one clause. Support:

- `who`: `u`, `g`, `o`, `a`; omitted `who` means all classes subject to the current umask.
- Operators: `+`, `-`, `=`.
- Permissions: `r`, `w`, `x`, conditional execute `X`, set-ID `s`, sticky `t`, and permission copying from `u`, `g`, or `o`.
- Sequential evaluation: each clause observes the mode produced by preceding clauses.

`X` adds execute/search only for directories or when an execute bit was already set before the operation. A permission-copy clause uses the source class's current bits at that point in the expression.

When `who` is omitted, GNU defaults to all classes while protecting permission bits selected by the current umask; explicit `a` does not apply that protection. `=` still replaces permissions rather than preserving every masked bit. Verified GNU examples with umask `0022`:

| Starting mode | Expression | Result | Meaning |
|---:|---|---:|---|
| `0444` | `+w` | `0644` | Omitted `who` protects group/other write from being added. |
| `0444` | `a+w` | `0666` | Explicit `a` is not filtered by umask. |
| `0666` | `-w` | `0466` | Masked group/other write bits are not removed. |
| `0777` | `=rw` | `0644` | Replace mode with requested permissions filtered by umask. |
| `0777` | `=` | `0000` | Empty permissions with `=` clear permissions. |
| `0600` | `g=u` | `0660` | Explicit class selection copies user bits to group. |
| `0700` | `u=rw,g=u,o=g` | `0666` | Comma clauses apply in order and copy current source-class bits. |

Empty permission lists are valid only with `=`. `s` and `t` set special bits according to the specified classes; umask protects ordinary `rwx` permissions, not special bits.

Malformed clauses and invalid octal values must produce clear errors without changing any target.

## 5. Guest Umask State

The umask used by chmod is **guest process state**, not host process state. No host umask ABI is needed for chmod.

1. A new Rusticated Go guest starts with umask `0022`, independent of the host OS and host process umask.
2. `syscall.Umask(mask)` atomically replaces the guest mask, restricted to `0777`, and returns the previous mask.
3. A guest-local getter returns the current mask to chmod without changing it.
4. Umask updates and chmod reads observe the same process state. The host process-global umask is never modified.
5. This scope covers chmod interpretation only; it does **not** claim umask filtering for newly created files.

## 6. Command-Line Behavior

Implement and test this Linux subset:

- `-R`, `--recursive`
- `-v`, `--verbose`
- `-c`, `--changes`
- `-f`, `--silent`, `--quiet`
- `--reference=RFILE`
- `-h`, `--no-dereference`
- `-H`, `-L`, `-P` recursive traversal controls
- `--preserve-root` (default) and `--no-preserve-root`
- `--` to end option parsing

`-c` reports only files whose modes changed; `-v` reports every processed file; `-f` suppresses ordinary diagnostics. Continue processing other operands after an operand error, but return failure overall. Define and test precedence when these options are combined.

`-h` controls whether chmod acts on a symlink or its referent; it is independent of recursive traversal selection. If Linux cannot change a symlink's own mode, return the platform error rather than silently changing its referent.

## 7. Recursive and Symlink Semantics

Match GNU traversal semantics when `-R` is used:

- `-P` is the default and does not traverse symlinks.
- `-H` traverses a command-line symlink to a directory, but not symlinks encountered below it.
- `-L` traverses every symlink to a directory encountered.

Traversal and symlink dereferencing are separate decisions. `-H` and `-L` can intentionally operate on paths outside the operand's lexical subtree; this proposal adds no physical containment boundary. `--preserve-root` protects the root operand according to GNU behavior, not arbitrary symlink targets.

Use `Lstat`/directory-entry type information during traversal so a symlink discovered under the default `-P` policy is not accidentally followed by a later `Stat`/`Chmod` call. Test command-line symlink operands separately from symlinks encountered during traversal.

## 8. Test Plan

### 8.1 Mode and command tests

Add focused tests for:

- Octal permission and special bits, including setuid, setgid, and sticky.
- Symbolic `rwxXst`, copied permissions, combined operation groups, comma-separated clauses, omitted `who`, umask behavior for `+`, `-`, and `=`, and sequential application.
- Invalid syntax and boundary values.
- `Umask` replacement/return values, initial `0022`, concurrent reads/updates, and chmod observing shell-updated guest state.
- `-v`, `-c`, `-f`, `--reference`, `-h`, `-H`, `-L`, `-P`, root protection, and `--` behavior.
- Recursive traversal modes, command-line symlink operands, and nested symlinks, including intentionally followed targets outside the lexical operand subtree for `-H`/`-L`.
- Multiple operands where an error occurs between successful changes.

### 8.2 Rusticated integration on WSL

Build the vegetable on Windows, copy it into WSL's Linux filesystem (not `/mnt/c`), and run `chmod` with `-c` through `kabibi.bat -c`. After each operation, use WSL `stat` to assert exact numeric modes. Include octal, symbolic, special-bit, recursive, omitted-`who`/umask, error, and reporting cases.

Set guest umask values using the shell's `umask` command, then verify omitted-`who` and explicit-`a` examples against the table above. Verify shell umask updates do not alter the WSL host's process umask.

### 8.3 Checklist

Keep `kabibi/docs/5-uroot.md` unchecked until the Linux vegetable tests verify resulting modes and the promised options. Record Windows permission limitations separately rather than treating Linux success as proof of Windows parity.

## 9. Separate Follow-Up and Non-Goals

- Propagating guest umask to permissions used when creating files. The current `path_open` route discards the requested permission and the Go host opens with `0666`; changing that is a separate filesystem-create contract and test plan.
- Defining Windows ACL-to-POSIX mode mapping or claiming that Windows supports Unix permission semantics.
- Claiming complete GNU coreutils parity beyond the mode forms and options listed here.
- Marking the command complete based solely on native unit tests or exit status.

## 10. References

- [GNU coreutils: Symbolic Modes](https://www.gnu.org/software/coreutils/manual/html_node/Symbolic-Modes.html)
- [GNU coreutils: The Umask and Protection](https://www.gnu.org/software/coreutils/manual/html_node/Umask-and-Protection.html)
- [GNU coreutils: Traversing symlinks](https://www.gnu.org/software/coreutils/manual/html_node/Traversing-symlinks.html)
