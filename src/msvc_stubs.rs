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


