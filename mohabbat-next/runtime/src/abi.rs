//! Canonical Native Rusticated PAL ABI (`PalAbiTable` at `-0x20000`) and
//! private Runtime Globals (`RuntimeGlobals` at `-0x10000`).

#[path = "../../pal/abi/guest.rs"]
mod shared;

pub use shared::{
    FLAG_COMPLETED, MEM_CODE_EXEC_RX, MEM_CODE_JIT_RW, MEM_READWRITE, Overlapped, PalAbiTable,
    ProcessSpawnOverlapped, READ_FLAG_POSITIONAL,
};
pub(crate) use shared::pal_fn;

#[repr(C)]
pub struct RuntimeGlobals {
    pub initialized: bool,

    pub guest_mem_base: *mut u8,
    pub guest_mem_len: usize,
    pub guest_mem_cap: usize,

    pub heap_base: *mut u8,
    pub heap_used: usize,
    pub heap_cap: usize,

    pub jit_code_base: *mut u8,
    pub jit_code_len: usize,

    pub guest_run_fn: Option<pal_fn!(fn())>,

    pub bump_empty_chunk: *mut u8,
    pub hashbrown_empty_tags: *mut u8,
    pub heap_rc_int: *const u8,
    pub heap_rc_float: *const u8,
    pub heap_rc_int_int: *const u8,
    pub heap_ty_i64_i64: *const u16,
    pub heap_ty_i8x16: *const u16,
}

#[inline(always)]
pub unsafe fn slice_origin() -> *mut u8 {
    let addr: usize;
    #[cfg(target_arch = "x86_64")]
    core::arch::asm!(
        "lea {0}, [rip + runtime_code_start]",
        out(reg) addr,
        options(pure, nomem, nostack),
    );
    #[cfg(target_arch = "aarch64")]
    core::arch::asm!(
        "adr {0}, runtime_code_start",
        out(reg) addr,
        options(pure, nomem, nostack),
    );
    addr as *mut u8
}

#[cfg(test)]
static mut TEST_GLOBALS: RuntimeGlobals = RuntimeGlobals {
    initialized: false,
    guest_mem_base: core::ptr::null_mut(),
    guest_mem_len: 0,
    guest_mem_cap: 0,
    heap_base: core::ptr::null_mut(),
    heap_used: 0,
    heap_cap: 0,
    jit_code_base: core::ptr::null_mut(),
    jit_code_len: 0,
    guest_run_fn: None,
    bump_empty_chunk: core::ptr::null_mut(),
    hashbrown_empty_tags: core::ptr::null_mut(),
    heap_rc_int: core::ptr::null(),
    heap_rc_float: core::ptr::null(),
    heap_rc_int_int: core::ptr::null(),
    heap_ty_i64_i64: core::ptr::null(),
    heap_ty_i8x16: core::ptr::null(),
};

#[cfg(test)]
static mut TEST_ABI: core::mem::MaybeUninit<PalAbiTable> = core::mem::MaybeUninit::uninit();

#[inline(always)]
pub unsafe fn abi() -> &'static PalAbiTable {
    #[cfg(not(test))]
    {
        &*(slice_origin().sub(0x20000) as *const PalAbiTable)
    }
    #[cfg(test)]
    {
        &*(core::ptr::addr_of!(TEST_ABI).cast::<PalAbiTable>())
    }
}

#[inline(always)]
pub unsafe fn globals() -> &'static mut RuntimeGlobals {
    #[cfg(not(test))]
    {
        &mut *(slice_origin().sub(0x10000) as *mut RuntimeGlobals)
    }
    #[cfg(test)]
    {
        &mut *(core::ptr::addr_of_mut!(TEST_GLOBALS))
    }
}
