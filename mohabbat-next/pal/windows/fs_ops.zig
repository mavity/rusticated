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
        if (written + rec_len > len) {
            he.dir_first = true;
            break;
        }
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
    if ((flags & pal_abi.PATH_STAT_FLAG_NOFOLLOW) == 0 and (attr.attributes & 0x400) != 0) {
        const create_file = s.p_create_file_w orelse {
            mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        const p_info = mem.resolveKernel32(s, "GetFileInformationByHandle") orelse {
            mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        const handle = create_file(wpath, 0x80, 0x7, null, 3, 0x0200_0000, null);
        if (handle == win.INVALID_HANDLE_VALUE or handle == null) {
            const gle = s.p_get_last_error orelse {
                mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
                return;
            };
            mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
            return;
        }
        defer if (s.p_close_handle) |close_handle| {
            _ = close_handle(handle);
        };
        const GetInfoFn = *const fn (win.HANDLE, *Win32ByHandleFileInformation) callconv(win.win_cc) i32;
        const get_info: GetInfoFn = @ptrCast(@alignCast(p_info));
        var info: Win32ByHandleFileInformation = .{};
        if (get_info(handle, &info) == 0) {
            const gle = s.p_get_last_error orelse {
                mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
                return;
            };
            mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
            return;
        }
        attr = .{
            .attributes = info.file_attributes,
            .creation_time = info.creation_time,
            .last_access_time = info.last_access_time,
            .last_write_time = info.last_write_time,
            .size_high = info.file_size_high,
            .size_low = info.file_size_low,
        };
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

const Win32ByHandleFileInformation = extern struct {
    file_attributes: u32 = 0,
    creation_time: win.FileTime = .{},
    last_access_time: win.FileTime = .{},
    last_write_time: win.FileTime = .{},
    volume_serial_number: u32 = 0,
    file_size_high: u32 = 0,
    file_size_low: u32 = 0,
    number_of_links: u32 = 0,
    file_index_high: u32 = 0,
    file_index_low: u32 = 0,
};

const Win32FileBasicInfo = extern struct {
    creation_time: i64 = 0,
    last_access_time: i64 = 0,
    last_write_time: i64 = 0,
    change_time: i64 = 0,
    file_attributes: u32 = 0,
};

fn fileTimeToInt64(time: win.FileTime) i64 {
    const raw = (@as(u64, time.high) << 32) | time.low;
    return @bitCast(raw);
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

    const create_file = s.p_create_file_w orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const p_get_info = mem.resolveKernel32(s, "GetFileInformationByHandle") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const p_set_info = mem.resolveKernel32(s, "SetFileInformationByHandle") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const handle = create_file(wpath, 0x0180, 0x7, null, 3, 0x0200_0000, null);
    if (handle == win.INVALID_HANDLE_VALUE or handle == null) {
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
        return;
    }
    defer if (s.p_close_handle) |close_handle| {
        _ = close_handle(handle);
    };

    const GetInfoFn = *const fn (win.HANDLE, *Win32ByHandleFileInformation) callconv(win.win_cc) i32;
    const get_info: GetInfoFn = @ptrCast(@alignCast(p_get_info));
    var info: Win32ByHandleFileInformation = .{};
    if (get_info(handle, &info) == 0) {
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
        return;
    }

    const next_attr: u32 = if ((mode & 0o200) == 0)
        (info.file_attributes & ~@as(u32, 0x80)) | 0x1
    else
        info.file_attributes & ~@as(u32, 0x1);
    if (next_attr != info.file_attributes) {
        const SetInfoFn = *const fn (win.HANDLE, u32, *const Win32FileBasicInfo, u32) callconv(win.win_cc) i32;
        const set_info: SetInfoFn = @ptrCast(@alignCast(p_set_info));
        const basic = Win32FileBasicInfo{
            .creation_time = fileTimeToInt64(info.creation_time),
            .last_access_time = fileTimeToInt64(info.last_access_time),
            .last_write_time = fileTimeToInt64(info.last_write_time),
            .change_time = 0,
            .file_attributes = if (next_attr == 0 or next_attr == 0x80) 0x80 else next_attr,
        };
        if (set_info(handle, 0, &basic, @sizeOf(Win32FileBasicInfo)) == 0) {
            const gle = s.p_get_last_error orelse {
                mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
                return;
            };
            mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
            return;
        }
    }
    mem.completeInline(ov, 0, 0, 0);
}

const PathTimesFlag = struct {
    const set_atime: u32 = 0x01;
    const atime_now: u32 = 0x02;
    const set_mtime: u32 = 0x04;
    const mtime_now: u32 = 0x08;
    const nofollow: u32 = pal_abi.PATH_SET_TIMES_FLAG_NOFOLLOW;
    const known: u32 = set_atime | atime_now | set_mtime | mtime_now | nofollow;
};

fn unixToFileTime(sec: i64, nsec: u32) ?win.FileTime {
    if (nsec >= 1_000_000_000) return null;
    const ticks: i128 = @as(i128, sec) * 10_000_000 + @as(i128, @intCast(nsec / 100)) + 116_444_736_000_000_000;
    if (ticks > std.math.maxInt(u64)) return null;
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
    if (flags & ~PathTimesFlag.known != 0 or
        flags & (PathTimesFlag.set_atime | PathTimesFlag.atime_now) == (PathTimesFlag.set_atime | PathTimesFlag.atime_now) or
        flags & (PathTimesFlag.set_mtime | PathTimesFlag.mtime_now) == (PathTimesFlag.set_mtime | PathTimesFlag.mtime_now) or
        (flags & PathTimesFlag.set_atime != 0 and at_nsec >= 1_000_000_000) or
        (flags & PathTimesFlag.set_mtime != 0 and mt_nsec >= 1_000_000_000))
    {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    var now_ft: ?win.FileTime = null;
    if (flags & (PathTimesFlag.atime_now | PathTimesFlag.mtime_now) != 0) {
        const get_system_time = s.p_get_system_time_ft orelse {
            mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        var current: win.FileTime = .{};
        get_system_time(&current);
        now_ft = current;
    }
    const at_ft: ?win.FileTime = if (flags & PathTimesFlag.set_atime != 0)
        unixToFileTime(at_sec, at_nsec)
    else if (flags & PathTimesFlag.atime_now != 0)
        now_ft
    else
        null;
    const mt_ft: ?win.FileTime = if (flags & PathTimesFlag.set_mtime != 0)
        unixToFileTime(mt_sec, mt_nsec)
    else if (flags & PathTimesFlag.mtime_now != 0)
        now_ft
    else
        null;
    if ((flags & PathTimesFlag.set_atime != 0 and at_ft == null) or
        (flags & PathTimesFlag.set_mtime != 0 and mt_ft == null))
    {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
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
    if ((flags & PathTimesFlag.nofollow) != 0) {
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

pub fn pathLink(s: *PalState, ov: *pal_abi.Overlapped, src: [*]const u8, s_len: usize, dst: [*]const u8, d_len: usize, flags: u32) void {
    if (flags & ~@as(u32, 1) != 0) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
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
    var link_source: [*:0]const u16 = w_src;
    var resolved_path: [32768]u16 = undefined;
    if (flags & 1 != 0) {
        const create_file = s.p_create_file_w orelse {
            mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        const close_handle = s.p_close_handle orelse {
            mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        const p_final_path = mem.resolveKernel32(s, "GetFinalPathNameByHandleW") orelse {
            mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        const GetFinalPathFn = *const fn (win.HANDLE, [*]u16, u32, u32) callconv(win.win_cc) u32;
        const get_final_path: GetFinalPathFn = @ptrCast(@alignCast(p_final_path));
        const handle = create_file(w_src, 0, 0x7, null, 3, 0x0200_0000, null);
        if (handle == win.INVALID_HANDLE_VALUE or handle == null) {
            const gle = s.p_get_last_error orelse {
                mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
                return;
            };
            mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
            return;
        }
        defer _ = close_handle(handle);
        const resolved_len = get_final_path(handle, &resolved_path, @intCast(resolved_path.len), 0);
        if (resolved_len == 0 or resolved_len >= @as(u32, @intCast(resolved_path.len))) {
            const gle = s.p_get_last_error orelse {
                mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
                return;
            };
            mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
            return;
        }
        link_source = @ptrCast(&resolved_path);
    }
    if (link(w_dst, link_source, null) == 0) {
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
    if (cap == 0) {
        mem.completeInline(ov, 0, 0, 0);
        return;
    }
    if (s.p_wide_to_mb == null) {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    }
    var utf8: [8192]u8 = undefined;
    const full_len = mem.wideToUtf8(s, wptr, @intCast(wlen), &utf8, utf8.len);
    if (full_len == 0) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const copied = @min(full_len, cap);
    @memcpy(out[0..copied], utf8[0..copied]);
    mem.completeInline(ov, 0, 0, copied);
}

fn testSystemTime(out: *win.FileTime) callconv(win.win_cc) void {
    out.* = .{ .low = 0x1234_5678, .high = 0x9abc_def0 };
}

test "Windows filesystem timestamps convert explicit Unix times" {
    const epoch = unixToFileTime(0, 999);
    try std.testing.expect(epoch != null);
    const epoch_raw = (@as(u64, epoch.?.high) << 32) | epoch.?.low;
    try std.testing.expectEqual(@as(u64, 116_444_736_000_000_009), epoch_raw);

    const before_unix_epoch = unixToFileTime(-315_619_200, 0);
    try std.testing.expect(before_unix_epoch != null);
    try std.testing.expectEqual(@as(i64, -315_619_200_000_000_000), win.fileTimeToUnixNs(before_unix_epoch.?));

    const before_windows_epoch = unixToFileTime(-11_644_473_601, 0);
    try std.testing.expect(before_windows_epoch != null);
    try std.testing.expectEqual(@as(u64, 0), (@as(u64, before_windows_epoch.?.high) << 32) | before_windows_epoch.?.low);

    try std.testing.expect(unixToFileTime(0, 1_000_000_000) == null);
    try std.testing.expect(unixToFileTime(std.math.maxInt(i64), 0) == null);
}

test "Windows filesystem seek validates handles and offsets without OS calls" {
    var state: PalState = .{};
    const handle = mem.allocHandle(&state).?;
    handle.seq_pos = 8;
    var ov: pal_abi.Overlapped = .{};

    seek(&state, &ov, handle.token, 5, 1);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(u64, 13), ov.result_ext);
    try std.testing.expectEqual(@as(u64, 13), handle.seq_pos);

    seek(&state, &ov, handle.token, -20, 1);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    try std.testing.expectEqual(@as(u64, 13), handle.seq_pos);

    seek(&state, &ov, handle.token, 0, 3);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    seek(&state, &ov, 0, 0, 0);
    try std.testing.expectEqual(pal_abi.ERROR_BADF, ov.@"error");

    handle.is_pipe = true;
    seek(&state, &ov, handle.token, 0, 0);
    try std.testing.expectEqual(pal_abi.ERROR_SPIPE, ov.@"error");
}

var test_file_size: i64 = 0;
var test_file_size_success = true;
var test_native_error: u32 = 0;
var test_set_pointer_success = true;
var test_set_eof_success = true;
var test_set_pointer_calls: usize = 0;
var test_set_pointer_offsets: [4]i64 = @splat(0);

fn testGetFileSize(_: win.HANDLE, size: *i64) callconv(win.win_cc) i32 {
    size.* = test_file_size;
    return @intFromBool(test_file_size_success);
}

fn testGetLastError() callconv(win.win_cc) u32 {
    return test_native_error;
}

fn testSetFilePointer(_: win.HANDLE, offset: i64, _: ?*i64, _: u32) callconv(win.win_cc) i32 {
    if (test_set_pointer_calls < test_set_pointer_offsets.len) {
        test_set_pointer_offsets[test_set_pointer_calls] = offset;
    }
    test_set_pointer_calls += 1;
    return @intFromBool(test_set_pointer_success);
}

fn testSetEndOfFile(_: win.HANDLE) callconv(win.win_cc) i32 {
    return @intFromBool(test_set_eof_success);
}

var test_path_wide_storage: [65536]u8 align(2) = undefined;
var test_path_create_access: u32 = 0;
var test_path_create_disposition: u32 = 0;
var test_path_create_calls: usize = 0;
var test_path_create_success = true;
var test_path_iocp_success = true;
var test_path_close_calls: usize = 0;
var test_attr_data: win.Win32FileAttributeData = .{};
var test_attr_success = true;
var test_attr_api_available = true;
var test_simple_attr_api_available = true;
var test_simple_attributes: u32 = 0;
var test_handle_info: Win32ByHandleFileInformation = .{};
var test_handle_info_success = true;
var test_handle_info_api_available = true;
var test_handle_info_calls: usize = 0;
var test_find_api_available = true;
var test_wide_to_mb_success = true;
var test_find_entries: [8]win.Win32FindDataW = @splat(.{});
var test_find_count: usize = 0;
var test_find_index: usize = 0;

fn testPathAlloc(size: usize) callconv(win.win_cc) ?*anyopaque {
    if (size > test_path_wide_storage.len) return null;
    return @ptrCast(&test_path_wide_storage);
}

fn testPathFree(_: ?*anyopaque) callconv(win.win_cc) void {}

fn testMbToWide(_: u32, _: u32, input: [*]const u8, input_len: i32, out: ?[*]u16, out_cap: i32) callconv(win.win_cc) i32 {
    if (input_len <= 0) return 0;
    const len: usize = @intCast(input_len);
    if (out) |wide| {
        if (out_cap < input_len) return 0;
        for (0..len) |i| wide[i] = input[i];
    }
    return input_len;
}

fn testCreateFile(
    _: [*:0]const u16,
    access: u32,
    _: u32,
    _: ?*anyopaque,
    disposition: u32,
    _: u32,
    _: win.HANDLE,
) callconv(win.win_cc) win.HANDLE {
    test_path_create_access = access;
    test_path_create_disposition = disposition;
    test_path_create_calls += 1;
    return if (test_path_create_success) @ptrFromInt(0x10) else win.INVALID_HANDLE_VALUE;
}

fn testAssociateIocp(_: win.HANDLE, _: win.HANDLE, _: usize, _: u32) callconv(win.win_cc) win.HANDLE {
    return if (test_path_iocp_success) @ptrFromInt(0x20) else null;
}

fn testCloseHandle(_: win.HANDLE) callconv(win.win_cc) i32 {
    test_path_close_calls += 1;
    return 1;
}

fn testGetFileAttributesEx(_: [*:0]const u16, _: u32, out: *win.Win32FileAttributeData) callconv(win.win_cc) i32 {
    out.* = test_attr_data;
    return @intFromBool(test_attr_success);
}

fn testGetFileAttributes(_: [*:0]const u16) callconv(win.win_cc) u32 {
    return test_simple_attributes;
}

fn testGetFileInformationByHandle(_: win.HANDLE, out: *Win32ByHandleFileInformation) callconv(win.win_cc) i32 {
    test_handle_info_calls += 1;
    out.* = test_handle_info;
    return @intFromBool(test_handle_info_success);
}

fn testFindFirst(_: [*:0]const u16, out: *win.Win32FindDataW) callconv(win.win_cc) win.HANDLE {
    test_find_index = 0;
    if (test_find_count == 0) return win.INVALID_HANDLE_VALUE;
    out.* = test_find_entries[0];
    test_find_index = 1;
    return @ptrFromInt(0x30);
}

fn testFindNext(_: win.HANDLE, out: *win.Win32FindDataW) callconv(win.win_cc) i32 {
    if (test_find_index >= test_find_count) return 0;
    out.* = test_find_entries[test_find_index];
    test_find_index += 1;
    return 1;
}

fn testWideToMultiByte(
    _: u32,
    _: u32,
    wide: [*]const u16,
    wide_len: i32,
    out: ?[*]u8,
    out_cap: i32,
    _: ?[*]const u8,
    _: ?*i32,
) callconv(win.win_cc) i32 {
    if (!test_wide_to_mb_success or wide_len < 0) return 0;
    if (out) |bytes| {
        if (out_cap < wide_len) return 0;
        for (0..@intCast(wide_len)) |i| {
            if (wide[i] > 0x7f) return 0;
            bytes[i] = @intCast(wide[i]);
        }
    }
    return wide_len;
}

fn testGetProcAddress(_: ?*anyopaque, name: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
    const symbol = std.mem.sliceTo(name, 0);
    if (std.mem.eql(u8, symbol, "GetFileAttributesExW") and test_attr_api_available) {
        return @ptrCast(@constCast(&testGetFileAttributesEx));
    }
    if (std.mem.eql(u8, symbol, "GetFileAttributesW") and test_simple_attr_api_available) {
        return @ptrCast(@constCast(&testGetFileAttributes));
    }
    if (std.mem.eql(u8, symbol, "GetFileInformationByHandle") and test_handle_info_api_available) {
        return @ptrCast(@constCast(&testGetFileInformationByHandle));
    }
    if (std.mem.eql(u8, symbol, "FindFirstFileW") and test_find_api_available) {
        return @ptrCast(@constCast(&testFindFirst));
    }
    if (std.mem.eql(u8, symbol, "FindNextFileW") and test_find_api_available) {
        return @ptrCast(@constCast(&testFindNext));
    }
    return null;
}

fn initPathConversion(s: *PalState) void {
    s.host.alloc = testPathAlloc;
    s.host.free = testPathFree;
    s.p_mb_to_wide = testMbToWide;
}

fn initPathStatState(s: *PalState) void {
    initPathConversion(s);
    s.kernel32 = @ptrFromInt(1);
    s.host.get_proc_address = testGetProcAddress;
    s.p_get_last_error = testGetLastError;
    s.p_create_file_w = testCreatePathHandle;
    s.p_close_handle = testCloseHandle;
}

fn initDirectoryState(s: *PalState, path: [*:0]u16) *mem.HandleEntry {
    s.kernel32 = @ptrFromInt(1);
    s.host.get_proc_address = testGetProcAddress;
    s.p_wide_to_mb = testWideToMultiByte;
    const handle = mem.allocHandle(s).?;
    handle.is_dir = true;
    handle.dir_path_w = path;
    return handle;
}

fn makeFindEntry(name: []const u8, attributes: u32) win.Win32FindDataW {
    var entry: win.Win32FindDataW = .{ .attributes = attributes };
    for (name, 0..) |byte, i| entry.file_name[i] = byte;
    return entry;
}

test "Windows filesystem seek covers origins overflow and native file-size errors" {
    var state: PalState = .{};
    const handle = mem.allocHandle(&state).?;
    handle.handle = @ptrFromInt(0x10);
    var ov: pal_abi.Overlapped = .{};

    seek(&state, &ov, handle.token, 12, 0);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(u64, 12), ov.result_ext);
    try std.testing.expectEqual(@as(u64, 12), handle.seq_pos);

    state.p_get_file_size_ex = testGetFileSize;
    test_file_size = 100;
    test_file_size_success = true;
    seek(&state, &ov, handle.token, -5, 2);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(u64, 95), ov.result_ext);

    seek(&state, &ov, handle.token, 5, 2);
    try std.testing.expectEqual(@as(u64, 105), ov.result_ext);

    state.p_get_file_size_ex = null;
    seek(&state, &ov, handle.token, 0, 2);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");

    state.p_get_file_size_ex = testGetFileSize;
    test_file_size_success = false;
    test_native_error = 5;
    state.p_get_last_error = testGetLastError;
    seek(&state, &ov, handle.token, 0, 2);
    try std.testing.expectEqual(pal_abi.ERROR_ACCES, ov.@"error");

    test_file_size_success = true;
    handle.seq_pos = 0x7fff_ffff_ffff_ffff;
    seek(&state, &ov, handle.token, 1, 1);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    try std.testing.expectEqual(@as(u64, 0x7fff_ffff_ffff_ffff), handle.seq_pos);

    handle.seq_pos = 0x8000_0000_0000_0000;
    seek(&state, &ov, handle.token, 0, 1);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    handle.is_socket = true;
    seek(&state, &ov, handle.token, 0, 0);
    try std.testing.expectEqual(pal_abi.ERROR_SPIPE, ov.@"error");
}

test "Windows filesystem truncate validates handles and restores sequential offsets" {
    var state: PalState = .{};
    const handle = mem.allocHandle(&state).?;
    handle.handle = @ptrFromInt(0x10);
    handle.seq_pos = 17;
    var ov: pal_abi.Overlapped = .{};

    handleTruncate(&state, &ov, handle.token, -1);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    handleTruncate(&state, &ov, 0xffff, 0);
    try std.testing.expectEqual(pal_abi.ERROR_BADF, ov.@"error");

    handleTruncate(&state, &ov, handle.token, 0);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");

    state.p_set_file_pointer_ex = testSetFilePointer;
    handleTruncate(&state, &ov, handle.token, 0);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");

    state.p_set_end_of_file = testSetEndOfFile;
    handle.is_pipe = true;
    handleTruncate(&state, &ov, handle.token, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    handle.is_pipe = false;

    handle.is_socket = true;
    handleTruncate(&state, &ov, handle.token, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    handle.is_socket = false;

    handle.is_dir = true;
    handleTruncate(&state, &ov, handle.token, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    handle.is_dir = false;

    handle.seq_pos = 0x8000_0000_0000_0000;
    handleTruncate(&state, &ov, handle.token, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    handle.seq_pos = 17;

    test_set_pointer_calls = 0;
    test_set_pointer_success = true;
    test_set_eof_success = true;
    handleTruncate(&state, &ov, handle.token, 64);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(usize, 2), test_set_pointer_calls);
    try std.testing.expectEqual(@as(i64, 64), test_set_pointer_offsets[0]);
    try std.testing.expectEqual(@as(i64, 17), test_set_pointer_offsets[1]);

    test_set_pointer_calls = 0;
    test_set_eof_success = false;
    test_native_error = 5;
    state.p_get_last_error = testGetLastError;
    handleTruncate(&state, &ov, handle.token, 32);
    try std.testing.expectEqual(pal_abi.ERROR_ACCES, ov.@"error");
    try std.testing.expectEqual(@as(usize, 2), test_set_pointer_calls);
    try std.testing.expectEqual(@as(i64, 17), test_set_pointer_offsets[1]);

    test_set_pointer_calls = 0;
    test_set_pointer_success = false;
    state.p_get_last_error = null;
    handleTruncate(&state, &ov, handle.token, 16);
    try std.testing.expectEqual(pal_abi.ERROR_IO, ov.@"error");
    try std.testing.expectEqual(@as(usize, 1), test_set_pointer_calls);
}

test "Windows filesystem path_open maps access and creation flags" {
    const Case = struct { flags: u32, access: u32, disposition: u32, append: bool };
    const cases = [_]Case{
        .{ .flags = 0, .access = 0x8000_0000, .disposition = 3, .append = false },
        .{ .flags = 1, .access = 0x4000_0000, .disposition = 3, .append = false },
        .{ .flags = 2, .access = 0xc000_0000, .disposition = 3, .append = false },
        .{ .flags = 3, .access = 0xc000_0000, .disposition = 3, .append = false },
        .{ .flags = pal_abi.OPEN_FLAG_CREATE, .access = 0x8000_0000, .disposition = 4, .append = false },
        .{ .flags = pal_abi.OPEN_FLAG_CREATE | pal_abi.OPEN_FLAG_EXCL, .access = 0x8000_0000, .disposition = 1, .append = false },
        .{ .flags = pal_abi.OPEN_FLAG_CREATE | pal_abi.OPEN_FLAG_TRUNC, .access = 0x8000_0000, .disposition = 2, .append = false },
        .{ .flags = pal_abi.OPEN_FLAG_TRUNC, .access = 0x8000_0000, .disposition = 5, .append = false },
        .{ .flags = 1 | pal_abi.OPEN_FLAG_APPEND, .access = 0x4, .disposition = 3, .append = true },
        .{ .flags = 1 | pal_abi.OPEN_FLAG_APPEND | pal_abi.OPEN_FLAG_CREATE, .access = 0x4, .disposition = 4, .append = true },
    };

    for (cases) |case| {
        var state: PalState = .{};
        initPathConversion(&state);
        state.p_create_file_w = testCreateFile;
        state.p_create_iocp = testAssociateIocp;
        test_path_create_calls = 0;
        test_path_create_success = true;
        test_path_iocp_success = true;

        var ov: pal_abi.Overlapped = .{};
        pathOpen(&state, &ov, "x", 1, case.flags);
        try std.testing.expectEqual(@as(u32, 0), ov.@"error");
        try std.testing.expectEqual(@as(usize, 1), test_path_create_calls);
        try std.testing.expectEqual(case.access, test_path_create_access);
        try std.testing.expectEqual(case.disposition, test_path_create_disposition);
        const handle = mem.findHandle(&state, ov.result_ext).?;
        try std.testing.expectEqual(case.append, handle.append_mode);
        try std.testing.expectEqual(@as(u8, @truncate(case.flags & 3)), handle.access_mode);
    }

    var state: PalState = .{};
    var ov: pal_abi.Overlapped = .{};
    pathOpen(&state, &ov, "x", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");

    initPathConversion(&state);
    state.p_create_file_w = testCreateFile;
    state.p_close_handle = testCloseHandle;
    pathOpen(&state, &ov, "", 0, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    state.p_get_last_error = testGetLastError;
    test_native_error = 2;
    test_path_create_success = false;
    pathOpen(&state, &ov, "x", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_NOENT, ov.@"error");

    test_path_create_success = true;
    state.p_create_iocp = null;
    test_path_close_calls = 0;
    pathOpen(&state, &ov, "x", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    try std.testing.expectEqual(@as(usize, 1), test_path_close_calls);

    state.p_create_iocp = testAssociateIocp;
    test_path_iocp_success = false;
    test_native_error = 5;
    pathOpen(&state, &ov, "x", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_ACCES, ov.@"error");
    try std.testing.expectEqual(@as(usize, 2), test_path_close_calls);
}

test "Windows filesystem path_stat validates output and maps file metadata" {
    const size = @sizeOf(pal_abi.AbiStat);
    var out: [@sizeOf(pal_abi.AbiStat) + 4]u8 align(@alignOf(pal_abi.AbiStat)) = @splat(0xcc);
    var state: PalState = .{};
    initPathStatState(&state);
    var ov: pal_abi.Overlapped = .{};

    pathStat(&state, &ov, "x", 1, 0, &out, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    try std.testing.expectEqual(@as(u8, 0xcc), out[0]);

    pathStat(&state, &ov, "x", 1, 0, &out, size - 1);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    try std.testing.expectEqual(@as(u8, 0xcc), out[0]);

    test_attr_data = .{ .size_high = 2, .size_low = 4 };
    test_attr_success = true;
    pathStat(&state, &ov, "x", 1, 0, &out, size);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    const regular: *const pal_abi.AbiStat = @ptrCast(@alignCast(&out));
    try std.testing.expectEqual(pal_abi.STAT_KIND_FILE, regular.kind);
    try std.testing.expectEqual(@as(u64, 0x2_0000_0004), regular.size);
    try std.testing.expectEqual(@as(u32, 0o755), regular.mode);

    @memset(&out, 0xcc);
    test_attr_data.attributes = 0x1;
    pathStat(&state, &ov, "x", 1, 0, &out, size);
    const read_only: *const pal_abi.AbiStat = @ptrCast(@alignCast(&out));
    try std.testing.expectEqual(@as(u32, 0o444), read_only.mode);

    test_attr_data.attributes = 0x10;
    pathStat(&state, &ov, "x", 1, 0, &out, size);
    const directory: *const pal_abi.AbiStat = @ptrCast(@alignCast(&out));
    try std.testing.expectEqual(pal_abi.STAT_KIND_DIR, directory.kind);
    try std.testing.expectEqual(@as(u32, 0o755), directory.mode);

    test_attr_data.attributes = 0x400;
    test_handle_info = .{
        .file_attributes = 0,
        .file_size_high = 1,
        .file_size_low = 9,
    };
    test_handle_info_success = true;
    test_handle_info_calls = 0;
    test_path_api_success = true;
    test_path_close_calls = 0;
    pathStat(&state, &ov, "x", 1, 0, &out, size);
    const followed_link: *const pal_abi.AbiStat = @ptrCast(@alignCast(&out));
    try std.testing.expectEqual(pal_abi.STAT_KIND_FILE, followed_link.kind);
    try std.testing.expectEqual(@as(u64, 0x1_0000_0009), followed_link.size);
    try std.testing.expectEqual(@as(usize, 1), test_handle_info_calls);
    try std.testing.expectEqual(@as(u32, 0x0200_0000), test_path_api_create_flags);
    try std.testing.expectEqual(@as(usize, 1), test_path_close_calls);

    test_simple_attributes = 0x400;
    pathStat(&state, &ov, "x", 1, pal_abi.PATH_STAT_FLAG_NOFOLLOW, &out, size);
    const nofollow_link: *const pal_abi.AbiStat = @ptrCast(@alignCast(&out));
    try std.testing.expectEqual(pal_abi.STAT_KIND_SYMLINK, nofollow_link.kind);
    try std.testing.expectEqual(@as(u32, 0o777), nofollow_link.mode);
    try std.testing.expectEqual(@as(usize, 1), test_handle_info_calls);

    test_handle_info_api_available = false;
    test_attr_data.attributes = 0x400;
    pathStat(&state, &ov, "x", 1, 0, &out, size);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    test_handle_info_api_available = true;

    test_handle_info_success = false;
    test_native_error = 5;
    pathStat(&state, &ov, "x", 1, 0, &out, size);
    try std.testing.expectEqual(pal_abi.ERROR_ACCES, ov.@"error");
    test_handle_info_success = true;

    test_simple_attributes = 0;
    test_attr_data = .{};
    pathStat(&state, &ov, "x", 1, pal_abi.PATH_STAT_FLAG_NOFOLLOW, &out, size);
    try std.testing.expectEqual(pal_abi.STAT_KIND_FILE, (@as(*const pal_abi.AbiStat, @ptrCast(@alignCast(&out)))).kind);

    test_simple_attr_api_available = false;
    pathStat(&state, &ov, "x", 1, pal_abi.PATH_STAT_FLAG_NOFOLLOW, &out, size);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    test_simple_attr_api_available = true;

    test_attr_api_available = false;
    pathStat(&state, &ov, "x", 1, 0, &out, size);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    test_attr_api_available = true;

    pathStat(&state, &ov, "", 0, 0, &out, size);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    var long_path: [32768]u8 = undefined;
    @memset(&long_path, 'x');
    pathStat(&state, &ov, &long_path, long_path.len, 0, &out, size);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    test_attr_success = false;
    test_native_error = 2;
    pathStat(&state, &ov, "x", 1, 0, &out, size);
    try std.testing.expectEqual(pal_abi.ERROR_NOENT, ov.@"error");
    test_attr_success = true;

    @memset(&out, 0xcc);
    pathStat(&state, &ov, "x", 1, 0, &out, out.len);
    try std.testing.expectEqual(@as(u8, 0xcc), out[size]);
}

test "Windows filesystem dir_read validates handles and packs bounded entries" {
    var path = [_:0]u16{'d'};
    var ov: pal_abi.Overlapped = .{};
    var out: [24]u8 = @splat(0xcc);

    var state: PalState = .{};
    _ = initDirectoryState(&state, &path);
    dirRead(&state, &ov, 0xffff, &out, out.len);
    try std.testing.expectEqual(pal_abi.ERROR_BADF, ov.@"error");

    const ordinary = mem.allocHandle(&state).?;
    dirRead(&state, &ov, ordinary.token, &out, out.len);
    try std.testing.expectEqual(pal_abi.ERROR_NOTDIR, ov.@"error");

    const missing_path = mem.allocHandle(&state).?;
    missing_path.is_dir = true;
    dirRead(&state, &ov, missing_path.token, &out, out.len);
    try std.testing.expectEqual(pal_abi.ERROR_NOTDIR, ov.@"error");

    const done = mem.allocHandle(&state).?;
    done.is_dir = true;
    done.dir_path_w = &path;
    done.dir_done = true;
    dirRead(&state, &ov, done.token, &out, out.len);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(u64, 0), ov.result_ext);

    test_find_api_available = false;
    const no_api = initDirectoryState(&state, &path);
    dirRead(&state, &ov, no_api.token, &out, out.len);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    test_find_api_available = true;

    const empty = initDirectoryState(&state, &path);
    test_find_count = 0;
    dirRead(&state, &ov, empty.token, &out, out.len);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(u64, 0), ov.result_ext);
    try std.testing.expect(empty.dir_done);

    const listing = initDirectoryState(&state, &path);
    test_find_entries = @splat(.{});
    test_find_entries[0] = makeFindEntry(".", 0x10);
    test_find_entries[1] = makeFindEntry("..", 0x10);
    test_find_entries[2] = makeFindEntry("file", 0);
    test_find_entries[3] = makeFindEntry("subdir", 0x10);
    test_find_entries[4] = makeFindEntry("link", 0x400);
    test_find_count = 5;
    test_wide_to_mb_success = true;
    dirRead(&state, &ov, listing.token, &out, 20);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(u64, 20), ov.result_ext);
    try std.testing.expectEqual(@as(u8, pal_abi.STAT_KIND_FILE), out[0]);
    try std.testing.expectEqualSlices(u8, "file\x00", out[1..6]);
    try std.testing.expectEqual(@as(u8, pal_abi.STAT_KIND_DIR), out[6]);
    try std.testing.expectEqualSlices(u8, "subdir\x00", out[7..14]);
    try std.testing.expectEqual(@as(u8, pal_abi.STAT_KIND_SYMLINK), out[14]);
    try std.testing.expectEqualSlices(u8, "link\x00", out[15..20]);
    try std.testing.expect(listing.dir_done);

    const bounded = initDirectoryState(&state, &path);
    test_find_entries = @splat(.{});
    test_find_entries[0] = makeFindEntry("file", 0);
    test_find_entries[1] = makeFindEntry("subdir", 0x10);
    test_find_count = 2;
    @memset(&out, 0xcc);
    dirRead(&state, &ov, bounded.token, &out, 5);
    try std.testing.expectEqual(@as(u64, 0), ov.result_ext);
    try std.testing.expectEqualSlices(u8, &@as([24]u8, @splat(0xcc)), &out);

    dirRead(&state, &ov, bounded.token, &out, 6);
    try std.testing.expectEqual(@as(u64, 6), ov.result_ext);
    try std.testing.expectEqual(@as(u8, pal_abi.STAT_KIND_FILE), out[0]);
    try std.testing.expectEqualSlices(u8, "file\x00", out[1..6]);

    dirRead(&state, &ov, bounded.token, &out, 8);
    try std.testing.expectEqual(@as(u64, 8), ov.result_ext);
    try std.testing.expectEqual(@as(u8, pal_abi.STAT_KIND_DIR), out[0]);
    try std.testing.expectEqualSlices(u8, "subdir\x00", out[1..8]);
    try std.testing.expect(bounded.dir_done);

    const conversion_failure = initDirectoryState(&state, &path);
    test_find_entries = @splat(.{});
    test_find_entries[0] = makeFindEntry("not-ascii", 0);
    test_find_count = 1;
    test_wide_to_mb_success = false;
    dirRead(&state, &ov, conversion_failure.token, &out, out.len);
    try std.testing.expectEqual(@as(u64, 0), ov.result_ext);
    try std.testing.expect(conversion_failure.dir_done);
    test_wide_to_mb_success = true;
}

var test_path_api_available: [11]bool = @splat(true);
var test_path_api_success = true;
var test_path_api_calls: usize = 0;
var test_delete_success = true;
var test_remove_dir_success = true;
var test_set_attrs_success = true;
var test_delete_results: [4]bool = @splat(true);
var test_delete_result_count: usize = 0;
var test_delete_calls: usize = 0;
var test_path_api_flags: u32 = 0;
var test_path_api_attrs: u32 = 0;
var test_path_api_set_attrs: u32 = 0;
var test_path_api_old: [*:0]const u16 = undefined;
var test_path_api_new: [*:0]const u16 = undefined;
var test_path_api_old_copy: [64]u16 = @splat(0);
var test_path_api_create_flags: u32 = 0;
var test_path_time_access: ?win.FileTime = null;
var test_path_time_modify: ?win.FileTime = null;
var test_path_device_data: [64]u8 = @splat(0);
var test_path_device_len: u32 = 0;
var test_path_device_success = true;

const PathApi = enum(usize) {
    mkdir,
    move,
    delete,
    remove_dir,
    set_attrs,
    set_time,
    hard_link,
    symlink,
    device_io,
    set_file_info,
    resolve_source,
};

fn testPathOpGetProcAddress(_: ?*anyopaque, name: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
    const symbol = std.mem.sliceTo(name, 0);
    if (std.mem.eql(u8, symbol, "CreateDirectoryW") and test_path_api_available[@intFromEnum(PathApi.mkdir)]) {
        return @ptrCast(@constCast(&testCreateDirectory));
    }
    if (std.mem.eql(u8, symbol, "MoveFileExW") and test_path_api_available[@intFromEnum(PathApi.move)]) {
        return @ptrCast(@constCast(&testMoveFile));
    }
    if (std.mem.eql(u8, symbol, "DeleteFileW") and test_path_api_available[@intFromEnum(PathApi.delete)]) {
        return @ptrCast(@constCast(&testDeleteFile));
    }
    if (std.mem.eql(u8, symbol, "RemoveDirectoryW") and test_path_api_available[@intFromEnum(PathApi.remove_dir)]) {
        return @ptrCast(@constCast(&testRemoveDirectory));
    }
    if (std.mem.eql(u8, symbol, "SetFileAttributesW") and test_path_api_available[@intFromEnum(PathApi.set_attrs)]) {
        return @ptrCast(@constCast(&testSetFileAttributes));
    }
    if (std.mem.eql(u8, symbol, "GetFileInformationByHandle") and test_handle_info_api_available) {
        return @ptrCast(@constCast(&testGetFileInformationByHandle));
    }
    if (std.mem.eql(u8, symbol, "SetFileInformationByHandle") and test_path_api_available[@intFromEnum(PathApi.set_file_info)]) {
        return @ptrCast(@constCast(&testSetFileInformationByHandle));
    }
    if (std.mem.eql(u8, symbol, "GetFinalPathNameByHandleW") and test_path_api_available[@intFromEnum(PathApi.resolve_source)]) {
        return @ptrCast(@constCast(&testGetFinalPathNameByHandle));
    }
    if (std.mem.eql(u8, symbol, "SetFileTime") and test_path_api_available[@intFromEnum(PathApi.set_time)]) {
        return @ptrCast(@constCast(&testSetFileTime));
    }
    if (std.mem.eql(u8, symbol, "CreateHardLinkW") and test_path_api_available[@intFromEnum(PathApi.hard_link)]) {
        return @ptrCast(@constCast(&testCreateHardLink));
    }
    if (std.mem.eql(u8, symbol, "CreateSymbolicLinkW") and test_path_api_available[@intFromEnum(PathApi.symlink)]) {
        return @ptrCast(@constCast(&testCreateSymbolicLink));
    }
    if (std.mem.eql(u8, symbol, "DeviceIoControl") and test_path_api_available[@intFromEnum(PathApi.device_io)]) {
        return @ptrCast(@constCast(&testDeviceIoControl));
    }
    if (std.mem.eql(u8, symbol, "GetFileAttributesW") and test_simple_attr_api_available) {
        return @ptrCast(@constCast(&testGetFileAttributes));
    }
    return null;
}

fn initPathApiState(s: *PalState) void {
    initPathConversion(s);
    s.kernel32 = @ptrFromInt(1);
    s.host.get_proc_address = testPathOpGetProcAddress;
    s.p_get_last_error = testGetLastError;
}

fn resetPathApi() void {
    test_path_api_available = @splat(true);
    test_path_api_success = true;
    test_path_api_calls = 0;
    test_delete_success = true;
    test_remove_dir_success = true;
    test_set_attrs_success = true;
    test_delete_results = @splat(true);
    test_delete_result_count = 0;
    test_delete_calls = 0;
    test_path_api_flags = 0;
    test_path_api_attrs = 0;
    test_path_api_set_attrs = 0;
    test_path_time_access = null;
    test_path_time_modify = null;
    test_path_device_data = @splat(0);
    test_path_device_len = 0;
    test_path_device_success = true;
    test_simple_attr_api_available = true;
    test_handle_info = .{};
    test_handle_info_success = true;
    test_handle_info_api_available = true;
    test_handle_info_calls = 0;
    test_path_basic_info = .{};
    test_path_info_class = 0;
    test_path_info_size = 0;
    test_path_set_info_calls = 0;
    test_set_info_success = true;
    test_path_api_old_copy = @splat(0);
    test_path_create_access = 0;
    test_path_create_disposition = 0;
    test_path_close_calls = 0;
    test_native_error = 0;
}

fn testCreateDirectory(_: [*:0]const u16, _: ?*anyopaque) callconv(win.win_cc) i32 {
    test_path_api_calls += 1;
    return @intFromBool(test_path_api_success);
}

fn testMoveFile(old_path: [*:0]const u16, new_path: [*:0]const u16, flags: u32) callconv(win.win_cc) i32 {
    test_path_api_old = old_path;
    test_path_api_new = new_path;
    test_path_api_flags = flags;
    test_path_api_calls += 1;
    return @intFromBool(test_path_api_success);
}

fn testDeleteFile(_: [*:0]const u16) callconv(win.win_cc) i32 {
    test_path_api_calls += 1;
    const success = if (test_delete_calls < test_delete_result_count)
        test_delete_results[test_delete_calls]
    else
        test_delete_success;
    test_delete_calls += 1;
    return @intFromBool(success);
}

fn testRemoveDirectory(_: [*:0]const u16) callconv(win.win_cc) i32 {
    test_path_api_calls += 1;
    return @intFromBool(test_remove_dir_success);
}

fn testSetFileAttributes(_: [*:0]const u16, attributes: u32) callconv(win.win_cc) i32 {
    test_path_api_set_attrs = attributes;
    test_path_api_calls += 1;
    return @intFromBool(test_set_attrs_success);
}

fn testCreatePathHandle(
    _: [*:0]const u16,
    access: u32,
    _: u32,
    _: ?*anyopaque,
    disposition: u32,
    flags: u32,
    _: win.HANDLE,
) callconv(win.win_cc) win.HANDLE {
    test_path_create_access = access;
    test_path_create_disposition = disposition;
    test_path_api_create_flags = flags;
    test_path_api_calls += 1;
    return if (test_path_api_success) @ptrFromInt(0x40) else win.INVALID_HANDLE_VALUE;
}

var test_path_basic_info: Win32FileBasicInfo = .{};
var test_path_info_class: u32 = 0;
var test_path_info_size: u32 = 0;
var test_path_set_info_calls: usize = 0;
var test_set_info_success = true;

fn testSetFileInformationByHandle(
    _: win.HANDLE,
    info_class: u32,
    info: *const Win32FileBasicInfo,
    size: u32,
) callconv(win.win_cc) i32 {
    test_path_basic_info = info.*;
    test_path_info_class = info_class;
    test_path_info_size = size;
    test_path_set_info_calls += 1;
    return @intFromBool(test_set_info_success);
}

fn testSetFileTime(_: win.HANDLE, _: ?*const win.FileTime, access: ?*const win.FileTime, modify: ?*const win.FileTime) callconv(win.win_cc) i32 {
    test_path_time_access = if (access) |value| value.* else null;
    test_path_time_modify = if (modify) |value| value.* else null;
    test_path_api_calls += 1;
    return @intFromBool(test_path_api_success);
}

fn testCreateHardLink(new_path: [*:0]const u16, old_path: [*:0]const u16, _: ?*anyopaque) callconv(win.win_cc) i32 {
    test_path_api_new = new_path;
    test_path_api_old = old_path;
    for (0..test_path_api_old_copy.len) |i| {
        test_path_api_old_copy[i] = old_path[i];
        if (old_path[i] == 0) break;
    }
    test_path_api_calls += 1;
    return @intFromBool(test_path_api_success);
}

fn testGetFinalPathNameByHandle(_: win.HANDLE, out: [*]u16, cap: u32, _: u32) callconv(win.win_cc) u32 {
    const resolved = "resolved-target";
    if (cap <= resolved.len) return @intCast(resolved.len + 1);
    for (resolved, 0..) |char, i| out[i] = char;
    out[resolved.len] = 0;
    return @intCast(resolved.len);
}

fn testCreateSymbolicLink(link_path: [*:0]const u16, target: [*:0]const u16, flags: u32) callconv(win.win_cc) u8 {
    test_path_api_new = link_path;
    test_path_api_old = target;
    test_path_api_flags = flags;
    test_path_api_calls += 1;
    return @intFromBool(test_path_api_success);
}

fn testDeviceIoControl(
    _: win.HANDLE,
    _: u32,
    _: ?*anyopaque,
    _: u32,
    out: [*]u8,
    out_cap: u32,
    returned: *u32,
    _: ?*anyopaque,
) callconv(win.win_cc) i32 {
    test_path_api_calls += 1;
    if (!test_path_device_success or test_path_device_len > out_cap) return 0;
    @memcpy(out[0..test_path_device_len], test_path_device_data[0..test_path_device_len]);
    returned.* = test_path_device_len;
    return 1;
}

fn writeTestUtf16(offset: usize, value: []const u8) void {
    for (value, 0..) |byte, i| {
        test_path_device_data[offset + i * 2] = byte;
        test_path_device_data[offset + i * 2 + 1] = 0;
    }
}

fn setTestSymlinkReparse(print_name: []const u8, substitute_name: []const u8) void {
    test_path_device_data = @splat(0);
    std.mem.writeInt(u32, test_path_device_data[0..4], 0xA000_000C, .little);
    std.mem.writeInt(u16, test_path_device_data[8..10], 0, .little);
    std.mem.writeInt(u16, test_path_device_data[10..12], @intCast(substitute_name.len * 2), .little);
    std.mem.writeInt(u16, test_path_device_data[12..14], @intCast(substitute_name.len * 2), .little);
    std.mem.writeInt(u16, test_path_device_data[14..16], @intCast(print_name.len * 2), .little);
    writeTestUtf16(20, substitute_name);
    writeTestUtf16(20 + substitute_name.len * 2, print_name);
    test_path_device_len = @intCast(20 + (substitute_name.len + print_name.len) * 2);
}

fn setTestMountPointReparse(name: []const u8) void {
    test_path_device_data = @splat(0);
    std.mem.writeInt(u32, test_path_device_data[0..4], 0xA000_0003, .little);
    std.mem.writeInt(u16, test_path_device_data[12..14], 0, .little);
    std.mem.writeInt(u16, test_path_device_data[14..16], @intCast(name.len * 2), .little);
    writeTestUtf16(16, name);
    test_path_device_len = @intCast(16 + name.len * 2);
}

test "Windows filesystem path_mkdir validates names and maps native failures" {
    resetPathApi();
    var state: PalState = .{};
    initPathApiState(&state);
    var ov: pal_abi.Overlapped = .{};

    pathMkdir(&state, &ov, "", 0, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    var long_path: [32768]u8 = undefined;
    @memset(&long_path, 'd');
    pathMkdir(&state, &ov, &long_path, long_path.len, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    state.p_mb_to_wide = null;
    pathMkdir(&state, &ov, "d", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    state.p_mb_to_wide = testMbToWide;

    test_path_api_available[@intFromEnum(PathApi.mkdir)] = false;
    pathMkdir(&state, &ov, "d", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    test_path_api_available[@intFromEnum(PathApi.mkdir)] = true;

    test_path_api_success = true;
    test_path_api_calls = 0;
    pathMkdir(&state, &ov, "nested\\dir", 10, 0o700);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(usize, 1), test_path_api_calls);

    const errors = [_]struct { native: u32, expected: u32 }{
        .{ .native = 2, .expected = pal_abi.ERROR_NOENT },
        .{ .native = 5, .expected = pal_abi.ERROR_ACCES },
        .{ .native = 183, .expected = pal_abi.ERROR_EXIST },
        .{ .native = 123, .expected = pal_abi.ERROR_INVALID },
    };
    for (errors) |case| {
        test_path_api_success = false;
        test_native_error = case.native;
        pathMkdir(&state, &ov, "d", 1, 0);
        try std.testing.expectEqual(case.expected, ov.@"error");
    }

    state.p_get_last_error = null;
    pathMkdir(&state, &ov, "d", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_IO, ov.@"error");
}

test "Windows filesystem path_rename validates both paths and maps native failures" {
    resetPathApi();
    var state: PalState = .{};
    initPathApiState(&state);
    var ov: pal_abi.Overlapped = .{};

    pathRename(&state, &ov, "", 0, "b", 1);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    pathRename(&state, &ov, "a", 1, "", 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    var long_path: [32768]u8 = undefined;
    @memset(&long_path, 'r');
    pathRename(&state, &ov, &long_path, long_path.len, "b", 1);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    pathRename(&state, &ov, "a", 1, &long_path, long_path.len);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    state.p_mb_to_wide = null;
    pathRename(&state, &ov, "a", 1, "b", 1);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    state.p_mb_to_wide = testMbToWide;

    test_path_api_available[@intFromEnum(PathApi.move)] = false;
    pathRename(&state, &ov, "a", 1, "b", 1);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    test_path_api_available[@intFromEnum(PathApi.move)] = true;

    test_path_api_success = true;
    test_path_api_calls = 0;
    pathRename(&state, &ov, "old", 3, "new", 3);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(u32, 1), test_path_api_flags);
    try std.testing.expectEqual(@as(usize, 1), test_path_api_calls);

    for ([_]struct { native: u32, expected: u32 }{
        .{ .native = 2, .expected = pal_abi.ERROR_NOENT },
        .{ .native = 5, .expected = pal_abi.ERROR_ACCES },
        .{ .native = 183, .expected = pal_abi.ERROR_EXIST },
        .{ .native = 123, .expected = pal_abi.ERROR_INVALID },
    }) |case| {
        test_path_api_success = false;
        test_native_error = case.native;
        pathRename(&state, &ov, "a", 1, "b", 1);
        try std.testing.expectEqual(case.expected, ov.@"error");
    }

    state.p_get_last_error = null;
    pathRename(&state, &ov, "a", 1, "b", 1);
    try std.testing.expectEqual(pal_abi.ERROR_IO, ov.@"error");
}

test "Windows filesystem path_remove selects file or directory and retries readonly files" {
    resetPathApi();
    var state: PalState = .{};
    initPathApiState(&state);
    var ov: pal_abi.Overlapped = .{};

    pathRemove(&state, &ov, "", 0, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    var long_path: [32768]u8 = undefined;
    @memset(&long_path, 'f');
    pathRemove(&state, &ov, &long_path, long_path.len, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    test_path_api_available[@intFromEnum(PathApi.delete)] = false;
    pathRemove(&state, &ov, "f", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    test_path_api_available[@intFromEnum(PathApi.delete)] = true;

    test_path_api_available[@intFromEnum(PathApi.remove_dir)] = false;
    pathRemove(&state, &ov, "d", 1, pal_abi.PATH_REMOVE_FLAG_DIR);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    test_path_api_available[@intFromEnum(PathApi.remove_dir)] = true;

    test_delete_success = true;
    pathRemove(&state, &ov, "f", 1, 0);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    test_remove_dir_success = true;
    pathRemove(&state, &ov, "d", 1, pal_abi.PATH_REMOVE_FLAG_DIR);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");

    test_delete_success = false;
    test_native_error = 2;
    pathRemove(&state, &ov, "f", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_NOENT, ov.@"error");

    test_native_error = 5;
    test_simple_attributes = 0;
    pathRemove(&state, &ov, "f", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_ACCES, ov.@"error");

    state.p_get_last_error = null;
    pathRemove(&state, &ov, "f", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_IO, ov.@"error");
    state.p_get_last_error = testGetLastError;

    test_delete_success = false;
    test_delete_results = .{ false, true, true, true };
    test_delete_result_count = 2;
    test_delete_calls = 0;
    test_path_api_calls = 0;
    test_simple_attributes = 1;
    test_set_attrs_success = true;
    test_native_error = 5;
    pathRemove(&state, &ov, "readonly", 8, 0);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(usize, 3), test_path_api_calls);

    test_delete_results = .{ false, false, true, true };
    test_delete_result_count = 2;
    test_delete_calls = 0;
    test_path_api_calls = 0;
    pathRemove(&state, &ov, "readonly", 8, 0);
    try std.testing.expectEqual(pal_abi.ERROR_ACCES, ov.@"error");
    try std.testing.expectEqual(@as(u32, 1), test_path_api_set_attrs);
    try std.testing.expectEqual(@as(usize, 4), test_path_api_calls);
}

test "Windows filesystem path_chmod applies writable mode and maps failures" {
    resetPathApi();
    var state: PalState = .{};
    initPathApiState(&state);
    state.p_create_file_w = testCreatePathHandle;
    state.p_close_handle = testCloseHandle;
    var ov: pal_abi.Overlapped = .{};

    pathChmod(&state, &ov, "", 0, 0o200);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    var long_path: [32768]u8 = undefined;
    @memset(&long_path, 'c');
    pathChmod(&state, &ov, &long_path, long_path.len, 0o200);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    state.p_mb_to_wide = null;
    pathChmod(&state, &ov, "c", 1, 0o200);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    state.p_mb_to_wide = testMbToWide;

    state.p_create_file_w = null;
    pathChmod(&state, &ov, "c", 1, 0o200);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    state.p_create_file_w = testCreatePathHandle;

    test_handle_info_api_available = false;
    pathChmod(&state, &ov, "c", 1, 0o200);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    test_handle_info_api_available = true;

    test_path_api_available[@intFromEnum(PathApi.set_file_info)] = false;
    pathChmod(&state, &ov, "c", 1, 0o200);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    test_path_api_available[@intFromEnum(PathApi.set_file_info)] = true;

    test_handle_info.file_attributes = 0;
    test_path_api_success = true;
    pathChmod(&state, &ov, "c", 1, 0o200);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(usize, 0), test_path_set_info_calls);
    try std.testing.expectEqual(@as(u32, 0x0180), test_path_create_access);
    try std.testing.expectEqual(@as(u32, 3), test_path_create_disposition);
    try std.testing.expectEqual(@as(u32, 0x0200_0000), test_path_api_create_flags);

    test_handle_info.file_attributes = 0x1;
    test_path_set_info_calls = 0;
    pathChmod(&state, &ov, "readonly", 8, 0o200);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(u32, 0x80), test_path_basic_info.file_attributes);
    try std.testing.expectEqual(@as(u32, 0), test_path_info_class);
    try std.testing.expectEqual(@as(u32, @sizeOf(Win32FileBasicInfo)), test_path_info_size);
    try std.testing.expectEqual(@as(usize, 1), test_path_set_info_calls);

    test_handle_info.file_attributes = 0x80;
    pathChmod(&state, &ov, "c", 1, 0);
    try std.testing.expectEqual(@as(u32, 1), test_path_basic_info.file_attributes);

    test_handle_info_success = false;
    test_native_error = 2;
    pathChmod(&state, &ov, "missing", 7, 0o200);
    try std.testing.expectEqual(pal_abi.ERROR_NOENT, ov.@"error");

    state.p_get_last_error = null;
    pathChmod(&state, &ov, "missing", 7, 0o200);
    try std.testing.expectEqual(pal_abi.ERROR_IO, ov.@"error");
    state.p_get_last_error = testGetLastError;
    test_handle_info_success = true;

    test_handle_info.file_attributes = 0;
    test_set_info_success = false;
    test_native_error = 5;
    pathChmod(&state, &ov, "c", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_ACCES, ov.@"error");

    state.p_get_last_error = null;
    pathChmod(&state, &ov, "c", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_IO, ov.@"error");
    state.p_get_last_error = testGetLastError;

    test_path_api_success = false;
    test_native_error = 2;
    pathChmod(&state, &ov, "c", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_NOENT, ov.@"error");
}

test "Windows filesystem path_set_times handles action flags and native failures" {
    resetPathApi();
    var state: PalState = .{};
    initPathApiState(&state);
    state.p_create_file_w = testCreatePathHandle;
    state.p_close_handle = testCloseHandle;
    var ov: pal_abi.Overlapped = .{};

    pathSetTimes(&state, &ov, "", 0, 0, 0, 0, 0, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    var long_path: [32768]u8 = undefined;
    @memset(&long_path, 't');
    pathSetTimes(&state, &ov, &long_path, long_path.len, 0, 0, 0, 0, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    state.p_mb_to_wide = null;
    pathSetTimes(&state, &ov, "t", 1, 0, 0, 0, 0, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    state.p_mb_to_wide = testMbToWide;

    state.p_create_file_w = null;
    pathSetTimes(&state, &ov, "t", 1, 0, 0, 0, 0, 0);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    state.p_create_file_w = testCreatePathHandle;

    test_path_api_available[@intFromEnum(PathApi.set_time)] = false;
    pathSetTimes(&state, &ov, "t", 1, 0, 0, 0, 0, 0);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    test_path_api_available[@intFromEnum(PathApi.set_time)] = true;

    test_path_api_success = false;
    test_native_error = 2;
    pathSetTimes(&state, &ov, "t", 1, 0, 0, 0, 0, 0);
    try std.testing.expectEqual(pal_abi.ERROR_NOENT, ov.@"error");

    test_path_api_success = true;
    test_path_api_calls = 0;
    test_path_close_calls = 0;
    pathSetTimes(&state, &ov, "t", 1, 1, 100, 2, 200, PathTimesFlag.set_atime | PathTimesFlag.set_mtime);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expect(test_path_time_access != null and test_path_time_modify != null);
    try std.testing.expectEqual(@as(u32, 0x0200_0000), test_path_api_create_flags);
    try std.testing.expectEqual(@as(usize, 2), test_path_api_calls);
    try std.testing.expectEqual(@as(usize, 1), test_path_close_calls);

    pathSetTimes(&state, &ov, "t", 1, 0, 0, 3, 0, PathTimesFlag.set_mtime);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expect(test_path_time_access == null and test_path_time_modify != null);

    state.p_get_system_time_ft = testSystemTime;
    pathSetTimes(&state, &ov, "t", 1, 0, 0, 0, 0, PathTimesFlag.atime_now);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(u32, 0x1234_5678), test_path_time_access.?.low);
    try std.testing.expect(test_path_time_modify == null);

    pathSetTimes(&state, &ov, "t", 1, 0, 0, 0, 0, PathTimesFlag.mtime_now);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expect(test_path_time_access == null);
    try std.testing.expectEqual(@as(u32, 0x1234_5678), test_path_time_modify.?.low);

    pathSetTimes(&state, &ov, "t", 1, 0, 0, 0, 0, pal_abi.PATH_SET_TIMES_FLAG_NOFOLLOW);
    try std.testing.expectEqual(@as(u32, 0x0220_0000), test_path_api_create_flags);

    pathSetTimes(&state, &ov, "t", 1, 0, 0, 0, 0, PathTimesFlag.set_atime | PathTimesFlag.atime_now);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    pathSetTimes(&state, &ov, "t", 1, 0, 0, 0, 0, PathTimesFlag.set_mtime | PathTimesFlag.mtime_now);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    pathSetTimes(&state, &ov, "t", 1, 0, 0, 0, 0, 0x20);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    pathSetTimes(&state, &ov, "t", 1, 0, 1_000_000_000, 0, 0, PathTimesFlag.set_atime);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    pathSetTimes(&state, &ov, "t", 1, std.math.maxInt(i64), 0, 0, 0, PathTimesFlag.set_atime);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    state.p_get_system_time_ft = null;
    pathSetTimes(&state, &ov, "t", 1, 0, 0, 0, 0, PathTimesFlag.mtime_now);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    state.p_get_system_time_ft = testSystemTime;

    test_path_api_success = false;
    test_native_error = 5;
    pathSetTimes(&state, &ov, "t", 1, 0, 0, 0, 0, 0);
    try std.testing.expectEqual(pal_abi.ERROR_ACCES, ov.@"error");
}

test "Windows filesystem path_link validates both names and maps native failures" {
    resetPathApi();
    var state: PalState = .{};
    initPathApiState(&state);
    var ov: pal_abi.Overlapped = .{};

    pathLink(&state, &ov, "", 0, "b", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    pathLink(&state, &ov, "a", 1, "", 0, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    pathLink(&state, &ov, "a", 1, "b", 1, 2);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    var long_path: [32768]u8 = undefined;
    @memset(&long_path, 'l');
    pathLink(&state, &ov, &long_path, long_path.len, "b", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    pathLink(&state, &ov, "a", 1, &long_path, long_path.len, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    state.p_mb_to_wide = null;
    pathLink(&state, &ov, "a", 1, "b", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    state.p_mb_to_wide = testMbToWide;

    test_path_api_available[@intFromEnum(PathApi.hard_link)] = false;
    pathLink(&state, &ov, "a", 1, "b", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    test_path_api_available[@intFromEnum(PathApi.hard_link)] = true;

    test_path_api_success = true;
    test_path_api_calls = 0;
    pathLink(&state, &ov, "source", 6, "destination", 11, 0);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(usize, 1), test_path_api_calls);

    state.p_create_file_w = testCreatePathHandle;
    state.p_close_handle = testCloseHandle;
    test_path_api_calls = 0;
    pathLink(&state, &ov, "source-link", 11, "destination", 11, 1);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(u32, 0x0200_0000), test_path_api_create_flags);
    try std.testing.expectEqualSlices(u16, &[_]u16{
        'r', 'e', 's', 'o', 'l', 'v', 'e', 'd', '-', 't', 'a', 'r', 'g', 'e', 't', 0,
    }, test_path_api_old_copy[0..16]);
    try std.testing.expectEqual(@as(usize, 1), test_path_close_calls);

    test_path_api_available[@intFromEnum(PathApi.resolve_source)] = false;
    pathLink(&state, &ov, "source-link", 11, "destination", 11, 1);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    test_path_api_available[@intFromEnum(PathApi.resolve_source)] = true;

    for ([_]struct { native: u32, expected: u32 }{
        .{ .native = 2, .expected = pal_abi.ERROR_NOENT },
        .{ .native = 5, .expected = pal_abi.ERROR_ACCES },
        .{ .native = 183, .expected = pal_abi.ERROR_EXIST },
        .{ .native = 123, .expected = pal_abi.ERROR_INVALID },
    }) |case| {
        test_path_api_success = false;
        test_native_error = case.native;
        pathLink(&state, &ov, "a", 1, "b", 1, 0);
        try std.testing.expectEqual(case.expected, ov.@"error");
    }

    state.p_get_last_error = null;
    pathLink(&state, &ov, "a", 1, "b", 1, 0);
    try std.testing.expectEqual(pal_abi.ERROR_IO, ov.@"error");
}

test "Windows filesystem path_symlink chooses file or directory flags" {
    resetPathApi();
    var state: PalState = .{};
    initPathApiState(&state);
    var ov: pal_abi.Overlapped = .{};

    pathSymlink(&state, &ov, "", 0, "link", 4, pal_abi.STAT_KIND_FILE);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    pathSymlink(&state, &ov, "target", 6, "", 0, pal_abi.STAT_KIND_FILE);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    var long_path: [32768]u8 = undefined;
    @memset(&long_path, 's');
    pathSymlink(&state, &ov, &long_path, long_path.len, "link", 4, pal_abi.STAT_KIND_FILE);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    pathSymlink(&state, &ov, "target", 6, &long_path, long_path.len, pal_abi.STAT_KIND_FILE);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    state.p_mb_to_wide = null;
    pathSymlink(&state, &ov, "target", 6, "link", 4, pal_abi.STAT_KIND_FILE);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    state.p_mb_to_wide = testMbToWide;

    test_path_api_available[@intFromEnum(PathApi.symlink)] = false;
    pathSymlink(&state, &ov, "target", 6, "link", 4, pal_abi.STAT_KIND_FILE);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    test_path_api_available[@intFromEnum(PathApi.symlink)] = true;

    test_path_api_success = true;
    test_path_api_calls = 0;
    pathSymlink(&state, &ov, "target", 6, "file-link", 9, pal_abi.STAT_KIND_FILE);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(u32, 2), test_path_api_flags);

    pathSymlink(&state, &ov, "target", 6, "dir-link", 8, pal_abi.STAT_KIND_DIR);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(u32, 3), test_path_api_flags);

    pathSymlink(&state, &ov, "target", 6, "other-link", 10, pal_abi.STAT_KIND_UNKNOWN);
    try std.testing.expectEqual(@as(u32, 2), test_path_api_flags);

    test_path_api_success = false;
    test_native_error = 2;
    pathSymlink(&state, &ov, "target", 6, "link", 4, pal_abi.STAT_KIND_FILE);
    try std.testing.expectEqual(pal_abi.ERROR_NOENT, ov.@"error");

    test_native_error = 5;
    pathSymlink(&state, &ov, "target", 6, "link", 4, pal_abi.STAT_KIND_DIR);
    try std.testing.expectEqual(pal_abi.ERROR_ACCES, ov.@"error");

    state.p_get_last_error = null;
    pathSymlink(&state, &ov, "target", 6, "link", 4, pal_abi.STAT_KIND_FILE);
    try std.testing.expectEqual(pal_abi.ERROR_IO, ov.@"error");
}

test "Windows filesystem path_readlink validates reparse data and bounds output" {
    resetPathApi();
    var state: PalState = .{};
    initPathApiState(&state);
    state.p_create_file_w = testCreatePathHandle;
    state.p_close_handle = testCloseHandle;
    state.p_wide_to_mb = testWideToMultiByte;
    var ov: pal_abi.Overlapped = .{};
    var out: [12]u8 = @splat(0xcc);

    state.p_create_file_w = null;
    pathReadlink(&state, &ov, "x", 1, &out, out.len);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    state.p_create_file_w = testCreatePathHandle;

    test_path_api_available[@intFromEnum(PathApi.device_io)] = false;
    pathReadlink(&state, &ov, "x", 1, &out, out.len);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    test_path_api_available[@intFromEnum(PathApi.device_io)] = true;

    pathReadlink(&state, &ov, "", 0, &out, out.len);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    var long_path: [32768]u8 = undefined;
    @memset(&long_path, 'p');
    pathReadlink(&state, &ov, &long_path, long_path.len, &out, out.len);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    test_path_api_success = false;
    test_native_error = 2;
    pathReadlink(&state, &ov, "x", 1, &out, out.len);
    try std.testing.expectEqual(pal_abi.ERROR_NOENT, ov.@"error");
    test_path_api_success = true;

    test_path_device_success = false;
    pathReadlink(&state, &ov, "x", 1, &out, out.len);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    test_path_device_success = true;

    test_path_device_len = 15;
    pathReadlink(&state, &ov, "x", 1, &out, out.len);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    test_path_device_data = @splat(0);
    std.mem.writeInt(u32, test_path_device_data[0..4], 0xdead_beef, .little);
    test_path_device_len = 16;
    pathReadlink(&state, &ov, "x", 1, &out, out.len);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    setTestSymlinkReparse("target", "substitute");
    pathReadlink(&state, &ov, "x", 1, &out, out.len);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(u64, 6), ov.result_ext);
    try std.testing.expectEqualSlices(u8, "target", out[0..6]);
    try std.testing.expectEqual(@as(u32, 0x0220_0000), test_path_api_create_flags);

    setTestSymlinkReparse("", "sub");
    pathReadlink(&state, &ov, "x", 1, &out, out.len);
    try std.testing.expectEqual(@as(u64, 3), ov.result_ext);
    try std.testing.expectEqualSlices(u8, "sub", out[0..3]);

    setTestMountPointReparse("mount");
    pathReadlink(&state, &ov, "x", 1, &out, out.len);
    try std.testing.expectEqual(@as(u64, 5), ov.result_ext);
    try std.testing.expectEqualSlices(u8, "mount", out[0..5]);

    setTestSymlinkReparse("x", "y");
    std.mem.writeInt(u16, test_path_device_data[14..16], 1, .little);
    pathReadlink(&state, &ov, "x", 1, &out, out.len);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    setTestSymlinkReparse("x", "");
    std.mem.writeInt(u16, test_path_device_data[12..14], 0xfffe, .little);
    pathReadlink(&state, &ov, "x", 1, &out, out.len);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    setTestSymlinkReparse("target", "substitute");
    @memset(&out, 0xcc);
    pathReadlink(&state, &ov, "x", 1, &out, 2);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(u64, 2), ov.result_ext);
    try std.testing.expectEqualSlices(u8, "ta", out[0..2]);
    try std.testing.expectEqualSlices(u8, &@as([10]u8, @splat(0xcc)), out[2..]);
}
