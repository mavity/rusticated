const std = @import("std");
const pal_abi = @import("pal_abi");
const win = @import("win_ffi.zig");
const mem = @import("mem.zig");
const PalState = mem.PalState;

const WSAData = extern struct {
    version: u16 = 0,
    high_version: u16 = 0,
    max_sockets: u16 = 0,
    max_udp_dg: u16 = 0,
    vendor_info: ?[*]u8 = null,
    description: [257]u8 = @splat(0),
    system_status: [129]u8 = @splat(0),
};

const SockAddrIn = extern struct {
    family: u16 = 2,
    port_be: u16 = 0,
    addr: [4]u8 = @splat(0),
    zero: [8]u8 = @splat(0),
};

const AddrInfoA = extern struct {
    flags: i32 = 0,
    family: i32 = 0,
    socktype: i32 = 0,
    protocol: i32 = 0,
    addrlen: usize = 0,
    canonname: ?[*:0]u8 = null,
    addr: ?[*]const u8 = null,
    next: ?*AddrInfoA = null,
};

fn ensureWsa(s: *PalState) bool {
    if (s.wsa_started) return true;
    const p_fn = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "WSAStartup") orelse return false;
    const WsaStartupFn = *const fn (u16, *WSAData) callconv(win.win_cc) i32;
    const startup: WsaStartupFn = @ptrCast(@alignCast(p_fn));
    var data: WSAData = .{};
    if (startup(0x0202, &data) != 0) return false;
    s.wsa_started = true;
    return true;
}

fn parseIpv4(addr: []const u8) ?[4]u8 {
    var out: [4]u8 = undefined;
    var part: usize = 0;
    var val: u32 = 0;
    var has_digit = false;
    for (addr) |b| {
        if (b == '.') {
            if (!has_digit or part >= 3 or val > 255) return null;
            out[part] = @intCast(val);
            part += 1;
            val = 0;
            has_digit = false;
        } else if (b >= '0' and b <= '9') {
            val = val * 10 + (b - '0');
            if (val > 255) return null;
            has_digit = true;
        } else {
            return null;
        }
    }
    if (!has_digit or part != 3) return null;
    out[3] = @intCast(val);
    return out;
}

pub fn netOpen(s: *PalState, ov: *pal_abi.Overlapped, addr_ptr: [*]const u8, addr_len: usize, port: u32, flags: u32) void {
    if (!ensureWsa(s)) {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    }
    const p_sock = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "socket") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const p_close = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "closesocket") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const p_gle = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "WSAGetLastError") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const SocketFn = *const fn (i32, i32, i32) callconv(win.win_cc) usize;
    const CloseSockFn = *const fn (usize) callconv(win.win_cc) i32;
    const WsaGleFn = *const fn () callconv(win.win_cc) i32;
    const sock_fn: SocketFn = @ptrCast(@alignCast(p_sock));
    const close_fn: CloseSockFn = @ptrCast(@alignCast(p_close));
    const wsa_gle: WsaGleFn = @ptrCast(@alignCast(p_gle));

    const sock = sock_fn(2, 1, 6); // AF_INET, SOCK_STREAM, IPPROTO_TCP
    if (sock == std.math.maxInt(usize)) {
        mem.completeInline(ov, win.mapWinError(@intCast(wsa_gle())), 0, 0);
        return;
    }

    const ip = parseIpv4(addr_ptr[0..addr_len]) orelse {
        _ = close_fn(sock);
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    var sa: SockAddrIn = .{
        .family = 2,
        .port_be = std.mem.nativeToBig(u16, @intCast(port & 0xffff)),
        .addr = ip,
    };

    const listen_mode = (flags & pal_abi.NET_OPEN_FLAG_LISTEN) != 0;
    if (listen_mode) {
        const p_bind = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "bind") orelse {
            _ = close_fn(sock);
            mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        const p_listen = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "listen") orelse {
            _ = close_fn(sock);
            mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        const BindFn = *const fn (usize, *const SockAddrIn, i32) callconv(win.win_cc) i32;
        const ListenFn = *const fn (usize, i32) callconv(win.win_cc) i32;
        const bind_fn: BindFn = @ptrCast(@alignCast(p_bind));
        const listen_fn: ListenFn = @ptrCast(@alignCast(p_listen));
        if (bind_fn(sock, &sa, @sizeOf(SockAddrIn)) != 0 or listen_fn(sock, 128) != 0) {
            const err = win.mapWinError(@intCast(wsa_gle()));
            _ = close_fn(sock);
            mem.completeInline(ov, err, 0, 0);
            return;
        }
        var eff_port: u64 = port;
        if (mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "getsockname")) |p_gsn| {
            const GsnFn = *const fn (usize, *SockAddrIn, *i32) callconv(win.win_cc) i32;
            const gsn: GsnFn = @ptrCast(@alignCast(p_gsn));
            var slen: i32 = @sizeOf(SockAddrIn);
            if (gsn(sock, &sa, &slen) == 0) {
                eff_port = std.mem.bigToNative(u16, sa.port_be);
            }
        }
        const he = mem.allocHandle(s) orelse {
            _ = close_fn(sock);
            mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        he.handle = @ptrFromInt(sock);
        he.is_socket = true;
        mem.completeInline(ov, 0, eff_port, he.token);
        return;
    }

    const p_conn = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "connect") orelse {
        _ = close_fn(sock);
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const ConnectFn = *const fn (usize, *const SockAddrIn, i32) callconv(win.win_cc) i32;
    const conn_fn: ConnectFn = @ptrCast(@alignCast(p_conn));
    if (conn_fn(sock, &sa, @sizeOf(SockAddrIn)) != 0) {
        const err = win.mapWinError(@intCast(wsa_gle()));
        _ = close_fn(sock);
        mem.completeInline(ov, err, 0, 0);
        return;
    }
    const he = mem.allocHandle(s) orelse {
        _ = close_fn(sock);
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    he.handle = @ptrFromInt(sock);
    he.is_socket = true;
    mem.completeInline(ov, 0, port, he.token);
}

pub fn netAccept(s: *PalState, ov: *pal_abi.Overlapped, listener: u64) void {
    const he = mem.findHandle(s, listener) orelse {
        mem.completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    if (!he.is_socket) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const p_acc = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "accept") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const AcceptFn = *const fn (usize, ?*anyopaque, ?*i32) callconv(win.win_cc) usize;
    const acc: AcceptFn = @ptrCast(@alignCast(p_acc));
    const cli = acc(@intFromPtr(he.handle), null, null);
    if (cli == std.math.maxInt(usize)) {
        mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
        return;
    }
    const cli_he = mem.allocHandle(s) orelse {
        if (mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "closesocket")) |p_c| {
            const CloseSockFn = *const fn (usize) callconv(win.win_cc) i32;
            const c_fn: CloseSockFn = @ptrCast(@alignCast(p_c));
            _ = c_fn(cli);
        }
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    cli_he.handle = @ptrFromInt(cli);
    cli_he.is_socket = true;
    mem.completeInline(ov, 0, 0, cli_he.token);
}

pub fn netLookup(s: *PalState, ov: *pal_abi.Overlapped, name: [*]const u8, len: usize, out: [*]u8, cap: usize) void {
    if (!ensureWsa(s)) {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    }
    if (len == 0 or len > 255) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const p_gai = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "getaddrinfo") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const p_fai = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "freeaddrinfo") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const GetAddrFn = *const fn ([*:0]const u8, ?[*:0]const u8, ?*const AddrInfoA, *?*AddrInfoA) callconv(win.win_cc) i32;
    const FreeAddrFn = *const fn (*AddrInfoA) callconv(win.win_cc) void;
    const gai: GetAddrFn = @ptrCast(@alignCast(p_gai));
    const fai: FreeAddrFn = @ptrCast(@alignCast(p_fai));

    var zname: [256]u8 = undefined;
    @memcpy(zname[0..len], name[0..len]);
    zname[len] = 0;

    var res: ?*AddrInfoA = null;
    if (gai(@ptrCast(&zname), null, null, &res) != 0 or res == null) {
        mem.completeInline(ov, pal_abi.ERROR_NOENT, 0, 0);
        return;
    }
    defer fai(res.?);

    var written: usize = 0;
    var cur = res;
    while (cur) |ai| : (cur = ai.next) {
        if (ai.family == 2 and ai.addrlen >= 8 and ai.addr != null) { // AF_INET
            if (written + 5 > cap) break;
            out[written] = 4;
            @memcpy(out[written + 1 ..][0..4], ai.addr.?[4..8]);
            written += 5;
        } else if (ai.family == 23 and ai.addrlen >= 24 and ai.addr != null) { // AF_INET6
            if (written + 17 > cap) break;
            out[written] = 16;
            @memcpy(out[written + 1 ..][0..16], ai.addr.?[8..24]);
            written += 17;
        }
    }
    mem.completeInline(ov, 0, 0, written);
}

pub fn netCertVerify(_: *PalState, ov: *pal_abi.Overlapped, _: [*]const u8, _: usize, _: [*]const u8, _: usize) void {
    mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
}
