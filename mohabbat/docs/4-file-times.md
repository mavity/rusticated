# Rusticated Filesystem Timestamp-Setting ABI

**Status:** Implemented. The Go guest syscall bridge and Go, Node, and browser host exports use this contract.

## 1. Purpose

Add guest-to-host support for setting file access and modification times. This is needed by standard Go `os.Chtimes` / `Utimensat` users, including `cp -p` and `cp -a`.

This is not a generic metadata setter. The existing `path_chmod` operation changes mode bits only; `path_stat` reads metadata. Timestamps have their own platform semantics and should be set through a dedicated operation.

## 2. Current State and Gap

- `Utimensat` and `UtimesNano` now call the guest `path_set_times` import. The wrapper translates `UTIME_NOW`, `UTIME_OMIT`, `AT_SYMLINK_NOFOLLOW`, and relative directory-fd paths.
- Go, Node, and browser hosts export the same eight-argument overlapped `path_set_times` operation.
- The Go host uses native timestamp syscalls on Linux and Darwin, `SetFileTime` on Windows, and `os.Chtimes` on other supported targets. Unsupported no-follow behavior is an explicit `ENOTSUP`.
- Node uses asynchronous `utimes` and `lutimes`; precision is limited by Node's numeric-time API and the underlying filesystem.
- Browser `AsyncFS` persists independent integer-nanosecond atime/mtime values, migrates legacy millisecond `mtime` entries when they are written, and emits the shared 112-byte `AbiStat`. Reads do not update the virtual atime. Symlink timestamp operations return `ENOTSUP`.

The original gap—setting atime/mtime through the Rusticated host boundary—is closed.

## 3. Proposed ABI Shape

Add one overlapped host import named `path_set_times`:

```text
path_set_times(
    overlapped: *mut Overlapped,
    path_ptr: *const u8,
    path_len: u32,
    atime_sec: i64,
    atime_nsec: u32,
    mtime_sec: i64,
    mtime_nsec: u32,
    flags: u32,
)
```

The seconds/nanoseconds pair avoids limiting representable dates to the `i64` nanosecond range and matches native `timespec` APIs. `nsec` must be in `[0, 1_000_000_000)`.

Proposed flag bits:

| Bit | Name | Meaning |
|---:|---|---|
| `0x01` | `SET_ATIME` | Set atime from `atime_sec` / `atime_nsec`. |
| `0x02` | `ATIME_NOW` | Set atime to the host's current time; conflicts with `SET_ATIME`. |
| `0x04` | `SET_MTIME` | Set mtime from `mtime_sec` / `mtime_nsec`. |
| `0x08` | `MTIME_NOW` | Set mtime to the host's current time; conflicts with `SET_MTIME`. |
| `0x10` | `NOFOLLOW` | Apply to the symlink itself rather than its target. |

If neither the explicit-set nor `_NOW` bit is set for a timestamp, leave that timestamp unchanged. Reject unknown or contradictory flags with `EINVAL`. A backend that cannot honor `NOFOLLOW` must return `ENOTSUP` rather than silently following the link. `ctime` and birth/creation time are deliberately excluded: they are not generally user-settable through portable filesystem APIs.

The guest Go `Utimensat` wrapper should translate its `Timespec` values and `AT_SYMLINK_NOFOLLOW` into this ABI. `UTIME_NOW` and `UTIME_OMIT` map to the corresponding flags. Relative `dirfd` paths must be resolved using the existing Rusticated fd/path rules before invoking the path-based operation.

## 4. Async and Error Contract

The call follows the existing overlapped ABI:

1. Validate and copy the path and scalar arguments while guest memory is available.
2. Register the `overlapped` operation before starting work.
3. Perform host filesystem work without blocking the guest run loop. Promise/callback-based hosts retain their existing async model; Go host work should use the host's operation/queue pattern where the filesystem call may block.
4. Complete through the host completion queue with the existing errno mapping.
5. Cancellation must prevent a stale completion from being published, following the existing operation-lifecycle rules.

Invalid paths, permissions, and unsupported no-follow behavior must be returned as errors. The ABI must not report success when a backend ignored a requested timestamp.

## 5. Backend Implementation Notes

### 5.1 Go `washmhost` native host

- `env_impl.go` exports `path_set_times`; `env_fs_times.go` validates requests and completes through the asynchronous file-operation queue.
- Linux uses `UtimesNanoAt`, including `UTIME_NOW`, `UTIME_OMIT`, and `AT_SYMLINK_NOFOLLOW`. Darwin resolves omitted values before using `UtimesNanoAt`.
- Windows uses `SetFileTime` with 100-nanosecond granularity; omitted values are read and preserved before the update.
- Other platforms use `os.Chtimes` for following requests and return `ENOTSUP` for no-follow mutation.

### 5.2 Node.js host

- `js/node/index.js` exports the same signature and uses `fs.promises.utimes` or `fs.promises.lutimes`.
- Omitted timestamps are read before the update because Node has no `UTIME_OMIT` argument. Unsupported `lutimes` is reported as `ENOTSUP`; the host does not silently follow a no-follow request.
- Seconds/nanoseconds are converted to Node's numeric seconds API, so sub-microsecond fidelity is not guaranteed.

### 5.3 Browser host and `AsyncFS`

- `js/browser/index.js` exports the operation through the existing queued promise/operation lifecycle.
- IndexedDB entries persist separate `atimeNs` and `mtimeNs` BigInts. Legacy `mtime` values remain readable and are copied into both timestamps when migrated; writes update mtime without changing atime.
- The virtual filesystem uses a no-atime-on-read policy. It cannot perform symlink timestamp operations and returns `ENOTSUP`.
- Browser `path_stat` and its tests now use the shared 112-byte record. Its synthetic ctime and birthtime values are not marked valid.

## 6. Compatibility and Rollout

This is an additive import, so the Go, Node, and browser export maps were updated together with the guest import. A maintained host that cannot implement a requested behavior must return `ENOSYS` or `ENOTSUP`; it must not omit the import or report false success.

Keep `path_chmod` unchanged. It remains the mode-bit operation and should not be generalized to carry timestamps or unrelated metadata.

## 7. Test Coverage

- Go host tests cover explicit timestamps, `NOW`, omitted-field preservation, invalid values, and no-follow symlink updates where the host permits them.
- Node tests cover explicit/`NOW` timestamps, omitted-field preservation, and no-follow symlink updates where available; browser tests cover independent timestamps, `NOW`, invalid values, unsupported symlinks, and the 112-byte stat payload.
- End-to-end Rusticated validation passed through the custom Go sysroot: `touch -d 2001-01-01T00:00:00Z` succeeded, and `cp -a` preserved the displayed mtime on the copy.

## 8. `cp` Integration

The `wasip1` timestamp skip has been removed from `u-root/pkg/cp/platform_other.go`. Its guest copy helper uses `syscall.UtimesNanoAt` with `AT_SYMLINK_NOFOLLOW` for symlink timestamps; other non-Linux guest targets retain their explicit unsupported behavior. Symlink preservation was not exercised in the Rusticated shell because symlink creation is currently unavailable there. `cp -a` may continue its documented best-effort policy for genuinely unsupported metadata, but supported backends must preserve timestamps; explicit preserve requests must surface errors rather than silently succeed.
