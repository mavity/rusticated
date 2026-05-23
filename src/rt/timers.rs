use crate::cell::{Cell, UnsafeCell};
use crate::task::Waker;
use crate::time::{Duration, Instant};
use crate::vec::Vec;

#[repr(align(16))]
struct TimersStorage(UnsafeCell<Option<Vec<(Instant, u64, Waker)>>>);

unsafe impl Sync for TimersStorage {}

#[repr(align(16))]
struct TimerIdStorage(UnsafeCell<Option<Cell<u64>>>);

unsafe impl Sync for TimerIdStorage {}

static TIMERS_STORAGE: TimersStorage = TimersStorage(UnsafeCell::new(None));
static NEXT_TIMER_ID_STORAGE: TimerIdStorage = TimerIdStorage(UnsafeCell::new(None));

fn timers_mut<R>(f: impl FnOnce(&mut Vec<(Instant, u64, Waker)>) -> R) -> R {
    unsafe {
        let storage = core::ptr::addr_of!(TIMERS_STORAGE).cast::<TimersStorage>();
        let storage = (*storage).0.get();
        if (*storage).is_none() {
            *storage = Some(Vec::new());
        }
        let timers = (*storage).as_mut().unwrap_unchecked();
        f(timers)
    }
}

fn next_timer_id<R>(f: impl FnOnce(&mut Cell<u64>) -> R) -> R {
    unsafe {
        let storage = core::ptr::addr_of!(NEXT_TIMER_ID_STORAGE).cast::<TimerIdStorage>();
        let storage = (*storage).0.get();
        if (*storage).is_none() {
            *storage = Some(Cell::new(1));
        }
        let cell = (*storage).as_mut().unwrap_unchecked();
        f(cell)
    }
}

pub(crate) fn register_timer(deadline: Instant, waker: Waker) -> u64 {
    let id = next_timer_id(|c| c.get());
    next_timer_id(|c| c.set(id.wrapping_add(1)));
    timers_mut(|t| {
        let pos = t.partition_point(|(d, _, _)| *d <= deadline);
        t.insert(pos, (deadline, id, waker));
    });
    id
}

pub(crate) fn cancel_timer(id: u64) {
    timers_mut(|t| {
        if let Some(pos) = t.iter().position(|(_, i, _)| *i == id) {
            t.remove(pos);
        }
    });
}

pub(crate) fn wake_expired() -> bool {
    let now = Instant::now();
    let mut woken = false;
    timers_mut(|t| {
        while let Some(&(d, _, _)) = t.first() {
            if d <= now {
                let (_, _, waker) = t.remove(0);
                waker.wake();
                woken = true;
            } else {
                break;
            }
        }
    });
    woken
}

pub(crate) fn has_timers() -> bool {
    timers_mut(|t| !t.is_empty())
}

pub(crate) fn next_deadline() -> Option<Duration> {
    timers_mut(|t| {
        t.first().map(|(d, _, _)| {
            let now = Instant::now();
            if *d <= now { Duration::ZERO } else { *d - now }
        })
    })
}
