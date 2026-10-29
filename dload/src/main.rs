#![no_std]
#![no_main]

extern crate alloc;

mod allocator;
mod arch_call;
mod callbacks;
mod protocol;

#[cfg(windows)]
mod os_win;
#[cfg(windows)]
use os_win as os;

#[cfg(not(windows))]
mod os_unix;
#[cfg(not(windows))]
use os_unix as os;

use alloc::string::String;
use alloc::vec::Vec;
use core::sync::atomic::{AtomicBool, Ordering};
use protocol::*;

static READER_BUSY: AtomicBool = AtomicBool::new(false);

pub fn try_acquire_reader_lock() -> bool {
    READER_BUSY
        .compare_exchange(false, true, Ordering::Acquire, Ordering::Relaxed)
        .is_ok()
}

pub fn release_reader_lock() {
    READER_BUSY.store(false, Ordering::Release);
}

#[global_allocator]
static ALLOCATOR: allocator::OsAllocator = allocator::OsAllocator;

// ─── C stdlib intrinsics & unwind symbols ─────────────────────────────────────

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memcpy(dest: *mut u8, src: *const u8, n: usize) -> *mut u8 {
    let mut i = 0;
    while i < n { unsafe { *dest.add(i) = *src.add(i); } i += 1; }
    dest
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn memset(s: *mut u8, c: i32, n: usize) -> *mut u8 {
    let mut i = 0;
    while i < n { unsafe { *s.add(i) = c as u8; } i += 1; }
    s
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn memmove(dest: *mut u8, src: *const u8, n: usize) -> *mut u8 {
    if dest < src as *mut u8 { unsafe { memcpy(dest, src, n) } }
    else {
        let mut i = n;
        while i > 0 { i -= 1; unsafe { *dest.add(i) = *src.add(i); } }
        dest
    }
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn memcmp(s1: *const u8, s2: *const u8, n: usize) -> i32 {
    let mut i = 0;
    while i < n {
        let v1 = unsafe { *s1.add(i) }; let v2 = unsafe { *s2.add(i) };
        if v1 != v2 { return v1 as i32 - v2 as i32; }
        i += 1;
    }
    0
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn bcmp(s1: *const u8, s2: *const u8, n: usize) -> i32 { unsafe { memcmp(s1, s2, n) } }
#[unsafe(no_mangle)]
pub unsafe extern "C" fn strlen(s: *const u8) -> usize {
    let mut n = 0; while unsafe { *s.add(n) } != 0 { n += 1; } n
}

#[cfg(windows)]
#[unsafe(no_mangle)]
pub extern "C" fn __main() {}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn _Unwind_Resume() {}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn rust_eh_personality() {}

#[cfg(windows)]
#[unsafe(no_mangle)]
pub static _fltused: i32 = 0;

#[panic_handler]
fn panic(_info: &core::panic::PanicInfo) -> ! {
    exit_process(101)
}

fn exit_process(code: i32) -> ! {
    #[cfg(windows)]
    unsafe {
        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" {
            fn ExitProcess(uExitCode: u32) -> !;
        }
        ExitProcess(code as u32);
    }
    #[cfg(target_os = "linux")]
    unsafe {
        #[cfg(target_arch = "x86_64")]
        core::arch::asm!("syscall", in("rax") 231usize, in("rdi") code as usize, options(noreturn));
        #[cfg(target_arch = "aarch64")]
        core::arch::asm!("svc #0", in("x8") 94usize, in("x0") code as usize, options(noreturn));
        #[cfg(target_arch = "arm")]
        core::arch::asm!("svc #0", in("r7") 248usize, in("r0") code as usize, options(noreturn));
    }
    #[cfg(target_os = "macos")]
    unsafe {
        unsafe extern "C" {
            fn exit(code: i32) -> !;
        }
        exit(code);
    }
}

pub fn dispatch_frame(hdr: Header, body: &[u8]) {
    match hdr.opcode {
        OP_OPEN_REQ => {
            if body.len() < 8 {
                let _ = send_frame(OP_OPEN_RESP, hdr.id, &[1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]);
                return;
            }
            let flags = i32::from_le_bytes(body[0..4].try_into().unwrap());
            let path_len = u32::from_le_bytes(body[4..8].try_into().unwrap()) as usize;
            let path_bytes = &body[8..8 + path_len];
            let path = core::str::from_utf8(path_bytes).unwrap_or("");

            let mut resp = Vec::new();
            match os::open(path, flags) {
                Ok(h) => {
                    resp.push(0); // ok
                    resp.extend_from_slice(&h.to_le_bytes());
                    resp.extend_from_slice(&0u32.to_le_bytes());
                }
                Err(err) => {
                    resp.push(1); // err
                    resp.extend_from_slice(&0u64.to_le_bytes());
                    let err_bytes = err.as_bytes();
                    resp.extend_from_slice(&(err_bytes.len() as u32).to_le_bytes());
                    resp.extend_from_slice(err_bytes);
                }
            }
            let _ = send_frame(OP_OPEN_RESP, hdr.id, &resp);
        }
        OP_SYM_REQ => {
            if body.len() < 12 {
                return;
            }
            let handle = u64::from_le_bytes(body[0..8].try_into().unwrap());
            let name_len = u32::from_le_bytes(body[8..12].try_into().unwrap()) as usize;
            let name_bytes = &body[12..12 + name_len];
            let name = core::str::from_utf8(name_bytes).unwrap_or("");

            let mut resp = Vec::new();
            match os::sym(handle, name) {
                Ok(s) => {
                    resp.push(0);
                    resp.extend_from_slice(&s.to_le_bytes());
                    resp.extend_from_slice(&0u32.to_le_bytes());
                }
                Err(err) => {
                    resp.push(1);
                    resp.extend_from_slice(&0u64.to_le_bytes());
                    let err_bytes = err.as_bytes();
                    resp.extend_from_slice(&(err_bytes.len() as u32).to_le_bytes());
                    resp.extend_from_slice(err_bytes);
                }
            }
            let _ = send_frame(OP_SYM_RESP, hdr.id, &resp);
        }
        OP_CLOSE_REQ => {
            if body.len() < 8 {
                return;
            }
            let handle = u64::from_le_bytes(body[0..8].try_into().unwrap());
            let mut resp = Vec::new();
            match os::close(handle) {
                Ok(()) => {
                    resp.push(0);
                    resp.extend_from_slice(&0u32.to_le_bytes());
                }
                Err(err) => {
                    resp.push(1);
                    let err_bytes = err.as_bytes();
                    resp.extend_from_slice(&(err_bytes.len() as u32).to_le_bytes());
                    resp.extend_from_slice(err_bytes);
                }
            }
            let _ = send_frame(OP_CLOSE_RESP, hdr.id, &resp);
        }
        OP_CALL_REQ => {
            if body.len() < 24 {
                return;
            }
            let sym_addr = u64::from_le_bytes(body[0..8].try_into().unwrap());
            let parent_inv_id = u64::from_le_bytes(body[8..16].try_into().unwrap());
            let sig_len = u32::from_le_bytes(body[16..20].try_into().unwrap()) as usize;
            let off = 20 + sig_len;
            if body.len() < off + 4 {
                return;
            }
            let argc = u32::from_le_bytes(body[off..off + 4].try_into().unwrap()) as usize;
            let mut args = Vec::with_capacity(argc);
            let mut arg_off = off + 4;
            for _ in 0..argc {
                if body.len() < arg_off + 8 {
                    break;
                }
                args.push(u64::from_le_bytes(body[arg_off..arg_off + 8].try_into().unwrap()));
                arg_off += 8;
            }

            if callbacks::try_delegate_call(parent_inv_id, hdr.id, sym_addr as usize, &args) {
                return;
            }

            let release_during_call = parent_inv_id == 0
                && os::current_thread_id() == callbacks::reader_thread_id();
            if release_during_call {
                release_reader_lock();
            }

            let ret_val = unsafe { arch_call::call_c_func(sym_addr as usize, &args) };

            if release_during_call {
                while !try_acquire_reader_lock() {
                    core::hint::spin_loop();
                }
            }

            let mut resp = Vec::new();
            resp.push(0); // ok
            resp.extend_from_slice(&1u32.to_le_bytes()); // 1 result
            resp.extend_from_slice(&ret_val.to_le_bytes());
            resp.extend_from_slice(&0u32.to_le_bytes()); // err_len = 0
            let _ = send_frame(OP_CALL_RESP, hdr.id, &resp);
        }
        OP_ALLOC_REQ => {
            if body.len() < 12 {
                return;
            }
            let size = u64::from_le_bytes(body[0..8].try_into().unwrap());
            let mut resp = Vec::new();
            match os::alloc(size) {
                Ok(ptr) => {
                    resp.push(0);
                    resp.extend_from_slice(&ptr.to_le_bytes());
                    resp.extend_from_slice(&0u32.to_le_bytes());
                }
                Err(err) => {
                    resp.push(1);
                    resp.extend_from_slice(&0u64.to_le_bytes());
                    let err_bytes = err.as_bytes();
                    resp.extend_from_slice(&(err_bytes.len() as u32).to_le_bytes());
                    resp.extend_from_slice(err_bytes);
                }
            }
            let _ = send_frame(OP_ALLOC_RESP, hdr.id, &resp);
        }
        OP_FREE_REQ => {
            if body.len() < 8 {
                return;
            }
            let ptr = u64::from_le_bytes(body[0..8].try_into().unwrap());
            let mut resp = Vec::new();
            match os::free(ptr) {
                Ok(()) => {
                    resp.push(0);
                    resp.extend_from_slice(&0u32.to_le_bytes());
                }
                Err(err) => {
                    resp.push(1);
                    let err_bytes = err.as_bytes();
                    resp.extend_from_slice(&(err_bytes.len() as u32).to_le_bytes());
                    resp.extend_from_slice(err_bytes);
                }
            }
            let _ = send_frame(OP_FREE_RESP, hdr.id, &resp);
        }
        OP_READ_MEM_REQ => {
            if body.len() < 12 {
                return;
            }
            let ptr = u64::from_le_bytes(body[0..8].try_into().unwrap());
            let len = u32::from_le_bytes(body[8..12].try_into().unwrap());
            let mut resp = Vec::new();
            match os::read_mem(ptr, len) {
                Ok(data) => {
                    resp.push(0);
                    resp.extend_from_slice(&(data.len() as u32).to_le_bytes());
                    resp.extend_from_slice(&data);
                    resp.extend_from_slice(&0u32.to_le_bytes());
                }
                Err(err) => {
                    resp.push(1);
                    resp.extend_from_slice(&0u32.to_le_bytes());
                    let err_bytes = err.as_bytes();
                    resp.extend_from_slice(&(err_bytes.len() as u32).to_le_bytes());
                    resp.extend_from_slice(err_bytes);
                }
            }
            let _ = send_frame(OP_READ_MEM_RESP, hdr.id, &resp);
        }
        OP_WRITE_MEM_REQ => {
            if body.len() < 12 {
                return;
            }
            let ptr = u64::from_le_bytes(body[0..8].try_into().unwrap());
            let len = u32::from_le_bytes(body[8..12].try_into().unwrap()) as usize;
            let data = &body[12..12 + len];
            let mut resp = Vec::new();
            match os::write_mem(ptr, data) {
                Ok(()) => {
                    resp.push(0);
                    resp.extend_from_slice(&0u32.to_le_bytes());
                }
                Err(err) => {
                    resp.push(1);
                    let err_bytes = err.as_bytes();
                    resp.extend_from_slice(&(err_bytes.len() as u32).to_le_bytes());
                    resp.extend_from_slice(err_bytes);
                }
            }
            let _ = send_frame(OP_WRITE_MEM_RESP, hdr.id, &resp);
        }
        OP_CB_REG_REQ => {
            if body.len() < 8 {
                return;
            }
            let cb_id = u64::from_le_bytes(body[0..8].try_into().unwrap());
            let mut resp = Vec::new();
            match callbacks::register_trampoline(cb_id) {
                Some(trampoline_ptr) => {
                    resp.push(0);
                    resp.extend_from_slice(&trampoline_ptr.to_le_bytes());
                    resp.extend_from_slice(&0u32.to_le_bytes());
                }
                None => {
                    resp.push(1);
                    resp.extend_from_slice(&0u64.to_le_bytes());
                    let err_str = String::from("no callback trampoline slots available");
                    let err_bytes = err_str.as_bytes();
                    resp.extend_from_slice(&(err_bytes.len() as u32).to_le_bytes());
                    resp.extend_from_slice(err_bytes);
                }
            }
            let _ = send_frame(OP_CB_REG_RESP, hdr.id, &resp);
        }
        OP_CB_UNREG_REQ => {
            if body.len() >= 8 {
                let cb_id = u64::from_le_bytes(body[0..8].try_into().unwrap());
                callbacks::unregister_trampoline(cb_id);
            }
            let _ = send_frame(OP_CB_UNREG_RESP, hdr.id, &[0]);
        }
        OP_CB_RETURN => {
            let mut ret_val = 0u64;
            if body.len() >= 13 {
                let res_count = u32::from_le_bytes(body[1..5].try_into().unwrap());
                if res_count > 0 {
                    ret_val = u64::from_le_bytes(body[5..13].try_into().unwrap());
                }
            }
            callbacks::on_cb_return(hdr.id, ret_val);
        }
        _ => {}
    }
}

fn run() {
    callbacks::set_reader_thread_id(os::current_thread_id());
    // Send initial handshake frame: [os_kind: u8][arch_kind: u8][version: u16]
    #[cfg(windows)]
    let os_kind: u8 = 1;
    #[cfg(all(target_os = "linux", not(target_os = "android")))]
    let os_kind: u8 = 2;
    #[cfg(target_os = "macos")]
    let os_kind: u8 = 3;
    #[cfg(target_os = "android")]
    let os_kind: u8 = 4;

    #[cfg(target_arch = "x86_64")]
    let arch_kind: u8 = 1;
    #[cfg(target_arch = "aarch64")]
    let arch_kind: u8 = 2;
    #[cfg(target_arch = "arm")]
    let arch_kind: u8 = 3;

    let handshake_payload: [u8; 4] = [os_kind, arch_kind, 1, 0];
    let _ = send_frame(OP_HANDSHAKE, 0, &handshake_payload);

    while !try_acquire_reader_lock() {
        core::hint::spin_loop();
    }
    loop {
        let frame_res = read_frame();
        match frame_res {
            Ok((hdr, body)) => dispatch_frame(hdr, &body),
            Err(()) => break,
        }
    }
    release_reader_lock();
}

#[cfg(windows)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mainCRTStartup() -> ! {
    run();
    exit_process(0);
}

#[cfg(windows)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn main() -> i32 {
    run();
    0
}

#[cfg(target_os = "linux")]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn _start() -> ! {
    run();
    exit_process(0);
}

#[cfg(target_os = "macos")]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn main() -> i32 {
    run();
    0
}
