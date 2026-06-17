use core::ffi::c_void;

pub mod Win32 {
    pub mod Foundation {
        use super::super::c_void;

        pub type BOOL = i32;
        pub type BYTE = u8;
        pub type WORD = u16;
        pub type DWORD = u32;
        pub type ULONG = u32;
        #[allow(non_camel_case_types)]
        pub type ULONG_PTR = usize;
        pub type HANDLE = *mut c_void;
        pub type HMODULE = HANDLE;
        pub type LPVOID = *mut c_void;
        pub type LPCVOID = *const c_void;
        pub type PVOID = *mut c_void;
        pub type LPWSTR = *mut u16;
        pub type LPCWSTR = *const u16;
        pub type LPSTR = *mut u8;
        pub type LPCSTR = *const u8;
        pub type LONGLONG = i64;

        pub const INVALID_HANDLE_VALUE: HANDLE = -1isize as HANDLE;
        pub const TRUE: BOOL = 1;
        pub const FALSE: BOOL = 0;

        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" {
            pub fn CloseHandle(hObject: HANDLE) -> BOOL;
            pub fn GetLastError() -> DWORD;
        }
    }

    pub mod System {
        pub mod Console {
            use super::super::Foundation::*;

            pub const STD_ERROR_HANDLE: DWORD = (-12i32) as DWORD;

            #[link(name = "kernel32", kind = "raw-dylib")]
            unsafe extern "system" {
                pub fn GetStdHandle(nStdHandle: DWORD) -> HANDLE;
            }
        }

        pub mod Environment {
            use super::super::Foundation::*;

            #[link(name = "kernel32", kind = "raw-dylib")]
            unsafe extern "system" {
                pub fn GetCommandLineW() -> LPWSTR;
                pub fn GetEnvironmentVariableW(
                    lpName: LPCWSTR,
                    lpBuffer: LPWSTR,
                    nSize: DWORD,
                ) -> DWORD;
                pub fn SetEnvironmentVariableW(lpName: LPCWSTR, lpValue: LPCWSTR) -> BOOL;
            }
        }

        pub mod Threading {
            use super::super::Foundation::*;

            #[repr(C)]
            pub struct STARTUPINFOW {
                pub cb: DWORD,
                pub lpReserved: LPWSTR,
                pub lpDesktop: LPWSTR,
                pub lpTitle: LPWSTR,
                pub dwX: DWORD,
                pub dwY: DWORD,
                pub dwXSize: DWORD,
                pub dwYSize: DWORD,
                pub dwXCountChars: DWORD,
                pub dwYCountChars: DWORD,
                pub dwFillAttribute: DWORD,
                pub dwFlags: DWORD,
                pub wShowWindow: WORD,
                pub cbReserved2: WORD,
                pub lpReserved2: *mut u8,
                pub hStdInput: HANDLE,
                pub hStdOutput: HANDLE,
                pub hStdError: HANDLE,
            }

            #[repr(C)]
            pub struct PROCESS_INFORMATION {
                pub hProcess: HANDLE,
                pub hThread: HANDLE,
                pub dwProcessId: DWORD,
                pub dwThreadId: DWORD,
            }

            pub const INFINITE: DWORD = 0xFFFFFFFF;

            #[link(name = "kernel32", kind = "raw-dylib")]
            unsafe extern "system" {
                pub fn GetCurrentProcessId() -> DWORD;
                pub fn GetCurrentProcess() -> HANDLE;
                pub fn ExitProcess(uExitCode: DWORD) -> !;
                pub fn CreateThread(
                    lpThreadAttributes: LPVOID,
                    dwStackSize: usize,
                    lpStartAddress: Option<
                        unsafe extern "system" fn(*mut core::ffi::c_void) -> u32,
                    >,
                    lpParameter: LPVOID,
                    dwCreationFlags: DWORD,
                    lpThreadId: *mut DWORD,
                ) -> HANDLE;
                pub fn CreateProcessW(
                    lpApplicationName: LPCWSTR,
                    lpCommandLine: LPWSTR,
                    lpProcessAttributes: LPVOID,
                    lpThreadAttributes: LPVOID,
                    bInheritHandles: BOOL,
                    dwCreationFlags: DWORD,
                    lpEnvironment: LPVOID,
                    lpCurrentDirectory: LPCWSTR,
                    lpStartupInfo: *mut STARTUPINFOW,
                    lpProcessInformation: *mut PROCESS_INFORMATION,
                ) -> BOOL;
                pub fn WaitForSingleObject(hHandle: HANDLE, dwMilliseconds: DWORD) -> DWORD;
                pub fn GetExitCodeProcess(hProcess: HANDLE, lpExitCode: *mut DWORD) -> BOOL;
            }
        }

        pub mod LibraryLoader {
            use super::super::Foundation::*;

            #[link(name = "kernel32", kind = "raw-dylib")]
            unsafe extern "system" {
                pub fn LoadLibraryA(lpLibFileName: LPCSTR) -> HMODULE;
                pub fn GetProcAddress(hModule: HMODULE, lpProcName: LPCSTR) -> LPVOID;
            }
        }

        pub mod Diagnostics {
            pub mod Debug {
                use super::super::super::super::c_void;
                use super::super::super::Foundation::*;

                #[link(name = "kernel32", kind = "raw-dylib")]
                unsafe extern "system" {
                    pub fn FlushInstructionCache(
                        hProcess: HANDLE,
                        lpBaseAddress: LPVOID,
                        dwSize: usize,
                    ) -> BOOL;
                }

                #[link(name = "ntdll", kind = "raw-dylib")]
                unsafe extern "system" {
                    pub fn RtlAddFunctionTable(
                        FunctionTable: *const c_void,
                        EntryCount: u32,
                        BaseAddress: u64,
                    ) -> BOOL;
                }
            }
        }

        pub mod Memory {
            use super::super::Foundation::*;

            pub const MEM_COMMIT: DWORD = 0x1000;
            pub const MEM_RESERVE: DWORD = 0x2000;
            pub const PAGE_READWRITE: DWORD = 0x04;

            #[link(name = "kernel32", kind = "raw-dylib")]
            unsafe extern "system" {
                pub fn VirtualAlloc(
                    lpAddress: LPVOID,
                    dwSize: usize,
                    flAllocationType: DWORD,
                    flProtect: DWORD,
                ) -> LPVOID;
                pub fn VirtualProtect(
                    lpAddress: LPVOID,
                    dwSize: usize,
                    flNewProtect: DWORD,
                    lpflOldProtect: *mut DWORD,
                ) -> BOOL;
                pub fn FlushInstructionCache(
                    hProcess: HANDLE,
                    lpBaseAddress: LPVOID,
                    dwSize: usize,
                ) -> BOOL;
            }
        }

        pub mod Pipes {
            use super::super::Foundation::*;

            pub const PIPE_ACCESS_OUTBOUND: DWORD = 0x00000002;
            pub const PIPE_TYPE_BYTE: DWORD = 0x00000000;
            pub const PIPE_WAIT: DWORD = 0x00000000;

            #[link(name = "kernel32", kind = "raw-dylib")]
            unsafe extern "system" {
                pub fn CreateNamedPipeW(
                    lpName: LPCWSTR,
                    dwOpenMode: DWORD,
                    dwPipeMode: DWORD,
                    nMaxInstances: DWORD,
                    nOutBufferSize: DWORD,
                    nInBufferSize: DWORD,
                    nDefaultTimeOut: DWORD,
                    lpSecurityAttributes: LPVOID,
                ) -> HANDLE;
                pub fn ConnectNamedPipe(hNamedPipe: HANDLE, lpOverlapped: LPVOID) -> BOOL;
            }
        }
    }

    pub mod Storage {
        pub mod FileSystem {
            use super::super::Foundation::*;

            pub const FILE_ATTRIBUTE_NORMAL: DWORD = 0x80;
            pub const FILE_SHARE_READ: DWORD = 0x00000001;
            pub const FILE_GENERIC_WRITE: DWORD = 0x40000000;
            pub const FILE_READ_DATA: DWORD = 0x0001;
            pub const FILE_READ_ATTRIBUTES: DWORD = 0x0080;
            pub const OPEN_EXISTING: DWORD = 3;
            pub const CREATE_ALWAYS: DWORD = 2;
            pub const FILE_BEGIN: DWORD = 0;
            pub const MAX_PATH: DWORD = 260;

            #[link(name = "kernel32", kind = "raw-dylib")]
            unsafe extern "system" {
                pub fn CreateFileW(
                    lpFileName: LPCWSTR,
                    dwDesiredAccess: DWORD,
                    dwShareMode: DWORD,
                    lpSecurityAttributes: LPVOID,
                    dwCreationDisposition: DWORD,
                    dwFlagsAndAttributes: DWORD,
                    hTemplateFile: HANDLE,
                ) -> HANDLE;
                pub fn GetFileSizeEx(hFile: HANDLE, lpFileSize: *mut i64) -> BOOL;
                pub fn SetFilePointerEx(
                    hFile: HANDLE,
                    liDistanceToMove: i64,
                    lpNewFilePointer: *mut i64,
                    dwMoveMethod: DWORD,
                ) -> BOOL;
                pub fn ReadFile(
                    hFile: HANDLE,
                    lpBuffer: LPVOID,
                    nNumberOfBytesToRead: DWORD,
                    lpNumberOfBytesRead: *mut DWORD,
                    lpOverlapped: LPVOID,
                ) -> BOOL;
                pub fn WriteFile(
                    hFile: HANDLE,
                    lpBuffer: LPCVOID,
                    nNumberOfBytesToWrite: DWORD,
                    lpNumberOfBytesWritten: *mut DWORD,
                    lpOverlapped: LPVOID,
                ) -> BOOL;
                pub fn GetTempPathW(nBufferLength: DWORD, lpBuffer: LPWSTR) -> DWORD;
                pub fn DeleteFileW(lpFileName: LPCWSTR) -> BOOL;
            }
        }
    }

    pub mod UI {
        pub mod Shell {
            use super::super::Foundation::*;

            pub type PWSTR = *mut u16;

            #[link(name = "shell32", kind = "raw-dylib")]
            unsafe extern "system" {
                pub fn CommandLineToArgvW(lpCmdLine: LPCWSTR, pNumArgs: *mut i32) -> *mut PWSTR;
            }
        }
    }
}
