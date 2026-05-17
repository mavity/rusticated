use crate::cell::RefCell;
use crate::collections::HashSet;

thread_local! {
    /// Tokens for I/O events that have fired but whose futures have not yet
    /// been re-polled to observe the result.
    static READY: RefCell<HashSet<u64>> = RefCell::new(HashSet::new());
}

pub(crate) fn mark_ready(token: u64) {
    READY.with(|r| { r.borrow_mut().insert(token); });
}

pub(crate) fn consume_ready(token: u64) -> bool {
    READY.with(|r| r.borrow_mut().remove(&token))
}
