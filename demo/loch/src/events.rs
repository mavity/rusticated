use crate::app::App;

pub async fn handle_input(app: &mut App, bytes: &[u8], inner_height: usize) {
    if bytes.is_empty() {
        return;
    }

    if bytes == b"\x03" || bytes == b"\x1b" || bytes == b"q" {
        app.should_quit = true;
        return;
    }

    if bytes == b"\t" {
        app.active_pane = (app.active_pane + 1) % 2;
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

    if bytes == b"\x1b[A" { // Up
        let new_sel = selected.saturating_sub(1);
        set_sel(app, new_sel);
    } else if bytes == b"\x1b[B" { // Down
        let new_sel = (selected + 1).min(files_len.saturating_sub(1));
        set_sel(app, new_sel);
    } else if bytes == b"\x1b[D" { // Left - Jump column left
        let new_sel = selected.saturating_sub(inner_height);
        set_sel(app, new_sel);
    } else if bytes == b"\x1b[C" { // Right - Jump column right
        let new_sel = (selected + inner_height).min(files_len.saturating_sub(1));
        set_sel(app, new_sel);
    } else if bytes == b"\r" || bytes == b"\n" { // Enter
        handle_enter(app).await;
    }
}

fn set_sel(app: &mut App, sel: usize) {
    if app.active_pane == 0 {
        app.left_selected = sel;
    } else {
        app.right_selected = sel;
    }
}

async fn handle_enter(app: &mut App) {
    if app.active_pane == 0 {
        if app.left_selected < app.left_files.len() {
            let target = &app.left_files[app.left_selected];
            if target == ".." {
                app.current_left_dir.push_str("/..");
            } else {
                app.current_left_dir.push_str("/");
                app.current_left_dir.push_str(target);
            }
            app.left_files = App::load_dir(&app.current_left_dir).await;
            app.left_selected = 0;
        }
    } else {
        if app.right_selected < app.right_files.len() {
            let target = &app.right_files[app.right_selected];
            if target == ".." {
                app.current_right_dir.push_str("/..");
            } else {
                app.current_right_dir.push_str("/");
                app.current_right_dir.push_str(target);
            }
            app.right_files = App::load_dir(&app.current_right_dir).await;
            app.right_selected = 0;
        }
    }
}