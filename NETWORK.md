# Rusticated Network ABI — Implementation Plan

## Overview

This document specifies the complete plan for end-to-end network support in the Rusticated
WASM ABI. It covers every layer from the host socket primitives already implemented through
to the two demo mini-projects.

The work decomposes into five layers:

| Layer | Location | Status |
|-------|----------|--------|
| A. Host TCP ABI (`net_open`, `net_accept`) | `mohabbat/washmhost/env_net.go` | **DONE** |
| B. Guest overlay wiring (syscall stubs — ABI) | `rusticated-go/syscall/net_rusticated.go` | **DONE** |
| C. Host DNS ABI (`net_lookup`) | `mohabbat/washmhost/env_net.go` | **DONE** |
| D. Host cert ABI (`net_cert_verify`) | `mohabbat/washmhost/env_net.go` | **DONE** |
| E. Demo mini-projects (`curl`, `server`) | `demo-go/` | **DONE** |

**NO shortcuts are permitted at any stage.** No `InsecureSkipVerify`, no hardcoded bypass
paths, no certificate pinning. The goal is a WASM guest that connects via TCP, resolves DNS
through the host, and performs HTTPS with full chain verification using the host's own root
of trust.

---

## EXPLICIT MANDATE

- DO run the checklist for the demos as a debugging and diagnostic tool. It is `go run . <project> -r` that is the MAIN tool.
- DO run the demos in a native Go environment for resolving any doubts too.
- DO NOT attempt to run any other way by creating some shadow executables etc. ONLY run the demos via `go run . demo-go/<name> <args>` or similar.


## Part 1 — Host TCP ABI (DONE)

### 1.1  Implemented calls

File: `mohabbat/washmhost/env_net.go`
Registration: `mohabbat/washmhost/env_impl.go`

Both calls use the standard Overlapped async convention: the host posts to `fileOpsQueue`
before returning; the guest blocks on `awaitOverlapped`.

#### `net_open`

```
net_open(ovPtr i32, addrPtr i32, addrLen i32, port i32, flags i32)
```

| Parameter | Description |
|-----------|-------------|
| `ovPtr`   | Overlapped completion pointer |
| `addrPtr` | UTF-8 host/IP string in WASM memory |
| `addrLen` | Byte length of address string |
| `port`    | TCP port number (u16) |
| `flags`   | Bit 0 = 1 → connect (client); 0 → listen (server) |

On success `writeOverlapped` is called with `errno=0` and `resultExt=handle` (a `uint64`
key into `HostEnv.handles`). The handle is a `net.Conn` (connect mode) or `net.Listener`
(listen mode).

On error `writeOverlapped` is called with an appropriate POSIX errno.

#### `net_accept`

```
net_accept(ovPtr i32, listenHandle i64)
```

Calls `ln.Accept()` asynchronously. On success `resultExt=connHandle` (a `net.Conn`
handle). Fails with `EBADF` if `listenHandle` is not a listener.

#### Shared I/O calls (reused for network handles)

Network connection handles are stored in the same `HostEnv.handles` map as file handles
and are `io.ReadWriteCloser`-compatible. The following generic calls work on them without
any additional host code:

```
read(ovPtr i32, handle i64, bufPtr i32, bufLen i32)
write(ovPtr i32, handle i64, bufPtr i32, bufLen i32)
handle_close(handle i64)
```

### 1.2  Test coverage

`mohabbat/washmhost/env_net_test.go` contains tests for `sys_net_open` in both listen and
connect modes, and for `sys_net_accept`. These tests pass against the real host network
stack.

---

## Part 2 — Guest Overlay Wiring (DONE)

### 2.1  How the overlay mechanism works

`mohabbat/prebuild.go`:`generateGoOverlay` builds `target/overlay.json`, which remaps Go
SDK source files to Rusticated replacements before the compiler sees them. The mapping for
the network syscall layer is:

```
src/syscall/net_wasip1.go               → rusticated-go/syscall/net_rusticated.go
src/internal/syscall/unix/net_wasip1.go → rusticated-go/internal/syscall/unix/net_rusticated.go
```

`generateGoOverlay` also uses `regast` (`patchGoStr`) to rewrite SDK source files in place
for cases where surgical AST-aware substitution is needed (e.g. `os_wasm.go` signal
wiring, `path/filepath/path.go` separator constants). The same mechanism is available for
`net_fake.go` if needed.

### 2.2  Current state of the guest stubs

`rusticated-go/syscall/net_rusticated.go` currently contains **only** stubs:

```go
//go:build wasip1
package syscall

func Socket(proto, sotype, unused int) (fd int, err error) { return 0, ENOSYS }
func Bind(fd int, sa Sockaddr) error                        { return ENOSYS }
func StopIO(fd int) error                                   { return ENOSYS }
func Listen(fd int, backlog int) error                      { return ENOSYS }
func Accept(fd int) (int, Sockaddr, error)                  { return -1, nil, ENOSYS }
func Connect(fd int, sa Sockaddr) error                     { return ENOSYS }
// Recvfrom, Sendto, Recvmsg, SendmsgN, Getsockopt, Setsockopt,
// SetReadDeadline, SetWriteDeadline, Shutdown â€” all ENOSYS
```

`rusticated-go/internal/syscall/unix/net_rusticated.go` is identical: all ENOSYS.

Because these all return `ENOSYS`, Go's `net` package **cannot establish any connection**.
The entire `net.Dial` / `net.Listen` / `http.Get` stack fails at the first syscall.

### 2.3  What the stubs must do

The Go `net` package reaches the syscall layer through `net/fd_unix.go` â†’
`internal/poll.FD` â†’ `syscall.*`. The bridge must be implemented at the syscall level.

The correct approach is a **virtual file descriptor table** inside
`rusticated-go/syscall/net_rusticated.go`:

1. Add `//go:wasmimport env net_open` and `//go:wasmimport env net_accept` declarations.
2. `syscall.Socket` allocates a slot in an internal `[]netSlot` table and returns a
   virtual fd (slot index + a reserved base offset so it never collides with real fds).
3. `syscall.Connect(fd, sa)` calls host `net_open` with the address from `sa`, flags=1
   (connect), and stores the returned handle in the slot.
4. `syscall.Bind(fd, sa)` + `syscall.Listen(fd, backlog)` call host `net_open` with the
   address from `sa`, flags=0 (listen), and stores the listener handle.
5. `syscall.Accept(fd)` calls host `net_accept` with the listener handle, gets a new
   connection handle.
6. `syscall.Read`/`Write` on a virtual fd call host `read`/`write` with the stored handle.
7. `syscall.SetReadDeadline`/`SetWriteDeadline` and `syscall.Shutdown` are genuine no-ops
   for now (deadline management not yet exposed by the host).

An alternative is a `regast` patch on `src/net/net_fake.go` (or `net/net_wasip1.go`) to
redirect `socket()` at the higher `net` layer instead. This is viable but more
SDK-version sensitive than the syscall approach.

## Part 3 — Host DNS ABI (`net_lookup`) (DONE)

### 3.1  The problem

The Go guest cannot read `/etc/resolv.conf` — it does not exist in the WASM sandbox.
The pure-Go DNS resolver tries to dial UDP 53, but the host ABI only exposes TCP.

### 3.2  Solution: delegate lookup to the host

Add a new ABI call that performs name resolution on the host and returns packed IP
addresses to the guest.

#### Wire signature

```
net_lookup(ovPtr i32, namePtr i32, nameLen i32, bufPtr i32, bufLen i32)
```

| Parameter | Description |
|-----------|-------------|
| `ovPtr`   | Overlapped completion pointer |
| `namePtr` | UTF-8 hostname in WASM memory |
| `nameLen` | Byte length of hostname |
| `bufPtr`  | Destination buffer |
| `bufLen`  | Size of destination buffer |

#### Wire format (response buffer)

All integers little-endian unsigned.

```
Offset    Size    Field
------    ----    -----
0         4       total_size  â€” size of complete payload
4         4       count       â€” number of IP addresses returned
8         1*N     af[]        â€” address family per entry: 4 = IPv4, 6 = IPv6
8+N       ...     addr_data   â€” 4 bytes per IPv4, 16 bytes per IPv6, packed in order
```

If `bufLen < total_size` the host writes what fits; the guest must reallocate and retry.

#### Host implementation (Go, `env_net.go`)

```go
func (h *HostEnv) sys_net_lookup(ctx context.Context, m api.Module, stack []uint64) {
    ovPtr := uint32(stack[0])
    namePtr := uint32(stack[1])
    nameLen := uint32(stack[2])
    // implementation uses net.LookupHost(name) and packs response into wire format
}
```

### 3.3  Guest-side DNS override (TODO)

Overridden `net.DefaultResolver` in `rusticated-go/net/net_rusticated.go` to use the host lookup.

---

## Part 4 — Host Cert ABI (`net_cert_verify`) (DONE)

### 4.1  Purpose

Instead of copying the entire host certificate store into the WASM guest, this API delegates the cryptographic validation of a specific server's certificate chain to the host on demand. This aligns with how modern operating systems optimize TLS verification and ensures compatibility with non-exportable enterprise/hardware roots.

### 4.2  Wire signature

```
net_cert_verify(ovPtr i32, namePtr i32, nameLen i32, chainPtr i32, chainLen i32)
```

| Parameter | Type | Description |
| --- | --- | --- |
| `ovPtr` | `i32` | Overlapped completion pointer. |
| `namePtr` | `i32` | Address of target server's UTF-8 DNS name. |
| `nameLen` | `i32` | Byte length of name string. |
| `chainPtr` | `i32` | Address of packed peer certificate payload. |
| `chainLen` | `i32` | Total byte length of chain payload. |

#### Payload Wire Format (`chainPtr`)

```
Offset          Size    Field
------          ----    -----
0               4       count        — Number of certificates (leaf + intermediates)
4               4*N     lengths[]    — Array of DER byte lengths
4 + 4*count     ...     der_data     — Raw binary DER bytes
```

### 4.3  Host Implementation (Go, `env_net.go`)

Uses Go's native `(*x509.Certificate).Verify()` method. By passing `nil` to `VerifyOptions.Roots`, the host automatically uses its native OS Platform Verifier.

### 4.4 Completion Behavior

The host invokes `writeOverlapped`:
* **Success:** `errno = 0`, `resultExt = 1`
* **Failure:** `errno = 0`, `resultExt = 0`

### 4.5  Guest-side Architecture (TODO)

The guest intercepts verification via a custom `VerifyPeerCertificate` hook in `crypto/tls` or `crypto/x509`. When the guest receives peer certificates, it calls `net_cert_verify` and resumes the handshake once the host signals completion.

---

## Part 5 — Demo Mini-projects (DONE)

Both demos live under `demo-go/` and are invoked through the standard
`go run . demo-go/<name> <args>` mechanism.

### 5.1  `demo-go/curl`

A command-line HTTP(S) client.

**Invocation:** `go run . demo-go/curl <url>`

**Constraints:**
- Uses `http.Get` with no custom `Transport`.
- No `-k` / `InsecureSkipVerify` flag under any circumstances.
- Certificates are verified via the host `net_cert_verify` ABI.
- Streams response body to stdout; exits non-zero on error.

### 5.2  `demo-go/server`

A minimal static-file HTTP server.

**Invocation:** `go run . demo-go/server <root-directory> [<addr>]`

Default address: `127.0.0.1:8080`. Uses `http.FileServer`. No TLS on the server side.

**Validation**: while running, `http://127.0.0.1:8080/<file>` returns the file verbatim.

---

## Part 6 â€” Validation Checklist

All items must pass **without** `InsecureSkipVerify`, `-k`, or any CA bypass:

- [ ] `go run . demo-go/curl http://example.com` â€” plain HTTP response printed.
- [ ] `go run . demo-go/curl https://example.com` â€” HTTPS with full chain verification.
- [ ] `go run . demo-go/curl https://google.com` â€” different CA chain.
- [ ] `go run . demo-go/server <dir>` + host `curl http://127.0.0.1:8080/` â€” file served.
- [ ] `go run . demo-go/server <dir>` + `go run . demo-go/curl http://127.0.0.1:8080/` â€” WASM-to-WASM loopback.

---

## Part 7 â€” Implementation Order

Items marked âœ“ are complete. Listed in dependency order.

1. âœ“ **Host TCP ABI** â€” `sys_net_open` and `sys_net_accept` in `env_net.go`; exports
   `net_open` and `net_accept` registered in `env_impl.go`. Tests pass.

2. **Guest TCP bridge** â€” Implement the virtual-fd table in
   `rusticated-go/syscall/net_rusticated.go` as specified in Â§2.3. Replace all ENOSYS
   stubs with real calls to `net_open`, `net_accept`, `read`, `write`, `handle_close`.
   *Gate: `net.Dial("tcp", "8.8.8.8:80")` and `net.Listen("tcp", ":0")` + `Accept()`
   both succeed from within a guest.*

3. **Host DNS ABI** â€” Add `sys_net_lookup` to `env_net.go`; register as `net_lookup`
   in `env_impl.go`; add Node.js handler. Write a unit test in `env_net_test.go` that
   resolves `"example.com"` and asserts at least one returned address.
   *Gate: unit test passes.*

4. **Guest DNS override** â€” Override `net.DefaultResolver` (or patch the relevant SDK
   source via overlay) to use `hostNetLookup` as specified in Â§3.3.
   *Gate: `net.LookupHost("example.com")` returns a non-empty result from within a guest.*

5. ✓ **Host cert ABI** — Implement `net_cert_verify` for delegated verification.
   *Gate: `https://example.com` succeeds via host-side pure-Go verifier.*

6. ✓ **Guest cert injection** — Patch `crypto/x509` to use `net_cert_verify`.
   *Gate: Guest TLS handshake completes without local roots.*

7. **`demo-go/curl`** â€” Implement per Â§5.1. No TLS workarounds.
   *Gate: `https://example.com` prints response with no errors.*

8. **`demo-go/server`** â€” Implement per Â§5.2.
   *Gate: full loopback test in Â§6 passes.*

9. **End-to-end validation** â€” Run the full Â§6 checklist. No item may be waived.

---

## Absolute Prohibitions

- `InsecureSkipVerify: true` anywhere in guest or host code.
- Bundled CA PEM files committed to the repository.
- Hardcoded certificate fingerprints or pins.
- Any HTTP fallback that silently downgrades a failing HTTPS request.
- A -k or --insecure flag in either demo program.
- Using terminal commands to read or write files (use built-in tools only).

## EXPLICIT MANDATE: REPEAT

- DO run the checklist for the demos as a debugging and diagnostic tool. It is `go run . <project> -r` that is the MAIN tool.
- DO run the demos in a native Go environment for resolving any doubts too.
- DO NOT attempt to run any other way by creating some shadow executables etc. ONLY run the demos via `go run . demo-go/<name> <args>` or similar.
- DO reaffirm the commitment to the mandate REGULARLY