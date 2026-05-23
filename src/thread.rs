//! Thread-local storage key type and OS thread spawning.

use core::marker::PhantomData;

/// A key for accessing thread-local storage, analogous to [`std::thread::LocalKey`].
///
/// Instances are produced exclusively by the [`thread_local!`] macro and must be
/// stored in `static` items.
pub struct LocalKey<T: 'static> {
    inner: fn() -> *const T,
}

impl<T: 'static> LocalKey<T> {
    /// Constructs a new `LocalKey` from a getter function.
    ///
    /// This is an implementation detail used by the [`thread_local!`] macro; do
    /// not call it directly.
    #[doc(hidden)]
    pub const fn new(inner: fn() -> *const T) -> Self {
        Self { inner }
    }

    /// Acquires a reference to the thread-local value, initialising it on first
    /// access, then passes it to `f`.
    pub fn with<R>(&'static self, f: impl FnOnce(&T) -> R) -> R {
        // SAFETY: `inner` was supplied by the `thread_local!` macro and returns a
        // pointer into a `#[thread_local]` static.  The pointer is valid for the
        // entire lifetime of the calling thread, and `f` cannot outlive this call.
        f(unsafe { &*(self.inner)() })
    }
}

/// Handle to a spawned OS thread. Dropping it detaches the thread.
pub struct JoinHandle<T> {
    _phantom: PhantomData<T>,
}

impl<T> JoinHandle<T> {
    /// Wait for the thread to finish. Currently unimplemented — returns an error.
    pub fn join(self) -> Result<T, alloc::boxed::Box<dyn core::any::Any + Send + 'static>> {
        Err(alloc::boxed::Box::new("thread join not implemented"))
    }
}

#[cfg(not(target_family = "wasm"))]
type ThreadBox = alloc::boxed::Box<dyn FnOnce() + Send + 'static>;

/// Trampoline for OS thread entry on Linux/Unix (pthread_create).
#[cfg(all(not(target_family = "wasm"), not(windows)))]
unsafe extern "C" fn thread_trampoline_unix(arg: *mut core::ffi::c_void) -> *mut core::ffi::c_void {
    let boxed: alloc::boxed::Box<ThreadBox> =
        unsafe { alloc::boxed::Box::from_raw(arg as *mut ThreadBox) };
    (*boxed)();
    core::ptr::null_mut()
}

/// Trampoline for OS thread entry on Windows (CreateThread).
#[cfg(windows)]
unsafe extern "system" fn thread_trampoline_win(arg: *mut core::ffi::c_void) -> u32 {
    let boxed: alloc::boxed::Box<ThreadBox> =
        unsafe { alloc::boxed::Box::from_raw(arg as *mut ThreadBox) };
    (*boxed)();
    0
}

/// Spawn a new OS thread that runs `f`.
#[cfg(not(target_family = "wasm"))]
pub fn spawn<F: FnOnce() + Send + 'static>(f: F) -> JoinHandle<()> {
    let boxed: alloc::boxed::Box<ThreadBox> = alloc::boxed::Box::new(alloc::boxed::Box::new(f));
    let arg = alloc::boxed::Box::into_raw(boxed) as *mut core::ffi::c_void;

    #[cfg(windows)]
    {
        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" {
            fn CreateThread(
                lp_thread_attributes: *mut core::ffi::c_void,
                dw_stack_size: usize,
                lp_start_address: Option<unsafe extern "system" fn(*mut core::ffi::c_void) -> u32>,
                lp_parameter: *mut core::ffi::c_void,
                dw_creation_flags: u32,
                lp_thread_id: *mut u32,
            ) -> usize;
        }
        let mut thread_id = 0u32;
        unsafe {
            CreateThread(
                core::ptr::null_mut(),
                0,
                Some(thread_trampoline_win),
                arg,
                0,
                &mut thread_id,
            );
        }
    }
    #[cfg(not(windows))]
    {
        unsafe extern "C" {
            fn pthread_create(
                thread: *mut usize,
                attr: *const core::ffi::c_void,
                start_routine: unsafe extern "C" fn(*mut core::ffi::c_void) -> *mut core::ffi::c_void,
                arg: *mut core::ffi::c_void,
            ) -> i32;
        }
        let mut thread_id: usize = 0;
        unsafe {
            pthread_create(
                &mut thread_id,
                core::ptr::null(),
                thread_trampoline_unix,
                arg,
            );
        }
    }

    JoinHandle { _phantom: PhantomData }
}

/// Sleep the current thread for approximately `ms` milliseconds.
#[cfg(not(target_family = "wasm"))]
pub(crate) fn sleep_ms(ms: u64) {
    #[cfg(windows)]
    {
        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" {
            fn Sleep(dw_milliseconds: u32);
        }
        unsafe { Sleep(ms as u32) };
    }
    #[cfg(not(windows))]
    {
        #[repr(C)]
        struct Timespec { tv_sec: i64, tv_nsec: i64 }
        unsafe extern "C" {
            fn nanosleep(req: *const Timespec, rem: *mut Timespec) -> i32;
        }
        let ts = Timespec { tv_sec: (ms / 1000) as i64, tv_nsec: ((ms % 1000) * 1_000_000) as i64 };
        unsafe { nanosleep(&ts, core::ptr::null_mut()); }
    }
}

/// Yield the current thread's time slice.
#[cfg(not(target_family = "wasm"))]
pub(crate) fn yield_now() {
    #[cfg(windows)]
    {
        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" { fn SwitchToThread() -> i32; }
        unsafe { SwitchToThread(); }
    }
    #[cfg(not(windows))]
    {
        unsafe extern "C" { fn sched_yield() -> i32; }
        unsafe { sched_yield(); }
    }
}
