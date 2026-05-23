mod truant;
pub mod app;
pub mod ui;
pub mod events;

use ratatui::backend::Backend;
use truant::TruantBackend;
use app::App;
use ui::draw_ui;
use events::handle_input;

use ratatui::Terminal;
use rusticated::io::{AsyncRead, AsyncWrite};
use rusticated::tty::{stdin, stdout, Tty};
use rusticated::io;

struct RawModeGuard;

impl RawModeGuard {
    fn new() -> Self {
        let _ = rusticated::tty::enable_raw_mode();
        Self
    }
}

impl Drop for RawModeGuard {
    fn drop(&mut self) {
        let _ = rusticated::tty::disable_raw_mode();
    }
}

async fn write_all(out: &mut Tty, bytes: &[u8]) {
    let mut buf = bytes.to_vec();
    while !buf.is_empty() {
        let (result, mut returned) = out.write(buf).await;
        match result {
            Ok(written) => {
                buf = returned.split_off(written);
            }
            Err(_) => break,
        }
    }
}

async fn async_main() {
    let _raw_guard = RawModeGuard::new();
    let mut out = stdout();
    let mut input = stdin();
    
    let (w, h) = rusticated::tty::get_size(1).unwrap_or((80, 24));
    let backend = TruantBackend::new(w, h);
    let mut terminal = Terminal::new(backend).unwrap();
    terminal.clear().unwrap();

    let mut app = App::new().await;

    while !app.should_quit {
        // Handle resizing dynamically
        let (cur_w, cur_h) = rusticated::tty::get_size(1).unwrap_or((80, 24));
        if cur_w != getattr_width(&terminal) || cur_h != getattr_height(&terminal) {
            terminal.backend_mut().resize(cur_w, cur_h);
            terminal.clear().unwrap();
        }

        terminal.draw(|f| draw_ui(f, &mut app)).unwrap();

        write_all(&mut out, terminal.backend().buffer.as_bytes()).await;
        terminal.backend_mut().buffer.clear();

        // Calculate approximate inner height roughly matching the layout
        let cur_h = getattr_height(&terminal);
        let inner_height = cur_h.saturating_sub(2) as usize;

        let (res, returned) = input.read(Vec::with_capacity(32)).await;
        if let Ok(n) = res {
            if n > 0 {
                let bytes = &returned[..n];
                handle_input(&mut app, bytes, inner_height).await;
            }
        }
    }

    let mut clear_str = rusticated::vec::Vec::new();
    clear_str.extend_from_slice(b"\x1b[2J\x1b[1;1H\x1b[?25h\x1b[0m"); // make sure to restore cursor and colors
    write_all(&mut out, &clear_str).await;
}

fn getattr_width(terminal: &Terminal<TruantBackend>) -> u16 {
    terminal.backend().size().unwrap_or(ratatui::layout::Size { width: 0, height: 0 }).width
}

fn getattr_height(terminal: &Terminal<TruantBackend>) -> u16 {
    terminal.backend().size().unwrap_or(ratatui::layout::Size { width: 0, height: 0 }).height
}

rusticated::main!(async_main());
