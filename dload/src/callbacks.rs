use alloc::boxed::Box;
use alloc::vec::Vec;
use core::cell::UnsafeCell;
use core::sync::atomic::{AtomicBool, AtomicU32, AtomicU64, Ordering};
use crate::arch_call;
use crate::os;
use crate::protocol::*;

static NEXT_INV_ID: AtomicU64 = AtomicU64::new(1);
static READER_THREAD_ID: AtomicU64 = AtomicU64::new(0);
static OUTER_CALL_ACTIVE: AtomicU32 = AtomicU32::new(0);

pub fn set_outer_call_depth(val: u32) {
    OUTER_CALL_ACTIVE.store(val, Ordering::Release);
}

pub fn is_outer_call_active() -> bool {
    OUTER_CALL_ACTIVE.load(Ordering::Acquire) != 0
}

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

// ─── Dynamic RW -> RX Trampoline Page Allocator ─────────────────────────────

const SLOTS_PER_PAGE: usize = 128;
const THUNK_STRIDE: usize = 32;

struct TrampolinePage {
    exec_base: usize,
    cb_ids: Box<[AtomicU64; SLOTS_PER_PAGE]>,
}

static TRAMPOLINE_LOCK: AtomicBool = AtomicBool::new(false);
static mut TRAMPOLINE_PAGES: Option<Vec<TrampolinePage>> = None;

fn lock_trampolines() {
    while TRAMPOLINE_LOCK
        .compare_exchange_weak(false, true, Ordering::Acquire, Ordering::Relaxed)
        .is_err()
    {
        core::hint::spin_loop();
    }
}

fn unlock_trampolines() {
    TRAMPOLINE_LOCK.store(false, Ordering::Release);
}

#[cfg(all(target_arch = "x86_64", windows))]
#[unsafe(naked)]
unsafe extern "C" fn trampoline_common_entry() {
    // Win64 ABI: rcx, rdx, r8, r9; stack args at [rsp+40], [rsp+48], [rsp+56], [rsp+64].
    // r10 holds cb_id loaded by the 32-byte thunk.
    // On entry, [rsp] is the 8-byte return address, so `rsp % 16 == 8`.
    // Subtracting 104 (6 * 16 + 8) makes `rsp % 16 == 0`, so `call` pushes 8 bytes
    // (`rsp % 16 == 8` at callee entry, as required by the x86_64 ABI!).
    core::arch::naked_asm!(
        "sub rsp, 104",
        "mov [rsp + 32], rcx",
        "mov [rsp + 40], rdx",
        "mov [rsp + 48], r8",
        "mov [rsp + 56], r9",
        "mov rax, [rsp + 144]",
        "mov [rsp + 64], rax",
        "mov rax, [rsp + 152]",
        "mov [rsp + 72], rax",
        "mov rax, [rsp + 160]",
        "mov [rsp + 80], rax",
        "mov rax, [rsp + 168]",
        "mov [rsp + 88], rax",
        "mov rcx, r10",
        "lea rdx, [rsp + 32]",
        "call {handler}",
        "add rsp, 104",
        "ret",
        handler = sym on_callback_dynamic,
    );
}

#[cfg(all(target_arch = "x86_64", not(windows)))]
#[unsafe(naked)]
unsafe extern "C" fn trampoline_common_entry() {
    // SysV AMD64 ABI: rdi, rsi, rdx, rcx, r8, r9; stack args at [rsp+8], [rsp+16].
    // r10 holds cb_id loaded by the 32-byte thunk.
    // On entry, [rsp] is the 8-byte return address, so `rsp % 16 == 8`.
    // Subtracting 72 (4 * 16 + 8) makes `rsp % 16 == 0`, so `call` pushes 8 bytes
    // (`rsp % 16 == 8` at callee entry, as required by the SysV AMD64 ABI!).
    core::arch::naked_asm!(
        "sub rsp, 72",
        "mov [rsp + 0], rdi",
        "mov [rsp + 8], rsi",
        "mov [rsp + 16], rdx",
        "mov [rsp + 24], rcx",
        "mov [rsp + 32], r8",
        "mov [rsp + 40], r9",
        "mov rax, [rsp + 80]",
        "mov [rsp + 48], rax",
        "mov rax, [rsp + 88]",
        "mov [rsp + 56], rax",
        "mov rdi, r10",
        "mov rsi, rsp",
        "call {handler}",
        "add rsp, 72",
        "ret",
        handler = sym on_callback_dynamic,
    );
}

#[cfg(target_arch = "aarch64")]
#[unsafe(naked)]
unsafe extern "C" fn trampoline_common_entry() {
    // AAPCS64 ABI: x0..x7 hold the 8 arguments; x16 holds cb_id loaded by the thunk.
    core::arch::naked_asm!(
        "stp x29, x30, [sp, #-80]!",
        "mov x29, sp",
        "stp x0, x1, [sp, #16]",
        "stp x2, x3, [sp, #32]",
        "stp x4, x5, [sp, #48]",
        "stp x6, x7, [sp, #64]",
        "mov x0, x16",
        "add x1, sp, #16",
        "bl {handler}",
        "ldp x29, x30, [sp], #80",
        "ret",
        handler = sym on_callback_dynamic,
    );
}

#[cfg(target_arch = "arm")]
#[unsafe(naked)]
unsafe extern "C" fn trampoline_common_entry() {
    // AAPCS32 ABI: r0..r3 in registers, args 4..7 on stack; r12 (ip) holds &cb_ids[i].
    core::arch::naked_asm!(
        "push {{r4, lr}}",
        "sub sp, sp, #32",
        "stmia sp, {{r0, r1, r2, r3}}",
        "ldr r0, [sp, #40]",
        "str r0, [sp, #16]",
        "ldr r0, [sp, #44]",
        "str r0, [sp, #20]",
        "ldr r0, [sp, #48]",
        "str r0, [sp, #24]",
        "ldr r0, [sp, #52]",
        "str r0, [sp, #28]",
        "ldr r0, [r12]",
        "ldr r1, [r12, #4]",
        "mov r2, sp",
        "bl {handler}",
        "add sp, sp, #32",
        "pop {{r4, pc}}",
        handler = sym on_callback_dynamic,
    );
}

fn write_thunk(slot_buf: &mut [u8], cb_id_addr: usize, entry_addr: usize) {
    #[cfg(target_arch = "x86_64")]
    {
        // movabs r10, cb_id_addr (49 BA <u64>)
        // mov    r10, [r10]      (4D 8B 12)
        // movabs rax, entry_addr (48 B8 <u64>)
        // jmp    rax             (FF E0)
        slot_buf[0] = 0x49;
        slot_buf[1] = 0xBA;
        slot_buf[2..10].copy_from_slice(&(cb_id_addr as u64).to_le_bytes());
        slot_buf[10] = 0x4D;
        slot_buf[11] = 0x8B;
        slot_buf[12] = 0x12;
        slot_buf[13] = 0x48;
        slot_buf[14] = 0xB8;
        slot_buf[15..23].copy_from_slice(&(entry_addr as u64).to_le_bytes());
        slot_buf[23] = 0xFF;
        slot_buf[24] = 0xE0;
    }
    #[cfg(target_arch = "aarch64")]
    {
        // 0x00: ldr x16, #16  (0x58000090) -> loads cb_id_addr from +16
        // 0x04: ldr x16, [x16](0xF9400210) -> dereferences cb_id_addr into x16
        // 0x08: ldr x17, #16  (0x58000091) -> loads entry_addr from +24
        // 0x0c: br  x17       (0xD61F0220)
        // 0x10: .quad cb_id_addr
        // 0x18: .quad entry_addr
        slot_buf[0..4].copy_from_slice(&0x58000090u32.to_le_bytes());
        slot_buf[4..8].copy_from_slice(&0xF9400210u32.to_le_bytes());
        slot_buf[8..12].copy_from_slice(&0x58000091u32.to_le_bytes());
        slot_buf[12..16].copy_from_slice(&0xD61F0220u32.to_le_bytes());
        slot_buf[16..24].copy_from_slice(&(cb_id_addr as u64).to_le_bytes());
        slot_buf[24..32].copy_from_slice(&(entry_addr as u64).to_le_bytes());
    }
    #[cfg(target_arch = "arm")]
    {
        // 0x00: ldr r12, [pc, #0] (0xE59FC000) -> loads cb_id_addr from +8
        // 0x04: ldr pc,  [pc, #0] (0xE59FF000) -> jumps to entry_addr at +12
        // 0x08: .word cb_id_addr
        // 0x0c: .word entry_addr
        slot_buf[0..4].copy_from_slice(&0xE59FC000u32.to_le_bytes());
        slot_buf[4..8].copy_from_slice(&0xE59FF000u32.to_le_bytes());
        slot_buf[8..12].copy_from_slice(&(cb_id_addr as u32).to_le_bytes());
        slot_buf[12..16].copy_from_slice(&(entry_addr as u32).to_le_bytes());
    }
}

fn alloc_trampoline_page() -> Option<TrampolinePage> {
    let cb_ids: Box<[AtomicU64; SLOTS_PER_PAGE]> =
        Box::new([const { AtomicU64::new(0) }; SLOTS_PER_PAGE]);
    let base_cb_ptr = cb_ids.as_ptr() as usize;
    let entry_addr = trampoline_common_entry as *const () as usize;

    let exec_ptr = os::alloc_executable_page(|page_buf| {
        for i in 0..SLOTS_PER_PAGE {
            let off = i * THUNK_STRIDE;
            let cb_id_addr = base_cb_ptr + i * core::mem::size_of::<AtomicU64>();
            write_thunk(&mut page_buf[off..off + THUNK_STRIDE], cb_id_addr, entry_addr);
        }
    })?;

    Some(TrampolinePage {
        exec_base: exec_ptr as usize,
        cb_ids,
    })
}

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

pub fn register_trampoline(cb_id: u64) -> Option<u64> {
    if cb_id == 0 {
        return None;
    }
    lock_trampolines();
    let res = unsafe {
        let pages_ptr = &raw mut TRAMPOLINE_PAGES;
        if (*pages_ptr).is_none() {
            *pages_ptr = Some(Vec::new());
        }
        let pages = (*pages_ptr).as_mut().unwrap();
        let mut found: Option<u64> = None;
        'search: for page in pages.iter() {
            for i in 0..SLOTS_PER_PAGE {
                let cur = page.cb_ids[i].load(Ordering::Acquire);
                if cur == 0 || cur == cb_id {
                    page.cb_ids[i].store(cb_id, Ordering::Release);
                    found = Some((page.exec_base + i * THUNK_STRIDE) as u64);
                    break 'search;
                }
            }
        }
        if found.is_none() {
            if let Some(new_page) = alloc_trampoline_page() {
                new_page.cb_ids[0].store(cb_id, Ordering::Release);
                let addr = new_page.exec_base as u64;
                pages.push(new_page);
                found = Some(addr);
            }
        }
        found
    };
    unlock_trampolines();
    res
}

pub fn unregister_trampoline(cb_id: u64) {
    if cb_id == 0 {
        return;
    }
    lock_trampolines();
    unsafe {
        let pages_ptr = &raw mut TRAMPOLINE_PAGES;
        if let Some(pages) = (*pages_ptr).as_ref() {
            for page in pages.iter() {
                for i in 0..SLOTS_PER_PAGE {
                    if page.cb_ids[i].load(Ordering::Acquire) == cb_id {
                        page.cb_ids[i].store(0, Ordering::Release);
                    }
                }
            }
        }
    }
    unlock_trampolines();
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

#[cfg(not(target_arch = "arm"))]
unsafe extern "C" fn on_callback_dynamic(cb_id: u64, args_ptr: *const usize) -> usize {
    let slice = unsafe { core::slice::from_raw_parts(args_ptr, 8) };
    let args = [slice[0], slice[1], slice[2], slice[3], slice[4], slice[5], slice[6], slice[7]];
    unsafe { on_callback(cb_id, args) }
}

#[cfg(target_arch = "arm")]
unsafe extern "C" fn on_callback_dynamic(cb_id_lo: u32, cb_id_hi: u32, args_ptr: *const usize) -> usize {
    let cb_id = (cb_id_lo as u64) | ((cb_id_hi as u64) << 32);
    let slice = unsafe { core::slice::from_raw_parts(args_ptr, 8) };
    let args = [slice[0], slice[1], slice[2], slice[3], slice[4], slice[5], slice[6], slice[7]];
    unsafe { on_callback(cb_id, args) }
}

unsafe fn on_callback(cb_id: u64, args: [usize; 8]) -> usize {
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
    let mut spin_iters: u32 = 0;

    loop {
        let st = inv_slot.state.load(Ordering::Acquire);
        if st == SLOT_RETURNED {
            let ret = inv_slot.return_val.load(Ordering::Acquire);
            inv_slot.inv_id.store(0, Ordering::Release);
            inv_slot.state.store(SLOT_FREE, Ordering::Release);
            return ret as usize;
        }
        if st == SLOT_HAS_CALL {
            spin_iters = 0;
            let dcall = unsafe { *inv_slot.call.get() };
            // Reset state to WAITING BEFORE calling into C in case the C function
            // itself triggers a nested callback or another re-entrant call!
            inv_slot.state.store(SLOT_WAITING, Ordering::Release);
            let ret_val = unsafe { arch_call::call_c_func(dcall.sym_addr, &dcall.args[..dcall.argc]) };
            send_call_response(dcall.req_id, ret_val);
            continue;
        }

        if is_reader_thread || (is_outer_call_active() && crate::try_acquire_reader_lock()) {
            // Either we ARE the reader thread (synchronous callback), or the main
            // reader thread is currently blocked inside a synchronous C call (such
            // as WaitForSingleObject) so we can temporarily pump stdin frames!
            let is_temp_pump = !is_reader_thread;
            if is_temp_pump
                && (!is_outer_call_active() || inv_slot.state.load(Ordering::Acquire) != SLOT_WAITING)
            {
                crate::release_reader_lock();
                continue;
            }
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
            spin_iters = spin_iters.wrapping_add(1);
            if spin_iters < 4096 {
                core::hint::spin_loop();
                continue;
            }
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

