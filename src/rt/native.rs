//! Minimal single-threaded native executor backed by `compio-driver`.
//!
//! # Design
//!
//! A thread-local [`Proactor`] drives all I/O completions.  [`block_on`] runs
//! a single future to completion by alternating between polling the future and
//! waiting for the next batch of I/O events.  Individual operations are
//! submitted and awaited through [`OpFuture`].

use std::{
    cell::RefCell,
    future::Future,
    io,
    pin::pin,
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    task::{Context, Poll, RawWaker, RawWakerVTable, Waker},
};

use compio_buf::BufResult;
use compio_driver::{Key, OpCode, Proactor, PushEntry};

thread_local! {
    /// Thread-local proactor instance for the current executor thread.
    static PROACTOR: RefCell<io::Result<Proactor>> = RefCell::new(Proactor::new());
}

/// Run `f` with a mutable reference to the thread-local proactor.
///
/// Returns [`Err`] if the proactor failed to initialise.
pub(crate) fn with_proactor<R>(f: impl FnOnce(&mut Proactor) -> R) -> io::Result<R> {
    PROACTOR.with(|cell| {
        let mut borrow = cell.borrow_mut();
        match &mut *borrow {
            Ok(p) => Ok(f(p)),
            Err(e) => Err(io::Error::new(e.kind(), e.to_string())),
        }
    })
}

/// Run a future to completion on the current thread.
///
/// The thread-local [`Proactor`] is polled whenever the future returns
/// [`Poll::Pending`], blocking until at least one I/O event arrives.
///
/// # Errors
///
/// Returns [`Err`] only if the thread-local proactor failed to initialise or
/// encountered an unrecoverable driver error.
pub fn block_on<F: Future>(f: F) -> io::Result<F::Output> {
    let flag = Arc::new(AtomicBool::new(true));
    let waker = flag_waker(Arc::clone(&flag));
    let mut cx = Context::from_waker(&waker);
    let mut f = pin!(f);

    loop {
        // Only poll the future when the flag has been set (initially true, or
        // set by a waker call after an I/O completion).
        if flag.swap(false, Ordering::AcqRel) {
            if let Poll::Ready(output) = f.as_mut().poll(&mut cx) {
                return Ok(output);
            }
        }
        // Block until at least one I/O event completes.  The proactor calls
        // our waker, which stores `true` into `flag`.
        // `poll()` returns `io::Result<()>`; `with_proactor` wraps it in
        // another `io::Result`, so we need `??` to propagate both errors.
        with_proactor(|p| p.poll(None))??;
        // Ensure we re-enter the poll branch even if the waker fired before
        // we called `poll()`.
        flag.store(true, Ordering::Release);
    }
}

// ——— Flag-based waker ———————————————————————————————————————————————————————

fn flag_waker(flag: Arc<AtomicBool>) -> Waker {
    // SAFETY: `into_raw_waker` and VTABLE satisfy all RawWaker invariants.
    unsafe { Waker::from_raw(into_raw_waker(flag)) }
}

fn into_raw_waker(flag: Arc<AtomicBool>) -> RawWaker {
    RawWaker::new(Arc::into_raw(flag).cast::<()>(), &VTABLE)
}

static VTABLE: RawWakerVTable = RawWakerVTable::new(
    // clone — increment refcount, return a new RawWaker.
    |ptr| {
        // SAFETY: `ptr` was produced by `Arc::into_raw` for `Arc<AtomicBool>`.
        let arc = unsafe { Arc::from_raw(ptr.cast::<AtomicBool>()) };
        let clone = Arc::clone(&arc);
        std::mem::forget(arc); // keep the original alive
        into_raw_waker(clone)
    },
    // wake — consume the Arc and set the flag.
    |ptr| {
        // SAFETY: `ptr` was produced by `Arc::into_raw`; this call takes
        // ownership of it.
        let arc = unsafe { Arc::from_raw(ptr.cast::<AtomicBool>()) };
        arc.store(true, Ordering::Release);
    },
    // wake_by_ref — set the flag without consuming the Arc.
    |ptr| {
        // SAFETY: `ptr` is a valid, live `Arc<AtomicBool>` borrow.
        unsafe { &*ptr.cast::<AtomicBool>() }.store(true, Ordering::Release);
    },
    // drop — decrement refcount.
    |ptr| {
        // SAFETY: `ptr` was produced by `Arc::into_raw`.
        drop(unsafe { Arc::from_raw(ptr.cast::<AtomicBool>()) });
    },
);

// ——— OpFuture ————————————————————————————————————————————————————————————————

/// Internal state of an [`OpFuture`].
enum OpState<T: OpCode> {
    /// The operation has not been submitted yet.
    Idle(T),
    /// The operation has been submitted and we are waiting for completion.
    Submitted(Key<T>),
    /// The operation has completed (or errored).  Polling again is an error.
    Done,
}

/// A future that drives a single [`OpCode`] to completion via the proactor.
///
/// Submits the operation on first poll, then repeatedly calls
/// [`Proactor::pop`] until the driver signals completion.
pub(crate) struct OpFuture<T: OpCode + 'static> {
    state: OpState<T>,
}

impl<T: OpCode + 'static> OpFuture<T> {
    /// Wrap `op` in a new future.
    pub(crate) const fn new(op: T) -> Self {
        Self {
            state: OpState::Idle(op),
        }
    }
}

impl<T: OpCode + 'static + Unpin> Future for OpFuture<T> {
    type Output = io::Result<BufResult<usize, T>>;

    fn poll(self: std::pin::Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        let this = self.get_mut();
        match std::mem::replace(&mut this.state, OpState::Done) {
            OpState::Idle(op) => {
                // `push` returns `PushEntry` directly (no `io::Result`).
                // The closure inside `with_proactor` handles the proactor
                // init error via the outer `io::Result`.
                match with_proactor(|p| p.push(op)) {
                    Err(e) => Poll::Ready(Err(e)),
                    Ok(PushEntry::Ready(buf_result)) => {
                        // Completed immediately (e.g. cached or blocking op).
                        Poll::Ready(Ok(buf_result))
                    }
                    Ok(PushEntry::Pending(key)) => {
                        // Register waker so the proactor can wake us on
                        // completion, then park.
                        let _ = with_proactor(|p| p.update_waker(&key, cx.waker()));
                        this.state = OpState::Submitted(key);
                        Poll::Pending
                    }
                }
            }
            OpState::Submitted(key) => {
                // Combine pop + waker update in a single proactor borrow to
                // avoid two `RefCell::borrow_mut` calls.
                match with_proactor(|p| {
                    let entry = p.pop(key);
                    if let PushEntry::Pending(ref k) = entry {
                        p.update_waker(k, cx.waker());
                    }
                    entry
                }) {
                    Err(e) => Poll::Ready(Err(e)),
                    Ok(PushEntry::Ready(buf_result)) => Poll::Ready(Ok(buf_result)),
                    Ok(PushEntry::Pending(key)) => {
                        this.state = OpState::Submitted(key);
                        Poll::Pending
                    }
                }
            }
            OpState::Done => Poll::Ready(Err(io::Error::other(
                "OpFuture polled after completion",
            ))),
        }
    }
}
