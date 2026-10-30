package main

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestSysNet(t *testing.T) {
	env := NewHostEnv()
	mod := newMockModule(0x10000)

	waitForCompletion := func(t *testing.T, ovPtr uint32) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for {
			header, ok := mod.Memory().Read(ovPtr, 4)
			if !ok {
				t.Fatalf("overlapped pointer %d is out of bounds", ovPtr)
			}
			if binary.LittleEndian.Uint32(header) == 1 {
				result, ok := mod.Memory().Read(ovPtr, 24)
				if !ok {
					t.Fatalf("overlapped result at %d is out of bounds", ovPtr)
				}
				return result
			}
			env.Poll(ctx, mod)
			if ctx.Err() != nil {
				t.Fatalf("timeout waiting for overlapped completion at %d", ovPtr)
			}
		}
	}

	runOperation := func(t *testing.T, ovPtr uint32, operation func()) []byte {
		t.Helper()
		if !mod.Memory().Write(ovPtr, make([]byte, 24)) {
			t.Fatalf("failed to initialize overlapped request at %d", ovPtr)
		}
		operation()
		return waitForCompletion(t, ovPtr)
	}

	t.Run("1. sys_net_open listen", func(t *testing.T) {
		ovPtr := uint32(0x100)
		addr := "127.0.0.1"
		ptrAddr := uint32(0x200)
		mod.Memory().Write(ptrAddr, []byte(addr))

		stack := []uint64{uint64(ovPtr), uint64(ptrAddr), uint64(len(addr)), 0, 0} // port 0 = random, flags 0 = listen
		val := runOperation(t, ovPtr, func() {
			env.sys_net_open(context.Background(), mod, stack)
		})
		errno := binary.LittleEndian.Uint32(val[4:8])
		boundPort := binary.LittleEndian.Uint64(val[8:16])
		handle := binary.LittleEndian.Uint64(val[16:24])

		if errno != wasiSuccess {
			t.Errorf("failed to listen: %d", errno)
		}
		if handle == 0 {
			t.Error("expected non-zero handle")
		} else {
			env.sys_handle_close(context.Background(), mod, []uint64{handle})
		}
		if boundPort == 0 {
			t.Error("expected non-zero bound port")
		}
	})

	t.Run("2. sys_net_open dial loopback", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		port := ln.Addr().(*net.TCPAddr).Port

		ovPtr := uint32(0x300)
		addr := "127.0.0.1"
		ptrAddr := uint32(0x400)
		mod.Memory().Write(ptrAddr, []byte(addr))

		stack := []uint64{uint64(ovPtr), uint64(ptrAddr), uint64(len(addr)), uint64(port), 1} // flags 1 = connect
		val := runOperation(t, ovPtr, func() {
			env.sys_net_open(context.Background(), mod, stack)
		})
		errno := binary.LittleEndian.Uint32(val[4:8])
		handle := binary.LittleEndian.Uint64(val[16:24])
		if errno != wasiSuccess {
			t.Errorf("failed to dial: %d", errno)
		}
		if handle == 0 {
			t.Error("expected non-zero connection handle")
		} else {
			env.sys_handle_close(context.Background(), mod, []uint64{handle})
		}
	})

	t.Run("3. sys_net_accept", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}

		// Register listener in env
		env.mu.Lock()
		lHandle := env.nextHandle
		env.nextHandle++
		env.handles[lHandle] = ln
		env.mu.Unlock()
		defer env.sys_handle_close(context.Background(), mod, []uint64{lHandle})

		ovPtr := uint32(0x500)
		dialDone := make(chan error, 1)
		go func() {
			conn, err := net.Dial("tcp", ln.Addr().String())
			if conn != nil {
				conn.Close()
			}
			dialDone <- err
		}()
		val := runOperation(t, ovPtr, func() {
			env.sys_net_accept(context.Background(), mod, []uint64{uint64(ovPtr), lHandle})
		})
		errno := binary.LittleEndian.Uint32(val[4:8])
		if errno != wasiSuccess {
			t.Errorf("accept failed: %d", errno)
		}
		cHandle := binary.LittleEndian.Uint64(val[16:24])
		if cHandle == 0 {
			t.Error("accepted handle is 0")
		} else {
			env.sys_handle_close(context.Background(), mod, []uint64{cHandle})
		}
		if err := <-dialDone; err != nil {
			t.Errorf("dial to trigger accept failed: %v", err)
		}
	})

	t.Run("4. sys_net_open invalid address", func(t *testing.T) {
		ovPtr := uint32(0x600)
		addr := "999.999.999.999"
		ptrAddr := uint32(0x700)
		mod.Memory().Write(ptrAddr, []byte(addr))

		stack := []uint64{uint64(ovPtr), uint64(ptrAddr), uint64(len(addr)), 80, 1}
		val := runOperation(t, ovPtr, func() {
			env.sys_net_open(context.Background(), mod, stack)
		})
		errno := binary.LittleEndian.Uint32(val[4:8])
		if errno == 0 {
			t.Error("expected dial to fail for invalid address")
		}
		if handle := binary.LittleEndian.Uint64(val[16:24]); handle != 0 {
			t.Errorf("failed dial returned handle %d", handle)
			env.sys_handle_close(context.Background(), mod, []uint64{handle})
		}
	})

	t.Run("5. sys_net_accept on non-existent handle", func(t *testing.T) {
		ovPtr := uint32(0x800)
		val := runOperation(t, ovPtr, func() {
			env.sys_net_accept(context.Background(), mod, []uint64{uint64(ovPtr), 999999})
		})
		errno := binary.LittleEndian.Uint32(val[4:8])
		if errno != wasiEBADF {
			t.Errorf("expected EBADF (%d), got %d", wasiEBADF, errno)
		}
	})

	t.Run("6. read/write on network handle", func(t *testing.T) {
		// Set up a pair
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		defer ln.Close()

		serverDone := make(chan error, 1)
		go func() {
			c, err := ln.Accept()
			if err != nil {
				serverDone <- err
				return
			}
			_, err = c.Write([]byte("HELLO"))
			c.Close()
			serverDone <- err
		}()

		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		env.mu.Lock()
		cHandle := env.nextHandle
		env.nextHandle++
		env.handles[cHandle] = conn
		env.mu.Unlock()
		defer env.sys_handle_close(context.Background(), mod, []uint64{cHandle})

		ovPtr := uint32(0x900)
		bufPtr := uint32(0xA00)
		val := runOperation(t, ovPtr, func() {
			env.sys_read(context.Background(), mod, []uint64{uint64(ovPtr), uint64(cHandle), uint64(bufPtr), 5})
		})
		if errno := binary.LittleEndian.Uint32(val[4:8]); errno != wasiSuccess {
			t.Fatalf("network read failed: %d", errno)
		}
		readBytes := binary.LittleEndian.Uint64(val[16:24])
		if readBytes != 5 {
			t.Errorf("expected 5 bytes, got %d", readBytes)
		}

		data, _ := mod.Memory().Read(bufPtr, 5)
		if string(data) != "HELLO" {
			t.Errorf("expected HELLO, got %q", string(data))
		}
		if err := <-serverDone; err != nil {
			t.Errorf("server failed to accept connection: %v", err)
		}
	})

	t.Run("7. sys_net_open multiple concurrent dials", func(t *testing.T) {
		const count = 5
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		port := uint64(ln.Addr().(*net.TCPAddr).Port)

		acceptDone := make(chan error, count)
		go func() {
			for i := 0; i < count; i++ {
				conn, err := ln.Accept()
				if err != nil {
					for j := i; j < count; j++ {
						acceptDone <- err
					}
					return
				}
				conn.Close()
				acceptDone <- nil
			}
		}()

		ovPtrs := make([]uint32, count)
		for i := 0; i < count; i++ {
			ovPtr := uint32(0xB00 + i*32)
			addr := "127.0.0.1"
			ptrAddr := uint32(0xC00 + i*32)
			ovPtrs[i] = ovPtr
			mod.Memory().Write(ptrAddr, []byte(addr))
			if !mod.Memory().Write(ovPtr, make([]byte, 24)) {
				t.Fatalf("failed to initialize overlapped request at %d", ovPtr)
			}
			env.sys_net_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(ptrAddr), uint64(len(addr)), port, 1})
		}

		for i := 0; i < count; i++ {
			val := waitForCompletion(t, ovPtrs[i])
			if errno := binary.LittleEndian.Uint32(val[4:8]); errno != wasiSuccess {
				t.Errorf("concurrent dial %d failed with errno %d", i, errno)
			}
			handle := binary.LittleEndian.Uint64(val[16:24])
			if handle == 0 {
				t.Errorf("concurrent dial %d returned no handle", i)
			} else {
				env.sys_handle_close(context.Background(), mod, []uint64{handle})
			}
		}
		for i := 0; i < count; i++ {
			if err := <-acceptDone; err != nil {
				t.Errorf("listener failed to accept connection %d: %v", i, err)
			}
		}
	})

	t.Run("8. close listener stops accept", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		env.mu.Lock()
		lHandle := env.nextHandle
		env.nextHandle++
		env.handles[lHandle] = ln
		env.mu.Unlock()

		ovPtr := uint32(0xD00)
		if !mod.Memory().Write(ovPtr, make([]byte, 24)) {
			t.Fatal("failed to initialize accept request")
		}
		env.sys_net_accept(context.Background(), mod, []uint64{uint64(ovPtr), lHandle})

		// Close while accept is pending
		env.sys_handle_close(context.Background(), mod, []uint64{lHandle})

		val := waitForCompletion(t, ovPtr)
		errno := binary.LittleEndian.Uint32(val[4:8])
		if errno == wasiSuccess {
			t.Error("accept succeeded after its listener was closed")
		}
		if handle := binary.LittleEndian.Uint64(val[16:24]); handle != 0 {
			t.Errorf("failed accept returned connection handle %d", handle)
			env.sys_handle_close(context.Background(), mod, []uint64{handle})
		}
	})

	t.Run("9. sys_handle_close on already closed net handle", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
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
		val := runOperation(t, ovPtr, func() {
			env.sys_net_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(ptrAddr), uint64(len(addr)), 0, 0})
		})
		errno := binary.LittleEndian.Uint32(val[4:8])
		boundPort := binary.LittleEndian.Uint64(val[8:16])
		handle := binary.LittleEndian.Uint64(val[16:24])
		if errno != wasiSuccess {
			t.Errorf("listen failed with errno %d", errno)
		}
		if handle == 0 {
			t.Error("listen failed")
		}
		if boundPort == 0 {
			t.Error("expected non-zero bound port in Overlapped.continued for port 0 listen")
		}
		if handle != 0 {
			env.sys_handle_close(context.Background(), mod, []uint64{handle})
		}
	})

	t.Run("11. accept on closed listener", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ln.Close()
		env.mu.Lock()
		h := env.nextHandle
		env.nextHandle++
		env.handles[h] = ln
		env.mu.Unlock()

		ovPtr := uint32(0x1000)
		val := runOperation(t, ovPtr, func() {
			env.sys_net_accept(context.Background(), mod, []uint64{uint64(ovPtr), h})
		})
		errno := binary.LittleEndian.Uint32(val[4:8])
		if errno == 0 {
			t.Error("expected error accepting on closed listener")
		}
		if handle := binary.LittleEndian.Uint64(val[16:24]); handle != 0 {
			t.Errorf("failed accept returned connection handle %d", handle)
			env.sys_handle_close(context.Background(), mod, []uint64{handle})
		}
		env.sys_handle_close(context.Background(), mod, []uint64{h})
	})

	t.Run("12. write to closed network handle", func(t *testing.T) {
		conn, peer := net.Pipe()
		defer peer.Close()
		env.mu.Lock()
		h := env.nextHandle
		env.nextHandle++
		env.handles[h] = conn
		env.mu.Unlock()

		conn.Close() // manually close

		ovPtr := uint32(0x1100)
		val := runOperation(t, ovPtr, func() {
			env.sys_write(context.Background(), mod, []uint64{uint64(ovPtr), h, 0x1200, 10})
		})
		errno := binary.LittleEndian.Uint32(val[4:8])
		if errno == 0 {
			t.Error("write to closed network handle unexpectedly succeeded")
		}
		env.sys_handle_close(context.Background(), mod, []uint64{h})
	})

	t.Run("13. sys_net_open with localhost addr", func(t *testing.T) {
		ovPtr := uint32(0x1300)
		addr := "127.0.0.1"
		ptrAddr := uint32(0x1400)
		mod.Memory().Write(ptrAddr, []byte(addr))
		val := runOperation(t, ovPtr, func() {
			env.sys_net_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(ptrAddr), uint64(len(addr)), 0, 0})
		})
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
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		env.mu.Lock()
		h := env.nextHandle
		env.nextHandle++
		env.handles[h] = ln
		env.mu.Unlock()
		defer env.sys_handle_close(context.Background(), mod, []uint64{h})

		const count = 5
		ovPtrs := make([]uint32, count)
		clients := make([]net.Conn, 0, count)
		defer func() {
			for _, conn := range clients {
				conn.Close()
			}
		}()
		for i := 0; i < count; i++ {
			ovPtr := uint32(0x1400 + i*32)
			ovPtrs[i] = ovPtr
			if !mod.Memory().Write(ovPtr, make([]byte, 24)) {
				t.Fatalf("failed to initialize accept request at %d", ovPtr)
			}
			env.sys_net_accept(context.Background(), mod, []uint64{uint64(ovPtr), h})
			conn, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatalf("dial %d to listener failed: %v", i, err)
			}
			clients = append(clients, conn)
		}

		for i := 0; i < count; i++ {
			val := waitForCompletion(t, ovPtrs[i])
			if errno := binary.LittleEndian.Uint32(val[4:8]); errno != wasiSuccess {
				t.Errorf("accept %d failed with errno %d", i, errno)
			}
			handle := binary.LittleEndian.Uint64(val[16:24])
			if handle == 0 {
				t.Errorf("accept %d returned no connection handle", i)
			} else {
				env.sys_handle_close(context.Background(), mod, []uint64{handle})
			}
		}
	})
}
