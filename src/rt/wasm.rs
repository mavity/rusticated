//! Wasm backend implementation

use std::cell::{OnceCell, RefCell};
use std::future::Future;
use std::pin::Pin;
use std::task::{Context, Poll, Waker};
use crate::abi::Overlapped;
use crate::abi::imports;

#[cfg(target_family = "wasm")]
#[unsafe(no_mangle)]
extern "Rust" fn __getrandom_v03_custom(dest: *mut u8, len: usize) -> Result<(), getrandom::Error> {
    unsafe {
        imports::get_random(dest, len as u32);
    }
    Ok(())
}

thread_local! {
    static COMPLETION_REGISTRY: RefCell<Vec<(*mut Overlapped, Waker)>> = RefCell::new(Vec::new());
    static MAIN_FUTURE: RefCell<Option<Pin<Box<dyn Future<Output = ()>>>>> = const { RefCell::new(None) };
    static INITIALIZED: OnceCell<()> = const { OnceCell::new() };
}

/// Registers an overlapped I/O pointer and its associated waker
pub fn register_overlapped(overlapped: *mut Overlapped, waker: Waker) {
    COMPLETION_REGISTRY.with(|registry| {
        registry.borrow_mut().push((overlapped, waker));
    });
}

/// The internal reactive step that harvests completions.
fn tick() {
    COMPLETION_REGISTRY.with(|registry| {
        let mut reg = registry.borrow_mut();
        let mut i = 0;
        while i < reg.len() {
            let (overlapped, _) = reg[i];
            // Safety: The host is expected to update the memory backing this pointer.
            // The guest logic guarantees the pointer remains valid while registered.
            let is_complete = unsafe { (*overlapped).is_complete() };
            if is_complete {
                let (_, waker) = reg.remove(i);
                waker.wake();
            } else {
                i += 1;
            }
        }
    });

    // After harvesting, try to make progress on the main future
    MAIN_FUTURE.with(|main_fut| {
        if let Some(fut) = main_fut.borrow_mut().as_mut() {
            let waker = dummy_waker();
            let mut cx = Context::from_waker(&waker);
            let _ = fut.as_mut().poll(&mut cx);
        }
    });
}

fn dummy_waker() -> Waker {
    use std::task::{RawWaker, RawWakerVTable};
    unsafe fn clone(_: *const ()) -> RawWaker { dummy_raw_waker() }
    unsafe fn wake(_: *const ()) {}
    unsafe fn wake_by_ref(_: *const ()) {}
    unsafe fn drop(_: *const ()) {}
    static VTABLE: RawWakerVTable = RawWakerVTable::new(clone, wake, wake_by_ref, drop);
    fn dummy_raw_waker() -> RawWaker { RawWaker::new(std::ptr::null(), &VTABLE) }
    unsafe { Waker::from_raw(dummy_raw_waker()) }
}

/// A future that waits for an Overlapped operation to complete.
pub struct OverlappedFuture {
    overlapped: Box<Overlapped>,
    started: bool,
    op: Option<Box<dyn FnOnce(*mut Overlapped)>>,
}

impl OverlappedFuture {
    /// Create a new future for an overlapped operation.
    pub fn new<F>(op: F) -> Self
    where
        F: FnOnce(*mut Overlapped) + 'static,
    {
        Self {
            overlapped: Box::new(Overlapped::default()),
            started: false,
            op: Some(Box::new(op)),
        }
    }
}

impl Future for OverlappedFuture {
    type Output = (u32, u64, u64); // (error, result_ext, continued)

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        if !self.started {
            let op = self.op.take().expect("op taken");
            let ptr: *mut Overlapped = &mut *self.overlapped;
            op(ptr);
            self.started = true;
            
            // Check for synchronous completion
            if self.overlapped.is_complete() {
                return Poll::Ready((self.overlapped.error, self.overlapped.result_ext, self.overlapped.continued));
            }
            
            // Otherwise register for later
            register_overlapped(ptr, cx.waker().clone());
            return Poll::Pending;
        }

        if self.overlapped.is_complete() {
            Poll::Ready((self.overlapped.error, self.overlapped.result_ext, self.overlapped.continued))
        } else {
            Poll::Pending
        }
    }
}

unsafe extern "Rust" {
    fn guest_init();
}

/// Run
#[unsafe(no_mangle)]
pub extern "C" fn run() {
    INITIALIZED.with(|init| {
        if init.get().is_none() {
            // Safety: The guest (brush-shell) is required to define this symbol
            // if it wants to use the reactive runner.
            unsafe { guest_init() };
            let _ = init.set(());
        }
    });

    tick();
}


