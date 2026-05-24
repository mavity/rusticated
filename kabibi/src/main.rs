use ratatui::layout::{Constraint, Direction, Layout};
use ratatui::backend::Backend;
use ratatui::style::{Color, Modifier, Style};
use ratatui::widgets::{Block, Borders, List, ListItem, Paragraph};
use ratatui::Terminal;
use std::io::{AsyncRead, AsyncWrite};
use std::mem;
use std::tty::{disable_raw_mode, enable_raw_mode, size, stdin, stdout, Tty};

mod app;
mod truant;

use app::{App, ChatState};

const PLUME_MAX: usize = 10;

struct RawModeGuard;

impl RawModeGuard {
    fn new() -> Self {
        let _ = enable_raw_mode();
        Self
    }
}

impl Drop for RawModeGuard {
    fn drop(&mut self) {
        let _ = disable_raw_mode();
    }
}

fn main() {
    std::spawn!(async_main());
}

async fn async_main() {
    let _raw_guard = RawModeGuard::new();

    let mut out = stdout();
    let mut input = stdin();
    let mut size_stream = size();

    let (width, height) = size_stream.next().await;
    let backend = truant::TruantBackend::new(width, height);
    let mut terminal = Terminal::new(backend).unwrap();
    terminal.clear().unwrap();

    let mut app = App::new().await;

    terminal.draw(|f| draw_ui(f, &app)).unwrap();
    write_all(&mut out, terminal.backend().buffer.as_bytes()).await;
    terminal.backend_mut().buffer.clear();

    while !app.should_quit {
        let current_width = getattr_width(&terminal);
        let current_height = getattr_height(&terminal);
        let inner_height = current_height.saturating_sub(2) as usize;

        match std::rt::select(size_stream.next(), input.read(Vec::with_capacity(64))).await {
            std::rt::Either::Left((new_width, new_height)) => {
                if new_width != current_width || new_height != current_height {
                    terminal.backend_mut().resize(new_width, new_height);
                    terminal.clear().unwrap();
                }
            }
            std::rt::Either::Right((result, returned)) => {
                if let Ok(count) = result {
                    if count > 0 {
                        handle_input(&mut app, &returned[..count], inner_height).await;
                    }
                }
            }
        }

        terminal.draw(|f| draw_ui(f, &app)).unwrap();
        write_all(&mut out, terminal.backend().buffer.as_bytes()).await;
        terminal.backend_mut().buffer.clear();
    }

    let mut clear = Vec::new();
    clear.extend_from_slice(b"\x1b[2J\x1b[1;1H\x1b[?25h\x1b[0m");
    write_all(&mut out, &clear).await;
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

fn draw_ui(f: &mut ratatui::Frame, app: &App) {
    let vertical_chunks = Layout::default()
        .direction(Direction::Vertical)
        .constraints([Constraint::Percentage(70), Constraint::Percentage(30)].as_ref())
        .split(f.area());
    let top = vertical_chunks[0];
    let bottom = vertical_chunks[1];

    let panel_chunks = Layout::default()
        .direction(Direction::Horizontal)
        .constraints([Constraint::Percentage(50), Constraint::Percentage(50)].as_ref())
        .split(top);

    let left_items: Vec<ListItem> = app
        .left_files
        .iter()
        .enumerate()
        .map(|(index, file)| {
            let mut style = Style::default();
            if index == app.left_selected && app.active_pane == 0 {
                style = style.fg(Color::Yellow).add_modifier(Modifier::BOLD);
            }
            if file.is_dir {
                style = style.fg(Color::Cyan);
            }
            ListItem::new(file.name.clone()).style(style)
        })
        .collect();
    let left_list = List::new(left_items)
        .block(Block::default().borders(Borders::ALL).title("Left"))
        .highlight_style(Style::default().bg(Color::Blue));
    f.render_widget(left_list, panel_chunks[0]);

    if app.chat_state == ChatState::Open {
        let chat_items: Vec<ListItem> = app
            .chat_messages
            .iter()
            .map(|message| ListItem::new(message.clone()))
            .collect();
        let chat_list = List::new(chat_items)
            .block(Block::default().borders(Borders::ALL).title("AI Chat"));
        f.render_widget(chat_list, panel_chunks[1]);
    } else {
        let right_items: Vec<ListItem> = app
            .right_files
            .iter()
            .enumerate()
            .map(|(index, file)| {
                let mut style = Style::default();
                if index == app.right_selected && app.active_pane == 1 {
                    style = style.fg(Color::Yellow).add_modifier(Modifier::BOLD);
                }
                if file.is_dir {
                    style = style.fg(Color::Cyan);
                }
                ListItem::new(file.name.clone()).style(style)
            })
            .collect();
        let right_list = List::new(right_items)
            .block(Block::default().borders(Borders::ALL).title("Right"))
            .highlight_style(Style::default().bg(Color::Blue));
        f.render_widget(right_list, panel_chunks[1]);
    }

    let bottom_chunks = Layout::default()
        .direction(Direction::Vertical)
        .constraints([Constraint::Min(0), Constraint::Length(3)].as_ref())
        .split(bottom);

    let plume_paragraph = Paragraph::new(app.plume.join("\n"))
        .block(Block::default().borders(Borders::ALL).title("Plume"))
        .style(Style::default().fg(Color::White));
    f.render_widget(plume_paragraph, bottom_chunks[0]);

    let prompt_prefix = if app.chat_state == ChatState::Open { "AI> " } else { "$ " };
    let prompt_content = if app.chat_state == ChatState::Open {
        format!("{}{}", prompt_prefix, app.chat_input)
    } else {
        format!("{}{}", prompt_prefix, app.prompt)
    };
    let prompt_paragraph = Paragraph::new(prompt_content)
        .block(Block::default().borders(Borders::ALL).title("Input"))
        .style(Style::default().fg(Color::Green));
    f.render_widget(prompt_paragraph, bottom_chunks[1]);
}

fn getattr_width(terminal: &Terminal<truant::TruantBackend>) -> u16 {
    terminal
        .backend()
        .size()
        .unwrap_or(ratatui::layout::Size {
            width: 0,
            height: 0,
        })
        .width
}

fn getattr_height(terminal: &Terminal<truant::TruantBackend>) -> u16 {
    terminal
        .backend()
        .size()
        .unwrap_or(ratatui::layout::Size {
            width: 0,
            height: 0,
        })
        .height
}

async fn handle_input(app: &mut App, bytes: &[u8], _inner_height: usize) {
    if bytes.is_empty() {
        return;
    }

    match bytes {
        b"\x03" | b"\x1b" => {
            app.should_quit = true;
            return;
        }
        b"\t" => {
            app.handle_tab();
            return;
        }
        b"\x1b[A" => {
            let selected = if app.active_pane == 0 {
                app.left_selected
            } else {
                app.right_selected
            };
            let new_sel = selected.saturating_sub(1);
            if app.active_pane == 0 {
                app.left_selected = new_sel;
            } else if app.active_pane == 1 {
                app.right_selected = new_sel;
            }
            return;
        }
        b"\x1b[B" => {
            let files_len = if app.active_pane == 0 {
                app.left_files.len()
            } else {
                app.right_files.len()
            };
            let selected = if app.active_pane == 0 {
                app.left_selected
            } else {
                app.right_selected
            };
            let new_sel = (selected + 1).min(files_len.saturating_sub(1));
            if app.active_pane == 0 {
                app.left_selected = new_sel;
            } else if app.active_pane == 1 {
                app.right_selected = new_sel;
            }
            return;
        }
        b"\x1b[D" => {
            if app.chat_state == ChatState::Closed {
                app.active_pane = 0;
            }
            return;
        }
        b"\x1b[C" => {
            if app.chat_state == ChatState::Closed {
                app.active_pane = 1;
            }
            return;
        }
        b"\x08" | b"\x7f" => {
            if app.chat_state == ChatState::Open {
                app.chat_input.pop();
            } else {
                app.prompt.pop();
            }
            return;
        }
        b"\r" | b"\n" => {
            if app.chat_state == ChatState::Open {
                let user_msg = mem::take(&mut app.chat_input);
                if !user_msg.is_empty() {
                    app.chat_messages.push(format!("You: {}", user_msg));
                    app.chat_messages.push(format!("AI: {}", user_msg));
                }
                return;
            }

            if app.active_pane == 0 || app.active_pane == 1 {
                if open_selected_directory(app).await {
                    return;
                }
            }

            let cmd = mem::take(&mut app.prompt);
            if !cmd.is_empty() {
                app.plume.push(format!("Executed: {}", cmd));
                if app.plume.len() > PLUME_MAX {
                    app.plume.drain(0..1);
                }
            }
            return;
        }
        _ => {}
    }

    for &byte in bytes {
        if byte.is_ascii_graphic() || byte == b' ' {
            if app.chat_state == ChatState::Open {
                app.chat_input.push(byte as char);
            } else {
                app.prompt.push(byte as char);
            }
        }
    }
}

async fn open_selected_directory(app: &mut App) -> bool {
    let (active_dir, active_sel, files) = if app.active_pane == 0 {
        (
            &mut app.current_left_dir,
            &mut app.left_selected,
            &app.left_files,
        )
    } else {
        (
            &mut app.current_right_dir,
            &mut app.right_selected,
            &app.right_files,
        )
    };

    let selected = *active_sel;
    if selected >= files.len() {
        return false;
    }

    let target = files[selected].clone();
    if !target.is_dir {
        return false;
    }

    app.history.insert(active_dir.clone(), selected);
    App::push_path(active_dir, &target.name);

    let new_path = active_dir.clone();
    let new_files = App::load_dir(&new_path).await;
    let restored_sel = app.history.get(&new_path).copied().unwrap_or(0);
    let final_sel = restored_sel.min(new_files.len().saturating_sub(1));

    if app.active_pane == 0 {
        app.left_files = new_files;
        app.left_selected = final_sel;
    } else {
        app.right_files = new_files;
        app.right_selected = final_sel;
    }

    true
}
