use alloc::vec::Vec;
use core::cell::UnsafeCell;
use core::sync::atomic::{AtomicU32, AtomicU64, Ordering};
use crate::arch_call;
use crate::os;
use crate::protocol::*;

static NEXT_INV_ID: AtomicU64 = AtomicU64::new(1);
static READER_THREAD_ID: AtomicU64 = AtomicU64::new(0);

pub const SLOT_FREE: u32 = 0;
pub const SLOT_WAITING: u32 = 1;
pub const SLOT_HAS_CALL: u32 = 2;
pub const SLOT_RETURNED: u32 = 3;

pub const MAX_INVOCATION_SLOTS: usize = 32;

#[derive(Clone, Copy)]
pub struct DelegatedCall {
    pub req_id: u64,
    pub sym_addr: usize,
    pub args: [u64; 8],
    pub argc: usize,
}

impl DelegatedCall {
    pub const fn empty() -> Self {
        DelegatedCall {
            req_id: 0,
            sym_addr: 0,
            args: [0; 8],
            argc: 0,
        }
    }
}

pub struct InvocationSlot {
    pub state: AtomicU32,
    pub inv_id: AtomicU64,
    pub thread_id: AtomicU64,
    pub return_val: AtomicU64,
    pub call: UnsafeCell<DelegatedCall>,
}

unsafe impl Sync for InvocationSlot {}

impl InvocationSlot {
    pub const fn new() -> Self {
        InvocationSlot {
            state: AtomicU32::new(SLOT_FREE),
            inv_id: AtomicU64::new(0),
            thread_id: AtomicU64::new(0),
            return_val: AtomicU64::new(0),
            call: UnsafeCell::new(DelegatedCall::empty()),
        }
    }
}

static INVOCATION_SLOTS: [InvocationSlot; MAX_INVOCATION_SLOTS] = [
    InvocationSlot::new(), InvocationSlot::new(), InvocationSlot::new(), InvocationSlot::new(),
    InvocationSlot::new(), InvocationSlot::new(), InvocationSlot::new(), InvocationSlot::new(),
    InvocationSlot::new(), InvocationSlot::new(), InvocationSlot::new(), InvocationSlot::new(),
    InvocationSlot::new(), InvocationSlot::new(), InvocationSlot::new(), InvocationSlot::new(),
    InvocationSlot::new(), InvocationSlot::new(), InvocationSlot::new(), InvocationSlot::new(),
    InvocationSlot::new(), InvocationSlot::new(), InvocationSlot::new(), InvocationSlot::new(),
    InvocationSlot::new(), InvocationSlot::new(), InvocationSlot::new(), InvocationSlot::new(),
    InvocationSlot::new(), InvocationSlot::new(), InvocationSlot::new(), InvocationSlot::new(),
];

// Maps trampoline slot to registered cb_id
static mut SLOT_CB_IDS: [u64; 16] = [0; 16];

pub fn set_reader_thread_id(tid: u64) {
    READER_THREAD_ID.store(tid, Ordering::Release);
}

pub fn reader_thread_id() -> u64 {
    READER_THREAD_ID.load(Ordering::Acquire)
}

pub fn on_cb_return(inv_id: u64, ret_val: u64) {
    for i in 0..MAX_INVOCATION_SLOTS {
        let slot = &INVOCATION_SLOTS[i];
        let st = slot.state.load(Ordering::Acquire);
        if st != SLOT_FREE && slot.inv_id.load(Ordering::Acquire) == inv_id {
            slot.return_val.store(ret_val, Ordering::Release);
            slot.state.store(SLOT_RETURNED, Ordering::Release);
            return;
        }
    }
}

/// Routes an `OP_CALL_REQ` with `parent_inv_id != 0` to the waiting callback slot.
/// Returns `true` if delegated to a background callback thread, or `false` if the
/// caller (e.g. synchronous pump on the same thread) should execute it inline.
pub fn try_delegate_call(parent_inv_id: u64, req_id: u64, sym_addr: usize, args: &[u64]) -> bool {
    if parent_inv_id == 0 {
        return false;
    }
    let cur_tid = os::current_thread_id();
    for i in 0..MAX_INVOCATION_SLOTS {
        let slot = &INVOCATION_SLOTS[i];
        if slot.state.load(Ordering::Acquire) == SLOT_WAITING
            && slot.inv_id.load(Ordering::Acquire) == parent_inv_id
        {
            let target_tid = slot.thread_id.load(Ordering::Acquire);
            if target_tid == cur_tid {
                // Same thread (synchronous callback pumping read_frame): execute inline!
                return false;
            }
            let mut dcall = DelegatedCall::empty();
            dcall.req_id = req_id;
            dcall.sym_addr = sym_addr;
            let count = args.len().min(8);
            dcall.argc = count;
            for j in 0..count {
                dcall.args[j] = args[j];
            }
            unsafe {
                *slot.call.get() = dcall;
            }
            slot.state.store(SLOT_HAS_CALL, Ordering::Release);
            return true;
        }
    }
    false
}

unsafe extern "C" fn trampoline_0(a0: usize, a1: usize, a2: usize, a3: usize, a4: usize, a5: usize, a6: usize, a7: usize) -> usize { unsafe { on_callback(0, [a0, a1, a2, a3, a4, a5, a6, a7]) } }
unsafe extern "C" fn trampoline_1(a0: usize, a1: usize, a2: usize, a3: usize, a4: usize, a5: usize, a6: usize, a7: usize) -> usize { unsafe { on_callback(1, [a0, a1, a2, a3, a4, a5, a6, a7]) } }
unsafe extern "C" fn trampoline_2(a0: usize, a1: usize, a2: usize, a3: usize, a4: usize, a5: usize, a6: usize, a7: usize) -> usize { unsafe { on_callback(2, [a0, a1, a2, a3, a4, a5, a6, a7]) } }
unsafe extern "C" fn trampoline_3(a0: usize, a1: usize, a2: usize, a3: usize, a4: usize, a5: usize, a6: usize, a7: usize) -> usize { unsafe { on_callback(3, [a0, a1, a2, a3, a4, a5, a6, a7]) } }
unsafe extern "C" fn trampoline_4(a0: usize, a1: usize, a2: usize, a3: usize, a4: usize, a5: usize, a6: usize, a7: usize) -> usize { unsafe { on_callback(4, [a0, a1, a2, a3, a4, a5, a6, a7]) } }
unsafe extern "C" fn trampoline_5(a0: usize, a1: usize, a2: usize, a3: usize, a4: usize, a5: usize, a6: usize, a7: usize) -> usize { unsafe { on_callback(5, [a0, a1, a2, a3, a4, a5, a6, a7]) } }
unsafe extern "C" fn trampoline_6(a0: usize, a1: usize, a2: usize, a3: usize, a4: usize, a5: usize, a6: usize, a7: usize) -> usize { unsafe { on_callback(6, [a0, a1, a2, a3, a4, a5, a6, a7]) } }
unsafe extern "C" fn trampoline_7(a0: usize, a1: usize, a2: usize, a3: usize, a4: usize, a5: usize, a6: usize, a7: usize) -> usize { unsafe { on_callback(7, [a0, a1, a2, a3, a4, a5, a6, a7]) } }
unsafe extern "C" fn trampoline_8(a0: usize, a1: usize, a2: usize, a3: usize, a4: usize, a5: usize, a6: usize, a7: usize) -> usize { unsafe { on_callback(8, [a0, a1, a2, a3, a4, a5, a6, a7]) } }
unsafe extern "C" fn trampoline_9(a0: usize, a1: usize, a2: usize, a3: usize, a4: usize, a5: usize, a6: usize, a7: usize) -> usize { unsafe { on_callback(9, [a0, a1, a2, a3, a4, a5, a6, a7]) } }
unsafe extern "C" fn trampoline_10(a0: usize, a1: usize, a2: usize, a3: usize, a4: usize, a5: usize, a6: usize, a7: usize) -> usize { unsafe { on_callback(10, [a0, a1, a2, a3, a4, a5, a6, a7]) } }
unsafe extern "C" fn trampoline_11(a0: usize, a1: usize, a2: usize, a3: usize, a4: usize, a5: usize, a6: usize, a7: usize) -> usize { unsafe { on_callback(11, [a0, a1, a2, a3, a4, a5, a6, a7]) } }
unsafe extern "C" fn trampoline_12(a0: usize, a1: usize, a2: usize, a3: usize, a4: usize, a5: usize, a6: usize, a7: usize) -> usize { unsafe { on_callback(12, [a0, a1, a2, a3, a4, a5, a6, a7]) } }
unsafe extern "C" fn trampoline_13(a0: usize, a1: usize, a2: usize, a3: usize, a4: usize, a5: usize, a6: usize, a7: usize) -> usize { unsafe { on_callback(13, [a0, a1, a2, a3, a4, a5, a6, a7]) } }
unsafe extern "C" fn trampoline_14(a0: usize, a1: usize, a2: usize, a3: usize, a4: usize, a5: usize, a6: usize, a7: usize) -> usize { unsafe { on_callback(14, [a0, a1, a2, a3, a4, a5, a6, a7]) } }
unsafe extern "C" fn trampoline_15(a0: usize, a1: usize, a2: usize, a3: usize, a4: usize, a5: usize, a6: usize, a7: usize) -> usize { unsafe { on_callback(15, [a0, a1, a2, a3, a4, a5, a6, a7]) } }

const TRAMPOLINES: [unsafe extern "C" fn(usize, usize, usize, usize, usize, usize, usize, usize) -> usize; 16] = [
    trampoline_0, trampoline_1, trampoline_2, trampoline_3,
    trampoline_4, trampoline_5, trampoline_6, trampoline_7,
    trampoline_8, trampoline_9, trampoline_10, trampoline_11,
    trampoline_12, trampoline_13, trampoline_14, trampoline_15,
];

pub fn register_trampoline(cb_id: u64) -> Option<u64> {
    for i in 0..16 {
        unsafe {
            if SLOT_CB_IDS[i] == 0 || SLOT_CB_IDS[i] == cb_id {
                SLOT_CB_IDS[i] = cb_id;
                return Some(TRAMPOLINES[i] as usize as u64);
            }
        }
    }
    None
}

fn alloc_invocation_slot(inv_id: u64, tid: u64) -> &'static InvocationSlot {
    loop {
        for i in 0..MAX_INVOCATION_SLOTS {
            let slot = &INVOCATION_SLOTS[i];
            if slot.state.compare_exchange(SLOT_FREE, SLOT_WAITING, Ordering::AcqRel, Ordering::Relaxed).is_ok() {
                slot.inv_id.store(inv_id, Ordering::Release);
                slot.thread_id.store(tid, Ordering::Release);
                slot.return_val.store(0, Ordering::Release);
                return slot;
            }
        }
        core::hint::spin_loop();
    }
}

fn send_call_response(req_id: u64, ret_val: u64) {
    let mut resp = Vec::with_capacity(17);
    resp.push(0); // ok
    resp.extend_from_slice(&1u32.to_le_bytes()); // 1 result
    resp.extend_from_slice(&ret_val.to_le_bytes());
    resp.extend_from_slice(&0u32.to_le_bytes()); // err_len = 0
    let _ = send_frame(OP_CALL_RESP, req_id, &resp);
}

unsafe fn on_callback(slot_idx: usize, args: [usize; 8]) -> usize {
    let cb_id = unsafe { SLOT_CB_IDS[slot_idx] };
    let inv_id = NEXT_INV_ID.fetch_add(1, Ordering::SeqCst);
    let tid = os::current_thread_id();
    let inv_slot = alloc_invocation_slot(inv_id, tid);

    // Payload: [cb_id: u64][thread_id: u64][argc: u32][args: [u64; 8]]
    let mut payload = Vec::with_capacity(8 + 8 + 4 + 64);
    payload.extend_from_slice(&cb_id.to_le_bytes());
    payload.extend_from_slice(&tid.to_le_bytes());
    payload.extend_from_slice(&8u32.to_le_bytes()); // argc = 8
    for a in args {
        payload.extend_from_slice(&(a as u64).to_le_bytes());
    }

    let _ = send_frame(OP_CB_INVOKE, inv_id, &payload);

    let is_reader_thread = tid != 0 && tid == reader_thread_id();

    loop {
        let st = inv_slot.state.load(Ordering::Acquire);
        if st == SLOT_RETURNED {
            let ret = inv_slot.return_val.load(Ordering::Acquire);
            inv_slot.inv_id.store(0, Ordering::Release);
            inv_slot.state.store(SLOT_FREE, Ordering::Release);
            return ret as usize;
        }
        if st == SLOT_HAS_CALL {
            let dcall = unsafe { *inv_slot.call.get() };
            // Reset state to WAITING BEFORE calling into C in case the C function
            // itself triggers a nested callback or another re-entrant call!
            inv_slot.state.store(SLOT_WAITING, Ordering::Release);
            let ret_val = unsafe { arch_call::call_c_func(dcall.sym_addr, &dcall.args[..dcall.argc]) };
            send_call_response(dcall.req_id, ret_val);
            continue;
        }

        if is_reader_thread || crate::try_acquire_reader_lock() {
            // Either we ARE the reader thread (synchronous callback), or the main
            // reader thread is currently blocked inside a synchronous C call (such
            // as WaitForSingleObject) so we can temporarily pump stdin frames!
            let is_temp_pump = !is_reader_thread;
            if let Ok((hdr, body)) = read_frame() {
                crate::dispatch_frame(hdr, &body);
            } else {
                if is_temp_pump {
                    crate::release_reader_lock();
                }
                inv_slot.inv_id.store(0, Ordering::Release);
                inv_slot.state.store(SLOT_FREE, Ordering::Release);
                return 0;
            }
            if is_temp_pump {
                crate::release_reader_lock();
            }
        } else {
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
}

