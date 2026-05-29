use std::prelude::rust_2024::*;

use std::collections::HashMap;
use std::fs::File;
use std::sync::mpsc;

/// A file descriptor or file owned by the host.
pub enum HandleKind {
    Fd(i32),
    File(File),
    Process(std::process::Child),
    Dir(std::fs::ReadDir, Vec<u8>),
}

pub enum FileOpResult {
    PathOpen {
        ov_ptr: u32,
        result: Result<HandleKind, u32>,
    },
    PathStat {
        ov_ptr: u32,
        result: Result<StatInfo, u32>,
    },
    Read {
        ov_ptr: u32,
        handle: u64,
        guest_ptr: u32,
        guest_len: u32,
        data: Vec<u8>,
        error: u32,
        file: File,
    },
    Write {
        ov_ptr: u32,
        handle: u64,
        written: u64,
        error: u32,
        file: File,
    },
}

pub struct StatInfo {
    pub len: u64,
    pub is_dir: bool,
    pub is_symlink: bool,
    pub readonly: bool,
    pub mode: u32,
    pub nlink: u64,
    pub uid: u32,
    pub gid: u32,
    pub inode: u64,
    pub mtime_ns: u64,
    pub atime_ns: u64,
    pub ctime_ns: u64,
}

pub struct PendingOp {
    pub ov_ptr: u32,
    pub guest_ptr: u32,
    pub guest_len: u32,
    pub _fd: i32,
}

// -- EpollState: used for non-stdin stream fds --------------------------------

pub struct EpollState {
    pub pending: HashMap<u64, PendingOp>,
    pub next_token: u64,
}

impl EpollState {
    pub fn new() -> Self {
        Self {
            pending: HashMap::new(),
            next_token: 1,
        }
    }

    /// Register fd for readable events. Returns a token identifying this registration.
    /// Not used for stdin (fd=0) -- that is handled via the stdin reader thread.
    #[allow(dead_code)]
    pub fn register_read(&mut self, _fd: i32) -> u64 {
        let token = self.next_token;
        self.next_token += 1;
        token
    }

    pub fn poll(&mut self, _timeout_ms: i32) -> Vec<u64> {
        Vec::new()
    }
}

impl Drop for EpollState {
    fn drop(&mut self) {}
}

// -- HostState ----------------------------------------------------------------

pub struct HostState {
    pub handles: HashMap<u64, HandleKind>,
    pub stats: HashMap<u64, StatInfo>,
    pub epoll: EpollState,
    pub timers: HashMap<u32, std::time::Instant>,
    pub next_handle: u64,
    pub next_stat: u64,
    pub stdin_rx: mpsc::Receiver<Vec<u8>>,
    pub stdin_buf: Vec<u8>,
    pub stdin_pending: Option<PendingOp>,
    pub file_op_tx: mpsc::Sender<FileOpResult>,
    pub file_op_rx: mpsc::Receiver<FileOpResult>,
    pub child_wait_pending: Vec<(u32, u64)>,
}

impl HostState {
    pub fn new() -> Self {
        let mut handles: HashMap<u64, HandleKind> = HashMap::new();
        handles.insert(0, HandleKind::Fd(0));
        handles.insert(1, HandleKind::Fd(1));
        handles.insert(2, HandleKind::Fd(2));

        let (stdin_tx, stdin_rx) = mpsc::channel();
        let (file_op_tx, file_op_rx) = mpsc::channel();
        let _ = stdin_tx;

        Self {
            handles,
            stats: HashMap::new(),
            epoll: EpollState::new(),
            timers: HashMap::new(),
            next_handle: 3,
            next_stat: 1,
            stdin_rx,
            stdin_buf: Vec::new(),
            stdin_pending: None,
            file_op_tx,
            file_op_rx,
            child_wait_pending: Vec::new(),
        }
    }

    pub fn alloc_handle(&mut self, kind: HandleKind) -> u64 {
        let h = self.next_handle;
        self.next_handle += 1;
        self.handles.insert(h, kind);
        h
    }

    pub fn alloc_stat(&mut self, info: StatInfo) -> u64 {
        let h = self.next_stat;
        self.next_stat += 1;
        self.stats.insert(h, info);
        h
    }

    pub fn fd_for(&self, handle: u64) -> Option<i32> {
        match self.handles.get(&handle)? {
            HandleKind::Fd(fd) => Some(*fd),
            HandleKind::File(_) => Some(-1),
            HandleKind::Process(_) => None,
            HandleKind::Dir(_, _) => None,
        }
    }

    pub fn is_regular_file(&self, handle: u64) -> bool {
        matches!(self.handles.get(&handle), Some(HandleKind::File(_)))
    }
}
