#[cfg(any(target_os = "linux", rusticated_linux))]
use super::linux_state::OpState;
use crate::io;
use alloc::rc::Rc;

/// Abstraction over available Linux runtime drivers.
pub(crate) enum Driver {
    Uring(super::linux_uring::UringDriver),
    Epoll(super::linux_epoll::EpollDriver),
}

impl Driver {
    /// Creates a new Linux driver, preferring io_uring when available.
    pub fn new() -> io::Result<Self> {
        if let Ok(uring) = super::linux_uring::UringDriver::new() {
            Ok(Self::Uring(uring))
        } else {
            Ok(Self::Epoll(super::linux_epoll::EpollDriver::new()?))
        }
    }

    /// Polls for readiness events with an optional timeout.
    pub fn poll_with_timeout(&mut self, timeout_ms: Option<u32>) -> io::Result<bool> {
        match self {
            Self::Uring(d) => d.poll_with_timeout(timeout_ms),
            Self::Epoll(d) => d.poll_with_timeout(timeout_ms),
        }
    }

    /// Registers a waker for the given readiness token.
    pub fn register_waker(&mut self, token: u64, waker: core::task::Waker) {
        match self {
            Self::Uring(d) => d.register_waker(token, waker),
            Self::Epoll(d) => d.register_waker(token, waker),
        }
    }

    /// Registers a file descriptor for read readiness.
    pub fn register_read(&mut self, fd: i32) -> io::Result<u64> {
        match self {
            Self::Uring(d) => d.register_read(fd),
            Self::Epoll(d) => d.register_read(fd),
        }
    }

    /// Registers a file descriptor for write readiness.
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

    pub(crate) fn submit_cancel(&mut self, state: Rc<OpState>) -> io::Result<()> {
        match self {
            Self::Uring(d) => d.submit_cancel(state),
            Self::Epoll(d) => d.submit_cancel(state),
        }
    }
}

impl Driver {
    /// Returns the number of outstanding registered operations.
    ///
    /// This wrapper is retained for future instrumentation and stubbed driver
    /// implementations, even when it is not currently called.
    #[allow(dead_code)]
    pub fn outstanding_io(&self) -> usize {
        match self {
            Self::Uring(d) => d.outstanding_io(),
            Self::Epoll(d) => d.outstanding_io(),
        }
    }
}

/// Interrupt a sleeping `epoll_wait` from any thread (e.g. a blocking-op worker).
/// On uring this is a no-op because uring submissions already produce completions.
pub(crate) fn queue_wake() {
    super::linux_epoll::queue_wake();
}
