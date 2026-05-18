use crate::cell::RefCell;
use crate::collections::HashSet;

thread_local! {
    static READY: RefCell<HashSet<u64>> = RefCell::new(HashSet::new());
}

fn with_ready<R>(f: impl FnOnce(&mut HashSet<u64>) -> R) -> R {
    READY.with(|r| f(&mut *r.borrow_mut()))
}

pub(crate) fn mark_ready(token: u64) {
    with_ready(|r| {
        r.insert(token);
    });
}

pub(crate) fn consume_ready(token: u64) -> bool {
    with_ready(|r| r.remove(&token))
}
