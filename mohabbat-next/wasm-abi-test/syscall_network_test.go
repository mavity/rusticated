//go:build wasip1

package wasmabitest

import (
	"bytes"
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestRusticatedABISyscallUDPLoopback(t *testing.T) {
	serverFD, err := syscall.Socket(2, 2, 17)
	if err != nil {
		t.Fatalf("create UDP listener socket: %v", err)
	}
	defer syscall.Close(serverFD)
	serverAddr := syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}
	if err := syscall.Bind(serverFD, serverAddr); err != nil {
		t.Fatalf("bind UDP listener: %v", err)
	}
	if err := syscall.Listen(serverFD, 0); err != nil {
		t.Fatalf("listen on UDP socket: %v", err)
	}
	port := syscall.SocketBoundPort(serverFD)
	if port == 0 {
		t.Fatal("UDP bind did not report its assigned port")
	}

	clientFD, err := syscall.Socket(2, 2, 17)
	if err != nil {
		t.Fatalf("create UDP client socket: %v", err)
	}
	defer syscall.Close(clientFD)
	clientAddr := syscall.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}
	if err := syscall.Connect(clientFD, clientAddr); err != nil {
		t.Fatalf("connect UDP client: %v", err)
	}

	want := []byte("rusticated-udp")
	if n, err := syscall.Write(clientFD, want); err != nil || n != len(want) {
		t.Fatalf("write UDP datagram = (%d, %v), want %d bytes", n, err, len(want))
	}
	type readResult struct{ n int; err error }
	read := make(chan readResult, 1)
	go func() {
		got := make([]byte, len(want)+1)
		n, err := syscall.Read(serverFD, got)
		if err == nil && !bytes.Equal(got[:n], want) {
			err = errors.New("received datagram payload differs")
		}
		read <- readResult{n: n, err: err}
	}()
	select {
	case result := <-read:
		if result.err != nil {
			t.Fatalf("read UDP datagram: %v", result.err)
		}
		if result.n != len(want) {
			t.Fatalf("UDP payload length = %d, want %d", result.n, len(want))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UDP loopback read did not complete")
	}
}

func TestRusticatedABISyscallCertificateVerificationRejectsMalformedDER(t *testing.T) {
	verified, err := syscall.NetCertVerify("localhost", [][]byte{{0x30, 0x00}})
	if verified != 0 || err == nil {
		t.Fatalf("verify malformed certificate = (%d, %v), want an error and untrusted result", verified, err)
	}
	if !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.EIO) {
		t.Fatalf("malformed certificate error = %v, want EINVAL or host validation error EIO", err)
	}
}

func TestRusticatedABISyscallIsattyRejectsInvalidDescriptor(t *testing.T) {
	if syscall.Isatty(^uintptr(0)) {
		t.Fatal("invalid descriptor reported as a TTY")
	}
}
