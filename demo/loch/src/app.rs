use rusticated::fs::read_dir;

pub struct App {
    pub left_files: Vec<String>,
    pub right_files: Vec<String>,
    pub left_selected: usize,
    pub right_selected: usize,
    pub current_left_dir: String,
    pub current_right_dir: String,
    pub should_quit: bool,
    pub active_pane: u8,
}

impl App {
    pub async fn new() -> Self {
        Self {
            left_files: Self::load_dir(".").await,
            right_files: Self::load_dir(".").await,
            left_selected: 0,
            right_selected: 0,
            current_left_dir: String::from("."),
            current_right_dir: String::from("."),
            should_quit: false,
            active_pane: 0,
        }
    }

    pub async fn load_dir(path: &str) -> Vec<String> {
        let mut files = vec![String::from("..")];
        if let Ok(dir) = read_dir(path).await {
            for entry in dir {
                if let Ok(entry) = entry {
                    files.push(entry.file_name().to_string());
                }
            }
        }
        files.sort();
        files
    }
}
