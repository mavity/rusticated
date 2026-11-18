//go:build wasip1

package wasmabitest

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestMiscAndPlatformInfo(t *testing.T) {
	t0 := time.Now()
	if t0.IsZero() {
		t.Fatal("time.Now() returned zero")
	}

	var rnd [32]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		t.Fatalf("crypto/rand.Read failed: %v", err)
	}
	allZero := true
	for _, b := range rnd {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Fatal("crypto/rand.Read returned all zeroes")
	}

	cwd, err := os.Getwd()
	if err != nil || cwd == "" {
		t.Fatalf("os.Getwd failed: %v (cwd=%q)", err, cwd)
	}

	pi := syscall.GetPlatformInfo()
	if pi.OSKind == 0 || pi.CPUType == 0 || pi.CPUBitness != 64 {
		t.Fatalf("unexpected PlatformInfo: OSKind=%d CPUType=%d CPUBitness=%d", pi.OSKind, pi.CPUType, pi.CPUBitness)
	}
}

func TestTimerSleep(t *testing.T) {
	start := time.Now()
	<-time.After(20 * time.Millisecond)
	if time.Since(start) < 5*time.Millisecond {
		t.Fatalf("time.After returned too early: %v", time.Since(start))
	}
}

func TestFilesystemLifecycle(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	p1 := filepath.Join(sub, "a.txt")
	p2 := filepath.Join(sub, "b.txt")
	want := []byte("hello-wasm-abi-fs")
	if err := os.WriteFile(p1, want, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(p1)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("ReadFile = (%q, %v), want %q", got, err, want)
	}
	if err := os.Truncate(p1, 5); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	st, err := os.Stat(p1)
	if err != nil || st.Size() != 5 {
		t.Fatalf("Stat after Truncate = (%v, size=%d)", err, st.Size())
	}
	if err := os.Rename(p1, p2); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	entries, err := os.ReadDir(sub)
	if err != nil || len(entries) != 1 || entries[0].Name() != "b.txt" {
		t.Fatalf("ReadDir = (%v, %v)", entries, err)
	}
}

func TestRemoveReadOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read-only")
	if err := os.WriteFile(path, []byte("read-only"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove read-only file: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stat after remove = %v, want not-exist", err)
	}
}

func TestOverlappedFileSeekAndAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overlapped-file")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, "abcdef"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if _, err := file.Seek(2, io.SeekStart); err != nil {
		file.Close()
		t.Fatal(err)
	}
	got := make([]byte, 2)
	if _, err := io.ReadFull(file, got); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if string(got) != "cd" {
		file.Close()
		t.Fatalf("seek read = %q, want %q", got, "cd")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	file, err = os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	var first [1]byte
	if _, err := io.ReadFull(file, first[:]); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if first[0] != 'a' {
		file.Close()
		t.Fatalf("read at initial append-mode offset = %q, want %q", first[0], 'a')
	}
	if _, err := io.WriteString(file, "!"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abcdef!" {
		t.Fatalf("file after append = %q, want %q", got, "abcdef!")
	}
}

func TestLinksAndMetadata(t *testing.T) {
	dir := t.TempDir()
	tgt := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(tgt, []byte("abc"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	hlink := filepath.Join(dir, "hard.txt")
	if err := os.Link(tgt, hlink); err != nil {
		t.Fatalf("Link: %v", err)
	}
	slink := filepath.Join(dir, "sym.txt")
	if err := os.Symlink(tgt, slink); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	rl, err := os.Readlink(slink)
	if err != nil || rl != tgt {
		t.Fatalf("Readlink = (%q, %v), want %q", rl, err, tgt)
	}
	if err := os.Chmod(tgt, 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	now := time.Now().Truncate(time.Second)
	if err := os.Chtimes(tgt, now, now); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
}

func TestNetLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_, err = io.Copy(conn, conn)
		done <- err
	}()

	cli, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cli.Close()

	if _, err := cli.Write([]byte("ping")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var buf [4]byte
	if _, err := io.ReadFull(cli, buf[:]); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if string(buf[:]) != "ping" {
		t.Fatalf("got %q, want ping", buf[:])
	}
	_ = cli.Close()
	if err := <-done; err != nil {
		t.Fatalf("server: %v", err)
	}
}

func TestPipeAndSubprocess(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if _, err := w.Write([]byte("pipe")); err != nil {
		t.Fatalf("pipe write: %v", err)
	}
	_ = w.Close()
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || string(got) != "pipe" {
		t.Fatalf("pipe read = (%q, %v)", got, err)
	}

	out, err := exec.Command("cmd", "/c", "echo", "ok").Output()
	if err != nil {
		t.Fatalf("exec.Command: %v", err)
	}
	if !bytes.Contains(out, []byte("ok")) {
		t.Fatalf("exec output = %q", out)
	}
}

func TestOverlappedPipeReadWriteAndEOF(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	payload := bytes.Repeat([]byte("pipe payload\n"), 16*1024)
	writeDone := make(chan error, 1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		_, writeErr := io.Copy(writer, bytes.NewReader(payload))
		closeErr := writer.Close()
		if writeErr != nil {
			writeDone <- writeErr
		} else {
			writeDone <- closeErr
		}
	}()

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("pipe data length = %d, want %d", len(got), len(payload))
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
}
