pub mod app;
pub mod events;
mod truant;
pub mod ui;

use app::App;
use events::handle_input;
use ratatui::backend::Backend;
use truant::TruantBackend;
use ui::draw_ui;

use ratatui::Terminal;
use std::io::{AsyncRead, AsyncWrite};
use std::tty::{Tty, stdin, stdout};

struct RawModeGuard;

impl RawModeGuard {
    fn new() -> Self {
        let _ = std::tty::enable_raw_mode();
        Self
    }
}

impl Drop for RawModeGuard {
    fn drop(&mut self) {
        let _ = std::tty::disable_raw_mode();
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
    print!("size_stream");
    let mut size_stream = std::tty::size();

    let (w, h) = size_stream.next().await;
    println!("{}x{}", w, h);

    print!("truant backend/terminal...");
    let backend = TruantBackend::new(w, h);
    let mut terminal = Terminal::new(backend).unwrap();
    terminal.clear().unwrap();
    println!(" OK");

    print!("App:new()...");
    let mut app = App::new().await;
    println!(" OK");


    print!("Initial draw...");
    // Initial draw
    terminal.draw(|f| draw_ui(f, &mut app)).unwrap();
    print!(" write_all...");
    write_all(&mut out, terminal.backend().buffer.as_bytes()).await;
    print!(" backend_mut().buffer.clear()...");
    terminal.backend_mut().buffer.clear();
    println!(" OK");

    while !app.should_quit {
        // Calculate approximate inner height roughly matching the layout
        let cur_h = getattr_height(&terminal);
        let inner_height = cur_h.saturating_sub(2) as usize;

        match std::rt::select(
            size_stream.next(),
            input.read(std::vec::Vec::with_capacity(32)),
        )
        .await
        {
            std::rt::Either::Left((cur_w, cur_h)) => {
                if cur_w != getattr_width(&terminal) || cur_h != getattr_height(&terminal) {
                    terminal.backend_mut().resize(cur_w, cur_h);
                    terminal.clear().unwrap();
                }
            }
            std::rt::Either::Right((res, returned)) => {
                if let Ok(n) = res {
                    if n > 0 {
                        let bytes = &returned[..n];
                        handle_input(&mut app, bytes, inner_height).await;
                    }
                }
            }
        }

        terminal.draw(|f| draw_ui(f, &mut app)).unwrap();
        write_all(&mut out, terminal.backend().buffer.as_bytes()).await;
        terminal.backend_mut().buffer.clear();
    }

    let mut clear_str = std::vec::Vec::new();
    clear_str.extend_from_slice(b"\x1b[2J\x1b[1;1H\x1b[?25h\x1b[0m"); // make sure to restore cursor and colors
    write_all(&mut out, &clear_str).await;
}

fn getattr_width(terminal: &Terminal<TruantBackend>) -> u16 {
    terminal
        .backend()
        .size()
        .unwrap_or(ratatui::layout::Size {
            width: 0,
            height: 0,
        })
        .width
}

fn getattr_height(terminal: &Terminal<TruantBackend>) -> u16 {
    terminal
        .backend()
        .size()
        .unwrap_or(ratatui::layout::Size {
            width: 0,
            height: 0,
        })
        .height
}

fn main() {
    std::spawn!(async_main());
}
