use alloc::vec::Vec;

pub const HEADER_SIZE: usize = 13;

pub const OP_HANDSHAKE: u8 = 0x01;
pub const OP_OPEN_REQ: u8 = 0x10;
pub const OP_OPEN_RESP: u8 = 0x11;
pub const OP_SYM_REQ: u8 = 0x12;
pub const OP_SYM_RESP: u8 = 0x13;
pub const OP_CLOSE_REQ: u8 = 0x14;
pub const OP_CLOSE_RESP: u8 = 0x15;
pub const OP_CALL_REQ: u8 = 0x20;
pub const OP_CALL_RESP: u8 = 0x21;
pub const OP_ALLOC_REQ: u8 = 0x30;
pub const OP_ALLOC_RESP: u8 = 0x31;
pub const OP_FREE_REQ: u8 = 0x32;
pub const OP_FREE_RESP: u8 = 0x33;
pub const OP_READ_MEM_REQ: u8 = 0x34;
pub const OP_READ_MEM_RESP: u8 = 0x35;
pub const OP_WRITE_MEM_REQ: u8 = 0x36;
pub const OP_WRITE_MEM_RESP: u8 = 0x37;
pub const OP_CB_REG_REQ: u8 = 0x40;
pub const OP_CB_REG_RESP: u8 = 0x41;
pub const OP_CB_INVOKE: u8 = 0x42;
pub const OP_CB_RETURN: u8 = 0x43;

#[derive(Clone, Copy, Debug)]
pub struct Header {
    pub payload_len: u32,
    pub opcode: u8,
    pub id: u64,
}

impl Header {
    pub fn encode(&self) -> [u8; HEADER_SIZE] {
        let mut buf = [0u8; HEADER_SIZE];
        buf[0..4].copy_from_slice(&self.payload_len.to_le_bytes());
        buf[4] = self.opcode;
        buf[5..13].copy_from_slice(&self.id.to_le_bytes());
        buf
    }

    pub fn decode(buf: &[u8]) -> Option<Self> {
        if buf.len() < HEADER_SIZE {
            return None;
        }
        let payload_len = u32::from_le_bytes(buf[0..4].try_into().ok()?);
        let opcode = buf[4];
        let id = u64::from_le_bytes(buf[5..13].try_into().ok()?);
        Some(Header {
            payload_len,
            opcode,
            id,
        })
    }
}

// ─── Low-level I/O primitives ────────────────────────────────────────────────

#[cfg(windows)]
mod io_impl {
    #[link(name = "kernel32", kind = "raw-dylib")]
    unsafe extern "system" {
        fn GetStdHandle(nStdHandle: u32) -> *mut core::ffi::c_void;
        fn ReadFile(
            hFile: *mut core::ffi::c_void,
            lpBuffer: *mut u8,
            nNumberOfBytesToRead: u32,
            lpNumberOfBytesRead: *mut u32,
            lpOverlapped: *mut core::ffi::c_void,
        ) -> i32;
        fn WriteFile(
            hFile: *mut core::ffi::c_void,
            lpBuffer: *const u8,
            nNumberOfBytesToWrite: u32,
            lpNumberOfBytesWritten: *mut u32,
            lpOverlapped: *mut core::ffi::c_void,
        ) -> i32;
    }

    const STD_INPUT_HANDLE: u32 = 0xFFFFFFF6;  // -10
    const STD_OUTPUT_HANDLE: u32 = 0xFFFFFFF5; // -11

    pub fn read_stdin(buf: &mut [u8]) -> Result<usize, ()> {
        let h = unsafe { GetStdHandle(STD_INPUT_HANDLE) };
        if h.is_null() || h as isize == -1 {
            return Err(());
        }
        let mut read = 0u32;
        let ok = unsafe {
            ReadFile(
                h,
                buf.as_mut_ptr(),
                buf.len() as u32,
                &mut read,
                core::ptr::null_mut(),
            )
        };
        if ok == 0 {
            Err(())
        } else {
            Ok(read as usize)
        }
    }

    pub fn write_stdout(buf: &[u8]) -> Result<usize, ()> {
        let h = unsafe { GetStdHandle(STD_OUTPUT_HANDLE) };
        if h.is_null() || h as isize == -1 {
            return Err(());
        }
        let mut written = 0u32;
        let ok = unsafe {
            WriteFile(
                h,
                buf.as_ptr(),
                buf.len() as u32,
                &mut written,
                core::ptr::null_mut(),
            )
        };
        if ok == 0 {
            Err(())
        } else {
            Ok(written as usize)
        }
    }
}

#[cfg(not(windows))]
mod io_impl {
    unsafe extern "C" {
        fn read(fd: i32, buf: *mut u8, count: usize) -> isize;
        fn write(fd: i32, buf: *const u8, count: usize) -> isize;
    }

    pub fn read_stdin(buf: &mut [u8]) -> Result<usize, ()> {
        let ret = unsafe { read(0, buf.as_mut_ptr(), buf.len()) };
        if ret <= 0 {
            Err(())
        } else {
            Ok(ret as usize)
        }
    }

    pub fn write_stdout(buf: &[u8]) -> Result<usize, ()> {
        let ret = unsafe { write(1, buf.as_ptr(), buf.len()) };
        if ret <= 0 {
            Err(())
        } else {
            Ok(ret as usize)
        }
    }
}

pub fn read_exact(mut buf: &mut [u8]) -> Result<(), ()> {
    while !buf.is_empty() {
        let n = io_impl::read_stdin(buf)?;
        if n == 0 {
            return Err(());
        }
        buf = &mut buf[n..];
    }
    Ok(())
}

pub fn write_all(mut buf: &[u8]) -> Result<(), ()> {
    while !buf.is_empty() {
        let n = io_impl::write_stdout(buf)?;
        if n == 0 {
            return Err(());
        }
        buf = &buf[n..];
    }
    Ok(())
}

use core::sync::atomic::{AtomicBool, Ordering};

struct SpinLock(AtomicBool);
impl SpinLock {
    const fn new() -> Self { SpinLock(AtomicBool::new(false)) }
    fn lock(&self) {
        while self.0.compare_exchange_weak(false, true, Ordering::Acquire, Ordering::Relaxed).is_err() {
            core::hint::spin_loop();
        }
    }
    fn unlock(&self) {
        self.0.store(false, Ordering::Release);
    }
}
static WRITE_LOCK: SpinLock = SpinLock::new();

pub fn send_frame(opcode: u8, id: u64, payload: &[u8]) -> Result<(), ()> {
    let header = Header {
        payload_len: payload.len() as u32,
        opcode,
        id,
    };
    WRITE_LOCK.lock();
    let res = (|| {
        write_all(&header.encode())?;
        if !payload.is_empty() {
            write_all(payload)?;
        }
        Ok(())
    })();
    WRITE_LOCK.unlock();
    res
}

pub fn read_frame() -> Result<(Header, Vec<u8>), ()> {
    let mut header_buf = [0u8; HEADER_SIZE];
    read_exact(&mut header_buf)?;
    let header = Header::decode(&header_buf).ok_or(())?;
    let mut payload = alloc::vec![0u8; header.payload_len as usize];
    if header.payload_len > 0 {
        read_exact(&mut payload)?;
    }
    Ok((header, payload))
}
