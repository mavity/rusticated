use std::collections::HashMap;
use std::fs::read_dir;

#[derive(Clone)]
pub struct FileItem {
    pub name: String,
    pub is_dir: bool,
}

pub struct App {
    pub left_files: Vec<FileItem>,
    pub right_files: Vec<FileItem>,
    pub left_selected: usize,
    pub right_selected: usize,
    pub current_left_dir: String,
    pub current_right_dir: String,
    pub should_quit: bool,
    pub active_pane: u8,
    pub history: HashMap<String, usize>,
}

impl App {
    pub async fn new() -> Self {
        print!(" std::env::current_dir().unwrap_or_else()...");
        let p = std::env::current_dir().unwrap_or_else(|_| std::path::PathBuf::from("."));
        print!(" p.to_string_lossy().into_owned()...");
        let p_str = p.to_string_lossy().into_owned();

        print!(" left_files=Self::load_dir()...");
        let left_files = Self::load_dir(&p_str).await;
        print!(" OK[{}]", left_files.len());

        print!(" right_files=Self::load_dir()...");
        let right_files = Self::load_dir(&p_str).await;
        print!(" OK[{}]", right_files.len());

        print!(" Self...");
        Self {
            left_files: left_files,
            right_files: right_files,
            left_selected: 0,
            right_selected: 0,
            current_left_dir: p_str.clone(),
            current_right_dir: p_str,
            should_quit: false,
            active_pane: 0,
            history: HashMap::new(),
        }
    }

    pub fn push_path(dir: &mut String, entry: &str) {
        if entry == ".." {
            if dir == "." || dir == ".." || dir.ends_with("/..") || dir.ends_with("\\..") {
                dir.push_str("/..");
            } else {
                if let Some(pos) = dir.rfind(|c| c == '/' || c == '\\') {
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
        let mut files = vec![FileItem {
            name: String::from(".."),
            is_dir: true,
        }];
        print!(" read_dir({})...", path);
        if let Ok(dir) = read_dir(path).await {
            for entry in dir {
                if let Ok(entry) = entry {
                    let is_dir = entry.metadata().map(|m| m.is_dir()).unwrap_or(false);
                    files.push(FileItem {
                        name: entry.file_name().to_string(),
                        is_dir,
                    });
                }
            }
        }
        print!(" OK[{} entries]", files.len());

        print!(" sorting...");
        files.sort_unstable_by(|a, b| {
            if a.name == ".." {
                return std::cmp::Ordering::Less;
            }
            if b.name == ".." {
                return std::cmp::Ordering::Greater;
            }
            b.is_dir
                .cmp(&a.is_dir)
                .then_with(|| a.name.to_lowercase().cmp(&b.name.to_lowercase()))
        });
        print!(" OK");
        files
    }
}
