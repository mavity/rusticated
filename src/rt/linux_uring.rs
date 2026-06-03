#![cfg(any(target_os = "linux", rusticated_linux))]
#![allow(dead_code)]
//! Internal Linux io_uring stubs are present for future implementation.

use super::linux_state::OpState;
use crate::io;
use alloc::rc::Rc;

const SYS_IO_URING_SETUP: usize = 425;
const SYS_IO_URING_ENTER: usize = 426;

// Minimal inline struct definitions according to stable abi
#[repr(C)]
struct io_uring_sqe {
    opcode: u8,
    flags: u8,
    ioprio: u16,
    fd: i32,
    off: u64,
    addr: u64,
    len: u32,
    rw_flags: u32,
    user_data: u64,
    buf_index: u16,
    personality: u16,
    splice_fd_in: i32,
    addr3: u64,
    __pad2: [u64; 1],
}

#[repr(C)]
struct io_uring_cqe {
    user_data: u64,
    res: i32,
    flags: u32,
}

/// Stubbed io_uring-based Linux driver.
pub(crate) struct UringDriver {
    // ...
}

impl UringDriver {
    /// Creates a new io_uring driver instance.
    pub fn new() -> io::Result<Self> {
        Err(io::Error::other("io_uring not yet fully implemented"))
    }
    /// Polls io_uring for completions with an optional timeout.
    pub fn poll_with_timeout(&mut self, _timeout_ms: Option<u32>) -> io::Result<bool> {
        Ok(false)
    }
    pub(crate) fn submit_read(&mut self, _fd: i32, _state: Rc<OpState>) -> io::Result<()> {
        Ok(())
    }
    pub(crate) fn submit_write(&mut self, _fd: i32, _state: Rc<OpState>) -> io::Result<()> {
        Ok(())
    }
    /// Registers a waker token with the io_uring driver.
    pub fn register_waker(&mut self, _token: u64, _waker: core::task::Waker) {}

    /// Requests a read readiness token for the given file descriptor.
    pub fn register_read(&mut self, _fd: i32) -> io::Result<u64> {
        Ok(0)
    }
    /// Requests a write readiness token for the given file descriptor.
    pub fn register_write(&mut self, _fd: i32) -> io::Result<u64> {
        Ok(0)
    }
}

impl UringDriver {
    /// Returns the number of outstanding I/O operations in the driver.
    pub fn outstanding_io(&self) -> usize {
        0
    }
}
