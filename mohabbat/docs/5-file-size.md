# Rusticated File-Size ABI

**Status:** Proposed

## 1. Purpose

Add an asynchronous Rusticated ABI operation that sets the logical length of an
open file. This enables descriptor-based truncation and lets the custom Go
syscall layer implement `Ftruncate` and `Truncate` without pretending success
when the operation is unavailable.

The current ABI can truncate a file to zero during `path_open` with
`O_TRUNC`, but it has no operation for setting an arbitrary length. Seeking
changes a handle's offset; it does not change the file's length.

## 2. ABI contract

Add the handle operation:

```text
handle_truncate(overlapped, handle, length: i64)
```

It sets the logical length of the file referred to by `handle` to exactly
`length` bytes. The operation uses the existing `Overlapped` completion
contract; on success, `result_ext` is zero.

The operation must:

1. Reject a negative length with `EINVAL`.
2. Return `EBADF` for an invalid or closed handle.
3. Require a handle that supports file-length changes. Preserve the host's
   mapped error for permission failures, unsupported handle types, and other
   filesystem errors; never report success if the host did not change the
   length.
4. Shrink by discarding bytes past the new end. Extend such that reads from
   the newly exposed range return zero bytes. Sparse allocation is permitted
   but is not an ABI guarantee.
5. Leave the handle's current offset unchanged, including when it is beyond
   the new end.
6. Treat a length beyond the backend's representable limit as a size error
   (`EFBIG` or the platform's equivalent). Do not round or wrap the value.

Setting the length to zero has the same file-content effect as `O_TRUNC`.
`O_TRUNC` remains unchanged and continues to truncate during open. The
operation updates normal host-managed modification metadata; it does not
accept timestamps or other attributes.

## 3. Async behavior and errors

Hosts must follow the existing overlapped-operation lifecycle: associate the
operation with its `Overlapped` record and handle, complete it exactly once,
and suppress stale completion after cancellation. Cancellation does not promise
to roll back a truncation that the host has already performed. The operation
must retain a safe reference to its handle until the host operation finishes.

Host filesystem errors must be mapped through the existing Rusticated errno
mapping. In particular, invalid handles and negative lengths are not
success-shaped fallbacks. A backend that cannot implement the operation must
return its established unsupported-operation error.

## 4. Guest syscall mapping

Declare `handle_truncate` in the Rusticated ABI declarations and provide the
corresponding import in each host environment.

In `rusticated-go/syscall`, implement:

- `Ftruncate(fd, length)` by resolving `fd` through the existing descriptor
  table and invoking `handle_truncate`.
- `Truncate(path, length)` by opening the path with write access, invoking
  `Ftruncate`, and closing the descriptor on every path. Return the truncation
  error in preference to a later close error; if truncation succeeds, return a
  close error rather than silently discarding it.

The path-based wrapper must use the existing path-open behavior and native
symlink semantics. The ABI operation itself remains handle-based so the
mutation applies to the opened object rather than re-resolving a path.

## 5. Host implementations

- **Go host:** resolve the ABI handle to its open file and call the host
  file's `Truncate` method. Run and complete it through the existing async
  filesystem operation path.
- **Node host:** resolve the handle-table entry and use Node's asynchronous
  file-descriptor truncation operation. Validate the signed length and reject
  values not exactly representable by the host API.
- **Browser host:** resize the existing virtual-filesystem entry without
  replacing its identity. Preserve the existing prefix when shrinking or
  growing, zero-fill newly exposed bytes, update the entry's size and content,
  and persist it through `AsyncFS`. Reject lengths outside the safe integer or
  allocation limits instead of rounding, wrapping, or returning success.

## 6. Validation

Tests must cover the ABI and each host backend where available:

- Shrink, extend, and zero-length operations; verify preserved bytes and
  zero-filled extension.
- Verify the current offset is unchanged and invalid descriptors fail.
- Verify negative and out-of-range lengths return errors without changing the
  file.
- Verify `Truncate` and `Ftruncate` propagate errors and close path-opened
  descriptors.
- Verify browser entry size/content is persisted and remains visible through
  subsequent opens.
- Verify cancellation does not produce duplicate or stale completion.
- Add a Rusticated end-to-end check of the `truncate` command, including
  command failure when the underlying operation fails.
