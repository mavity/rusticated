#[cfg(windows)]
#[path = "os/windows.rs"]
mod platform;
#[cfg(target_os = "linux")]
#[path = "os/linux.rs"]
mod platform;
#[cfg(target_os = "macos")]
#[path = "os/macos.rs"]
mod platform;

pub const EINVAL: i32 = 22;

pub use platform::{close_child, close_pty, poll, resize, signal, spawn};

pub struct SpawnResult {
    bytes: [u8; 512],
    len: usize,
}

impl SpawnResult {
    pub fn new(
        id: i32,
        pid: i32,
        read_fd: i32,
        write_fd: i32,
        input: &[u8],
        output: &[u8],
    ) -> Self {
        let mut result = Self {
            bytes: [0; 512],
            len: 0,
        };
        result.push_i32(id);
        result.push(b'|');
        result.push_i32(pid);
        result.push(b'|');
        result.push_i32(read_fd);
        result.push(b'|');
        result.push_i32(write_fd);
        result.push(b'|');
        result.push_bytes(input);
        result.push(b'|');
        result.push_bytes(output);
        result
    }

    pub fn as_bytes(&self) -> &[u8] {
        &self.bytes[..self.len]
    }

    fn push(&mut self, byte: u8) {
        self.bytes[self.len] = byte;
        self.len += 1;
    }

    fn push_bytes(&mut self, bytes: &[u8]) {
        for &byte in bytes {
            self.push(byte);
        }
    }

    fn push_i32(&mut self, value: i32) {
        let mut n = value.unsigned_abs();
        let mut digits = [0u8; 10];
        let mut count = 0;
        loop {
            digits[count] = b'0' + (n % 10) as u8;
            count += 1;
            n /= 10;
            if n == 0 {
                break;
            }
        }
        if value < 0 {
            self.push(b'-');
        }
        while count > 0 {
            count -= 1;
            self.push(digits[count]);
        }
    }
}

pub struct SpawnSpec {
    pub program: *const u8,
    pub argv: [*const u8; 256],
    pub argc: usize,
    pub envp: [*const u8; 512],
    pub envc: usize,
    pub cwd: *const u8,
}

impl SpawnSpec {
    pub fn parse(config: &mut [u8]) -> Result<Self, i32> {
        let mut spec = Self {
            program: core::ptr::null(),
            argv: [core::ptr::null(); 256],
            argc: 0,
            envp: [core::ptr::null(); 512],
            envc: 0,
            cwd: core::ptr::null(),
        };
        let Some(program_end) = config.iter().position(|&b| b == 0) else {
            return Err(EINVAL);
        };
        if program_end == 0 {
            return Err(EINVAL);
        }
        spec.program = config.as_ptr();
        spec.argv[0] = spec.program;
        spec.argc = 1;

        let mut section = 0u8;
        let mut start = program_end + 1;
        let mut i = start;
        while i < config.len() {
            if config[i] != 0 {
                i += 1;
                continue;
            }
            if i == start {
                section += 1;
                if section > 3 {
                    break;
                }
            } else {
                let ptr = unsafe { config.as_ptr().add(start) };
                match section {
                    0 if spec.argc < spec.argv.len() - 1 => {
                        spec.argv[spec.argc] = ptr;
                        spec.argc += 1;
                    }
                    1 if spec.envc < spec.envp.len() - 1 => {
                        spec.envp[spec.envc] = ptr;
                        spec.envc += 1;
                    }
                    2 if spec.cwd.is_null() => spec.cwd = ptr,
                    _ => {}
                }
            }
            start = i + 1;
            i += 1;
        }
        spec.argv[spec.argc] = core::ptr::null();
        spec.envp[spec.envc] = core::ptr::null();
        Ok(spec)
    }
}
