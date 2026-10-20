# Mohabbat Stats Wire Contract and Guest Projection

Here is the field-by-field breakdown across all three layers:

1. **The Wire Contract (`AbiStat`)** — shared by the Go host (`env_fs.go`), the Node.js fallback host (`index.js`), the Rust guest sysroot (`abi.rs` & `fs.rs`), and the Go guest syscall layer (`fs_rusticated.go`).
2. **The Guest Go `syscall.Stat_t` & `unix.Stat_t` Projection** — exposed to standard library `os.FileInfo.Sys()` and third-party Unix packages via `patch_go_deps.go`.
3. **Explicitly Excluded Native Fields** — platform-specific C layout artefacts we should omit.

---

### 1. Proposed `AbiStat` Wire Contract (112 bytes, 8-byte aligned)

Currently `AbiStat` is 64 bytes and conflates `ctime` (Unix status change time) with `btime` (creation/birth time), while omitting `dev`, `rdev`, `blksize`, and `blocks`. Expanding it to **112 bytes** (14 × 8-byte slots, zero internal padding) captures the full portable Unix stat model without losing precision or conflating concepts.

| Offset | Wire Field | Type | Linux Host (`syscall.Stat_t`) | macOS Host (`syscall.Stat_t`) | Windows Host (`os.FileInfo` / Win32) | Node.js Host (`fs.Stats` with `bigint: true`) |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `0..4` | `kind` | `u32` | Derived from `S_IFMT` (`File`, `Dir`, `Symlink`, `Char`, `Block`, `Fifo`, `Socket`) | Same as Linux | Derived from `os.FileMode` (`IsDir`, `ModeSymlink`, `ModeCharDevice`, `ModeNamedPipe`, etc.) | Derived from `st.isFile()`, `isDirectory()`, `isSymbolicLink()`, `isFIFO()`, `isSocket()`, `isCharacterDevice()`, `isBlockDevice()` |
| `4..8` | `mode` | `u32` | Full raw `st.Mode` (`S_IFMT` + `0o7777` including setuid/setgid/sticky) | Full raw `uint32(st.Mode)` | Synthesized `S_IFMT` + `Perm()` (`0666`/`0444`, `+0111` for dirs/executables) | Full `Number(st.mode)` |
| `8..12` | `uid` | `u32` | `st.Uid` | `st.Uid` | `0` | `Number(st.uid)` (`0` on Windows) |
| `12..16` | `gid` | `u32` | `st.Gid` | `st.Gid` | `0` | `Number(st.gid)` (`0` on Windows) |
| `16..24` | `size` | `u64` | `uint64(st.Size)` | `uint64(st.Size)` | `uint64(fi.Size())` | `st.size` |
| `24..32` | `modified_ns` | `i64` / `u64` | `st.Mtim` (`Sec*1e9 + Nsec`) | `st.Mtimespec` (`Sec*1e9 + Nsec`) | `LastWriteTime.Nanoseconds()` | `st.mtimeNs` |
| `32..40` | `accessed_ns` | `i64` / `u64` | `st.Atim` (`Sec*1e9 + Nsec`) | `st.Atimespec` (`Sec*1e9 + Nsec`) | `LastAccessTime.Nanoseconds()` (real `Win32FileAttributeData`, not copied from `mtime`) | `st.atimeNs` |
| `40..48` | `changed_ns` | `i64` / `u64` | `st.Ctim` (real status-change time) | `st.Ctimespec` (real status-change time) | Fallback to `modified_ns` (Windows has no separate POSIX `ctime` in `Win32FileAttributeData`) | `st.ctimeNs` |
| `48..56` | `created_ns` | `i64` / `u64` | `0` (unless retrieved via `statx`; see notes) | `st.Birthtimespec` (real birth time) | `CreationTime.Nanoseconds()` (real creation time) | `st.birthtimeNs` |
| `56..64` | `nlink` | `u64` | `uint64(st.Nlink)` | `uint64(st.Nlink)` | `1` (or `NumberOfLinks` when handle info is available) | `st.nlink` |
| `64..72` | `inode` | `u64` | `st.Ino` | `st.Ino` | `0` (or 64-bit FileIndex when available) | `st.ino` |
| `72..80` | `dev` | `u64` | `uint64(st.Dev)` | `uint64(uint32(st.Dev))` | `0` (or `VolumeSerialNumber` when available) | `st.dev` |
| `80..88` | `rdev` | `u64` | `uint64(st.Rdev)` | `uint64(uint32(st.Rdev))` | `0` | `st.rdev` |
| `88..96` | `blksize` | `i64` | `int64(st.Blksize)` | `int64(st.Blksize)` | `4096` (sensible default so block math never divides by zero) | `st.blksize || 4096n` |
| `96..104`| `blocks` | `i64` | `int64(st.Blocks)` | `int64(st.Blocks)` | `int64((size + 511) / 512)` (standard POSIX 512-byte block count derived from size) | `st.blocks || ((size + 511n) / 512n)` |
| `104..112`| `valid_mask` | `u64` | Bitmask of natively populated fields | Bitmask of natively populated fields | Bitmask indicating e.g. `uid`/`gid`/`inode` are synthesized defaults | Bitmask |

#### Field Notes on `AbiStat`
* **Why separate `changed_ns` and `created_ns`?** Previously `AbiStat` only had `modified_ns`, `accessed_ns`, and `created_ns`, and Go's `parseAbiStat` mapped `created_ns` into `st.Ctime`. On Unix, `ctime` is inode metadata change time, whereas birth/creation time is `btime` (`Birthtimespec`). Separating them allows both `Ctim` and `Birthtimespec` to be faithful.
* **Why widen `kind` and `mode` handling?** `createAbiStat` in `env_fs.go` previously did `uint32(fi.Mode().Perm())` and only checked `IsDir()` and `ModeSymlink`. That stripped `setuid`/`setgid`/`sticky` bits (`0o7000`) and misclassified sockets, FIFOs, and device nodes as regular files. Preserving the full Unix `st_mode` (or translating all `os.FileMode` flags) fixes `ls`, `find`, `stat`, and `p9`.
* **Why synthesize `blksize = 4096` and `blocks = ceil(size/512)` on Windows?** Commands like `du` (`u-root/cmds/core/du/du.go`) and `ls -s` sum `st.Blocks` directly. If `Blocks` is left `0` on Windows hosts, `du` reports `0` disk usage for every file. Computing 512-byte blocks when the host OS doesn't provide them gives consistent, useful behaviour everywhere.

---

### 2. Guest Go Projection (`syscall.Stat_t` and `unix.Stat_t`)

To make both standard library `os.Stat` (`fi.Sys().(*syscall.Stat_t)`) and `golang.org/x/sys/unix` (`unix.Stat_t`) work seamlessly with zero data loss, we should **unify `unix.Stat_t = syscall.Stat_t`** (via a type alias in `zerrors_rusticated_wasip1.go`) and give `syscall.Stat_t` the complete set of fields expected by Linux and BSD/macOS Go code:

| Field in `syscall.Stat_t` | Go Type | Purpose & Justification |
| :--- | :--- | :--- |
| `Dev` | `uint64` | Standard Linux/64-bit Unix type (`p9`, `os.SameFile`, `u-root/cmds/core/df`, `switch_root`). |
| `Ino` | `uint64` | Universal across Linux, macOS, and BSDs (`os.SameFile`, `p9`, `du` hardlink deduplication). |
| `Nlink` | `uint64` | `uint64` on Linux amd64 (`p9/internal/stat_standard.go` assigns `Nlink` to a `uint64` field). |
| `Mode` | `uint32` | **Crucial change**: currently `int` in `fs_rusticated.go`, which breaks every caller expecting Linux `uint32` (including `p9`, `u-root/pkg/strace`, `filepath-securejoin`). Only `prebuild.go` casts `FileMode(fs.sys.Mode)`, which works identically with `uint32`. |
| `Uid` | `uint32` | Universal `uint32` across Linux and macOS (`ls`, `which`, `p9`). |
| `Gid` | `uint32` | Universal `uint32` across Linux and macOS (`ls`, `which`, `p9`). |
| `Rdev` | `uint64` | Used with `unix.Major(st.Rdev)` / `unix.Minor(st.Rdev)` in `ls`, `mknod`, `cpio`, and `p9`. |
| `Size` | `int64` | **Crucial change**: currently `uint64` in `fs_rusticated.go`, whereas every Unix `syscall.Stat_t` and `unix.Stat_t` uses `int64` (`p9`, `u-root`, `x/sys/unix`). `os/stat_wasip1.go` already does `int64(fs.sys.Size)`, so changing `Size` to `int64` is 100% compatible with the stdlib overlay while fixing all Unix callers. |
| `Blksize` | `int64` | Linux amd64 uses `int64`; `p9` (`stat_standard.go`) assigns `nativeStat.Blksize` directly to an `int64` field. |
| `Blocks` | `int64` | Universal `int64` on Linux and macOS; required by `du`, `ls`, `p9`, and `strace`. |
| `Atim`, `Mtim`, `Ctim` | `Timespec` | Standard Linux field names (`p9/internal/stat_standard.go`, `u-root/pkg/ls/fileinfo_linux.go`, `go-git/worktree_unix_other.go`, `touch`). Must be populated by `parseAbiStat`! |
| `Atimespec`, `Mtimespec`, `Ctimespec`, `Birthtimespec` | `Timespec` | Standard BSD/macOS field names (`u-root/pkg/ls/fileinfo_unix.go`, `go-git/worktree_bsd.go`, `p9/internal/stat_bsd.go`). Keeping and populating both the `*tim` and `*timespec` pairs costs 48 bytes in Go memory and makes **both** Linux-targeted and BSD/macOS-targeted Go files compile and run without patching. |
| `Filetype`, `Atime`, `Mtime`, `Ctime` | `uint8`, `uint64` | Retained on `syscall.Stat_t` solely because Go's own `src/os/stat_wasip1.go` reads `fs.sys.Filetype`, `fs.sys.Mtime`, and `st.Atime`. Keeping them avoids extra patches to `src/os/`. |

In addition, `unix/zerrors_rusticated_wasip1.go` should expose:
* `type Stat_t = syscall.Stat_t`
* `func Stat(path string, st *Stat_t) error`, `Lstat`, `Fstat`, `Fstatat` delegating to `syscall`
* `func NsecToTimespec(nsec int64) Timespec` and `TimespecToNsec(ts Timespec) int64` (used by `p9` and `u-root`).

---

### 3. Fields We Should Explicitly Exclude (and Why)

| Excluded Field | Source OS | Justification for Exclusion |
| :--- | :--- | :--- |
| `X__pad0`, `Pad_cgo_0`, `X__unused`, `_` | Linux / macOS | C struct ABI alignment and reserved kernel expansion slots. Guest Go code in Rusticated never passes `Stat_t` across a raw C/kernel pointer boundary—`parseAbiStat` decodes the wire buffer into Go struct fields explicitly. |
| `Lspare`, `Qspare` | macOS / FreeBSD | Unused reserved fields in Darwin's `struct stat64`. No user-space code reads them. |
| `Flags`, `Gen` (`st_flags`, `st_gen`) | macOS / BSD | BSD-specific `chflags(2)` user flags and inode generation number. Not present on Linux or Windows, and not referenced by `u-root`, `kabibi`, `p9`, or `go-git`. Leaving them out keeps the cross-platform contract focused on universal concepts. |
| `[AMCB]tim_ext` | AIX / z/OS | Architecture-specific timestamp extensions hidden even by `x/sys/unix/mkpost.go`. |

---

### Summary of Impact

1. **Zero-loss Unix fidelity on Linux & macOS hosts**: `washmhost` extracts the real `syscall.Stat_t` from `fi.Sys()` in OS-specific helpers (`stat_unix.go` / `stat_windows.go`) instead of zeroing `Uid`, `Gid`, `Ino`, `Dev`, `Rdev`, `Blocks` or copying `Mtime` over `Atime`/`Ctime`.
2. **Intelligent Windows defaults**: `washmhost` on Windows reads real `LastAccessTime` and `CreationTime` from `syscall.Win32FileAttributeData`, synthesizes `Blksize = 4096` and `Blocks = (Size + 511) / 512`, and sets proper `S_IFMT` mode bits.
3. **Fixes `p9` and `u-root` under Rusticated**: `github.com/hugelgupf/p9/internal` gets `unix.Stat_t` (and its `stat_standard.go` gate-flips cleanly because `Stat_t` matches the Linux field names and types), unblocking the Rusticated Kabibi test suite.

