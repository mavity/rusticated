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

    if bytes == b"\x1b[A" {
        // Up
        let new_sel = selected.saturating_sub(1);
        set_sel(app, new_sel);
    } else if bytes == b"\x1b[B" {
        // Down
        let new_sel = (selected + 1).min(files_len.saturating_sub(1));
        set_sel(app, new_sel);
    } else if bytes == b"\x1b[D" {
        // Left - Jump column left
        let new_sel = selected.saturating_sub(inner_height);
        set_sel(app, new_sel);
    } else if bytes == b"\x1b[C" {
        // Right - Jump column right
        let new_sel = (selected + inner_height).min(files_len.saturating_sub(1));
        set_sel(app, new_sel);
    } else if bytes == b"\r" || bytes == b"\n" {
        // Enter
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
        return;
    }

    let target = files[selected].clone();
    if !target.is_dir {
        return;
    }

    // Save history for current directory
    app.history.insert(active_dir.clone(), selected);

    // Navigate to target directory
    App::push_path(active_dir, &target.name);

    let new_path = active_dir.clone();
    let new_files = App::load_dir(&new_path).await;

    // Restore selected index from history, default to 0
    let restored_sel = app.history.get(&new_path).copied().unwrap_or(0);
    let final_sel = restored_sel.min(new_files.len().saturating_sub(1));

    if app.active_pane == 0 {
        app.left_files = new_files;
        app.left_selected = final_sel;
    } else {
        app.right_files = new_files;
        app.right_selected = final_sel;
    }
}
