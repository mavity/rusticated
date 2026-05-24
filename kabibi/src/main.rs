use crossterm::event::{self, Event, KeyCode, KeyModifiers};
use crossterm::execute;
use crossterm::terminal::{disable_raw_mode, enable_raw_mode, EnterAlternateScreen, LeaveAlternateScreen};
use ratatui::backend::Backend;
use ratatui::layout::{Constraint, Direction, Layout, Rect};
use ratatui::style::{Color, Modifier, Style};
use ratatui::widgets::{Block, Borders, List, ListItem, Paragraph};
use ratatui::Terminal;
use std::io::{self, Write};
use std::time::Duration as StdDuration;

mod app;
mod truant;

use app::{App, ChatState};

const PLUME_MAX: usize = 10;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    // Setup terminal
    let mut stdout = io::stdout();
    execute!(stdout, EnterAlternateScreen)?;
    enable_raw_mode()?;

    let (width, height) = crossterm::terminal::size()?;
    let backend = truant::TruantBackend::new(width, height);
    let mut terminal = Terminal::new(backend)?;

    let mut app = App::new().await;

    loop {
        // Draw UI
        terminal.draw(|f| draw_ui(f, &app))?;

        if app.should_quit {
            break;
        }

        // Input handling (non‑blocking poll)
        if event::poll(StdDuration::from_millis(100))? {
            match event::read()? {
                Event::Key(key) => handle_key(&mut app, key),
                _ => {}
            }
        }
    }

    // Restore terminal
    disable_raw_mode()?;
    execute!(io::stdout(), LeaveAlternateScreen)?;
    Ok(())
}

fn draw_ui<B: Backend>(f: &mut ratatui::Frame<B>, app: &App) {
    // Split screen vertically: top area for panels, bottom for plume & prompt
    let vertical_chunks = Layout::default()
        .direction(Direction::Vertical)
        .constraints([Constraint::Percentage(70), Constraint::Percentage(30)].as_ref())
        .split(f.size());
    let top = vertical_chunks[0];
    let bottom = vertical_chunks[1];

    // Top: file manager panels (left/right) – if chat is open, right panel becomes chat view
    let horiz_constraints = if app.chat_state == ChatState::Open {
        [Constraint::Percentage(50), Constraint::Percentage(50)].as_ref()
    } else {
        [Constraint::Percentage(50), Constraint::Percentage(50)].as_ref()
    };
    let panel_chunks = Layout::default()
        .direction(Direction::Horizontal)
        .constraints(horiz_constraints)
        .split(top);

    // Left file manager
    let left_items: Vec<ListItem> = app
        .left_files
        .iter()
        .enumerate()
        .map(|(i, file)| {
            let mut style = Style::default();
            if i == app.left_selected && app.active_pane == 0 {
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

    // Right panel – either file manager or chat
    if app.chat_state == ChatState::Open {
        // Chat viewport (messages)
        let chat_items: Vec<ListItem> = app
            .chat_messages
            .iter()
            .map(|msg| ListItem::new(msg.clone()))
            .collect();
        let chat_list = List::new(chat_items)
            .block(Block::default().borders(Borders::ALL).title("AI Chat"));
        f.render_widget(chat_list, panel_chunks[1]);
    } else {
        let right_items: Vec<ListItem> = app
            .right_files
            .iter()
            .enumerate()
            .map(|(i, file)| {
                let mut style = Style::default();
                if i == app.right_selected && app.active_pane == 1 {
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

    // Bottom area – plume (output) + prompt
    let bottom_chunks = Layout::default()
        .direction(Direction::Vertical)
        .constraints([Constraint::Min(0), Constraint::Length(3)].as_ref())
        .split(bottom);

    // Plume (previous command output)
    let plume_text = app.plume.join("\n");
    let plume_paragraph = Paragraph::new(plume_text)
        .block(Block::default().borders(Borders::ALL).title("Plume"))
        .style(Style::default().fg(Color::White));
    f.render_widget(plume_paragraph, bottom_chunks[0]);

    // Prompt line (or chat input when chat is active)
    let prompt_prefix = if app.chat_state == ChatState::Open { "AI> " } else { "$ " };
    let prompt_content = format!("{}{}", prompt_prefix, if app.chat_state == ChatState::Open { &app.chat_input } else { &app.prompt });
    let prompt_paragraph = Paragraph::new(prompt_content)
        .block(Block::default().borders(Borders::ALL).title("Input"))
        .style(Style::default().fg(Color::Green));
    f.render_widget(prompt_paragraph, bottom_chunks[1]);
}

fn handle_key(app: &mut App, key: crossterm::event::KeyEvent) {
    use KeyCode::*;
    // Global shortcuts
    if key.modifiers.contains(KeyModifiers::CONTROL) && matches!(key.code, Char('c')) {
        app.should_quit = true;
        return;
    }
    match key.code {
        Tab => {
            app.handle_tab();
        }
        Char(c) => {
            if app.chat_state == ChatState::Open {
                app.chat_input.push(c);
            } else {
                app.prompt.push(c);
            }
        }
        Backspace => {
            if app.chat_state == ChatState::Open {
                app.chat_input.pop();
            } else {
                app.prompt.pop();
            }
        }
        Enter => {
            if app.chat_state == ChatState::Open {
                // Submit chat input
                let user_msg = app.chat_input.drain(..).collect::<String>();
                app.chat_messages.push(format!("You: {}", user_msg));
                // Mock AI reply (echo)
                app.chat_messages.push(format!("AI: {}", user_msg));
            } else {
                // Submit bash prompt
                let cmd = app.prompt.drain(..).collect::<String>();
                if !cmd.is_empty() {
                    // Mock execution: echo the command as output
                    let output = format!("Executed: {}", cmd);
                    app.plume.push(output);
                    // If plume exceeds limit, flush oldest lines to stdout
                    if app.plume.len() > PLUME_MAX {
                        if let Some(line) = app.plume.drain(0..1).next() {
                            println!("{}", line);
                        }
                    }
                }
            }
        }
        Up => {
            // Navigate file list selection
            match app.active_pane {
                0 => {
                    if app.left_selected > 0 {
                        app.left_selected -= 1;
                    }
                }
                1 => {
                    if app.right_selected > 0 {
                        app.right_selected -= 1;
                    }
                }
                _ => {}
            }
        }
        Down => {
            match app.active_pane {
                0 => {
                    if app.left_selected + 1 < app.left_files.len() {
                        app.left_selected += 1;
                    }
                }
                1 => {
                    if app.right_selected + 1 < app.right_files.len() {
                        app.right_selected += 1;
                    }
                }
                _ => {}
            }
        }
        Left => {
            // Switch active pane (left/right) when AI panel collapsed
            if app.chat_state == ChatState::Closed {
                app.active_pane = 0;
            }
        }
        Right => {
            if app.chat_state == ChatState::Closed {
                app.active_pane = 1;
            }
        }
        Enter => {
            // For file manager: open directory on Enter when a directory is selected
            if app.chat_state == ChatState::Closed && (app.active_pane == 0 || app.active_pane == 1) {
                let (files, selected, dir) = if app.active_pane == 0 {
                    (&mut app.left_files, app.left_selected, &mut app.current_left_dir)
                } else {
                    (&mut app.right_files, app.right_selected, &mut app.current_right_dir)
                };
                if let Some(item) = files.get(selected) {
                    if item.is_dir {
                        App::push_path(dir, &item.name);
                        // Reload asynchronously (blocking here for simplicity)
                        let loaded = futures::executor::block_on(App::load_dir(dir));
                        *files = loaded;
                        // Reset selection
                        if app.active_pane == 0 {
                            app.left_selected = 0;
                        } else {
                            app.right_selected = 0;
                        }
                    }
                }
            }
        }
        _ => {}
    }
}
