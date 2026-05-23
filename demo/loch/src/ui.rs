use ratatui::Frame;
use ratatui::layout::{Alignment, Constraint, Direction, Layout, Rect};
use ratatui::style::{Color, Style};
use ratatui::widgets::{Block, BorderType, Borders, List, ListItem, ListState};

use crate::app::{App, FileItem};

pub fn draw_ui(f: &mut Frame, app: &mut App) {
    // Far manager overall theme: Dark blue background, Cyan text.
    f.render_widget(
        Block::default().style(Style::default().bg(Color::Blue)),
        f.area(),
    );

    let chunks = Layout::default()
        .direction(Direction::Horizontal)
        .constraints([Constraint::Percentage(50), Constraint::Percentage(50)].as_ref())
        .split(f.area());

    draw_panel(f, chunks[0], app, 0, &app.left_files, app.left_selected);
    draw_panel(f, chunks[1], app, 1, &app.right_files, app.right_selected);
}

fn draw_panel(
    f: &mut Frame,
    area: Rect,
    app: &App,
    pane_index: u8,
    files: &[FileItem],
    selected: usize,
) {
    let is_active = app.active_pane == pane_index;

    // Both panels have the same border color
    let border_color = Color::Cyan;

    let list_style = Style::default().bg(Color::Blue).fg(Color::Cyan);
    let highlight_style = Style::default().bg(Color::Cyan).fg(Color::Black);

    let path = if pane_index == 0 {
        &app.current_left_dir
    } else {
        &app.current_right_dir
    };
    let cur_dir = path.split('/').last().unwrap_or(".");
    let dir_name = if cur_dir.is_empty() { "." } else { cur_dir };

    let title_style = if is_active {
        highlight_style
    } else {
        Style::default().fg(Color::Cyan)
    };

    let title_line = ratatui::text::Line::from(format!(" {} ", dir_name)).style(title_style);

    // Draw the outer block with double borders
    let block = Block::default()
        .borders(Borders::ALL)
        .border_type(BorderType::Double)
        .title(title_line)
        .title_alignment(Alignment::Center)
        .border_style(Style::default().fg(border_color))
        .style(list_style);

    let inner_area = block.inner(area);
    f.render_widget(block, area);

    // If the panel is too small to even show items, bail out early
    if inner_area.height == 0 || inner_area.width == 0 {
        return;
    }

    // Determine how many items fit in a single column
    let items_per_col = inner_area.height as usize;

    let num_cols = (inner_area.width / 18).max(1) as usize;
    let col_width_constraints =
        std::vec::from_elem(Constraint::Ratio(1, num_cols as u32), num_cols);

    let col_chunks = Layout::default()
        .direction(Direction::Horizontal)
        .constraints(col_width_constraints)
        .split(inner_area);

    let items_per_page = items_per_col * num_cols;
    let page = selected / items_per_page.max(1);
    let start_idx = page * items_per_page;

    let mut cols_items = std::vec::from_elem(Vec::new(), num_cols);
    let mut cols_states = std::vec::from_elem(ListState::default(), num_cols);

    for c in 0..num_cols {
        for r in 0..items_per_col {
            let idx = start_idx + c * items_per_col + r;
            if idx < files.len() {
                let item = &files[idx];
                // Directories in the list show with bright white color
                let fg_color = if item.is_dir {
                    Color::White
                } else {
                    Color::Cyan
                };
                cols_items[c]
                    .push(ListItem::new(item.name.as_str()).style(Style::default().fg(fg_color)));
            }
        }
    }

    if is_active {
        let local_sel = selected.saturating_sub(start_idx);
        let sel_col = local_sel / items_per_col;
        let sel_row = local_sel % items_per_col;
        if sel_col < num_cols {
            cols_states[sel_col].select(Some(sel_row));
        }
    }

    for c in 0..num_cols {
        let list = List::new(cols_items[c].clone())
            .style(list_style)
            .highlight_style(highlight_style);
        f.render_stateful_widget(list, col_chunks[c], &mut cols_states[c]);
    }
}
