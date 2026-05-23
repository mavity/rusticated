#![allow(clippy::missing_safety_doc)]

#[unsafe(no_mangle)]
pub unsafe extern "C" fn fmod(x: f64, y: f64) -> f64 {
    x % y
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn cos(_x: f64) -> f64 {
    0.0
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn sin(_x: f64) -> f64 {
    0.0
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn _CxxThrowException(_: *mut u8, _: *mut u8) -> ! {
    core::panic!("_CxxThrowException called (C++ EH not supported without std)");
}

#[cfg(any(target_arch = "x86_64", target_arch = "aarch64"))]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn __CxxFrameHandler3() -> i32 {
    0
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn free(_: *mut u8) {}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memset(dest: *mut u8, c: i32, n: core::primitive::usize) -> *mut u8 {
    unsafe {
        let mut i = 0;
        while i < n {
            *dest.add(i) = c as u8;
            i += 1;
        }
        dest
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memcpy(
    dest: *mut u8,
    src: *const u8,
    n: core::primitive::usize,
) -> *mut u8 {
    unsafe {
        let mut i = 0;
        while i < n {
            *dest.add(i) = *src.add(i);
            i += 1;
        }
        dest
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memcmp(s1: *const u8, s2: *const u8, n: core::primitive::usize) -> i32 {
    unsafe {
        let mut i = 0;
        while i < n {
            let a = *s1.add(i);
            let b = *s2.add(i);
            if a != b {
                return a as i32 - b as i32;
            }
            i += 1;
        }
        0
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memmove(
    dest: *mut u8,
    src: *const u8,
    n: core::primitive::usize,
) -> *mut u8 {
    unsafe {
        if src < dest as *const u8 {
            let mut i = n;
            while i > 0 {
                i -= 1;
                *dest.add(i) = *src.add(i);
            }
        } else {
            let mut i = 0;
            while i < n {
                *dest.add(i) = *src.add(i);
                i += 1;
            }
        }
        dest
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn strlen(s: *const u8) -> core::primitive::usize {
    unsafe {
        let mut n = 0;
        while *s.add(n) != 0 {
            n += 1;
        }
        n
    }
}
