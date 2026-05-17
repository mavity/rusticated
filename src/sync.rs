//! Synchronisation primitives for `#![no_std]` environments.

use crate::cell::UnsafeCell;
use crate::hint;
use crate::ops::{Deref, DerefMut};
use crate::sync::atomic::{AtomicBool, Ordering};

/// A mutual-exclusion lock implemented as a busy-waiting spinlock.
///
/// Unlike `std::sync::Mutex`, this never calls into the OS scheduler and
/// never parks the calling thread.  It is intended for short-held critical
/// sections such as storing a [`core::task::Waker`].
pub struct SpinMutex<T> {
    locked: AtomicBool,
    data: UnsafeCell<T>,
}

// SAFETY: `SpinMutex<T>` is `Send + Sync` when `T: Send` because the
// `AtomicBool` gate ensures exclusive access to the inner `T`.
unsafe impl<T: Send> Send for SpinMutex<T> {}
unsafe impl<T: Send> Sync for SpinMutex<T> {}

impl<T> SpinMutex<T> {
    /// Creates a new `SpinMutex` wrapping `value`.
    pub const fn new(value: T) -> Self {
        Self {
            locked: AtomicBool::new(false),
            data: UnsafeCell::new(value),
        }
    }

    /// Acquires the lock, spinning until it is available.
    ///
    /// Returns a [`SpinGuard`] that releases the lock when dropped.
    pub fn lock(&self) -> SpinGuard<'_, T> {
        while self
            .locked
            .compare_exchange(false, true, Ordering::Acquire, Ordering::Relaxed)
            .is_err()
        {
            hint::spin_loop();
        }
        SpinGuard { mutex: self }
    }
}

/// RAII guard that releases the spinlock on drop.
pub struct SpinGuard<'a, T> {
    mutex: &'a SpinMutex<T>,
}

impl<T> Deref for SpinGuard<'_, T> {
    type Target = T;
    fn deref(&self) -> &T {
        // SAFETY: We hold the lock exclusively.
        unsafe { &*self.mutex.data.get() }
    }
}

impl<T> DerefMut for SpinGuard<'_, T> {
    fn deref_mut(&mut self) -> &mut T {
        // SAFETY: We hold the lock exclusively.
        unsafe { &mut *self.mutex.data.get() }
    }
}

impl<T> Drop for SpinGuard<'_, T> {
    fn drop(&mut self) {
        self.mutex.locked.store(false, Ordering::Release);
    }
}

/// Atomic types and memory orderings.
pub mod atomic {
    pub use core::sync::atomic::*;
}
