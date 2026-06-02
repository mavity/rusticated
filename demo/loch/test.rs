fn main() {
    let mut buf = vec![0u16; 512];
    let len = unsafe { GetCurrentDirectoryW(buf.len() as u32, buf.as_mut_ptr()) };
    println!("len: {}", len);
}
#[link(name = "kernel32", kind = "raw-dylib")]
extern "system" {
    fn GetCurrentDirectoryW(nBufferLength: u32, lpBuffer: *mut u16) -> u32;
}
