use ratatui::backend::{Backend, WindowSize};
use ratatui::buffer::Cell;
use ratatui::layout::Size;
use ratatui::style::{Color, Modifier};
use std::io;

#[derive(Default)]
pub struct TruantBackend {
    pub buffer: String,
    width: u16,
    height: u16,
}

impl TruantBackend {
    pub fn new(width: u16, height: u16) -> Self {
        Self {
            buffer: String::with_capacity(width as usize * height as usize * 4),
            width,
            height,
        }
    }

    fn apply_color(&mut self, color: Color, is_bg: bool) {
        let base = if is_bg { 40 } else { 30 };
        match color {
            Color::Reset => self
                .buffer
                .push_str(if is_bg { "\x1b[49m" } else { "\x1b[39m" }),
            Color::Black => self.buffer.push_str(&format!("\x1b[{}m", base)),
            Color::Red => self.buffer.push_str(&format!("\x1b[{}m", base + 1)),
            Color::Green => self.buffer.push_str(&format!("\x1b[{}m", base + 2)),
            Color::Yellow => self.buffer.push_str(&format!("\x1b[{}m", base + 3)),
            Color::Blue => self.buffer.push_str(&format!("\x1b[{}m", base + 4)),
            Color::Magenta => self.buffer.push_str(&format!("\x1b[{}m", base + 5)),
            Color::Cyan => self.buffer.push_str(&format!("\x1b[{}m", base + 6)),
            Color::Gray => self.buffer.push_str(&format!("\x1b[{}m", base + 7)),
            Color::DarkGray => self.buffer.push_str(&format!("\x1b[{}m", base + 60)),
            Color::LightRed => self.buffer.push_str(&format!("\x1b[{}m", base + 61)),
            Color::LightGreen => self.buffer.push_str(&format!("\x1b[{}m", base + 62)),
            Color::LightYellow => self.buffer.push_str(&format!("\x1b[{}m", base + 63)),
            Color::LightBlue => self.buffer.push_str(&format!("\x1b[{}m", base + 64)),
            Color::LightMagenta => self.buffer.push_str(&format!("\x1b[{}m", base + 65)),
            Color::LightCyan => self.buffer.push_str(&format!("\x1b[{}m", base + 66)),
            Color::White => self.buffer.push_str(&format!("\x1b[{}m", base + 67)),
            Color::Rgb(r, g, b) => {
                let code = if is_bg { 48 } else { 38 };
                self.buffer.push_str(&format!("\x1b[{code};2;{r};{g};{b}m"));
            }
            Color::Indexed(i) => {
                let code = if is_bg { 48 } else { 38 };
                self.buffer.push_str(&format!("\x1b[{code};5;{i}m"));
            }
        }
    }

    pub fn resize(&mut self, width: u16, height: u16) {
        self.width = width;
        self.height = height;
    }
}

impl Backend for TruantBackend {
    type Error = io::Error;

    fn clear_region(&mut self, clear_type: ratatui::backend::ClearType) -> Result<(), Self::Error> {
        match clear_type {
            ratatui::backend::ClearType::All => {
                self.buffer.push_str("\x1b[2J");
            }
            ratatui::backend::ClearType::AfterCursor => {
                self.buffer.push_str("\x1b[J");
            }
            ratatui::backend::ClearType::BeforeCursor => {
                self.buffer.push_str("\x1b[1J");
            }
            ratatui::backend::ClearType::CurrentLine => {
                self.buffer.push_str("\x1b[2K");
            }
            ratatui::backend::ClearType::UntilNewLine => {
                self.buffer.push_str("\x1b[K");
            }
        }
        Ok(())
    }
    fn draw<'a, I>(&mut self, content: I) -> io::Result<()>
    where
        I: Iterator<Item = (u16, u16, &'a Cell)>,
    {
        let mut last_y = u16::MAX;
        let mut last_x = u16::MAX;

        let mut prev_fg = Color::Reset;
        let mut prev_bg = Color::Reset;
        let mut prev_modifier = Modifier::empty();

        for (x, y, cell) in content {
            if y != last_y || x != last_x + 1 {
                self.buffer.push_str(&format!("\x1b[{};{}H", y + 1, x + 1));
            }
            last_x = x;
            last_y = y;

            if cell.modifier != prev_modifier {
                // If modifiers changed, reset everything because there's no reliable
                // way to "turn off" specific modifiers dynamically everywhere
                // (e.g. \x1b[22m is dim off, bold off, etc.)
                self.buffer.push_str("\x1b[0m");
                prev_fg = Color::Reset;
                prev_bg = Color::Reset;
                prev_modifier = cell.modifier;

                if cell.modifier.contains(Modifier::BOLD) {
                    self.buffer.push_str("\x1b[1m");
                }
                if cell.modifier.contains(Modifier::DIM) {
                    self.buffer.push_str("\x1b[2m");
                }
                if cell.modifier.contains(Modifier::ITALIC) {
                    self.buffer.push_str("\x1b[3m");
                }
                if cell.modifier.contains(Modifier::UNDERLINED) {
                    self.buffer.push_str("\x1b[4m");
                }
                if cell.modifier.contains(Modifier::SLOW_BLINK) {
                    self.buffer.push_str("\x1b[5m");
                }
                if cell.modifier.contains(Modifier::RAPID_BLINK) {
                    self.buffer.push_str("\x1b[6m");
                }
                if cell.modifier.contains(Modifier::REVERSED) {
                    self.buffer.push_str("\x1b[7m");
                }
                if cell.modifier.contains(Modifier::HIDDEN) {
                    self.buffer.push_str("\x1b[8m");
                }
                if cell.modifier.contains(Modifier::CROSSED_OUT) {
                    self.buffer.push_str("\x1b[9m");
                }
            }

            if cell.fg != prev_fg {
                self.apply_color(cell.fg, false);
                prev_fg = cell.fg;
            }

            if cell.bg != prev_bg {
                self.apply_color(cell.bg, true);
                prev_bg = cell.bg;
            }

            self.buffer.push_str(cell.symbol());
        }

        self.buffer.push_str("\x1b[0m");
        Ok(())
    }

    fn hide_cursor(&mut self) -> io::Result<()> {
        self.buffer.push_str("\x1b[?25l");
        Ok(())
    }

    fn show_cursor(&mut self) -> io::Result<()> {
        self.buffer.push_str("\x1b[?25h");
        Ok(())
    }

    fn get_cursor(&mut self) -> io::Result<(u16, u16)> {
        Ok((0, 0))
    }

    fn set_cursor(&mut self, x: u16, y: u16) -> io::Result<()> {
        self.buffer.push_str(&format!("\x1b[{};{}H", y + 1, x + 1));
        Ok(())
    }

    fn get_cursor_position(&mut self) -> io::Result<ratatui::layout::Position> {
        Ok(ratatui::layout::Position { x: 0, y: 0 })
    }

    fn set_cursor_position<P>(&mut self, position: P) -> io::Result<()>
    where
        P: Into<ratatui::layout::Position>,
    {
        let pos = position.into();
        self.buffer
            .push_str(&format!("\x1b[{};{}H", pos.y + 1, pos.x + 1));
        Ok(())
    }

    fn clear(&mut self) -> io::Result<()> {
        self.buffer.push_str("\x1b[2J\x1b[1;1H");
        Ok(())
    }

    fn size(&self) -> io::Result<Size> {
        Ok(Size {
            width: self.width,
            height: self.height,
        })
    }

    fn window_size(&mut self) -> io::Result<WindowSize> {
        Ok(WindowSize {
            columns_rows: Size {
                width: self.width,
                height: self.height,
            },
            pixels: Size {
                width: 0,
                height: 0,
            },
        })
    }

    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}
