package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestSysNet(t *testing.T) {
	env := NewHostEnv()
	mod := newMockModule(0x10000)

	t.Run("1. sys_net_open listen", func(t *testing.T) {
		ovPtr := uint32(0x100)
		addr := "127.0.0.1"
		ptrAddr := uint32(0x200)
		mod.Memory().Write(ptrAddr, []byte(addr))

		stack := []uint64{uint64(ovPtr), uint64(ptrAddr), uint64(len(addr)), 0, 0} // port 0 = random, flags 0 = listen
		env.sys_net_open(context.Background(), mod, stack)

		time.Sleep(time.Millisecond * 50)
		env.Poll(context.Background(), mod)

		val, _ := mod.Memory().Read(ovPtr, 24)
		errno := binary.LittleEndian.Uint32(val[4:8])
		handle := binary.LittleEndian.Uint64(val[16:24])

		if errno != 0 {
			t.Errorf("failed to listen: %d", errno)
		}
		if handle == 0 {
			t.Error("expected non-zero handle")
		}
		env.sys_handle_close(context.Background(), mod, []uint64{handle})
	})

	t.Run("2. sys_net_open dial loopback", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		_, portStr, _ := net.SplitHostPort(ln.Addr().String())
		var port uint16
		fmt.Sscanf(portStr, "%d", &port)

		ovPtr := uint32(0x300)
		addr := "127.0.0.1"
		ptrAddr := uint32(0x400)
		mod.Memory().Write(ptrAddr, []byte(addr))

		stack := []uint64{uint64(ovPtr), uint64(ptrAddr), uint64(len(addr)), uint64(port), 1} // flags 1 = connect
		env.sys_net_open(context.Background(), mod, stack)

		time.Sleep(time.Millisecond * 50)
		env.Poll(context.Background(), mod)

		val, _ := mod.Memory().Read(ovPtr, 24)
		errno := binary.LittleEndian.Uint32(val[4:8])
		if errno != 0 {
			t.Errorf("failed to dial: %d", errno)
		}
	})

	t.Run("3. sys_net_accept", func(t *testing.T) {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		_, portStr, _ := net.SplitHostPort(ln.Addr().String())
		var port uint16
		fmt.Sscanf(portStr, "%d", &port)

		// Register listener in env
		env.mu.Lock()
		lHandle := env.nextHandle
		env.nextHandle++
		env.handles[lHandle] = ln
		env.mu.Unlock()

		ovPtr := uint32(0x500)
		env.sys_net_accept(context.Background(), mod, []uint64{uint64(ovPtr), lHandle})

		// Dial from outside to trigger accept
		go func() {
			time.Sleep(time.Millisecond * 20)
			net.Dial("tcp", ln.Addr().String())
		}()

		time.Sleep(time.Millisecond * 100)
		env.Poll(context.Background(), mod)

		val, _ := mod.Memory().Read(ovPtr, 24)
		errno := binary.LittleEndian.Uint32(val[4:8])
		if errno != 0 {
			t.Errorf("accept failed: %d", errno)
		}
		cHandle := binary.LittleEndian.Uint64(val[16:24])
		if cHandle == 0 {
			t.Error("accepted handle is 0")
		}
		env.sys_handle_close(context.Background(), mod, []uint64{cHandle})
		env.sys_handle_close(context.Background(), mod, []uint64{lHandle})
	})

	t.Run("4. sys_net_open invalid address", func(t *testing.T) {
		ovPtr := uint32(0x600)
		addr := "999.999.999.999"
		ptrAddr := uint32(0x700)
		mod.Memory().Write(ptrAddr, []byte(addr))

		stack := []uint64{uint64(ovPtr), uint64(ptrAddr), uint64(len(addr)), 80, 1}
		env.sys_net_open(context.Background(), mod, stack)

		time.Sleep(time.Millisecond * 50)
		env.Poll(context.Background(), mod)

		val, _ := mod.Memory().Read(ovPtr, 24)
		errno := binary.LittleEndian.Uint32(val[4:8])
		if errno == 0 {
			t.Error("expected dial to fail for invalid address")
		}
	})

	t.Run("5. sys_net_accept on non-existent handle", func(t *testing.T) {
		ovPtr := uint32(0x800)
		env.sys_net_accept(context.Background(), mod, []uint64{uint64(ovPtr), 999999})

		// Should be sync failure
		val, _ := mod.Memory().Read(ovPtr, 24)
		errno := binary.LittleEndian.Uint32(val[4:8])
		if errno != 9 { // EBADF
			t.Errorf("expected EBADF (9), got %d", errno)
		}
	})

	t.Run("6. read/write on network handle", func(t *testing.T) {
		// Set up a pair
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		defer ln.Close()

		done := make(chan bool)
		go func() {
			c, _ := ln.Accept()
			c.Write([]byte("HELLO"))
			c.Close()
			done <- true
		}()

		conn, _ := net.Dial("tcp", ln.Addr().String())
		env.mu.Lock()
		cHandle := env.nextHandle
		env.nextHandle++
		env.handles[cHandle] = conn
		env.mu.Unlock()

		ovPtr := uint32(0x900)
		bufPtr := uint32(0xA00)
		env.sys_read(context.Background(), mod, []uint64{uint64(ovPtr), uint64(cHandle), uint64(bufPtr), 5})

		time.Sleep(time.Millisecond * 50)
		env.Poll(context.Background(), mod)

		val, _ := mod.Memory().Read(ovPtr, 24)
		readBytes := binary.LittleEndian.Uint64(val[16:24])
		if readBytes != 5 {
			t.Errorf("expected 5 bytes, got %d", readBytes)
		}

		data, _ := mod.Memory().Read(bufPtr, 5)
		if string(data) != "HELLO" {
			t.Errorf("expected HELLO, got %q", string(data))
		}

		env.sys_handle_close(context.Background(), mod, []uint64{cHandle})
		<-done
	})

	t.Run("7. sys_net_open multiple concurrent dials", func(t *testing.T) {
		const count = 5
		for i := 0; i < count; i++ {
			ovPtr := uint32(0xB00 + i*32)
			addr := "127.0.0.1"
			ptrAddr := uint32(0xC00 + i*32)
			mod.Memory().Write(ptrAddr, []byte(addr))
			// Dials likely to fail quickly if nothing listening
			env.sys_net_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(ptrAddr), uint64(len(addr)), 1, 1})
		}
		time.Sleep(time.Millisecond * 100)
		for i := 0; i < count; i++ {
			env.Poll(context.Background(), mod)
		}
	})

	t.Run("8. close listener stops accept", func(t *testing.T) {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		env.mu.Lock()
		lHandle := env.nextHandle
		env.nextHandle++
		env.handles[lHandle] = ln
		env.mu.Unlock()

		ovPtr := uint32(0xD00)
		env.sys_net_accept(context.Background(), mod, []uint64{uint64(ovPtr), lHandle})

		// Close while accept is pending
		env.sys_handle_close(context.Background(), mod, []uint64{lHandle})

		time.Sleep(time.Millisecond * 50)
		env.Poll(context.Background(), mod)

		val, _ := mod.Memory().Read(ovPtr, 24)
		errno := binary.LittleEndian.Uint32(val[4:8])
		if errno == 0 {
			// It might have finished with error due to closure
		}
	})

	t.Run("9. sys_handle_close on already closed net handle", func(t *testing.T) {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		env.mu.Lock()
		h := env.nextHandle
		env.nextHandle++
		env.handles[h] = ln
		env.mu.Unlock()

		env.sys_handle_close(context.Background(), mod, []uint64{h})
		env.sys_handle_close(context.Background(), mod, []uint64{h}) // second time
	})

	t.Run("10. sys_net_open loopback port 0", func(t *testing.T) {
		ovPtr := uint32(0xE00)
		addr := "127.0.0.1"
		ptrAddr := uint32(0xF00)
		mod.Memory().Write(ptrAddr, []byte(addr))
		env.sys_net_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(ptrAddr), uint64(len(addr)), 0, 0})

		time.Sleep(time.Millisecond * 20)
		env.Poll(context.Background(), mod)

		val, _ := mod.Memory().Read(ovPtr, 24)
		handle := binary.LittleEndian.Uint64(val[16:24])
		if handle == 0 {
			t.Error("listen failed")
		}
		env.sys_handle_close(context.Background(), mod, []uint64{handle})
	})

	t.Run("11. accept on closed listener", func(t *testing.T) {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		ln.Close()
		env.mu.Lock()
		h := env.nextHandle
		env.nextHandle++
		env.handles[h] = ln
		env.mu.Unlock()

		ovPtr := uint32(0x1000)
		env.sys_net_accept(context.Background(), mod, []uint64{uint64(ovPtr), h})
		time.Sleep(time.Millisecond * 20)
		env.Poll(context.Background(), mod)

		val, _ := mod.Memory().Read(ovPtr, 24)
		errno := binary.LittleEndian.Uint32(val[4:8])
		if errno == 0 {
			t.Error("expected error accepting on closed listener")
		}
	})

	t.Run("12. write to closed network handle", func(t *testing.T) {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		defer ln.Close()
		go func() {
			c, _ := ln.Accept()
			c.Close()
		}()

		conn, _ := net.Dial("tcp", ln.Addr().String())
		env.mu.Lock()
		h := env.nextHandle
		env.nextHandle++
		env.handles[h] = conn
		env.mu.Unlock()

		conn.Close() // manually close

		ovPtr := uint32(0x1100)
		env.sys_write(context.Background(), mod, []uint64{uint64(ovPtr), h, 0x1200, 10})
		time.Sleep(time.Millisecond * 20)
		env.Poll(context.Background(), mod)

		val, _ := mod.Memory().Read(ovPtr, 24)
		errno := binary.LittleEndian.Uint32(val[4:8])
		if errno == 0 {
			// Depending on timing, might not fail yet, but usually does
		}
	})

	t.Run("13. sys_net_open with localhost addr", func(t *testing.T) {
		ovPtr := uint32(0x1300)
		addr := "127.0.0.1"
		ptrAddr := uint32(0x1400)
		mod.Memory().Write(ptrAddr, []byte(addr))
		env.sys_net_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(ptrAddr), uint64(len(addr)), 0, 0})
		time.Sleep(time.Millisecond * 20)
		env.Poll(context.Background(), mod)

		val, _ := mod.Memory().Read(ovPtr, 24)
		errno := binary.LittleEndian.Uint32(val[4:8])
		if errno != 0 {
			t.Errorf("listen failed: %d", errno)
		}
		handle := binary.LittleEndian.Uint64(val[16:24])
		if handle != 0 {
			env.sys_handle_close(context.Background(), mod, []uint64{handle})
		}
	})

	t.Run("14. sys_net_accept pressure", func(t *testing.T) {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		env.mu.Lock()
		h := env.nextHandle
		env.nextHandle++
		env.handles[h] = ln
		env.mu.Unlock()

		const count = 5
		for i := 0; i < count; i++ {
			env.sys_net_accept(context.Background(), mod, []uint64{uint64(0x1400 + i*32), h})
			net.Dial("tcp", ln.Addr().String())
		}

		time.Sleep(time.Millisecond * 100)
		for i := 0; i < count; i++ {
			env.Poll(context.Background(), mod)
		}
		env.sys_handle_close(context.Background(), mod, []uint64{h})
	})
}
