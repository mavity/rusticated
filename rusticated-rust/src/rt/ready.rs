use crate::collections::HashSet;
use core::cell::UnsafeCell;

#[repr(align(8))]
struct ReadyStorage(UnsafeCell<Option<HashSet<u64>>>);

unsafe impl Sync for ReadyStorage {}

static READY_STORAGE: ReadyStorage = ReadyStorage(UnsafeCell::new(None));

fn with_ready<R>(f: impl FnOnce(&mut HashSet<u64>) -> R) -> R {
    unsafe {
        let storage = core::ptr::addr_of!(READY_STORAGE).cast::<ReadyStorage>();
        let cell = (*storage).0.get();
        if (*cell).is_none() {
            *cell = Some(HashSet::new());
        }
        let set = (*cell).as_mut().unwrap_unchecked();
        f(set)
    }
}

pub(crate) fn mark_ready(token: u64) {
    with_ready(|r| {
        r.insert(token);
    });
}

pub(crate) fn consume_ready(token: u64) -> bool {
    with_ready(|r| r.remove(&token))
}
