use alloc::string::String;
use alloc::vec::Vec;

#[link(name = "kernel32", kind = "raw-dylib")]
unsafe extern "system" {
    fn LoadLibraryW(lpLibFileName: *const u16) -> usize;
    fn GetProcAddress(hModule: usize, lpProcName: *const u8) -> usize;
    fn FreeLibrary(hModule: usize) -> i32;
    fn SetDllDirectoryW(lpPathName: *const u16) -> i32;
    fn GetLastError() -> u32;
    fn GetProcessHeap() -> *mut core::ffi::c_void;
    fn HeapAlloc(
        hHeap: *mut core::ffi::c_void,
        dwFlags: u32,
        dwBytes: usize,
    ) -> *mut core::ffi::c_void;
    fn HeapFree(
        hHeap: *mut core::ffi::c_void,
        dwFlags: u32,
        lpMem: *mut core::ffi::c_void,
    ) -> i32;
}

fn to_wide(s: &str) -> Vec<u16> {
    let mut v: Vec<u16> = s.encode_utf16().collect();
    v.push(0);
    v
}

pub fn open(path: &str, _flags: i32) -> Result<u64, String> {
    // Set DLL directory to parent directory of path so dependencies are found
    if let Some(pos) = path.rfind(['/', '\\']) {
        let parent = &path[..pos];
        let w_parent = to_wide(parent);
        unsafe { SetDllDirectoryW(w_parent.as_ptr()) };
    }

    let w_path = to_wide(path);
    let h = unsafe { LoadLibraryW(w_path.as_ptr()) };
    if h == 0 {
        let err = unsafe { GetLastError() };
        return Err(alloc::format!("LoadLibraryW failed with error code {}", err));
    }
    Ok(h as u64)
}

pub fn sym(handle: u64, name: &str) -> Result<u64, String> {
    let mut c_name = Vec::from(name.as_bytes());
    c_name.push(0);
    let s = unsafe { GetProcAddress(handle as usize, c_name.as_ptr()) };
    if s == 0 {
        let err = unsafe { GetLastError() };
        return Err(alloc::format!("GetProcAddress('{}') failed with error code {}", name, err));
    }
    Ok(s as u64)
}

pub fn close(handle: u64) -> Result<(), String> {
    let ok = unsafe { FreeLibrary(handle as usize) };
    if ok == 0 {
        let err = unsafe { GetLastError() };
        return Err(alloc::format!("FreeLibrary failed with error code {}", err));
    }
    Ok(())
}

pub fn alloc(size: u64) -> Result<u64, String> {
    let heap = unsafe { GetProcessHeap() };
    if heap.is_null() {
        return Err(String::from("GetProcessHeap failed"));
    }
    let p = unsafe { HeapAlloc(heap, 0, size.max(1) as usize) };
    if p.is_null() {
        return Err(String::from("HeapAlloc failed"));
    }
    Ok(p as usize as u64)
}

pub fn free(ptr: u64) -> Result<(), String> {
    if ptr == 0 {
        return Ok(());
    }
    let heap = unsafe { GetProcessHeap() };
    if !heap.is_null() {
        unsafe { HeapFree(heap, 0, ptr as usize as *mut core::ffi::c_void) };
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
