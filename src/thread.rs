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
    #[cfg(all(not(windows), not(target_family = "wasm")))]
    {
        const CLONE_VM: usize = 0x00000100;
        const CLONE_FS: usize = 0x00000200;
        const CLONE_FILES: usize = 0x00000400;
        const CLONE_SIGHAND: usize = 0x00000800;
        const CLONE_THREAD: usize = 0x00010000;
        const CLONE_SYSVSEM: usize = 0x00040000;
        const CLONE_IO: usize = 0x80000000;
        const STACK_SIZE: usize = 256 * 1024;

        let mut stack: alloc::vec::Vec<u8> = alloc::vec::Vec::with_capacity(STACK_SIZE);
        unsafe { stack.set_len(STACK_SIZE) };
        let stack_top = unsafe { stack.as_mut_ptr().add(STACK_SIZE) };
        let _stack_leak = alloc::boxed::Box::leak(stack.into_boxed_slice());

        let flags = CLONE_VM
            | CLONE_FS
            | CLONE_FILES
            | CLONE_SIGHAND
            | CLONE_THREAD
            | CLONE_SYSVSEM
            | CLONE_IO;

        let result = crate::syscall!(
            crate::os::linux::syscall::nr::CLONE,
            flags,
            stack_top as usize,
            0usize,
            0usize,
            0usize
        ) as isize;

        if result < 0 {
            let err = -result as i32;
            report_clone_error(err);
        }

        if result == 0 {
            let boxed: alloc::boxed::Box<ThreadBox> =
                unsafe { alloc::boxed::Box::from_raw(arg as *mut ThreadBox) };
            (*boxed)();
            crate::syscall!(crate::os::linux::syscall::nr::EXIT, 0usize);
        }
    }

    JoinHandle {
        _phantom: PhantomData,
    }
}

#[cfg(all(not(windows), not(target_family = "wasm")))]
fn report_clone_error(code: i32) {
    let mut buffer = [0u8; 128];
    let mut len = 0;
    let prefix = b"clone failed: ";
    buffer[..prefix.len()].copy_from_slice(prefix);
    len += prefix.len();

    let mut num = code;
    if num == 0 {
        buffer[len] = b'0';
        len += 1;
    } else {
        if num < 0 {
            buffer[len] = b'-';
            len += 1;
            num = -num;
        }
        let start = len;
        while num > 0 {
            buffer[len] = b'0' + (num % 10) as u8;
            num /= 10;
            len += 1;
        }
        buffer[start..len].reverse();
    }
    buffer[len] = b'\n';
    len += 1;

    crate::syscall!(
        crate::os::linux::syscall::nr::WRITE,
        2usize,
        buffer.as_ptr() as usize,
        len
    );
    crate::syscall!(crate::os::linux::syscall::nr::EXIT, 1usize);
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
        struct Timespec {
            tv_sec: i64,
            tv_nsec: i64,
        }
        unsafe extern "C" {
            fn nanosleep(req: *const Timespec, rem: *mut Timespec) -> i32;
        }
        let ts = Timespec {
            tv_sec: (ms / 1000) as i64,
            tv_nsec: ((ms % 1000) * 1_000_000) as i64,
        };
        unsafe {
            nanosleep(&ts, core::ptr::null_mut());
        }
    }
}

/// Yield the current thread's time slice.
#[cfg(not(target_family = "wasm"))]
pub fn yield_now() {
    #[cfg(windows)]
    {
        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" {
            fn SwitchToThread() -> i32;
        }
        unsafe {
            SwitchToThread();
        }
    }
    #[cfg(not(windows))]
    {
        unsafe extern "C" {
            fn sched_yield() -> i32;
        }
        unsafe {
            sched_yield();
        }
    }
}
