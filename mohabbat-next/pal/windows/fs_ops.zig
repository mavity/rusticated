const std = @import("std");
const pal_abi = @import("pal_abi");
const win = @import("win_ffi.zig");
const mem = @import("mem.zig");
const PalState = mem.PalState;

pub fn pathOpen(s: *PalState, ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, flags: u32) void {
    const cf = s.p_create_file_w orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const wpath = mem.utf8ToWideAlloc(s, path, len) orelse {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    defer if (s.host.free) |free| free(@ptrCast(@constCast(wpath)));

    const is_dir = (flags & pal_abi.OPEN_FLAG_DIRECTORY) != 0;
    if (is_dir) {
        // Verify directory exists via GetFileAttributesExW
        const p_attr = mem.resolveKernel32(s, "GetFileAttributesExW") orelse {
            mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        const GetAttrFn = *const fn ([*:0]const u16, u32, *win.Win32FileAttributeData) callconv(win.win_cc) i32;
        const get_attr: GetAttrFn = @ptrCast(@alignCast(p_attr));
        var attr: win.Win32FileAttributeData = .{};
        if (get_attr(wpath, 0, &attr) == 0) {
            const gle = s.p_get_last_error orelse {
                mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
                return;
            };
            mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
            return;
        }
        if ((attr.attributes & 0x10) == 0) {
            mem.completeInline(ov, pal_abi.ERROR_NOTDIR, 0, 0);
            return;
        }
        const he = mem.allocHandle(s) orelse {
            mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        he.is_dir = true;
        // Build search pattern "<wpath>\*"
        var wlen: usize = 0;
        while (wpath[wlen] != 0) : (wlen += 1) {}
        if (s.host.alloc) |alloc| {
            if (alloc((wlen + 3) * 2)) |pat_raw| {
                const pat: [*]u16 = @ptrCast(@alignCast(pat_raw));
                @memcpy(pat[0..wlen], wpath[0..wlen]);
                pat[wlen] = '\\';
                pat[wlen + 1] = '*';
                pat[wlen + 2] = 0;
                he.dir_path_w = @ptrCast(pat);
            }
        }
        mem.completeInline(ov, 0, 0, he.token);
        return;
    }

    const access_mode = flags & 3;
    const desired_access: u32 = switch (access_mode) {
        0 => 0x8000_0000, // GENERIC_READ
        1 => 0x4000_0000, // GENERIC_WRITE
        else => 0xC000_0000, // GENERIC_READ | GENERIC_WRITE
    };

    const create = (flags & pal_abi.OPEN_FLAG_CREATE) != 0;
    const excl = (flags & pal_abi.OPEN_FLAG_EXCL) != 0;
    const trunc = (flags & pal_abi.OPEN_FLAG_TRUNC) != 0;
    const disposition: u32 = if (create and excl)
        1 // CREATE_NEW
    else if (create and trunc)
        2 // CREATE_ALWAYS
    else if (create)
        4 // OPEN_ALWAYS
    else if (trunc)
        5 // TRUNCATE_EXISTING
    else
        3; // OPEN_EXISTING

    const share: u32 = 0x1 | 0x2 | 0x4; // FILE_SHARE_READ | WRITE | DELETE
    const h = cf(wpath, desired_access, share, null, disposition, 0x80, null);
    if (h == win.INVALID_HANDLE_VALUE or h == null) {
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
        return;
    }

    if ((flags & pal_abi.OPEN_FLAG_APPEND) != 0) {
        if (s.p_set_file_pointer_ex) |sfp| {
            _ = sfp(h, 0, null, 2); // FILE_END
        }
    }

    const he = mem.allocHandle(s) orelse {
        if (s.p_close_handle) |ch| _ = ch(h);
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    he.handle = h;
    he.append_mode = (flags & pal_abi.OPEN_FLAG_APPEND) != 0;
    he.access_mode = @intCast(access_mode);
    mem.completeInline(ov, 0, 0, he.token);
}

pub fn seek(s: *PalState, ov: *pal_abi.Overlapped, handle: u64, offset: i64, whence: u32) void {
    const he = mem.findHandle(s, handle) orelse {
        mem.completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    const sfp = s.p_set_file_pointer_ex orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    if (whence > 2) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    var new_pos: i64 = 0;
    if (sfp(he.handle, offset, &new_pos, whence) == 0) {
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
        return;
    }
    he.seq_pos = @intCast(@max(new_pos, 0));
    mem.completeInline(ov, 0, 0, @intCast(new_pos));
}

pub fn handleTruncate(s: *PalState, ov: *pal_abi.Overlapped, handle: u64, size: i64) void {
    if (size < 0) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const he = mem.findHandle(s, handle) orelse {
        mem.completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    const sfp = s.p_set_file_pointer_ex orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const seo = s.p_set_end_of_file orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    var saved: i64 = 0;
    _ = sfp(he.handle, 0, &saved, 1); // FILE_CURRENT
    if (sfp(he.handle, size, null, 0) == 0 or seo(he.handle) == 0) {
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        const err = win.mapWinError(gle());
        _ = sfp(he.handle, saved, null, 0);
        mem.completeInline(ov, err, 0, 0);
        return;
    }
    _ = sfp(he.handle, saved, null, 0);
    mem.completeInline(ov, 0, 0, 0);
}

pub fn dirRead(s: *PalState, ov: *pal_abi.Overlapped, handle: u64, buf: [*]u8, len: usize) void {
    const he = mem.findHandle(s, handle) orelse {
        mem.completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    if (!he.is_dir or he.dir_path_w == null) {
        mem.completeInline(ov, pal_abi.ERROR_NOTDIR, 0, 0);
        return;
    }
    if (he.dir_done) {
        mem.completeInline(ov, 0, 0, 0);
        return;
    }
    const p_ff = mem.resolveKernel32(s, "FindFirstFileW") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const p_fn = mem.resolveKernel32(s, "FindNextFileW") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const FindFirstFn = *const fn ([*:0]const u16, *win.Win32FindDataW) callconv(win.win_cc) win.HANDLE;
    const FindNextFn = *const fn (win.HANDLE, *win.Win32FindDataW) callconv(win.win_cc) i32;
    const find_first: FindFirstFn = @ptrCast(@alignCast(p_ff));
    const find_next: FindNextFn = @ptrCast(@alignCast(p_fn));

    if (he.dir_find == win.INVALID_HANDLE_VALUE) {
        he.dir_find = find_first(he.dir_path_w.?, &he.dir_find_data);
        if (he.dir_find == win.INVALID_HANDLE_VALUE) {
            he.dir_done = true;
            mem.completeInline(ov, 0, 0, 0);
            return;
        }
        he.dir_first = true;
    }

    var written: usize = 0;
    while (true) {
        if (!he.dir_first) {
            if (find_next(he.dir_find, &he.dir_find_data) == 0) {
                he.dir_done = true;
                break;
            }
        }
        he.dir_first = false;

        var wlen: usize = 0;
        while (wlen < he.dir_find_data.file_name.len and he.dir_find_data.file_name[wlen] != 0) : (wlen += 1) {}
        var name_utf8: [512]u8 = undefined;
        const n = mem.wideToUtf8(s, &he.dir_find_data.file_name, @intCast(wlen), &name_utf8, name_utf8.len);
        if (n == 0) continue;
        if (std.mem.eql(u8, name_utf8[0..n], ".") or std.mem.eql(u8, name_utf8[0..n], "..")) continue;

        const rec_len = 1 + n + 1; // [kind: u8][name][0]
        if (written + rec_len > len) break;
        const kind: u8 = if ((he.dir_find_data.attributes & 0x400) != 0)
            @intCast(pal_abi.STAT_KIND_SYMLINK)
        else if ((he.dir_find_data.attributes & 0x10) != 0)
            @intCast(pal_abi.STAT_KIND_DIR)
        else
            @intCast(pal_abi.STAT_KIND_FILE);

        buf[written] = kind;
        @memcpy(buf[written + 1 ..][0..n], name_utf8[0..n]);
        buf[written + 1 + n] = 0;
        written += rec_len;
    }
    mem.completeInline(ov, 0, 0, written);
}

pub fn pathStat(s: *PalState, ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, _: u32, out: [*]u8, cap: usize) void {
    if (cap < @sizeOf(pal_abi.AbiStat)) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const p_attr = mem.resolveKernel32(s, "GetFileAttributesExW") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const wpath = mem.utf8ToWideAlloc(s, path, len) orelse {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    defer if (s.host.free) |free| free(@ptrCast(@constCast(wpath)));

    const GetAttrFn = *const fn ([*:0]const u16, u32, *win.Win32FileAttributeData) callconv(win.win_cc) i32;
    const get_attr: GetAttrFn = @ptrCast(@alignCast(p_attr));
    var attr: win.Win32FileAttributeData = .{};
    if (get_attr(wpath, 0, &attr) == 0) {
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
        return;
    }

    var st: pal_abi.AbiStat = .{};
    st.kind = if ((attr.attributes & 0x400) != 0)
        pal_abi.STAT_KIND_SYMLINK
    else if ((attr.attributes & 0x10) != 0)
        pal_abi.STAT_KIND_DIR
    else
        pal_abi.STAT_KIND_FILE;
    st.mode = if ((attr.attributes & 0x1) != 0) 0o444 else 0o644;
    if (st.kind == pal_abi.STAT_KIND_DIR) st.mode |= 0o111;
    st.size = (@as(u64, attr.size_high) << 32) | attr.size_low;
    st.mtime_ns = win.fileTimeToUnixNs(attr.last_write_time);
    st.atime_ns = win.fileTimeToUnixNs(attr.last_access_time);
    st.btime_ns = win.fileTimeToUnixNs(attr.creation_time);
    st.ctime_ns = st.mtime_ns;
    st.nlink = 1;
    st.blksize = 4096;
    st.valid_mask = 0x1f;

    const bytes = std.mem.asBytes(&st);
    @memcpy(out[0..bytes.len], bytes);
    mem.completeInline(ov, 0, 0, 0);
}

pub fn pathRemove(s: *PalState, ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, flags: u32) void {
    const wpath = mem.utf8ToWideAlloc(s, path, len) orelse {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    defer if (s.host.free) |free| free(@ptrCast(@constCast(wpath)));
    const is_dir = (flags & pal_abi.PATH_REMOVE_FLAG_DIR) != 0;
    const fn_name: [*:0]const u8 = if (is_dir) "RemoveDirectoryW" else "DeleteFileW";
    const p_fn = mem.resolveKernel32(s, fn_name) orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const DelFn = *const fn ([*:0]const u16) callconv(win.win_cc) i32;
    const del: DelFn = @ptrCast(@alignCast(p_fn));
    if (del(wpath) == 0) {
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
        return;
    }
    mem.completeInline(ov, 0, 0, 0);
}

pub fn pathMkdir(s: *PalState, ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, _: u32) void {
    const wpath = mem.utf8ToWideAlloc(s, path, len) orelse {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    defer if (s.host.free) |free| free(@ptrCast(@constCast(wpath)));
    const p_fn = mem.resolveKernel32(s, "CreateDirectoryW") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const MkdirFn = *const fn ([*:0]const u16, ?*anyopaque) callconv(win.win_cc) i32;
    const mkdir: MkdirFn = @ptrCast(@alignCast(p_fn));
    if (mkdir(wpath, null) == 0) {
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
        return;
    }
    mem.completeInline(ov, 0, 0, 0);
}

pub fn pathRename(s: *PalState, ov: *pal_abi.Overlapped, old_p: [*]const u8, o_len: usize, new_p: [*]const u8, n_len: usize) void {
    const w_old = mem.utf8ToWideAlloc(s, old_p, o_len) orelse {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    defer if (s.host.free) |free| free(@ptrCast(@constCast(w_old)));
    const w_new = mem.utf8ToWideAlloc(s, new_p, n_len) orelse {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    defer if (s.host.free) |free| free(@ptrCast(@constCast(w_new)));
    const p_fn = mem.resolveKernel32(s, "MoveFileExW") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const MoveFn = *const fn ([*:0]const u16, [*:0]const u16, u32) callconv(win.win_cc) i32;
    const mv: MoveFn = @ptrCast(@alignCast(p_fn));
    // MOVEFILE_REPLACE_EXISTING = 1
    if (mv(w_old, w_new, 1) == 0) {
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
        return;
    }
    mem.completeInline(ov, 0, 0, 0);
}

pub fn pathLink(s: *PalState, ov: *pal_abi.Overlapped, src: [*]const u8, s_len: usize, dst: [*]const u8, d_len: usize, _: u32) void {
    const w_src = mem.utf8ToWideAlloc(s, src, s_len) orelse {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    defer if (s.host.free) |free| free(@ptrCast(@constCast(w_src)));
    const w_dst = mem.utf8ToWideAlloc(s, dst, d_len) orelse {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    defer if (s.host.free) |free| free(@ptrCast(@constCast(w_dst)));
    const p_fn = mem.resolveKernel32(s, "CreateHardLinkW") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const LinkFn = *const fn ([*:0]const u16, [*:0]const u16, ?*anyopaque) callconv(win.win_cc) i32;
    const link: LinkFn = @ptrCast(@alignCast(p_fn));
    if (link(w_dst, w_src, null) == 0) {
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
        return;
    }
    mem.completeInline(ov, 0, 0, 0);
}

pub fn pathSymlink(s: *PalState, ov: *pal_abi.Overlapped, target: [*]const u8, t_len: usize, link: [*]const u8, l_len: usize, kind: u32) void {
    const w_tgt = mem.utf8ToWideAlloc(s, target, t_len) orelse {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    defer if (s.host.free) |free| free(@ptrCast(@constCast(w_tgt)));
    const w_lnk = mem.utf8ToWideAlloc(s, link, l_len) orelse {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    defer if (s.host.free) |free| free(@ptrCast(@constCast(w_lnk)));
    const p_fn = mem.resolveKernel32(s, "CreateSymbolicLinkW") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const SymFn = *const fn ([*:0]const u16, [*:0]const u16, u32) callconv(win.win_cc) u8;
    const sym: SymFn = @ptrCast(@alignCast(p_fn));
    // SYMBOLIC_LINK_FLAG_ALLOW_UNPRIVILEGED_CREATE = 2, DIRECTORY = 1
    const flags: u32 = 2 | (if (kind == pal_abi.STAT_KIND_DIR) @as(u32, 1) else 0);
    if (sym(w_lnk, w_tgt, flags) == 0) {
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
        return;
    }
    mem.completeInline(ov, 0, 0, 0);
}
