//go:build wasip1

package syscall

import (
	"encoding/binary"
	"internal/strconv"
	"runtime"
	"unsafe"
)

// ── Host ABI imports ───────────────────────────────────────────────────────

//go:wasmimport env net_open
//go:noescape
func rusticated_net_open(overlapped unsafe.Pointer, addrPtr *byte, addrLen uint32, port uint32, flags uint32)

//go:wasmimport env net_accept
//go:noescape
func rusticated_net_accept(overlapped unsafe.Pointer, handle uint64)

//go:wasmimport env net_lookup
//go:noescape
func rusticated_net_lookup(overlapped unsafe.Pointer, namePtr *byte, nameLen uint32, bufPtr *byte, bufLen uint32)

// ── Socket-state tracking ──────────────────────────────────────────────────
// netPendingHandle is a sentinel stored in fdMap while a socket is allocated
// but not yet connected or bound+listened. The host's handle-close for an
// unknown key is a no-op, so cleanup of pre-connected sockets is safe.
const netPendingHandle = uint64(0xDEADC0DEDEADC0DE)

// netSocketBind records the address bound via Bind() before Listen() is called.
// WASM is single-threaded so this map needs no mutex.
var netSocketBind = map[int32]struct {
	addr string
	port uint16
}{}

// ── Helper: extract host:port from a Sockaddr ─────────────────────────────

func netSockAddrToHostPort(sa Sockaddr) (addr string, port uint16, err error) {
	switch v := sa.(type) {
	case SockaddrInet4:
		ip := v.Addr
		if ip[0] == 0 && ip[1] == 0 && ip[2] == 0 && ip[3] == 0 {
			addr = ""
		} else {
			addr = strconv.Itoa(int(ip[0])) + "." +
				strconv.Itoa(int(ip[1])) + "." +
				strconv.Itoa(int(ip[2])) + "." +
				strconv.Itoa(int(ip[3]))
		}
		port = uint16(v.Port)
	case *SockaddrInet4:
		if v == nil {
			return "", 0, EINVAL
		}
		addr, port, err = netSockAddrToHostPort(*v)
	case SockaddrInet6:
		allZero := true
		for _, b := range v.Addr {
			if b != 0 {
				allZero = false
				break
			}
		}
		if !allZero {
			addr = netFmtIPv6(v.Addr)
		}
		port = uint16(v.Port)
	case *SockaddrInet6:
		if v == nil {
			return "", 0, EINVAL
		}
		addr, port, err = netSockAddrToHostPort(*v)
	default:
		err = EAFNOSUPPORT
	}
	return
}

func netFmtIPv6(addr [16]byte) string {
	var buf [39]byte
	j := 0
	for i := 0; i < 16; i += 2 {
		if i > 0 {
			buf[j] = ':'
			j++
		}
		h := (int(addr[i]) << 8) | int(addr[i+1])
		const hex = "0123456789abcdef"
		buf[j] = hex[(h>>12)&0xf]; j++
		buf[j] = hex[(h>>8)&0xf]; j++
		buf[j] = hex[(h>>4)&0xf]; j++
		buf[j] = hex[h&0xf]; j++
	}
	return string(buf[:j])
}

// ── callNetOpen: shared helper ─────────────────────────────────────────────

func callNetOpen(addr string, port uint16, flags uint32) (uint64, Errno) {
	var dummy byte
	var ctx overlappedContext
	if len(addr) > 0 {
		b := []byte(addr)
		rusticated_net_open(unsafe.Pointer(&ctx.o), &b[0], uint32(len(b)), uint32(port), flags)
		runtime.KeepAlive(b)
	} else {
		rusticated_net_open(unsafe.Pointer(&ctx.o), &dummy, 0, uint32(port), flags)
	}
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return 0, Errno(ctx.o.hostError)
	}
	return ctx.o.resultExt, 0
}

// ── Socket syscall bridge ──────────────────────────────────────────────────

func Socket(proto, sotype, unused int) (fd int, err error) {
	if sotype != SOCK_STREAM {
		return 0, ENOSYS
	}
	ifd, errno := allocFD(netPendingHandle, "socket:pending")
	if errno != 0 {
		return 0, errnoErr(errno)
	}
	return int(ifd), nil
}

func Bind(fd int, sa Sockaddr) error {
	addr, port, err := netSockAddrToHostPort(sa)
	if err != nil {
		return err
	}
	netSocketBind[int32(fd)] = struct {
		addr string
		port uint16
	}{addr, port}
	return nil
}

func StopIO(fd int) error { return nil }

func Listen(fd int, backlog int) error {
	bind, ok := netSocketBind[int32(fd)]
	if !ok {
		// No prior Bind — listen on all interfaces, port 0.
		bind.addr = ""
		bind.port = 0
	}
	handle, errno := callNetOpen(bind.addr, bind.port, 0 /* listen */)
	if errno != 0 {
		return errnoErr(errno)
	}
	fdMap[int32(fd)] = fdEntry{handle: handle, path: "socket:listening"}
	delete(netSocketBind, int32(fd))
	return nil
}

func Accept(fd int) (int, Sockaddr, error) {
	handle, errno := fdToHandle(int32(fd))
	if errno != 0 {
		return -1, nil, errnoErr(errno)
	}

	var ctx overlappedContext
	rusticated_net_accept(unsafe.Pointer(&ctx.o), handle)
	awaitOverlapped(&ctx)
	if ctx.o.hostError != 0 {
		return -1, nil, errnoErr(Errno(ctx.o.hostError))
	}
	newHandle := ctx.o.resultExt

	newFd, errFD := allocFD(newHandle, "socket:accepted")
	if errFD != 0 {
		rusticated_handle_close(newHandle)
		return -1, nil, errnoErr(errFD)
	}
	return int(newFd), SockaddrInet4{}, nil
}

func Connect(fd int, sa Sockaddr) error {
	addr, port, err := netSockAddrToHostPort(sa)
	if err != nil {
		return err
	}
	if addr == "" {
		return errnoErr(EADDRNOTAVAIL)
	}
	handle, errno := callNetOpen(addr, port, 1 /* connect */)
	if errno != 0 {
		return errnoErr(errno)
	}
	fdMap[int32(fd)] = fdEntry{handle: handle, path: "socket:connected"}
	delete(netSocketBind, int32(fd))
	return nil
}

func Recvfrom(fd int, p []byte, flags int) (n int, from Sockaddr, err error) {
	return 0, nil, ENOSYS
}

func Sendto(fd int, p []byte, flags int, to Sockaddr) error { return ENOSYS }

func Recvmsg(fd int, p, oob []byte, flags int) (n, oobn, recvflags int, from Sockaddr, err error) {
	return 0, 0, 0, nil, ENOSYS
}

func SendmsgN(fd int, p, oob []byte, to Sockaddr, flags int) (n int, err error) { return 0, ENOSYS }

func GetsockoptInt(fd, level, opt int) (value int, err error) { return 0, ENOSYS }

func SetsockoptInt(fd int, level, opt, value int) error { return ENOSYS }

func SetReadDeadline(fd int, t int64) error  { return nil }

func SetWriteDeadline(fd int, t int64) error { return nil }

func Shutdown(fd int, how int) error { return nil }

// ── Net certs ABI ─────────────────────────────────────────────────────────

//go:wasmimport env net_cert_verify
//go:noescape
func rusticated_net_cert_verify(overlapped unsafe.Pointer, namePtr *byte, nameLen uint32, chainPtr *byte, chainLen uint32)

// NetCertVerify delegates TLS certificate verification to the host.
// name is the server DNS name (for SAN/SNI check).
// chain is the leaf + intermediate DER certificates.
// Returns (1, nil) if trusted, (0, nil) if untrusted, or (0, err) on error.
func NetCertVerify(name string, chain [][]byte) (int, error) {
	var ctx overlappedContext
	
	count := len(chain)
	if count == 0 {
		return 0, EINVAL
	}
	
	dersTotal := 0
	for _, d := range chain {
		dersTotal += len(d)
	}
	
	payloadLen := 4 + 4*count + dersTotal
	payload := make([]byte, payloadLen)
	
	binary.LittleEndian.PutUint32(payload[0:4], uint32(count))
	off := 4 + 4*count
	for i, d := range chain {
		binary.LittleEndian.PutUint32(payload[4+4*i : 8+4*i], uint32(len(d)))
		copy(payload[off:], d)
		off += len(d)
	}

	rusticated_net_cert_verify(
		unsafe.Pointer(&ctx.o),
		(*byte)(unsafe.StringData(name)), uint32(len(name)),
		&payload[0], uint32(len(payload)),
	)
	runtime.KeepAlive(name)
	runtime.KeepAlive(payload)
	debugPrintln("GUEST: net_cert_verify BEFORE await", name)
	awaitOverlapped(&ctx)
	debugPrintln("GUEST: net_cert_verify AFTER await", name, ctx.o.flags, ctx.o.hostError, ctx.o.resultExt)
	
	if ctx.o.hostError != 0 {
		return 0, errnoErr(Errno(ctx.o.hostError))
	}
	return int(ctx.o.resultExt), nil
}

// NetLookup resolves a hostname on the host.
// It returns a slice of IP addresses (4 or 16 bytes each).
func NetLookup(name string) ([]IP, error) {
	var ctx overlappedContext
	
	// We don't know the size upfront, but 4KB is plenty for DNS.
	// The host returns the actual required size in resultExt.
	buf := make([]byte, 4096)
	
	rusticated_net_lookup(
		unsafe.Pointer(&ctx.o),
		(*byte)(unsafe.StringData(name)), uint32(len(name)),
		&buf[0], uint32(len(buf)),
	)
	runtime.KeepAlive(name)
	runtime.KeepAlive(buf)
	awaitOverlapped(&ctx)
	
	if ctx.o.hostError != 0 {
		return nil, errnoErr(Errno(ctx.o.hostError))
	}
	
	totalSize := uint32(ctx.o.resultExt)
	if totalSize < 8 {
		return nil, EINVAL
	}
	
	// Wire format: [totalSize u32][count u32][families u8*N][data...]
	count := binary.LittleEndian.Uint32(buf[4:8])
	if count == 0 {
		return nil, nil
	}
	
	families := buf[8 : 8+count]
	off := 8 + int(count)
	
	var res []IP
	for _, f := range families {
		var ip IP
		if f == 4 {
			if off+4 > int(totalSize) { return nil, EINVAL }
			ip = make(IP, 4)
			copy(ip, buf[off:off+4])
			off += 4
		} else if f == 6 {
			if off+16 > int(totalSize) { return nil, EINVAL }
			ip = make(IP, 16)
			copy(ip, buf[off:off+16])
			off += 16
		} else {
			return nil, EAFNOSUPPORT
		}
		res = append(res, ip)
	}
	
	return res, nil
}

type IP []byte
