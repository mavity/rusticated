use crate::cell::{Cell, RefCell};
use crate::task::Waker;
use crate::time::{Duration, Instant};
use crate::vec::Vec;

thread_local! {
    static TIMERS: RefCell<Vec<(Instant, u64, Waker)>> = RefCell::new(Vec::new());
    static NEXT_TIMER_ID: Cell<u64> = Cell::new(1);
}

fn with_timers<R>(f: impl FnOnce(&mut Vec<(Instant, u64, Waker)>) -> R) -> R {
    TIMERS.with(|t| f(&mut *t.borrow_mut()))
}

pub(crate) fn register_timer(deadline: Instant, waker: Waker) -> u64 {
    let id = NEXT_TIMER_ID.with(|c| c.get());
    NEXT_TIMER_ID.with(|c| c.set(id.wrapping_add(1)));
    with_timers(|t| {
        let pos = t.partition_point(|(d, _, _)| *d <= deadline);
        t.insert(pos, (deadline, id, waker));
    });
    id
}

pub(crate) fn cancel_timer(id: u64) {
    with_timers(|t| {
        if let Some(pos) = t.iter().position(|(_, i, _)| *i == id) {
            t.remove(pos);
        }
    });
}

pub(crate) fn wake_expired() -> bool {
    let now = Instant::now();
    let mut woken = false;
    with_timers(|t| {
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

pub(crate) fn next_deadline() -> Option<Duration> {
    with_timers(|t| {
        t.first().map(|(d, _, _)| {
            let now = Instant::now();
            if *d <= now { Duration::ZERO } else { *d - now }
        })
    })
}
