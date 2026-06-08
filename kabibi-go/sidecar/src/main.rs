#![allow(linker_messages)]

use std::ffi::{c_char, c_void, CString};
use core::ffi::CStr;
use std::io::{AsyncRead, AsyncWrite};
use std::tty::{stdin, stdout};

// Windows raw-dylib bindings for library loading
#[link(name = "kernel32", kind = "raw-dylib")]
unsafe extern "system" {
    fn LoadLibraryA(lpLibFileName: *const u8) -> usize;
    fn GetProcAddress(hModule: usize, lpProcName: *const u8) -> *const c_void;
    fn GetEnvironmentVariableA(lpName: *const u8, lpBuffer: *mut u8, nSize: u32) -> u32;
    fn GetLastError() -> u32;
    fn SetDllDirectoryA(lpPathName: *const u8) -> bool;
    fn GetStdHandle(nStdHandle: u32) -> usize;
    fn WriteFile(h: usize, b: *const u8, n: u32, out: *mut u32, ov: *mut c_void) -> i32;
}

// LiteRT-LM C API signatures (as used in sidecar)
type TokenCallback = unsafe extern "system" fn(
    user_data: *mut c_void,
    chunk: *const c_char,
    is_final: u8,
    err_msg: *const c_char,
) -> isize;

type EngineSettingsCreateFn = unsafe extern "system" fn(
    model_path: *const c_char,
    backend: *const c_char,
    vision_backend: *const c_char,
    audio_backend: *const c_char,
) -> usize;
type EngineSettingsDeleteFn = unsafe extern "system" fn(settings: usize);

type EngineCreateFn = unsafe extern "system" fn(settings: usize) -> usize;

type ConvConfigCreateFn = unsafe extern "system" fn(engine: usize) -> usize;
type ConvConfigDeleteFn = unsafe extern "system" fn(config: usize);

type ConvCreateFn = unsafe extern "system" fn(engine: usize, config: usize) -> usize;
type ConvSendStreamFn = unsafe extern "system" fn(
    conv: usize,
    message: *const c_char,
    extra_context: *const c_char,
    optional_args: *const c_void,
    callback: TokenCallback,
    user_data: *mut c_void,
) -> i32;

#[derive(Default)]
struct SidecarRuntime {
    engine: usize,
    conversation: usize,
    // Function pointers
    settings_create: usize,
    settings_delete: usize,
    engine_create: usize,
    engine_delete: usize,
    conv_config_create: usize,
    conv_config_delete: usize,
    conv_create: usize,
    conv_delete: usize,
    conv_send: usize,
}

fn main() {
    std::spawn!(async_main());
}

async fn async_main() {
    let mut input = stdin();
    
    let lib_path = get_lib_path();
    // Try to load the base lib first to help with dependencies if they are in the same folder
    if let Some(s) = core::str::from_utf8(&lib_path).ok() {
        if let Some(idx) = s.rfind('\\') {
            let dir = format!("{}\0", &s[..idx]);
            unsafe { SetDllDirectoryA(dir.as_ptr()); }

            let base_name = "litert-lm.dll\0";
            let new_path = format!("{}{}", &s[..idx+1], base_name);
            let _ = unsafe { LoadLibraryA(new_path.as_ptr()) };
        }
    }

    let lib = unsafe { LoadLibraryA(lib_path.as_ptr()) };
    if lib == 0 {
        let err = unsafe { GetLastError() };
        let path_str = core::str::from_utf8(&lib_path).unwrap_or("invalid utf8").trim_matches('\0');
        send_error(&format!("Failed to load library (err={}, path={})", err, path_str), 0).await;
        return;
    }

    let mut rt = SidecarRuntime::default();
    rt.settings_create = unsafe { GetProcAddress(lib, b"litert_lm_engine_settings_create\0".as_ptr()) } as usize;
    rt.settings_delete = unsafe { GetProcAddress(lib, b"litert_lm_engine_settings_delete\0".as_ptr()) } as usize;
    rt.engine_create = unsafe { GetProcAddress(lib, b"litert_lm_engine_create\0".as_ptr()) } as usize;
    rt.engine_delete = unsafe { GetProcAddress(lib, b"litert_lm_engine_delete\0".as_ptr()) } as usize;
    rt.conv_config_create = unsafe { GetProcAddress(lib, b"litert_lm_conversation_config_create\0".as_ptr()) } as usize;
    rt.conv_config_delete = unsafe { GetProcAddress(lib, b"litert_lm_conversation_config_delete\0".as_ptr()) } as usize;
    rt.conv_create = unsafe { GetProcAddress(lib, b"litert_lm_conversation_create\0".as_ptr()) } as usize;
    rt.conv_delete = unsafe { GetProcAddress(lib, b"litert_lm_conversation_delete\0".as_ptr()) } as usize;
    rt.conv_send = unsafe { GetProcAddress(lib, b"litert_lm_conversation_send_message_stream\0".as_ptr()) } as usize;

    if rt.settings_create == 0 || rt.settings_delete == 0 || rt.engine_create == 0 || rt.engine_delete == 0 ||
       rt.conv_config_create == 0 || rt.conv_config_delete == 0 ||
       rt.conv_create == 0 || rt.conv_delete == 0 || rt.conv_send == 0 {
        send_error("Missing one or more symbols from library", 0).await;
        return;
    }

    let mut line_buf = String::new();

    let mut out = stdout();
    let _ = out.write(b"{\"event\":\"ready\"}\n".to_vec()).await;

    unsafe {
        let h_err = GetStdHandle(0xFFFF_FFF4); // STD_ERROR_HANDLE
        let mut written = 0;
        let msg = b"Entering loop\n";
        WriteFile(h_err, msg.as_ptr(), msg.len() as u32, &mut written, core::ptr::null_mut());
    }

    loop {
        let mut buf_vec = vec![0u8; 1];
        let (res, b) = input.read(buf_vec).await;
        buf_vec = b;
        
        /*
        unsafe {
            let h_err = GetStdHandle(0xFFFF_FFF4);
            let mut written = 0;
            let msg = match &res {
                Ok(n) => format!("Read Ok({})\n", n),
                Err(e) => format!("Read Err({:?})\n", e),
            };
            WriteFile(h_err, msg.as_ptr(), msg.len() as u32, &mut written, core::ptr::null_mut());
        }
        */

        match res {
            Ok(0) => break,
            Ok(_) => {
                let c = buf_vec[0] as char;
                if c == '\n' {
                    if !line_buf.is_empty() {
                        handle_command(line_buf.trim(), &mut rt).await;
                        line_buf.clear();
                    }
                } else if c != '\r' {
                    line_buf.push(c);
                }
            }
            Err(e) => {
                let err_msg = format!(r#"{{"event":"error","msg":"read error: {:?}"}}"#, e);
                let mut out = stdout();
                let _ = out.write(err_msg.into_bytes()).await;
                let _ = out.write(b"\n".to_vec()).await;
                break;
            }
        }
    }

    if !line_buf.is_empty() {
        handle_command(line_buf.trim(), &mut rt).await;
    }
}

fn get_lib_path() -> Vec<u8> {
    let mut buf = vec![0u8; 512];
    let n = unsafe { GetEnvironmentVariableA(b"LITERTLM_LIB_PATH\0".as_ptr(), buf.as_mut_ptr(), 512) };
    if n > 0 {
        unsafe {
            buf.set_len(n as usize);
        }
        buf.push(0);
        return buf;
    }

    // Default to LOCALAPPDATA/kabibi-go/litert_cache/lib/litert_lm_ext.dll
    let n = unsafe { GetEnvironmentVariableA(b"LocalAppData\0".as_ptr(), buf.as_mut_ptr(), 512) };
    if n > 0 {
        unsafe {
            buf.set_len(n as usize);
        }
        let mut path = String::from_utf8_lossy(&buf).into_owned();
        path.push_str("\\kabibi-go\\litert_cache\\lib\\litert_lm_ext.dll\0");
        return path.into_bytes();
    }

    b"litert_lm_ext.dll\0".to_vec()
}

async fn handle_command(line: &str, rt: &mut SidecarRuntime) {
    unsafe {
        let h_err = GetStdHandle(0xFFFF_FFF4);
        let mut written = 0;
        let msg = format!("Handling command: {}\n", line);
        WriteFile(h_err, msg.as_ptr(), msg.len() as u32, &mut written, core::ptr::null_mut());
    }

    // Very simple hand-rolled JSON parser for the specific command format:
    // {"action":"...", "model_path":"...", "callback":123, "message":"..."}
    
    let action = if line.contains("\"action\":\"engine_create\"") { "engine_create" }
        else if line.contains("\"action\":\"engine_delete\"") { "engine_delete" }
        else if line.contains("\"action\":\"conversation_create\"") { "conversation_create" }
        else if line.contains("\"action\":\"conversation_delete\"") { "conversation_delete" }
        else if line.contains("\"action\":\"conversation_send\"") { "conversation_send" }
        else { "" };

    let callback_id = if let Some(pos) = line.find("\"callback\":") {
        let start = pos + 11;
        let end = line[start..].find(|c: char| !c.is_ascii_digit()).unwrap_or(line[start..].len());
        line[start..start+end].parse::<u64>().unwrap_or(0)
    } else { 0 };

    match action {
        "engine_create" => {
            let model_path = if let Some(pos) = line.find("\"model_path\":\"") {
                let start = pos + 14;
                let end = line[start..].find('"').unwrap_or(0);
                &line[start..start+end]
            } else { "" };

            let backend = if let Some(pos) = line.find("\"backend\":\"") {
                let start = pos + 11;
                let end = line[start..].find('"').unwrap_or(0);
                &line[start..start+end]
            } else { "cpu" };

            let model_path_c = CString::new(model_path).unwrap();
            let backend_c = CString::new(backend).unwrap();

            unsafe {
                let set_create: EngineSettingsCreateFn = core::mem::transmute(rt.settings_create);
                let set_delete: EngineSettingsDeleteFn = core::mem::transmute(rt.settings_delete);
                let eng_create: EngineCreateFn = core::mem::transmute(rt.engine_create);

                let settings = set_create(
                    model_path_c.as_ptr() as *const c_char,
                    backend_c.as_ptr() as *const c_char,
                    core::ptr::null(),
                    core::ptr::null(),
                );

                if settings == 0 {
                    send_error("Failed to create engine settings", callback_id).await;
                    return;
                }

                rt.engine = eng_create(settings);
                set_delete(settings);
            }

            if rt.engine == 0 {
                send_error("Failed to create engine", callback_id).await;
            } else {
                let res = format!(r#"{{"status":"success","engine":"{}","callback":{}}}"#, rt.engine, callback_id);
                let mut out = stdout();
                let _ = out.write(res.into_bytes()).await;
                let _ = out.write(b"\n".to_vec()).await;
            }
        }
        "conversation_create" => {
            if rt.engine == 0 {
                send_error("No engine", callback_id).await;
                return;
            }
            unsafe {
                let conf_create: ConvConfigCreateFn = core::mem::transmute(rt.conv_config_create);
                let conf_delete: ConvConfigDeleteFn = core::mem::transmute(rt.conv_config_delete);
                let conv_create: ConvCreateFn = core::mem::transmute(rt.conv_create);
                
                let config = conf_create(rt.engine);
                rt.conversation = conv_create(rt.engine, config);
                conf_delete(config);
            }
            if rt.conversation == 0 {
                send_error("Failed to create conversation", callback_id).await;
            } else {
                let res = format!(r#"{{"status":"success","conversation":"{}","callback":{}}}"#, rt.conversation, callback_id);
                let mut out = stdout();
                let _ = out.write(res.into_bytes()).await;
                let _ = out.write(b"\n".to_vec()).await;
            }
        }
        "conversation_send" => {
            if rt.conversation == 0 {
                send_error("No conversation", callback_id).await;
                return;
            }

            let message = if let Some(pos) = line.find("\"message\":\"") {
                let start = pos + 11;
                let end = line[start..].find('"').unwrap_or(0);
                &line[start..start+end]
            } else { "" };
            
            // Wrap in JSON envelope {"role":"user","content":"..."}
            let msg_json = format!(r#"{{"role":"user","content":"{}"}}"#, message);
            let message_c = CString::new(&msg_json).unwrap();

            unsafe {
                let func: ConvSendStreamFn = core::mem::transmute(rt.conv_send);
                func(
                    rt.conversation,
                    message_c.as_ptr() as *const c_char,
                    core::ptr::null(),
                    core::ptr::null(),
                    token_callback_impl,
                    callback_id as *mut c_void,
                );
            }

            let res = format!(r#"{{"event":"complete","status":"success","callback":{}}}"#, callback_id);
            let mut out = stdout();
            let _ = out.write(res.into_bytes()).await;
            let _ = out.write(b"\n".to_vec()).await;
        }
        _ => {
            send_error("Unknown action", callback_id).await;
        }
    }
}

async fn send_error(msg: &str, callback_id: u64) {
    let res = format!(r#"{{"status":"error","error":"{}","callback":{}}}"#, msg, callback_id);
    let mut out = stdout();
    let _ = out.write(res.into_bytes()).await;
    let _ = out.write(b"\n".to_vec()).await;
}

unsafe extern "system" fn token_callback_impl(
    user_data: *mut c_void,
    chunk_ptr: *const c_char,
    is_final: u8,
    _err_msg: *const c_char,
) -> isize {
    let callback_id = user_data as u64;
    
    if !chunk_ptr.is_null() {
        let chunk = unsafe { CStr::from_ptr(chunk_ptr).to_str().unwrap_or("") };
        
        let res = format!(r#"{{"event":"token","token":"{}","callback":{}}}"#, chunk, callback_id);
        let json_b = res.into_bytes();
        unsafe {
            let h = GetStdHandle(0xFFFF_FFF5); // STD_OUTPUT_HANDLE
            let mut written = 0;
            WriteFile(h, json_b.as_ptr(), json_b.len() as u32, &mut written, core::ptr::null_mut());
            WriteFile(h, b"\n".as_ptr(), 1, &mut written, core::ptr::null_mut());
        }
    }

    if is_final != 0 {
        // We could send a 'complete' event here too if we want.
    }
    
    0
}
