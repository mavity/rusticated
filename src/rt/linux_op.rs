#![cfg(target_os = "linux")]

use super::linux_state::OpState;
use crate::future::Future;
use crate::io;
use crate::pin::Pin;
use crate::task::{Context, Poll};
use alloc::rc::Rc;
use alloc::vec::Vec;

/// Future representing a Linux asynchronous file operation.
pub struct LinuxOpFuture {
    state: Rc<OpState>,
}

impl LinuxOpFuture {
    /// Initiates an asynchronous read operation.
    pub fn read(fd: i32, buf: Vec<u8>) -> Self {
        let state = OpState::new(fd, true, Some(buf));
        let state_clone = Rc::clone(&state);
        let _ = super::executor::with_driver(|d| d.submit_read(fd, state_clone));
        Self { state }
    }

    /// Initiates an asynchronous write operation.
    pub fn write(fd: i32, buf: Vec<u8>) -> Self {
        let state = OpState::new(fd, false, Some(buf));
        let state_clone = Rc::clone(&state);
        let _ = super::executor::with_driver(|d| d.submit_write(fd, state_clone));
        Self { state }
    }
}

impl Future for LinuxOpFuture {
    type Output = (io::Result<usize>, Vec<u8>);

    fn poll(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        let mut result_borrow = self.state.result.borrow_mut();
        if let Some((error_code, bytes)) = result_borrow.take() {
            // Reclaim buffer
            let buffer = unsafe { &mut *self.state.buffer.get() }.take().unwrap();
            if error_code != 0 {
                return Poll::Ready((Err(io::Error::from_raw_os_error(error_code)), buffer));
            } else {
                return Poll::Ready((Ok(bytes as usize), buffer));
            }
        }

        *self.state.waker.borrow_mut() = Some(cx.waker().clone());
        Poll::Pending
    }
}
