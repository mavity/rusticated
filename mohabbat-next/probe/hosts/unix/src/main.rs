use std::env;
use std::ffi::{c_char, c_void, OsString};
use std::fs::{self, File};
use std::io::{self, Read, Seek, SeekFrom};
use std::path::PathBuf;
use std::process;

use ruzstd::frame_decoder::{BlockDecodingStrategy, FrameDecoder};
use ruzstd::io::Read as RuzstdRead;

const COMPONENT_LIMIT: usize = 4096;
const COMPRESSED_COMPONENT_LIMIT: usize = 8192;
const PROT_READ: i32 = 1;
const PROT_WRITE: i32 = 2;
const PROT_EXEC: i32 = 4;
const MAP_PRIVATE: i32 = 2;
#[cfg(any(target_os = "linux", target_os = "android"))]
const MAP_ANONYMOUS: i32 = 0x20;
#[cfg(target_os = "macos")]
const MAP_ANONYMOUS: i32 = 0x1000;
#[cfg(target_os = "macos")]
const MAP_JIT: i32 = 0x800;

#[repr(C)]
struct UnixPlatformHost {
    load_library: Option<unsafe extern "C" fn(*const c_char, i32) -> *mut c_void>,
    get_proc_address: Option<unsafe extern "C" fn(*mut c_void, *const c_char) -> *mut c_void>,
    alloc: Option<unsafe extern "C" fn(usize) -> *mut c_void>,
    free: Option<unsafe extern "C" fn(*mut c_void, usize)>,
    flush_cache: Option<unsafe extern "C" fn(*mut c_void, usize)>,
    set_jit_write_protect: Option<unsafe extern "C" fn(i32)>,
    hello_payload: *const u8,
    hello_payload_size: usize,
}

type PlatformEntry = unsafe extern "C" fn(*const UnixPlatformHost) -> i32;

#[cfg(any(target_os = "linux", target_os = "android"))]
#[link(name = "dl")]
unsafe extern "C" {
    fn dlopen(filename: *const c_char, flags: i32) -> *mut c_void;
    fn dlsym(handle: *mut c_void, symbol: *const c_char) -> *mut c_void;
}

#[cfg(target_os = "macos")]
unsafe extern "C" {
    fn dlopen(filename: *const c_char, flags: i32) -> *mut c_void;
    fn dlsym(handle: *mut c_void, symbol: *const c_char) -> *mut c_void;
    fn pthread_jit_write_protect_np(enabled: i32);
    fn sys_icache_invalidate(start: *const c_void, length: usize);
}

unsafe extern "C" {
    fn mmap(
        address: *mut c_void,
        length: usize,
        protection: i32,
        flags: i32,
        descriptor: i32,
        offset: i64,
    ) -> *mut c_void;
    fn munmap(address: *mut c_void, length: usize) -> i32;
}

unsafe extern "C" fn load_library(name: *const c_char, flags: i32) -> *mut c_void {
    unsafe { dlopen(name, flags) }
}

unsafe extern "C" fn get_proc_address(module: *mut c_void, name: *const c_char) -> *mut c_void {
    unsafe { dlsym(module, name) }
}

unsafe extern "C" fn alloc_executable(size: usize) -> *mut c_void {
    #[cfg(target_os = "macos")]
    let flags = MAP_PRIVATE | MAP_ANONYMOUS | MAP_JIT;
    #[cfg(not(target_os = "macos"))]
    let flags = MAP_PRIVATE | MAP_ANONYMOUS;
    let memory = unsafe {
        mmap(
            std::ptr::null_mut(),
            size,
            PROT_READ | PROT_WRITE | PROT_EXEC,
            flags,
            -1,
            0,
        )
    };
    if memory as isize == -1 {
        std::ptr::null_mut()
    } else {
        memory
    }
}

unsafe extern "C" fn free_executable(memory: *mut c_void, size: usize) {
    if !memory.is_null() {
        unsafe {
            munmap(memory, size);
        }
    }
}

unsafe extern "C" fn flush_instruction_cache(memory: *mut c_void, size: usize) {
    #[cfg(all(target_arch = "aarch64", not(target_os = "macos")))]
    unsafe {
        let start = memory as usize;
        let end = start.saturating_add(size);
        let ctr: u64;
        core::arch::asm!("mrs {0}, ctr_el0", out(reg) ctr);
        let data_line = 4usize << ((ctr >> 16) & 0xf);
        let instruction_line = 4usize << (ctr & 0xf);
        let mut address = start & !(data_line - 1);
        while address < end {
            core::arch::asm!("dc cvau, {0}", in(reg) address, options(nostack));
            address += data_line;
        }
        core::arch::asm!("dsb ish", options(nostack));
        address = start & !(instruction_line - 1);
        while address < end {
            core::arch::asm!("ic ivau, {0}", in(reg) address, options(nostack));
            address += instruction_line;
        }
        core::arch::asm!("dsb ish", "isb", options(nostack));
    }
    #[cfg(all(target_os = "macos", target_arch = "aarch64"))]
    unsafe {
        sys_icache_invalidate(memory, size);
    }
    #[cfg(target_arch = "x86_64")]
    let _ = (memory, size);
}

#[cfg(target_os = "macos")]
unsafe extern "C" fn set_jit_write_protect(enabled: i32) {
    unsafe { pthread_jit_write_protect_np(enabled) }
}

fn decompress(frame: &[u8]) -> Result<Vec<u8>, String> {
    let mut input = frame;
    let mut decoder = FrameDecoder::new();
    decoder
        .init(&mut input)
        .map_err(|error| format!("invalid Zstandard frame: {error:?}"))?;

    let mut output = Vec::new();
    let mut chunk = [0u8; 512];
    loop {
        decoder
            .decode_blocks(&mut input, BlockDecodingStrategy::UptoBlocks(1))
            .map_err(|error| format!("Zstandard decompression failed: {error:?}"))?;
        while decoder.can_collect() > 0 {
            let count = decoder
                .read(&mut chunk)
                .map_err(|error| format!("Zstandard output failed: {error:?}"))?;
            if count == 0 {
                break;
            }
            if output.len().saturating_add(count) > COMPONENT_LIMIT {
                return Err("decompressed component exceeds the 4 KiB limit".into());
            }
            output.extend_from_slice(&chunk[..count]);
        }
        if decoder.is_finished() {
            if !input.is_empty() {
                return Err("trailing bytes after Zstandard frame".into());
            }
            break;
        }
        if input.is_empty() && decoder.can_collect() == 0 {
            return Err("truncated Zstandard frame".into());
        }
    }
    if output.is_empty() {
        return Err("decompressed component is empty".into());
    }
    Ok(output)
}

fn read_component(file: &mut File, offset: u64, length: usize) -> Result<Vec<u8>, String> {
    if length == 0 || length > COMPRESSED_COMPONENT_LIMIT {
        return Err(format!("invalid compressed component length: {length}"));
    }
    file.seek(SeekFrom::Start(offset))
        .map_err(|error| format!("seek bundle: {error}"))?;
    let mut data = vec![0; length];
    file.read_exact(&mut data)
        .map_err(|error| format!("read bundle component: {error}"))?;
    decompress(&data)
}

fn parse_range(offset: &OsString, length: &OsString) -> Result<(u64, usize), String> {
    let offset = offset
        .to_str()
        .ok_or("component offset is not valid UTF-8")?
        .parse::<u64>()
        .map_err(|_| "invalid component offset")?;
    let length = length
        .to_str()
        .ok_or("component length is not valid UTF-8")?
        .parse::<usize>()
        .map_err(|_| "invalid component length")?;
    if length == 0 || length > COMPRESSED_COMPONENT_LIMIT {
        return Err("component length is outside the supported range".into());
    }
    Ok((offset, length))
}

fn run(mut args: impl Iterator<Item = OsString>) -> Result<i32, String> {
    let executable = args.next().ok_or("missing executable path")?;
    fs::remove_file(PathBuf::from(executable))
        .map_err(|error| format!("unlink temporary Brot executable: {error}"))?;

    let bundle = args.next().ok_or(
        "usage: brot <bundle> <platform-offset> <platform-length> <hello-offset> <hello-length>",
    )?;
    let platform_offset = args.next().ok_or("missing platform offset")?;
    let platform_length = args.next().ok_or("missing platform length")?;
    let hello_offset = args.next().ok_or("missing hello payload offset")?;
    let hello_length = args.next().ok_or("missing hello payload length")?;
    if args.next().is_some() {
        return Err("unexpected extra arguments".into());
    }
    let (platform_offset, platform_length) = parse_range(&platform_offset, &platform_length)?;
    let (hello_offset, hello_length) = parse_range(&hello_offset, &hello_length)?;

    let mut file = File::open(bundle).map_err(|error| format!("open bundle: {error}"))?;
    let bundle_size = file
        .metadata()
        .map_err(|error| format!("stat bundle: {error}"))?
        .len();
    for (offset, length) in [
        (platform_offset, platform_length),
        (hello_offset, hello_length),
    ] {
        let end = offset
            .checked_add(length as u64)
            .ok_or("component range overflows")?;
        if end > bundle_size {
            return Err("component range extends beyond the bundle".into());
        }
    }
    let platform = read_component(&mut file, platform_offset, platform_length)?;
    let hello = read_component(&mut file, hello_offset, hello_length)?;
    let host = UnixPlatformHost {
        load_library: Some(load_library),
        get_proc_address: Some(get_proc_address),
        alloc: Some(alloc_executable),
        free: Some(free_executable),
        flush_cache: Some(flush_instruction_cache),
        #[cfg(target_os = "macos")]
        set_jit_write_protect: Some(set_jit_write_protect),
        #[cfg(not(target_os = "macos"))]
        set_jit_write_protect: None,
        hello_payload: hello.as_ptr(),
        hello_payload_size: hello.len(),
    };

    let platform_code = unsafe { alloc_executable(platform.len()) };
    if platform_code.is_null() {
        return Err("allocate executable memory for platform abstraction failed".into());
    }
    #[cfg(target_os = "macos")]
    unsafe {
        set_jit_write_protect(0);
    }
    unsafe {
        std::ptr::copy_nonoverlapping(platform.as_ptr(), platform_code.cast(), platform.len());
        flush_instruction_cache(platform_code, platform.len());
    }
    #[cfg(target_os = "macos")]
    unsafe {
        set_jit_write_protect(1);
    }

    let entry: PlatformEntry = unsafe { std::mem::transmute(platform_code) };
    let status = unsafe { entry(&host) };
    unsafe {
        free_executable(platform_code, platform.len());
    }
    Err(format!(
        "Unix platform abstraction returned unexpectedly with status {status}"
    ))
}

fn main() {
    let status = match run(env::args_os()) {
        Ok(status) => status,
        Err(error) => {
            let _ = writeln_stderr(&error);
            1
        }
    };
    process::exit(status);
}

fn writeln_stderr(message: &str) -> io::Result<()> {
    use std::io::Write;
    writeln!(io::stderr().lock(), "{message}")
}
