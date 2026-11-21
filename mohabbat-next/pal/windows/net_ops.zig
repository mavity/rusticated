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

const WSAEINVAL = 10022;
const WSAENOTSOCK = 10038;
const WSAEOPNOTSUPP = 10045;

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
            const digit: u32 = b - '0';
            if (val > (255 - digit) / 10) return null;
            val = val * 10 + digit;
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

    const is_udp = (flags & (1 << 2)) != 0;
    const sock = sock_fn(2, if (is_udp) 2 else 1, if (is_udp) 17 else 6); // AF_INET, DGRAM/STREAM
    if (sock == std.math.maxInt(usize)) {
        mem.completeInline(ov, win.mapWinError(@intCast(wsa_gle())), 0, 0);
        return;
    }

    const ip = if (addr_len == 0) [4]u8{ 0, 0, 0, 0 } else (parseIpv4(addr_ptr[0..addr_len]) orelse {
        _ = close_fn(sock);
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    });
    var sa: SockAddrIn = .{
        .family = 2,
        .port_be = std.mem.nativeToBig(u16, @intCast(port & 0xffff)),
        .addr = ip,
    };

    const listen_mode = (flags & 1) == 0;
    if (listen_mode) {
        const p_bind = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "bind") orelse {
            _ = close_fn(sock);
            mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        const BindFn = *const fn (usize, *const SockAddrIn, i32) callconv(win.win_cc) i32;
        const bind_fn: BindFn = @ptrCast(@alignCast(p_bind));
        if (bind_fn(sock, &sa, @sizeOf(SockAddrIn)) != 0) {
            const err = win.mapWinError(@intCast(wsa_gle()));
            _ = close_fn(sock);
            mem.completeInline(ov, err, 0, 0);
            return;
        }
        if (!is_udp) {
            const p_listen = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "listen") orelse {
                _ = close_fn(sock);
                mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
                return;
            };
            const ListenFn = *const fn (usize, i32) callconv(win.win_cc) i32;
            const listen_fn: ListenFn = @ptrCast(@alignCast(p_listen));
            if (listen_fn(sock, 128) != 0) {
                const err = win.mapWinError(@intCast(wsa_gle()));
                _ = close_fn(sock);
                mem.completeInline(ov, err, 0, 0);
                return;
            }
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
        he.is_udp = is_udp;
        he.is_listener = !is_udp;
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
    he.is_udp = is_udp;
    mem.completeInline(ov, 0, port, he.token);
}

fn netAcceptWorker(ctx: ?*anyopaque) callconv(win.win_cc) u32 {
    const op: *mem.PendingOp = @ptrCast(@alignCast(ctx.?));
    const s: *PalState = @ptrCast(@alignCast(op.pal_state orelse return 0));
    const pq = s.p_post_queued orelse return 0;
    const p_gle = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "WSAGetLastError");
    const p_acc = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "accept") orelse {
        op.win_ov.internal = pal_abi.ERROR_UNSUPPORTED;
        _ = pq(s.iocp, 0, @intFromPtr(op), &op.win_ov);
        return 0;
    };
    const AcceptFn = *const fn (usize, ?*anyopaque, ?*i32) callconv(win.win_cc) usize;
    const acc: AcceptFn = @ptrCast(@alignCast(p_acc));
    const cli = acc(op.sock_fd, null, null);
    if (cli == std.math.maxInt(usize)) {
        if (p_gle) |p| {
            const WsaGleFn = *const fn () callconv(win.win_cc) i32;
            const wsa_gle: WsaGleFn = @ptrCast(@alignCast(p));
            op.win_ov.internal = switch (wsa_gle()) {
                WSAEINVAL, WSAENOTSOCK, WSAEOPNOTSUPP => pal_abi.ERROR_INVALID,
                else => pal_abi.ERROR_IO,
            };
        } else {
            op.win_ov.internal = pal_abi.ERROR_IO;
        }
        op.win_ov.internal_high = 0;
    } else {
        op.win_ov.internal = 0;
        op.win_ov.internal_high = cli;
    }
    _ = pq(s.iocp, 0, @intFromPtr(op), &op.win_ov);
    return 0;
}

pub fn netAccept(s: *PalState, ov: *pal_abi.Overlapped, listener: u64) void {
    if (listener < 3) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const he = mem.findHandle(s, listener) orelse {
        mem.completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    if (!he.is_socket or !he.is_listener) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const op = mem.allocOp(s) orelse {
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .accept;
    op.guest_ov = ov;
    op.sock_fd = @intFromPtr(he.handle);
    op.pal_state = s;
    if (s.p_create_thread) |ct| {
        const th = ct(null, 0, netAcceptWorker, op, 0, null);
        if (th != null) {
            if (s.p_close_handle) |ch| _ = ch(th);
            return;
        }
    }
    mem.freeOp(s, op);
    mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
}

const NetLookupAddress = struct {
    family: u8,
    bytes: []const u8,
};

fn netLookupAddress(ai: *AddrInfoA) ?NetLookupAddress {
    if (ai.addr) |addr| {
        if (ai.family == 2 and ai.addrlen >= 8) {
            return .{ .family = 4, .bytes = addr[4..8] };
        }
        if (ai.family == 23 and ai.addrlen >= 24) {
            return .{ .family = 6, .bytes = addr[8..24] };
        }
    }
    return null;
}

fn writeNetLookupBytes(out: []u8, offset: usize, bytes: []const u8) void {
    if (offset >= out.len) return;
    const count = @min(bytes.len, out.len - offset);
    @memcpy(out[offset..][0..count], bytes[0..count]);
}

fn writeNetLookupU32(out: []u8, offset: usize, value: u32) void {
    var bytes: [4]u8 = undefined;
    std.mem.writeInt(u32, &bytes, value, .little);
    writeNetLookupBytes(out, offset, &bytes);
}

fn writeNetLookupEntry(out: []u8, index: usize, data_offset: usize, entry: NetLookupAddress) void {
    const family_offset = 8 + index;
    if (family_offset < out.len) out[family_offset] = entry.family;
    writeNetLookupBytes(out, data_offset, entry.bytes);
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

    var count: usize = 0;
    var data_len: usize = 0;
    var cur = res;
    while (cur) |ai| : (cur = ai.next) {
        if (netLookupAddress(ai)) |entry| {
            if (count == std.math.maxInt(usize) or data_len > std.math.maxInt(usize) - entry.bytes.len) {
                mem.completeInline(ov, pal_abi.ERROR_OVERFLOW, 0, 0);
                return;
            }
            count += 1;
            data_len += entry.bytes.len;
        }
    }
    if (count == 0) {
        mem.completeInline(ov, pal_abi.ERROR_NOENT, 0, 0);
        return;
    }
    const total_size = 8 + count + data_len;
    if (total_size > @as(usize, std.math.maxInt(u32))) {
        mem.completeInline(ov, pal_abi.ERROR_OVERFLOW, 0, 0);
        return;
    }

    const out_slice = out[0..cap];
    writeNetLookupU32(out_slice, 0, @intCast(total_size));
    writeNetLookupU32(out_slice, 4, @intCast(count));
    var index: usize = 0;
    var data_offset = 8 + count;
    cur = res;
    while (cur) |ai| : (cur = ai.next) {
        if (netLookupAddress(ai)) |entry| {
            writeNetLookupEntry(out_slice, index, data_offset, entry);
            index += 1;
            data_offset += entry.bytes.len;
        }
    }
    mem.completeInline(ov, 0, 0, total_size);
}

const CertChainPara = extern struct {
    cb_size: u32 = @sizeOf(CertChainPara),
    requested_usage: [24]u8 = @splat(0),
};

const SslExtraCertChainPolicyPara = extern struct {
    cb_size: u32 = @sizeOf(SslExtraCertChainPolicyPara),
    dw_auth_type: u32 = 2, // AUTHTYPE_SERVER
    fdw_checks: u32 = 0,
    pwsz_server_name: ?[*:0]const u16 = null,
};

const CertChainPolicyPara = extern struct {
    cb_size: u32 = @sizeOf(CertChainPolicyPara),
    dw_flags: u32 = 0,
    pv_extra_policy_para: ?*anyopaque = null,
};

const CertChainPolicyStatus = extern struct {
    cb_size: u32 = @sizeOf(CertChainPolicyStatus),
    dw_error: u32 = 0,
    l_chain_index: i32 = 0,
    l_element_index: i32 = 0,
    pv_extra_policy_status: ?*anyopaque = null,
};

pub fn netCertVerify(s: *PalState, ov: *pal_abi.Overlapped, name: [*]const u8, n_len: usize, chain_ptr: [*]const u8, c_len: usize) void {
    if (c_len < 4) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const payload = chain_ptr[0..c_len];
    const count = std.mem.readInt(u32, payload[0..4], .little);
    if (count == 0 or 4 + @as(usize, count) * 4 > c_len) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }

    const p_open = mem.resolveDll(s, &s.crypt32, "crypt32.dll", "CertOpenStore") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const p_add = mem.resolveDll(s, &s.crypt32, "crypt32.dll", "CertAddEncodedCertificateToStore") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const p_chain = mem.resolveDll(s, &s.crypt32, "crypt32.dll", "CertGetCertificateChain") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const p_verify = mem.resolveDll(s, &s.crypt32, "crypt32.dll", "CertVerifyCertificateChainPolicy") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const p_free_chain = mem.resolveDll(s, &s.crypt32, "crypt32.dll", "CertFreeCertificateChain") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const p_close = mem.resolveDll(s, &s.crypt32, "crypt32.dll", "CertCloseStore") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const p_free_ctx = mem.resolveDll(s, &s.crypt32, "crypt32.dll", "CertFreeCertificateContext");

    const OpenStoreFn = *const fn (usize, u32, usize, u32, ?*const anyopaque) callconv(win.win_cc) ?*anyopaque;
    const AddCertFn = *const fn (?*anyopaque, u32, [*]const u8, u32, u32, ?*?*anyopaque) callconv(win.win_cc) i32;
    const GetChainFn = *const fn (?*anyopaque, ?*anyopaque, ?*anyopaque, ?*anyopaque, *const CertChainPara, u32, ?*anyopaque, *?*anyopaque) callconv(win.win_cc) i32;
    const VerifyPolicyFn = *const fn (usize, ?*anyopaque, *const CertChainPolicyPara, *CertChainPolicyStatus) callconv(win.win_cc) i32;
    const FreeChainFn = *const fn (?*anyopaque) callconv(win.win_cc) void;
    const CloseStoreFn = *const fn (?*anyopaque, u32) callconv(win.win_cc) i32;
    const FreeCtxFn = *const fn (?*anyopaque) callconv(win.win_cc) i32;

    const open_store: OpenStoreFn = @ptrCast(@alignCast(p_open));
    const add_cert: AddCertFn = @ptrCast(@alignCast(p_add));
    const get_chain: GetChainFn = @ptrCast(@alignCast(p_chain));
    const verify_policy: VerifyPolicyFn = @ptrCast(@alignCast(p_verify));
    const free_chain: FreeChainFn = @ptrCast(@alignCast(p_free_chain));
    const close_store: CloseStoreFn = @ptrCast(@alignCast(p_close));

    // CERT_STORE_PROV_MEMORY = 2
    const store = open_store(2, 0, 0, 0, null) orelse {
        mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
        return;
    };
    defer _ = close_store(store, 0);

    var off: usize = 4 + @as(usize, count) * 4;
    var leaf_ctx: ?*anyopaque = null;
    defer if (leaf_ctx) |lc| {
        if (p_free_ctx) |fc| {
            const free_ctx: FreeCtxFn = @ptrCast(@alignCast(fc));
            _ = free_ctx(lc);
        }
    };

    for (0..count) |i| {
        const dlen = std.mem.readInt(u32, payload[4 + i * 4 ..][0..4], .little);
        if (dlen == 0 or off + dlen > c_len) {
            mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
            return;
        }
        const der = payload[off .. off + dlen];
        off += dlen;
        // X509_ASN_ENCODING | PKCS_7_ASN_ENCODING = 0x10001, CERT_STORE_ADD_ALWAYS = 4
        if (i == 0) {
            if (add_cert(store, 0x10001, der.ptr, dlen, 4, &leaf_ctx) == 0 or leaf_ctx == null) {
                mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
                return;
            }
        } else if (add_cert(store, 0x10001, der.ptr, dlen, 4, null) == 0) {
            mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
            return;
        }
    }

    var wname: ?[*:0]u16 = null;
    if (n_len > 0) {
        wname = mem.utf8ToWideAlloc(s, name, n_len);
    }
    defer if (wname) |wn| {
        mem.freeWideAlloc(s, @ptrCast(@constCast(wn)));
    };

    var chain_para: CertChainPara = .{};
    var chain_ctx: ?*anyopaque = null;
    if (get_chain(null, leaf_ctx, null, store, &chain_para, 0, null, &chain_ctx) == 0 or chain_ctx == null) {
        mem.completeInline(ov, 0, 0, 0);
        return;
    }
    defer free_chain(chain_ctx);

    var ssl_para: SslExtraCertChainPolicyPara = .{
        .pwsz_server_name = wname,
    };
    var policy_para: CertChainPolicyPara = .{
        .pv_extra_policy_para = &ssl_para,
    };
    var status: CertChainPolicyStatus = .{};
    // CERT_CHAIN_POLICY_SSL = 4
    if (verify_policy(4, chain_ctx, &policy_para, &status) == 0) {
        mem.completeInline(ov, 0, 0, 0);
        return;
    }
    mem.completeInline(ov, 0, 0, if (status.dw_error == 0) 1 else 0);
}

test "Windows net lookup matches the shared ABI wire format" {
    const ipv4 = [_]u8{ 127, 0, 0, 1 };
    const ipv6 = [_]u8{ 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1 };
    var actual: [30]u8 = @splat(0);
    writeNetLookupU32(&actual, 0, @intCast(actual.len));
    writeNetLookupU32(&actual, 4, 2);
    writeNetLookupEntry(&actual, 0, 10, .{ .family = 4, .bytes = &ipv4 });
    writeNetLookupEntry(&actual, 1, 14, .{ .family = 6, .bytes = &ipv6 });

    const expected = [_]u8{
        30, 0, 0, 0, 2, 0, 0, 0, 4, 6,
        127, 0, 0, 1,
        0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1,
    };
    try std.testing.expectEqualSlices(u8, &expected, &actual);
}

test "Windows net lookup writes only the available prefix" {
    var actual: [12]u8 = @splat(0);
    writeNetLookupU32(&actual, 0, 30);
    writeNetLookupU32(&actual, 4, 2);
    writeNetLookupEntry(&actual, 0, 10, .{ .family = 4, .bytes = &.{ 127, 0, 0, 1 } });

    const expected = [_]u8{ 30, 0, 0, 0, 2, 0, 0, 0, 4, 0, 127, 0 };
    try std.testing.expectEqualSlices(u8, &expected, &actual);
}

test "Windows net open IPv4 parser accepts octet boundaries" {
    const cases = .{
        .{ "0.0.0.0", .{ 0, 0, 0, 0 } },
        .{ "255.255.255.255", .{ 255, 255, 255, 255 } },
        .{ "192.168.1.10", .{ 192, 168, 1, 10 } },
    };
    inline for (cases) |case| {
        const actual = parseIpv4(case.@"0") orelse return error.TestUnexpectedResult;
        try std.testing.expectEqualSlices(u8, &case.@"1", &actual);
    }
}

test "Windows net open IPv4 parser rejects malformed and overflowing octets" {
    const malformed = .{
        "",
        "1.2.3",
        "1.2.3.4.",
        "1..3.4",
        ".1.2.3.4",
        "256.0.0.1",
        "1.2.3.-1",
        "1.2.3.4x",
        "999999999999999999999999999999.1.2.3",
    };
    inline for (malformed) |addr| {
        try std.testing.expect(parseIpv4(addr) == null);
    }
}

test "Windows net lookup extracts IPv4 and IPv6 sockaddr addresses" {
    const ipv4_sockaddr = [_]u8{ 2, 0, 0x1f, 0x90, 192, 0, 2, 9 };
    var ipv4_ai = AddrInfoA{
        .family = 2,
        .addrlen = ipv4_sockaddr.len,
        .addr = &ipv4_sockaddr,
    };
    const ipv4 = netLookupAddress(&ipv4_ai) orelse return error.TestUnexpectedResult;
    try std.testing.expectEqual(@as(u8, 4), ipv4.family);
    try std.testing.expectEqualSlices(u8, ipv4_sockaddr[4..8], ipv4.bytes);

    const ipv6_sockaddr = [_]u8{
        23, 0, 0x01, 0xbb, 0x12, 0x34, 0x56, 0x78,
        0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0,
        0,   0,   0,    0,    0, 0, 0, 1,
        0,   0,   0,    0,
    };
    var ipv6_ai = AddrInfoA{
        .family = 23,
        .addrlen = 24,
        .addr = &ipv6_sockaddr,
    };
    const ipv6 = netLookupAddress(&ipv6_ai) orelse return error.TestUnexpectedResult;
    try std.testing.expectEqual(@as(u8, 6), ipv6.family);
    try std.testing.expectEqualSlices(u8, ipv6_sockaddr[8..24], ipv6.bytes);
}

test "Windows net lookup rejects unsupported and truncated sockaddr entries" {
    const sockaddr = [_]u8{
        23, 0, 0x01, 0xbb, 0, 0, 0, 0,
        0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0,
        0, 0, 0, 0, 0, 0, 0, 1,
    };
    var ai = AddrInfoA{ .family = 23, .addrlen = sockaddr.len - 1, .addr = &sockaddr };
    try std.testing.expect(netLookupAddress(&ai) == null);

    ai.addrlen = sockaddr.len;
    ai.family = 10;
    try std.testing.expect(netLookupAddress(&ai) == null);

    ai.family = 2;
    ai.addrlen = 7;
    try std.testing.expect(netLookupAddress(&ai) == null);

    ai.family = 23;
    ai.addrlen = sockaddr.len;
    ai.addr = null;
    try std.testing.expect(netLookupAddress(&ai) == null);
}

test "Windows net lookup safely truncates family and address fields" {
    var actual: [10]u8 = @splat(0);
    const address = [_]u8{ 0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1 };
    writeNetLookupEntry(&actual, 2, 9, .{ .family = 6, .bytes = &address });

    const expected = [_]u8{ 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x20 };
    try std.testing.expectEqualSlices(u8, &expected, &actual);

    writeNetLookupU32(&actual, actual.len, 0x12345678);
    try std.testing.expectEqualSlices(u8, &expected, &actual);
}

var net_test_missing_symbol: ?[]const u8 = null;
var net_test_startup_result: i32 = 0;
var net_test_socket_result: usize = 0x40;
var net_test_socket_error: i32 = 10061;
var net_test_socket_calls: usize = 0;
var net_test_socket_type: i32 = 0;
var net_test_socket_protocol: i32 = 0;
var net_test_close_calls: usize = 0;
var net_test_bind_result: i32 = 0;
var net_test_bind_calls: usize = 0;
var net_test_bind_addr: SockAddrIn = .{};
var net_test_listen_result: i32 = 0;
var net_test_listen_calls: usize = 0;
var net_test_connect_result: i32 = 0;
var net_test_connect_calls: usize = 0;
var net_test_gai_result: i32 = 0;
var net_test_gai_res: ?*AddrInfoA = null;
var net_test_gai_calls: usize = 0;
var net_test_gai_name: [256]u8 = @splat(0);
var net_test_free_addr_calls: usize = 0;
var net_test_accept_result: usize = 0x51;
var net_test_accept_calls: usize = 0;
var net_test_post_calls: usize = 0;
var net_test_post_internal: usize = 0;
var net_test_post_high: usize = 0;
var net_test_thread_success: bool = true;
var net_test_thread_calls: usize = 0;
var net_test_thread_close_calls: usize = 0;
var net_test_open_store_success: bool = true;
var net_test_add_failure_at: usize = 0;
var net_test_add_calls: usize = 0;
var net_test_chain_success: bool = true;
var net_test_policy_call_success: bool = true;
var net_test_policy_error: u32 = 0;
var net_test_free_chain_calls: usize = 0;
var net_test_close_store_calls: usize = 0;
var net_test_free_context_calls: usize = 0;

fn netTestLoadLibrary(_: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
    return @ptrFromInt(1);
}

fn netTestSocket(_: i32, sock_type: i32, protocol: i32) callconv(win.win_cc) usize {
    net_test_socket_calls += 1;
    net_test_socket_type = sock_type;
    net_test_socket_protocol = protocol;
    return net_test_socket_result;
}

fn netTestCloseSocket(_: usize) callconv(win.win_cc) i32 {
    net_test_close_calls += 1;
    return 0;
}

fn netTestWsaStartup(_: u16, _: *WSAData) callconv(win.win_cc) i32 {
    return net_test_startup_result;
}

fn netTestWsaGetLastError() callconv(win.win_cc) i32 {
    return net_test_socket_error;
}

fn netTestBind(_: usize, addr: *const SockAddrIn, _: i32) callconv(win.win_cc) i32 {
    net_test_bind_calls += 1;
    net_test_bind_addr = addr.*;
    return net_test_bind_result;
}

fn netTestListen(_: usize, _: i32) callconv(win.win_cc) i32 {
    net_test_listen_calls += 1;
    return net_test_listen_result;
}

fn netTestConnect(_: usize, _: *const SockAddrIn, _: i32) callconv(win.win_cc) i32 {
    net_test_connect_calls += 1;
    return net_test_connect_result;
}

fn netTestAccept(_: usize, _: ?*anyopaque, _: ?*i32) callconv(win.win_cc) usize {
    net_test_accept_calls += 1;
    return net_test_accept_result;
}

fn netTestPostQueued(_: win.HANDLE, _: u32, _: usize, ov: ?*win.WinOverlapped) callconv(win.win_cc) i32 {
    net_test_post_calls += 1;
    if (ov) |posted| {
        net_test_post_internal = posted.internal;
        net_test_post_high = posted.internal_high;
    }
    return 1;
}

fn netTestThread(_: ?*anyopaque, _: usize, start: *const fn (?*anyopaque) callconv(win.win_cc) u32, ctx: ?*anyopaque, _: u32, _: ?*u32) callconv(win.win_cc) win.HANDLE {
    net_test_thread_calls += 1;
    if (!net_test_thread_success) return null;
    _ = start(ctx);
    return @ptrFromInt(0x70);
}

fn netTestCloseThread(_: win.HANDLE) callconv(win.win_cc) i32 {
    net_test_thread_close_calls += 1;
    return 1;
}

fn netTestGetAddrInfo(name: [*:0]const u8, _: ?[*:0]const u8, _: ?*const AddrInfoA, out: *?*AddrInfoA) callconv(win.win_cc) i32 {
    net_test_gai_calls += 1;
    const bytes = std.mem.sliceTo(name, 0);
    @memset(&net_test_gai_name, 0);
    @memcpy(net_test_gai_name[0..bytes.len], bytes);
    out.* = net_test_gai_res;
    return net_test_gai_result;
}

fn netTestFreeAddrInfo(_: *AddrInfoA) callconv(win.win_cc) void {
    net_test_free_addr_calls += 1;
}

fn netTestOpenStore(_: usize, _: u32, _: usize, _: u32, _: ?*const anyopaque) callconv(win.win_cc) ?*anyopaque {
    return if (net_test_open_store_success) @ptrFromInt(0x81) else null;
}

fn netTestAddCert(_: ?*anyopaque, _: u32, _: [*]const u8, _: u32, _: u32, out: ?*?*anyopaque) callconv(win.win_cc) i32 {
    net_test_add_calls += 1;
    if (net_test_add_failure_at == net_test_add_calls) return 0;
    if (out) |ctx| ctx.* = @ptrFromInt(0x82);
    return 1;
}

fn netTestGetChain(_: ?*anyopaque, _: ?*anyopaque, _: ?*anyopaque, _: ?*anyopaque, _: *const CertChainPara, _: u32, _: ?*anyopaque, out: *?*anyopaque) callconv(win.win_cc) i32 {
    out.* = if (net_test_chain_success) @ptrFromInt(0x83) else null;
    return @intFromBool(net_test_chain_success);
}

fn netTestVerifyPolicy(_: usize, _: ?*anyopaque, _: *const CertChainPolicyPara, status: *CertChainPolicyStatus) callconv(win.win_cc) i32 {
    status.dw_error = net_test_policy_error;
    return @intFromBool(net_test_policy_call_success);
}

fn netTestFreeChain(_: ?*anyopaque) callconv(win.win_cc) void {
    net_test_free_chain_calls += 1;
}

fn netTestCloseStore(_: ?*anyopaque, _: u32) callconv(win.win_cc) i32 {
    net_test_close_store_calls += 1;
    return 1;
}

fn netTestFreeContext(_: ?*anyopaque) callconv(win.win_cc) i32 {
    net_test_free_context_calls += 1;
    return 1;
}

fn netTestGetProcAddress(_: ?*anyopaque, name: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
    const symbol = std.mem.sliceTo(name, 0);
    if (net_test_missing_symbol) |missing| {
        if (std.mem.eql(u8, symbol, missing)) return null;
    }
    if (std.mem.eql(u8, symbol, "WSAStartup")) return @ptrCast(@constCast(&netTestWsaStartup));
    if (std.mem.eql(u8, symbol, "socket")) return @ptrCast(@constCast(&netTestSocket));
    if (std.mem.eql(u8, symbol, "closesocket")) return @ptrCast(@constCast(&netTestCloseSocket));
    if (std.mem.eql(u8, symbol, "WSAGetLastError")) return @ptrCast(@constCast(&netTestWsaGetLastError));
    if (std.mem.eql(u8, symbol, "bind")) return @ptrCast(@constCast(&netTestBind));
    if (std.mem.eql(u8, symbol, "listen")) return @ptrCast(@constCast(&netTestListen));
    if (std.mem.eql(u8, symbol, "connect")) return @ptrCast(@constCast(&netTestConnect));
    if (std.mem.eql(u8, symbol, "accept")) return @ptrCast(@constCast(&netTestAccept));
    if (std.mem.eql(u8, symbol, "getaddrinfo")) return @ptrCast(@constCast(&netTestGetAddrInfo));
    if (std.mem.eql(u8, symbol, "freeaddrinfo")) return @ptrCast(@constCast(&netTestFreeAddrInfo));
    if (std.mem.eql(u8, symbol, "CertOpenStore")) return @ptrCast(@constCast(&netTestOpenStore));
    if (std.mem.eql(u8, symbol, "CertAddEncodedCertificateToStore")) return @ptrCast(@constCast(&netTestAddCert));
    if (std.mem.eql(u8, symbol, "CertGetCertificateChain")) return @ptrCast(@constCast(&netTestGetChain));
    if (std.mem.eql(u8, symbol, "CertVerifyCertificateChainPolicy")) return @ptrCast(@constCast(&netTestVerifyPolicy));
    if (std.mem.eql(u8, symbol, "CertFreeCertificateChain")) return @ptrCast(@constCast(&netTestFreeChain));
    if (std.mem.eql(u8, symbol, "CertCloseStore")) return @ptrCast(@constCast(&netTestCloseStore));
    if (std.mem.eql(u8, symbol, "CertFreeCertificateContext")) return @ptrCast(@constCast(&netTestFreeContext));
    if (std.mem.eql(u8, symbol, "PostQueuedCompletionStatus")) return @ptrCast(@constCast(&netTestPostQueued));
    return null;
}

fn initNetTestState(s: *PalState) void {
    net_test_missing_symbol = null;
    net_test_startup_result = 0;
    net_test_socket_result = 0x40;
    net_test_socket_error = 10061;
    net_test_socket_calls = 0;
    net_test_socket_type = 0;
    net_test_socket_protocol = 0;
    net_test_close_calls = 0;
    net_test_bind_result = 0;
    net_test_bind_calls = 0;
    net_test_bind_addr = .{};
    net_test_listen_result = 0;
    net_test_listen_calls = 0;
    net_test_connect_result = 0;
    net_test_connect_calls = 0;
    net_test_gai_result = 0;
    net_test_gai_res = null;
    net_test_gai_calls = 0;
    net_test_gai_name = @splat(0);
    net_test_free_addr_calls = 0;
    net_test_accept_result = 0x51;
    net_test_accept_calls = 0;
    net_test_post_calls = 0;
    net_test_post_internal = 0;
    net_test_post_high = 0;
    net_test_thread_success = true;
    net_test_thread_calls = 0;
    net_test_thread_close_calls = 0;
    net_test_open_store_success = true;
    net_test_add_failure_at = 0;
    net_test_add_calls = 0;
    net_test_chain_success = true;
    net_test_policy_call_success = true;
    net_test_policy_error = 0;
    net_test_free_chain_calls = 0;
    net_test_close_store_calls = 0;
    net_test_free_context_calls = 0;
    s.* = .{};
    s.host = .{
        .load_library = netTestLoadLibrary,
        .get_proc_address = netTestGetProcAddress,
        .alloc = null,
        .free = null,
        .runtime_slice = null,
        .runtime_slice_size = 0,
        .argv = null,
        .argc = 0,
        .wasm_payload = null,
        .wasm_payload_size = 0,
    };
    s.iocp = @ptrFromInt(0x60);
    s.p_post_queued = netTestPostQueued;
    s.p_create_thread = netTestThread;
    s.p_close_handle = netTestCloseThread;
}

fn expectNetResult(ov: *const pal_abi.Overlapped, err: u32, continued: u64, result_ext: u64) !void {
    try std.testing.expectEqual(err, ov.@"error");
    try std.testing.expectEqual(continued, ov.continued);
    try std.testing.expectEqual(result_ext, ov.result_ext);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, @atomicLoad(u32, &ov.flags, .acquire));
}

fn netCertPayload(out: *[9]u8) void {
    out.* = .{ 1, 0, 0, 0, 1, 0, 0, 0, 0x30 };
}

test "Windows net open validates setup, addresses, and native socket failures" {
    const malformed = .{
        "1.2.3",
        "1..3.4",
        "256.0.0.1",
        "1.2.3.-1",
    };
    inline for (malformed) |addr| {
        var state: PalState = undefined;
        initNetTestState(&state);
        state.wsa_started = true;
        var ov: pal_abi.Overlapped = .{};
        netOpen(&state, &ov, addr, addr.len, 80, 0);
        try expectNetResult(&ov, pal_abi.ERROR_INVALID, 0, 0);
        try std.testing.expectEqual(@as(usize, 1), net_test_close_calls);
    }

    var state: PalState = undefined;
    initNetTestState(&state);
    net_test_missing_symbol = "WSAStartup";
    var ov: pal_abi.Overlapped = .{};
    netOpen(&state, &ov, "127.0.0.1", 9, 80, 1);
    try expectNetResult(&ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
    try std.testing.expectEqual(@as(usize, 0), net_test_socket_calls);

    initNetTestState(&state);
    net_test_startup_result = 1;
    ov = .{};
    netOpen(&state, &ov, "127.0.0.1", 9, 80, 1);
    try expectNetResult(&ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);

    initNetTestState(&state);
    state.wsa_started = true;
    net_test_missing_symbol = "socket";
    ov = .{};
    netOpen(&state, &ov, "127.0.0.1", 9, 80, 1);
    try expectNetResult(&ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);

    initNetTestState(&state);
    state.wsa_started = true;
    net_test_missing_symbol = "closesocket";
    ov = .{};
    netOpen(&state, &ov, "127.0.0.1", 9, 80, 1);
    try expectNetResult(&ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
    try std.testing.expectEqual(@as(usize, 0), net_test_socket_calls);

    initNetTestState(&state);
    state.wsa_started = true;
    net_test_missing_symbol = "WSAGetLastError";
    ov = .{};
    netOpen(&state, &ov, "127.0.0.1", 9, 80, 1);
    try expectNetResult(&ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);

    initNetTestState(&state);
    state.wsa_started = true;
    net_test_socket_result = std.math.maxInt(usize);
    net_test_socket_error = 10061;
    ov = .{};
    netOpen(&state, &ov, "127.0.0.1", 9, 80, 1);
    try expectNetResult(&ov, pal_abi.ERROR_CONNREFUSED, 0, 0);

    initNetTestState(&state);
    state.wsa_started = true;
    net_test_missing_symbol = "bind";
    ov = .{};
    netOpen(&state, &ov, "127.0.0.1", 9, 80, 0);
    try expectNetResult(&ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
    try std.testing.expectEqual(@as(usize, 1), net_test_close_calls);

    initNetTestState(&state);
    state.wsa_started = true;
    net_test_missing_symbol = "listen";
    ov = .{};
    netOpen(&state, &ov, "127.0.0.1", 9, 80, 0);
    try expectNetResult(&ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
    try std.testing.expectEqual(@as(usize, 1), net_test_close_calls);

    initNetTestState(&state);
    state.wsa_started = true;
    net_test_bind_result = -1;
    net_test_socket_error = 5;
    ov = .{};
    netOpen(&state, &ov, "192.0.2.7", 9, 0x12345, 0);
    try expectNetResult(&ov, pal_abi.ERROR_ACCES, 0, 0);
    try std.testing.expectEqual(@as(usize, 1), net_test_close_calls);
    try std.testing.expectEqual(@as(u16, 0x2345), std.mem.bigToNative(u16, net_test_bind_addr.port_be));

    initNetTestState(&state);
    state.wsa_started = true;
    net_test_listen_result = -1;
    net_test_socket_error = 10060;
    ov = .{};
    netOpen(&state, &ov, "127.0.0.1", 9, 80, 0);
    try expectNetResult(&ov, pal_abi.ERROR_TIMEDOUT, 0, 0);
    try std.testing.expectEqual(@as(usize, 1), net_test_close_calls);

    initNetTestState(&state);
    state.wsa_started = true;
    for (&state.handles) |*handle| handle.used = true;
    ov = .{};
    netOpen(&state, &ov, "127.0.0.1", 9, 80, 0);
    try expectNetResult(&ov, pal_abi.ERROR_NOMEM, 0, 0);
    try std.testing.expectEqual(@as(usize, 1), net_test_close_calls);

    initNetTestState(&state);
    state.wsa_started = true;
    ov = .{};
    netOpen(&state, &ov, "192.0.2.9", 9, 443, 0);
    try expectNetResult(&ov, 0, 443, state.handles[0].token);
    try std.testing.expect(state.handles[0].is_socket);
    try std.testing.expect(!state.handles[0].is_udp);
    try std.testing.expect(state.handles[0].is_listener);
    try std.testing.expectEqual(@as(i32, 1), net_test_socket_type);
    try std.testing.expectEqual(@as(i32, 6), net_test_socket_protocol);
    try std.testing.expectEqual(@as(usize, 1), net_test_bind_calls);
    try std.testing.expectEqualSlices(u8, &.{ 192, 0, 2, 9 }, &net_test_bind_addr.addr);
    try std.testing.expectEqual(@as(usize, 1), net_test_listen_calls);

    initNetTestState(&state);
    state.wsa_started = true;
    ov = .{};
    netOpen(&state, &ov, "0.0.0.0", 7, 53, 1 << 2);
    try expectNetResult(&ov, 0, 53, state.handles[0].token);
    try std.testing.expect(state.handles[0].is_udp);
    try std.testing.expect(!state.handles[0].is_listener);
    try std.testing.expectEqual(@as(i32, 2), net_test_socket_type);
    try std.testing.expectEqual(@as(i32, 17), net_test_socket_protocol);
    try std.testing.expectEqual(@as(usize, 0), net_test_listen_calls);

    initNetTestState(&state);
    state.wsa_started = true;
    net_test_missing_symbol = "connect";
    ov = .{};
    netOpen(&state, &ov, "127.0.0.1", 9, 443, 1);
    try expectNetResult(&ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
    try std.testing.expectEqual(@as(usize, 1), net_test_close_calls);

    initNetTestState(&state);
    state.wsa_started = true;
    net_test_connect_result = -1;
    net_test_socket_error = 10060;
    ov = .{};
    netOpen(&state, &ov, "127.0.0.1", 9, 443, 1);
    try expectNetResult(&ov, pal_abi.ERROR_TIMEDOUT, 0, 0);
    try std.testing.expectEqual(@as(usize, 1), net_test_close_calls);

    initNetTestState(&state);
    state.wsa_started = true;
    ov = .{};
    netOpen(&state, &ov, "127.0.0.1", 9, 443, 1);
    try expectNetResult(&ov, 0, 443, state.handles[0].token);
    try std.testing.expectEqual(@as(usize, 1), net_test_connect_calls);
    try std.testing.expect(state.handles[0].is_socket);
    try std.testing.expect(!state.handles[0].is_listener);
}

test "Windows net accept validates handles and dispatches native accept results" {
    var state: PalState = undefined;
    var ov: pal_abi.Overlapped = .{};
    for ([_]u64{ 0, 1, 2 }) |token| {
        initNetTestState(&state);
        ov = .{};
        netAccept(&state, &ov, token);
        try expectNetResult(&ov, pal_abi.ERROR_INVALID, 0, 0);
    }

    initNetTestState(&state);
    ov = .{};
    netAccept(&state, &ov, 999);
    try expectNetResult(&ov, pal_abi.ERROR_BADF, 0, 0);

    initNetTestState(&state);
    const non_listener_socket = mem.allocHandle(&state).?;
    non_listener_socket.is_socket = true;
    ov = .{};
    netAccept(&state, &ov, non_listener_socket.token);
    try expectNetResult(&ov, pal_abi.ERROR_INVALID, 0, 0);

    initNetTestState(&state);
    const ordinary = mem.allocHandle(&state).?;
    ov = .{};
    netAccept(&state, &ov, ordinary.token);
    try expectNetResult(&ov, pal_abi.ERROR_INVALID, 0, 0);

    initNetTestState(&state);
    const no_op_listener = mem.allocHandle(&state).?;
    no_op_listener.is_socket = true;
    no_op_listener.is_listener = true;
    for (&state.ops) |*op| op.used = true;
    ov = .{};
    netAccept(&state, &ov, no_op_listener.token);
    try expectNetResult(&ov, pal_abi.ERROR_NOMEM, 0, 0);

    initNetTestState(&state);
    const no_thread_listener = mem.allocHandle(&state).?;
    no_thread_listener.is_socket = true;
    no_thread_listener.is_listener = true;
    state.p_create_thread = null;
    ov = .{};
    netAccept(&state, &ov, no_thread_listener.token);
    try expectNetResult(&ov, pal_abi.ERROR_IO, 0, 0);
    try std.testing.expectEqual(@as(usize, 0), state.pending_count);

    initNetTestState(&state);
    const failed_thread_listener = mem.allocHandle(&state).?;
    failed_thread_listener.is_socket = true;
    failed_thread_listener.is_listener = true;
    net_test_thread_success = false;
    ov = .{};
    netAccept(&state, &ov, failed_thread_listener.token);
    try expectNetResult(&ov, pal_abi.ERROR_IO, 0, 0);
    try std.testing.expectEqual(@as(usize, 0), state.pending_count);

    initNetTestState(&state);
    const missing_accept_listener = mem.allocHandle(&state).?;
    missing_accept_listener.is_socket = true;
    missing_accept_listener.is_listener = true;
    missing_accept_listener.handle = @ptrFromInt(0x44);
    state.ws2_32 = @ptrFromInt(1);
    net_test_missing_symbol = "accept";
    ov = .{};
    netAccept(&state, &ov, missing_accept_listener.token);
    try std.testing.expectEqual(@as(u32, 0), @atomicLoad(u32, &ov.flags, .acquire));
    try std.testing.expectEqual(@as(usize, 1), net_test_post_calls);
    try std.testing.expectEqual(@as(usize, pal_abi.ERROR_UNSUPPORTED), net_test_post_internal);

    initNetTestState(&state);
    const accepted_listener = mem.allocHandle(&state).?;
    accepted_listener.is_socket = true;
    accepted_listener.is_listener = true;
    accepted_listener.handle = @ptrFromInt(0x44);
    state.ws2_32 = @ptrFromInt(1);
    ov = .{};
    netAccept(&state, &ov, accepted_listener.token);
    try std.testing.expectEqual(@as(u32, 0), @atomicLoad(u32, &ov.flags, .acquire));
    try std.testing.expectEqual(@as(usize, 1), net_test_accept_calls);
    try std.testing.expectEqual(@as(usize, 1), net_test_thread_calls);
    try std.testing.expectEqual(@as(usize, 1), net_test_post_calls);
    try std.testing.expectEqual(@as(usize, 0), net_test_post_internal);
    try std.testing.expectEqual(@as(usize, 0x51), net_test_post_high);
    try std.testing.expectEqual(@as(usize, 1), net_test_thread_close_calls);

    initNetTestState(&state);
    const failed_accept_listener = mem.allocHandle(&state).?;
    failed_accept_listener.is_socket = true;
    failed_accept_listener.is_listener = true;
    failed_accept_listener.handle = @ptrFromInt(0x44);
    state.ws2_32 = @ptrFromInt(1);
    net_test_accept_result = std.math.maxInt(usize);
    ov = .{};
    netAccept(&state, &ov, failed_accept_listener.token);
    try std.testing.expectEqual(@as(usize, pal_abi.ERROR_IO), net_test_post_internal);
    try std.testing.expectEqual(@as(usize, 0), net_test_post_high);

    inline for (.{ WSAEINVAL, WSAENOTSOCK, WSAEOPNOTSUPP }) |wsa_error| {
        initNetTestState(&state);
        const invalid_accept_listener = mem.allocHandle(&state).?;
        invalid_accept_listener.is_socket = true;
        invalid_accept_listener.is_listener = true;
        invalid_accept_listener.handle = @ptrFromInt(0x44);
        state.ws2_32 = @ptrFromInt(1);
        net_test_accept_result = std.math.maxInt(usize);
        net_test_socket_error = wsa_error;
        ov = .{};
        netAccept(&state, &ov, invalid_accept_listener.token);
        try std.testing.expectEqual(@as(usize, pal_abi.ERROR_INVALID), net_test_post_internal);
    }
}

fn runNetLookupCapacity(cap: usize) !void {
    var state: PalState = undefined;
    initNetTestState(&state);
    state.wsa_started = true;
    const sockaddr = [_]u8{ 2, 0, 0x1f, 0x90, 192, 0, 2, 9 };
    var ai = AddrInfoA{ .family = 2, .addrlen = sockaddr.len, .addr = &sockaddr };
    net_test_gai_res = &ai;
    var out: [16]u8 = @splat(0xcc);
    var ov: pal_abi.Overlapped = .{};
    netLookup(&state, &ov, "lookup.test", 11, &out, cap);
    try expectNetResult(&ov, 0, 0, 13);
    const wire = [_]u8{ 13, 0, 0, 0, 1, 0, 0, 0, 4, 192, 0, 2, 9, 0xcc, 0xcc, 0xcc };
    try std.testing.expectEqualSlices(u8, wire[0..cap], out[0..cap]);
    const untouched: [16]u8 = @splat(0xcc);
    try std.testing.expectEqualSlices(u8, untouched[cap..], out[cap..]);
    try std.testing.expectEqual(@as(usize, 1), net_test_free_addr_calls);
}

test "Windows net lookup validates names, maps resolver outcomes, and bounds output" {
    var state: PalState = undefined;
    var ov: pal_abi.Overlapped = .{};
    var empty: [0]u8 = .{};
    initNetTestState(&state);
    state.wsa_started = true;
    netLookup(&state, &ov, "", 0, &empty, 0);
    try expectNetResult(&ov, pal_abi.ERROR_INVALID, 0, 0);

    var too_long: [256]u8 = @splat('x');
    ov = .{};
    netLookup(&state, &ov, &too_long, too_long.len, &empty, 0);
    try expectNetResult(&ov, pal_abi.ERROR_INVALID, 0, 0);

    initNetTestState(&state);
    net_test_missing_symbol = "WSAStartup";
    ov = .{};
    netLookup(&state, &ov, "host", 4, &empty, 0);
    try expectNetResult(&ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);

    initNetTestState(&state);
    state.wsa_started = true;
    net_test_missing_symbol = "getaddrinfo";
    ov = .{};
    netLookup(&state, &ov, "host", 4, &empty, 0);
    try expectNetResult(&ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);

    initNetTestState(&state);
    state.wsa_started = true;
    net_test_missing_symbol = "freeaddrinfo";
    ov = .{};
    netLookup(&state, &ov, "host", 4, &empty, 0);
    try expectNetResult(&ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);

    initNetTestState(&state);
    state.wsa_started = true;
    net_test_gai_result = 11001;
    var out: [32]u8 = @splat(0xcc);
    ov = .{};
    netLookup(&state, &ov, "missing.test", 12, &out, out.len);
    try expectNetResult(&ov, pal_abi.ERROR_NOENT, 0, 0);
    try std.testing.expectEqual(@as(usize, 1), net_test_gai_calls);

    initNetTestState(&state);
    state.wsa_started = true;
    net_test_gai_res = null;
    ov = .{};
    netLookup(&state, &ov, "missing.test", 12, &out, out.len);
    try expectNetResult(&ov, pal_abi.ERROR_NOENT, 0, 0);

    initNetTestState(&state);
    state.wsa_started = true;
    const unsupported_sockaddr = [_]u8{ 0, 0, 0, 0, 192, 0, 2, 1 };
    var unsupported_ai = AddrInfoA{ .family = 10, .addrlen = unsupported_sockaddr.len, .addr = &unsupported_sockaddr };
    net_test_gai_res = &unsupported_ai;
    ov = .{};
    netLookup(&state, &ov, "unsupported.test", 16, &out, out.len);
    try expectNetResult(&ov, pal_abi.ERROR_NOENT, 0, 0);
    try std.testing.expectEqual(@as(usize, 1), net_test_free_addr_calls);

    inline for (.{ 0, 1, 7, 8, 9, 10, 12, 13, 16 }) |cap| {
        try runNetLookupCapacity(cap);
    }

    initNetTestState(&state);
    state.wsa_started = true;
    const ipv6_sockaddr = [_]u8{
        23, 0, 0, 0, 0, 0, 0, 0,
        0x20, 1, 0x0d, 0xb8, 0, 0, 0, 0,
        0, 0, 0, 0, 0, 0, 0, 1,
    };
    var ipv6_ai = AddrInfoA{ .family = 23, .addrlen = ipv6_sockaddr.len, .addr = &ipv6_sockaddr };
    net_test_gai_res = &ipv6_ai;
    out = @splat(0);
    ov = .{};
    netLookup(&state, &ov, "v6.test", 7, &out, out.len);
    try expectNetResult(&ov, 0, 0, 25);
    try std.testing.expectEqualSlices(u8, &.{ 25, 0, 0, 0, 1, 0, 0, 0, 6 }, out[0..9]);
    try std.testing.expectEqualSlices(u8, ipv6_sockaddr[8..24], out[9..25]);
    try std.testing.expectEqualSlices(u8, "v6.test", std.mem.sliceTo(&net_test_gai_name, 0));

    initNetTestState(&state);
    state.wsa_started = true;
    const ipv4_sockaddr = [_]u8{ 2, 0, 0, 0, 127, 0, 0, 1 };
    const mixed_ipv6_sockaddr = [_]u8{
        23, 0, 0, 0, 0, 0, 0, 0,
        0x20, 1, 0x0d, 0xb8, 0, 0, 0, 0,
        0, 0, 0, 0, 0, 0, 0, 2,
    };
    var second = AddrInfoA{ .family = 23, .addrlen = mixed_ipv6_sockaddr.len, .addr = &mixed_ipv6_sockaddr };
    var first = AddrInfoA{ .family = 2, .addrlen = ipv4_sockaddr.len, .addr = &ipv4_sockaddr, .next = &second };
    net_test_gai_res = &first;
    out = @splat(0);
    ov = .{};
    netLookup(&state, &ov, "mixed.test", 10, &out, out.len);
    try expectNetResult(&ov, 0, 0, 30);
    try std.testing.expectEqualSlices(u8, &.{ 30, 0, 0, 0, 2, 0, 0, 0, 4, 6 }, out[0..10]);
    try std.testing.expectEqualSlices(u8, ipv4_sockaddr[4..8], out[10..14]);
    try std.testing.expectEqualSlices(u8, mixed_ipv6_sockaddr[8..24], out[14..30]);
}

test "Windows net certificate verification validates chains and reports policy results" {
    var state: PalState = undefined;
    var ov: pal_abi.Overlapped = .{};
    var payload: [16]u8 = @splat(0);
    for ([_]usize{ 0, 3 }) |len| {
        initNetTestState(&state);
        netCertVerify(&state, &ov, "", 0, &payload, len);
        try expectNetResult(&ov, pal_abi.ERROR_INVALID, 0, 0);
    }

    initNetTestState(&state);
    payload[0..4].* = .{ 0, 0, 0, 0 };
    ov = .{};
    netCertVerify(&state, &ov, "", 0, &payload, 4);
    try expectNetResult(&ov, pal_abi.ERROR_INVALID, 0, 0);

    initNetTestState(&state);
    payload[0..8].* = .{ 1, 0, 0, 0, 1, 0, 0, 0 };
    ov = .{};
    netCertVerify(&state, &ov, "", 0, &payload, 7);
    try expectNetResult(&ov, pal_abi.ERROR_INVALID, 0, 0);

    initNetTestState(&state);
    payload[0..8].* = .{ 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0 };
    ov = .{};
    netCertVerify(&state, &ov, "", 0, &payload, 8);
    try expectNetResult(&ov, pal_abi.ERROR_INVALID, 0, 0);

    initNetTestState(&state);
    payload[0..8].* = .{ 1, 0, 0, 0, 1, 0, 0, 0 };
    ov = .{};
    netCertVerify(&state, &ov, "", 0, &payload, 8);
    try expectNetResult(&ov, pal_abi.ERROR_INVALID, 0, 0);

    var valid_payload: [9]u8 = undefined;
    netCertPayload(&valid_payload);
    const required_symbols = .{
        "CertOpenStore",
        "CertAddEncodedCertificateToStore",
        "CertGetCertificateChain",
        "CertVerifyCertificateChainPolicy",
        "CertFreeCertificateChain",
        "CertCloseStore",
    };
    inline for (required_symbols) |symbol| {
        initNetTestState(&state);
        net_test_missing_symbol = symbol;
        ov = .{};
        netCertVerify(&state, &ov, "", 0, &valid_payload, valid_payload.len);
        try expectNetResult(&ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
    }

    initNetTestState(&state);
    net_test_open_store_success = false;
    ov = .{};
    netCertVerify(&state, &ov, "", 0, &valid_payload, valid_payload.len);
    try expectNetResult(&ov, pal_abi.ERROR_IO, 0, 0);
    try std.testing.expectEqual(@as(usize, 0), net_test_close_store_calls);

    initNetTestState(&state);
    net_test_add_failure_at = 1;
    ov = .{};
    netCertVerify(&state, &ov, "", 0, &valid_payload, valid_payload.len);
    try expectNetResult(&ov, pal_abi.ERROR_INVALID, 0, 0);
    try std.testing.expectEqual(@as(usize, 1), net_test_close_store_calls);

    initNetTestState(&state);
    net_test_chain_success = false;
    ov = .{};
    netCertVerify(&state, &ov, "", 0, &valid_payload, valid_payload.len);
    try expectNetResult(&ov, 0, 0, 0);
    try std.testing.expectEqual(@as(usize, 1), net_test_free_context_calls);
    try std.testing.expectEqual(@as(usize, 1), net_test_close_store_calls);

    initNetTestState(&state);
    net_test_policy_call_success = false;
    ov = .{};
    netCertVerify(&state, &ov, "", 0, &valid_payload, valid_payload.len);
    try expectNetResult(&ov, 0, 0, 0);
    try std.testing.expectEqual(@as(usize, 1), net_test_free_chain_calls);

    initNetTestState(&state);
    net_test_policy_error = 1;
    ov = .{};
    netCertVerify(&state, &ov, "", 0, &valid_payload, valid_payload.len);
    try expectNetResult(&ov, 0, 0, 0);
    try std.testing.expectEqual(@as(usize, 1), net_test_free_context_calls);
    try std.testing.expectEqual(@as(usize, 1), net_test_free_chain_calls);

    initNetTestState(&state);
    net_test_missing_symbol = "CertFreeCertificateContext";
    ov = .{};
    netCertVerify(&state, &ov, "", 0, &valid_payload, valid_payload.len);
    try expectNetResult(&ov, 0, 0, 1);
    try std.testing.expectEqual(@as(usize, 0), net_test_free_context_calls);

    initNetTestState(&state);
    ov = .{};
    netCertVerify(&state, &ov, "", 0, &valid_payload, valid_payload.len);
    try expectNetResult(&ov, 0, 0, 1);
    try std.testing.expectEqual(@as(usize, 1), net_test_add_calls);
    try std.testing.expectEqual(@as(usize, 1), net_test_close_store_calls);
    try std.testing.expectEqual(@as(usize, 1), net_test_free_chain_calls);

    initNetTestState(&state);
    var two_cert_payload: [14]u8 = .{
        2, 0, 0, 0,
        1, 0, 0, 0,
        1, 0, 0, 0,
        0x30, 0x31,
    };
    net_test_add_failure_at = 2;
    ov = .{};
    netCertVerify(&state, &ov, "", 0, &two_cert_payload, two_cert_payload.len);
    try expectNetResult(&ov, pal_abi.ERROR_INVALID, 0, 0);
    try std.testing.expectEqual(@as(usize, 2), net_test_add_calls);
    try std.testing.expectEqual(@as(usize, 1), net_test_free_context_calls);
    try std.testing.expectEqual(@as(usize, 1), net_test_close_store_calls);

    initNetTestState(&state);
    var empty_intermediate_payload: [13]u8 = .{
        2, 0, 0, 0,
        1, 0, 0, 0,
        0, 0, 0, 0,
        0x30,
    };
    ov = .{};
    netCertVerify(&state, &ov, "", 0, &empty_intermediate_payload, empty_intermediate_payload.len);
    try expectNetResult(&ov, pal_abi.ERROR_INVALID, 0, 0);
    try std.testing.expectEqual(@as(usize, 1), net_test_add_calls);
    try std.testing.expectEqual(@as(usize, 1), net_test_free_context_calls);
    try std.testing.expectEqual(@as(usize, 1), net_test_close_store_calls);
}
