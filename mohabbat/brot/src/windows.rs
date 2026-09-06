use alloc::format;
use alloc::string::String;
use alloc::vec::Vec;
use core::ptr::null_mut;

use crate::win32::Win32::Foundation::*;
use crate::win32::Win32::Storage::FileSystem::*;

use crate::win32::Win32::System::Environment::{
    GetCommandLineW, SetEnvironmentVariableW,
};
use crate::win32::Win32::System::Threading::{
    CreateProcessW, ExitProcess, GetCurrentProcessId, GetExitCodeProcess, INFINITE,
    PROCESS_INFORMATION, STARTUPINFOW, WaitForSingleObject,
};
use crate::win32::Win32::UI::Shell::CommandLineToArgvW;

// Using the rust_eh_personality already provided by rusticated's lib.rs

fn parse_u16_u64(ptr: *const u16) -> Option<u64> {
    if ptr.is_null() {
        return None;
    }
    let mut start = 0;
    while unsafe { *ptr.offset(start) } == b' ' as u16 {
        start += 1;
    }
    let mut end = start;
    while unsafe { *ptr.offset(end) } != 0 {
        end += 1;
    }
    while end > start && unsafe { *ptr.offset(end - 1) } == b' ' as u16 {
        end -= 1;
    }
    if start == end {
        return None;
    }
    let mut res: u64 = 0;
    for i in start..end {
        let ch = unsafe { *ptr.offset(i) };
        if ch < b'0' as u16 || ch > b'9' as u16 {
            return None;
        }
        res = res.checked_mul(10)?.checked_add((ch - b'0' as u16) as u64)?;
    }
    Some(res)
}

pub unsafe fn run() -> ! {
    let mut num_args = 0;
    let argv = unsafe { CommandLineToArgvW(GetCommandLineW(), &mut num_args) };
    if argv.is_null() || num_args < 7 {
        unsafe { ExitProcess(101) };
    }

    let arg1_ptr = unsafe { *argv.offset(1) };
    let mut len = 0;
    while unsafe { *arg1_ptr.offset(len) } != 0 {
        len += 1;
    }
    let mut wide_path = alloc::vec![0u16; (len + 1) as usize];
    unsafe { core::ptr::copy_nonoverlapping(arg1_ptr, wide_path.as_mut_ptr(), len as usize) };
    wide_path[len as usize] = 0;

    let pool_len = parse_u16_u64(unsafe { *argv.offset(2) }).unwrap_or_else(|| unsafe { ExitProcess(103) }) as usize;
    let washmhost_offset = parse_u16_u64(unsafe { *argv.offset(3) }).unwrap_or_else(|| unsafe { ExitProcess(103) }) as usize;
    let washmhost_len = parse_u16_u64(unsafe { *argv.offset(4) }).unwrap_or_else(|| unsafe { ExitProcess(103) }) as usize;
    let payload_offset = parse_u16_u64(unsafe { *argv.offset(5) }).unwrap_or_else(|| unsafe { ExitProcess(103) }) as usize;
    let payload_len = parse_u16_u64(unsafe { *argv.offset(6) }).unwrap_or_else(|| unsafe { ExitProcess(103) }) as usize;

    unsafe {
        let handle = CreateFileW(
            wide_path.as_ptr(),
            FILE_READ_ATTRIBUTES | FILE_READ_DATA,
            FILE_SHARE_READ,
            core::ptr::null_mut(),
            OPEN_EXISTING,
            FILE_ATTRIBUTE_NORMAL,
            core::ptr::null_mut(),
        );

        if handle == INVALID_HANDLE_VALUE {
            ExitProcess(2);
        }

        let mut file_size: i64 = 0;
        if GetFileSizeEx(handle, &mut file_size) == 0 {
            ExitProcess(3);
        }

        if pool_len == 0 {
            crate::print_err("brot: pool_len is 0, nothing to do. exiting.\n");
            ExitProcess(4);
        }

        if file_size < pool_len as i64 {
            ExitProcess(5);
        }

        let mut distance: i64 = 0;
        if SetFilePointerEx(
            handle,
            file_size - pool_len as i64,
            &mut distance,
            FILE_BEGIN,
        ) == 0
        {
            ExitProcess(6);
        }

        let mut compressed_data = alloc::vec![0u8; pool_len];

        let mut total_read = 0;
        while total_read < pool_len {
            let mut n: u32 = 0;
            let to_read = core::cmp::min(pool_len - total_read, 0xFFFF_FFFF) as u32;
            if ReadFile(
                handle,
                compressed_data.as_mut_ptr().add(total_read) as *mut _,
                to_read,
                &mut n,
                null_mut(),
            ) == 0
            {
                ExitProcess(7);
            }
            if n == 0 {
                ExitProcess(8);
            }
            total_read += n as usize;
        }
        CloseHandle(handle);

        let total_pool = payload_offset + payload_len;
        let mut decompressed_pool = alloc::vec![0u8; total_pool];

        let mut out_offset = 0;
        let _ = crate::decompress::decompress_to_writer(&compressed_data, |chunk| {
            let end = out_offset + chunk.len();
            if end <= decompressed_pool.len() {
                decompressed_pool[out_offset..end].copy_from_slice(chunk);
                out_offset = end;
            }
        });

        let washmhost_data = &decompressed_pool
            [washmhost_offset..washmhost_offset + washmhost_len];
        let payload_data = &decompressed_pool
            [payload_offset..payload_offset + payload_len];

        let mut temp_path = alloc::vec![0u16; MAX_PATH as usize + 1];
        let len = GetTempPathW(temp_path.len() as u32, temp_path.as_mut_ptr());
        temp_path.truncate(len as usize);

        let temp_str = String::from_utf16_lossy(&temp_path);
        let pid = GetCurrentProcessId();

        let washmhost_exe = format!("{}m_hlp_{}.exe", temp_str, pid);
        let payload_wasm = format!("{}m_pld_{}.wasm", temp_str, pid);

        let mut washmhost_exe_w: Vec<u16> = washmhost_exe.encode_utf16().collect();
        washmhost_exe_w.push(0);
        let mut payload_wasm_w: Vec<u16> = payload_wasm.encode_utf16().collect();
        payload_wasm_w.push(0);

        for (path_w, data) in [
            (washmhost_exe_w.as_slice(), washmhost_data),
            (payload_wasm_w.as_slice(), payload_data),
        ] {
            let h = CreateFileW(
                path_w.as_ptr(),
                FILE_GENERIC_WRITE,
                0,
                core::ptr::null_mut(),
                CREATE_ALWAYS,
                FILE_ATTRIBUTE_NORMAL,
                core::ptr::null_mut(),
            );
            if h != INVALID_HANDLE_VALUE {
                let mut written = 0u32;
                WriteFile(
                    h,
                    data.as_ptr() as *const _,
                    data.len() as u32,
                    &mut written,
                    core::ptr::null_mut(),
                );
                CloseHandle(h);
            }
        }

        let env_name: Vec<u16> = "MOHABBAT_WASM_FD\0".encode_utf16().collect();
        SetEnvironmentVariableW(env_name.as_ptr(), payload_wasm_w.as_ptr());

        let vegetable_str = String::from_utf16_lossy(&wide_path);
        let mut cmd_str = format!("\"{}\"", vegetable_str.trim_end_matches('\0'));

        if num_args > 7 {
            for i in 7..num_args {
                let arg_ptr = *argv.offset(i as isize);
                let mut len = 0;
                while *arg_ptr.offset(len) != 0 {
                    len += 1;
                }
                let mut arg_wide = alloc::vec![0u16; len as usize];
                core::ptr::copy_nonoverlapping(arg_ptr, arg_wide.as_mut_ptr(), len as usize);
                let arg_str = String::from_utf16_lossy(&arg_wide);
                cmd_str.push(' ');
                if arg_str.contains(' ') {
                    cmd_str.push('"');
                    cmd_str.push_str(&arg_str);
                    cmd_str.push('"');
                } else {
                    cmd_str.push_str(&arg_str);
                }
            }
        }

        #[cfg(feature = "verbose")]
        crate::print_err("brot: spawning washmhost...\n");

        let mut startup_info: STARTUPINFOW = core::mem::zeroed();
        startup_info.cb = core::mem::size_of::<STARTUPINFOW>() as u32;
        let mut process_info: PROCESS_INFORMATION = core::mem::zeroed();

        let mut cmdline: Vec<u16> = cmd_str.encode_utf16().collect();
        cmdline.push(0);

        use crate::win32::Win32::System::Threading::{
            CreateJobObjectW, SetInformationJobObject, AssignProcessToJobObject, ResumeThread,
            JOBOBJECT_EXTENDED_LIMIT_INFORMATION, JOB_OBJECT_EXTENDED_LIMIT_INFORMATION,
            JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, CREATE_SUSPENDED
        };

        // Create job object to enforce shutdown of washmhost if brot dies
        let h_job = CreateJobObjectW(core::ptr::null_mut(), core::ptr::null());
        if !h_job.is_null() && h_job != crate::win32::Win32::Foundation::INVALID_HANDLE_VALUE {
            let mut jeli: JOBOBJECT_EXTENDED_LIMIT_INFORMATION = core::mem::zeroed();
            jeli.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
            SetInformationJobObject(
                h_job,
                JOB_OBJECT_EXTENDED_LIMIT_INFORMATION,
                &mut jeli as *mut _ as *mut core::ffi::c_void,
                core::mem::size_of::<JOBOBJECT_EXTENDED_LIMIT_INFORMATION>() as u32,
            );
        }

        let res = CreateProcessW(
            washmhost_exe_w.as_ptr(),
            cmdline.as_mut_ptr(),
            core::ptr::null_mut(),
            core::ptr::null_mut(),
            0,
            CREATE_SUSPENDED,
            core::ptr::null_mut(),
            core::ptr::null_mut(),
            &mut startup_info,
            &mut process_info,
        );

        let mut exit_code: u32 = 1;
        if res != 0 {
            if !h_job.is_null() && h_job != crate::win32::Win32::Foundation::INVALID_HANDLE_VALUE {
                AssignProcessToJobObject(h_job, process_info.hProcess);
            }
            ResumeThread(process_info.hThread);

            #[cfg(feature = "verbose")]
            crate::print_err("brot: washmhost spawned, waiting...\n");
            WaitForSingleObject(process_info.hProcess, INFINITE);
            GetExitCodeProcess(process_info.hProcess, &mut exit_code);
            CloseHandle(process_info.hProcess);
            CloseHandle(process_info.hThread);
        } else {
            use crate::win32::Win32::Foundation::GetLastError;
            let err = GetLastError();
            crate::print_err(&format!(
                "brot: failed to spawn washmhost (error {})\n",
                err
            ));
        }

        DeleteFileW(washmhost_exe_w.as_ptr());
        DeleteFileW(payload_wasm_w.as_ptr());

        ExitProcess(exit_code);
    }
}
