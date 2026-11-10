#![cfg_attr(not(test), no_std)]

#[cfg(not(test))]
use core::panic::PanicInfo;

#[path = "../pal_abi.rs"]
mod pal_abi;
#[cfg(test)]
#[path = "../pal_abi_stubs.rs"]
mod pal_abi_stubs;
use pal_abi::{Overlapped, PalAbiTable, FLAG_COMPLETED};

#[cfg(not(any(target_arch = "x86_64", target_arch = "aarch64")))]
compile_error!("The probe payload supports only x86-64 and AArch64.");

#[cfg(not(test))]
const PREFIX_ABI_OFFSET: usize = 0x20000;
#[cfg(not(test))]
const PREFIX_GLOBALS_OFFSET: usize = 0x10000;
const LINE_CAPACITY: usize = 256;
const OUTPUT_CAPACITY: usize = 512;
const PHASE_WRITING: u8 = 1;
const PHASE_READING: u8 = 2;

#[repr(C)]
pub struct ProbeGlobals {
    initialized: bool,
    phase: u8,
    skip_lf: bool,
    line_len: usize,
    output_len: usize,
    output_offset: usize,
    input_byte: u8,
    overlapped: Overlapped,
    line: [u8; LINE_CAPACITY],
    output: [u8; OUTPUT_CAPACITY],
}

#[cfg(not(test))]
#[inline(always)]
unsafe fn slice_origin() -> *mut u8 {
    let addr: usize;
    #[cfg(target_arch = "x86_64")]
    core::arch::asm!(
        "lea {0}, [rip + probe_code_start]",
        out(reg) addr,
        options(pure, nomem, nostack),
    );
    #[cfg(target_arch = "aarch64")]
    core::arch::asm!(
        "adr {0}, probe_code_start",
        out(reg) addr,
        options(pure, nomem, nostack),
    );
    addr as *mut u8
}

#[inline(always)]
unsafe fn abi() -> &'static PalAbiTable {
    #[cfg(not(test))]
    {
        &*(slice_origin().sub(PREFIX_ABI_OFFSET) as *const PalAbiTable)
    }
    #[cfg(test)]
    {
        &*core::ptr::addr_of!(TEST_ABI).cast::<PalAbiTable>()
    }
}

#[inline(always)]
unsafe fn globals() -> &'static mut ProbeGlobals {
    #[cfg(not(test))]
    {
        &mut *(slice_origin().sub(PREFIX_GLOBALS_OFFSET) as *mut ProbeGlobals)
    }
    #[cfg(test)]
    {
        &mut *core::ptr::addr_of_mut!(TEST_GLOBALS)
    }
}

const GREETING_PREFIX: &[u8] = b"Anonymous-memory probe ready (compiled ";
const BUILD_TIME: &str = match option_env!("BUILD_TIME") {
    Some(value) => value,
    None => "unknown",
};
const GREETING_SUFFIX: &[u8] = b"). Enter an empty line to exit.\n> ";
const ECHO_PREFIX: &[u8] = b"echo: ";
const NEWLINE: &[u8] = b"\n";
const PROMPT: &[u8] = b"> ";

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memcpy(dest: *mut u8, src: *const u8, len: usize) -> *mut u8 {
    let mut index = 0;
    while index < len {
        dest.add(index)
            .write_volatile(src.add(index).read_volatile());
        index += 1;
    }
    dest
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memset(dest: *mut u8, value: i32, len: usize) -> *mut u8 {
    let mut index = 0;
    while index < len {
        dest.add(index).write_volatile(value as u8);
        index += 1;
    }
    dest
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memmove(dest: *mut u8, src: *const u8, len: usize) -> *mut u8 {
    if (dest as usize) <= src as usize || (dest as usize) >= (src as usize).wrapping_add(len) {
        memcpy(dest, src, len)
    } else {
        let mut index = len;
        while index > 0 {
            index -= 1;
            dest.add(index)
                .write_volatile(src.add(index).read_volatile());
        }
        dest
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memcmp(left: *const u8, right: *const u8, len: usize) -> i32 {
    let mut index = 0;
    while index < len {
        let left_byte = left.add(index).read_volatile();
        let right_byte = right.add(index).read_volatile();
        if left_byte != right_byte {
            return left_byte as i32 - right_byte as i32;
        }
        index += 1;
    }
    0
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn bcmp(left: *const u8, right: *const u8, len: usize) -> i32 {
    memcmp(left, right, len)
}

unsafe fn append_output(g: &mut ProbeGlobals, bytes: &[u8]) -> bool {
    let Some(end) = g.output_len.checked_add(bytes.len()) else {
        return false;
    };
    if end > OUTPUT_CAPACITY {
        return false;
    }
    core::ptr::copy_nonoverlapping(
        bytes.as_ptr(),
        g.output.as_mut_ptr().add(g.output_len),
        bytes.len(),
    );
    g.output_len = end;
    true
}

unsafe fn schedule_write(g: &mut ProbeGlobals) {
    g.overlapped = Overlapped::default();
    (abi().write)(
        &mut g.overlapped,
        1,
        g.output.as_ptr().add(g.output_offset),
        g.output_len - g.output_offset,
    );
}

unsafe fn schedule_read(g: &mut ProbeGlobals) {
    g.overlapped = Overlapped::default();
    (abi().read)(&mut g.overlapped, 0, &mut g.input_byte, 1);
}

unsafe fn prepare_greeting(g: &mut ProbeGlobals) -> bool {
    g.output_len = 0;
    g.output_offset = 0;
    append_output(g, GREETING_PREFIX)
        && append_output(g, BUILD_TIME.as_bytes())
        && append_output(g, GREETING_SUFFIX)
}

unsafe fn prepare_echo(g: &mut ProbeGlobals) -> bool {
    g.output_len = 0;
    g.output_offset = 0;
    if !append_output(g, ECHO_PREFIX) {
        return false;
    }
    let line_len = g.line_len;
    let mut index = 0;
    while index < line_len {
        g.output
            .as_mut_ptr()
            .add(g.output_len + index)
            .write(g.line.as_ptr().add(index).read());
        index += 1;
    }
    g.output_len += line_len;
    g.line_len = 0;
    append_output(g, NEWLINE) && append_output(g, PROMPT)
}

unsafe fn finish_line(g: &mut ProbeGlobals) {
    if g.line_len == 0 {
        (abi().process_exit)(0);
    }
    if !prepare_echo(g) {
        (abi().process_exit)(3);
    }
    g.phase = PHASE_WRITING;
    schedule_write(g);
}

unsafe fn resume() {
    let g = globals();
    if !g.initialized {
        core::ptr::write_bytes(g as *mut ProbeGlobals, 0, 1);
        g.initialized = true;
        if !prepare_greeting(g) {
            (abi().process_exit)(3);
        }
        g.phase = PHASE_WRITING;
        schedule_write(g);
    }

    loop {
        if g.overlapped.flags & FLAG_COMPLETED == 0 {
            return;
        }
        if g.overlapped.error != 0 {
            (abi().process_exit)(3);
        }

        match g.phase {
            PHASE_WRITING => {
                let count = g.overlapped.result_ext as usize;
                let remaining = g.output_len - g.output_offset;
                if count == 0 || count > remaining {
                    (abi().process_exit)(3);
                }
                g.output_offset += count;
                if g.output_offset < g.output_len {
                    schedule_write(g);
                    continue;
                }
                g.output_len = 0;
                g.output_offset = 0;
                g.phase = PHASE_READING;
                schedule_read(g);
            }
            PHASE_READING => {
                match g.overlapped.result_ext {
                    0 => (abi().process_exit)(0),
                    1 => {}
                    _ => (abi().process_exit)(3),
                }
                let byte = g.input_byte;
                if g.skip_lf {
                    g.skip_lf = false;
                    if byte == b'\n' {
                        schedule_read(g);
                        continue;
                    }
                }
                if byte == b'\r' || byte == b'\n' {
                    g.skip_lf = byte == b'\r';
                    finish_line(g);
                } else {
                    if g.line_len == LINE_CAPACITY {
                        (abi().process_exit)(2);
                    }
                    g.line.as_mut_ptr().add(g.line_len).write(byte);
                    g.line_len += 1;
                    schedule_read(g);
                }
            }
            _ => (abi().process_exit)(3),
        }
    }
}

#[cfg(target_arch = "x86_64")]
#[cfg(not(test))]
#[inline(never)]
unsafe extern "win64" fn probe_activation() {
    resume();
}

#[cfg(target_arch = "aarch64")]
#[cfg(not(test))]
#[inline(never)]
unsafe extern "C" fn probe_activation() {
    resume();
}

#[cfg(all(not(test), target_arch = "x86_64"))]
core::arch::global_asm!(
    ".section .text.probe_entry,\"ax\"",
    ".globl probe_code_start",
    "probe_code_start:",
    "jmp {activation}",
    activation = sym probe_activation,
);

#[cfg(all(not(test), target_arch = "aarch64"))]
core::arch::global_asm!(
    ".section .text.probe_entry,\"ax\"",
    ".globl probe_code_start",
    "probe_code_start:",
    "b {activation}",
    activation = sym probe_activation,
);

#[cfg(not(test))]
#[panic_handler]
fn panic(_info: &PanicInfo) -> ! {
    loop {}
}

#[cfg(test)]
static mut TEST_GLOBALS: ProbeGlobals = ProbeGlobals {
    initialized: false,
    phase: 0,
    skip_lf: false,
    line_len: 0,
    output_len: 0,
    output_offset: 0,
    input_byte: 0,
    overlapped: Overlapped {
        flags: 0,
        error: 0,
        continued: 0,
        result_ext: 0,
    },
    line: [0; LINE_CAPACITY],
    output: [0; OUTPUT_CAPACITY],
};

#[cfg(test)]
static mut TEST_ABI: core::mem::MaybeUninit<PalAbiTable> = core::mem::MaybeUninit::uninit();

#[cfg(test)]
static mut TEST_PENDING_KIND: u8 = 0;
#[cfg(test)]
static mut TEST_OPERATION_QUEUED: bool = false;
#[cfg(test)]
static mut TEST_PENDING_PTR: usize = 0;
#[cfg(test)]
static mut TEST_PENDING_LEN: usize = 0;
#[cfg(test)]
static mut TEST_INPUT: [u8; 16] = *b"hello\r\n\0\0\0\0\0\0\0\0\0";
#[cfg(test)]
static mut TEST_INPUT_OFFSET: usize = 0;
#[cfg(test)]
static mut TEST_OUTPUT: [u8; 2048] = [0; 2048];
#[cfg(test)]
static mut TEST_OUTPUT_LEN: usize = 0;

#[cfg(test)]
mod tests {
    use super::*;
    use core::mem::{offset_of, size_of};

    unsafe fn assert_unsupported_async(
        overlapped: &mut Overlapped,
        call: impl FnOnce(*mut Overlapped),
    ) {
        *overlapped = Overlapped {
            flags: 0,
            error: 0,
            continued: 17,
            result_ext: 23,
        };
        call(overlapped);
        assert_eq!(overlapped.flags, FLAG_COMPLETED);
        assert_eq!(overlapped.error, pal_abi::ERROR_UNSUPPORTED);
        assert_eq!(overlapped.continued, 0);
        assert_eq!(overlapped.result_ext, 0);
    }

    #[cfg(target_arch = "x86_64")]
    unsafe extern "win64" fn mock_read(
        ov: *mut Overlapped,
        _handle: u64,
        buffer: *mut u8,
        capacity: usize,
    ) {
        begin_pending(ov, 2, buffer as usize, capacity);
    }

    #[cfg(target_arch = "aarch64")]
    unsafe extern "C" fn mock_read(
        ov: *mut Overlapped,
        _handle: u64,
        buffer: *mut u8,
        capacity: usize,
    ) {
        begin_pending(ov, 2, buffer as usize, capacity);
    }

    #[cfg(target_arch = "x86_64")]
    unsafe extern "win64" fn mock_write(
        ov: *mut Overlapped,
        _handle: u64,
        buffer: *const u8,
        length: usize,
    ) {
        begin_pending(ov, 1, buffer as usize, length);
    }

    #[cfg(target_arch = "aarch64")]
    unsafe extern "C" fn mock_write(
        ov: *mut Overlapped,
        _handle: u64,
        buffer: *const u8,
        length: usize,
    ) {
        begin_pending(ov, 1, buffer as usize, length);
    }

    unsafe fn begin_pending(ov: *mut Overlapped, kind: u8, ptr: usize, len: usize) {
        (*ov).flags = 0;
        TEST_PENDING_KIND = kind;
        TEST_OPERATION_QUEUED = true;
        TEST_PENDING_PTR = ptr;
        TEST_PENDING_LEN = len;
    }

    #[cfg(target_arch = "x86_64")]
    unsafe extern "win64" fn mock_exit(_status: i32) -> ! {
        loop {}
    }

    #[cfg(target_arch = "aarch64")]
    unsafe extern "C" fn mock_exit(_status: i32) -> ! {
        loop {}
    }

    #[cfg(target_arch = "x86_64")]
    #[no_mangle]
    unsafe extern "win64" fn pal_abi_unsupported_exit(status: i32) -> ! {
        mock_exit(status)
    }

    #[cfg(target_arch = "aarch64")]
    #[no_mangle]
    unsafe extern "C" fn pal_abi_unsupported_exit(status: i32) -> ! {
        mock_exit(status)
    }

    unsafe fn complete_pending() {
        let g = globals();
        let count = if TEST_PENDING_KIND == 1 {
            let count = TEST_PENDING_LEN.min(3);
            let source = core::slice::from_raw_parts(TEST_PENDING_PTR as *const u8, count);
            core::ptr::copy_nonoverlapping(
                source.as_ptr(),
                core::ptr::addr_of_mut!(TEST_OUTPUT)
                    .cast::<u8>()
                    .add(TEST_OUTPUT_LEN),
                count,
            );
            TEST_OUTPUT_LEN += count;
            count
        } else {
            let input = TEST_INPUT[TEST_INPUT_OFFSET];
            TEST_INPUT_OFFSET += 1;
            (TEST_PENDING_PTR as *mut u8).write(input);
            1
        };
        g.overlapped.error = 0;
        g.overlapped.continued = 0;
        g.overlapped.result_ext = count as u64;
        g.overlapped.flags = FLAG_COMPLETED;
        TEST_PENDING_KIND = 0;
        TEST_OPERATION_QUEUED = false;
    }

    #[test]
    fn test_canonical_abi_and_globals_layout() {
        assert_eq!(size_of::<PalAbiTable>(), 47 * 8);
        assert_eq!(offset_of!(PalAbiTable, read), 144);
        assert_eq!(offset_of!(PalAbiTable, write), 152);
        assert_eq!(offset_of!(PalAbiTable, process_exit), 64);
        assert_eq!(offset_of!(ProbeGlobals, overlapped) % 8, 0);
    }

    #[test]
    fn test_async_write_partial_completion_and_reentry() {
        unsafe {
            TEST_GLOBALS.initialized = false;
            TEST_PENDING_KIND = 0;
            TEST_OPERATION_QUEUED = false;
            TEST_INPUT_OFFSET = 0;
            TEST_OUTPUT_LEN = 0;
            let table = pal_abi_stubs::for_probe(mock_read, mock_write, mock_exit);
            let mut unsupported = Overlapped {
                flags: 0,
                error: 0,
                continued: 17,
                result_ext: 23,
            };
            (table.timer_set)(&mut unsupported, 1);
            assert_eq!(unsupported.flags, FLAG_COMPLETED);
            assert_eq!(unsupported.error, pal_abi::ERROR_UNSUPPORTED);
            assert_eq!(unsupported.continued, 0);
            assert_eq!(unsupported.result_ext, 0);
            core::ptr::addr_of_mut!(TEST_ABI)
                .cast::<PalAbiTable>()
                .write(table);
            resume();
            assert!(TEST_OPERATION_QUEUED);

            for _ in 0..2048 {
                complete_pending();
                resume();
                if TEST_OUTPUT_LEN > 0
                    && core::str::from_utf8(&TEST_OUTPUT[..TEST_OUTPUT_LEN])
                        .unwrap()
                        .contains("echo: hello\n> ")
                {
                    break;
                }
            }

            assert!(TEST_OPERATION_QUEUED);
            assert_eq!(core::ptr::addr_of!(TEST_PENDING_KIND).read(), 2);
            let output = core::str::from_utf8(&TEST_OUTPUT[..TEST_OUTPUT_LEN]).unwrap();
            assert!(output.starts_with("Anonymous-memory probe ready (compiled "));
            assert!(output.contains("echo: hello\n> "));
        }
    }

    #[test]
    fn test_unsupported_async_slots_publish_complete_headers() {
        unsafe {
            let table = pal_abi_stubs::for_probe(mock_read, mock_write, mock_exit);
            let mut ov = Overlapped::default();

            assert_unsupported_async(&mut ov, |p| (table.timer_set)(p, 1));
            assert_unsupported_async(&mut ov, |p| (table.seek)(p, 1, 0, 0));
            assert_unsupported_async(&mut ov, |p| (table.handle_truncate)(p, 1, 0));
            assert_unsupported_async(&mut ov, |p| (table.path_open)(p, core::ptr::null(), 0, 0));
            assert_unsupported_async(&mut ov, |p| {
                (table.dir_read)(p, 1, core::ptr::null_mut(), 0)
            });
            assert_unsupported_async(&mut ov, |p| {
                (table.path_stat)(p, core::ptr::null(), 0, 0, core::ptr::null_mut(), 0)
            });
            assert_unsupported_async(&mut ov, |p| {
                (table.path_link)(p, core::ptr::null(), 0, core::ptr::null(), 0, 0)
            });
            assert_unsupported_async(&mut ov, |p| {
                (table.path_symlink)(p, core::ptr::null(), 0, core::ptr::null(), 0, 0)
            });
            assert_unsupported_async(&mut ov, |p| {
                (table.path_readlink)(p, core::ptr::null(), 0, core::ptr::null_mut(), 0)
            });
            assert_unsupported_async(&mut ov, |p| (table.path_chmod)(p, core::ptr::null(), 0, 0));
            assert_unsupported_async(&mut ov, |p| {
                (table.path_set_times)(p, core::ptr::null(), 0, 0, 0, 0, 0, 0)
            });
            assert_unsupported_async(&mut ov, |p| (table.path_remove)(p, core::ptr::null(), 0, 0));
            assert_unsupported_async(&mut ov, |p| (table.path_mkdir)(p, core::ptr::null(), 0, 0));
            assert_unsupported_async(&mut ov, |p| {
                (table.path_rename)(p, core::ptr::null(), 0, core::ptr::null(), 0)
            });
            assert_unsupported_async(&mut ov, |p| (table.net_open)(p, core::ptr::null(), 0, 0, 0));
            assert_unsupported_async(&mut ov, |p| (table.net_accept)(p, 1));
            assert_unsupported_async(&mut ov, |p| {
                (table.net_lookup)(p, core::ptr::null(), 0, core::ptr::null_mut(), 0)
            });
            assert_unsupported_async(&mut ov, |p| {
                (table.net_cert_verify)(p, core::ptr::null(), 0, core::ptr::null(), 0)
            });
            assert_unsupported_async(&mut ov, |p| (table.process_spawn)(p, core::ptr::null(), 0));
            assert_unsupported_async(&mut ov, |p| (table.process_pipe)(p));
            assert_unsupported_async(&mut ov, |p| (table.process_wait)(p, 1));
            assert_unsupported_async(&mut ov, |p| (table.signal_wait)(p, 0));
        }
    }
}
