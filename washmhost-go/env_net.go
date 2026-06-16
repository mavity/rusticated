package main

import (
	"context"
	"fmt"
	"io"
	"net"

	"github.com/tetratelabs/wazero/api"
)

func (h *HostEnv) sys_net_open(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	addrPtr := uint32(stack[1])
	addrLen := uint32(stack[2])
	port := uint16(stack[3])
	flags := uint32(stack[4])

	mem := m.Memory()
	buf, ok := mem.Read(addrPtr, addrLen)
	if !ok {
		writeOverlapped(m, ovPtr, 22, 0, 0)
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

func (h *HostEnv) sys_net_accept(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	listenHandle := stack[1]

	h.mu.Lock()
	lnAny, ok := h.handles[listenHandle]
	h.mu.Unlock()

	if !ok {
		writeOverlapped(m, ovPtr, 9, 0, 0) // EBADF
		return
	}
	ln, ok := lnAny.(net.Listener)
	if !ok {
		writeOverlapped(m, ovPtr, 22, 0, 0) // EINVAL
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
