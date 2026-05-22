use super::linux_state::OpState;
use alloc::rc::Rc;
use crate::io;

pub(crate) enum Driver {
    Uring(super::linux_uring::UringDriver),
    Epoll(super::linux_epoll::EpollDriver),
}

impl Driver {
    pub fn new() -> io::Result<Self> {
        if let Ok(uring) = super::linux_uring::UringDriver::new() {
            Ok(Self::Uring(uring))
        } else {
            Ok(Self::Epoll(super::linux_epoll::EpollDriver::new()?))
        }
    }

    pub fn poll_with_timeout(&mut self, timeout_ms: Option<u32>) -> io::Result<bool> {
        match self {
            Self::Uring(d) => d.poll_with_timeout(timeout_ms),
            Self::Epoll(d) => d.poll_with_timeout(timeout_ms),
        }
    }

    pub fn register_waker(&mut self, token: u64, waker: core::task::Waker) {
        match self {
            Self::Uring(d) => d.register_waker(token, waker),
            Self::Epoll(d) => d.register_waker(token, waker),
        }
    }

    pub fn register_read(&mut self, fd: i32) -> io::Result<u64> {
        match self {
            Self::Uring(d) => d.register_read(fd),
            Self::Epoll(d) => d.register_read(fd),
        }
    }

    pub fn register_write(&mut self, fd: i32) -> io::Result<u64> {
        match self {
            Self::Uring(d) => d.register_write(fd),
            Self::Epoll(d) => d.register_write(fd),
        }
    }

    pub(crate) fn submit_read(&mut self, fd: i32, state: Rc<OpState>) -> io::Result<()> {
        match self {
            Self::Uring(d) => d.submit_read(fd, state),
            Self::Epoll(d) => d.submit_read(fd, state),
        }
    }

    pub(crate) fn submit_write(&mut self, fd: i32, state: Rc<OpState>) -> io::Result<()> {
        match self {
            Self::Uring(d) => d.submit_write(fd, state),
            Self::Epoll(d) => d.submit_write(fd, state),
        }
    }
}

impl Driver {
    pub fn outstanding_io(&self) -> usize {
        match self {
            Self::Uring(d) => d.outstanding_io(),
            Self::Epoll(d) => d.outstanding_io(),
        }
    }
}
