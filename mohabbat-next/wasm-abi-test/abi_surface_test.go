//go:build wasip1

package wasmabitest

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syscall"
)

func TestRusticatedABIPlatformDirectories(t *testing.T) {
	info := syscall.GetPlatformInfo()
	if info == nil || info.OSKind == 0 {
		t.Fatal("platform information is unavailable")
	}
	if info.UserHomeDir == "" || info.TempDir == "" {
		t.Fatalf("platform directories are incomplete: home=%q temp=%q", info.UserHomeDir, info.TempDir)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("get user home: %v", err)
	}
	if home != info.UserHomeDir {
		t.Fatalf("user home = %q, platform info has %q", home, info.UserHomeDir)
	}
	if temp := os.TempDir(); temp != info.TempDir {
		t.Fatalf("temporary directory = %q, platform info has %q", temp, info.TempDir)
	}
	for label, path := range map[string]string{"home": home, "temp": info.TempDir} {
		stat, err := os.Stat(path)
		if err != nil || !stat.IsDir() {
			t.Errorf("%s directory %q is unavailable: %v", label, path, err)
		}
	}
}

func TestRusticatedABIRandomGet(t *testing.T) {
	if err := syscall.RandomGet(nil); err != nil {
		t.Fatalf("zero-length random request: %v", err)
	}
	data := make([]byte, 64)
	if err := syscall.RandomGet(data); err != nil {
		t.Fatalf("random request: %v", err)
	}
}

func TestRusticatedABIArguments(t *testing.T) {
	if len(os.Args) == 0 || os.Args[0] == "" {
		t.Fatalf("argument vector is empty or has no program name: %q", os.Args)
	}
}

func TestRusticatedABIDirectoryEnumeration(t *testing.T) {
	root := t.TempDir()
	subdir := filepath.Join(root, "subdir")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatalf("create subdirectory: %v", err)
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read directory: %v", err)
	}
	found := make(map[string]bool, len(entries))
	for _, entry := range entries {
		found[entry.Name()] = entry.IsDir()
	}
	if len(found) != 3 || found["first.txt"] || found["second.txt"] || !found["subdir"] {
		t.Fatalf("directory entries = %v, want two files and one directory", found)
	}
}

func TestRusticatedABIFileDescriptorOperations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "descriptor.bin")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open file: %v", err)
	}
	defer file.Close()
	if _, err := file.Write([]byte("abcdef")); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := file.Seek(2, io.SeekStart); err != nil {
		t.Fatalf("seek file: %v", err)
	}
	read := make([]byte, 2)
	if _, err := io.ReadFull(file, read); err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(read) != "cd" {
		t.Fatalf("read after seek = %q, want %q", read, "cd")
	}
	if err := file.Truncate(4); err != nil {
		t.Fatalf("truncate file: %v", err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatalf("stat open file: %v", err)
	}
	if info.Size() != 4 {
		t.Fatalf("truncated file size = %d, want 4", info.Size())
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}
}

func TestRusticatedABIFileTimestamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timestamp.bin")
	if err := os.WriteFile(path, []byte("timestamp"), 0o600); err != nil {
		t.Fatalf("create file: %v", err)
	}
	want := time.Date(2020, time.January, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(path, want, want); err != nil {
		t.Fatalf("set file times: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if delta := info.ModTime().Sub(want); delta < -time.Second || delta > time.Second {
		t.Fatalf("modification time = %s, want within one second of %s", info.ModTime(), want)
	}
}

func TestRusticatedABIMissingPathErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat missing path error = %v, want not-exist", err)
	}
	if _, err := os.Open(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("open missing path error = %v, want not-exist", err)
	}
	if err := os.Remove(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("remove missing path error = %v, want not-exist", err)
	}
	if err := os.Rename(missing, missing+"-renamed"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("rename missing path error = %v, want not-exist", err)
	}
}

func TestRusticatedABINetworkLocalhostLookup(t *testing.T) {
	addresses, err := net.LookupHost("localhost")
	if err != nil {
		t.Fatalf("lookup localhost: %v", err)
	}
	for _, address := range addresses {
		if ip := net.ParseIP(address); ip != nil && ip.IsLoopback() {
			return
		}
	}
	t.Fatalf("localhost addresses %v contain no loopback address", addresses)
}

func TestRusticatedABINetworkLoopback(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	defer listener.Close()
	tcpListener, ok := listener.(*net.TCPListener)
	if !ok {
		t.Fatalf("listener type = %T, want *net.TCPListener", listener)
	}
	if err := tcpListener.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set listener deadline: %v", err)
	}

	type acceptResult struct {
		conn net.Conn
		err  error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		conn, err := listener.Accept()
		accepted <- acceptResult{conn: conn, err: err}
	}()

	client, err := net.DialTimeout("tcp", listener.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial loopback: %v", err)
	}
	defer client.Close()

	var server net.Conn
	select {
	case result := <-accepted:
		if result.err != nil {
			t.Fatalf("accept loopback connection: %v", result.err)
		}
		server = result.conn
	case <-time.After(5 * time.Second):
		t.Fatal("loopback accept did not complete")
	}
	defer server.Close()
	if err := server.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set server deadline: %v", err)
	}
	if _, err := client.Write([]byte("abi")); err != nil {
		t.Fatalf("write loopback connection: %v", err)
	}
	got := make([]byte, 3)
	if _, err := io.ReadFull(server, got); err != nil {
		t.Fatalf("read loopback connection: %v", err)
	}
	if string(got) != "abi" {
		t.Fatalf("loopback payload = %q, want %q", got, "abi")
	}
}

func TestRusticatedABIProcessExitStatus(t *testing.T) {
	info := syscall.GetPlatformInfo()
	if info == nil {
		t.Fatal("platform information is unavailable")
	}
	var command *exec.Cmd
	switch info.OSKind {
	case 1:
		command = exec.Command("cmd.exe", "/C", "exit", "23")
	case 2, 3:
		command = exec.Command("/bin/sh", "-c", "exit 23")
	default:
		t.Fatalf("no process test command is defined for OS kind %d", info.OSKind)
	}
	command.Stdin = bytes.NewReader(nil)
	_, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("process exited successfully, want exit status 23")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("process error = %T (%v), want *exec.ExitError", err, err)
	}
	if code := exitErr.ExitCode(); code != 23 {
		t.Fatalf("child exit status = %d, want 23", code)
	}
}

func TestRusticatedABIProcessSpawnAndPipes(t *testing.T) {
	info := syscall.GetPlatformInfo()
	if info == nil {
		t.Fatal("platform information is unavailable")
	}
	var command *exec.Cmd
	switch info.OSKind {
	case 1:
		command = exec.Command("cmd.exe", "/C", "echo", "rusticated-abi-process")
	case 2, 3:
		command = exec.Command("/bin/sh", "-c", "printf rusticated-abi-process")
	default:
		t.Fatalf("no process test command is defined for OS kind %d", info.OSKind)
	}
	command.Stdin = bytes.NewReader(nil)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run child process: %v; output: %s", err, output)
	}
	if !strings.Contains(string(output), "rusticated-abi-process") {
		t.Fatalf("child output = %q, want process marker", output)
	}
}

func TestRusticatedABIWorkingDirectoryChange(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("get original working directory: %v", err)
	}
	want := t.TempDir()
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	if err := os.Chdir(want); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	got, err := os.Getwd()
	if err != nil {
		t.Fatalf("get changed working directory: %v", err)
	}
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("working directory = %q, want %q", got, want)
	}
}
