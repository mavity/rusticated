//! WASM backend: host-driven proactor with ownership-preserving completion
//! registry.
//!
//! Ownership invariants
//! --------------------
//!
//! Every in-flight overlapped operation has its state ([`OpState`]) owned by an
//! [`Rc`]. The registry holds one clone; the future awaiting completion holds
//! another. When the future is dropped before completion, its clone goes away
//! but the registry's clone keeps the [`Overlapped`] struct — and any
//! associated buffer — alive at a stable address. The host may therefore
//! continue to write into that memory safely. When [`tick()`] later observes
//! the completion flag, the registry drops its clone; if the future is also
//! gone, the allocation is freed cleanly with no use-after-free.

use crate::abi::Overlapped;
use crate::abi::imports;
use crate::boxed::Box;
use crate::cell::{OnceCell, RefCell, UnsafeCell};
use crate::future::Future;
use crate::pin::Pin;
use crate::rc::Rc;
use crate::task::{Context, Poll, Waker};
use crate::vec::Vec;

type MainFuture = Pin<Box<dyn Future<Output = ()>>>;

#[unsafe(no_mangle)]
extern "Rust" fn __getrandom_v03_custom(dest: *mut u8, len: usize) -> Result<(), getrandom::Error> {
    // SAFETY: `dest`/`len` come from getrandom which validates the slice is
    // writable for `len` bytes.
    unsafe {
        imports::get_random(dest, len as u32);
    }
    Ok(())
}

// ─── Op state ────────────────────────────────────────────────────────────────

/// Shared, pinned state for one in-flight overlapped operation.
///
/// The host receives `*mut Overlapped` derived from `overlapped.get()`. As
/// long as at least one [`Rc`] clone of the enclosing [`OpState`] exists, the
/// pointer is valid.
struct OpState {
    overlapped: UnsafeCell<Overlapped>,
    /// Optional buffer pinned for the duration of the operation. Held here so
    /// the buffer outlives the future that originated it.
    buffer: UnsafeCell<Option<Vec<u8>>>,
}

impl Drop for OpState {
    fn drop(&mut self) {
        // If the operation is not complete, we must tell the host to stop
        // using this pointer before we free it.
        if !self.is_complete() {
            unsafe { imports::cancel(self.overlapped.get()) };
        }
    }
}

impl OpState {
    fn new() -> Rc<Self> {
        Rc::new(Self {
            overlapped: UnsafeCell::new(Overlapped::default()),
            buffer: UnsafeCell::new(None),
        })
    }

    fn with_buffer(buf: Vec<u8>) -> Rc<Self> {
        Rc::new(Self {
            overlapped: UnsafeCell::new(Overlapped::default()),
            buffer: UnsafeCell::new(Some(buf)),
        })
    }

    fn overlapped_ptr(&self) -> *mut Overlapped {
        self.overlapped.get()
    }

    fn is_complete(&self) -> bool {
        // SAFETY: host writes the `flags` field via the same address; reading
        // it once per `tick` is sound on the single-threaded WASM target.
        unsafe { (*self.overlapped.get()).is_complete() }
    }

    fn snapshot(&self) -> (u32, u64, u64) {
        // SAFETY: completion has been signalled; the host no longer writes.
        let ov = unsafe { &*self.overlapped.get() };
        (ov.error, ov.result_ext, ov.continued)
    }

    fn take_buffer(&self) -> Option<Vec<u8>> {
        // SAFETY: completion has been signalled; no aliasing with host.
        unsafe { (*self.buffer.get()).take() }
    }

    fn buffer_ptr_len(&self) -> Option<(*mut u8, u32)> {
        // SAFETY: only called from the originating future before submission.
        unsafe {
            let opt: &mut Option<Vec<u8>> = &mut *self.buffer.get();
            opt.as_mut().map(|v| (v.as_mut_ptr(), v.capacity() as u32))
        }
    }
}

// ─── Registry & runtime state ────────────────────────────────────────────────

#[repr(align(8))]
struct CompletionRegistryStorage(UnsafeCell<Option<RefCell<Vec<(Rc<OpState>, Waker)>>>>);

unsafe impl Sync for CompletionRegistryStorage {}

#[repr(align(8))]
struct MainFutureStorage(UnsafeCell<Option<RefCell<Option<MainFuture>>>>);

unsafe impl Sync for MainFutureStorage {}

#[repr(align(8))]
struct InitializedStorage(UnsafeCell<OnceCell<()>>);

unsafe impl Sync for InitializedStorage {}

static COMPLETION_REGISTRY_STORAGE: CompletionRegistryStorage =
    CompletionRegistryStorage(UnsafeCell::new(None));
static MAIN_FUTURE_STORAGE: MainFutureStorage = MainFutureStorage(UnsafeCell::new(None));
static INITIALIZED_STORAGE: InitializedStorage =
    InitializedStorage(UnsafeCell::new(OnceCell::new()));

fn completion_registry() -> &'static RefCell<Vec<(Rc<OpState>, Waker)>> {
    unsafe {
        let storage = &mut *COMPLETION_REGISTRY_STORAGE.0.get();
        if storage.is_none() {
            *storage = Some(RefCell::new(Vec::new()));
        }
        storage.as_ref().unwrap_unchecked()
    }
}

fn main_future() -> &'static RefCell<Option<Pin<Box<dyn Future<Output = ()>>>>> {
    unsafe {
        let storage = &mut *MAIN_FUTURE_STORAGE.0.get();
        if storage.is_none() {
            *storage = Some(RefCell::new(None));
        }
        storage.as_ref().unwrap_unchecked()
    }
}

fn initialized() -> &'static OnceCell<()> {
    unsafe { &*INITIALIZED_STORAGE.0.get() }
}

fn register(state: Rc<OpState>, waker: Waker) {
    completion_registry().borrow_mut().push((state, waker));
}

/// Submit the main future to the runtime.
///
/// Invoked by the guest's `guest_init` callback during the first [`run()`]
/// call.
pub fn submit_main<F>(future: F)
where
    F: Future<Output = ()> + 'static,
{
    *main_future().borrow_mut() = Some(Box::pin(future));
}

/// One iteration of the reactive loop:
///
/// 1. Walk the registry; wake any operation whose completion flag is set, then drop the registry's
///    own [`Rc`] clone for that entry.
/// 2. Poll the main future once so any newly-ready work can make progress.
fn tick() {
    {
        let mut reg = completion_registry().borrow_mut();
        let mut i = 0;
        while i < reg.len() {
            if reg[i].0.is_complete() {
                let (_state, waker) = reg.remove(i);
                waker.wake();
            } else {
                i += 1;
            }
        }
    }

    let mut borrow = main_future().borrow_mut();
    let done = if let Some(fut) = borrow.as_mut() {
        let waker = noop_waker();
        let mut cx = Context::from_waker(&waker);
        fut.as_mut().poll(&mut cx).is_ready()
    } else {
        false
    };
    if done {
        *borrow = None;
    }
}

fn noop_waker() -> Waker {
    use core::task::{RawWaker, RawWakerVTable};
    unsafe fn clone(_: *const ()) -> RawWaker {
        raw()
    }
    unsafe fn wake(_: *const ()) {}
    unsafe fn wake_by_ref(_: *const ()) {}
    unsafe fn drop_(_: *const ()) {}
    static VTABLE: RawWakerVTable = RawWakerVTable::new(clone, wake, wake_by_ref, drop_);
    fn raw() -> RawWaker {
        RawWaker::new(core::ptr::null(), &VTABLE)
    }
    // SAFETY: VTable functions match the contract — all no-ops, clone returns
    // an identical waker.
    unsafe { Waker::from_raw(raw()) }
}

// ─── OverlappedFuture (no buffer) ────────────────────────────────────────────

/// Future awaiting a host-driven overlapped operation.
///
/// The op closure is invoked on first poll with a `*mut Overlapped` whose
/// address remains valid until the host signals completion (or the program
/// terminates). Dropping this future before completion leaves the registry
/// holding the state until completion; no use-after-free can occur.
pub struct OverlappedFuture {
    state: Rc<OpState>,
    op: Option<Box<dyn FnOnce(*mut Overlapped)>>,
    started: bool,
}

impl OverlappedFuture {
    /// Build a future that will invoke `op` on first poll.
    pub fn new<F>(op: F) -> Self
    where
        F: FnOnce(*mut Overlapped) + 'static,
    {
        Self {
            state: OpState::new(),
            op: Some(Box::new(op)),
            started: false,
        }
    }
}

impl Future for OverlappedFuture {
    /// `(error, result_ext, continued)` as reported by the host.
    type Output = (u32, u64, u64);

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        if !self.started {
            self.started = true;
            if let Some(op) = self.op.take() {
                op(self.state.overlapped_ptr());
            }
            if self.state.is_complete() {
                return Poll::Ready(self.state.snapshot());
            }
            register(Rc::clone(&self.state), cx.waker().clone());
            return Poll::Pending;
        }
        if self.state.is_complete() {
            Poll::Ready(self.state.snapshot())
        } else {
            Poll::Pending
        }
    }
}

impl Drop for OverlappedFuture {
    fn drop(&mut self) {
        if self.started && !self.state.is_complete() {
            unsafe {
                imports::cancel(self.state.overlapped_ptr());
            }
        }
    }
}

// ─── OverlappedBufferFuture ──────────────────────────────────────────────────

/// Future awaiting a host-driven overlapped operation that owns a buffer for
/// the duration of the operation.
///
/// The buffer is moved into the future, kept alive by the [`Rc`]-shared state
/// while the operation is in flight, and returned to the caller alongside the
/// completion result. Dropping the future cancels the caller's claim to the
/// buffer but the registry retains it until completion, so the host's writes
/// remain sound.
pub struct OverlappedBufferFuture {
    state: Rc<OpState>,
    op: Option<Box<dyn FnOnce(*mut Overlapped, *mut u8, u32)>>,
    started: bool,
}

impl OverlappedBufferFuture {
    /// Build a future that owns `buffer` and invokes `op` on first poll with
    /// the overlapped pointer plus the buffer's `(ptr, capacity)`.
    pub fn new<F>(buffer: Vec<u8>, op: F) -> Self
    where
        F: FnOnce(*mut Overlapped, *mut u8, u32) + 'static,
    {
        Self {
            state: OpState::with_buffer(buffer),
            op: Some(Box::new(op)),
            started: false,
        }
    }
}

impl Future for OverlappedBufferFuture {
    /// `(error, result_ext, continued, buffer)` — the buffer is returned to
    /// the caller on completion.
    type Output = (u32, u64, u64, Vec<u8>);

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        if !self.started {
            self.started = true;
            if let Some(op) = self.op.take() {
                if let Some((ptr, len)) = self.state.buffer_ptr_len() {
                    op(self.state.overlapped_ptr(), ptr, len);
                }
            }
            if self.state.is_complete() {
                let (e, r, c) = self.state.snapshot();
                let buf = self.state.take_buffer().unwrap_or_default();
                return Poll::Ready((e, r, c, buf));
            }
            register(Rc::clone(&self.state), cx.waker().clone());
            return Poll::Pending;
        }
        if self.state.is_complete() {
            let (e, r, c) = self.state.snapshot();
            let buf = self.state.take_buffer().unwrap_or_default();
            Poll::Ready((e, r, c, buf))
        } else {
            Poll::Pending
        }
    }
}

impl Drop for OverlappedBufferFuture {
    fn drop(&mut self) {
        if self.started && !self.state.is_complete() {
            unsafe {
                imports::cancel(self.state.overlapped_ptr());
            }
        }
    }
}

// ─── Host entry points ───────────────────────────────────────────────────────

unsafe extern "Rust" {
    /// Guest-supplied initialiser. Called exactly once by the host on the
    /// first invocation of [`run()`]. The guest typically uses this to call
    /// [`submit_main`] with its top-level future.
    fn guest_init();
}

/// Host entry point. The host calls this once per event-loop iteration.
///
/// First call: invokes the guest's `guest_init` which is expected to register
/// the main future. Every call: harvests completed operations and polls the
/// main future once. The call returns immediately; the host is responsible for
/// scheduling subsequent ticks (typically: after any host-side completion is
/// signalled into shared memory, or on a host timer).
#[unsafe(no_mangle)]
pub extern "C" fn run() {
    if initialized().get().is_none() {
        // SAFETY: the guest is required to define `guest_init`; if it
        // does not, the program would have failed to link. Calling once
        // per process is enforced by the `OnceCell`.
        unsafe { guest_init() };
        let _ = initialized().set(());
    }

    tick();
}

/// Drive one iteration of the runtime without re-running `guest_init`.
///
/// Symmetrical with the native [`poll_step`] entry point.
///
/// [`poll_step`]: super::poll_step
pub fn poll_step() {
    tick();
}
