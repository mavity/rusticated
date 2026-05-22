use alloc::vec::Vec;
use alloc::rc::Rc;
use core::cell::{RefCell, UnsafeCell};
use core::task::Waker;

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
