use super::{abi, slice_origin, PREFIX_GLOBALS_OFFSET};
use crate::pal_abi::{
    Overlapped, ERROR_BADF, ERROR_EXIST, ERROR_INVALID, ERROR_IO, ERROR_NOMEM, FLAG_COMPLETED,
    READ_FLAG_POSITIONAL,
};

const O_CREATE: u32 = 0x40;
const O_EXCL: u32 = 0x80;
const O_TRUNC: u32 = 0x200;
const O_APPEND: u32 = 0x400;
const O_DIRECTORY: u32 = 0x2000;

const MAX_ARGUMENT_BYTES: usize = 16 * 1024;
const MAX_ARGUMENT_COUNT: usize = 2048;
const MAX_TRANSFER_BYTES: usize = 4096;
const MAX_OUTPUT_BYTES: usize = MAX_TRANSFER_BYTES * 2 + 512;
const MAX_CONCURRENT_READS: usize = 64;
const MAX_HANDLES: usize = 16;
const HANDLE_NAME_BYTES: usize = 32;

const PHASE_READY: u32 = 0;
const PHASE_OPERATION: u32 = 1;
const PHASE_OUTPUT: u32 = 2;
const PHASE_CONCURRENT_WAIT: u32 = 3;

const OP_OPEN: u32 = 1;
const OP_WRITE: u32 = 2;
const OP_READ: u32 = 3;
const OP_PREAD: u32 = 4;
const OP_SEEK: u32 = 5;
const OP_TRUNCATE: u32 = 6;
const OP_CLOSE: u32 = 7;

#[derive(Clone, Copy)]
struct Handle {
    used: bool,
    closed: bool,
    name: [u8; HANDLE_NAME_BYTES],
    name_len: usize,
    token: u64,
}

const EMPTY_HANDLE: Handle = Handle {
    used: false,
    closed: false,
    name: [0; HANDLE_NAME_BYTES],
    name_len: 0,
    token: 0,
};

#[repr(C)]
#[derive(Clone, Copy)]
struct ConcurrentRead {
    overlapped: Overlapped,
    offset: i64,
    byte: u8,
}

const EMPTY_CONCURRENT_READ: ConcurrentRead = ConcurrentRead {
    overlapped: Overlapped {
        flags: 0,
        error: 0,
        continued: 0,
        result_ext: 0,
    },
    offset: 0,
    byte: 0,
};

#[derive(Clone, Copy)]
struct Token {
    ptr: *const u8,
    len: usize,
}

#[repr(C)]
struct FileIoGlobals {
    initialized: bool,
    phase: u32,
    operation: u32,
    command_index: u32,
    current_index: u32,
    argument_count: usize,
    argument_index: usize,
    argument_length: usize,
    argument_cursor: usize,
    arguments: [u8; MAX_ARGUMENT_BYTES],
    handles: [Handle; MAX_HANDLES],
    current_handle: u64,
    current_handle_slot: usize,
    current_name: [u8; HANDLE_NAME_BYTES],
    current_name_len: usize,
    overlapped: Overlapped,
    positional_offset: i64,
    base_offset: i64,
    requested: usize,
    transferred: usize,
    data: [u8; MAX_TRANSFER_BYTES],
    concurrent_read_count: usize,
    concurrent_reads: [ConcurrentRead; MAX_CONCURRENT_READS],
    output: [u8; MAX_OUTPUT_BYTES],
    output_length: usize,
    output_offset: usize,
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

unsafe fn next_token(g: &mut FileIoGlobals) -> Option<Token> {
    if g.argument_index >= g.argument_count || g.argument_cursor >= g.argument_length {
        return None;
    }

    let start = g.argument_cursor;
    let mut end = start;
    while end < g.argument_length && g.arguments.as_ptr().add(end).read() != 0 {
        end += 1;
    }
    if end == g.argument_length {
        return None;
    }

    g.argument_cursor = end + 1;
    g.argument_index += 1;
    Some(Token {
        ptr: g.arguments.as_ptr().add(start),
        len: end - start,
    })
}

unsafe fn required_token(g: &mut FileIoGlobals) -> Token {
    match next_token(g) {
        Some(token) => token,
        None => fail(2),
    }
}

unsafe fn token_eq(token: Token, expected: &[u8]) -> bool {
    if token.len != expected.len() {
        return false;
    }
    let mut index = 0usize;
    while index < token.len {
        if token.ptr.add(index).read() != expected.as_ptr().add(index).read() {
            return false;
        }
        index += 1;
    }
    true
}

unsafe fn parse_unsigned(token: Token) -> Option<u64> {
    if token.len == 0 {
        return None;
    }
    let mut value = 0u64;
    let mut index = 0usize;
    while index < token.len {
        let byte = token.ptr.add(index).read();
        if !(b'0'..=b'9').contains(&byte) {
            return None;
        }
        value = value.checked_mul(10)?.checked_add((byte - b'0') as u64)?;
        index += 1;
    }
    Some(value)
}

unsafe fn parse_signed(token: Token) -> Option<i64> {
    if token.len == 0 {
        return None;
    }
    let negative = token.ptr.read() == b'-';
    let start = if negative { 1 } else { 0 };
    if start == token.len {
        return None;
    }
    let limit = if negative {
        i64::MAX as u64 + 1
    } else {
        i64::MAX as u64
    };
    let mut value = 0u64;
    let mut index = start;
    while index < token.len {
        let byte = token.ptr.add(index).read();
        if !(b'0'..=b'9').contains(&byte) {
            return None;
        }
        value = value.checked_mul(10)?.checked_add((byte - b'0') as u64)?;
        if value > limit {
            return None;
        }
        index += 1;
    }
    if negative {
        if value == i64::MAX as u64 + 1 {
            Some(i64::MIN)
        } else {
            Some(-(value as i64))
        }
    } else {
        Some(value as i64)
    }
}

unsafe fn parse_name(token: Token, output: &mut [u8; HANDLE_NAME_BYTES]) -> Option<usize> {
    if token.len == 0 || token.len > HANDLE_NAME_BYTES {
        return None;
    }
    let mut index = 0usize;
    while index < token.len {
        let byte = token.ptr.add(index).read();
        if !(byte.is_ascii_alphanumeric() || byte == b'_' || byte == b'-') {
            return None;
        }
        output.as_mut_ptr().add(index).write(byte);
        index += 1;
    }
    Some(token.len)
}

unsafe fn parse_access(token: Token) -> Option<u32> {
    if token_eq(token, b"ro") {
        Some(0)
    } else if token_eq(token, b"wo") {
        Some(1)
    } else if token_eq(token, b"rw") {
        Some(2)
    } else {
        None
    }
}

unsafe fn parse_flags(token: Token) -> Option<u32> {
    if token_eq(token, b"-") {
        return Some(0);
    }
    if token.len == 0 {
        return None;
    }

    let mut flags = 0u32;
    let mut start = 0usize;
    while start < token.len {
        let mut end = start;
        while end < token.len && token.ptr.add(end).read() != b',' {
            end += 1;
        }
        if end == start {
            return None;
        }
        let option = Token {
            ptr: token.ptr.add(start),
            len: end - start,
        };
        let flag = if token_eq(option, b"create") {
            O_CREATE
        } else if token_eq(option, b"exclusive") {
            O_EXCL
        } else if token_eq(option, b"truncate") {
            O_TRUNC
        } else if token_eq(option, b"append") {
            O_APPEND
        } else if token_eq(option, b"directory") {
            O_DIRECTORY
        } else {
            return None;
        };
        if flags & flag != 0 {
            return None;
        }
        flags |= flag;
        start = end + 1;
    }
    Some(flags)
}

unsafe fn parse_hex(token: Token, output: &mut [u8; MAX_TRANSFER_BYTES]) -> Option<usize> {
    if token.len % 2 != 0 || token.len / 2 > output.len() {
        return None;
    }
    let mut index = 0usize;
    while index < token.len / 2 {
        let high = hex_nibble(token.ptr.add(index * 2).read())?;
        let low = hex_nibble(token.ptr.add(index * 2 + 1).read())?;
        output.as_mut_ptr().add(index).write((high << 4) | low);
        index += 1;
    }
    Some(token.len / 2)
}

fn hex_nibble(byte: u8) -> Option<u8> {
    match byte {
        b'0'..=b'9' => Some(byte - b'0'),
        b'a'..=b'f' => Some(byte - b'a' + 10),
        b'A'..=b'F' => Some(byte - b'A' + 10),
        _ => None,
    }
}

unsafe fn find_handle(
    g: &FileIoGlobals,
    name: &[u8; HANDLE_NAME_BYTES],
    length: usize,
) -> Option<usize> {
    let mut index = 0usize;
    while index < g.handles.len() {
        let handle = &*g.handles.as_ptr().add(index);
        if handle.used && handle.name_len == length {
            let mut name_index = 0usize;
            while name_index < length
                && handle.name.as_ptr().add(name_index).read()
                    == name.as_ptr().add(name_index).read()
            {
                name_index += 1;
            }
            if name_index == length {
                return Some(index);
            }
        }
        index += 1;
    }
    None
}

unsafe fn append_output_ptr(g: &mut FileIoGlobals, bytes: *const u8, length: usize) {
    let Some(end) = g.output_length.checked_add(length) else {
        fail(90);
    };
    if end > g.output.len() {
        fail(90);
    }
    let mut index = 0usize;
    while index < length {
        g.output
            .as_mut_ptr()
            .add(g.output_length + index)
            .write(bytes.add(index).read_volatile());
        index += 1;
    }
    g.output_length = end;
}

unsafe fn append_output(g: &mut FileIoGlobals, bytes: &[u8]) {
    append_output_ptr(g, bytes.as_ptr(), bytes.len());
}

unsafe fn append_number(g: &mut FileIoGlobals, mut value: u64) {
    let mut digits = [0u8; 20];
    let mut count = 0usize;
    loop {
        digits
            .as_mut_ptr()
            .add(count)
            .write(b'0' + (value % 10) as u8);
        count += 1;
        value /= 10;
        if value == 0 {
            break;
        }
    }
    while count > 0 {
        count -= 1;
        append_output_ptr(g, digits.as_ptr().add(count), 1);
    }
}

unsafe fn append_hex(g: &mut FileIoGlobals, length: usize) {
    const HEX: &[u8; 16] = b"0123456789abcdef";
    let mut index = 0usize;
    while index < length {
        let byte = g.data.as_ptr().add(index).read();
        let encoded = [
            HEX.as_ptr().add((byte >> 4) as usize).read(),
            HEX.as_ptr().add((byte & 0x0f) as usize).read(),
        ];
        append_output(g, &encoded);
        index += 1;
    }
}

unsafe fn append_operation_name(g: &mut FileIoGlobals) {
    let name = match g.operation {
        OP_OPEN => b"open".as_slice(),
        OP_WRITE => b"write".as_slice(),
        OP_READ => b"read".as_slice(),
        OP_PREAD => b"pread".as_slice(),
        OP_SEEK => b"seek".as_slice(),
        OP_TRUNCATE => b"truncate".as_slice(),
        OP_CLOSE => b"close".as_slice(),
        _ => fail(90),
    };
    append_output(g, name);
}

unsafe fn append_name(g: &mut FileIoGlobals) {
    if g.current_name_len != 0 {
        let name = g.current_name.as_ptr();
        let length = g.current_name_len;
        append_output_ptr(g, name, length);
    }
}

unsafe fn schedule_output(g: &mut FileIoGlobals) {
    g.phase = PHASE_OUTPUT;
    g.output_offset = 0;
    schedule_output_chunk(g);
}

unsafe fn schedule_output_chunk(g: &mut FileIoGlobals) {
    g.overlapped = Overlapped::default();
    let remaining = g.output_length - g.output_offset;
    (abi().write)(
        &mut g.overlapped,
        1,
        g.output.as_ptr().add(g.output_offset),
        remaining,
    );
}

unsafe fn begin_record(g: &mut FileIoGlobals, success: bool) {
    g.output_length = 0;
    append_number(g, g.current_index as u64);
    append_output(g, if success { b" OK " } else { b" ERR " });
    append_operation_name(g);
    if g.current_name_len != 0 {
        append_output(g, b" ");
        append_name(g);
    }
}

unsafe fn queue_error(g: &mut FileIoGlobals, error: u32) {
    begin_record(g, false);
    append_output(g, b" error=");
    append_number(g, error as u64);
    if g.operation == OP_READ || g.operation == OP_PREAD || g.operation == OP_WRITE {
        append_output(g, b" bytes=");
        append_number(g, g.transferred as u64);
    }
    if g.operation == OP_READ || g.operation == OP_PREAD {
        append_output(g, b" hex=");
        append_hex(g, g.transferred);
    }
    append_output(g, b"\n");
    schedule_output(g);
}

unsafe fn queue_success(g: &mut FileIoGlobals, eof: bool) {
    begin_record(g, true);
    match g.operation {
        OP_OPEN | OP_CLOSE | OP_TRUNCATE => {}
        OP_WRITE => {
            append_output(g, b" bytes=");
            append_number(g, g.transferred as u64);
        }
        OP_READ | OP_PREAD => {
            append_output(g, b" bytes=");
            append_number(g, g.transferred as u64);
            append_output(g, if eof { b" eof=1 hex=" } else { b" eof=0 hex=" });
            append_hex(g, g.transferred);
        }
        OP_SEEK => {
            append_output(g, b" position=");
            append_number(g, g.overlapped.result_ext);
        }
        _ => fail(90),
    }
    append_output(g, b"\n");
    schedule_output(g);
}

unsafe fn schedule_transfer(g: &mut FileIoGlobals) {
    let remaining = g.requested - g.transferred;
    g.overlapped = Overlapped::default();
    match g.operation {
        OP_WRITE => (abi().write)(
            &mut g.overlapped,
            g.current_handle,
            g.data.as_ptr().add(g.transferred),
            remaining,
        ),
        OP_READ => (abi().read)(
            &mut g.overlapped,
            g.current_handle,
            g.data.as_mut_ptr().add(g.transferred),
            remaining,
        ),
        OP_PREAD => {
            let Some(offset) = g.base_offset.checked_add(g.transferred as i64) else {
                queue_error(g, ERROR_INVALID);
                return;
            };
            g.positional_offset = offset;
            g.overlapped.flags = READ_FLAG_POSITIONAL;
            (abi().read)(
                &mut g.overlapped,
                g.current_handle,
                g.data.as_mut_ptr().add(g.transferred),
                remaining,
            );
        }
        _ => fail(90),
    }
    g.phase = PHASE_OPERATION;
}

unsafe fn queue_concurrent_read_results(g: &mut FileIoGlobals) {
    g.output_length = 0;
    let mut index = 0usize;
    while index < g.concurrent_read_count {
        let request = &*g.concurrent_reads.as_ptr().add(index);
        append_number(g, g.current_index as u64);
        append_output(g, b".");
        append_number(g, index as u64);
        if request.overlapped.error == 0 {
            let count = request.overlapped.result_ext as usize;
            if count > 1 {
                fail(90);
            }
            append_output(g, b" OK pread ");
            append_name(g);
            append_output(g, b" bytes=");
            append_number(g, count as u64);
            append_output(
                g,
                if count == 0 {
                    b" eof=1 hex="
                } else {
                    b" eof=0 hex="
                },
            );
            if count == 1 {
                g.data.as_mut_ptr().write(request.byte);
                append_hex(g, 1);
            }
        } else {
            append_output(g, b" ERR pread ");
            append_name(g);
            append_output(g, b" error=");
            append_number(g, request.overlapped.error as u64);
            append_output(g, b" bytes=0 hex=");
        }
        append_output(g, b"\n");
        index += 1;
    }
    schedule_output(g);
}

unsafe fn start_command(g: &mut FileIoGlobals) {
    let command = required_token(g);
    g.current_index = g.command_index;
    g.command_index += 1;
    g.current_name_len = 0;
    g.current_handle = 0;
    g.transferred = 0;

    if token_eq(command, b"pread-many") {
        g.operation = OP_PREAD;
        let name = required_token(g);
        let Some(name_len) = parse_name(name, &mut g.current_name) else {
            fail(2);
        };
        g.current_name_len = name_len;
        let Some(slot) = find_handle(g, &g.current_name, g.current_name_len) else {
            queue_error(g, ERROR_BADF);
            return;
        };
        let count = match parse_unsigned(required_token(g)) {
            Some(count) if (2..=MAX_CONCURRENT_READS as u64).contains(&count) => count as usize,
            _ => fail(2),
        };
        g.current_handle_slot = slot;
        g.current_handle = (*g.handles.as_ptr().add(slot)).token;
        g.concurrent_read_count = count;
        let mut index = 0usize;
        while index < count {
            let request = g.concurrent_reads.as_mut_ptr().add(index);
            request.write(EMPTY_CONCURRENT_READ);
            (*request).offset = index as i64;
            (*request).overlapped.flags = READ_FLAG_POSITIONAL;
            (abi().read)(
                &mut (*request).overlapped,
                g.current_handle,
                &mut (*request).byte,
                1,
            );
            index += 1;
        }
        g.phase = PHASE_CONCURRENT_WAIT;
        return;
    }

    if token_eq(command, b"open") {
        g.operation = OP_OPEN;
        let name = required_token(g);
        let Some(name_len) = parse_name(name, &mut g.current_name) else {
            fail(2);
        };
        g.current_name_len = name_len;
        let path = required_token(g);
        let access = match parse_access(required_token(g)) {
            Some(access) => access,
            None => fail(2),
        };
        let flags = match parse_flags(required_token(g)) {
            Some(flags) => flags,
            None => fail(2),
        };
        if path.len == 0 {
            fail(2);
        }
        if find_handle(g, &g.current_name, g.current_name_len).is_some() {
            queue_error(g, ERROR_EXIST);
            return;
        }
        let mut slot = 0usize;
        while slot < g.handles.len() && (*g.handles.as_ptr().add(slot)).used {
            slot += 1;
        }
        if slot == g.handles.len() {
            queue_error(g, ERROR_NOMEM);
            return;
        }
        g.current_handle_slot = slot;
        g.overlapped = Overlapped::default();
        (abi().path_open)(&mut g.overlapped, path.ptr, path.len, access | flags);
        g.phase = PHASE_OPERATION;
        return;
    }

    if token_eq(command, b"write")
        || token_eq(command, b"read")
        || token_eq(command, b"pread")
        || token_eq(command, b"seek")
        || token_eq(command, b"truncate")
        || token_eq(command, b"close")
    {
        g.operation = if token_eq(command, b"write") {
            OP_WRITE
        } else if token_eq(command, b"read") {
            OP_READ
        } else if token_eq(command, b"pread") {
            OP_PREAD
        } else if token_eq(command, b"seek") {
            OP_SEEK
        } else if token_eq(command, b"truncate") {
            OP_TRUNCATE
        } else {
            OP_CLOSE
        };

        let name = required_token(g);
        let Some(name_len) = parse_name(name, &mut g.current_name) else {
            fail(2);
        };
        g.current_name_len = name_len;
        let Some(slot) = find_handle(g, &g.current_name, g.current_name_len) else {
            queue_error(g, ERROR_BADF);
            return;
        };
        g.current_handle_slot = slot;
        g.current_handle = (*g.handles.as_ptr().add(slot)).token;
        if g.operation == OP_CLOSE {
            if (*g.handles.as_ptr().add(slot)).closed {
                queue_error(g, ERROR_BADF);
                return;
            }
            (abi().handle_close)(g.current_handle);
            (*g.handles.as_mut_ptr().add(slot)).closed = true;
            queue_success(g, false);
            return;
        }

        if g.operation == OP_WRITE {
            let bytes = required_token(g);
            let Some(length) = parse_hex(bytes, &mut g.data) else {
                fail(2);
            };
            g.requested = length;
            g.transferred = 0;
            schedule_transfer(g);
            return;
        }
        if g.operation == OP_READ {
            let length = match parse_unsigned(required_token(g)) {
                Some(length) if length <= MAX_TRANSFER_BYTES as u64 => length as usize,
                _ => fail(2),
            };
            g.requested = length;
            g.transferred = 0;
            schedule_transfer(g);
            return;
        }
        if g.operation == OP_PREAD {
            let Some(offset) = parse_signed(required_token(g)) else {
                fail(2);
            };
            let length = match parse_unsigned(required_token(g)) {
                Some(length) if length <= MAX_TRANSFER_BYTES as u64 => length as usize,
                _ => fail(2),
            };
            g.base_offset = offset;
            g.requested = length;
            g.transferred = 0;
            schedule_transfer(g);
            return;
        }
        if g.operation == OP_SEEK {
            let whence_token = required_token(g);
            let whence = if token_eq(whence_token, b"start") {
                0
            } else if token_eq(whence_token, b"current") {
                1
            } else if token_eq(whence_token, b"end") {
                2
            } else {
                fail(2)
            };
            let Some(offset) = parse_signed(required_token(g)) else {
                fail(2);
            };
            g.overlapped = Overlapped::default();
            (abi().seek)(&mut g.overlapped, g.current_handle, offset, whence);
            g.phase = PHASE_OPERATION;
            return;
        }

        let Some(length) = parse_signed(required_token(g)) else {
            fail(2);
        };
        g.overlapped = Overlapped::default();
        (abi().handle_truncate)(&mut g.overlapped, g.current_handle, length);
        g.phase = PHASE_OPERATION;
        return;
    }

    fail(2);
}

unsafe fn complete_operation(g: &mut FileIoGlobals) {
    let error = g.overlapped.error;
    if error != 0 {
        queue_error(g, error);
        return;
    }

    match g.operation {
        OP_OPEN => {
            let token = g.overlapped.result_ext;
            if token < 3 {
                queue_error(g, ERROR_INVALID);
                return;
            }
            let slot = g.current_handle_slot;
            g.handles.as_mut_ptr().add(slot).write(Handle {
                used: true,
                closed: false,
                name: g.current_name,
                name_len: g.current_name_len,
                token,
            });
            queue_success(g, false);
        }
        OP_WRITE => {
            let count = g.overlapped.result_ext as usize;
            let remaining = g.requested - g.transferred;
            if count > remaining {
                fail(90);
            }
            if count == 0 && remaining != 0 {
                queue_error(g, ERROR_IO);
                return;
            }
            g.transferred += count;
            if g.transferred < g.requested {
                schedule_transfer(g);
            } else {
                queue_success(g, false);
            }
        }
        OP_READ | OP_PREAD => {
            let count = g.overlapped.result_ext as usize;
            let remaining = g.requested - g.transferred;
            if count > remaining {
                fail(90);
            }
            if count == 0 {
                queue_success(g, g.transferred < g.requested);
                return;
            }
            g.transferred += count;
            if g.transferred < g.requested {
                schedule_transfer(g);
            } else {
                queue_success(g, false);
            }
        }
        OP_SEEK | OP_TRUNCATE => queue_success(g, false),
        _ => fail(90),
    }
}

#[inline(never)]
pub(super) unsafe fn resume() {
    let g = globals();
    if !g.initialized {
        core::ptr::write_bytes(g as *mut FileIoGlobals, 0, 1);
        g.initialized = true;
        let packed = (abi().get_args)(g.arguments.as_mut_ptr(), g.arguments.len());
        if packed == u64::MAX {
            fail(2);
        }
        g.argument_count = (packed >> 32) as usize;
        g.argument_length = packed as u32 as usize;
        if g.argument_count == 0
            || g.argument_count > MAX_ARGUMENT_COUNT
            || g.argument_length > MAX_ARGUMENT_BYTES
            || (g.argument_length != 0
                && g.arguments.as_ptr().add(g.argument_length - 1).read() != 0)
        {
            fail(2);
        }
    }

    loop {
        match g.phase {
            PHASE_READY => {
                if g.argument_index == g.argument_count {
                    if g.argument_cursor != g.argument_length {
                        fail(2);
                    }
                    (abi().process_exit)(0);
                }
                start_command(g);
            }
            PHASE_OPERATION => {
                if g.overlapped.flags & FLAG_COMPLETED == 0 {
                    return;
                }
                complete_operation(g);
            }
            PHASE_CONCURRENT_WAIT => {
                let mut index = 0usize;
                while index < g.concurrent_read_count {
                    if (*g.concurrent_reads.as_ptr().add(index)).overlapped.flags & FLAG_COMPLETED
                        == 0
                    {
                        return;
                    }
                    index += 1;
                }
                queue_concurrent_read_results(g);
            }
            PHASE_OUTPUT => {
                if g.overlapped.flags & FLAG_COMPLETED == 0 {
                    return;
                }
                if g.overlapped.error != 0 {
                    fail(90);
                }
                let count = g.overlapped.result_ext as usize;
                let remaining = g.output_length - g.output_offset;
                if count == 0 || count > remaining {
                    fail(90);
                }
                g.output_offset += count;
                if g.output_offset == g.output_length {
                    g.phase = PHASE_READY;
                } else {
                    schedule_output_chunk(g);
                }
            }
            _ => fail(90),
        }
    }
}
