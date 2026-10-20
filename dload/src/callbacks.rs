use alloc::vec::Vec;
use core::sync::atomic::{AtomicU64, Ordering};
use crate::protocol::*;

static NEXT_INV_ID: AtomicU64 = AtomicU64::new(1);
static CB_RETURN_INV: AtomicU64 = AtomicU64::new(0);

// Maps trampoline slot to registered cb_id
static mut SLOT_CB_IDS: [u64; 16] = [0; 16];

pub fn on_cb_return(inv_id: u64) {
    CB_RETURN_INV.store(inv_id, Ordering::Release);
}

unsafe extern "C" fn trampoline_0(a0: usize, a1: usize) { unsafe { on_callback(0, a0, a1) } }
unsafe extern "C" fn trampoline_1(a0: usize, a1: usize) { unsafe { on_callback(1, a0, a1) } }
unsafe extern "C" fn trampoline_2(a0: usize, a1: usize) { unsafe { on_callback(2, a0, a1) } }
unsafe extern "C" fn trampoline_3(a0: usize, a1: usize) { unsafe { on_callback(3, a0, a1) } }
unsafe extern "C" fn trampoline_4(a0: usize, a1: usize) { unsafe { on_callback(4, a0, a1) } }
unsafe extern "C" fn trampoline_5(a0: usize, a1: usize) { unsafe { on_callback(5, a0, a1) } }
unsafe extern "C" fn trampoline_6(a0: usize, a1: usize) { unsafe { on_callback(6, a0, a1) } }
unsafe extern "C" fn trampoline_7(a0: usize, a1: usize) { unsafe { on_callback(7, a0, a1) } }

const TRAMPOLINES: [unsafe extern "C" fn(usize, usize); 8] = [
    trampoline_0, trampoline_1, trampoline_2, trampoline_3,
    trampoline_4, trampoline_5, trampoline_6, trampoline_7,
];

pub fn register_trampoline(cb_id: u64) -> Option<u64> {
    for i in 0..8 {
        unsafe {
            if SLOT_CB_IDS[i] == 0 || SLOT_CB_IDS[i] == cb_id {
                SLOT_CB_IDS[i] = cb_id;
                return Some(TRAMPOLINES[i] as usize as u64);
            }
        }
    }
    None
}

unsafe fn on_callback(slot: usize, a0: usize, a1: usize) {
    let cb_id = unsafe { SLOT_CB_IDS[slot] };
    let inv_id = NEXT_INV_ID.fetch_add(1, Ordering::SeqCst);

    // Payload: [cb_id: u64][thread_id: u64][argc: u32][args: [u64; argc]]
    let mut payload = Vec::with_capacity(8 + 8 + 4 + 16);
    payload.extend_from_slice(&cb_id.to_le_bytes());
    payload.extend_from_slice(&0u64.to_le_bytes()); // thread_id
    payload.extend_from_slice(&2u32.to_le_bytes()); // argc = 2
    payload.extend_from_slice(&(a0 as u64).to_le_bytes());
    payload.extend_from_slice(&(a1 as u64).to_le_bytes());

    let _ = send_frame(OP_CB_INVOKE, inv_id, &payload);

    // Wait for main reader thread to signal OP_CB_RETURN for this inv_id
    while CB_RETURN_INV.load(Ordering::Acquire) != inv_id {
        #[cfg(windows)]
        unsafe {
            #[link(name = "kernel32", kind = "raw-dylib")]
            unsafe extern "system" { fn Sleep(ms: u32); }
            Sleep(1);
        }
        #[cfg(not(windows))]
        unsafe {
            unsafe extern "C" { fn usleep(usec: u32) -> i32; }
            usleep(500);
        }
    }
}
