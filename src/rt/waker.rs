use crate::task::{RawWaker, RawWakerVTable, Waker};
use alloc::sync::Arc;
use core::sync::atomic::{AtomicBool, Ordering};

static TASK_WAKER_VTABLE: RawWakerVTable = RawWakerVTable::new(
    // clone: increment Arc refcount
    |ptr| {
        // SAFETY: ptr was produced by Arc::into_raw and is still live.
        let arc = unsafe { Arc::from_raw(ptr as *const AtomicBool) };
        let cloned = Arc::clone(&arc);
        core::mem::forget(arc);
        RawWaker::new(Arc::into_raw(cloned) as *const (), &TASK_WAKER_VTABLE)
    },
    // wake: set flag, consume ownership (decrement refcount on drop)
    |ptr| {
        // SAFETY: consuming the Arc that was stored in the RawWaker.
        let arc = unsafe { Arc::from_raw(ptr as *const AtomicBool) };
        arc.store(true, Ordering::Release);
        // arc drops here → refcount decremented
    },
    // wake_by_ref: set flag, borrow without consuming
    |ptr| {
        // SAFETY: ptr is a live Arc; we do not take ownership.
        let arc = unsafe { Arc::from_raw(ptr as *const AtomicBool) };
        arc.store(true, Ordering::Release);
        core::mem::forget(arc);
    },
    // drop: decrement refcount
    |ptr| {
        // SAFETY: ptr was produced by Arc::into_raw.
        drop(unsafe { Arc::from_raw(ptr as *const AtomicBool) });
    },
);

/// Create a [`Waker`] backed by an [`Arc<AtomicBool>`] flag.
///
/// When woken, sets the flag to `true`. The executor checks the flag on
/// each [`poll_step`] to decide which tasks to poll, and resets it before
/// polling so that any subsequent wake during the poll is not lost.
pub(crate) fn task_waker(flag: Arc<AtomicBool>) -> Waker {
    let ptr = Arc::into_raw(flag) as *const ();
    // SAFETY: vtable correctly manages Arc refcount; ptr is non-null.
    unsafe { Waker::from_raw(RawWaker::new(ptr, &TASK_WAKER_VTABLE)) }
}

/// A no-op waker for contexts where notification is handled out-of-band.
pub(crate) fn noop_waker() -> Waker {
    static VTABLE: RawWakerVTable = RawWakerVTable::new(
        |_| RawWaker::new(core::ptr::null(), &VTABLE),
        |_| {},
        |_| {},
        |_| {},
    );
    // SAFETY: vtable functions are all no-ops; the data pointer is never
    // dereferenced.
    unsafe { Waker::from_raw(RawWaker::new(core::ptr::null(), &VTABLE)) }
}
