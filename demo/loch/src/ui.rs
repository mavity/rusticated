use ratatui::layout::{Constraint, Direction, Layout, Rect, Margin};
use ratatui::style::{Color, Style};
use ratatui::widgets::{Block, BorderType, Borders, List, ListItem, ListState};
use ratatui::Frame;

use crate::app::App;

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

    draw_panel(f, chunks[0], app, 0, &app.left_files, app.left_selected, "Left");
    draw_panel(f, chunks[1], app, 1, &app.right_files, app.right_selected, "Right");
}

fn draw_panel(
    f: &mut Frame,
    area: Rect,
    app: &App,
    pane_index: u8,
    files: &[String],
    selected: usize,
    title: &str,
) {
    let active_border = Color::White;
    let inactive_border = Color::Cyan;

    let border_color = if app.active_pane == pane_index {
        active_border
    } else {
        inactive_border
    };

    let list_style = Style::default().bg(Color::Blue).fg(Color::White);
    let highlight_style = Style::default().bg(Color::Cyan).fg(Color::Black);

    // Draw the outer block with double borders
    let block = Block::default()
        .borders(Borders::ALL)
        .border_type(BorderType::Double)
        .title(title)
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
    
    let col_chunks = Layout::default()
        .direction(Direction::Horizontal)
        .constraints([Constraint::Percentage(50), Constraint::Percentage(50)].as_ref())
        .split(inner_area);

    let mut col1_items = Vec::new();
    let mut col2_items = Vec::new();

    // Determine current scroll offset
    let page = selected / (items_per_col * 2).max(1);
    let start_idx = page * (items_per_col * 2);

    for i in 0..items_per_col {
        let idx1 = start_idx + i;
        if idx1 < files.len() {
            col1_items.push(ListItem::new(files[idx1].as_str()));
        }

        let idx2 = start_idx + items_per_col + i;
        if idx2 < files.len() {
            col2_items.push(ListItem::new(files[idx2].as_str()));
        }
    }

    let mut col1_state = ListState::default();
    let mut col2_state = ListState::default();

    if app.active_pane == pane_index {
        let local_sel = selected.saturating_sub(start_idx);
        if local_sel < items_per_col {
            col1_state.select(Some(local_sel));
        } else {
            col2_state.select(Some(local_sel - items_per_col));
        }
    } else {
        // optionally highlight in inactive panel, just with a different color,
        // or omit selection. We can highlight it.
        let local_sel = selected.saturating_sub(start_idx);
        if local_sel < items_per_col {
            col1_state.select(Some(local_sel));
        } else {
            col2_state.select(Some(local_sel - items_per_col));
        }
    }

    // Use regular list style but allow selection
    let list1 = List::new(col1_items).style(list_style).highlight_style(highlight_style);
    let list2 = List::new(col2_items).style(list_style).highlight_style(highlight_style);

    f.render_stateful_widget(list1, col_chunks[0], &mut col1_state);
    f.render_stateful_widget(list2, col_chunks[1], &mut col2_state);
}
