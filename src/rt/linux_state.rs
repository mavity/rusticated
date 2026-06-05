#[cfg(any(target_os = "linux", rusticated_linux))]
use alloc::rc::Rc;
use alloc::vec::Vec;
use core::cell::{RefCell, UnsafeCell};
use core::task::Waker;

/// Internal Linux io_uring operation state.
///
/// This type is currently a placeholder for the stubbed io_uring driver.
/// Several fields exist for future completion and wake-up logic, but are not
/// yet read in the current implementation.
#[allow(dead_code)]
pub(crate) struct OpState {
    pub(crate) buffer: UnsafeCell<Option<Vec<u8>>>,
    pub(crate) waker: RefCell<Option<Waker>>,
    pub(crate) result: RefCell<Option<(i32, u32)>>, // (error_code, bytes_transferred)
    pub(crate) fd: i32,
    pub(crate) is_read: bool,
}

impl OpState {
    pub(crate) fn new(fd: i32, is_read: bool, buf: Option<Vec<u8>>) -> Rc<Self> {
        Rc::new(Self {
            buffer: UnsafeCell::new(buf),
            waker: RefCell::new(None),
            result: RefCell::new(None),
            fd,
            is_read,
        })
    }
}
