use super::{EINVAL, SpawnResult, SpawnSpec};
use core::ffi::c_void;

const SESSION_COUNT: usize = 32;
const INVALID_HANDLE_VALUE: Handle = -1isize as Handle;
const ERROR_PIPE_CONNECTED: u32 = 535;
const WAIT_TIMEOUT: u32 = 258;
const INFINITE: u32 = u32::MAX;
const EMPTY: Session = Session {
    pid: 0,
    process: core::ptr::null_mut(),
    pseudo_console: core::ptr::null_mut(),
    input_pipe: core::ptr::null_mut(),
    output_pipe: core::ptr::null_mut(),
    connector_thread: core::ptr::null_mut(),
    exit_code: 0,
    exited: false,
    pty_closed: false,
    child_closed: false,
    input_path: [0; 128],
    output_path: [0; 128],
    input_path_len: 0,
    output_path_len: 0,
};

type Handle = *mut c_void;

#[repr(C)]
struct Coord {
    x: i16,
    y: i16,
}

#[repr(C)]
struct StartupInfoW {
    cb: u32,
    reserved: *mut u16,
    desktop: *mut u16,
    title: *mut u16,
    x: u32,
    y: u32,
    x_size: u32,
    y_size: u32,
    x_chars: u32,
    y_chars: u32,
    fill_attribute: u32,
    flags: u32,
    show_window: u16,
    reserved2_size: u16,
    reserved2: *mut u8,
    std_input: Handle,
    std_output: Handle,
    std_error: Handle,
}

#[repr(C)]
struct StartupInfoExW {
    startup: StartupInfoW,
    attributes: *mut c_void,
}

#[repr(C)]
struct ProcessInfo {
    process: Handle,
    thread: Handle,
    process_id: u32,
    thread_id: u32,
}

#[derive(Clone, Copy)]
struct Session {
    pid: u32,
    process: Handle,
    pseudo_console: Handle,
    input_pipe: Handle,
    output_pipe: Handle,
    connector_thread: Handle,
    exit_code: i32,
    exited: bool,
    pty_closed: bool,
    child_closed: bool,
    input_path: [u8; 128],
    output_path: [u8; 128],
    input_path_len: usize,
    output_path_len: usize,
}

static mut SESSIONS: [Session; SESSION_COUNT] = [EMPTY; SESSION_COUNT];
static mut NEXT_PIPE_ID: u32 = 1;

#[link(name = "kernel32", kind = "raw-dylib")]
unsafe extern "system" {
    fn CreatePseudoConsole(
        size: Coord,
        input: Handle,
        output: Handle,
        flags: u32,
        result: *mut Handle,
    ) -> i32;
    fn ResizePseudoConsole(console: Handle, size: Coord) -> i32;
    fn ClosePseudoConsole(console: Handle);
    fn CreateNamedPipeW(
        name: *const u16,
        open_mode: u32,
        pipe_mode: u32,
        max_instances: u32,
        out_buffer_size: u32,
        in_buffer_size: u32,
        default_timeout: u32,
        security_attributes: *mut c_void,
    ) -> Handle;
    fn ConnectNamedPipe(pipe: Handle, overlapped: *mut c_void) -> i32;
    fn CreateThread(
        attributes: *mut c_void,
        stack_size: usize,
        start: unsafe extern "system" fn(*mut c_void) -> u32,
        parameter: *mut c_void,
        flags: u32,
        thread_id: *mut u32,
    ) -> Handle;
    fn CancelSynchronousIo(thread: Handle) -> i32;
    fn CloseHandle(handle: Handle) -> i32;
    fn GetLastError() -> u32;
    fn GetCurrentProcessId() -> u32;
    fn GetProcessHeap() -> Handle;
    fn HeapAlloc(heap: Handle, flags: u32, bytes: usize) -> *mut c_void;
    fn HeapFree(heap: Handle, flags: u32, memory: *mut c_void) -> i32;
    fn InitializeProcThreadAttributeList(
        list: *mut c_void,
        count: u32,
        flags: u32,
        size: *mut usize,
    ) -> i32;
    fn UpdateProcThreadAttribute(
        list: *mut c_void,
        flags: u32,
        attribute: usize,
        value: *mut c_void,
        size: usize,
        previous: *mut c_void,
        returned_size: *mut usize,
    ) -> i32;
    fn DeleteProcThreadAttributeList(list: *mut c_void);
    fn CreateProcessW(
        application: *const u16,
        command_line: *mut u16,
        process_attributes: *mut c_void,
        thread_attributes: *mut c_void,
        inherit_handles: i32,
        creation_flags: u32,
        environment: *mut c_void,
        current_directory: *const u16,
        startup_info: *mut StartupInfoExW,
        process_info: *mut ProcessInfo,
    ) -> i32;
    fn WaitForSingleObject(handle: Handle, milliseconds: u32) -> u32;
    fn GetExitCodeProcess(process: Handle, exit_code: *mut u32) -> i32;
    fn TerminateProcess(process: Handle, exit_code: u32) -> i32;
}

fn last_error() -> i32 {
    unsafe { GetLastError() as i32 }
}

fn hresult_error(result: i32) -> i32 {
    let value = result as u32;
    if value >> 16 == 0x8007 {
        (value & 0xffff) as i32
    } else {
        31
    }
}

fn find_session(id: i32) -> Option<usize> {
    if id <= 0 || id as usize > SESSION_COUNT {
        return None;
    }
    let index = id as usize - 1;
    if unsafe { core::ptr::addr_of!(SESSIONS[index]).read() }.pid != 0 {
        Some(index)
    } else {
        None
    }
}

fn to_wide(value: *const u8, output: &mut [u16]) -> Result<usize, i32> {
    let mut length = 0usize;
    while unsafe { *value.add(length) } != 0 {
        length += 1;
    }
    let bytes = unsafe { core::slice::from_raw_parts(value, length) };
    let text = core::str::from_utf8(bytes).map_err(|_| EINVAL)?;
    let mut written = 0usize;
    for ch in text.chars() {
        let mut encoded = [0u16; 2];
        for unit in ch.encode_utf16(&mut encoded).iter().copied() {
            if written + 1 >= output.len() {
                return Err(EINVAL);
            }
            output[written] = unit;
            written += 1;
        }
    }
    output[written] = 0;
    Ok(written)
}

fn append_quoted(input: *const u8, command: &mut [u16], position: &mut usize) -> Result<(), i32> {
    let mut wide = [0u16; 8192];
    let length = to_wide(input, &mut wide)?;
    let quote = length == 0
        || wide[..length]
            .iter()
            .any(|&ch| matches!(ch, 0x20 | 0x09 | 0x0a | 0x0b | 0x22));
    if !quote {
        if *position + length + 1 >= command.len() {
            return Err(EINVAL);
        }
        command[*position..*position + length].copy_from_slice(&wide[..length]);
        *position += length;
        return Ok(());
    }
    if *position + 2 >= command.len() {
        return Err(EINVAL);
    }
    command[*position] = b'"' as u16;
    *position += 1;
    let mut index = 0usize;
    while index < length {
        let mut slashes = 0usize;
        while index < length && wide[index] == b'\\' as u16 {
            slashes += 1;
            index += 1;
        }
        if index == length {
            for _ in 0..slashes * 2 {
                if *position + 2 >= command.len() {
                    return Err(EINVAL);
                }
                command[*position] = b'\\' as u16;
                *position += 1;
            }
            break;
        }
        let ch = wide[index];
        if ch == b'"' as u16 {
            for _ in 0..slashes * 2 + 1 {
                if *position + 2 >= command.len() {
                    return Err(EINVAL);
                }
                command[*position] = b'\\' as u16;
                *position += 1;
            }
            if *position + 2 >= command.len() {
                return Err(EINVAL);
            }
            command[*position] = ch;
            *position += 1;
        } else {
            for _ in 0..slashes {
                if *position + 2 >= command.len() {
                    return Err(EINVAL);
                }
                command[*position] = b'\\' as u16;
                *position += 1;
            }
            if *position + 2 >= command.len() {
                return Err(EINVAL);
            }
            command[*position] = ch;
            *position += 1;
        }
        index += 1;
        if *position + 2 >= command.len() {
            return Err(EINVAL);
        }
    }
    command[*position] = b'"' as u16;
    *position += 1;
    Ok(())
}

fn push_path(path: &mut [u8; 128], prefix: &[u8], pid: u32, sequence: u32, suffix: &[u8]) -> usize {
    let mut at = 0;
    for byte in b"\\\\.\\pipe\\moh-pty-" {
        path[at] = *byte;
        at += 1;
    }
    for byte in prefix {
        path[at] = *byte;
        at += 1;
    }
    at += write_decimal(&mut path[at..], pid);
    path[at] = b'-';
    at += 1;
    at += write_decimal(&mut path[at..], sequence);
    path[at] = b'-';
    at += 1;
    for byte in suffix {
        path[at] = *byte;
        at += 1;
    }
    path[at] = 0;
    at
}

fn write_decimal(out: &mut [u8], value: u32) -> usize {
    let mut digits = [0u8; 10];
    let mut len = 0;
    let mut n = value;
    loop {
        digits[len] = b'0' + (n % 10) as u8;
        len += 1;
        n /= 10;
        if n == 0 {
            break;
        }
    }
    for i in 0..len {
        out[i] = digits[len - i - 1];
    }
    len
}

fn ascii_to_wide(input: &[u8], output: &mut [u16]) {
    for (i, byte) in input.iter().enumerate() {
        output[i] = *byte as u16;
    }
    output[input.len()] = 0;
}

unsafe extern "system" fn connect_pipes(parameter: *mut c_void) -> u32 {
    let index = parameter as usize - 1;
    let session = unsafe { core::ptr::addr_of!(SESSIONS[index]).read() };
    for pipe in [session.input_pipe, session.output_pipe] {
        if unsafe { ConnectNamedPipe(pipe, core::ptr::null_mut()) } == 0 {
            let error = unsafe { GetLastError() };
            if error != ERROR_PIPE_CONNECTED {
                return error;
            }
        }
    }
    0
}

fn close_resources(session: &mut Session) {
    unsafe {
        if !session.connector_thread.is_null() {
            CancelSynchronousIo(session.connector_thread);
        }
        for handle in [session.input_pipe, session.output_pipe] {
            if !handle.is_null() && handle != INVALID_HANDLE_VALUE {
                CloseHandle(handle);
            }
        }
        session.input_pipe = core::ptr::null_mut();
        session.output_pipe = core::ptr::null_mut();
        if !session.connector_thread.is_null() {
            WaitForSingleObject(session.connector_thread, INFINITE);
            CloseHandle(session.connector_thread);
            session.connector_thread = core::ptr::null_mut();
        }
        if !session.pseudo_console.is_null() {
            ClosePseudoConsole(session.pseudo_console);
            session.pseudo_console = core::ptr::null_mut();
        }
    }
    session.pty_closed = true;
}

pub fn spawn(config: &mut [u8], cols: i32, rows: i32) -> Result<SpawnResult, i32> {
    let spec = SpawnSpec::parse(config)?;
    let Some(index) =
        (0..SESSION_COUNT).find(|&i| unsafe { core::ptr::addr_of!(SESSIONS[i]).read().pid == 0 })
    else {
        return Err(24);
    };
    let pid = unsafe { GetCurrentProcessId() };
    let sequence = unsafe {
        let next = NEXT_PIPE_ID;
        NEXT_PIPE_ID = NEXT_PIPE_ID.wrapping_add(1).max(1);
        next
    };

    let mut session = EMPTY;
    session.input_path_len = push_path(&mut session.input_path, b"", pid, sequence, b"in");
    session.output_path_len = push_path(&mut session.output_path, b"", pid, sequence, b"out");
    let mut input_wide = [0u16; 128];
    let mut output_wide = [0u16; 128];
    ascii_to_wide(
        &session.input_path[..session.input_path_len],
        &mut input_wide,
    );
    ascii_to_wide(
        &session.output_path[..session.output_path_len],
        &mut output_wide,
    );

    const PIPE_ACCESS_INBOUND: u32 = 0x00000001;
    const PIPE_ACCESS_OUTBOUND: u32 = 0x00000002;
    const PIPE_TYPE_BYTE: u32 = 0;
    const PIPE_READMODE_BYTE: u32 = 0;
    const PIPE_WAIT: u32 = 0;
    session.input_pipe = unsafe {
        CreateNamedPipeW(
            input_wide.as_ptr(),
            PIPE_ACCESS_INBOUND,
            PIPE_TYPE_BYTE | PIPE_READMODE_BYTE | PIPE_WAIT,
            1,
            4096,
            4096,
            0,
            core::ptr::null_mut(),
        )
    };
    session.output_pipe = unsafe {
        CreateNamedPipeW(
            output_wide.as_ptr(),
            PIPE_ACCESS_OUTBOUND,
            PIPE_TYPE_BYTE | PIPE_READMODE_BYTE | PIPE_WAIT,
            1,
            4096,
            4096,
            0,
            core::ptr::null_mut(),
        )
    };
    if session.input_pipe == INVALID_HANDLE_VALUE || session.output_pipe == INVALID_HANDLE_VALUE {
        let error = last_error();
        close_resources(&mut session);
        return Err(error);
    }
    let mut console = core::ptr::null_mut();
    let coord = Coord {
        x: cols.clamp(1, i16::MAX as i32) as i16,
        y: rows.clamp(1, i16::MAX as i32) as i16,
    };
    let console_status = unsafe {
        CreatePseudoConsole(
            coord,
            session.input_pipe,
            session.output_pipe,
            0,
            &mut console,
        )
    };
    if console_status < 0 {
        close_resources(&mut session);
        return Err(hresult_error(console_status));
    }
    session.pseudo_console = console;

    let mut command_line = [0u16; 32768];
    let mut command_len = 0;
    for arg in spec.argv[..spec.argc].iter().copied() {
        if command_len > 0 {
            command_line[command_len] = b' ' as u16;
            command_len += 1;
        }
        if let Err(error) = append_quoted(arg, &mut command_line, &mut command_len) {
            close_resources(&mut session);
            return Err(error);
        }
    }
    command_line[command_len] = 0;
    let mut environment = [0u16; 32768];
    let mut env_len = 0usize;
    let env_capacity = environment.len() - 1;
    for entry in spec.envp[..spec.envc].iter().copied() {
        if env_len >= env_capacity {
            close_resources(&mut session);
            return Err(EINVAL);
        }
        let written = match to_wide(entry, &mut environment[env_len..env_capacity]) {
            Ok(written) => written,
            Err(error) => {
                close_resources(&mut session);
                return Err(error);
            }
        };
        env_len += written + 1;
    }
    environment[env_len] = 0;
    if env_len == 0 {
        environment[1] = 0;
    }

    let mut directory = [0u16; 8192];
    let cwd = if spec.cwd.is_null() {
        core::ptr::null()
    } else {
        if let Err(error) = to_wide(spec.cwd, &mut directory) {
            close_resources(&mut session);
            return Err(error);
        }
        directory.as_ptr()
    };

    let mut attribute_size = 0usize;
    unsafe {
        InitializeProcThreadAttributeList(core::ptr::null_mut(), 1, 0, &mut attribute_size);
    }
    let heap = unsafe { GetProcessHeap() };
    let attributes = unsafe { HeapAlloc(heap, 0, attribute_size) };
    if attributes.is_null() {
        close_resources(&mut session);
        return Err(8);
    }
    if unsafe { InitializeProcThreadAttributeList(attributes, 1, 0, &mut attribute_size) } == 0 {
        let error = last_error();
        unsafe { HeapFree(heap, 0, attributes) };
        close_resources(&mut session);
        return Err(error);
    }
    const PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE: usize = 0x00020016;
    if unsafe {
        UpdateProcThreadAttribute(
            attributes,
            0,
            PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
            session.pseudo_console,
            core::mem::size_of::<Handle>(),
            core::ptr::null_mut(),
            core::ptr::null_mut(),
        )
    } == 0
    {
        let error = last_error();
        unsafe {
            DeleteProcThreadAttributeList(attributes);
            HeapFree(heap, 0, attributes);
        }
        close_resources(&mut session);
        return Err(error);
    }

    let mut startup = StartupInfoExW {
        startup: StartupInfoW {
            cb: core::mem::size_of::<StartupInfoExW>() as u32,
            reserved: core::ptr::null_mut(),
            desktop: core::ptr::null_mut(),
            title: core::ptr::null_mut(),
            x: 0,
            y: 0,
            x_size: 0,
            y_size: 0,
            x_chars: 0,
            y_chars: 0,
            fill_attribute: 0,
            flags: 0x00000100,
            show_window: 0,
            reserved2_size: 0,
            reserved2: core::ptr::null_mut(),
            std_input: core::ptr::null_mut(),
            std_output: core::ptr::null_mut(),
            std_error: core::ptr::null_mut(),
        },
        attributes,
    };
    let mut process_info = ProcessInfo {
        process: core::ptr::null_mut(),
        thread: core::ptr::null_mut(),
        process_id: 0,
        thread_id: 0,
    };
    const EXTENDED_STARTUPINFO_PRESENT: u32 = 0x00080000;
    const CREATE_UNICODE_ENVIRONMENT: u32 = 0x00000400;
    let created = unsafe {
        CreateProcessW(
            core::ptr::null(),
            command_line.as_mut_ptr(),
            core::ptr::null_mut(),
            core::ptr::null_mut(),
            0,
            EXTENDED_STARTUPINFO_PRESENT | CREATE_UNICODE_ENVIRONMENT,
            environment.as_mut_ptr().cast(),
            cwd,
            &mut startup,
            &mut process_info,
        )
    };
    let error = if created == 0 { last_error() } else { 0 };
    unsafe {
        DeleteProcThreadAttributeList(attributes);
        HeapFree(heap, 0, attributes);
    }
    if created == 0 {
        close_resources(&mut session);
        return Err(error);
    }
    unsafe { CloseHandle(process_info.thread) };
    session.pid = process_info.process_id;
    session.process = process_info.process;
    unsafe { core::ptr::addr_of_mut!(SESSIONS[index]).write(session) };
    session.connector_thread = unsafe {
        CreateThread(
            core::ptr::null_mut(),
            0,
            connect_pipes,
            (index + 1) as *mut c_void,
            0,
            core::ptr::null_mut(),
        )
    };
    if session.connector_thread.is_null() {
        let error = last_error();
        unsafe {
            TerminateProcess(session.process, 127);
            WaitForSingleObject(session.process, INFINITE);
            CloseHandle(session.process);
        }
        close_resources(&mut session);
        unsafe { core::ptr::addr_of_mut!(SESSIONS[index]).write(EMPTY) };
        return Err(error);
    }
    unsafe { core::ptr::addr_of_mut!(SESSIONS[index]).write(session) };

    Ok(SpawnResult::new(
        index as i32 + 1,
        process_info.process_id as i32,
        -1,
        -1,
        &session.input_path[..session.input_path_len],
        &session.output_path[..session.output_path_len],
    ))
}

pub fn resize(id: i32, cols: i32, rows: i32) -> i32 {
    let Some(index) = find_session(id) else {
        return 9;
    };
    if cols <= 0 || rows <= 0 {
        return EINVAL;
    }
    let session = unsafe { core::ptr::addr_of!(SESSIONS[index]).read() };
    if session.pty_closed || session.pseudo_console.is_null() {
        return 9;
    }
    let coord = Coord {
        x: cols.min(i16::MAX as i32) as i16,
        y: rows.min(i16::MAX as i32) as i16,
    };
    let status = unsafe { ResizePseudoConsole(session.pseudo_console, coord) };
    if status == 0 {
        0
    } else {
        hresult_error(status)
    }
}

pub fn poll(id: i32) -> i64 {
    let Some(index) = find_session(id) else {
        return -9;
    };
    let mut session = unsafe { core::ptr::addr_of!(SESSIONS[index]).read() };
    if session.exited {
        return (1i64 << 32) | session.exit_code as u32 as i64;
    }
    if unsafe { WaitForSingleObject(session.process, 0) } == WAIT_TIMEOUT {
        return 0;
    }
    let mut code = 0u32;
    if unsafe { GetExitCodeProcess(session.process, &mut code) } == 0 {
        return -(last_error() as i64);
    }
    if unsafe { WaitForSingleObject(session.connector_thread, 0) } == WAIT_TIMEOUT {
        return 0;
    }
    session.exited = true;
    session.exit_code = code as i32;
    close_resources(&mut session);
    unsafe { core::ptr::addr_of_mut!(SESSIONS[index]).write(session) };
    (1i64 << 32) | session.exit_code as u32 as i64
}

pub fn signal(id: i32, signum: i32) -> i32 {
    let Some(index) = find_session(id) else {
        return 9;
    };
    let session = unsafe { core::ptr::addr_of!(SESSIONS[index]).read() };
    if unsafe { TerminateProcess(session.process, (128 + signum.max(0)) as u32) } != 0 {
        0
    } else {
        last_error()
    }
}

pub fn close_pty(id: i32) -> i32 {
    let Some(index) = find_session(id) else {
        return 9;
    };
    let mut session = unsafe { core::ptr::addr_of!(SESSIONS[index]).read() };
    if !session.pty_closed {
        close_resources(&mut session);
        unsafe {
            if session.child_closed && session.exited {
                core::ptr::addr_of_mut!(SESSIONS[index]).write(EMPTY);
            } else {
                core::ptr::addr_of_mut!(SESSIONS[index]).write(session);
            }
        }
    }
    0
}

pub fn close_child(id: i32) -> i32 {
    let Some(index) = find_session(id) else {
        return 9;
    };
    let mut session = unsafe { core::ptr::addr_of!(SESSIONS[index]).read() };
    if !session.child_closed {
        unsafe { CloseHandle(session.process) };
        session.process = core::ptr::null_mut();
        session.child_closed = true;
    }
    if session.exited && session.pty_closed {
        unsafe { core::ptr::addr_of_mut!(SESSIONS[index]).write(EMPTY) };
    } else {
        unsafe { core::ptr::addr_of_mut!(SESSIONS[index]).write(session) };
    }
    0
}
