use std::collections::HashMap;
use ::std::time::{Instant, Duration};
use std::fs::read_dir;

#[derive(Clone)]
pub struct FileItem {
    pub name: String,
    pub is_dir: bool,
}

#[derive(Debug, PartialEq, Eq)]
pub enum ChatState {
    Closed,
    Open,
}

pub struct App {
    // File manager state
    pub left_files: Vec<FileItem>,
    pub right_files: Vec<FileItem>,
    pub left_selected: usize,
    pub right_selected: usize,
    pub current_left_dir: String,
    pub current_right_dir: String,
    // Bash prompt / plume state
    pub prompt: String,
    pub plume: Vec<String>, // older output lines (in‑memory)
    // AI chat state
    pub chat_state: ChatState,
    pub chat_input: String,
    pub chat_messages: Vec<String>,
    pub chat_scroll: usize, // 0 means pinned to latest message
    // focus / misc
    pub should_quit: bool,
    pub active_pane: u8, // 0 = left, 1 = right, 2 = chat when open
    // Tab handling state
    pub tab_counter: u8, // counts consecutive Tab presses
    pub last_tab_instant: Option<std::time::Instant>,
    pub history: HashMap<String, usize>,
}

impl App {
    pub async fn new() -> Self {
        extern crate std as real_std;
        let p = real_std::env::current_dir().unwrap_or_else(|_| real_std::path::PathBuf::from("."));
        let p_str = p.to_string_lossy().into_owned();
        Self {
            left_files: Self::load_dir(&p_str).await,
            right_files: Self::load_dir(&p_str).await,
            left_selected: 0,
            right_selected: 0,
            current_left_dir: p_str.clone(),
            current_right_dir: p_str,
            prompt: String::new(),
            plume: Vec::new(),
            chat_state: ChatState::Closed,
            chat_input: String::new(),
            chat_messages: Vec::new(),
            chat_scroll: 0,
            should_quit: false,
            active_pane: 0,
            // initialize tab handling
            tab_counter: 0,
            last_tab_instant: None,
            history: HashMap::new(),
        }
    }

    /// Handle Tab key presses for UI navigation.
    ///
    /// - Single Tab when AI panel is collapsed toggles active file manager pane.
    /// - Double Tab (within 300ms) expands AI panel and focuses chat input.
    /// - Any Tab when AI panel is expanded collapses it back.
    pub fn handle_tab(&mut self) {
        // Determine time since last tab press
        let now = Instant::now();
        let is_fast = match self.last_tab_instant {
            Some(prev) => now.duration_since(prev) <= Duration::from_millis(300),
            None => false,
        };
        // Update counter based on timing
        if is_fast {
            self.tab_counter = self.tab_counter.saturating_add(1);
        } else {
            self.tab_counter = 1; // reset to first press
        }
        self.last_tab_instant = Some(now);

        match self.chat_state {
            ChatState::Closed => {
                // AI panel is collapsed
                if self.tab_counter == 1 {
                    // Toggle between left/right pane
                    self.active_pane = if self.active_pane == 0 { 1 } else { 0 };
                } else if self.tab_counter >= 2 {
                    // Expand AI panel and focus chat
                    self.chat_state = ChatState::Open;
                    self.active_pane = 2;
                    self.tab_counter = 0;
                }
            }
            ChatState::Open => {
                // AI panel is open, any tab collapses it
                self.chat_state = ChatState::Closed;
                // Return focus to left pane by default
                self.active_pane = 0;
                self.tab_counter = 0;
                self.last_tab_instant = None;
            }
        }
    }
    pub fn push_path(dir: &mut String, entry: &str) {
        if entry == ".." {
            if dir == "." || dir == ".." || dir.ends_with("/..") || dir.ends_with("\\..") {
                dir.push_str("/..");
            } else if let Some(pos) = dir.rfind(|c| c == '/' || c == '\\') {
                if pos == 2 && dir.chars().nth(1) == Some(':') {
                    dir.truncate(pos + 1);
                } else if pos == 0 {
                    dir.truncate(1);
                } else {
                    dir.truncate(pos);
                }
                if dir.is_empty() {
                    *dir = ".".to_string();
                }
            } else {
                *dir = ".".to_string();
            }
        } else {
            if dir == "." {
                *dir = entry.to_string();
            } else if dir.ends_with('/') || dir.ends_with('\\') {
                dir.push_str(entry);
            } else {
                dir.push('/');
                dir.push_str(entry);
            }
        }
    }

    pub async fn load_dir(path: &str) -> Vec<FileItem> {
        let mut files = vec![FileItem { name: String::from(".."), is_dir: true }];
        if let Ok(dir) = read_dir(path).await {
            for entry in dir {
                if let Ok(entry) = entry {
                    let is_dir = entry.metadata().map(|m| m.is_dir()).unwrap_or(false);
                    files.push(FileItem { name: entry.file_name().to_string(), is_dir });
                }
            }
        }
        files.sort_by(|a, b| {
            if a.name == ".." { return std::cmp::Ordering::Less; }
            if b.name == ".." { return std::cmp::Ordering::Greater; }
            b.is_dir.cmp(&a.is_dir).then_with(|| a.name.to_lowercase().cmp(&b.name.to_lowercase()))
        });
        files
    }
}
