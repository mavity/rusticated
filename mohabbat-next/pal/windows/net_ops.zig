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
    const p_acc = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "accept") orelse {
        op.win_ov.internal = pal_abi.ERROR_UNSUPPORTED;
        _ = pq(s.iocp, 0, @intFromPtr(op), &op.win_ov);
        return 0;
    };
    const AcceptFn = *const fn (usize, ?*anyopaque, ?*i32) callconv(win.win_cc) usize;
    const acc: AcceptFn = @ptrCast(@alignCast(p_acc));
    const cli = acc(op.sock_fd, null, null);
    if (cli == std.math.maxInt(usize)) {
        op.win_ov.internal = pal_abi.ERROR_IO;
        op.win_ov.internal_high = 0;
    } else {
        op.win_ov.internal = 0;
        op.win_ov.internal_high = cli;
    }
    _ = pq(s.iocp, 0, @intFromPtr(op), &op.win_ov);
    return 0;
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
        if (off + dlen > c_len) {
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
        } else {
            _ = add_cert(store, 0x10001, der.ptr, dlen, 4, null);
        }
    }

    var wname: ?[*:0]u16 = null;
    if (n_len > 0) {
        wname = mem.utf8ToWideAlloc(s, name, n_len);
    }
    defer if (wname) |wn| {
        if (s.host.free) |free| free(@ptrCast(@constCast(wn)));
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
