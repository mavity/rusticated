#![allow(unsafe_op_in_unsafe_fn)]
use crate::META;
use core::ffi::c_void;
use std::ptr::{null, null_mut};

use windows_sys::Win32::Foundation::*;
use windows_sys::Win32::Storage::FileSystem::*;
use windows_sys::Win32::System::LibraryLoader::{GetModuleFileNameW, GetProcAddress, LoadLibraryW};

type RunPayloadFunc = unsafe extern "C" fn(*const u8, usize) -> u32;

use windows_sys::Win32::System::Environment::GetCommandLineW;
use windows_sys::Win32::UI::Shell::CommandLineToArgvW;

// Windows unwind tables reference rust_eh_personality even with panic=abort.
// Provide a no-op stub — brot never unwinds, it aborts on panic.
#[unsafe(no_mangle)]
extern "C" fn rust_eh_personality() {}

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
    let mut read_bytes: u32 = 0;

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

    // Write washmhost to a temp file and load it via LoadLibraryW.
    // This ensures the OS handles TLS, CRT init, and PEB registration for all threads
    // (including rayon worker threads spawned by wasmtime's cranelift JIT).

    // Get temp directory
    let mut temp_dir = vec![0u16; 32768];
    let temp_dir_len = GetTempPathW(32768, temp_dir.as_mut_ptr());
    if temp_dir_len == 0 {
        std::process::exit(18);
    }

    // GetTempFileNameW creates a unique 0-byte file and returns its path
    let prefix: [u16; 4] = [b'm' as u16, b'o' as u16, b'h' as u16, 0u16];
    let mut temp_file_path = vec![0u16; 32768];
    let unique = GetTempFileNameW(
        temp_dir.as_ptr(),
        prefix.as_ptr(),
        0,
        temp_file_path.as_mut_ptr(),
    );
    if unique == 0 {
        std::process::exit(19);
    }

    // Open the temp file for writing (it was just created by GetTempFileNameW)
    let file_handle = CreateFileW(
        temp_file_path.as_ptr(),
        GENERIC_WRITE,
        0,
        null(),
        TRUNCATE_EXISTING,
        FILE_ATTRIBUTE_NORMAL,
        null_mut(),
    );
    if file_handle == INVALID_HANDLE_VALUE {
        std::process::exit(20);
    }

    // Write washmhost DLL bytes
    {
        let mut offset = 0usize;
        let mut remaining = washmhost_data.len();
        while remaining > 0 {
            let to_write = core::cmp::min(remaining, 0x7FFFFFFF) as u32;
            let mut written: u32 = 0;
            if WriteFile(
                file_handle,
                washmhost_data.as_ptr().add(offset) as *const u8,
                to_write,
                &mut written,
                null_mut(),
            ) == 0
            {
                CloseHandle(file_handle);
                DeleteFileW(temp_file_path.as_ptr());
                std::process::exit(21);
            }
            offset += written as usize;
            remaining -= written as usize;
        }
        CloseHandle(file_handle);
    }

    // Load washmhost as a normal DLL — OS handles TLS init, CRT, PEB registration
    let h_module = LoadLibraryW(temp_file_path.as_ptr());
    if h_module.is_null() {
        DeleteFileW(temp_file_path.as_ptr());
        std::process::exit(22);
    }

    // Resolve run_payload export
    let run_payload_name = b"run_payload\0";
    let run_payload_ptr = GetProcAddress(h_module, run_payload_name.as_ptr());
    if run_payload_ptr.is_none() {
        FreeLibrary(h_module);
        DeleteFileW(temp_file_path.as_ptr());
        std::process::exit(23);
    }

    let run_payload: RunPayloadFunc = core::mem::transmute(run_payload_ptr.unwrap());
    let exit_code = run_payload(payload_data.as_ptr(), payload_data.len());

    // Cleanup: unload DLL, delete temp file
    FreeLibrary(h_module);
    DeleteFileW(temp_file_path.as_ptr());
    std::process::exit(exit_code as i32);
}
