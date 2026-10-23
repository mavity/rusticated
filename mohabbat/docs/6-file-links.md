# Rusticated File-Link ABI

**Status:** Proposed

## 1. Purpose and Scope

Add asynchronous Rusticated filesystem operations for creating hard links,
creating symbolic links, and reading a symbolic link's stored target. The
operations express filesystem semantics, not operating-system-specific
mechanisms. Host filesystems and drivers determine which operations they
support; unsupported operations must return an error rather than emulate a
different kind of link.

This ABI does not expose arbitrary reparse points, directory junctions, volume
mount points, bind mounts, or copy-on-write clones. These have distinct
semantics and are not interchangeable with hard or symbolic links. A future
junction operation, if needed, must be separately named and specified.

## 2. Current State

- `path_stat` can identify a symbolic link and can return its metadata without
  following it. Its stat record includes a hard-link count when available.
- The ABI has no operation to create a hard link or symbolic link, or to read a
  link's stored target.
- The Rusticated Go syscall functions `Link`, `Symlink`, and `Readlink`
  currently return `ENOSYS`.
- Host capabilities vary by filesystem, server, driver, and permissions.
  Mounting a filesystem on a host does not imply that the host driver exposes
  every link operation.

## 3. ABI Operations

Add the following overlapped imports to the Rusticated ABI and corresponding
exports to every host environment:

```text
path_link(
    overlapped: *mut Overlapped,
    source_ptr: *const u8,
    source_len: u32,
    new_path_ptr: *const u8,
    new_path_len: u32,
    flags: u32,
)

path_symlink(
    overlapped: *mut Overlapped,
    target_ptr: *const u8,
    target_len: u32,
    link_path_ptr: *const u8,
    link_path_len: u32,
    target_kind: u32,
)

path_readlink(
    overlapped: *mut Overlapped,
    path_ptr: *const u8,
    path_len: u32,
    out_ptr: *mut u8,
    out_len: u32,
)
```

Paths, targets, and link targets use the same byte representation and
non-NUL-terminated convention as the existing path ABI. Hosts must validate
guest-memory ranges before starting asynchronous work and retain or copy all
inputs needed until the operation completes.

### 3.1 `path_link`

Create a new hard-link name for the file selected by `source_ptr`. The
destination must not already exist; this operation never replaces it. The
operation must be performed by the host filesystem, not by copying file
contents.

The initial flag set is:

| Value | Name | Meaning |
|---:|---|---|
| `0x0001` | `LINK_FLAG_FOLLOW_SOURCE` | Follow the final source symlink and link its referent. |

With the flag clear, request a hard link to the source directory entry itself.
This is the default and corresponds to physical source-link handling (`-P`);
the flag corresponds to logical source-link handling (`-L`). Reject unknown
flag bits with `EINVAL`. If a host cannot honor the requested source-link
behavior, it must return an explicit unsupported or native filesystem error.

The ABI does not impose support for directory hard links. It must not silently
turn a directory request into a junction or mount point. Filesystem limits,
same-filesystem/volume restrictions, and security policy are reported by the
host.

### 3.2 `path_symlink`

Create a symbolic link at `link_path_ptr` containing the supplied target
string. The target is link data, not a path to resolve during creation:

- Do not require the target to exist.
- Do not resolve, absolutize, or otherwise rewrite the target before passing
  it to the host's native symlink operation.
- Relative targets remain relative to the link's containing directory.
- A dangling target is valid when the backend supports symbolic links.

`target_kind` is a hint for host APIs that distinguish file-target and
directory-target symbolic links:

| Value | Name | Meaning |
|---:|---|---|
| `0` | `SYMLINK_TARGET_UNKNOWN` | The guest does not know or specify the target kind. |
| `1` | `SYMLINK_TARGET_FILE` | The target is intended to be a file. |
| `2` | `SYMLINK_TARGET_DIRECTORY` | The target is intended to be a directory. |

Other values return `EINVAL`. Unix-like hosts may ignore this hint. A host
that requires a kind may infer it from an existing target when the hint is
unknown, but must not claim that an unknown, dangling target is a directory.
The guest interface should allow callers to specify a target kind explicitly
for dangling directory links.

The destination must not already exist; this operation never replaces it.

### 3.3 `path_readlink`

Read the target stored in the final path component without following that
component. On success, write the target bytes without a trailing NUL and set
`result_ext` to the number of bytes written.

If the output buffer is too small, complete with `ERANGE` and set `result_ext`
to the required target length. The guest can then retry with a suitably sized
buffer. This avoids silently returning a truncated target. Intermediate path
components follow the host's normal path-resolution rules.

## 4. Error and Async Contract

All three operations use the existing `Overlapped` lifecycle:

1. Validate scalar arguments and guest-memory ranges.
2. Register the operation before asynchronous host work begins.
3. Complete exactly once through the host's established completion path.
4. Suppress stale guest-memory completion after cancellation.
5. Keep operation inputs and any host resources alive until the host work
   finishes.

Cancellation does not promise to roll back a link creation that already
completed on the filesystem. Hosts must report filesystem and permission
errors through the existing errno mapping. Relevant results include
`EEXIST`, `EXDEV`, `EACCES`, `EPERM`, `ENOENT`, `ENOTDIR`, `ELOOP`, `EINVAL`,
`ERANGE`, and `ENOTSUP`/`ENOSYS`. A backend that cannot perform the requested
operation must not return success.

## 5. Guest Mapping

### 5.1 Rusticated Go

Declare imports for all three operations and implement:

- `Link(source, newPath)` using `path_link` with the specified default
  no-follow source semantics.
- The Go syscall bridge's follow-source variant, if required by the supported
  Go API, using `LINK_FLAG_FOLLOW_SOURCE`.
- `Symlink(target, linkPath)` using `path_symlink`.
- `Readlink(path, buffer)` using `path_readlink`; retry when the returned
  required length exceeds the buffer, while preserving the Go API's return
  count and error behavior.

Path resolution must use existing Rusticated guest path rules. The symlink
target must remain link data; do not resolve it relative to the guest working
directory before calling the ABI.

### 5.2 Rusticated Rust

Declare the ABI imports and provide async filesystem wrappers for hard-link
creation, symbolic-link creation, and reading a link target where those
operations are part of the Rusticated filesystem API. They must use the
overlapped runtime and return host errors; they must not introduce blocking
filesystem calls on the WASM guest.

## 6. Host Implementations

### 6.1 Go `washmhost`

- Resolve guest paths using the existing host path resolver.
- Use native filesystem operations for hard links and link-target reads.
- Use native symbolic-link operations, including a platform-specific API where
  necessary to honor `target_kind`.
- Perform potentially blocking filesystem calls outside the guest run loop and
  publish completion through the existing file-operation queue.
- Preserve native errors, including unsupported behavior and cross-volume
  restrictions.

### 6.2 Node.js host

- Use asynchronous filesystem link, symlink, and readlink operations.
- Pass `target_kind` to the host API when applicable.
- Map native errors using the shared Rusticated errno mapping and complete
  through the existing operation queue.

### 6.3 Browser host and `AsyncFS`

- Represent a symbolic link as a link entry containing its target, rather than
  as copied target contents.
- Integrate link traversal consistently with open, stat, remove, rename, and
  directory operations. `path_stat` with its no-follow flag must continue to
  describe the link entry itself; `path_readlink` returns the stored target.
- To support hard links, `AsyncFS` must represent directory entries separately
  from shared file data/identity. Writes and metadata changes through either
  name must affect the same object, and the reported link count must reflect
  the names. Copying the entry or content is not a hard link.
- If that object-identity model is unavailable, return `ENOTSUP` for hard-link
  creation. Do not emulate a hard link by copying.
- Persist mutations before reporting success and use the existing async
  completion/cancellation lifecycle.

## 7. `ln` Command Semantics

The ABI operations create links only when the destination is absent. Higher
level command behavior remains in `ln`:

- Default `ln` requests a hard link.
- `ln -s` requests a symbolic link; it must not fall back to a junction, hard
  link, copy, or shortcut.
- GNU source-following options map to the hard-link source flag.
- `-r` computes a relative target in the guest and passes that target string
  unchanged to `path_symlink`.
- `-n` and destination-directory handling are command-layer path-selection
  rules, not filesystem link types.
- `-f` replacement is not an ABI create flag. The command removes the
  destination according to its documented rules, then requests creation. The
  sequence may not be atomic on every backend and must report failure.
- An explicit junction option is out of scope for this ABI. If added later, it
  must have distinct command and ABI semantics.

## 8. Validation

Tests must cover:

- Hard links share file contents and metadata; source-follow and no-follow
  behavior is tested where supported.
- Symbolic links preserve relative target strings, support dangling targets
  where available, and honor file/directory target hints.
- `path_readlink` returns the exact stored target without NUL termination and
  reports the required size for undersized buffers.
- `path_stat` no-follow reports the link itself; normal path operations follow
  links according to each operation's contract.
- Existing destinations, missing sources, invalid flags/kinds, permissions,
  cross-filesystem hard links, and unsupported filesystems return errors and
  never success-shaped fallbacks.
- Browser persistence and shared hard-link identity are tested if hard links
  are implemented there; otherwise the backend returns `ENOTSUP`.
- Cancellation does not publish duplicate or stale completion. A cancelled
  creation may still have taken effect if the host completed it before
  cancellation.
- Rusticated end-to-end tests exercise `ln` creation, `readlink`, follow/no-
  follow behavior, and expected backend limitations.

Filesystem-dependent tests must detect and report genuine unsupported
capabilities rather than treating a skipped operation as a successful link.
