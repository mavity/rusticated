#![allow(unsafe_op_in_unsafe_fn)]
use std::ptr::null_mut;
use windows_sys::Win32::Foundation::*;
use windows_sys::Win32::Storage::FileSystem::*;
use windows_sys::Win32::System::Diagnostics::Debug::*;
use windows_sys::Win32::System::Environment::*;
use windows_sys::Win32::System::LibraryLoader::*;
use windows_sys::Win32::System::Memory::*;
use windows_sys::Win32::System::Pipes::*;
use windows_sys::Win32::System::Threading::*;

#[repr(C)]
pub struct FakePeb {
    pub reserved1: [u8; 16],
    pub image_base: *mut u8,
    pub ldr: *mut core::ffi::c_void,
    pub process_parameters: *mut core::ffi::c_void,
}

#[repr(C)]
pub struct IMAGE_DOS_HEADER {
    pub e_magic: u16,
    pub e_cblp: u16,
    pub e_cp: u16,
    pub e_crlc: u16,
    pub e_cparhdr: u16,
    pub e_minalloc: u16,
    pub e_maxalloc: u16,
    pub e_ss: u16,
    pub e_sp: u16,
    pub e_csum: u16,
    pub e_ip: u16,
    pub e_cs: u16,
    pub e_lfarlc: u16,
    pub e_ovno: u16,
    pub e_res: [u16; 4],
    pub e_oemid: u16,
    pub e_oeminfo: u16,
    pub e_res2: [u16; 10],
    pub e_lfanew: i32,
}

#[repr(C)]
pub struct IMAGE_IMPORT_DESCRIPTOR {
    pub OriginalFirstThunk: u32,
    pub TimeDateStamp: u32,
    pub ForwarderChain: u32,
    pub Name: u32,
    pub FirstThunk: u32,
}

pub unsafe fn reflective_load_and_run(washmhost: &[u8], payload: &[u8]) -> ! {
    let dos_header = &*(washmhost.as_ptr() as *const IMAGE_DOS_HEADER);
    let lfanew = dos_header.e_lfanew as usize;
    let nt_headers = &*(washmhost.as_ptr().add(lfanew) as *const IMAGE_NT_HEADERS64);
    let opt_hdr = &nt_headers.OptionalHeader;

    let size_of_image = opt_hdr.SizeOfImage as usize;

    // Allocate the region + an extra page for the code cave
    let image_base = VirtualAlloc(
        null_mut(),
        size_of_image + 0x1000,
        MEM_RESERVE | MEM_COMMIT,
        PAGE_READWRITE,
    );
    if image_base.is_null() {
        std::process::exit(50);
    }

    // Copy Headers
    core::ptr::copy_nonoverlapping(
        washmhost.as_ptr(),
        image_base as *mut _,
        opt_hdr.SizeOfHeaders as usize,
    );

    // Copy Sections
    let num_sections = nt_headers.FileHeader.NumberOfSections as usize;
    let sec_offset = lfanew + core::mem::size_of::<IMAGE_NT_HEADERS64>();
    let sections = core::slice::from_raw_parts(
        washmhost.as_ptr().add(sec_offset) as *const IMAGE_SECTION_HEADER,
        num_sections,
    );

    for s in sections {
        if s.SizeOfRawData > 0 {
            let dest = image_base.cast::<u8>().add(s.VirtualAddress as usize);
            let src = washmhost.as_ptr().add(s.PointerToRawData as usize);
            let size = core::cmp::min(s.SizeOfRawData, s.Misc.VirtualSize) as usize;
            core::ptr::copy_nonoverlapping(src, dest, size);
        }
    }

    // Relocations
    let delta = image_base as isize - opt_hdr.ImageBase as isize;
    if delta != 0 {
        let reloc_dir = opt_hdr.DataDirectory[IMAGE_DIRECTORY_ENTRY_BASERELOC as usize];
        if reloc_dir.Size > 0 {
            let mut curr = image_base
                .cast::<u8>()
                .add(reloc_dir.VirtualAddress as usize);
            let end = curr.add(reloc_dir.Size as usize);
            while curr < end {
                let rva = *(curr.cast::<u32>());
                let size = *(curr.add(4).cast::<u32>());
                if size == 0 {
                    break;
                }
                let entries_count = (size - 8) / 2;
                let entries =
                    core::slice::from_raw_parts(curr.add(8).cast::<u16>(), entries_count as usize);

                for &entry in entries {
                    let rel_type = entry >> 12;
                    let offset = entry & 0x0FFF;
                    if rel_type == 10 {
                        // IMAGE_REL_BASED_DIR64
                        let target = image_base
                            .cast::<u8>()
                            .add(rva as usize + offset as usize)
                            .cast::<u64>();
                        *target = (*target as isize + delta) as u64;
                    }
                }
                curr = curr.add(size as usize);
            }
        }
    }

    // Imports
    let import_dir = opt_hdr.DataDirectory[IMAGE_DIRECTORY_ENTRY_IMPORT as usize];
    if import_dir.Size > 0 {
        let mut curr = image_base
            .cast::<u8>()
            .add(import_dir.VirtualAddress as usize)
            as *const IMAGE_IMPORT_DESCRIPTOR;
        while (*curr).Name != 0 {
            let dll_name_ptr = image_base.cast::<u8>().add((*curr).Name as usize);
            let mut dll_name = String::new();
            let mut p = dll_name_ptr;
            while *p != 0 {
                dll_name.push(*p as char);
                p = p.add(1);
            }

            let dll_name_zero = format!("{}\0", dll_name);
            let h_lib = LoadLibraryA(dll_name_zero.as_ptr());
            if h_lib.is_null() {
                std::process::exit(51);
            }

            let mut thunk = if (*curr).OriginalFirstThunk != 0 {
                image_base
                    .cast::<u8>()
                    .add((*curr).OriginalFirstThunk as usize)
                    .cast::<u64>()
            } else {
                image_base
                    .cast::<u8>()
                    .add((*curr).FirstThunk as usize)
                    .cast::<u64>()
            };
            let mut func = image_base
                .cast::<u8>()
                .add((*curr).FirstThunk as usize)
                .cast::<u64>();

            while *thunk != 0 {
                let thunk_data = *thunk;
                let proc_addr = if (thunk_data & 0x8000000000000000) != 0 {
                    let ordinal = thunk_data & 0xFFFF;
                    GetProcAddress(h_lib, ordinal as *const u8)
                } else {
                    let name_rva = (thunk_data & 0x7FFFFFFF) as usize;
                    let import_name_ptr = image_base.cast::<u8>().add(name_rva + 2);
                    GetProcAddress(h_lib, import_name_ptr)
                };

                if proc_addr.is_none() {
                    std::process::exit(52);
                }

                *func = proc_addr.unwrap() as u64;
                thunk = thunk.add(1);
                func = func.add(1);
            }
            curr = curr.add(1);
        }
    }

    // Set up named pipe and env
    let pid = GetCurrentProcessId();
    let pipe_name = format!(r"\\.\pipe\mohabbat-wasm-{pid}");
    let mut pipe_name_w: Vec<u16> = pipe_name.encode_utf16().collect();
    pipe_name_w.push(0);

    let h_pipe = CreateNamedPipeW(
        pipe_name_w.as_ptr(),
        PIPE_ACCESS_OUTBOUND,
        PIPE_TYPE_BYTE | PIPE_WAIT,
        1,
        64 * 1024,
        0,
        0,
        null_mut(),
    );
    if h_pipe == INVALID_HANDLE_VALUE {
        std::process::exit(53);
    }

    // Set environment variable (updates the process' actual PEB params)
    let h = "MOHABBAT_WASM_FD\0".encode_utf16().collect::<Vec<_>>();
    SetEnvironmentVariableW(h.as_ptr(), pipe_name_w.as_ptr());

    // Send payload on background thread
    let pl = payload.to_vec();
    let pipe_handle = h_pipe as usize;
    std::thread::spawn(move || {
        let h = pipe_handle as HANDLE;
        ConnectNamedPipe(h, null_mut());
        let mut written = 0;
        WriteFile(h, pl.as_ptr(), pl.len() as u32, &mut written, null_mut());
        CloseHandle(h);
    });

    // Code cave for FakePeb and patching
    // Get PEB
    #[cfg(target_arch = "x86_64")]
    let real_peb: *mut u8 = {
        let peb: *mut u8;
        core::arch::asm!(
            "mov {}, gs:[0x60]",
            out(reg) peb,
        );
        peb
    };

    #[cfg(target_arch = "aarch64")]
    let real_peb: *mut u8 = {
        let peb: *mut u8;
        core::arch::asm!(
            "mrs x0, tpidr_el0",
            "ldr {}, [x0, #0x60]",
            out(reg) peb,
            out("x0") _,
        );
        peb
    };

    let ldr = *(real_peb.add(0x18).cast::<*mut core::ffi::c_void>());
    let proc_params = *(real_peb.add(0x20).cast::<*mut core::ffi::c_void>());

    // We store FakePeb exactly at image_base + size_of_image + 0x800
    let fake_peb_ptr = image_base
        .cast::<u8>()
        .add(size_of_image + 0x800)
        .cast::<FakePeb>();
    *fake_peb_ptr = FakePeb {
        reserved1: [0; 16],
        image_base: image_base as *mut _,
        ldr,
        process_parameters: proc_params,
    };

    // Hot-patch PEB loads
    #[cfg(target_arch = "x86_64")]
    {
        // For x86_64, scan for `mov rax, gs:[0x60]` = 65 48 8b 04 25 60 00 00 00
        let sig = [0x65u8, 0x48, 0x8B, 0x04, 0x25, 0x60, 0x00, 0x00, 0x00];
        let text_sec = sections.iter().find(|s| &s.Name[..5] == b".text").unwrap();
        let text_start = image_base
            .cast::<u8>()
            .add(text_sec.VirtualAddress as usize);
        let text_size = text_sec.Misc.VirtualSize as usize;

        let mut i = 0;
        while i < text_size - 9 {
            let slice = core::slice::from_raw_parts(text_start.add(i), 9);
            if slice == sig {
                let ins_ptr = text_start.add(i);
                // MOV RAX, [RIP + offset]
                // 48 8B 05 xx xx xx xx
                let offset = (fake_peb_ptr as isize - (ins_ptr as isize + 7)) as i32;
                *ins_ptr.add(0) = 0x48;
                *ins_ptr.add(1) = 0x8B;
                *ins_ptr.add(2) = 0x05;
                *(ins_ptr.add(3).cast::<i32>()) = offset;
                *ins_ptr.add(7) = 0x90; // NOP
                *ins_ptr.add(8) = 0x90; // NOP
            }
            i += 1;
        }
    }

    #[cfg(target_arch = "aarch64")]
    {
        // For aarch64, scan for MRS X0, TPIDR_EL0 (D0 3B 38 D5 or D0 3B 3B D5? Actually D53BD040 generally for MRS x0, tpidr_el0)
        // And LDR X1, [X0, #0x60] -> F9403001
        // Rather than simple byte matching, `bad64` could be used.
        // Let's do simple byte matching for standard Go code:
        // mrs x0, tpidr_el0 is 0xd53bd040
        // ldr *, [x0, #0x60] is 0xf9403000 + rt
        let text_sec = sections.iter().find(|s| &s.Name[..5] == b".text").unwrap();
        let text_start = image_base
            .cast::<u8>()
            .add(text_sec.VirtualAddress as usize)
            .cast::<u32>();
        let text_size = text_sec.Misc.VirtualSize as usize / 4;

        let mut i = 0;
        while i < text_size - 1 {
            let inst1 = *text_start.add(i);
            let inst2 = *text_start.add(i + 1);
            if inst1 == 0xd53bd040 && (inst2 & 0xfffffc00) == 0xf9403000 {
                let target_reg = inst2 & 0x1f;

                // Replace with ADRP target_reg, offset
                // LDR target_reg, [target_reg, #offset]
                let fake_peb_addr = fake_peb_ptr as u64;
                let pc = text_start.add(i) as u64;
                let page_pc = pc & !0xfffu64;
                let page_fake = fake_peb_addr & !0xfffu64;
                let page_offset = ((page_fake as i64 - page_pc as i64) >> 12) as i32;

                let immlo = (page_offset & 3) << 29;
                let immhi = ((page_offset >> 2) & 0x7ffff) << 5;
                let adrp = 0x90000000 | (immlo as u32) | (immhi as u32) | target_reg;

                let pgoffset = (fake_peb_addr & 0xfff) as u32;
                let ldr = 0xf9400000 | ((pgoffset >> 3) << 10) | (target_reg << 5) | target_reg;

                *text_start.add(i) = adrp;
                *text_start.add(i + 1) = ldr;
            }
            i += 1;
        }
    }

    // Register exception handlers
    let pdata_dir = opt_hdr.DataDirectory[IMAGE_DIRECTORY_ENTRY_EXCEPTION as usize];
    if pdata_dir.Size > 0 {
        let pdata_ptr = image_base
            .cast::<u8>()
            .add(pdata_dir.VirtualAddress as usize);
        let pdata_count =
            pdata_dir.Size / core::mem::size_of::<IMAGE_RUNTIME_FUNCTION_ENTRY>() as u32;
        #[cfg(target_arch = "x86_64")]
        RtlAddFunctionTable(
            pdata_ptr as *const _,
            pdata_count,
            image_base as usize as u64,
        );
        #[cfg(target_arch = "aarch64")]
        RtlAddFunctionTable(pdata_ptr as *const _, pdata_count, image_base as usize);
    }

    // VirtualProtect
    for s in sections {
        if s.Misc.VirtualSize == 0 {
            continue;
        }
        let executable = (s.Characteristics & IMAGE_SCN_MEM_EXECUTE) != 0;
        let readable = (s.Characteristics & IMAGE_SCN_MEM_READ) != 0;
        let writable = (s.Characteristics & IMAGE_SCN_MEM_WRITE) != 0;

        let mut protect = PAGE_NOACCESS;
        if executable {
            if readable && writable {
                protect = PAGE_EXECUTE_READWRITE;
            } else if readable {
                protect = PAGE_EXECUTE_READ;
            } else if writable {
                protect = PAGE_EXECUTE_WRITECOPY;
            } else {
                protect = PAGE_EXECUTE;
            }
        } else {
            if readable && writable {
                protect = PAGE_READWRITE;
            } else if readable {
                protect = PAGE_READONLY;
            } else if writable {
                protect = PAGE_WRITECOPY;
            }
        }

        let mut old = 0;
        VirtualProtect(
            image_base
                .cast::<u8>()
                .add(s.VirtualAddress as usize)
                .cast(),
            s.Misc.VirtualSize as usize,
            protect,
            &mut old,
        );
    }

    let mut old = 0;
    // Unprotect the whole image (header) as READONLY? Not doing it, just leaving it.
    VirtualProtect(image_base, 0x1000, PAGE_READONLY, &mut old);

    FlushInstructionCache(GetCurrentProcess(), image_base, size_of_image);

    // Jump
    let entry_point = image_base
        .cast::<u8>()
        .add(opt_hdr.AddressOfEntryPoint as usize);
    let jump: extern "C" fn() = core::mem::transmute(entry_point);
    jump();

    std::process::exit(0);
}
