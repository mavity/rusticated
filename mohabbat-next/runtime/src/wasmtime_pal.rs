//! Wasmtime `min-platform` (`vm::sys::custom`) C-ABI bridge over `PalAbiTable`
//! and `RuntimeGlobals`.

use crate::abi::{AbiPlatformInfo, MEM_CODE_EXEC_RX, MEM_CODE_JIT_RW, MEM_READWRITE, abi, globals};

const WASMTIME_PROT_WRITE: u32 = 1 << 1;
const WASMTIME_PROT_EXEC: u32 = 1 << 2;

#[inline(always)]
fn prot_to_pal_flags(prot_flags: u32) -> u32 {
    if (prot_flags & WASMTIME_PROT_EXEC) != 0 {
        if (prot_flags & WASMTIME_PROT_WRITE) != 0 {
            MEM_CODE_JIT_RW
        } else {
            MEM_CODE_EXEC_RX
        }
    } else {
        MEM_READWRITE
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn wasmtime_page_size() -> usize {
    let mut info = core::mem::MaybeUninit::<AbiPlatformInfo>::zeroed();
    let rc = (abi().get_platform_info)(
        info.as_mut_ptr().cast::<u8>(),
        core::mem::size_of::<AbiPlatformInfo>(),
    );
    if rc == 0 {
        let page = info.assume_init().page_size as usize;
        if page >= 4096 && page.is_power_of_two() {
            return page;
        }
    }
    4096
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn wasmtime_mmap_new(
    size: usize,
    prot_flags: u32,
    ret: &mut *mut u8,
) -> i32 {
    let page = wasmtime_page_size();
    let ptr = (abi().alloc_mem)(size.max(page), page, prot_to_pal_flags(prot_flags));
    if ptr.is_null() {
        return 12; // ENOMEM
    }
    *ret = ptr;
    0
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn wasmtime_mmap_remap(
    addr: *mut u8,
    size: usize,
    prot_flags: u32,
) -> i32 {
    if addr.is_null() {
        return 22; // EINVAL
    }
    if size > 0 && (prot_flags & WASMTIME_PROT_WRITE) != 0 {
        crate::slice_memset(addr, 0, size);
    }
    if (abi().protect_mem)(addr, size, prot_to_pal_flags(prot_flags)) != 0 {
        return 1;
    }
    0
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn wasmtime_munmap(ptr: *mut u8, size: usize) -> i32 {
    if !ptr.is_null() && size > 0 {
        (abi().free_mem)(ptr, size);
    }
    0
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn wasmtime_mprotect(
    ptr: *mut u8,
    size: usize,
    prot_flags: u32,
) -> i32 {
    let rc = (abi().protect_mem)(ptr, size, prot_to_pal_flags(prot_flags));
    if rc != 0 {
        return rc;
    }
    if (prot_flags & WASMTIME_PROT_EXEC) != 0 && !ptr.is_null() && size > 0 {
        (abi().flush_icache)(ptr, size);
    }
    0
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn wasmtime_memory_image_new(
    _ptr: *const u8,
    _len: usize,
    ret: &mut *mut u8,
) -> i32 {
    *ret = core::ptr::null_mut();
    0
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn wasmtime_memory_image_map_at(
    _image: *mut u8,
    _addr: *mut u8,
    _len: usize,
) -> i32 {
    22 // EINVAL: memory images are disabled
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn wasmtime_memory_image_free(_image: *mut u8) {}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn wasmtime_tls_get(slot: usize) -> *mut u8 {
    let g = globals();
    if slot < g.wasmtime_tls_slots.len() {
        g.wasmtime_tls_slots[slot]
    } else {
        core::ptr::null_mut()
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn wasmtime_tls_set(slot: usize, ptr: *mut u8) {
    let g = globals();
    if slot < g.wasmtime_tls_slots.len() {
        g.wasmtime_tls_slots[slot] = ptr;
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn mohabbat_wasmtime_global_code_slot() -> *mut *mut u8 {
    &raw mut globals().wasmtime_global_code
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn mohabbat_next_id(slot: usize) -> u64 {
    let g = globals();
    if slot < g.wasmtime_id_counters.len() {
        let prev = g.wasmtime_id_counters[slot];
        g.wasmtime_id_counters[slot] = prev.wrapping_add(1);
        prev
    } else {
        0
    }
}
