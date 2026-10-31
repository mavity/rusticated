#![no_std]
#![allow(missing_docs)]
#![allow(unsafe_op_in_unsafe_fn)]

mod os;

use core::ffi::{c_char, c_void};

type NapiEnv = *mut c_void;
type NapiValue = *mut c_void;
type NapiInfo = *mut c_void;
type NapiCallback = unsafe extern "C" fn(NapiEnv, NapiInfo) -> NapiValue;

#[cfg(windows)]
type GetCallbackInfo = unsafe extern "C" fn(
    NapiEnv,
    NapiInfo,
    *mut usize,
    *mut NapiValue,
    *mut NapiValue,
    *mut *mut c_void,
) -> i32;
#[cfg(windows)]
type GetString = unsafe extern "C" fn(NapiEnv, NapiValue, *mut c_char, usize, *mut usize) -> i32;
#[cfg(windows)]
type GetInt32 = unsafe extern "C" fn(NapiEnv, NapiValue, *mut i32) -> i32;
#[cfg(windows)]
type CreateFunction = unsafe extern "C" fn(
    NapiEnv,
    *const c_char,
    usize,
    NapiCallback,
    *mut c_void,
    *mut NapiValue,
) -> i32;
#[cfg(windows)]
type SetNamedProperty = unsafe extern "C" fn(NapiEnv, NapiValue, *const c_char, NapiValue) -> i32;
#[cfg(windows)]
type CreateInt32 = unsafe extern "C" fn(NapiEnv, i32, *mut NapiValue) -> i32;
#[cfg(windows)]
type CreateBigintUint64 = unsafe extern "C" fn(NapiEnv, u64, *mut NapiValue) -> i32;
#[cfg(windows)]
type CreateString = unsafe extern "C" fn(NapiEnv, *const c_char, usize, *mut NapiValue) -> i32;

#[cfg(windows)]
struct NapiApi {
    get_callback_info: core::sync::atomic::AtomicPtr<c_void>,
    get_string: core::sync::atomic::AtomicPtr<c_void>,
    get_int32: core::sync::atomic::AtomicPtr<c_void>,
    create_function: core::sync::atomic::AtomicPtr<c_void>,
    set_named_property: core::sync::atomic::AtomicPtr<c_void>,
    create_int32: core::sync::atomic::AtomicPtr<c_void>,
    create_bigint_uint64: core::sync::atomic::AtomicPtr<c_void>,
    create_string: core::sync::atomic::AtomicPtr<c_void>,
}

#[cfg(windows)]
static NAPI: NapiApi = NapiApi {
    get_callback_info: core::sync::atomic::AtomicPtr::new(core::ptr::null_mut()),
    get_string: core::sync::atomic::AtomicPtr::new(core::ptr::null_mut()),
    get_int32: core::sync::atomic::AtomicPtr::new(core::ptr::null_mut()),
    create_function: core::sync::atomic::AtomicPtr::new(core::ptr::null_mut()),
    set_named_property: core::sync::atomic::AtomicPtr::new(core::ptr::null_mut()),
    create_int32: core::sync::atomic::AtomicPtr::new(core::ptr::null_mut()),
    create_bigint_uint64: core::sync::atomic::AtomicPtr::new(core::ptr::null_mut()),
    create_string: core::sync::atomic::AtomicPtr::new(core::ptr::null_mut()),
};

#[cfg(not(windows))]
unsafe extern "C" {
    fn napi_get_cb_info(
        env: NapiEnv,
        info: NapiInfo,
        argc: *mut usize,
        argv: *mut NapiValue,
        this_arg: *mut NapiValue,
        data: *mut *mut c_void,
    ) -> i32;
    fn napi_get_value_string_utf8(
        env: NapiEnv,
        value: NapiValue,
        buf: *mut c_char,
        bufsize: usize,
        result: *mut usize,
    ) -> i32;
    fn napi_get_value_int32(env: NapiEnv, value: NapiValue, result: *mut i32) -> i32;
    fn napi_create_function(
        env: NapiEnv,
        name: *const c_char,
        length: usize,
        cb: NapiCallback,
        data: *mut c_void,
        result: *mut NapiValue,
    ) -> i32;
    fn napi_set_named_property(
        env: NapiEnv,
        object: NapiValue,
        name: *const c_char,
        value: NapiValue,
    ) -> i32;
    fn napi_create_int32(env: NapiEnv, value: i32, result: *mut NapiValue) -> i32;
    fn napi_create_bigint_uint64(env: NapiEnv, value: u64, result: *mut NapiValue) -> i32;
    fn napi_create_string_utf8(
        env: NapiEnv,
        value: *const c_char,
        length: usize,
        result: *mut NapiValue,
    ) -> i32;
}

#[cfg(windows)]
fn resolve_napi(name: &'static [u8]) -> *mut c_void {
    #[link(name = "kernel32", kind = "raw-dylib")]
    unsafe extern "system" {
        fn GetModuleHandleW(name: *const u16) -> *mut c_void;
        fn GetProcAddress(module: *mut c_void, name: *const u8) -> *mut c_void;
    }
    unsafe { GetProcAddress(GetModuleHandleW(core::ptr::null()), name.as_ptr()) }
}

#[cfg(windows)]
unsafe fn load_napi() -> bool {
    use core::sync::atomic::Ordering;
    macro_rules! resolve {
        ($field:ident, $symbol:literal) => {{
            let pointer = resolve_napi($symbol);
            NAPI.$field.store(pointer, Ordering::Relaxed);
            !pointer.is_null()
        }};
    }
    resolve!(get_callback_info, b"napi_get_cb_info\0")
        & resolve!(get_string, b"napi_get_value_string_utf8\0")
        & resolve!(get_int32, b"napi_get_value_int32\0")
        & resolve!(create_function, b"napi_create_function\0")
        & resolve!(set_named_property, b"napi_set_named_property\0")
        & resolve!(create_int32, b"napi_create_int32\0")
        & resolve!(create_bigint_uint64, b"napi_create_bigint_uint64\0")
        & resolve!(create_string, b"napi_create_string_utf8\0")
}

#[cfg(windows)]
macro_rules! api_accessor {
    ($name:ident, $field:ident, $ty:ty) => {
        fn $name() -> Option<$ty> {
            use core::sync::atomic::Ordering;
            let pointer = NAPI.$field.load(Ordering::Relaxed);
            if pointer.is_null() {
                None
            } else {
                Some(unsafe { core::mem::transmute::<*mut c_void, $ty>(pointer) })
            }
        }
    };
}

#[cfg(windows)]
api_accessor!(get_callback_info_fn, get_callback_info, GetCallbackInfo);
#[cfg(windows)]
api_accessor!(get_string_fn, get_string, GetString);
#[cfg(windows)]
api_accessor!(get_int32_fn, get_int32, GetInt32);
#[cfg(windows)]
api_accessor!(create_function_fn, create_function, CreateFunction);
#[cfg(windows)]
api_accessor!(set_named_property_fn, set_named_property, SetNamedProperty);
#[cfg(windows)]
api_accessor!(create_int32_fn, create_int32, CreateInt32);
#[cfg(windows)]
api_accessor!(
    create_bigint_uint64_fn,
    create_bigint_uint64,
    CreateBigintUint64
);
#[cfg(windows)]
api_accessor!(create_string_fn, create_string, CreateString);

unsafe fn get_args(env: NapiEnv, info: NapiInfo, args: &mut [NapiValue]) -> usize {
    let mut count = args.len();
    #[cfg(windows)]
    let status = if let Some(function) = get_callback_info_fn() {
        function(
            env,
            info,
            &mut count,
            args.as_mut_ptr(),
            core::ptr::null_mut(),
            core::ptr::null_mut(),
        )
    } else {
        return 0;
    };
    #[cfg(not(windows))]
    let status = napi_get_cb_info(
        env,
        info,
        &mut count,
        args.as_mut_ptr(),
        core::ptr::null_mut(),
        core::ptr::null_mut(),
    );
    if status == 0 {
        count.min(args.len())
    } else {
        0
    }
}

unsafe fn get_string(env: NapiEnv, value: NapiValue, buf: &mut [u8]) -> Option<usize> {
    let mut length = 0usize;
    #[cfg(windows)]
    let status = if let Some(function) = get_string_fn() {
        function(env, value, buf.as_mut_ptr().cast(), buf.len(), &mut length)
    } else {
        return None;
    };
    #[cfg(not(windows))]
    let status =
        napi_get_value_string_utf8(env, value, buf.as_mut_ptr().cast(), buf.len(), &mut length);
    if status == 0 && length < buf.len() {
        Some(length)
    } else {
        None
    }
}

unsafe fn get_int32(env: NapiEnv, value: NapiValue) -> Option<i32> {
    let mut result = 0i32;
    #[cfg(windows)]
    let status = if let Some(function) = get_int32_fn() {
        function(env, value, &mut result)
    } else {
        return None;
    };
    #[cfg(not(windows))]
    let status = napi_get_value_int32(env, value, &mut result);
    if status == 0 { Some(result) } else { None }
}

unsafe fn new_int32(env: NapiEnv, value: i32) -> NapiValue {
    let mut result = core::ptr::null_mut();
    #[cfg(windows)]
    if let Some(function) = create_int32_fn() {
        function(env, value, &mut result);
    } else {
        return core::ptr::null_mut();
    }
    #[cfg(not(windows))]
    let _ = napi_create_int32(env, value, &mut result);
    result
}

unsafe fn new_bigint_uint64(env: NapiEnv, value: u64) -> NapiValue {
    let mut result = core::ptr::null_mut();
    #[cfg(windows)]
    if let Some(function) = create_bigint_uint64_fn() {
        function(env, value, &mut result);
    } else {
        return core::ptr::null_mut();
    }
    #[cfg(not(windows))]
    let _ = napi_create_bigint_uint64(env, value, &mut result);
    result
}

unsafe fn new_string(env: NapiEnv, value: &[u8]) -> NapiValue {
    let mut result = core::ptr::null_mut();
    #[cfg(windows)]
    if let Some(function) = create_string_fn() {
        function(env, value.as_ptr().cast(), value.len(), &mut result);
    } else {
        return core::ptr::null_mut();
    }
    #[cfg(not(windows))]
    let _ = napi_create_string_utf8(env, value.as_ptr().cast(), value.len(), &mut result);
    result
}

unsafe extern "C" fn spawn(env: NapiEnv, info: NapiInfo) -> NapiValue {
    let mut args = [core::ptr::null_mut(); 3];
    if get_args(env, info, &mut args) != 3 {
        return new_int32(env, os::EINVAL);
    }
    let Some(cols) = get_int32(env, args[1]) else {
        return new_int32(env, os::EINVAL);
    };
    let Some(rows) = get_int32(env, args[2]) else {
        return new_int32(env, os::EINVAL);
    };
    let mut config = [0u8; 32768];
    let Some(length) = get_string(env, args[0], &mut config) else {
        return new_int32(env, os::EINVAL);
    };
    match os::spawn(&mut config[..=length], cols, rows) {
        Ok(result) => new_string(env, result.as_bytes()),
        Err(errno) => new_int32(env, errno),
    }
}

unsafe extern "C" fn resize(env: NapiEnv, info: NapiInfo) -> NapiValue {
    let mut args = [core::ptr::null_mut(); 3];
    if get_args(env, info, &mut args) != 3 {
        return new_int32(env, os::EINVAL);
    }
    let Some(id) = get_int32(env, args[0]) else {
        return new_int32(env, os::EINVAL);
    };
    let Some(cols) = get_int32(env, args[1]) else {
        return new_int32(env, os::EINVAL);
    };
    let Some(rows) = get_int32(env, args[2]) else {
        return new_int32(env, os::EINVAL);
    };
    new_int32(env, os::resize(id, cols, rows))
}

unsafe extern "C" fn poll(env: NapiEnv, info: NapiInfo) -> NapiValue {
    let mut args = [core::ptr::null_mut(); 1];
    if get_args(env, info, &mut args) != 1 {
        return new_bigint_uint64(env, (-os::EINVAL as i64) as u64);
    }
    let Some(id) = get_int32(env, args[0]) else {
        return new_bigint_uint64(env, (-os::EINVAL as i64) as u64);
    };
    new_bigint_uint64(env, os::poll(id) as u64)
}

unsafe extern "C" fn signal(env: NapiEnv, info: NapiInfo) -> NapiValue {
    let mut args = [core::ptr::null_mut(); 2];
    if get_args(env, info, &mut args) != 2 {
        return new_int32(env, os::EINVAL);
    }
    let (Some(id), Some(sig)) = (get_int32(env, args[0]), get_int32(env, args[1])) else {
        return new_int32(env, os::EINVAL);
    };
    new_int32(env, os::signal(id, sig))
}

unsafe extern "C" fn close_pty(env: NapiEnv, info: NapiInfo) -> NapiValue {
    let mut args = [core::ptr::null_mut(); 1];
    if get_args(env, info, &mut args) != 1 {
        return new_int32(env, os::EINVAL);
    }
    let Some(id) = get_int32(env, args[0]) else {
        return new_int32(env, os::EINVAL);
    };
    new_int32(env, os::close_pty(id))
}

unsafe extern "C" fn close_child(env: NapiEnv, info: NapiInfo) -> NapiValue {
    let mut args = [core::ptr::null_mut(); 1];
    if get_args(env, info, &mut args) != 1 {
        return new_int32(env, os::EINVAL);
    }
    let Some(id) = get_int32(env, args[0]) else {
        return new_int32(env, os::EINVAL);
    };
    new_int32(env, os::close_child(id))
}

unsafe fn add_function(
    env: NapiEnv,
    exports: NapiValue,
    name: &'static [u8],
    callback: NapiCallback,
) -> bool {
    let mut value = core::ptr::null_mut();
    #[cfg(windows)]
    let create_status = if let Some(function) = create_function_fn() {
        function(
            env,
            name.as_ptr().cast(),
            usize::MAX,
            callback,
            core::ptr::null_mut(),
            &mut value,
        )
    } else {
        return false;
    };
    #[cfg(not(windows))]
    let create_status = napi_create_function(
        env,
        name.as_ptr().cast(),
        usize::MAX,
        callback,
        core::ptr::null_mut(),
        &mut value,
    );
    if create_status != 0 {
        return false;
    }
    #[cfg(windows)]
    let set_status = if let Some(function) = set_named_property_fn() {
        function(env, exports, name.as_ptr().cast(), value)
    } else {
        return false;
    };
    #[cfg(not(windows))]
    let set_status = napi_set_named_property(env, exports, name.as_ptr().cast(), value);
    set_status == 0
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn napi_register_module_v1(env: NapiEnv, exports: NapiValue) -> NapiValue {
    #[cfg(windows)]
    if !load_napi() {
        return core::ptr::null_mut();
    }

    let ok = add_function(env, exports, b"spawn\0", spawn)
        && add_function(env, exports, b"resize\0", resize)
        && add_function(env, exports, b"poll\0", poll)
        && add_function(env, exports, b"signal\0", signal)
        && add_function(env, exports, b"closePty\0", close_pty)
        && add_function(env, exports, b"closeChild\0", close_child);
    if ok { exports } else { core::ptr::null_mut() }
}

#[cfg(windows)]
#[unsafe(no_mangle)]
pub unsafe extern "system" fn DllMainCRTStartup(
    _module: *mut c_void,
    _reason: u32,
    _reserved: *mut c_void,
) -> i32 {
    1
}

#[cfg(windows)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn _Unwind_Resume(_: *mut c_void) -> ! {
    loop {}
}

#[cfg(windows)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn rust_eh_personality() {}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memcpy(dst: *mut u8, src: *const u8, len: usize) -> *mut u8 {
    for i in 0..len {
        unsafe { *dst.add(i) = *src.add(i) };
    }
    dst
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memset(dst: *mut u8, value: i32, len: usize) -> *mut u8 {
    for i in 0..len {
        unsafe { *dst.add(i) = value as u8 };
    }
    dst
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memmove(dst: *mut u8, src: *const u8, len: usize) -> *mut u8 {
    if (dst as usize) <= (src as usize) {
        unsafe { memcpy(dst, src, len) }
    } else {
        for i in (0..len).rev() {
            unsafe { *dst.add(i) = *src.add(i) };
        }
        dst
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memcmp(a: *const u8, b: *const u8, len: usize) -> i32 {
    for i in 0..len {
        let av = unsafe { *a.add(i) };
        let bv = unsafe { *b.add(i) };
        if av != bv {
            return av as i32 - bv as i32;
        }
    }
    0
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn strlen(value: *const u8) -> usize {
    let mut len = 0;
    while unsafe { *value.add(len) } != 0 {
        len += 1;
    }
    len
}

#[panic_handler]
fn panic(_info: &core::panic::PanicInfo<'_>) -> ! {
    loop {}
}
