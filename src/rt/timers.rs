use std::cell::RefCell;
use std::time::{Duration, Instant};

thread_local! {
    /// Sorted (by deadline ascending) list of pending timers. Each entry is a
    /// `(deadline, id)` pair; the matching `Sleep` future polls by checking
    /// `Instant::now() >= deadline`.
    static TIMERS: RefCell<Vec<(Instant, u64)>> = const { RefCell::new(Vec::new()) };
    static NEXT_TIMER_ID: RefCell<u64> = const { RefCell::new(1) };
}

pub(crate) fn register_timer(deadline: Instant) -> u64 {
    NEXT_TIMER_ID.with(|n| {
        let mut n = n.borrow_mut();
        let id = *n;
        *n = n.wrapping_add(1);
        TIMERS.with(|t| {
            let mut t = t.borrow_mut();
            // Insert maintaining ascending order by deadline.
            let pos = t.partition_point(|(d, _)| *d <= deadline);
            t.insert(pos, (deadline, id));
        });
        id
    })
}

pub(crate) fn cancel_timer(id: u64) {
    TIMERS.with(|t| {
        let mut t = t.borrow_mut();
        if let Some(pos) = t.iter().position(|(_, i)| *i == id) {
            t.remove(pos);
        }
    });
}

pub(crate) fn next_deadline() -> Option<Duration> {
    TIMERS.with(|t| {
        let t = t.borrow();
        t.first().map(|(d, _)| {
            let now = Instant::now();
            if *d <= now { Duration::ZERO } else { *d - now }
        })
    })
}