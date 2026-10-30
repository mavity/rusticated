package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

type mockReadWriter struct {
	readFunc  func([]byte) (int, error)
	writeFunc func([]byte) (int, error)
}

func (m *mockReadWriter) Read(p []byte) (int, error)  { return m.readFunc(p) }
func (m *mockReadWriter) Write(p []byte) (int, error) { return m.writeFunc(p) }

func TestSysReadExtended(t *testing.T) {
	env := NewHostEnv()
	mod := newMockModule(1024 * 1024)
	mem := mod.Memory()

	setupFile := func(t *testing.T, content string) uint64 {
		tmpDir := t.TempDir()
		tmp := tmpDir + "/read_test.txt"
		_ = os.WriteFile(tmp, []byte(content), 0644)
		f, _ := os.Open(tmp)
		env.mu.Lock()
		h := uint64(len(env.handles) + 10)
		env.handles[h] = f
		env.mu.Unlock()
		// Important for Windows: close handle before TempDir cleanup
		t.Cleanup(func() {
			env.mu.Lock()
			if f, ok := env.handles[h].(*os.File); ok {
				f.Close()
			}
			delete(env.handles, h)
			env.mu.Unlock()
		})
		return h
	}

	runOp := func() {
		select {
		case op := <-env.fileOpsQueue:
			op()
			if env.PendingOps() == 0 {
				for {
					select {
					case wakeup := <-env.fileOpsQueue:
						wakeup()
					default:
						return
					}
				}
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("timeout waiting for op")
		}
	}
	submitRead := func(stack []uint64, flags uint32, offset uint64) {
		requestLen := 24
		if flags&readFlagPositional != 0 {
			requestLen = 32
		}
		request := make([]byte, requestLen)
		binary.LittleEndian.PutUint32(request[:4], flags)
		if requestLen == 32 {
			binary.LittleEndian.PutUint64(request[24:32], offset)
		}
		mem.Write(uint32(stack[0]), request)
		env.sys_read(context.Background(), mod, stack)
	}

	t.Run("1. read small chunk happy path", func(t *testing.T) {
		h := setupFile(t, "hello world")
		stack := []uint64{200, h, 400, 5}
		submitRead(stack, 0, 0)
		runOp()
		buf, _ := mem.Read(400, 5)
		if string(buf) != "hello" {
			t.Errorf("got %q, want %q", string(buf), "hello")
		}
	})

	t.Run("2. read zero bytes", func(t *testing.T) {
		h := setupFile(t, "data")
		stack := []uint64{200, h, 401, 0}
		submitRead(stack, 0, 0)
		runOp()
		buf, _ := mem.Read(200, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) != 0 {
			t.Errorf("errno got %d, want 0", binary.LittleEndian.Uint32(buf[4:8]))
		}
	})

	t.Run("3. read at EOF", func(t *testing.T) {
		h := setupFile(t, "a")
		submitRead([]uint64{200, h, 402, 1}, 0, 0)
		runOp()
		submitRead([]uint64{200, h, 402, 1}, 0, 0)
		runOp()
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		if n != 0 {
			t.Errorf("n at EOF got %d, want 0", n)
		}
	})

	t.Run("4. read invalid handle", func(t *testing.T) {
		stack := []uint64{200, 99999, 403, 5}
		submitRead(stack, 0, 0)
		buf, _ := mem.Read(200, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) != wasiEBADF {
			t.Errorf("errno got %d, want %d", binary.LittleEndian.Uint32(buf[4:8]), wasiEBADF)
		}
	})

	t.Run("5. read invalid guest memory", func(t *testing.T) {
		h := setupFile(t, "data")
		stack := []uint64{200, h, 2000000, 5}
		submitRead(stack, 0, 0)
		buf, _ := mem.Read(200, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) != wasiEINVAL {
			t.Errorf("errno got %d, want %d", binary.LittleEndian.Uint32(buf[4:8]), wasiEINVAL)
		}
	})

	t.Run("6. read from write-only handle", func(t *testing.T) {
		tmpDir := t.TempDir()
		tmp := tmpDir + "/write_only.txt"
		f, _ := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE, 0644)
		h := uint64(50)
		env.mu.Lock()
		env.handles[h] = f
		env.mu.Unlock()
		t.Cleanup(func() { f.Close() })

		stack := []uint64{200, h, 300, 5}
		submitRead(stack, 0, 0)
		runOp()

		buf, _ := mem.Read(200, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) == 0 {
			t.Error("read succeeded on write-only handle")
		}
	})

	t.Run("7. read large chunk", func(t *testing.T) {
		data := bytes.Repeat([]byte("a"), 10000)
		h := setupFile(t, string(data))
		stack := []uint64{200, h, 404, 10000}
		submitRead(stack, 0, 0)
		runOp()
		buf, _ := mem.Read(404, 10000)
		if !bytes.Equal(buf, data) {
			t.Error("large read failed")
		}
	})

	t.Run("8. sequential reads", func(t *testing.T) {
		h := setupFile(t, "abcdef")
		submitRead([]uint64{200, h, 405, 3}, 0, 0)
		runOp()
		submitRead([]uint64{200, h, 408, 3}, 0, 0)
		runOp()
		buf, _ := mem.Read(405, 6)
		if string(buf) != "abcdef" {
			t.Errorf("got %q, want %q", string(buf), "abcdef")
		}
	})

	t.Run("8a. positioned read leaves shared position unchanged", func(t *testing.T) {
		h := setupFile(t, "abcdef")
		env.mu.Lock()
		f := env.handles[h].(*os.File)
		_, err := f.Seek(3, 0)
		env.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}

		submitRead([]uint64{200, h, 500, 2}, readFlagPositional, 1)
		runOp()
		positioned, _ := mem.Read(500, 2)
		if string(positioned) != "bc" {
			t.Fatalf("positioned read got %q, want %q", positioned, "bc")
		}
		pos, err := f.Seek(0, io.SeekCurrent)
		if err != nil {
			t.Fatal(err)
		}
		if pos != 3 {
			t.Fatalf("positioned read changed shared position to %d, want 3", pos)
		}

		submitRead([]uint64{230, h, 510, 2}, 0, 0)
		runOp()
		completion, _ := mem.Read(230, 24)
		if errno := binary.LittleEndian.Uint32(completion[4:8]); errno != 0 {
			t.Fatalf("sequential read errno=%d", errno)
		}
		if n := binary.LittleEndian.Uint64(completion[16:24]); n != 2 {
			t.Fatalf("sequential read returned %d bytes, want 2", n)
		}
		sequential, _ := mem.Read(510, 2)
		if string(sequential) != "de" {
			t.Errorf("sequential read got %q, want %q", sequential, "de")
		}
	})

	t.Run("8b. rejects unsupported read flags and offsets", func(t *testing.T) {
		h := setupFile(t, "data")
		submitRead([]uint64{200, h, 520, 1}, 1<<25, 0)
		flags, _ := mem.Read(204, 4)
		if got := binary.LittleEndian.Uint32(flags); got != wasiEINVAL {
			t.Errorf("unsupported flags errno=%d, want %d", got, wasiEINVAL)
		}

		submitRead([]uint64{230, h, 521, 1}, readFlagPositional, ^uint64(0))
		flags, _ = mem.Read(234, 4)
		if got := binary.LittleEndian.Uint32(flags); got != wasiEOVERFLOW {
			t.Errorf("overflowing offset errno=%d, want %d", got, wasiEOVERFLOW)
		}
	})

	t.Run("8c. positioned read rejects non-positionable handles", func(t *testing.T) {
		h := uint64(63)
		env.mu.Lock()
		env.handles[h] = &mockReadWriter{readFunc: func(p []byte) (int, error) {
			return copy(p, "pipe"), nil
		}}
		env.mu.Unlock()
		t.Cleanup(func() {
			env.mu.Lock()
			delete(env.handles, h)
			env.mu.Unlock()
		})

		submitRead([]uint64{200, h, 522, 1}, readFlagPositional, 0)
		flags, _ := mem.Read(204, 4)
		if got := binary.LittleEndian.Uint32(flags); got != wasiESPIPE {
			t.Errorf("non-positionable errno=%d, want %d", got, wasiESPIPE)
		}
	})

	t.Run("9. read error mapping", func(t *testing.T) {
		h := uint64(60)
		env.mu.Lock()
		env.handles[h] = &mockReadWriter{
			readFunc: func(p []byte) (int, error) {
				return 0, errors.New("io error")
			},
		}
		env.mu.Unlock()
		t.Cleanup(func() {
			env.mu.Lock()
			delete(env.handles, h)
			env.mu.Unlock()
		})

		stack := []uint64{200, h, 411, 5}
		submitRead(stack, 0, 0)
		runOp()
		buf, _ := mem.Read(200, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) == 0 {
			t.Error("expected error but got 0")
		}
	})

	t.Run("10. read cancelation", func(t *testing.T) {
		h := uint64(61)
		readStarted := make(chan struct{})
		env.mu.Lock()
		env.handles[h] = &mockReadWriter{
			readFunc: func(p []byte) (int, error) {
				close(readStarted)
				time.Sleep(50 * time.Millisecond)
				return 5, nil
			},
		}
		env.mu.Unlock()
		t.Cleanup(func() {
			env.mu.Lock()
			delete(env.handles, h)
			env.mu.Unlock()
		})

		stack := []uint64{200, h, 412, 5}
		submitRead(stack, 0, 0)
		<-readStarted
		env.sys_cancel(context.Background(), mod, []uint64{200})
		runOp()
	})

	t.Run("11. read from directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		f, _ := os.Open(tmpDir)
		h := uint64(62)
		env.mu.Lock()
		env.handles[h] = f
		env.mu.Unlock()
		t.Cleanup(func() { f.Close() })

		stack := []uint64{200, h, 413, 5}
		submitRead(stack, 0, 0)
		runOp()
		buf, _ := mem.Read(200, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) == 0 {
			t.Error("read from directory should fail")
		}
	})

	t.Run("12. read partial data", func(t *testing.T) {
		h := setupFile(t, "abc")
		stack := []uint64{200, h, 418, 10}
		submitRead(stack, 0, 0)
		runOp()
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		if n != 3 {
			t.Errorf("n got %d, want 3", n)
		}
	})

	t.Run("13. concurrent reads different handles", func(t *testing.T) {
		h1 := setupFile(t, "111")
		h2 := setupFile(t, "222")
		submitRead([]uint64{200, h1, 428, 3}, 0, 0)
		submitRead([]uint64{300, h2, 431, 3}, 0, 0)
		runOp()
		runOp()
		b1, _ := mem.Read(428, 3)
		b2, _ := mem.Read(431, 3)
		if string(b1) != "111" || string(b2) != "222" {
			t.Error("concurrent reads failed")
		}
	})

	t.Run("14. read from handle 0 (stdin mock)", func(t *testing.T) {
		env.mu.Lock()
		env.handles[0] = bytes.NewReader([]byte("input"))
		env.mu.Unlock()
		submitRead([]uint64{200, 0, 440, 5}, 0, 0)
		runOp()
		buf, _ := mem.Read(440, 5)
		if string(buf) != "input" {
			t.Errorf("stdin read got %q", string(buf))
		}
	})

	t.Run("15. handle removed from map", func(t *testing.T) {
		h := setupFile(t, "data")
		env.mu.Lock()
		f := env.handles[h].(*os.File)
		delete(env.handles, h)
		env.mu.Unlock()
		f.Close()

		stack := []uint64{200, h, 445, 5}
		submitRead(stack, 0, 0)
		buf, _ := mem.Read(200, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) != wasiEBADF {
			t.Error("read should fail for removed handle")
		}
	})

	t.Run("16. closed file descriptor", func(t *testing.T) {
		h := setupFile(t, "data")
		env.mu.Lock()
		f := env.handles[h].(*os.File)
		f.Close()
		env.mu.Unlock()
		stack := []uint64{200, h, 450, 5}
		submitRead(stack, 0, 0)
		runOp()
		buf, _ := mem.Read(200, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) == 0 {
			t.Error("read should fail for closed FD")
		}
	})

	t.Run("17. huge length (overflow check)", func(t *testing.T) {
		h := setupFile(t, "a")
		stack := []uint64{200, h, 400, 0xFFFFFFFF}
		env.sys_read(context.Background(), mod, stack)
		buf, _ := mem.Read(200, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) != wasiEINVAL {
			t.Errorf("errno got %d, want %d", binary.LittleEndian.Uint32(buf[4:8]), wasiEINVAL)
		}
	})

	t.Run("18. read after seek", func(t *testing.T) {
		h := setupFile(t, "0123456789")
		env.mu.Lock()
		f := env.handles[h].(*os.File)
		f.Seek(5, 0)
		env.mu.Unlock()
		submitRead([]uint64{200, h, 460, 2}, 0, 0)
		runOp()
		buf, _ := mem.Read(460, 2)
		if string(buf) != "56" {
			t.Errorf("got %q, want %q", string(buf), "56")
		}
	})

	t.Run("19. read into end of memory", func(t *testing.T) {
		h := setupFile(t, "abc")
		lastByte := uint32(1024*1024 - 3)
		stack := []uint64{200, h, uint64(lastByte), 3}
		submitRead(stack, 0, 0)
		runOp()
		buf, _ := mem.Read(lastByte, 3)
		if string(buf) != "abc" {
			t.Error("read into end of memory failed")
		}
	})

	t.Run("20. concurrency queue stress", func(t *testing.T) {
		h := setupFile(t, "data")
		for i := 0; i < 10; i++ {
			submitRead([]uint64{uint64(500 + i*30), h, uint64(600 + i*10), 1}, 0, 0)
		}
		for i := 0; i < 10; i++ {
			runOp()
		}
		if env.PendingOps() != 0 {
			t.Error("pending ops not zero after drain")
		}
	})
}

func TestSysWriteExtended(t *testing.T) {
	env := NewHostEnv()
	mod := newMockModule(1024 * 1024)
	mem := mod.Memory()

	setupFile := func(t *testing.T) (uint64, string) {
		tmpDir := t.TempDir()
		tmp := tmpDir + "/write_test.txt"
		f, _ := os.Create(tmp)
		env.mu.Lock()
		h := uint64(len(env.handles) + 100)
		env.handles[h] = f
		env.mu.Unlock()
		t.Cleanup(func() {
			env.mu.Lock()
			if f, ok := env.handles[h].(*os.File); ok {
				f.Close()
			}
			delete(env.handles, h)
			env.mu.Unlock()
		})
		return h, tmp
	}

	clearOverlapped := func(t *testing.T, ovPtr uint32) {
		t.Helper()
		if !mem.Write(ovPtr, make([]byte, 24)) {
			t.Fatalf("failed to clear overlapped header at %d", ovPtr)
		}
	}
	runOp := func(t *testing.T, ovPtr uint32) {
		t.Helper()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		for {
			buf, ok := mem.Read(ovPtr, 4)
			if !ok {
				t.Fatalf("overlapped pointer %d is out of bounds", ovPtr)
			}
			if binary.LittleEndian.Uint32(buf) == 1 {
				if env.PendingOps() == 0 {
					for {
						select {
						case op := <-env.fileOpsQueue:
							op()
						default:
							return
						}
					}
				}
				return
			}

			select {
			case op := <-env.fileOpsQueue:
				op()
			case <-timer.C:
				t.Fatalf("timeout waiting for overlapped completion at %d", ovPtr)
			}
		}
	}
	submitWrite := func(t *testing.T, stack []uint64) {
		t.Helper()
		ovPtr := uint32(stack[0])
		clearOverlapped(t, ovPtr)
		env.sys_write(context.Background(), mod, stack)
	}
	write := func(t *testing.T, stack []uint64) {
		t.Helper()
		submitWrite(t, stack)
		runOp(t, uint32(stack[0]))
	}
	waitForCancelledWrite := func(t *testing.T) {
		t.Helper()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for len(env.fileOpsQueue) < 2 {
			select {
			case <-timer.C:
				t.Fatal("timeout waiting for cancelled write completion")
			case <-ticker.C:
			}
		}
		for len(env.fileOpsQueue) > 0 {
			op := <-env.fileOpsQueue
			op()
		}
	}

	t.Run("1. write small chunk happy path", func(t *testing.T) {
		h, path := setupFile(t)
		mem.Write(400, []byte("hello"))
		stack := []uint64{200, h, 400, 5}
		write(t, stack)
		got, _ := os.ReadFile(path)
		if string(got) != "hello" {
			t.Errorf("got %q, want %q", string(got), "hello")
		}
	})

	t.Run("2. write zero bytes", func(t *testing.T) {
		h, path := setupFile(t)
		stack := []uint64{200, h, 401, 0}
		write(t, stack)
		got, _ := os.ReadFile(path)
		if len(got) != 0 {
			t.Error("write zero bytes failed")
		}
	})

	t.Run("3. write to stdout (handle 1)", func(t *testing.T) {
		var buf bytes.Buffer
		env.mu.Lock()
		env.handles[1] = &buf
		env.mu.Unlock()
		mem.Write(402, []byte("stdout text"))
		write(t, []uint64{200, 1, 402, 11})
		if buf.String() != "stdout text" {
			t.Errorf("stdout got %q", buf.String())
		}
	})

	t.Run("4. write to stderr (handle 2)", func(t *testing.T) {
		var buf bytes.Buffer
		env.mu.Lock()
		env.handles[2] = &buf
		env.mu.Unlock()
		mem.Write(403, []byte("stderr text"))
		write(t, []uint64{200, 2, 403, 11})
		if buf.String() != "stderr text" {
			t.Errorf("stderr got %q", buf.String())
		}
	})

	t.Run("5. write to invalid handle", func(t *testing.T) {
		stack := []uint64{200, 88888, 404, 5}
		write(t, stack)
		buf, _ := mem.Read(200, 8)
		if got := binary.LittleEndian.Uint32(buf[4:8]); got != wasiEBADF {
			t.Errorf("invalid handle errno = %d, want %d", got, wasiEBADF)
		}
	})

	t.Run("6. write point to invalid memory", func(t *testing.T) {
		h, _ := setupFile(t)
		stack := []uint64{200, h, 2000000, 5}
		write(t, stack)
		buf, _ := mem.Read(200, 8)
		if got := binary.LittleEndian.Uint32(buf[4:8]); got != wasiEINVAL {
			t.Errorf("invalid memory errno = %d, want %d", got, wasiEINVAL)
		}
	})

	t.Run("7. write to read-only handle", func(t *testing.T) {
		h, path := setupFile(t)
		env.mu.Lock()
		f := env.handles[h].(*os.File)
		f.Close()
		f2, _ := os.Open(path)
		env.handles[h] = f2
		env.mu.Unlock()
		mem.Write(405, []byte("fail"))
		mem.Write(405, []byte("fail"))
		write(t, []uint64{200, h, 405, 4})
		buf, _ := mem.Read(200, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) == 0 {
			t.Error("write should fail on read-only handle")
		}
	})

	t.Run("8. write large chunk", func(t *testing.T) {
		h, path := setupFile(t)
		data := bytes.Repeat([]byte("b"), 50000)
		mem.Write(1000, data)
		write(t, []uint64{200, h, 1000, 50000})
		got, _ := os.ReadFile(path)
		if !bytes.Equal(got, data) {
			t.Error("large write data mismatch")
		}
	})

	t.Run("9. sequential writes same handle", func(t *testing.T) {
		h, path := setupFile(t)
		mem.Write(500, []byte("part1"))
		mem.Write(505, []byte("part2"))
		write(t, []uint64{200, h, 500, 5})
		write(t, []uint64{300, h, 505, 5})
		got, _ := os.ReadFile(path)
		if string(got) != "part1part2" {
			t.Errorf("got %q", string(got))
		}
	})

	t.Run("10. write error mapping (mocked)", func(t *testing.T) {
		h := uint64(160)
		env.mu.Lock()
		env.handles[h] = &mockReadWriter{
			writeFunc: func(p []byte) (int, error) {
				return 0, errors.New("write failure")
			},
		}
		env.mu.Unlock()
		t.Cleanup(func() {
			env.mu.Lock()
			delete(env.handles, h)
			env.mu.Unlock()
		})

		mem.Write(510, []byte("data"))
		write(t, []uint64{200, h, 510, 4})
		buf, _ := mem.Read(200, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) == 0 {
			t.Error("expected error")
		}
	})

	t.Run("11. write cancelation", func(t *testing.T) {
		h := uint64(161)
		writeStarted := make(chan struct{})
		env.mu.Lock()
		env.handles[h] = &mockReadWriter{
			writeFunc: func(p []byte) (int, error) {
				close(writeStarted)
				time.Sleep(50 * time.Millisecond)
				return len(p), nil
			},
		}
		env.mu.Unlock()
		t.Cleanup(func() {
			env.mu.Lock()
			delete(env.handles, h)
			env.mu.Unlock()
		})

		submitWrite(t, []uint64{200, h, 515, 4})
		<-writeStarted
		env.sys_cancel(context.Background(), mod, []uint64{200})
		waitForCancelledWrite(t)
	})

	t.Run("12. write to append handle", func(t *testing.T) {
		tmpDir := t.TempDir()
		tmp := tmpDir + "/append.txt"
		_ = os.WriteFile(tmp, []byte("init"), 0644)
		f, _ := os.OpenFile(tmp, os.O_WRONLY|os.O_APPEND, 0644)
		h := uint64(162)
		env.mu.Lock()
		env.handles[h] = f
		env.mu.Unlock()
		t.Cleanup(func() { f.Close() })

		mem.Write(520, []byte("append"))
		write(t, []uint64{200, h, 520, 6})
		got, _ := os.ReadFile(tmp)
		if string(got) != "initappend" {
			t.Errorf("got %q", string(got))
		}
	})

	t.Run("13. concurrent writes different handles", func(t *testing.T) {
		h1, p1 := setupFile(t)
		h2, p2 := setupFile(t)
		mem.Write(530, []byte("data1"))
		mem.Write(540, []byte("data2"))
		submitWrite(t, []uint64{200, h1, 530, 5})
		submitWrite(t, []uint64{300, h2, 540, 5})
		runOp(t, 200)
		runOp(t, 300)
		g1, _ := os.ReadFile(p1)
		g2, _ := os.ReadFile(p2)
		if string(g1) != "data1" || string(g2) != "data2" {
			t.Error("concurrent write failed")
		}
	})

	t.Run("14. handle removed from map during write", func(t *testing.T) {
		h, _ := setupFile(t)
		env.mu.Lock()
		f := env.handles[h].(*os.File)
		delete(env.handles, h)
		env.mu.Unlock()
		f.Close()

		stack := []uint64{200, h, 550, 5}
		write(t, stack)
		buf, _ := mem.Read(200, 8)
		if got := binary.LittleEndian.Uint32(buf[4:8]); got != wasiEBADF {
			t.Errorf("removed handle errno = %d, want %d", got, wasiEBADF)
		}
	})

	t.Run("15. write to closed file descriptor", func(t *testing.T) {
		h, _ := setupFile(t)
		env.mu.Lock()
		f := env.handles[h].(*os.File)
		f.Close()
		env.mu.Unlock()
		stack := []uint64{200, h, 560, 5}
		write(t, stack)
		buf, _ := mem.Read(200, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) == 0 {
			t.Error("write should fail for closed FD")
		}
	})

	t.Run("16. huge length (overflow check)", func(t *testing.T) {
		h, _ := setupFile(t)
		stack := []uint64{200, h, 400, 0xFFFFFFFF}
		write(t, stack)
		buf, _ := mem.Read(200, 8)
		if got := binary.LittleEndian.Uint32(buf[4:8]); got != wasiEINVAL {
			t.Errorf("huge write errno = %d, want %d", got, wasiEINVAL)
		}
	})

	t.Run("17. write and read back consistency", func(t *testing.T) {
		h, path := setupFile(t)
		mem.Write(570, []byte("consistency"))
		write(t, []uint64{200, h, 570, 11})
		f2, _ := os.Open(path)
		h2 := uint64(180)
		env.mu.Lock()
		env.handles[h2] = f2
		env.mu.Unlock()
		t.Cleanup(func() { f2.Close() })
		clearOverlapped(t, 300)
		env.sys_read(context.Background(), mod, []uint64{300, h2, 582, 11})
		runOp(t, 300)
		buf, _ := mem.Read(582, 11)
		if string(buf) != "consistency" {
			t.Error("read back failed")
		}
	})

	t.Run("18. write from end of memory", func(t *testing.T) {
		h, path := setupFile(t)
		lastPos := uint32(1024*1024 - 4)
		mem.Write(lastPos, []byte("last"))
		write(t, []uint64{200, h, uint64(lastPos), 4})
		got, _ := os.ReadFile(path)
		if string(got) != "last" {
			t.Error("write from end of memory failed")
		}
	})

	t.Run("19. short write (mocked)", func(t *testing.T) {
		h := uint64(190)
		env.mu.Lock()
		env.handles[h] = &mockReadWriter{
			writeFunc: func(p []byte) (int, error) {
				return 2, nil
			},
		}
		env.mu.Unlock()
		t.Cleanup(func() {
			env.mu.Lock()
			delete(env.handles, h)
			env.mu.Unlock()
		})
		mem.Write(590, []byte("12345"))
		write(t, []uint64{200, h, 590, 5})
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		if n != 2 {
			t.Errorf("expected n=2, got %d", n)
		}
	})

	t.Run("20. cross-boundary memory write", func(t *testing.T) {
		h, _ := setupFile(t)
		lastPos := uint32(1024*1024 - 2)
		write(t, []uint64{200, h, uint64(lastPos), 5})
		buf, _ := mem.Read(200, 8)
		if got := binary.LittleEndian.Uint32(buf[4:8]); got != wasiEINVAL {
			t.Errorf("cross-boundary write errno = %d, want %d", got, wasiEINVAL)
		}
	})
}
