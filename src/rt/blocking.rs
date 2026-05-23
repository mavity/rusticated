// src/rt/blocking.rs

use crate::boxed::Box;
use crate::collections::VecDeque;
use crate::future::Future;
use crate::io;
use crate::pin::Pin;
use crate::sync::Arc;
use crate::task::{Context, Poll, Waker};
use spin::Mutex;

#[cfg(windows)]
#[link(name = "kernel32", kind = "raw-dylib")]
unsafe extern "system" {
    fn CreateThread(
        lpThreadAttributes: *mut core::ffi::c_void,
        dwStackSize: usize,
        lpStartAddress: Option<unsafe extern "system" fn(*mut core::ffi::c_void) -> u32>,
        lpParameter: *mut core::ffi::c_void,
        dwCreationFlags: u32,
        lpThreadId: *mut u32,
    ) -> usize;
    fn CloseHandle(hObject: usize) -> i32;
    fn InitializeConditionVariable(cv: *mut CONDITION_VARIABLE);
    fn WakeConditionVariable(cv: *mut CONDITION_VARIABLE);
    fn SleepConditionVariableSRW(
        cv: *mut CONDITION_VARIABLE,
        lock: *mut SRWLOCK,
        timeout: u32,
        flags: u32,
    ) -> i32;
    fn InitializeSRWLock(lock: *mut SRWLOCK);
    fn AcquireSRWLockExclusive(lock: *mut SRWLOCK);
    fn ReleaseSRWLockExclusive(lock: *mut SRWLOCK);
    fn GetTickCount64() -> u64;
}

#[cfg(windows)]
#[repr(C)]
struct CONDITION_VARIABLE(usize);
#[cfg(windows)]
#[repr(C)]
struct SRWLOCK(usize);

#[cfg(windows)]
unsafe impl Send for CONDITION_VARIABLE {}
#[cfg(windows)]
unsafe impl Sync for CONDITION_VARIABLE {}
#[cfg(windows)]
unsafe impl Send for SRWLOCK {}
#[cfg(windows)]
unsafe impl Sync for SRWLOCK {}

pub struct ThreadPool {
    inner: Arc<PoolInner>,
}

struct PoolInner {
    state: Mutex<PoolState>,
    #[cfg(windows)]
    cv: CONDITION_VARIABLE,
    #[cfg(windows)]
    srw: SRWLOCK,
    max_cores: usize,
}

unsafe impl Send for PoolInner {}
unsafe impl Sync for PoolInner {}

struct PoolState {
    tasks: VecDeque<Box<dyn FnOnce() + Send>>,
    total_threads: usize,
    busy_threads: usize,
    next_spawn_delay_ms: u64,
    last_spawn_attempt_ms: u64,
}

impl ThreadPool {
    pub fn new() -> Self {
        #[cfg(windows)]
        let mut cv = CONDITION_VARIABLE(0);
        #[cfg(windows)]
        let mut srw = SRWLOCK(0);
        
        #[cfg(windows)]
        unsafe {
            InitializeConditionVariable(&mut cv);
            InitializeSRWLock(&mut srw);
        }

        Self {
            inner: Arc::new(PoolInner {
                state: Mutex::new(PoolState {
                    tasks: VecDeque::new(),
                    total_threads: 0,
                    busy_threads: 0,
                    next_spawn_delay_ms: 10,
                    last_spawn_attempt_ms: 0,
                }),
                #[cfg(windows)]
                cv,
                #[cfg(windows)]
                srw,
                max_cores: 4, // Default
            }),
        }
    }

    pub fn spawn<F>(&self, f: F)
    where
        F: FnOnce() + Send + 'static,
    {
        let mut state = self.inner.state.lock();
        state.tasks.push_back(Box::new(f));

        if state.total_threads < self.inner.max_cores {
            self.spawn_thread(&mut state);
        } else if state.busy_threads == state.total_threads {
            #[cfg(windows)]
            {
                let now = unsafe { GetTickCount64() };
                if state.last_spawn_attempt_ms == 0 || now >= state.last_spawn_attempt_ms + state.next_spawn_delay_ms {
                    state.last_spawn_attempt_ms = now;
                    state.next_spawn_delay_ms = (state.next_spawn_delay_ms * 2).min(5000);
                    self.spawn_thread(&mut state);
                }
            }
        }
        
        #[cfg(windows)]
        unsafe { WakeConditionVariable(&self.inner.cv as *const _ as *mut _) };
    }

    fn spawn_thread(&self, state: &mut PoolState) {
        state.total_threads += 1;
        let inner = Arc::clone(&self.inner);
        
        #[cfg(windows)]
        unsafe {
            let param = Arc::into_raw(inner) as *mut core::ffi::c_void;
            let h = CreateThread(core::ptr::null_mut(), 0, Some(worker_bridge), param, 0, core::ptr::null_mut());
            if h != 0 {
                CloseHandle(h);
            }
        }
    }
}

#[cfg(windows)]
unsafe extern "system" fn worker_bridge(param: *mut core::ffi::c_void) -> u32 {
    let inner = unsafe { Arc::from_raw(param as *const PoolInner) };
    loop {
        let task = {
            loop {
                // Try to get a task under the spinlock
                let mut state = inner.state.lock();
                if let Some(t) = state.tasks.pop_front() {
                    state.busy_threads += 1;
                    break t;
                }
                drop(state); // Must not hold state lock during CV sleep

                // Sleep on CV using the native SRWLock to avoid the busy-loop.
                // We use Acquire/Release on the native SRWLock to satisfy the API.
                unsafe {
                    let cv_ptr = &inner.cv as *const _ as *mut CONDITION_VARIABLE;
                    let srw_ptr = &inner.srw as *const _ as *mut SRWLOCK;
                    
                    AcquireSRWLockExclusive(srw_ptr);
                    SleepConditionVariableSRW(cv_ptr, srw_ptr, 10000, 0);
                    ReleaseSRWLockExclusive(srw_ptr);
                }
                
                // After waking, check if we should liquidate due to timeout
                let mut state = inner.state.lock();
                if state.tasks.is_empty() {
                    state.total_threads -= 1;
                    if state.total_threads == 0 {
                        state.next_spawn_delay_ms = 10;
                        state.last_spawn_attempt_ms = 0;
                    }
                    return 0;
                }
                // If not liquidating, loop back to try and pop a task
            }
        };

        task();

        let mut state = inner.state.lock();
        state.busy_threads -= 1;
    }
}

static GLOBAL_POOL: crate::sync::OnceLock<ThreadPool> = crate::sync::OnceLock::new();

pub fn pool() -> &'static ThreadPool {
    GLOBAL_POOL.get_or_init(|| ThreadPool::new())
}

pub struct BlockingOpState<T> {
    pub result: Mutex<Option<io::Result<T>>>,
    pub waker: Mutex<Option<Waker>>,
}

pub struct BlockingOpFuture<T> {
    state: Arc<BlockingOpState<T>>,
}

impl<T: Send + 'static> BlockingOpFuture<T> {
    pub fn new<F>(f: F) -> Self
    where
        F: FnOnce() -> io::Result<T> + Send + 'static,
    {
        let state = Arc::new(BlockingOpState {
            result: Mutex::new(None),
            waker: Mutex::new(None),
        });
        let state_clone = Arc::clone(&state);
        
        #[cfg(windows)]
        super::windows::outstanding_io().set(super::windows::outstanding_io().get() + 1);

        pool().spawn(move || {
            let res = f();
            let mut s = state_clone.result.lock();
            *s = Some(res);
            
            #[cfg(windows)]
            super::windows::outstanding_io().set(super::windows::outstanding_io().get() - 1);

            if let Some(w) = state_clone.waker.lock().take() {
                w.wake();
            }
        });
        
        Self { state }
    }
}

impl<T: Send + 'static> Future for BlockingOpFuture<T> {
    type Output = io::Result<T>;

    fn poll(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        let mut res = self.state.result.lock();
        if let Some(val) = res.take() {
            Poll::Ready(val)
        } else {
            *self.state.waker.lock() = Some(cx.waker().clone());
            Poll::Pending
        }
    }
}
