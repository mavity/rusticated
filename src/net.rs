//! Networking primitives for the rusticated standard library.
//!
//! Provides `TcpStream`, `TcpListener`, and address types.
//!
//! WARNING: This implementation is strictly async and uses the owned-buffer
//! model for I/O operations.

use crate::io;
use crate::traits::{AsyncRead, AsyncWrite};
use crate::vec::Vec;

/// An IPv4 address.
#[derive(Copy, Clone, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
pub struct Ipv4Addr {
    octets: [u8; 4],
}

impl Ipv4Addr {
    /// Creates a new IPv4 address from four octets.
    pub const fn new(a: u8, b: u8, c: u8, d: u8) -> Ipv4Addr {
        Ipv4Addr { octets: [a, b, c, d] }
    }

    /// Returns the four eight-bit integers that make up this address.
    pub const fn octets(&self) -> [u8; 4] {
        self.octets
    }
}

/// An IPv6 address.
#[derive(Copy, Clone, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
pub struct Ipv6Addr {
    segments: [u16; 8],
}

impl Ipv6Addr {
    /// Creates a new IPv6 address from eight 16-bit segments.
    pub const fn new(a: u16, b: u16, c: u16, d: u16, e: u16, f: u16, g: u16, h: u16) -> Ipv6Addr {
        Ipv6Addr {
            segments: [a, b, c, d, e, f, g, h],
        }
    }

    /// Returns the eight 16-bit integers that make up this address.
    pub const fn segments(&self) -> [u16; 8] {
        self.segments
    }
}

/// An IP address, either IPv4 or IPv6.
#[derive(Copy, Clone, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
pub enum IpAddr {
    /// An IPv4 address.
    V4(Ipv4Addr),
    /// An IPv6 address.
    V6(Ipv6Addr),
}

/// An IPv4 socket address (IP address and port number).
#[derive(Copy, Clone, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
pub struct SocketAddrV4 {
    ip: Ipv4Addr,
    port: u16,
}

impl SocketAddrV4 {
    /// Creates a new socket address from an IPv4 address and a port number.
    pub const fn new(ip: Ipv4Addr, port: u16) -> SocketAddrV4 {
        SocketAddrV4 { ip, port }
    }

    /// Returns the IP address associated with this socket address.
    pub const fn ip(&self) -> &Ipv4Addr {
        &self.ip
    }

    /// Returns the port number associated with this socket address.
    pub const fn port(&self) -> u16 {
        self.port
    }
}

/// An IPv6 socket address (IP address and port number).
#[derive(Copy, Clone, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
pub struct SocketAddrV6 {
    ip: Ipv6Addr,
    port: u16,
}

impl SocketAddrV6 {
    /// Creates a new socket address from an IPv6 address and a port number.
    pub const fn new(ip: Ipv6Addr, port: u16) -> SocketAddrV6 {
        SocketAddrV6 { ip, port }
    }

    /// Returns the IP address associated with this socket address.
    pub const fn ip(&self) -> &Ipv6Addr {
        &self.ip
    }

    /// Returns the port number associated with this socket address.
    pub const fn port(&self) -> u16 {
        self.port
    }
}

/// A socket address, either IPv4 or IPv6.
#[derive(Copy, Clone, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
pub enum SocketAddr {
    /// An IPv4 socket address.
    V4(SocketAddrV4),
    /// An IPv6 socket address.
    V6(SocketAddrV6),
}

impl SocketAddr {
    /// Returns the port number associated with this socket address.
    pub const fn port(&self) -> u16 {
        match *self {
            SocketAddr::V4(ref a) => a.port(),
            SocketAddr::V6(ref a) => a.port(),
        }
    }
}

/// A TCP stream between a local and a remote socket.
pub struct TcpStream {
    pub(crate) handle: u64,
}

impl TcpStream {
    /// Opens a TCP connection to a remote host.
    pub async fn connect<A: ToSocketAddrs>(_addr: A) -> io::Result<TcpStream> {
        let addrs = _addr.to_socket_addrs()?;
        let mut last_err = None;

        for addr in addrs {
            match Self::connect_single(addr).await {
                Ok(s) => return Ok(s),
                Err(e) => last_err = Some(e),
            }
        }

        Err(last_err.unwrap_or_else(|| io::Error::other("no addresses provided")))
    }

    async fn connect_single(_addr: SocketAddr) -> io::Result<TcpStream> {
        #[cfg(target_family = "wasm")]
        {
            let addr_str = crate::alloc::format!("{:?}", _addr);
            let addr_bytes = addr_str.into_bytes();
            let (err, handle, _, _) =
                crate::rt::wasm::OverlappedBufferFuture::new(addr_bytes, move |ov, ptr, len| {
                    unsafe { crate::abi::imports::net_open(ov, ptr, len, _addr.port(), 1) }; // flag 1 for connect
                })
                .await;

            if err != 0 {
                return Err(io::Error::from_raw_os_error(err as i32));
            }
            Ok(TcpStream { handle })
        }
        #[cfg(windows)]
        {
            crate::rt::windows::TcpConnect::new(_addr).await
        }
        #[cfg(any(target_os = "linux", rusticated_linux))]
        {
            crate::rt::linux_op::TcpConnect::new(_addr).await
        }
        #[cfg(any(
            target_os = "macos",
            target_os = "freebsd",
            target_os = "openbsd",
            target_os = "netbsd"
        ))]
        {
            crate::rt::bsd::TcpConnect::new(_addr).await
        }
    }
}

impl AsyncRead for TcpStream {
    async fn read(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
        #[cfg(target_family = "wasm")]
        {
            let handle = self.handle;
            let (err, read_len, _, buf) =
                crate::rt::wasm::OverlappedBufferFuture::new(buf, move |ov, ptr, len| {
                    unsafe { crate::abi::imports::read(ov, handle, ptr, len) };
                })
                .await;
            if err != 0 {
                return (Err(io::Error::from_raw_os_error(err as i32)), buf);
            }
            (Ok(read_len as usize), buf)
        }
        #[cfg(windows)]
        {
            crate::rt::windows::OverlappedRecv::new(self.handle, buf).await
        }
        #[cfg(any(target_os = "linux", rusticated_linux))]
        {
            crate::rt::linux_op::LinuxOpFuture::read(self.handle as i32, buf).await
        }
        #[cfg(any(
            target_os = "macos",
            target_os = "freebsd",
            target_os = "openbsd",
            target_os = "netbsd"
        ))]
        {
            crate::rt::bsd::OverlappedRecv::new(self.handle, buf).await
        }
    }
}

impl AsyncWrite for TcpStream {
    async fn write(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
        #[cfg(target_family = "wasm")]
        {
            let handle = self.handle;
            let (err, written, _, buf) =
                crate::rt::wasm::OverlappedBufferFuture::new(buf, move |ov, ptr, len| {
                    unsafe { crate::abi::imports::write(ov, handle, ptr, len) };
                })
                .await;
            if err != 0 {
                return (Err(io::Error::from_raw_os_error(err as i32)), buf);
            }
            (Ok(written as usize), buf)
        }
        #[cfg(windows)]
        {
            crate::rt::windows::OverlappedSend::new(self.handle, buf).await
        }
        #[cfg(any(target_os = "linux", rusticated_linux))]
        {
            crate::rt::linux_op::LinuxOpFuture::write(self.handle as i32, buf).await
        }
        #[cfg(any(
            target_os = "macos",
            target_os = "freebsd",
            target_os = "openbsd",
            target_os = "netbsd"
        ))]
        {
            crate::rt::bsd::OverlappedSend::new(self.handle, buf).await
        }
    }

    async fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

impl Drop for TcpStream {
    fn drop(&mut self) {
        #[cfg(target_family = "wasm")]
        unsafe {
            crate::abi::imports::handle_close(self.handle);
        }
        #[cfg(windows)]
        unsafe {
            #[link(name = "ws2_32", kind = "raw-dylib")]
            unsafe extern "system" {
                fn closesocket(s: usize) -> i32;
            }
            closesocket(self.handle as usize);
        }
        #[cfg(any(unix, rusticated_linux))]
        {
            #[cfg(any(target_os = "linux", rusticated_linux))]
            crate::syscall!(crate::os::linux::syscall::nr::CLOSE, self.handle as usize);
            #[cfg(not(any(target_os = "linux", rusticated_linux)))]
            unsafe {
                unsafe extern "C" {
                    fn close(fd: i32) -> i32;
                }
                close(self.handle as i32);
            }
        }
    }
}

/// A TCP socket server, listening for connections.
pub struct TcpListener {
    pub(crate) handle: u64,
}

impl TcpListener {
    /// Creates a new `TcpListener` which will be bound to the specified address.
    pub async fn bind<A: ToSocketAddrs>(_addr: A) -> io::Result<TcpListener> {
        let addrs = _addr.to_socket_addrs()?;
        let mut last_err = None;

        for addr in addrs {
            match Self::bind_single(addr).await {
                Ok(l) => return Ok(l),
                Err(e) => last_err = Some(e),
            }
        }

        Err(last_err.unwrap_or_else(|| io::Error::other("no addresses provided")))
    }

    async fn bind_single(_addr: SocketAddr) -> io::Result<TcpListener> {
        #[cfg(target_family = "wasm")]
        {
            let addr_str = crate::alloc::format!("{:?}", _addr);
            let addr_bytes = addr_str.into_bytes();
            let (err, handle, _, _) =
                crate::rt::wasm::OverlappedBufferFuture::new(addr_bytes, move |ov, ptr, len| {
                    unsafe { crate::abi::imports::net_open(ov, ptr, len, _addr.port(), 0) }; // flag 0 for listen
                })
                .await;

            if err != 0 {
                return Err(io::Error::from_raw_os_error(err as i32));
            }
            Ok(TcpListener { handle })
        }
        #[cfg(windows)]
        {
            crate::rt::windows::TcpListenerBind::new(_addr).await
        }
        #[cfg(any(target_os = "linux", rusticated_linux))]
        {
            crate::rt::linux_op::TcpListenerBind::new(_addr).await
        }
        #[cfg(any(
            target_os = "macos",
            target_os = "freebsd",
            target_os = "openbsd",
            target_os = "netbsd"
        ))]
        {
            crate::rt::bsd::TcpListenerBind::new(_addr).await
        }
    }

    /// Accepts a new incoming connection from this listener.
    pub async fn accept(&self) -> io::Result<(TcpStream, SocketAddr)> {
        #[cfg(target_family = "wasm")]
        {
            let handle = self.handle;
            let (err, client_handle, _) =
                crate::rt::wasm::OverlappedFuture::new(move |ov| unsafe {
                    crate::abi::imports::net_accept(ov, handle);
                })
                .await;

            if err != 0 {
                return Err(io::Error::from_raw_os_error(err as i32));
            }

            // In a real implementation we'd probably want to get the peer addr too
            // For now, return a dummy addr or extend the ABI
            Ok((
                TcpStream {
                    handle: client_handle,
                },
                SocketAddr::V4(SocketAddrV4::new(Ipv4Addr::new(0, 0, 0, 0), 0)),
            ))
        }
        #[cfg(windows)]
        {
            crate::rt::windows::TcpAccept::new(self.handle).await
        }
        #[cfg(any(target_os = "linux", rusticated_linux))]
        {
            crate::rt::linux_op::TcpAccept::new(self.handle as i32).await
        }
        #[cfg(any(
            target_os = "macos",
            target_os = "freebsd",
            target_os = "openbsd",
            target_os = "netbsd"
        ))]
        {
            crate::rt::bsd::TcpAccept::new(self.handle as i32).await
        }
    }
}

impl Drop for TcpListener {
    fn drop(&mut self) {
        #[cfg(target_family = "wasm")]
        unsafe {
            crate::abi::imports::handle_close(self.handle);
        }
        #[cfg(windows)]
        unsafe {
            #[link(name = "ws2_32", kind = "raw-dylib")]
            unsafe extern "system" {
                fn closesocket(s: usize) -> i32;
            }
            closesocket(self.handle as usize);
        }
        #[cfg(any(unix, rusticated_linux))]
        {
            #[cfg(any(target_os = "linux", rusticated_linux))]
            crate::syscall!(crate::os::linux::syscall::nr::CLOSE, self.handle as usize);
            #[cfg(not(any(target_os = "linux", rusticated_linux)))]
            unsafe {
                unsafe extern "C" {
                    fn close(fd: i32) -> i32;
                }
                close(self.handle as i32);
            }
        }
    }
}

/// A trait for objects which can be converted or resolved to one or more `SocketAddr` values.
pub trait ToSocketAddrs {
    /// Returned iterator over socket addresses which this type may resolve to.
    type Iter: Iterator<Item = SocketAddr>;

    /// Converts this object to an iterator of resolved `SocketAddr`s.
    fn to_socket_addrs(&self) -> io::Result<Self::Iter>;
}

impl ToSocketAddrs for SocketAddr {
    type Iter = core::option::IntoIter<SocketAddr>;
    fn to_socket_addrs(&self) -> io::Result<Self::Iter> {
        Ok(Some(*self).into_iter())
    }
}

impl ToSocketAddrs for (IpAddr, u16) {
    type Iter = core::option::IntoIter<SocketAddr>;
    fn to_socket_addrs(&self) -> io::Result<Self::Iter> {
        let addr = match self.0 {
            IpAddr::V4(ref a) => SocketAddr::V4(SocketAddrV4::new(*a, self.1)),
            IpAddr::V6(ref a) => SocketAddr::V6(SocketAddrV6::new(*a, self.1)),
        };
        Ok(Some(addr).into_iter())
    }
}

impl ToSocketAddrs for (Ipv4Addr, u16) {
    type Iter = core::option::IntoIter<SocketAddr>;
    fn to_socket_addrs(&self) -> io::Result<Self::Iter> {
        Ok(Some(SocketAddr::V4(SocketAddrV4::new(self.0, self.1))).into_iter())
    }
}

impl ToSocketAddrs for (Ipv6Addr, u16) {
    type Iter = core::option::IntoIter<SocketAddr>;
    fn to_socket_addrs(&self) -> io::Result<Self::Iter> {
        Ok(Some(SocketAddr::V6(SocketAddrV6::new(self.0, self.1))).into_iter())
    }
}

impl ToSocketAddrs for (&str, u16) {
    type Iter = crate::alloc::vec::IntoIter<SocketAddr>;
    fn to_socket_addrs(&self) -> io::Result<Self::Iter> {
        // Simple IP parser for now
        if let Some(v4) = parse_ipv4(self.0) {
            return Ok(crate::alloc::vec![SocketAddr::V4(SocketAddrV4::new(v4, self.1))].into_iter());
        }
        // TODO: DNS resolution or more complex parsers
        Err(io::Error::other("unsupported address format"))
    }
}

fn parse_ipv4(s: &str) -> Option<Ipv4Addr> {
    let mut octets = [0u8; 4];
    let mut i = 0;
    for part in s.split('.') {
        if i >= 4 {
            return None;
        }
        octets[i] = part.parse().ok()?;
        i += 1;
    }
    if i == 4 {
        Some(Ipv4Addr::new(octets[0], octets[1], octets[2], octets[3]))
    } else {
        None
    }
}
