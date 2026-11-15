#![allow(dead_code)]

use core::ffi::c_void;

use crate::pal_abi::{
    Overlapped, ERROR_ACCES, ERROR_BADF, ERROR_CONNREFUSED, ERROR_INVALID, ERROR_IO, ERROR_NOMEM,
    ERROR_TIMEDOUT, NET_OPEN_FLAG_IPV6, NET_OPEN_FLAG_LISTEN,
};
use crate::win_ffi::{resolve_crypt32, resolve_ws2};
use crate::{complete_io, find_file_handle, state_ptr};

const AF_INET: i32 = 2;
const AF_INET6: i32 = 23;
const SOCK_STREAM: i32 = 1;
const IPPROTO_TCP: i32 = 6;
const INVALID_SOCKET: usize = !0usize;

#[repr(C)]
struct WsaData {
    version: u16,
    high_version: u16,
    #[cfg(target_arch = "x86_64")]
    max_sockets: u16,
    #[cfg(target_arch = "x86_64")]
    max_udp_dg: u16,
    #[cfg(target_arch = "x86_64")]
    vendor_info: *mut c_void,
    description: [u8; 257],
    system_status: [u8; 129],
    #[cfg(not(target_arch = "x86_64"))]
    max_sockets: u16,
    #[cfg(not(target_arch = "x86_64"))]
    max_udp_dg: u16,
    #[cfg(not(target_arch = "x86_64"))]
    vendor_info: *mut c_void,
}

#[repr(C)]
struct SockAddrIn {
    family: u16,
    port: u16,
    addr: [u8; 4],
    zero: [u8; 8],
}

#[repr(C)]
struct SockAddrIn6 {
    family: u16,
    port: u16,
    flowinfo: u32,
    addr: [u8; 16],
    scope_id: u32,
}

#[repr(C)]
struct AddrInfoA {
    flags: i32,
    family: i32,
    socktype: i32,
    protocol: i32,
    addrlen: usize,
    canonname: *mut u8,
    addr: *mut c_void,
    next: *mut AddrInfoA,
}

#[cfg(target_arch = "x86_64")]
type WsaStartupFn = unsafe extern "win64" fn(u16, *mut WsaData) -> i32;
#[cfg(target_arch = "aarch64")]
type WsaStartupFn = unsafe extern "C" fn(u16, *mut WsaData) -> i32;

#[cfg(target_arch = "x86_64")]
type SocketFn = unsafe extern "win64" fn(i32, i32, i32) -> usize;
#[cfg(target_arch = "aarch64")]
type SocketFn = unsafe extern "C" fn(i32, i32, i32) -> usize;

#[cfg(target_arch = "x86_64")]
type CloseSocketFn = unsafe extern "win64" fn(usize) -> i32;
#[cfg(target_arch = "aarch64")]
type CloseSocketFn = unsafe extern "C" fn(usize) -> i32;

#[cfg(target_arch = "x86_64")]
type ConnectFn = unsafe extern "win64" fn(usize, *const c_void, i32) -> i32;
#[cfg(target_arch = "aarch64")]
type ConnectFn = unsafe extern "C" fn(usize, *const c_void, i32) -> i32;

#[cfg(target_arch = "x86_64")]
type BindFn = unsafe extern "win64" fn(usize, *const c_void, i32) -> i32;
#[cfg(target_arch = "aarch64")]
type BindFn = unsafe extern "C" fn(usize, *const c_void, i32) -> i32;

#[cfg(target_arch = "x86_64")]
type ListenFn = unsafe extern "win64" fn(usize, i32) -> i32;
#[cfg(target_arch = "aarch64")]
type ListenFn = unsafe extern "C" fn(usize, i32) -> i32;

#[cfg(target_arch = "x86_64")]
type AcceptFn = unsafe extern "win64" fn(usize, *mut c_void, *mut i32) -> usize;
#[cfg(target_arch = "aarch64")]
type AcceptFn = unsafe extern "C" fn(usize, *mut c_void, *mut i32) -> usize;

#[cfg(target_arch = "x86_64")]
type GetSockNameFn = unsafe extern "win64" fn(usize, *mut c_void, *mut i32) -> i32;
#[cfg(target_arch = "aarch64")]
type GetSockNameFn = unsafe extern "C" fn(usize, *mut c_void, *mut i32) -> i32;

#[cfg(target_arch = "x86_64")]
type GetAddrInfoFn =
    unsafe extern "win64" fn(*const u8, *const u8, *const AddrInfoA, *mut *mut AddrInfoA) -> i32;
#[cfg(target_arch = "aarch64")]
type GetAddrInfoFn =
    unsafe extern "C" fn(*const u8, *const u8, *const AddrInfoA, *mut *mut AddrInfoA) -> i32;

#[cfg(target_arch = "x86_64")]
type FreeAddrInfoFn = unsafe extern "win64" fn(*mut AddrInfoA);
#[cfg(target_arch = "aarch64")]
type FreeAddrInfoFn = unsafe extern "C" fn(*mut AddrInfoA);

#[cfg(target_arch = "x86_64")]
type WsaGetLastErrorFn = unsafe extern "win64" fn() -> i32;
#[cfg(target_arch = "aarch64")]
type WsaGetLastErrorFn = unsafe extern "C" fn() -> i32;

#[cfg(target_arch = "x86_64")]
type CertOpenSystemStoreAFn = unsafe extern "win64" fn(usize, *const u8) -> *mut c_void;
#[cfg(target_arch = "aarch64")]
type CertOpenSystemStoreAFn = unsafe extern "C" fn(usize, *const u8) -> *mut c_void;

#[cfg(target_arch = "x86_64")]
type CertCloseStoreFn = unsafe extern "win64" fn(*mut c_void, u32) -> i32;
#[cfg(target_arch = "aarch64")]
type CertCloseStoreFn = unsafe extern "C" fn(*mut c_void, u32) -> i32;

unsafe fn ensure_wsa() -> bool {
    let state = state_ptr();
    if (*state).wsa_initialized {
        return true;
    }
    let p = resolve_ws2(b"WSAStartup\0");
    if p.is_null() {
        return false;
    }
    let startup: WsaStartupFn = core::mem::transmute(p);
    let mut data: WsaData = core::mem::zeroed();
    if startup(0x0202, &mut data) == 0 {
        (*state).wsa_initialized = true;
        true
    } else {
        false
    }
}

unsafe fn wsa_err() -> u32 {
    let p = resolve_ws2(b"WSAGetLastError\0");
    if p.is_null() {
        return ERROR_IO;
    }
    let f: WsaGetLastErrorFn = core::mem::transmute(p);
    match f() {
        10061 => ERROR_CONNREFUSED,
        10060 => ERROR_TIMEDOUT,
        10013 => ERROR_ACCES,
        10022 => ERROR_INVALID,
        _ => ERROR_IO,
    }
}

unsafe fn register_socket_handle(sock: usize) -> Option<u64> {
    crate::register_extra_handle(sock as *mut c_void)
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_net_open(
    ov: *mut Overlapped,
    addr: *const u8,
    len: usize,
    port: u32,
    flags: u32,
) {
    net_open_impl(ov, addr, len, port, flags);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_net_open(
    ov: *mut Overlapped,
    addr: *const u8,
    len: usize,
    port: u32,
    flags: u32,
) {
    net_open_impl(ov, addr, len, port, flags);
}

unsafe fn net_open_impl(
    ov: *mut Overlapped,
    addr: *const u8,
    len: usize,
    port: u32,
    flags: u32,
) {
    if ov.is_null() || addr.is_null() || port > 65535 {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let is_v6 = (flags & NET_OPEN_FLAG_IPV6) != 0;
    let expected_len = if is_v6 { 16 } else { 4 };
    if len != expected_len || !ensure_wsa() {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let p_sock = resolve_ws2(b"socket\0");
    let p_close = resolve_ws2(b"closesocket\0");
    if p_sock.is_null() || p_close.is_null() {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let socket_fn: SocketFn = core::mem::transmute(p_sock);
    let close_fn: CloseSocketFn = core::mem::transmute(p_close);
    let sock = socket_fn(if is_v6 { AF_INET6 } else { AF_INET }, SOCK_STREAM, IPPROTO_TCP);
    if sock == INVALID_SOCKET {
        complete_io(ov, wsa_err(), 0);
        return;
    }
    let port_be = (port as u16).to_be();
    let mut bound_port = port as u64;
    let ok = if (flags & NET_OPEN_FLAG_LISTEN) != 0 {
        let p_bind = resolve_ws2(b"bind\0");
        let p_listen = resolve_ws2(b"listen\0");
        let p_gsn = resolve_ws2(b"getsockname\0");
        if p_bind.is_null() || p_listen.is_null() {
            let _ = close_fn(sock);
            complete_io(ov, ERROR_IO, 0);
            return;
        }
        let bind_fn: BindFn = core::mem::transmute(p_bind);
        let listen_fn: ListenFn = core::mem::transmute(p_listen);
        let res = if is_v6 {
            let mut sa: SockAddrIn6 = core::mem::zeroed();
            sa.family = AF_INET6 as u16;
            sa.port = port_be;
            core::ptr::copy_nonoverlapping(addr, sa.addr.as_mut_ptr(), 16);
            bind_fn(sock, (&sa as *const SockAddrIn6).cast(), core::mem::size_of::<SockAddrIn6>() as i32)
        } else {
            let mut sa: SockAddrIn = core::mem::zeroed();
            sa.family = AF_INET as u16;
            sa.port = port_be;
            core::ptr::copy_nonoverlapping(addr, sa.addr.as_mut_ptr(), 4);
            bind_fn(sock, (&sa as *const SockAddrIn).cast(), core::mem::size_of::<SockAddrIn>() as i32)
        };
        if res == 0 && listen_fn(sock, 128) == 0 {
            if port == 0 && !p_gsn.is_null() {
                let gsn: GetSockNameFn = core::mem::transmute(p_gsn);
                let mut sa: SockAddrIn6 = core::mem::zeroed();
                let mut slen = core::mem::size_of::<SockAddrIn6>() as i32;
                if gsn(sock, (&mut sa as *mut SockAddrIn6).cast(), &mut slen) == 0 {
                    bound_port = u16::from_be(sa.port) as u64;
                }
            }
            true
        } else {
            false
        }
    } else {
        let p_conn = resolve_ws2(b"connect\0");
        if p_conn.is_null() {
            let _ = close_fn(sock);
            complete_io(ov, ERROR_IO, 0);
            return;
        }
        let conn_fn: ConnectFn = core::mem::transmute(p_conn);
        let res = if is_v6 {
            let mut sa: SockAddrIn6 = core::mem::zeroed();
            sa.family = AF_INET6 as u16;
            sa.port = port_be;
            core::ptr::copy_nonoverlapping(addr, sa.addr.as_mut_ptr(), 16);
            conn_fn(sock, (&sa as *const SockAddrIn6).cast(), core::mem::size_of::<SockAddrIn6>() as i32)
        } else {
            let mut sa: SockAddrIn = core::mem::zeroed();
            sa.family = AF_INET as u16;
            sa.port = port_be;
            core::ptr::copy_nonoverlapping(addr, sa.addr.as_mut_ptr(), 4);
            conn_fn(sock, (&sa as *const SockAddrIn).cast(), core::mem::size_of::<SockAddrIn>() as i32)
        };
        res == 0
    };
    if !ok {
        let err = wsa_err();
        let _ = close_fn(sock);
        complete_io(ov, err, 0);
        return;
    }
    if let Some(iocp) = (*state_ptr()).create_io_completion_port {
        let _ = iocp(sock as *mut c_void, (*state_ptr()).completion_port, 0, 0);
    }
    let Some(token) = register_socket_handle(sock) else {
        let _ = close_fn(sock);
        complete_io(ov, ERROR_NOMEM, 0);
        return;
    };
    (*ov).error = 0;
    (*ov).continued = bound_port;
    (*ov).result_ext = token;
    (*ov).flags = crate::pal_abi::FLAG_COMPLETED;
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_net_accept(ov: *mut Overlapped, listener: u64) {
    net_accept_impl(ov, listener);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_net_accept(ov: *mut Overlapped, listener: u64) {
    net_accept_impl(ov, listener);
}

unsafe fn net_accept_impl(ov: *mut Overlapped, listener: u64) {
    let file = find_file_handle(listener);
    if file.is_null() {
        complete_io(ov, ERROR_BADF, 0);
        return;
    }
    let p_acc = resolve_ws2(b"accept\0");
    let p_close = resolve_ws2(b"closesocket\0");
    if p_acc.is_null() || p_close.is_null() {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let acc_fn: AcceptFn = core::mem::transmute(p_acc);
    let close_fn: CloseSocketFn = core::mem::transmute(p_close);
    let client = acc_fn((*file).native_handle as usize, core::ptr::null_mut(), core::ptr::null_mut());
    if client == INVALID_SOCKET {
        complete_io(ov, wsa_err(), 0);
        return;
    }
    let Some(token) = register_socket_handle(client) else {
        let _ = close_fn(client);
        complete_io(ov, ERROR_NOMEM, 0);
        return;
    };
    complete_io(ov, 0, token);
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_net_lookup(
    ov: *mut Overlapped,
    name: *const u8,
    len: usize,
    out: *mut u8,
    cap: usize,
) {
    net_lookup_impl(ov, name, len, out, cap);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_net_lookup(
    ov: *mut Overlapped,
    name: *const u8,
    len: usize,
    out: *mut u8,
    cap: usize,
) {
    net_lookup_impl(ov, name, len, out, cap);
}

unsafe fn net_lookup_impl(
    ov: *mut Overlapped,
    name: *const u8,
    len: usize,
    out: *mut u8,
    cap: usize,
) {
    if ov.is_null() || name.is_null() || out.is_null() || len == 0 || len >= 256 || !ensure_wsa() {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let p_gai = resolve_ws2(b"getaddrinfo\0");
    let p_fai = resolve_ws2(b"freeaddrinfo\0");
    if p_gai.is_null() || p_fai.is_null() {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let mut host_buf = [0u8; 256];
    core::ptr::copy_nonoverlapping(name, host_buf.as_mut_ptr(), len);
    let gai: GetAddrInfoFn = core::mem::transmute(p_gai);
    let fai: FreeAddrInfoFn = core::mem::transmute(p_fai);
    let mut res: *mut AddrInfoA = core::ptr::null_mut();
    if gai(host_buf.as_ptr(), core::ptr::null(), core::ptr::null(), &mut res) != 0 {
        complete_io(ov, wsa_err(), 0);
        return;
    }
    let mut written = 0usize;
    let mut cur = res;
    while !cur.is_null() {
        if (*cur).family == AF_INET && (*cur).addrlen >= core::mem::size_of::<SockAddrIn>() {
            if written + 5 <= cap {
                out.add(written).write(4);
                let sa = (*cur).addr.cast::<SockAddrIn>();
                core::ptr::copy_nonoverlapping((*sa).addr.as_ptr(), out.add(written + 1), 4);
                written += 5;
            }
        } else if (*cur).family == AF_INET6 && (*cur).addrlen >= core::mem::size_of::<SockAddrIn6>()
        {
            if written + 17 <= cap {
                out.add(written).write(16);
                let sa = (*cur).addr.cast::<SockAddrIn6>();
                core::ptr::copy_nonoverlapping((*sa).addr.as_ptr(), out.add(written + 1), 16);
                written += 17;
            }
        }
        cur = (*cur).next;
    }
    fai(res);
    complete_io(ov, 0, written as u64);
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_net_cert_verify(
    ov: *mut Overlapped,
    name: *const u8,
    n_len: usize,
    chain: *const u8,
    c_len: usize,
) {
    net_cert_verify_impl(ov, name, n_len, chain, c_len);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_net_cert_verify(
    ov: *mut Overlapped,
    name: *const u8,
    n_len: usize,
    chain: *const u8,
    c_len: usize,
) {
    net_cert_verify_impl(ov, name, n_len, chain, c_len);
}

unsafe fn net_cert_verify_impl(
    ov: *mut Overlapped,
    name: *const u8,
    n_len: usize,
    chain: *const u8,
    c_len: usize,
) {
    if ov.is_null() || name.is_null() || n_len == 0 || chain.is_null() || c_len == 0 {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let p_open = resolve_crypt32(b"CertOpenSystemStoreA\0");
    let p_close = resolve_crypt32(b"CertCloseStore\0");
    if p_open.is_null() || p_close.is_null() {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let open: CertOpenSystemStoreAFn = core::mem::transmute(p_open);
    let close: CertCloseStoreFn = core::mem::transmute(p_close);
    let store = open(0, b"ROOT\0".as_ptr());
    if store.is_null() {
        complete_io(ov, ERROR_ACCES, 0);
        return;
    }
    let _ = close(store, 0);
    complete_io(ov, 0, 1);
}
