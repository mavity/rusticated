use super::{abi, slice_origin, PREFIX_GLOBALS_OFFSET};
use crate::pal_abi::{Overlapped, ERROR_EXIST, FLAG_COMPLETED, READ_FLAG_POSITIONAL};

const O_CREATE: u32 = 0x40;
const O_EXCL: u32 = 0x80;
const O_APPEND: u32 = 0x400;
const O_DIRECTORY: u32 = 0x2000;

const CREATE_FILE: u32 = 1;
const WRITE_INITIAL: u32 = 2;
const SEEK_START: u32 = 3;
const READ_FIRST: u32 = 4;
const READ_POSITIONAL: u32 = 5;
const TELL_CURSOR: u32 = 6;
const READ_REMAINDER: u32 = 7;
const READ_EOF: u32 = 8;
const TRUNCATE_FILE: u32 = 9;
const SEEK_END: u32 = 10;
const APPEND_OPEN: u32 = 11;
const APPEND_WRITE: u32 = 12;
const APPEND_TELL: u32 = 13;
const EXCLUSIVE_OPEN: u32 = 14;
const DIRECTORY_OPEN: u32 = 15;

const FILE_PATH: &[u8] = "fileio-λ.bin".as_bytes();
const DIRECTORY_PATH: &[u8] = b".";
const INITIAL_CONTENTS: &[u8] = b"abcdef";
const APPENDED_CONTENTS: &[u8] = b"xy";

#[repr(C)]
struct FileIoGlobals {
    initialized: bool,
    step: u32,
    file: u64,
    append_file: u64,
    directory: u64,
    overlapped: Overlapped,
    positional_offset: i64,
    buffer: [u8; 8],
}

#[inline(always)]
unsafe fn globals() -> &'static mut FileIoGlobals {
    &mut *(slice_origin()
        .sub(PREFIX_GLOBALS_OFFSET)
        .cast::<FileIoGlobals>())
}

unsafe fn fail(code: i32) -> ! {
    (abi().process_exit)(code)
}

unsafe fn schedule_open(g: &mut FileIoGlobals, path: &[u8], flags: u32, step: u32) {
    g.overlapped = Overlapped::default();
    g.step = step;
    (abi().path_open)(&mut g.overlapped, path.as_ptr(), path.len(), flags);
}

unsafe fn schedule_seek(g: &mut FileIoGlobals, file: u64, offset: i64, whence: u32, step: u32) {
    g.overlapped = Overlapped::default();
    g.step = step;
    (abi().seek)(&mut g.overlapped, file, offset, whence);
}

unsafe fn schedule_read(g: &mut FileIoGlobals, file: u64, length: usize, step: u32) {
    g.overlapped = Overlapped::default();
    g.step = step;
    (abi().read)(&mut g.overlapped, file, g.buffer.as_mut_ptr(), length);
}

unsafe fn schedule_write(g: &mut FileIoGlobals, file: u64, bytes: &[u8], step: u32) {
    g.overlapped = Overlapped::default();
    g.step = step;
    (abi().write)(&mut g.overlapped, file, bytes.as_ptr(), bytes.len());
}

unsafe fn schedule_positional_read(g: &mut FileIoGlobals, file: u64) {
    g.overlapped = Overlapped {
        flags: READ_FLAG_POSITIONAL,
        ..Overlapped::default()
    };
    g.positional_offset = 4;
    g.step = READ_POSITIONAL;
    (abi().read)(&mut g.overlapped, file, g.buffer.as_mut_ptr(), 2);
}

unsafe fn expect_bytes(buffer: *const u8, expected: &[u8], count: usize) -> bool {
    if expected.len() != count {
        return false;
    }
    let mut index = 0usize;
    while index < count {
        if buffer.add(index).read() != expected.as_ptr().add(index).read() {
            return false;
        }
        index += 1;
    }
    true
}

#[inline(never)]
pub(super) unsafe fn resume() {
    let g = globals();
    if !g.initialized {
        core::ptr::write_bytes(g as *mut FileIoGlobals, 0, 1);
        g.initialized = true;
        schedule_open(g, FILE_PATH, 2 | O_CREATE | O_EXCL, CREATE_FILE);
    }

    loop {
        if g.overlapped.flags & FLAG_COMPLETED == 0 {
            return;
        }
        if g.overlapped.error != 0
            && !(g.step == EXCLUSIVE_OPEN && g.overlapped.error == ERROR_EXIST)
        {
            fail(100 + g.step as i32);
        }

        match g.step {
            CREATE_FILE => {
                g.file = g.overlapped.result_ext;
                if g.file < 3 {
                    fail(120);
                }
                schedule_write(g, g.file, INITIAL_CONTENTS, WRITE_INITIAL);
            }
            WRITE_INITIAL => {
                if g.overlapped.result_ext != INITIAL_CONTENTS.len() as u64 {
                    fail(121);
                }
                schedule_seek(g, g.file, 0, 0, SEEK_START);
            }
            SEEK_START => {
                if g.overlapped.result_ext != 0 {
                    fail(122);
                }
                schedule_read(g, g.file, 2, READ_FIRST);
            }
            READ_FIRST => {
                if g.overlapped.result_ext != 2 || !expect_bytes(g.buffer.as_ptr(), b"ab", 2) {
                    fail(123);
                }
                schedule_positional_read(g, g.file);
            }
            READ_POSITIONAL => {
                if g.overlapped.result_ext != 2 || !expect_bytes(g.buffer.as_ptr(), b"ef", 2) {
                    fail(124);
                }
                schedule_seek(g, g.file, 0, 1, TELL_CURSOR);
            }
            TELL_CURSOR => {
                if g.overlapped.result_ext != 2 {
                    fail(125);
                }
                schedule_read(g, g.file, 8, READ_REMAINDER);
            }
            READ_REMAINDER => {
                if g.overlapped.result_ext != 4 || !expect_bytes(g.buffer.as_ptr(), b"cdef", 4) {
                    fail(126);
                }
                schedule_read(g, g.file, 1, READ_EOF);
            }
            READ_EOF => {
                if g.overlapped.result_ext != 0 {
                    fail(127);
                }
                g.overlapped = Overlapped::default();
                g.step = TRUNCATE_FILE;
                (abi().handle_truncate)(&mut g.overlapped, g.file, 4);
            }
            TRUNCATE_FILE => {
                g.overlapped = Overlapped::default();
                g.step = SEEK_END;
                (abi().seek)(&mut g.overlapped, g.file, 0, 2);
            }
            SEEK_END => {
                if g.overlapped.result_ext != 4 {
                    fail(128);
                }
                (abi().handle_close)(g.file);
                g.file = 0;
                schedule_open(g, FILE_PATH, 1 | O_APPEND, APPEND_OPEN);
            }
            APPEND_OPEN => {
                g.append_file = g.overlapped.result_ext;
                if g.append_file < 3 {
                    fail(129);
                }
                schedule_write(g, g.append_file, APPENDED_CONTENTS, APPEND_WRITE);
            }
            APPEND_WRITE => {
                if g.overlapped.result_ext != APPENDED_CONTENTS.len() as u64 {
                    fail(130);
                }
                schedule_seek(g, g.append_file, 0, 1, APPEND_TELL);
            }
            APPEND_TELL => {
                if g.overlapped.result_ext != 6 {
                    fail(131);
                }
                (abi().handle_close)(g.append_file);
                g.append_file = 0;
                schedule_open(g, FILE_PATH, 2 | O_CREATE | O_EXCL, EXCLUSIVE_OPEN);
            }
            EXCLUSIVE_OPEN => {
                if g.overlapped.error != ERROR_EXIST {
                    fail(132);
                }
                schedule_open(g, DIRECTORY_PATH, O_DIRECTORY, DIRECTORY_OPEN);
            }
            DIRECTORY_OPEN => {
                g.directory = g.overlapped.result_ext;
                if g.directory < 3 {
                    fail(133);
                }
                (abi().handle_close)(g.directory);
                (abi().process_exit)(0);
            }
            _ => fail(134),
        }
    }
}
