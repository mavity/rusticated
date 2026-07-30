//go:build wasip1

// net_rusticated.go overrides net/net_fake.go's socket() function with a real
// TCP implementation that calls the host ABI (env.net_open / env.net_accept).
// It is added as a new overlay file via prebuild.go:generateGoOverlay.

package net

import (
	"context"
	"os"
	"syscall"
)

// rusticatedSocket replaces the body of socket() in net_fake.go.
//
// When raddr == nil the call is a listen (server). When raddr != nil it is a
// connect (client). laddr is always the local-side address; for a client it
// may be nil (let the OS pick the ephemeral port).
//
// The function creates a real fd via syscall.Socket, wires up the host TCP
// connection via syscall.Connect / syscall.Bind+Listen, and returns a *netFD
// whose fakeNetFD field is nil. With fakeNetFD==nil, fd_fake.go routes all
// I/O through poll.FD which calls syscall.Read / syscall.Write — those
// functions are implemented in our fs_rusticated.go overlay and reach the host
// via the env.read / env.write ABI calls.
func rusticatedSocket(
	ctx context.Context,
	netStr string,
	family, sotype, proto int,
	ipv6only bool,
	laddr, raddr sockaddr,
	ctrlCtxFn func(context.Context, string, string, syscall.RawConn) error,
) (*netFD, error) {
	if sotype != syscall.SOCK_STREAM {
		return nil, os.NewSyscallError("socket", syscall.ENOTSUP)
	}

	sysfd, err := syscall.Socket(family, sotype, proto)
	if err != nil {
		return nil, os.NewSyscallError("socket", err)
	}

	if raddr != nil {
		// ── Connect ──────────────────────────────────────────────────────────
		sa, saErr := raddr.sockaddr(family)
		if saErr != nil {
			syscall.Close(sysfd)
			return nil, saErr
		}
		if connErr := syscall.Connect(sysfd, sa); connErr != nil {
			syscall.Close(sysfd)
			return nil, os.NewSyscallError("connect", connErr)
		}
	} else {
		// ── Listen ───────────────────────────────────────────────────────────
		var sa syscall.Sockaddr
		if laddr != nil {
			var saErr error
			sa, saErr = laddr.sockaddr(family)
			if saErr != nil {
				syscall.Close(sysfd)
				return nil, saErr
			}
		} else {
			if family == syscall.AF_INET6 {
				sa = syscall.SockaddrInet6{}
			} else {
				sa = syscall.SockaddrInet4{}
			}
		}
		if bindErr := syscall.Bind(sysfd, sa); bindErr != nil {
			syscall.Close(sysfd)
			return nil, os.NewSyscallError("bind", bindErr)
		}
		if listenErr := syscall.Listen(sysfd, syscall.SOMAXCONN); listenErr != nil {
			syscall.Close(sysfd)
			return nil, os.NewSyscallError("listen", listenErr)
		}
	}

	fd := newFD(netStr, sysfd)
	if initErr := fd.init(); initErr != nil {
		fd.Close()
		return nil, initErr
	}

	// sockaddrToAddr converts a sockaddr interface to an Addr, returning nil
	// (not a typed-nil) if the argument itself is a nil interface or if its
	// underlying value is a nil pointer. A typed-nil stored as Addr passes the
	// != nil interface check but causes a nil-pointer dereference in selfConnect.
	sockaddrToAddr := func(sa sockaddr) Addr {
		if sa == nil {
			return nil
		}
		switch v := sa.(type) {
		case *TCPAddr:
			if v == nil {
				return nil
			}
			return v
		case *UDPAddr:
			if v == nil {
				return nil
			}
			return v
		case *IPAddr:
			if v == nil {
				return nil
			}
			return v
		default:
			return sa
		}
	}

	fd.setAddr(sockaddrToAddr(laddr), sockaddrToAddr(raddr))
	return fd, nil
}

func rusticatedLookupHost(ctx context.Context, host string) ([]string, error) {
	ips, err := syscall.NetLookup(host)
	if err != nil {
		return nil, err
	}
	var addrs []string
	for _, ip := range ips {
		addrs = append(addrs, IP(ip).String())
	}
	return addrs, nil
}

func rusticatedLookupIP(ctx context.Context, network, host string) ([]IPAddr, error) {
	ips, err := syscall.NetLookup(host)
	if err != nil {
		return nil, err
	}
	var addrs []IPAddr
	for _, ip := range ips {
		addrs = append(addrs, IPAddr{IP: IP(ip)})
	}
	return addrs, nil
}

func rusticatedLookupPort(ctx context.Context, network, service string) (int, error) {
	if service == "http" {
		return 80, nil
	}
	if service == "https" {
		return 443, nil
	}
	return DefaultResolver.lookupPort(ctx, network, service)
}

// init overrides the default DNS resolver to route through TCP to 8.8.8.8:53.
//
// The Go pure-Go DNS resolver reads /etc/resolv.conf. In the WASM sandbox that
// file does not exist, so the resolver falls back to defaultNS = ["127.0.0.1:53"]
// which also does not work. We override Resolver.Dial to ignore the provided
// server address and always connect to Google's public DNS over TCP instead.
// 8.8.8.8 is an IP address so no prior DNS lookup is needed to reach it.
func init() {
	DefaultResolver = &Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (Conn, error) {
			var d Dialer
			return d.DialContext(ctx, "tcp", "8.8.8.8:53")
		},
	}
}
