use ratatui::layout::{Constraint, Direction, Layout};
use ratatui::backend::Backend;
use ratatui::layout::Rect;
use ratatui::style::{Color, Style};
use ratatui::widgets::{Block, BorderType, Borders, List, ListItem, Paragraph, Wrap};
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
    let area = f.area();
    if area.height == 0 || area.width == 0 {
        return;
    }

    let prompt_prefix = if app.chat_state == ChatState::Open {
        "AI> "
    } else {
        "$ "
    };
    let prompt_body = if app.chat_state == ChatState::Open {
        app.chat_input.as_str()
    } else {
        app.prompt.as_str()
    };
    let prompt_content = format!("{}{}", prompt_prefix, prompt_body);
    let prompt_height = prompt_line_count(area.width, prompt_content.len());

    let panel_top_margin = 1u16;
    let plume_footer_lines = 4u16;
    let panel_reserved_bottom = prompt_height.saturating_add(plume_footer_lines);
    let panel_height = area
        .height
        .saturating_sub(panel_top_margin)
        .saturating_sub(panel_reserved_bottom)
        .max(1);
    let overlay_area = Rect {
        x: area.x,
        y: area.y.saturating_add(panel_top_margin),
        width: area.width,
        height: panel_height,
    };
    let prompt_area = Rect {
        x: area.x,
        y: area.y + area.height.saturating_sub(prompt_height),
        width: area.width,
        height: prompt_height,
    };

    let plume_canvas = Paragraph::new(bottom_aligned_plume_text(app, area.height))
        .style(Style::default().bg(Color::Black).fg(Color::DarkGray));
    f.render_widget(plume_canvas, area);

    let chat_full_width = sidebar_chat_width(area.width);
    let peek_width = 8u16.min(area.width.saturating_sub(2)).max(1);
    let (files_area, chat_rect) = if app.chat_state == ChatState::Open {
        let files_width = overlay_area.width.saturating_sub(chat_full_width).max(2);
        (
            Rect {
                x: overlay_area.x,
                y: overlay_area.y,
                width: files_width,
                height: overlay_area.height,
            },
            Rect {
                x: overlay_area.x.saturating_add(files_width),
                y: overlay_area.y,
                width: chat_full_width,
                height: overlay_area.height,
            },
        )
    } else {
        let files_width = overlay_area.width.saturating_sub(peek_width).max(2);
        (
            Rect {
                x: overlay_area.x,
                y: overlay_area.y,
                width: files_width,
                height: overlay_area.height,
            },
            Rect {
                // Intentionally render part of a full-width panel outside the viewport so
                // the visible sliver is the left-most part of a normal-width chat panel.
                x: overlay_area.x.saturating_add(files_width),
                y: overlay_area.y,
                width: chat_full_width,
                height: overlay_area.height,
            },
        )
    };

    let panel_chunks = Layout::default()
        .direction(Direction::Horizontal)
        .constraints([Constraint::Percentage(50), Constraint::Percentage(50)].as_ref())
        .split(files_area);

    let list_style = Style::default().bg(Color::Blue).fg(Color::Cyan);
    let highlight_style = Style::default().bg(Color::Cyan).fg(Color::Black);
    let border_style = Style::default().fg(Color::Cyan);

    let left_title_style = if app.active_pane == 0 {
        highlight_style
    } else {
        Style::default().fg(Color::Cyan)
    };
    let right_title_style = if app.active_pane == 1 {
        highlight_style
    } else {
        Style::default().fg(Color::Cyan)
    };

    f.render_widget(Block::default().style(list_style), panel_chunks[0]);

    let left_items: Vec<ListItem> = app
        .left_files
        .iter()
        .enumerate()
        .map(|(_index, file)| {
            let style = if file.is_dir {
                Style::default().fg(Color::White)
            } else {
                Style::default().fg(Color::Cyan)
            };
            ListItem::new(file.name.clone()).style(style)
        })
        .collect();
    let left_list = List::new(left_items)
        .block(
            Block::default()
                .borders(Borders::ALL)
                .border_type(BorderType::Double)
                .title(ratatui::text::Line::from(" Left ").style(left_title_style))
                .border_style(border_style)
                .style(list_style),
        )
        .style(list_style)
        .highlight_style(highlight_style);
    f.render_widget(left_list, panel_chunks[0]);

    f.render_widget(Block::default().style(list_style), panel_chunks[1]);

    let right_items: Vec<ListItem> = app
        .right_files
        .iter()
        .enumerate()
        .map(|(_index, file)| {
            let style = if file.is_dir {
                Style::default().fg(Color::White)
            } else {
                Style::default().fg(Color::Cyan)
            };
            ListItem::new(file.name.clone()).style(style)
        })
        .collect();
    let right_list = List::new(right_items)
        .block(
            Block::default()
                .borders(Borders::ALL)
                .border_type(BorderType::Double)
                .title(ratatui::text::Line::from(" Right ").style(right_title_style))
                .border_style(border_style)
                .style(list_style),
        )
        .style(list_style)
        .highlight_style(highlight_style);
    f.render_widget(right_list, panel_chunks[1]);

    let chat_items: Vec<ListItem> = app
        .chat_messages
        .iter()
        .map(|message| ListItem::new(message.clone()).style(Style::default().fg(Color::White)))
        .collect();
    let chat_title = if app.chat_state == ChatState::Open {
        " AI Chat "
    } else {
        " AI> "
    };
    let chat_list = List::new(chat_items)
        .block(
            Block::default()
                .borders(Borders::ALL)
                .title(chat_title)
                .border_style(Style::default().fg(Color::Indexed(242)))
                .style(Style::default().bg(Color::Indexed(234)).fg(Color::White)),
        )
        .style(Style::default().bg(Color::Indexed(234)).fg(Color::White));
    f.render_widget(chat_list, chat_rect);

    let prompt_paragraph = Paragraph::new(prompt_content)
        .style(Style::default().bg(Color::Black).fg(Color::Gray))
        .wrap(Wrap { trim: false });
    f.render_widget(prompt_paragraph, prompt_area);
}

fn sidebar_chat_width(total_width: u16) -> u16 {
    let preferred = ((total_width as u32 * 35) / 100) as u16;
    preferred.clamp(30, total_width.saturating_sub(1).max(1))
}

fn bottom_aligned_plume_text(app: &App, height: u16) -> String {
    let max_lines = height.max(1) as usize;
    let total = app.plume.len();
    let start = total.saturating_sub(max_lines);
    let visible = &app.plume[start..];
    let pad_count = max_lines.saturating_sub(visible.len());

    let mut out = String::new();
    for _ in 0..pad_count {
        out.push('\n');
    }
    out.push_str(&visible.join("\n"));
    out
}

fn prompt_line_count(width: u16, text_len: usize) -> u16 {
    let cols = width.max(1) as usize;
    let lines = text_len.max(1).div_ceil(cols);
    lines.min(u16::MAX as usize) as u16
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

            if !app.prompt.is_empty() {
                let cmd = mem::take(&mut app.prompt);
                app.plume.push(format!("Executed: {}", cmd));
                if app.plume.len() > PLUME_MAX {
                    app.plume.drain(0..1);
                }
                return;
            }

            if app.chat_state == ChatState::Closed && (app.active_pane == 0 || app.active_pane == 1) {
                if open_selected_directory(app).await {
                    return;
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
