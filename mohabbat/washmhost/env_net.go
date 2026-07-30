package main

import (
	"context"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/tetratelabs/wazero/api"
)

// ── net_open ──────────────────────────────────────────────────────────────────
// Signature: (ovPtr i32, addrPtr i32, addrLen i32, port i32, flags i32)
// flags bit 0 = connect (1) vs listen (0).
// On success writeOverlapped is called with errno=0 and resultExt=handle.
func (h *HostEnv) sys_net_open(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	addrPtr := uint32(stack[1])
	addrLen := uint32(stack[2])
	port := uint16(stack[3])
	flags := uint32(stack[4])

	mem := m.Memory()
	buf, ok := mem.Read(addrPtr, addrLen)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	addr := string(buf)
	isConnect := (flags & 1) != 0

	state := h.RegisterOp(ovPtr, nil)
	go func() {
		var handle uint64
		var err error
		if isConnect {
			var conn net.Conn
			conn, err = net.Dial("tcp", fmt.Sprintf("%s:%d", addr, port))
			if err == nil {
				h.mu.Lock()
				handle = h.nextHandle
				h.nextHandle++
				h.handles[handle] = conn
				h.mu.Unlock()
			}
		} else {
			var ln net.Listener
			ln, err = net.Listen("tcp", fmt.Sprintf("%s:%d", addr, port))
			if err == nil {
				h.mu.Lock()
				handle = h.nextHandle
				h.nextHandle++
				h.handles[handle] = ln
				h.mu.Unlock()
			}
		}

		retCode := uint32(0)
		if err != nil {
			retCode = mapErrno(err)
		}
		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				if err == nil {
					h.mu.Lock()
					hAny := h.handles[handle]
					delete(h.handles, handle)
					h.mu.Unlock()
					if c, ok := hAny.(io.Closer); ok {
						c.Close()
					}
				}
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()
			writeOverlapped(m, ovPtr, retCode, 0, handle)
		}
	}()
}

// ── net_accept ────────────────────────────────────────────────────────────────
// Signature: (ovPtr i32, listenHandle i64)
// On success writeOverlapped is called with errno=0 and resultExt=connHandle.
func (h *HostEnv) sys_net_accept(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	listenHandle := stack[1]

	h.mu.Lock()
	lnAny, ok := h.handles[listenHandle]
	h.mu.Unlock()

	if !ok {
		writeOverlapped(m, ovPtr, wasiEBADF, 0, 0)
		return
	}
	ln, ok := lnAny.(net.Listener)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	state := h.RegisterOp(ovPtr, lnAny)
	go func() {
		conn, err := ln.Accept()
		var handle uint64
		retCode := uint32(0)
		if err != nil {
			retCode = mapErrno(err)
		} else {
			h.mu.Lock()
			handle = h.nextHandle
			h.nextHandle++
			h.handles[handle] = conn
			h.mu.Unlock()
		}
		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				if err == nil {
					conn.Close()
				}
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()
			writeOverlapped(m, ovPtr, retCode, 0, handle)
		}
	}()
}

// ── net_lookup ────────────────────────────────────────────────────────────────
// Signature: (ovPtr i32, namePtr i32, nameLen i32, bufPtr i32, bufLen i32)
//
// Resolves the hostname on the host and writes packed IP addresses to bufPtr.
//
// Wire format:
//
//	[u32 total_size][u32 count][af_array: 1 byte each (4=IPv4, 6=IPv6)][addr_data]
//
// resultExt = total_size (caller may reallocate and retry if bufLen was too small).
func (h *HostEnv) sys_net_lookup(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	namePtr := uint32(stack[1])
	nameLen := uint32(stack[2])
	bufPtr := uint32(stack[3])
	bufLen := uint32(stack[4])

	mem := m.Memory()
	nameBuf, ok := mem.Read(namePtr, nameLen)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	name := string(nameBuf)

	state := h.RegisterOp(ovPtr, nil)
	go func() {
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, name)

		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()

			if err != nil {
				writeOverlapped(m, ovPtr, mapErrno(err), 0, 0)
				return
			}

			type entry struct {
				af   byte
				data []byte
			}
			entries := make([]entry, 0, len(addrs))
			for _, ia := range addrs {
				ip := ia.IP
				if v4 := ip.To4(); v4 != nil {
					entries = append(entries, entry{af: 4, data: v4})
				} else if v6 := ip.To16(); v6 != nil {
					entries = append(entries, entry{af: 6, data: v6})
				}
			}

			N := len(entries)
			dataBytes := 0
			for _, e := range entries {
				dataBytes += len(e.data)
			}
			totalSize := uint32(4 + 4 + N + dataBytes)

			payload := make([]byte, totalSize)
			binary.LittleEndian.PutUint32(payload[0:4], totalSize)
			binary.LittleEndian.PutUint32(payload[4:8], uint32(N))
			off := 8
			for _, e := range entries {
				payload[off] = e.af
				off++
			}
			for _, e := range entries {
				copy(payload[off:], e.data)
				off += len(e.data)
			}

			toWrite := totalSize
			if bufLen < toWrite {
				toWrite = bufLen
			}
			if toWrite > 0 {
				mem.Write(bufPtr, payload[:toWrite])
			}
			writeOverlapped(m, ovPtr, 0, 0, uint64(totalSize))
		}
	}()
}

// ── net_cert_verify ───────────────────────────────────────────────────────────
// Signature: (ovPtr i32, namePtr i32, nameLen i32, chainPtr i32, chainLen i32)
//
// Chain wire format:
//
//	[u32 count][u32[] lengths (count entries)][DER bytes...]
//
// Delegates certificate chain verification to the host OS trust store
// by calling x509.Certificate.Verify with Roots=nil.
//
// Completion:
//
//	success:        errno=0, resultExt=1
//	verify failure: errno=0, resultExt=0
//	format error:   errno=22 (EINVAL), resultExt=0
func (h *HostEnv) sys_net_cert_verify(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	namePtr := uint32(stack[1])
	nameLen := uint32(stack[2])
	chainPtr := uint32(stack[3])
	chainLen := uint32(stack[4])

	mem := m.Memory()
	nameBuf, ok := mem.Read(namePtr, nameLen)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	serverName := string(nameBuf)

	chainBuf, ok := mem.Read(chainPtr, chainLen)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	state := h.RegisterOp(ovPtr, nil)
	go func() {
		h.log("[%s] sys_net_cert_verify(ovPtr=%d, name=%q, chainLen=%d) starting", time.Now().Format("15:04:05.000"), ovPtr, serverName, len(chainBuf))
		errno, resultExt := func() (uint32, uint64) {
			if len(chainBuf) < 4 {
				h.log("[%s]   [!] chainBuf too short: %d", time.Now().Format("15:04:05.000"), len(chainBuf))
				return wasiEINVAL, 0
			}
			count := binary.LittleEndian.Uint32(chainBuf[0:4])
			h.log("[%s]   cert count: %d", time.Now().Format("15:04:05.000"), count)
			if count == 0 {
				return wasiEINVAL, 0
			}
			headerSize := 4 + 4*int(count)
			if len(chainBuf) < headerSize {
				h.log("[%s]   [!] chainBuf header underflow: %d < %d", time.Now().Format("15:04:05.000"), len(chainBuf), headerSize)
				return wasiEINVAL, 0
			}

			certs := make([]*x509.Certificate, 0, count)
			off := headerSize
			for i := 0; i < int(count); i++ {
				dlen := int(binary.LittleEndian.Uint32(chainBuf[4+4*i : 8+4*i]))
				if off+dlen > len(chainBuf) {
					h.log("[%s]   [!] cert %d length overflow: off=%d dlen=%d total=%d", time.Now().Format("15:04:05.000"), i, off, dlen, len(chainBuf))
					return wasiEINVAL, 0
				}
				certData := chainBuf[off : off+dlen]
				snippet := certData
				if len(snippet) > 32 {
					snippet = snippet[:32]
				}
				h.log("[%s]   parsing cert %d: len=%d, snippet=%x...", time.Now().Format("15:04:05.000"), i, dlen, snippet)
				cert, err := x509.ParseCertificate(certData)
				if err != nil {
					h.log("[%s]   [!] failed to parse cert %d: %v", time.Now().Format("15:04:05.000"), i, err)
					return wasiEINVAL, 0
				}
				certs = append(certs, cert)
				off += dlen
			}

			intermediates := x509.NewCertPool()
			for i, c := range certs[1:] {
				h.log("[%s]   adding intermediate %d: Subject=%q", time.Now().Format("15:04:05.000"), i+1, c.Subject)
				intermediates.AddCert(c)
			}
			h.log("[%s]   fetching system cert pool...", time.Now().Format("15:04:05.000"))
			roots, err := x509.SystemCertPool()
			if err != nil {
				h.log("[%s]   [!] system cert pool error: %v (falling back to empty)", time.Now().Format("15:04:05.000"), err)
				roots = x509.NewCertPool()
			} else {
				h.log("[%s]   system cert pool loaded (count hint: %d)", time.Now().Format("15:04:05.000"), len(roots.Subjects()))
			}
			opts := x509.VerifyOptions{
				DNSName:       serverName,
				Intermediates: intermediates,
				Roots:         roots,
			}
			h.log("[%s]   calling leaf.Verify(name=%q)...", time.Now().Format("15:04:05.000"), serverName)
			if _, err := certs[0].Verify(opts); err != nil {
				h.log("[%s]   [-] verification failed: %v", time.Now().Format("15:04:05.000"), err)
				return 0, 0
			}
			h.log("[%s]   [+] verification success", time.Now().Format("15:04:05.000"))
			return 0, 1
		}()

		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				h.log("[%s]   op not active, discarding result", time.Now().Format("15:04:05.000"))
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()
			h.log("[%s] sys_net_cert_verify(ovPtr=%d) completing: errno=%d, resultExt=%d", time.Now().Format("15:04:05.000"), ovPtr, errno, resultExt)
			writeOverlapped(m, ovPtr, errno, 0, resultExt)
		}
	}()
}
