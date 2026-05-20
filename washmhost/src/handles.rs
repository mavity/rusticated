use std::collections::HashMap;
use std::fs::File;
use std::sync::mpsc;

/// A file descriptor or file owned by the host.
pub enum HandleKind {
    Fd(i32),
    File(File),
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
    pub poller: polling::Poller,
    pub pending: HashMap<u64, PendingOp>,
    pub next_token: u64,
}

impl EpollState {
    pub fn new() -> anyhow::Result<Self> {
        Ok(Self {
            poller: polling::Poller::new()?,
            pending: HashMap::new(),
            next_token: 1,
        })
    }

    /// Register fd for readable events. Returns a token identifying this registration.
    /// Not used for stdin (fd=0) -- that is handled via the stdin reader thread.
    #[allow(dead_code)]
    pub fn register_read(&mut self, _fd: i32) -> anyhow::Result<u64> {
        let token = self.next_token;
        self.next_token += 1;
        Ok(token)
    }

    pub fn poll(&mut self, timeout_ms: i32) -> anyhow::Result<Vec<u64>> {
        let mut events = polling::Events::with_capacity(std::num::NonZero::new(32).unwrap());
        let timeout = if timeout_ms >= 0 {
            Some(std::time::Duration::from_millis(timeout_ms as u64))
        } else {
            None
        };
        self.poller.wait(&mut events, timeout)?;
        let mut fired = Vec::new();
        for ev in events.iter() {
            fired.push(ev.key as u64);
        }
        Ok(fired)
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
}

impl HostState {
    pub fn new() -> anyhow::Result<Self> {
        let mut handles: HashMap<u64, HandleKind> = HashMap::new();
        handles.insert(0, HandleKind::Fd(0));
        handles.insert(1, HandleKind::Fd(1));
        handles.insert(2, HandleKind::Fd(2));

        let (stdin_tx, stdin_rx) = mpsc::channel();
        std::thread::spawn(move || {
            use std::io::Read;
            let mut buf = [0u8; 256];
            loop {
                match std::io::stdin().read(&mut buf) {
                    Ok(0) => break,
                    Ok(n) => {
                        if stdin_tx.send(buf[..n].to_vec()).is_err() {
                            break;
                        }
                    }
                    Err(_) => break,
                }
            }
        });

        Ok(Self {
            handles,
            stats: HashMap::new(),
            epoll: EpollState::new()?,
            timers: HashMap::new(),
            next_handle: 3,
            next_stat: 1,
            stdin_rx,
            stdin_buf: Vec::new(),
            stdin_pending: None,
        })
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
        }
    }

    pub fn is_regular_file(&self, handle: u64) -> bool {
        matches!(self.handles.get(&handle), Some(HandleKind::File(_)))
    }
}
