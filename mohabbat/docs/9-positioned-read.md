# Rusticated Positioned-Read ABI

**Status:** Implemented

## 1. Purpose and Scope

Add asynchronous positioned reads to the existing Rusticated `read` operation.
This lets guests implement `Pread`-style APIs without seeking the shared file
handle or changing its current position.

The import signature remains unchanged. This specification uses the standard
24-byte `Overlapped` header and a read-specific trailing request payload, as
described in the [overlapped memory layout](1-pty.md#3-overlapped-memory-layout).
It does not add blocking I/O or change the cancellation and buffer-lifetime
contract of existing overlapped operations.

## 2. `Overlapped.flags` Allocation for `read`

Bits are numbered from the least-significant bit of the 32-bit word at offset
0 of the `Overlapped` header.

| Bits | Allocation | `read` rule |
|---|---|---|
| 0 | Shared completion bit | Guest submits it clear; host sets it on completion. |
| 1–23 | Shared-reserved | Must be zero; `read` must not assign meaning to them. |
| 24 | `READ_FLAG_POSITIONAL` | Guest sets it to request a positioned read. |
| 25–31 | Operation-specific reserved | Must be zero for `read` until separately specified. |

The request flag is input only. On completion, the host writes the standard
completion flag and does not preserve the request flag.

## 3. Request Layout

The `read` import remains:

```text
read(overlapped, handle, buffer_ptr, buffer_len)
```

When `READ_FLAG_POSITIONAL` is clear, the request uses the standard 24-byte
header and retains ordinary sequential-read behavior.

When `READ_FLAG_POSITIONAL` is set:

1. The guest provides at least 32 readable bytes beginning at `overlapped`.
2. Bytes 24–31 contain the absolute file offset as an unsigned 64-bit
   little-endian integer.
3. The host snapshots the flags and offset before starting asynchronous work;
   it must not retain a pointer to the trailing request payload.

All other request flag bits, including the completion bit, must be zero.
Malformed or unsupported request bits must complete with `EINVAL`.

## 4. Positioned-Read Semantics

The offset is measured from the beginning of the file. A positioned read must
not observe or modify the handle's shared current position. It must use a
host positional-read facility; implementing it as `seek` followed by `read`
is not conforming, including when reads on the same handle can overlap.

The requested length is the same `buffer_len` used by ordinary `read`.
A zero-length request succeeds with zero bytes. A representable offset at or
beyond EOF succeeds with zero bytes; a short read succeeds with the number of
bytes read. On success, `result_ext` is that byte count and the host writes
those bytes to the requested guest buffer.

## 5. Errors

- Invalid handle: `EBADF`.
- Positioned read on a handle that has no positional-read operation, such as a
  pipe or socket: `ESPIPE`.
- Negative offset in a guest API that accepts signed offsets: `EINVAL`, before
  submitting the ABI request.
- Offset not representable by the host's positional-read API: `EOVERFLOW`.
- Invalid output-buffer memory or an unreadable trailing request payload:
  `EINVAL`.
- The guest must provide a valid writable 24-byte overlapped header. If that
  header itself is invalid, the host has no valid location in which to report
  an error completion.
- Other I/O errors use the host's existing errno mapping.

`io.EOF` from a host positional-read API represents a successful EOF or short
read and must not be surfaced as an error.

## 6. Guest APIs

- Rusticated Go implements `syscall.Pread(fd, buffer, offset)` using the
  positioned form of `read`. It rejects negative offsets and does not emulate
  `Pread` with `Seek` plus `Read`.
- The async Rusticated sysroot provides `File::read_at(&self, buffer, offset)`,
  returning the byte count and buffer in the same style as its async `read`
  API. It uses the positioned form of the existing ABI import.

## 7. Host Requirements

Every host implementation of the `read` import must support the request flag
and trailing offset:

- Go uses `io.ReaderAt` for positioned reads.
- Node.js uses an explicit file-read position.
- The browser host reads from the requested position in the stored file
  content without advancing the handle's offset.

Hosts retain the existing overlapped completion and cancellation behavior.
Request metadata is copied before async work; buffer ownership and cancellation
follow the existing `read` contract.

## 8. Compatibility and Deployment

This extension adds no capability negotiation. A guest may submit
`READ_FLAG_POSITIONAL` only when paired with a host implementing this
specification. Guest and host updates must therefore be deployed together;
an older host may otherwise treat the request as an ordinary sequential read.

## 9. Conformance Tests

Implementations must verify that:

1. Positioned reads return bytes from the specified offset.
2. A positioned read leaves the handle's current position unchanged, including
   when followed by an ordinary read.
3. EOF, short reads, zero-length buffers, invalid flags, invalid offsets, and
   non-positionable handles follow this specification.
4. The Node.js, browser, and Go hosts preserve the existing sequential-read
   behavior when `READ_FLAG_POSITIONAL` is clear.
