//go:build wasip1

package wasmabitest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"syscall"
)

func rusticatedEdgeListenTCP(t *testing.T) (int, int) {
	t.Helper()
	fd, err := syscall.Socket(2, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("create TCP socket: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if err := syscall.Bind(fd, syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatalf("bind TCP socket: %v", err)
	}
	if err := syscall.Listen(fd, 8); err != nil {
		t.Fatalf("listen on TCP socket: %v", err)
	}
	port := syscall.SocketBoundPort(fd)
	if port == 0 {
		t.Fatal("TCP listener did not report its assigned port")
	}
	return fd, port
}

func rusticatedEdgeTCPPair(t *testing.T) (listenerFD, clientFD, acceptedFD int) {
	t.Helper()
	listenerFD, port := rusticatedEdgeListenTCP(t)

	clientFD, err := syscall.Socket(2, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("create TCP client: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Close(clientFD) })
	if err := syscall.Connect(clientFD, syscall.SockaddrInet4{
		Port: port, Addr: [4]byte{127, 0, 0, 1},
	}); err != nil {
		t.Fatalf("connect TCP client: %v", err)
	}

	acceptedFD, _, err = syscall.Accept(listenerFD)
	if err != nil {
		t.Fatalf("accept TCP client: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Close(acceptedFD) })
	return listenerFD, clientFD, acceptedFD
}

func TestRusticatedABIEdgeNetOpen(t *testing.T) {
	for _, test := range []struct {
		name   string
		sotype int
	}{
		{"zero", 0},
		{"raw", 3},
		{"negative", -1},
	} {
		t.Run("unsupported socket type "+test.name, func(t *testing.T) {
			if fd, err := syscall.Socket(2, test.sotype, 0); fd != 0 || !errors.Is(err, syscall.ENOSYS) {
				t.Fatalf("Socket(type=%d) = (%d, %v), want (0, ENOSYS)", test.sotype, fd, err)
			}
		})
	}

	for _, test := range []struct {
		name   string
		sotype int
	}{
		{"stream", syscall.SOCK_STREAM},
		{"datagram", syscall.SOCK_DGRAM},
	} {
		t.Run("supported socket type "+test.name, func(t *testing.T) {
			fd, err := syscall.Socket(2, test.sotype, 0)
			if err != nil {
				t.Fatalf("Socket(type=%d): %v", test.sotype, err)
			}
			if fd < 3 {
				t.Fatalf("Socket(type=%d) returned descriptor %d, want allocated descriptor", test.sotype, fd)
			}
			if err := syscall.Close(fd); err != nil {
				t.Fatalf("close socket type %d: %v", test.sotype, err)
			}
		})
	}

	t.Run("bound TCP listener reports its ephemeral port", func(t *testing.T) {
		_, port := rusticatedEdgeListenTCP(t)
		if port < 1 || port > 65535 {
			t.Fatalf("bound port = %d, want a TCP port", port)
		}
	})

	t.Run("unbound TCP listener receives an ephemeral port", func(t *testing.T) {
		fd, err := syscall.Socket(2, syscall.SOCK_STREAM, 0)
		if err != nil {
			t.Fatalf("create TCP socket: %v", err)
		}
		defer syscall.Close(fd)
		if err := syscall.Listen(fd, 1); err != nil {
			t.Fatalf("listen without Bind: %v", err)
		}
		if port := syscall.SocketBoundPort(fd); port == 0 {
			t.Fatal("implicit bind did not report an ephemeral port")
		}
	})

	t.Run("UDP listener reports its ephemeral port", func(t *testing.T) {
		fd, err := syscall.Socket(2, syscall.SOCK_DGRAM, 0)
		if err != nil {
			t.Fatalf("create UDP socket: %v", err)
		}
		defer syscall.Close(fd)
		if err := syscall.Bind(fd, syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
			t.Fatalf("bind UDP socket: %v", err)
		}
		if err := syscall.Listen(fd, 1); err != nil {
			t.Fatalf("listen on UDP socket: %v", err)
		}
		if port := syscall.SocketBoundPort(fd); port == 0 {
			t.Fatal("UDP listener did not report an ephemeral port")
		}
	})

	t.Run("loopback TCP connect succeeds", func(t *testing.T) {
		_, port := rusticatedEdgeListenTCP(t)
		fd, err := syscall.Socket(2, syscall.SOCK_STREAM, 0)
		if err != nil {
			t.Fatalf("create TCP client: %v", err)
		}
		defer syscall.Close(fd)
		if err := syscall.Connect(fd, syscall.SockaddrInet4{
			Port: port, Addr: [4]byte{127, 0, 0, 1},
		}); err != nil {
			t.Fatalf("connect to loopback listener: %v", err)
		}
	})

	t.Run("unspecified connect address is rejected", func(t *testing.T) {
		fd, err := syscall.Socket(2, syscall.SOCK_STREAM, 0)
		if err != nil {
			t.Fatalf("create TCP client: %v", err)
		}
		defer syscall.Close(fd)
		if err := syscall.Connect(fd, syscall.SockaddrInet4{Port: 1}); !errors.Is(err, syscall.EADDRNOTAVAIL) {
			t.Fatalf("connect to unspecified address error = %v, want EADDRNOTAVAIL", err)
		}
	})

	t.Run("refused loopback connect returns an error", func(t *testing.T) {
		listenerFD, port := rusticatedEdgeListenTCP(t)
		if err := syscall.Close(listenerFD); err != nil {
			t.Fatalf("close temporary listener: %v", err)
		}
		clientFD, err := syscall.Socket(2, syscall.SOCK_STREAM, 0)
		if err != nil {
			t.Fatalf("create TCP client: %v", err)
		}
		defer syscall.Close(clientFD)
		if err := syscall.Connect(clientFD, syscall.SockaddrInet4{
			Port: port, Addr: [4]byte{127, 0, 0, 1},
		}); err == nil {
			t.Fatal("connect to a closed loopback port unexpectedly succeeded")
		}
	})

	t.Run("nil IPv4 address is rejected", func(t *testing.T) {
		fd, err := syscall.Socket(2, syscall.SOCK_STREAM, 0)
		if err != nil {
			t.Fatalf("create TCP socket: %v", err)
		}
		defer syscall.Close(fd)
		var address *syscall.SockaddrInet4
		if err := syscall.Bind(fd, address); !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("Bind with nil address error = %v, want EINVAL", err)
		}
	})

	t.Run("unbound descriptor has no reported port", func(t *testing.T) {
		fd, err := syscall.Socket(2, syscall.SOCK_STREAM, 0)
		if err != nil {
			t.Fatalf("create TCP socket: %v", err)
		}
		defer syscall.Close(fd)
		if port := syscall.SocketBoundPort(fd); port != 0 {
			t.Fatalf("unbound socket port = %d, want 0", port)
		}
	})

	t.Run("unknown descriptor has no reported port", func(t *testing.T) {
		if port := syscall.SocketBoundPort(-1); port != 0 {
			t.Fatalf("unknown socket port = %d, want 0", port)
		}
	})
}

func TestRusticatedABIEdgeNetAccept(t *testing.T) {
	t.Run("accept returns an IPv4 peer address", func(t *testing.T) {
		listenerFD, port := rusticatedEdgeListenTCP(t)
		clientFD, err := syscall.Socket(2, syscall.SOCK_STREAM, 0)
		if err != nil {
			t.Fatalf("create TCP client: %v", err)
		}
		defer syscall.Close(clientFD)
		if err := syscall.Connect(clientFD, syscall.SockaddrInet4{
			Port: port, Addr: [4]byte{127, 0, 0, 1},
		}); err != nil {
			t.Fatalf("connect TCP client: %v", err)
		}
		acceptedFD, peer, err := syscall.Accept(listenerFD)
		if err != nil {
			t.Fatalf("accept TCP client: %v", err)
		}
		defer syscall.Close(acceptedFD)
		if acceptedFD < 3 {
			t.Fatalf("accepted descriptor = %d, want allocated descriptor", acceptedFD)
		}
		if _, ok := peer.(syscall.SockaddrInet4); !ok {
			t.Fatalf("accepted peer address type = %T, want SockaddrInet4", peer)
		}
	})

	t.Run("listener accepts successive clients", func(t *testing.T) {
		listenerFD, port := rusticatedEdgeListenTCP(t)
		for i, clientNumber := range []string{"first", "second"} {
			t.Run(clientNumber+" client", func(t *testing.T) {
				clientFD, err := syscall.Socket(2, syscall.SOCK_STREAM, 0)
				if err != nil {
					t.Fatalf("create client %d: %v", i, err)
				}
				defer syscall.Close(clientFD)
				if err := syscall.Connect(clientFD, syscall.SockaddrInet4{
					Port: port, Addr: [4]byte{127, 0, 0, 1},
				}); err != nil {
					t.Fatalf("connect client %d: %v", i, err)
				}
				acceptedFD, _, err := syscall.Accept(listenerFD)
				if err != nil {
					t.Fatalf("accept client %d: %v", i, err)
				}
				defer syscall.Close(acceptedFD)
			})
		}
	})

	t.Run("invalid descriptor returns EBADF", func(t *testing.T) {
		if fd, _, err := syscall.Accept(-1); fd != -1 || !errors.Is(err, syscall.EBADF) {
			t.Fatalf("Accept(-1) = (%d, %v), want (-1, EBADF)", fd, err)
		}
	})

	t.Run("largest positive descriptor returns EBADF", func(t *testing.T) {
		fd := int(^uint(0) >> 1)
		if acceptedFD, _, err := syscall.Accept(fd); acceptedFD != -1 || !errors.Is(err, syscall.EBADF) {
			t.Fatalf("Accept(max int) = (%d, %v), want (-1, EBADF)", acceptedFD, err)
		}
	})

	t.Run("unlistened socket returns EBADF", func(t *testing.T) {
		fd, err := syscall.Socket(2, syscall.SOCK_STREAM, 0)
		if err != nil {
			t.Fatalf("create unlistened socket: %v", err)
		}
		defer syscall.Close(fd)
		if acceptedFD, _, err := syscall.Accept(fd); acceptedFD != -1 || !errors.Is(err, syscall.EBADF) {
			t.Fatalf("Accept(unlistened socket) = (%d, %v), want (-1, EBADF)", acceptedFD, err)
		}
	})

	for _, fd := range []int{syscall.Stdin, syscall.Stdout, syscall.Stderr} {
		t.Run("standard stream "+strconv.Itoa(fd)+" is not a listener", func(t *testing.T) {
			if acceptedFD, _, err := syscall.Accept(fd); acceptedFD != -1 || !errors.Is(err, syscall.EINVAL) {
				t.Fatalf("Accept(stdio fd %d) = (%d, %v), want (-1, EINVAL)", fd, acceptedFD, err)
			}
		})
	}

	t.Run("closed listener returns EBADF", func(t *testing.T) {
		listenerFD, _ := rusticatedEdgeListenTCP(t)
		if err := syscall.Close(listenerFD); err != nil {
			t.Fatalf("close listener: %v", err)
		}
		if fd, _, err := syscall.Accept(listenerFD); fd != -1 || !errors.Is(err, syscall.EBADF) {
			t.Fatalf("Accept(closed listener) = (%d, %v), want (-1, EBADF)", fd, err)
		}
	})

	t.Run("connected stream is not a listener", func(t *testing.T) {
		_, clientFD, _ := rusticatedEdgeTCPPair(t)
		if fd, _, err := syscall.Accept(clientFD); fd != -1 || !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("Accept(connected socket) = (%d, %v), want (-1, EINVAL)", fd, err)
		}
	})

	t.Run("UDP socket is not an accept listener", func(t *testing.T) {
		fd, err := syscall.Socket(2, syscall.SOCK_DGRAM, 0)
		if err != nil {
			t.Fatalf("create UDP socket: %v", err)
		}
		defer syscall.Close(fd)
		if err := syscall.Bind(fd, syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
			t.Fatalf("bind UDP socket: %v", err)
		}
		if err := syscall.Listen(fd, 1); err != nil {
			t.Fatalf("listen on UDP socket: %v", err)
		}
		if acceptedFD, _, err := syscall.Accept(fd); acceptedFD != -1 || !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("Accept(UDP socket) = (%d, %v), want (-1, EINVAL)", acceptedFD, err)
		}
	})
}

func TestRusticatedABIEdgeNetLookup(t *testing.T) {
	for _, test := range []struct {
		name string
		ip   string
	}{
		{"ipv4 loopback", "127.0.0.1"},
		{"ipv4 loopback variant", "127.0.0.2"},
		{"ipv4 loopback high octet", "127.1.2.3"},
		{"ipv4 documentation address", "192.0.2.1"},
		{"ipv4 unspecified", "0.0.0.0"},
		{"ipv6 loopback", "::1"},
		{"expanded ipv6 loopback", "0:0:0:0:0:0:0:1"},
		{"ipv6 documentation address", "2001:db8::1"},
		{"ipv6 unspecified", "::"},
		{"ipv4-mapped ipv6", "::ffff:192.0.2.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			addresses, err := syscall.NetLookup(test.ip)
			if err != nil {
				t.Fatalf("NetLookup(%q): %v", test.ip, err)
			}
			want := net.ParseIP(test.ip)
			if want == nil {
				t.Fatalf("test address %q is invalid", test.ip)
			}
			found := false
			for _, address := range addresses {
				if len(address) != net.IPv4len && len(address) != net.IPv6len {
					t.Errorf("lookup returned address with invalid length %d", len(address))
				}
				if net.IP(address).Equal(want) {
					found = true
				}
			}
			if !found {
				t.Fatalf("NetLookup(%q) = %v, want the literal address", test.ip, addresses)
			}
		})
	}

	t.Run("empty name reports a resolver error", func(t *testing.T) {
		if addresses, err := syscall.NetLookup(""); err == nil {
			t.Fatalf("NetLookup(empty) = %v, nil error; want resolver error", addresses)
		}
	})
}

func rusticatedEdgeSelfSignedCert(t *testing.T) []byte {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		DNSNames:              []string{"localhost"},
		NotBefore:             time.Unix(1600000000, 0),
		NotAfter:              time.Unix(2200000000, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatalf("create self-signed test certificate: %v", err)
	}
	return der
}

func rusticatedEdgeIssuedCertChain(t *testing.T, expired bool) [][]byte {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	rootKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x53}, ed25519.SeedSize))
	rootPublicKey := rootKey.Public().(ed25519.PublicKey)
	rootTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "Rusticated ABI Test Root"},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, rootPublicKey, rootKey)
	if err != nil {
		t.Fatalf("create test root certificate: %v", err)
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatalf("parse test root certificate: %v", err)
	}

	leafKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x64}, ed25519.SeedSize))
	leafPublicKey := leafKey.Public().(ed25519.PublicKey)
	leafNotBefore := now.Add(-24 * time.Hour)
	leafNotAfter := now.Add(24 * time.Hour)
	if expired {
		leafNotBefore = now.Add(-48 * time.Hour)
		leafNotAfter = now.Add(-24 * time.Hour)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(3),
		Subject:               pkix.Name{CommonName: "localhost"},
		DNSNames:              []string{"localhost"},
		NotBefore:             leafNotBefore,
		NotAfter:              leafNotAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, rootCert, leafPublicKey, rootKey)
	if err != nil {
		t.Fatalf("create test leaf certificate: %v", err)
	}
	return [][]byte{leafDER, rootDER}
}

func TestRusticatedABIEdgeNetCertVerify(t *testing.T) {
	for _, test := range []struct {
		name  string
		chain [][]byte
	}{
		{"nil chain", nil},
		{"empty chain", [][]byte{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if verified, err := syscall.NetCertVerify("localhost", test.chain); verified != 0 || !errors.Is(err, syscall.EINVAL) {
				t.Fatalf("NetCertVerify(empty chain) = (%d, %v), want (0, EINVAL)", verified, err)
			}
		})
	}

	cert := rusticatedEdgeSelfSignedCert(t)
	for _, name := range []string{"localhost", "wrong-name.invalid", ""} {
		t.Run("untrusted certificate name "+name, func(t *testing.T) {
			verified, err := syscall.NetCertVerify(name, [][]byte{cert})
			if verified != 0 || err != nil {
				t.Fatalf("NetCertVerify(%q, self-signed cert) = (%d, %v), want untrusted (0, nil)", name, verified, err)
			}
		})
	}

	t.Run("multiple valid DER entries remain untrusted", func(t *testing.T) {
		verified, err := syscall.NetCertVerify("localhost", [][]byte{cert, cert})
		if verified != 0 || err != nil {
			t.Fatalf("NetCertVerify(duplicate self-signed chain) = (%d, %v), want untrusted (0, nil)", verified, err)
		}
	})

	t.Run("zero-length DER entry is rejected", func(t *testing.T) {
		if verified, err := syscall.NetCertVerify("localhost", [][]byte{{}}); verified != 0 || !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("NetCertVerify(empty DER entry) = (%d, %v), want (0, EINVAL)", verified, err)
		}
	})

	t.Run("empty later DER entry is rejected after a valid certificate", func(t *testing.T) {
		if verified, err := syscall.NetCertVerify("localhost", [][]byte{cert, {}}); verified != 0 || !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("NetCertVerify(valid plus empty DER) = (%d, %v), want (0, EINVAL)", verified, err)
		}
	})

	chain := rusticatedEdgeIssuedCertChain(t, false)
	t.Run("signed leaf and root chain is parsed but untrusted", func(t *testing.T) {
		verified, err := syscall.NetCertVerify("localhost", chain)
		if verified != 0 || err != nil {
			t.Fatalf("NetCertVerify(signed test chain) = (%d, %v), want untrusted (0, nil)", verified, err)
		}
	})

	t.Run("signed chain still rejects an absent issuer", func(t *testing.T) {
		verified, err := syscall.NetCertVerify("localhost", chain[:1])
		if verified != 0 || err != nil {
			t.Fatalf("NetCertVerify(leaf without issuer) = (%d, %v), want untrusted (0, nil)", verified, err)
		}
	})

	t.Run("signed chain rejects a name mismatch", func(t *testing.T) {
		verified, err := syscall.NetCertVerify("wrong-name.invalid", chain)
		if verified != 0 || err != nil {
			t.Fatalf("NetCertVerify(signed chain with wrong name) = (%d, %v), want untrusted (0, nil)", verified, err)
		}
	})

	t.Run("expired signed certificate is untrusted", func(t *testing.T) {
		verified, err := syscall.NetCertVerify("localhost", rusticatedEdgeIssuedCertChain(t, true))
		if verified != 0 || err != nil {
			t.Fatalf("NetCertVerify(expired signed chain) = (%d, %v), want untrusted (0, nil)", verified, err)
		}
	})
}

func rusticatedEdgeReadAll(t *testing.T, fd int, want []byte) {
	t.Helper()
	got := make([]byte, len(want))
	for offset := 0; offset < len(got); {
		n, err := syscall.Read(fd, got[offset:])
		if err != nil {
			t.Fatalf("read at offset %d: %v", offset, err)
		}
		if n == 0 {
			t.Fatalf("read stopped at %d of %d bytes", offset, len(got))
		}
		offset += n
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("read payload = %v, want %v", got, want)
	}
}

func rusticatedEdgeWriteAll(t *testing.T, fd int, data []byte) {
	t.Helper()
	for offset := 0; offset < len(data); {
		n, err := syscall.Write(fd, data[offset:])
		if err != nil {
			t.Fatalf("write at offset %d: %v", offset, err)
		}
		if n == 0 {
			t.Fatalf("write stopped at %d of %d bytes", offset, len(data))
		}
		offset += n
	}
}

func TestRusticatedABIEdgeSocketReadWrite(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"single byte", []byte{0xff}},
		{"binary payload", []byte{0, 1, 0x7f, 0x80, 0xfe, 0xff}},
		{"larger payload", bytes.Repeat([]byte("wasm-abi"), 1024)},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, clientFD, acceptedFD := rusticatedEdgeTCPPair(t)
			rusticatedEdgeWriteAll(t, clientFD, test.data)
			rusticatedEdgeReadAll(t, acceptedFD, test.data)
		})
	}

	t.Run("bidirectional writes use the accepted handle", func(t *testing.T) {
		_, clientFD, acceptedFD := rusticatedEdgeTCPPair(t)
		fromClient := []byte{0, 0xff, 3}
		fromServer := []byte{4, 0x80, 0}
		rusticatedEdgeWriteAll(t, clientFD, fromClient)
		rusticatedEdgeReadAll(t, acceptedFD, fromClient)
		rusticatedEdgeWriteAll(t, acceptedFD, fromServer)
		rusticatedEdgeReadAll(t, clientFD, fromServer)
	})

	t.Run("zero-length socket I/O is a no-op", func(t *testing.T) {
		_, clientFD, acceptedFD := rusticatedEdgeTCPPair(t)
		if n, err := syscall.Write(clientFD, nil); n != 0 || err != nil {
			t.Fatalf("zero-length Write = (%d, %v), want (0, nil)", n, err)
		}
		if n, err := syscall.Read(acceptedFD, nil); n != 0 || err != nil {
			t.Fatalf("zero-length Read = (%d, %v), want (0, nil)", n, err)
		}
	})

	t.Run("zero-length I/O bypasses invalid descriptor lookup", func(t *testing.T) {
		if n, err := syscall.Read(-1, nil); n != 0 || err != nil {
			t.Errorf("Read(invalid fd, empty buffer) = (%d, %v), want (0, nil)", n, err)
		}
		if n, err := syscall.Write(-1, nil); n != 0 || err != nil {
			t.Errorf("Write(invalid fd, empty buffer) = (%d, %v), want (0, nil)", n, err)
		}
	})

	t.Run("one-byte read leaves remaining stream data available", func(t *testing.T) {
		_, clientFD, acceptedFD := rusticatedEdgeTCPPair(t)
		want := []byte{0, 0x80, 0xff}
		rusticatedEdgeWriteAll(t, clientFD, want)
		first := []byte{0}
		if n, err := syscall.Read(acceptedFD, first); n != 1 || err != nil || first[0] != want[0] {
			t.Fatalf("first bounded Read = (%d, %v, %v), want one leading byte", n, err, first)
		}
		rusticatedEdgeReadAll(t, acceptedFD, want[1:])
	})

	t.Run("oversized read buffer returns only available bytes", func(t *testing.T) {
		_, clientFD, acceptedFD := rusticatedEdgeTCPPair(t)
		want := []byte{0x11, 0x22, 0x33}
		rusticatedEdgeWriteAll(t, clientFD, want)
		buffer := bytes.Repeat([]byte{0xaa}, len(want)+5)
		n, err := syscall.Read(acceptedFD, buffer)
		if err != nil || n != len(want) {
			t.Fatalf("oversized Read = (%d, %v), want (%d, nil)", n, err, len(want))
		}
		if !bytes.Equal(buffer[:n], want) {
			t.Fatalf("oversized Read payload = %v, want %v", buffer[:n], want)
		}
		if !bytes.Equal(buffer[n:], bytes.Repeat([]byte{0xaa}, len(buffer)-n)) {
			t.Fatalf("oversized Read modified bytes beyond returned count: %v", buffer[n:])
		}
	})

	t.Run("peer close is observed as EOF", func(t *testing.T) {
		_, clientFD, acceptedFD := rusticatedEdgeTCPPair(t)
		if err := syscall.Close(clientFD); err != nil {
			t.Fatalf("close client socket: %v", err)
		}
		if n, err := syscall.Read(acceptedFD, make([]byte, 1)); n != 0 || err != nil {
			t.Fatalf("Read after peer close = (%d, %v), want EOF as (0, nil)", n, err)
		}
	})

	t.Run("closed socket descriptor rejects read and write", func(t *testing.T) {
		_, _, acceptedFD := rusticatedEdgeTCPPair(t)
		if err := syscall.Close(acceptedFD); err != nil {
			t.Fatalf("close accepted socket: %v", err)
		}
		if n, err := syscall.Read(acceptedFD, []byte{0}); n != 0 || !errors.Is(err, syscall.EBADF) {
			t.Errorf("Read(closed socket) = (%d, %v), want (0, EBADF)", n, err)
		}
		if n, err := syscall.Write(acceptedFD, []byte{1}); n != 0 || !errors.Is(err, syscall.EBADF) {
			t.Errorf("Write(closed socket) = (%d, %v), want (0, EBADF)", n, err)
		}
	})
}

func TestRusticatedABIEdgePipe(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"single byte", []byte{0xff}},
		{"binary data", []byte{0, 1, 0x7f, 0x80, 0xff}},
		{"larger data", bytes.Repeat([]byte("pipe"), 256)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var fds [2]int
			if err := syscall.Pipe(fds[:]); err != nil {
				t.Fatalf("create pipe: %v", err)
			}
			t.Cleanup(func() {
				_ = syscall.Close(fds[0])
				_ = syscall.Close(fds[1])
			})
			if fds[0] < 3 || fds[1] < 3 || fds[0] == fds[1] {
				t.Fatalf("pipe descriptors = %v, want two distinct allocated descriptors", fds)
			}
			rusticatedEdgeWriteAll(t, fds[1], test.data)
			rusticatedEdgeReadAll(t, fds[0], test.data)
		})
	}

	t.Run("zero-length pipe I/O is a no-op", func(t *testing.T) {
		var fds [2]int
		if err := syscall.Pipe(fds[:]); err != nil {
			t.Fatalf("create pipe: %v", err)
		}
		defer syscall.Close(fds[0])
		defer syscall.Close(fds[1])
		if n, err := syscall.Write(fds[1], nil); n != 0 || err != nil {
			t.Fatalf("zero-length pipe Write = (%d, %v), want (0, nil)", n, err)
		}
		if n, err := syscall.Read(fds[0], nil); n != 0 || err != nil {
			t.Fatalf("zero-length pipe Read = (%d, %v), want (0, nil)", n, err)
		}
	})

	t.Run("closed pipe endpoint rejects I/O", func(t *testing.T) {
		var fds [2]int
		if err := syscall.Pipe(fds[:]); err != nil {
			t.Fatalf("create pipe: %v", err)
		}
		defer syscall.Close(fds[0])
		if err := syscall.Close(fds[1]); err != nil {
			t.Fatalf("close pipe writer: %v", err)
		}
		if n, err := syscall.Write(fds[1], []byte{1}); n != 0 || !errors.Is(err, syscall.EBADF) {
			t.Fatalf("Write(closed pipe) = (%d, %v), want (0, EBADF)", n, err)
		}
	})

	t.Run("closing the writer produces EOF at the reader", func(t *testing.T) {
		var fds [2]int
		if err := syscall.Pipe(fds[:]); err != nil {
			t.Fatalf("create pipe: %v", err)
		}
		defer syscall.Close(fds[0])
		if err := syscall.Close(fds[1]); err != nil {
			t.Fatalf("close pipe writer: %v", err)
		}
		if n, err := syscall.Read(fds[0], []byte{0}); n != 0 || err != nil {
			t.Fatalf("Read after writer close = (%d, %v), want EOF as (0, nil)", n, err)
		}
	})

	t.Run("short reads preserve the remaining pipe bytes", func(t *testing.T) {
		var fds [2]int
		if err := syscall.Pipe(fds[:]); err != nil {
			t.Fatalf("create pipe: %v", err)
		}
		defer syscall.Close(fds[0])
		defer syscall.Close(fds[1])
		want := []byte{0, 0x7f, 0xff}
		rusticatedEdgeWriteAll(t, fds[1], want)
		first := []byte{0}
		if n, err := syscall.Read(fds[0], first); n != 1 || err != nil || first[0] != want[0] {
			t.Fatalf("first pipe Read = (%d, %v, %v), want one leading byte", n, err, first)
		}
		rusticatedEdgeReadAll(t, fds[0], want[1:])
	})

	t.Run("oversized pipe read leaves buffer tail untouched", func(t *testing.T) {
		var fds [2]int
		if err := syscall.Pipe(fds[:]); err != nil {
			t.Fatalf("create pipe: %v", err)
		}
		defer syscall.Close(fds[0])
		defer syscall.Close(fds[1])
		want := []byte{0x12, 0x34}
		rusticatedEdgeWriteAll(t, fds[1], want)
		buffer := bytes.Repeat([]byte{0xaa}, len(want)+4)
		n, err := syscall.Read(fds[0], buffer)
		if err != nil || n != len(want) {
			t.Fatalf("oversized pipe Read = (%d, %v), want (%d, nil)", n, err, len(want))
		}
		if !bytes.Equal(buffer[:n], want) || !bytes.Equal(buffer[n:], bytes.Repeat([]byte{0xaa}, len(buffer)-n)) {
			t.Fatalf("oversized pipe Read buffer = %v, want prefix %v and untouched tail", buffer, want)
		}
	})

	t.Run("closing the writer preserves buffered bytes before EOF", func(t *testing.T) {
		var fds [2]int
		if err := syscall.Pipe(fds[:]); err != nil {
			t.Fatalf("create pipe: %v", err)
		}
		defer syscall.Close(fds[0])
		want := []byte{0, 0x80, 0xff}
		rusticatedEdgeWriteAll(t, fds[1], want)
		if err := syscall.Close(fds[1]); err != nil {
			t.Fatalf("close pipe writer: %v", err)
		}
		rusticatedEdgeReadAll(t, fds[0], want)
		if n, err := syscall.Read(fds[0], []byte{0}); n != 0 || err != nil {
			t.Fatalf("Read after draining closed pipe = (%d, %v), want EOF as (0, nil)", n, err)
		}
	})

	t.Run("independent pipes do not share buffered data", func(t *testing.T) {
		var first, second [2]int
		if err := syscall.Pipe(first[:]); err != nil {
			t.Fatalf("create first pipe: %v", err)
		}
		defer syscall.Close(first[0])
		defer syscall.Close(first[1])
		if err := syscall.Pipe(second[:]); err != nil {
			t.Fatalf("create second pipe: %v", err)
		}
		defer syscall.Close(second[0])
		defer syscall.Close(second[1])

		firstPayload := []byte{0x11}
		secondPayload := []byte{0x22, 0x33}
		rusticatedEdgeWriteAll(t, first[1], firstPayload)
		rusticatedEdgeWriteAll(t, second[1], secondPayload)
		rusticatedEdgeReadAll(t, first[0], firstPayload)
		rusticatedEdgeReadAll(t, second[0], secondPayload)
	})
}

func rusticatedEdgeExitCommand(t *testing.T, exitCode int) (string, []string) {
	t.Helper()
	info := syscall.GetPlatformInfo()
	if info == nil {
		t.Fatal("platform information is unavailable")
	}
	switch info.OSKind {
	case 1:
		return "cmd.exe", []string{"cmd.exe", "/C", "exit", strconv.Itoa(exitCode)}
	case 2, 3:
		return "/bin/sh", []string{"sh", "-c", "exit " + strconv.Itoa(exitCode)}
	default:
		t.Fatalf("no process command is defined for OS kind %d", info.OSKind)
		return "", nil
	}
}

func TestRusticatedABIEdgeProcessWaitAndSignals(t *testing.T) {
	for _, exitCode := range []int{0, 1, 29, 255} {
		t.Run("direct spawn and wait exit "+strconv.Itoa(exitCode), func(t *testing.T) {
			program, argv := rusticatedEdgeExitCommand(t, exitCode)
			pid, handle, err := syscall.StartProcess(program, argv, &syscall.ProcAttr{
				Files: []uintptr{0, 1, 2},
			})
			if err != nil {
				t.Fatalf("StartProcess: %v", err)
			}
			if pid <= 0 || handle != uintptr(pid) {
				t.Fatalf("StartProcess returned pid=%d handle=%d, want matching non-zero child handle", pid, handle)
			}
			var status syscall.WaitStatus
			waited, err := syscall.Wait4(pid, &status, 0, nil)
			if err != nil {
				t.Fatalf("Wait4(%d): %v", pid, err)
			}
			if waited != pid {
				t.Fatalf("Wait4 returned pid %d, want %d", waited, pid)
			}
			if !status.Exited() || status.ExitStatus() != exitCode || status.Signaled() {
				t.Fatalf("WaitStatus = %#v (exit=%d signaled=%t), want exit %d", status, status.ExitStatus(), status.Signaled(), exitCode)
			}
		})
	}

	t.Run("empty executable is rejected", func(t *testing.T) {
		if pid, _, err := syscall.StartProcess("", []string{""}, nil); pid != 0 || !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("StartProcess(empty executable) = (%d, %v), want (0, EINVAL)", pid, err)
		}
	})

	t.Run("invalid stdio descriptor is rejected before spawn", func(t *testing.T) {
		program, argv := rusticatedEdgeExitCommand(t, 0)
		if pid, _, err := syscall.StartProcess(program, argv, &syscall.ProcAttr{
			Files: []uintptr{uintptr(1 << 30), 1, 2},
		}); pid != 0 || !errors.Is(err, syscall.EBADF) {
			t.Fatalf("StartProcess(invalid stdio) = (%d, %v), want (0, EBADF)", pid, err)
		}
	})

	t.Run("wait for unknown process handle returns EBADF", func(t *testing.T) {
		status := syscall.WaitStatus(77)
		if pid, err := syscall.Wait4(-1, &status, 0, nil); pid != 0 || !errors.Is(err, syscall.EBADF) {
			t.Fatalf("Wait4(unknown handle) = (%d, %v), want (0, EBADF)", pid, err)
		}
		if status != 77 {
			t.Fatalf("failed Wait4 changed status to %d, want 77", status)
		}
	})

	t.Run("wait may omit the status destination", func(t *testing.T) {
		program, argv := rusticatedEdgeExitCommand(t, 0)
		pid, _, err := syscall.StartProcess(program, argv, &syscall.ProcAttr{
			Files: []uintptr{0, 1, 2},
		})
		if err != nil {
			t.Fatalf("StartProcess: %v", err)
		}
		if waited, err := syscall.Wait4(pid, nil, 0, nil); waited != pid || err != nil {
			t.Fatalf("Wait4(%d, nil status) = (%d, %v), want (%d, nil)", pid, waited, err, pid)
		}
	})

	t.Run("null standard streams still permit child exit and wait", func(t *testing.T) {
		program, argv := rusticatedEdgeExitCommand(t, 0)
		pid, _, err := syscall.StartProcess(program, argv, &syscall.ProcAttr{
			Files: []uintptr{^uintptr(0), ^uintptr(0), ^uintptr(0)},
		})
		if err != nil {
			t.Fatalf("StartProcess with null stdio: %v", err)
		}
		var status syscall.WaitStatus
		if waited, err := syscall.Wait4(pid, &status, 0, nil); waited != pid || err != nil {
			t.Fatalf("Wait4(%d) = (%d, %v), want (%d, nil)", pid, waited, err, pid)
		}
		if !status.Exited() || status.ExitStatus() != 0 {
			t.Fatalf("null-stdio child status = %d, want exit 0", status.ExitStatus())
		}
	})

	t.Run("signal to another guest pid returns ESRCH", func(t *testing.T) {
		if err := syscall.Kill(syscall.Getpid()+1, syscall.SIGTERM); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("Kill(nonexistent pid) = %v, want ESRCH", err)
		}
		if got := syscall.SIGTERM.String(); got != "terminated" {
			t.Fatalf("SIGTERM.String() = %q, want terminated", got)
		}
	})

	t.Run("missing executable returns an error", func(t *testing.T) {
		missing := filepath.Join(os.TempDir(), "mohabbat-abi-missing-executable")
		if info := syscall.GetPlatformInfo(); info != nil && info.OSKind != 1 {
			missing = "/mohabbat-abi-missing-executable"
		} else if info != nil {
			missing += ".exe"
		}
		if pid, _, err := syscall.StartProcess(missing, []string{missing}, nil); pid != 0 || err == nil {
			t.Fatalf("StartProcess(missing executable) = (%d, %v), want an error", pid, err)
		}
	})
}

