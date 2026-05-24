use ratatui::layout::{Alignment, Constraint, Direction, Layout};
use ratatui::backend::Backend;
use ratatui::layout::Rect;
use ratatui::style::{Color, Style};
use ratatui::widgets::{Block, Borders, List, ListItem, ListState, Paragraph, Wrap};
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
    let plume_area = Rect {
        x: area.x,
        y: area.y,
        width: area.width,
        height: area.height.saturating_sub(prompt_height),
    };

    let plume_canvas = Paragraph::new(bottom_aligned_plume_text(app, plume_area.height))
        .style(Style::default().bg(Color::Black).fg(Color::DarkGray));
    f.render_widget(plume_canvas, plume_area);

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

    let left_row_width = panel_chunks[0].width.saturating_sub(2).max(1) as usize;
    let left_items: Vec<ListItem> = app
        .left_files
        .iter()
        .enumerate()
        .map(|(_index, file)| {
            let style = if file.is_dir {
                Style::default().bg(Color::Blue).fg(Color::White)
            } else {
                Style::default().bg(Color::Blue).fg(Color::Cyan)
            };
            ListItem::new(pad_or_trim(&file.name, left_row_width)).style(style)
        })
        .collect();
    let left_list = List::new(left_items)
        .block(
            Block::default()
                .borders(Borders::ALL)
                .title(
                    ratatui::text::Line::from(format!(" {} ", app.current_left_dir))
                        .style(left_title_style),
                )
                .title_alignment(Alignment::Center)
                .border_style(border_style)
                .style(list_style),
        )
        .style(list_style)
        .highlight_style(highlight_style);
    let mut left_state = ListState::default();
    left_state.select((app.active_pane == 0).then_some(app.left_selected));
    f.render_stateful_widget(left_list, panel_chunks[0], &mut left_state);

    f.render_widget(Block::default().style(list_style), panel_chunks[1]);

    let right_row_width = panel_chunks[1].width.saturating_sub(2).max(1) as usize;
    let right_items: Vec<ListItem> = app
        .right_files
        .iter()
        .enumerate()
        .map(|(_index, file)| {
            let style = if file.is_dir {
                Style::default().bg(Color::Blue).fg(Color::White)
            } else {
                Style::default().bg(Color::Blue).fg(Color::Cyan)
            };
            ListItem::new(pad_or_trim(&file.name, right_row_width)).style(style)
        })
        .collect();
    let right_list = List::new(right_items)
        .block(
            Block::default()
                .borders(Borders::ALL)
                .title(
                    ratatui::text::Line::from(format!(" {} ", app.current_right_dir))
                        .style(right_title_style),
                )
                .title_alignment(Alignment::Center)
                .border_style(border_style)
                .style(list_style),
        )
        .style(list_style)
        .highlight_style(highlight_style);
    let mut right_state = ListState::default();
    right_state.select((app.active_pane == 1).then_some(app.right_selected));
    f.render_stateful_widget(right_list, panel_chunks[1], &mut right_state);

    let chat_inner = chat_inner_area(chat_rect, app.chat_state == ChatState::Open);
    let chat_text_width = chat_inner.width.saturating_sub(1).max(1);
    let wrapped_chat_lines = wrap_lines(chat_messages_with_input(app), chat_text_width as usize);
    let visible_chat_lines = chat_inner.height.max(1) as usize;
    let start = chat_view_start(
        wrapped_chat_lines.len(),
        visible_chat_lines,
        app.chat_scroll,
    );
    let end = (start + visible_chat_lines).min(wrapped_chat_lines.len());
    let mut visible = wrapped_chat_lines[start..end].join("\n");
    if !visible.is_empty() {
        visible.push('\n');
    }

    f.render_widget(
        Block::default().style(Style::default().bg(Color::DarkGray).fg(Color::White)),
        chat_rect,
    );
    let chat_title = if app.chat_state == ChatState::Open {
        " AI Chat "
    } else {
        " AI> "
    };
    let chat_borders = if app.chat_state == ChatState::Open {
        Borders::ALL
    } else {
        Borders::LEFT | Borders::TOP | Borders::BOTTOM
    };
    f.render_widget(
        Block::default()
            .borders(chat_borders)
            .title(chat_title)
            .border_style(Style::default().fg(Color::Indexed(242)))
            .style(Style::default().bg(Color::DarkGray).fg(Color::White)),
        chat_rect,
    );

    let chat_text_area = Rect {
        x: chat_inner.x,
        y: chat_inner.y,
        width: chat_inner.width.saturating_sub(1).max(1),
        height: chat_inner.height,
    };
    let chat_paragraph = Paragraph::new(visible)
        .style(Style::default().bg(Color::DarkGray).fg(Color::White))
        .wrap(Wrap { trim: false });
    f.render_widget(chat_paragraph, chat_text_area);

    render_scrollbar(
        f,
        wrapped_chat_lines.len(),
        visible_chat_lines,
        start,
        Rect {
            x: chat_inner.x + chat_inner.width.saturating_sub(1),
            y: chat_inner.y,
            width: 1,
            height: chat_inner.height,
        },
    );

    let prompt_paragraph = Paragraph::new(prompt_content)
        .style(Style::default().bg(Color::Black).fg(Color::Gray))
        .wrap(Wrap { trim: false });
    f.render_widget(prompt_paragraph, prompt_area);
}

fn sidebar_chat_width(total_width: u16) -> u16 {
    let preferred = ((total_width as u32 * 35) / 100) as u16;
    preferred.clamp(30, total_width.saturating_sub(1).max(1))
}

fn chat_inner_area(chat_rect: Rect, has_right_border: bool) -> Rect {
    let border_width = if has_right_border { 2 } else { 1 };
    Rect {
        x: chat_rect.x.saturating_add(1),
        y: chat_rect.y.saturating_add(1),
        width: chat_rect.width.saturating_sub(border_width),
        height: chat_rect.height.saturating_sub(2),
    }
}

fn chat_messages_with_input(app: &App) -> Vec<String> {
    let mut lines = app.chat_messages.clone();
    if app.chat_state == ChatState::Open {
        lines.push(format!("AI> {}", app.chat_input));
    }
    lines
}

fn wrap_lines(lines: Vec<String>, width: usize) -> Vec<String> {
    let mut wrapped = Vec::new();
    let safe_width = width.max(1);
    for line in lines {
        if line.is_empty() {
            wrapped.push(String::new());
            continue;
        }

        let mut current = String::new();
        let mut count = 0usize;
        for ch in line.chars() {
            current.push(ch);
            count += 1;
            if count >= safe_width {
                wrapped.push(current.clone());
                current.clear();
                count = 0;
            }
        }
        if !current.is_empty() {
            wrapped.push(current);
        }
    }
    wrapped
}

fn chat_view_start(total_lines: usize, visible_lines: usize, scroll: usize) -> usize {
    if total_lines <= visible_lines {
        return 0;
    }
    let max_start = total_lines - visible_lines;
    max_start.saturating_sub(scroll.min(max_start))
}

fn render_scrollbar(
    f: &mut ratatui::Frame,
    total_lines: usize,
    visible_lines: usize,
    start: usize,
    area: Rect,
) {
    if area.height == 0 || area.width == 0 {
        return;
    }

    let mut track = vec![" ".to_string(); area.height as usize];
    if total_lines > 0 && visible_lines > 0 {
        let thumb_height = ((visible_lines as u64 * area.height as u64) / total_lines as u64)
            .max(1)
            .min(area.height as u64) as usize;
        let max_top = area.height as usize - thumb_height;
        let thumb_top = if total_lines <= visible_lines {
            0
        } else {
            ((start as u64 * max_top as u64) / (total_lines - visible_lines) as u64) as usize
        };

        for i in 0..thumb_height {
            track[thumb_top + i] = "█".to_string();
        }
    }

    let track_widget = Paragraph::new(track.join("\n"))
        .style(Style::default().bg(Color::Indexed(234)).fg(Color::Indexed(242)));
    f.render_widget(track_widget, area);
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

fn pad_or_trim(value: &str, width: usize) -> String {
    let mut out = String::new();
    for ch in value.chars().take(width) {
        out.push(ch);
    }
    let len = out.chars().count();
    if len < width {
        out.push_str(&" ".repeat(width - len));
    }
    out
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
            if app.chat_state == ChatState::Open {
                app.chat_scroll = app.chat_scroll.saturating_add(1);
                return;
            }
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
            if app.chat_state == ChatState::Open {
                app.chat_scroll = app.chat_scroll.saturating_sub(1);
                return;
            }
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
            } else {
                app.chat_scroll = app.chat_scroll.saturating_add(3);
            }
            return;
        }
        b"\x1b[C" => {
            if app.chat_state == ChatState::Closed {
                app.active_pane = 1;
            } else {
                app.chat_scroll = app.chat_scroll.saturating_sub(3);
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
                    for line in mock_ai_reply(&user_msg) {
                        app.chat_messages.push(format!("AI: {}", line));
                    }
                    app.chat_scroll = 0;
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
                app.chat_scroll = 0;
            } else {
                app.prompt.push(byte as char);
            }
        }
    }
}

fn mock_ai_reply(input: &str) -> Vec<String> {
    let lowered = input.to_lowercase();
    let mut lines = Vec::new();

    if lowered.contains("help") {
        lines.push("I can summarize files, suggest commands, and draft snippets.".to_string());
        lines.push("Try: 'summarize README.md' or 'explain src/app.rs'.".to_string());
        return lines;
    }
    if lowered.contains("list") || lowered.contains("ls") {
        lines.push("Mock planner: inspect current directory and rank likely targets.".to_string());
        lines.push("Suggestion: open left panel and hit Enter on a directory.".to_string());
        return lines;
    }
    if lowered.contains("build") {
        lines.push("Mock execution: cargo build -p kabibi".to_string());
        lines.push("Result: success (simulated).".to_string());
        return lines;
    }

    lines.push("Received. I will treat this as a planning request.".to_string());
    lines.push(format!("Echo: {}", input));
    lines.push("No external model call is made in this mock mode.".to_string());
    lines
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
