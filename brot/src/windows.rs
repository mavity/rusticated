#![allow(unsafe_op_in_unsafe_fn)]
use crate::META;
use std::ptr::null_mut;

use windows_sys::Win32::Foundation::*;
use windows_sys::Win32::Storage::FileSystem::*;

type RunPayloadFunc = unsafe extern "C" fn(*const u8, usize) -> u32;

use windows_sys::Win32::System::Environment::{GetCommandLineW, GetEnvironmentVariableW};
use windows_sys::Win32::UI::Shell::CommandLineToArgvW;

// Using the rust_eh_personality already provided by rusticated's lib.rs

pub unsafe fn get_module_file_name() -> Vec<u16> {
    let mut num_args = 0;
    let argv = CommandLineToArgvW(GetCommandLineW(), &mut num_args);
    if argv == core::ptr::null_mut() || num_args < 1 {
        std::process::exit(101);
    }

    let arg0_ptr = *argv.offset(0);
    let mut len = 0;
    while *arg0_ptr.offset(len) != 0 {
        len += 1;
    }

    let mut wide_path = vec![0u16; (len + 1) as usize];
    core::ptr::copy_nonoverlapping(arg0_ptr, wide_path.as_mut_ptr(), len as usize);
    wide_path[len as usize] = 0;

    wide_path
}

pub unsafe fn get_vegetable_file_name() -> Vec<u16> {
    let mut buffer = vec![0u16; 32768];
    let env_var = "MOHABBAT_VEGETABLE_PATH\0"
        .encode_utf16()
        .collect::<Vec<u16>>();
    let len = GetEnvironmentVariableW(env_var.as_ptr(), buffer.as_mut_ptr(), buffer.len() as u32);
    if len > 0 && (len as usize) < buffer.len() {
        buffer.truncate(len as usize + 1);
        buffer[len as usize] = 0; // null terminator
        return buffer;
    }

    get_module_file_name()
}

pub unsafe fn run() {
    let wide_path = get_vegetable_file_name();

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

    extern crate alloc;
    use alloc::format;
    use alloc::string::String;

    let mut temp_path = vec![0u16; MAX_PATH as usize + 1];
    let len = GetTempPathW(temp_path.len() as u32, temp_path.as_mut_ptr());
    temp_path.truncate(len as usize);

    // convert temp_path to String
    let temp_str = String::from_utf16_lossy(&temp_path);
    let pid = windows_sys::Win32::System::Threading::GetCurrentProcessId();

    let washmhost_exe = format!("{}washm_tmp_{}.exe", temp_str, pid);
    let payload_wasm = format!("{}payload_tmp_{}.wasm", temp_str, pid);

    let mut washmhost_exe_w: Vec<u16> = washmhost_exe.encode_utf16().collect();
    washmhost_exe_w.push(0);
    let mut payload_wasm_w: Vec<u16> = payload_wasm.encode_utf16().collect();
    payload_wasm_w.push(0);

    for (path_w, data) in [
        (&washmhost_exe_w, washmhost_data),
        (&payload_wasm_w, payload_data),
    ] {
        let handle = CreateFileW(
            path_w.as_ptr(),
            windows_sys::Win32::Storage::FileSystem::FILE_GENERIC_WRITE,
            0,
            core::ptr::null(),
            CREATE_ALWAYS,
            FILE_ATTRIBUTE_NORMAL,
            core::ptr::null_mut(),
        );
        if handle != INVALID_HANDLE_VALUE {
            let mut written = 0;
            WriteFile(
                handle,
                data.as_ptr() as *const _,
                data.len() as u32,
                &mut written,
                core::ptr::null_mut(),
            );
            CloseHandle(handle);
        }
    }

    // Set Environment Variable MOHABBAT_WASM_FD
    let env_name: Vec<u16> = "MOHABBAT_WASM_FD\0".encode_utf16().collect();
    windows_sys::Win32::System::Environment::SetEnvironmentVariableW(
        env_name.as_ptr(),
        payload_wasm_w.as_ptr(),
    );

    // Create process using CreateProcessW
    let mut startup_info: windows_sys::Win32::System::Threading::STARTUPINFOW =
        unsafe { core::mem::zeroed() };
    startup_info.cb =
        core::mem::size_of::<windows_sys::Win32::System::Threading::STARTUPINFOW>() as u32;
    let mut process_info: windows_sys::Win32::System::Threading::PROCESS_INFORMATION =
        unsafe { core::mem::zeroed() };

    let vegetable_str = String::from_utf16_lossy(&wide_path);
    let mut cmd_str = format!("\"{}\"", vegetable_str.trim_end_matches('\0'));

    let mut num_args = 0;
    let argv = CommandLineToArgvW(GetCommandLineW(), &mut num_args);
    if argv != core::ptr::null_mut() && num_args > 1 {
        for i in 1..num_args {
            let arg_ptr = *argv.offset(i as isize);
            let mut len = 0;
            while *arg_ptr.offset(len) != 0 {
                len += 1;
            }
            let mut arg_wide = vec![0u16; len as usize];
            core::ptr::copy_nonoverlapping(arg_ptr, arg_wide.as_mut_ptr(), len as usize);
            let arg_str = String::from_utf16_lossy(&arg_wide);
            cmd_str.push_str(" ");
            if arg_str.contains(' ') {
                cmd_str.push('"');
                cmd_str.push_str(&arg_str);
                cmd_str.push('"');
            } else {
                cmd_str.push_str(&arg_str);
            }
        }
    }
    cmd_str.push('\0');

    let mut cmdline: Vec<u16> = cmd_str.encode_utf16().collect();

    let res = windows_sys::Win32::System::Threading::CreateProcessW(
        washmhost_exe_w.as_ptr(),
        cmdline.as_mut_ptr(),
        core::ptr::null(),
        core::ptr::null(),
        0,
        0,
        core::ptr::null(),
        core::ptr::null(),
        &startup_info,
        &mut process_info,
    );

    let mut exit_code = 1;
    if res != 0 {
        windows_sys::Win32::System::Threading::WaitForSingleObject(
            process_info.hProcess,
            windows_sys::Win32::System::Threading::INFINITE,
        );
        windows_sys::Win32::System::Threading::GetExitCodeProcess(
            process_info.hProcess,
            &mut exit_code,
        );
        CloseHandle(process_info.hProcess);
        CloseHandle(process_info.hThread);
    }

    DeleteFileW(washmhost_exe_w.as_ptr());
    DeleteFileW(payload_wasm_w.as_ptr());

    std::process::exit(exit_code as i32);
}
