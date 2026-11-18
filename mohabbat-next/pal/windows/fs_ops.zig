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

    var is_dir = (flags & pal_abi.OPEN_FLAG_DIRECTORY) != 0;
    const create = (flags & pal_abi.OPEN_FLAG_CREATE) != 0;
    if (!is_dir and !create) {
        if (mem.resolveKernel32(s, "GetFileAttributesExW")) |p_attr| {
            const GetAttrFn = *const fn ([*:0]const u16, u32, *win.Win32FileAttributeData) callconv(win.win_cc) i32;
            const get_attr: GetAttrFn = @ptrCast(@alignCast(p_attr));
            var attr: win.Win32FileAttributeData = .{};
            if (get_attr(wpath, 0, &attr) != 0 and (attr.attributes & 0x10) != 0) {
                is_dir = true;
            }
        }
    }
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
    const append = (flags & pal_abi.OPEN_FLAG_APPEND) != 0;
    const write_access: u32 = if (append) 0x0000_0004 else 0x4000_0000; // FILE_APPEND_DATA or GENERIC_WRITE
    const desired_access: u32 = switch (access_mode) {
        0 => 0x8000_0000, // GENERIC_READ
        1 => write_access,
        else => 0x8000_0000 | write_access,
    };

    const create_flag = (flags & pal_abi.OPEN_FLAG_CREATE) != 0;
    const excl = (flags & pal_abi.OPEN_FLAG_EXCL) != 0;
    const trunc = (flags & pal_abi.OPEN_FLAG_TRUNC) != 0;
    const disposition: u32 = if (create_flag and excl)
        1 // CREATE_NEW
    else if (create_flag and trunc)
        2 // CREATE_ALWAYS
    else if (create_flag)
        4 // OPEN_ALWAYS
    else if (trunc)
        5 // TRUNCATE_EXISTING
    else
        3; // OPEN_EXISTING

    const share: u32 = 0x1 | 0x2 | 0x4; // FILE_SHARE_READ | WRITE | DELETE
    const h = cf(wpath, desired_access, share, null, disposition, 0x4000_0080, null);
    if (h == win.INVALID_HANDLE_VALUE or h == null) {
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
        return;
    }

    const associate_iocp = s.p_create_iocp orelse {
        if (s.p_close_handle) |ch| _ = ch(h);
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    if (associate_iocp(h, s.iocp, 0, 0) == null) {
        if (s.p_close_handle) |ch| _ = ch(h);
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
        return;
    }

    const he = mem.allocHandle(s) orelse {
        if (s.p_close_handle) |ch| _ = ch(h);
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    he.handle = h;
    he.is_overlapped = true;
    he.append_mode = append;
    he.access_mode = @intCast(access_mode);
    mem.completeInline(ov, 0, 0, he.token);
}

pub fn seek(s: *PalState, ov: *pal_abi.Overlapped, handle: u64, offset: i64, whence: u32) void {
    const he = mem.findHandle(s, handle) orelse {
        mem.completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    if (he.is_pipe or he.is_socket) {
        mem.completeInline(ov, pal_abi.ERROR_SPIPE, 0, 0);
        return;
    }
    if (whence > 2) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    var base: i64 = 0;
    if (whence == 1) {
        if (he.seq_pos > 0x7fff_ffff_ffff_ffff) {
            mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
            return;
        }
        base = @intCast(he.seq_pos);
    } else if (whence == 2) {
        const get_size = s.p_get_file_size_ex orelse {
            mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        if (get_size(he.handle, &base) == 0) {
            const gle = s.p_get_last_error orelse {
                mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
                return;
            };
            mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
            return;
        }
    }
    const new_pos = @addWithOverflow(base, offset);
    if (new_pos[1] != 0 or new_pos[0] < 0) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    he.seq_pos = @intCast(new_pos[0]);
    mem.completeInline(ov, 0, 0, @intCast(new_pos[0]));
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
    if (he.is_pipe or he.is_socket or he.is_dir) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    if (he.seq_pos > 0x7fff_ffff_ffff_ffff) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const saved: i64 = @intCast(he.seq_pos);
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

pub fn pathStat(s: *PalState, ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, flags: u32, out: [*]u8, cap: usize) void {
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
    var st: pal_abi.AbiStat = .{};
    if ((flags & pal_abi.PATH_STAT_FLAG_NOFOLLOW) != 0) {
        const p_gfa = mem.resolveKernel32(s, "GetFileAttributesW") orelse {
            mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        const GetAttrSimpleFn = *const fn ([*:0]const u16) callconv(win.win_cc) u32;
        const get_attr_simple: GetAttrSimpleFn = @ptrCast(@alignCast(p_gfa));
        const simple = get_attr_simple(wpath);
        if (simple != 0xffff_ffff and (simple & 0x400) != 0) {
            st.kind = pal_abi.STAT_KIND_SYMLINK;
            st.mode = 0o777;
            st.valid_mask = 0x1f;
            const bytes = std.mem.asBytes(&st);
            @memcpy(out[0..bytes.len], bytes);
            mem.completeInline(ov, 0, 0, 0);
            return;
        }
    }
    if (get_attr(wpath, 0, &attr) == 0) {
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
        return;
    }

    st.kind = if ((attr.attributes & 0x400) != 0)
        pal_abi.STAT_KIND_SYMLINK
    else if ((attr.attributes & 0x10) != 0)
        pal_abi.STAT_KIND_DIR
    else
        pal_abi.STAT_KIND_FILE;
    st.mode = if ((attr.attributes & 0x1) != 0) 0o444 else 0o755;
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
    const is_dir = (flags & (pal_abi.PATH_REMOVE_FLAG_DIR | 0x200)) != 0;
    const fn_name: [*:0]const u8 = if (is_dir) "RemoveDirectoryW" else "DeleteFileW";
    const p_fn = mem.resolveKernel32(s, fn_name) orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const DelFn = *const fn ([*:0]const u16) callconv(win.win_cc) i32;
    const del: DelFn = @ptrCast(@alignCast(p_fn));
    if (del(wpath) != 0) {
        mem.completeInline(ov, 0, 0, 0);
        return;
    }

    const get_error = s.p_get_last_error orelse {
        mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
        return;
    };
    const delete_error = get_error();
    if (!is_dir and delete_error == 5) {
        const get_attr_ptr = mem.resolveKernel32(s, "GetFileAttributesW");
        const set_attr_ptr = mem.resolveKernel32(s, "SetFileAttributesW");
        if (get_attr_ptr != null and set_attr_ptr != null) {
            const GetAttrFn = *const fn ([*:0]const u16) callconv(win.win_cc) u32;
            const SetAttrFn = *const fn ([*:0]const u16, u32) callconv(win.win_cc) i32;
            const get_attr: GetAttrFn = @ptrCast(@alignCast(get_attr_ptr.?));
            const set_attr: SetAttrFn = @ptrCast(@alignCast(set_attr_ptr.?));
            const attributes = get_attr(wpath);
            if (attributes != 0xffff_ffff and (attributes & 0x1) != 0) {
                if (set_attr(wpath, attributes & ~@as(u32, 0x1)) != 0) {
                    if (del(wpath) != 0) {
                        mem.completeInline(ov, 0, 0, 0);
                        return;
                    }
                    const retry_error = get_error();
                    _ = set_attr(wpath, attributes);
                    mem.completeInline(ov, win.mapWinError(retry_error), 0, 0);
                    return;
                }
                const attr_error = get_error();
                mem.completeInline(ov, win.mapWinError(attr_error), 0, 0);
                return;
            }
        }
    }
    const mapped_error = if (delete_error == 0) pal_abi.ERROR_IO else win.mapWinError(delete_error);
    mem.completeInline(ov, mapped_error, 0, 0);
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

pub fn pathChmod(s: *PalState, ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, mode: u32) void {
    const wpath = mem.utf8ToWideAlloc(s, path, len) orelse {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    defer if (s.host.free) |free| free(@ptrCast(@constCast(wpath)));

    const p_get = mem.resolveKernel32(s, "GetFileAttributesW") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const p_set = mem.resolveKernel32(s, "SetFileAttributesW") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const GetAttrW = *const fn ([*:0]const u16) callconv(win.win_cc) u32;
    const SetAttrW = *const fn ([*:0]const u16, u32) callconv(win.win_cc) i32;
    const get_attr: GetAttrW = @ptrCast(@alignCast(p_get));
    const set_attr: SetAttrW = @ptrCast(@alignCast(p_set));

    const cur = get_attr(wpath);
    if (cur == 0xffff_ffff) {
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
        return;
    }
    const next_attr: u32 = if ((mode & 0o200) == 0) (cur | 0x1) else (cur & ~@as(u32, 0x1));
    if (set_attr(wpath, next_attr) == 0) {
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
        return;
    }
    mem.completeInline(ov, 0, 0, 0);
}

fn unixToFileTime(s: *PalState, sec: i64, nsec: u32) ?win.FileTime {
    if (nsec == 0x3ffffffe) return null; // UTIME_OMIT
    if (nsec == 0x3fffffff) { // UTIME_NOW
        var ft: win.FileTime = .{};
        if (s.p_get_system_time_ft) |gst| gst(&ft);
        return ft;
    }
    const ticks: i128 = @as(i128, sec) * 10_000_000 + @as(i128, @intCast(nsec / 100)) + 116_444_736_000_000_000;
    const u: u64 = if (ticks < 0) 0 else @intCast(ticks);
    return .{
        .low = @truncate(u),
        .high = @truncate(u >> 32),
    };
}

pub fn pathSetTimes(
    s: *PalState,
    ov: *pal_abi.Overlapped,
    path: [*]const u8,
    len: usize,
    at_sec: i64,
    at_nsec: u32,
    mt_sec: i64,
    mt_nsec: u32,
    flags: u32,
) void {
    const cf = s.p_create_file_w orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const p_sft = mem.resolveKernel32(s, "SetFileTime") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const wpath = mem.utf8ToWideAlloc(s, path, len) orelse {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    defer if (s.host.free) |free| free(@ptrCast(@constCast(wpath)));

    var win_flags: u32 = 0x0200_0000; // FILE_FLAG_BACKUP_SEMANTICS
    if ((flags & pal_abi.PATH_SET_TIMES_FLAG_NOFOLLOW) != 0) {
        win_flags |= 0x0020_0000; // FILE_FLAG_OPEN_REPARSE_POINT
    }
    const h = cf(wpath, 0x0100, 0x7, null, 3, win_flags, null); // FILE_WRITE_ATTRIBUTES
    if (h == win.INVALID_HANDLE_VALUE or h == null) {
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
        return;
    }
    defer if (s.p_close_handle) |ch| {
        _ = ch(h);
    };

    var at_ft = unixToFileTime(s, at_sec, at_nsec);
    var mt_ft = unixToFileTime(s, mt_sec, mt_nsec);
    const SetFileTimeFn = *const fn (win.HANDLE, ?*const win.FileTime, ?*const win.FileTime, ?*const win.FileTime) callconv(win.win_cc) i32;
    const sft: SetFileTimeFn = @ptrCast(@alignCast(p_sft));
    if (sft(h, null, if (at_ft) |*p| p else null, if (mt_ft) |*p| p else null) == 0) {
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

pub fn pathReadlink(s: *PalState, ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, out: [*]u8, cap: usize) void {
    const cf = s.p_create_file_w orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const p_dev = mem.resolveKernel32(s, "DeviceIoControl") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const wpath = mem.utf8ToWideAlloc(s, path, len) orelse {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    defer if (s.host.free) |free| free(@ptrCast(@constCast(wpath)));

    // FILE_FLAG_OPEN_REPARSE_POINT (0x00200000) | FILE_FLAG_BACKUP_SEMANTICS (0x02000000)
    const h = cf(wpath, 0, 0x7, null, 3, 0x0220_0000, null);
    if (h == win.INVALID_HANDLE_VALUE or h == null) {
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
        return;
    }
    defer if (s.p_close_handle) |ch| {
        _ = ch(h);
    };

    const DevIoFn = *const fn (win.HANDLE, u32, ?*anyopaque, u32, [*]u8, u32, *u32, ?*anyopaque) callconv(win.win_cc) i32;
    const dev_io: DevIoFn = @ptrCast(@alignCast(p_dev));
    var rbuf: [4096]u8 align(4) = undefined;
    var returned: u32 = 0;
    // FSCTL_GET_REPARSE_POINT = 0x000900a8
    if (dev_io(h, 0x0009_00a8, null, 0, &rbuf, rbuf.len, &returned, null) == 0 or returned < 16) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }

    const tag = std.mem.readInt(u32, rbuf[0..4], .little);
    var name_off_bytes: usize = 0;
    var name_len_bytes: usize = 0;
    var path_buf_start: usize = 0;
    if (tag == 0xA000_000C and returned >= 20) {
        // IO_REPARSE_TAG_SYMLINK: PrintNameOffset (12..14), PrintNameLength (14..16), SubstituteNameOffset (8..10), SubstituteNameLength (10..12)
        const print_off = std.mem.readInt(u16, rbuf[12..14], .little);
        const print_len = std.mem.readInt(u16, rbuf[14..16], .little);
        const sub_off = std.mem.readInt(u16, rbuf[8..10], .little);
        const sub_len = std.mem.readInt(u16, rbuf[10..12], .little);
        path_buf_start = 20;
        if (print_len > 0) {
            name_off_bytes = print_off;
            name_len_bytes = print_len;
        } else {
            name_off_bytes = sub_off;
            name_len_bytes = sub_len;
        }
    } else if (tag == 0xA000_0003 and returned >= 16) {
        // IO_REPARSE_TAG_MOUNT_POINT
        const print_off = std.mem.readInt(u16, rbuf[12..14], .little);
        const print_len = std.mem.readInt(u16, rbuf[14..16], .little);
        path_buf_start = 16;
        name_off_bytes = print_off;
        name_len_bytes = print_len;
    } else {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }

    const abs_off = path_buf_start + name_off_bytes;
    if (abs_off + name_len_bytes > returned or (name_len_bytes % 2) != 0) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const wlen = name_len_bytes / 2;
    const wptr: [*]const u16 = @ptrCast(@alignCast(&rbuf[abs_off]));
    const n = mem.wideToUtf8(s, wptr, @intCast(wlen), out, cap);
    mem.completeInline(ov, 0, 0, n);
}
