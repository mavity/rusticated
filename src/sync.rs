//! Synchronisation primitives for `#![no_std]` environments.

pub use core::sync::*;
pub use alloc::sync::Arc;
pub use spin::lazy::Lazy as LazyLock;
pub use spin::mutex::SpinMutex;
pub use spin::{Mutex, RwLock};

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
