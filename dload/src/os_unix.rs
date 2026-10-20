use alloc::string::String;
use alloc::vec::Vec;

unsafe extern "C" {
    fn dlopen(path: *const u8, flags: i32) -> usize;
    fn dlsym(handle: usize, symbol: *const u8) -> usize;
    fn dlclose(handle: usize) -> i32;
    fn dlerror() -> *const u8;
    #[link_name = "malloc"]
    fn c_malloc(size: usize) -> usize;
    #[link_name = "free"]
    fn c_free(ptr: usize);
}

fn last_dlerror() -> String {
    let p = unsafe { dlerror() };
    if p.is_null() {
        return String::from("unknown dynamic linker error");
    }
    let mut len = 0;
    while unsafe { *p.add(len) } != 0 {
        len += 1;
    }
    let slice = unsafe { core::slice::from_raw_parts(p, len) };
    String::from(core::str::from_utf8(slice).unwrap_or("non-utf8 dlerror"))
}

pub fn open(path: &str, mut flags: i32) -> Result<u64, String> {
    if flags == 0 {
        flags = 2; // RTLD_NOW
    }
    let mut c_path = Vec::from(path.as_bytes());
    c_path.push(0);

    // clear any pending dlerror
    unsafe { dlerror() };

    let h = unsafe { dlopen(c_path.as_ptr(), flags) };
    if h == 0 {
        return Err(last_dlerror());
    }
    Ok(h as u64)
}

pub fn sym(handle: u64, name: &str) -> Result<u64, String> {
    let mut c_name = Vec::from(name.as_bytes());
    c_name.push(0);

    unsafe { dlerror() };

    let s = unsafe { dlsym(handle as usize, c_name.as_ptr()) };
    if s == 0 {
        return Err(last_dlerror());
    }
    Ok(s as u64)
}

pub fn close(handle: u64) -> Result<(), String> {
    unsafe { dlerror() };
    let rc = unsafe { dlclose(handle as usize) };
    if rc != 0 {
        return Err(last_dlerror());
    }
    Ok(())
}

pub fn alloc(size: u64) -> Result<u64, String> {
    let p = unsafe { c_malloc(size.max(1) as usize) };
    if p == 0 {
        return Err(String::from("malloc failed"));
    }
    Ok(p as u64)
}

pub fn free(ptr: u64) -> Result<(), String> {
    if ptr != 0 {
        unsafe { c_free(ptr as usize) };
    }
    Ok(())
}

pub fn read_mem(ptr: u64, len: u32) -> Result<Vec<u8>, String> {
    if ptr < 4096 {
        return Err(String::from("invalid memory address"));
    }
    let mut out = alloc::vec![0u8; len as usize];
    unsafe {
        core::ptr::copy_nonoverlapping(ptr as *const u8, out.as_mut_ptr(), len as usize);
    }
    Ok(out)
}

pub fn write_mem(ptr: u64, data: &[u8]) -> Result<(), String> {
    if ptr < 4096 {
        return Err(String::from("invalid memory address"));
    }
    unsafe {
        core::ptr::copy_nonoverlapping(data.as_ptr(), ptr as *mut u8, data.len());
    }
    Ok(())
}
