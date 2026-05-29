//! Synchronisation primitives for `#![no_std]` environments.

pub use alloc::sync::{Arc, Weak};
pub use core::sync::*;
pub use spin::LazyLock;
pub use spin::mutex::SpinMutex;
pub use spin::{Mutex, MutexGuard, RwLock, RwLockReadGuard, RwLockWriteGuard};

/// A synchronization primitive which can be written to only once.
pub struct OnceLock<T> {
    inner: spin::Once<T>,
}

impl<T> OnceLock<T> {
    /// Creates a new empty cell.
    pub const fn new() -> Self {
        Self {
            inner: spin::Once::new(),
        }
    }

    /// Gets the contents of the cell, initializing it with `f` if the cell was empty.
    pub fn get_or_init<F>(&self, f: F) -> &T
    where
        F: FnOnce() -> T,
    {
        self.inner.call_once(f)
    }

    /// Gets the contents of the cell, returning `None` if the cell is empty.
    pub fn get(&self) -> Option<&T> {
        self.inner.get()
    }
}

// Safety: spin::Once is Send/Sync if T is.
unsafe impl<T: Sync + Send> Sync for OnceLock<T> {}
unsafe impl<T: Send> Send for OnceLock<T> {}

/// Atomic types and memory orderings.
pub mod atomic {
    pub use core::sync::atomic::*;
}

/// A multi-producer, single-consumer channel.
pub mod mpsc {
    use super::{Arc, Mutex};
    use alloc::collections::VecDeque;
    use core::sync::atomic::{AtomicBool, Ordering};

    struct Inner<T> {
        queue: Mutex<VecDeque<T>>,
        sender_alive: AtomicBool,
    }

    /// The sending half of a channel.
    pub struct Sender<T> {
        inner: Arc<Inner<T>>,
    }

    /// The receiving half of a channel.
    pub struct Receiver<T> {
        inner: Arc<Inner<T>>,
    }

    /// Error returned by `Sender::send` when the receiver has been dropped.
    pub struct SendError<T>(pub T);

    /// Error returned by `Receiver::try_recv`.
    #[derive(Debug, PartialEq, Eq)]
    pub enum TryRecvError {
        /// No message available yet.
        Empty,
        /// The sender has been dropped and the queue is empty.
        Disconnected,
    }

    /// Error returned by `Receiver::recv_timeout` and `Receiver::recv`.
    #[derive(Debug, PartialEq, Eq)]
    pub struct RecvTimeoutError;

    impl core::fmt::Display for RecvTimeoutError {
        fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
            write!(f, "channel receive timed out or disconnected")
        }
    }

    /// Create a new channel, returning `(Sender, Receiver)`.
    pub fn channel<T>() -> (Sender<T>, Receiver<T>) {
        let inner = Arc::new(Inner {
            queue: Mutex::new(VecDeque::new()),
            sender_alive: AtomicBool::new(true),
        });
        (
            Sender {
                inner: inner.clone(),
            },
            Receiver { inner },
        )
    }

    impl<T> Sender<T> {
        /// Send a value. Returns `Err` if the receiver has been dropped.
        pub fn send(&self, val: T) -> Result<(), SendError<T>> {
            if !self.inner.sender_alive.load(Ordering::Acquire) {
                return Err(SendError(val));
            }
            self.inner.queue.lock().push_back(val);
            Ok(())
        }
    }

    impl<T> Clone for Sender<T> {
        fn clone(&self) -> Self {
            Self {
                inner: self.inner.clone(),
            }
        }
    }

    impl<T> Drop for Sender<T> {
        fn drop(&mut self) {
            // Signal that no more sends are coming.
            self.inner.sender_alive.store(false, Ordering::Release);
        }
    }

    impl<T> Receiver<T> {
        /// Try to receive a value without blocking.
        pub fn try_recv(&self) -> Result<T, TryRecvError> {
            match self.inner.queue.lock().pop_front() {
                Some(val) => Ok(val),
                None => {
                    if !self.inner.sender_alive.load(Ordering::Acquire) {
                        Err(TryRecvError::Disconnected)
                    } else {
                        Err(TryRecvError::Empty)
                    }
                }
            }
        }

        /// Block until a value is available or the timeout expires.
        #[cfg(not(target_family = "wasm"))]
        pub fn recv_timeout(&self, timeout: crate::time::Duration) -> Result<T, RecvTimeoutError> {
            let deadline = crate::time::Instant::now() + timeout;
            loop {
                match self.try_recv() {
                    Ok(val) => return Ok(val),
                    Err(TryRecvError::Disconnected) => return Err(RecvTimeoutError),
                    Err(TryRecvError::Empty) => {}
                }
                if crate::time::Instant::now() >= deadline {
                    return Err(RecvTimeoutError);
                }
                crate::thread::sleep_ms(1);
            }
        }

        /// Block until a value is available.
        #[cfg(not(target_family = "wasm"))]
        pub fn recv(&self) -> Result<T, RecvTimeoutError> {
            loop {
                match self.try_recv() {
                    Ok(val) => return Ok(val),
                    Err(TryRecvError::Disconnected) => return Err(RecvTimeoutError),
                    Err(TryRecvError::Empty) => {}
                }
                crate::thread::yield_now();
            }
        }
    }

    // Safety: Sender/Receiver are Send if T is Send.
    unsafe impl<T: Send> Send for Sender<T> {}
    unsafe impl<T: Send> Send for Receiver<T> {}
    unsafe impl<T: Send> Sync for Sender<T> {}
}
