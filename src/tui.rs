//! Native terminal browser. Collection, terminal input, and frozen-window search are separate tasks.
use crate::{
    events::{Event, console},
    jev::{Importance, Jev, Usage},
    progress::{Progress, SharedProgress},
    report::SharedMetrics,
    search::{self, Decision, Mode, Relevance},
};
use crossterm::{
    cursor::Show,
    event::{
        DisableMouseCapture, EnableMouseCapture, Event as Input, EventStream, KeyCode, KeyEvent,
        KeyEventKind, KeyModifiers, MouseButton, MouseEventKind,
    },
    execute,
    terminal::{EnterAlternateScreen, LeaveAlternateScreen, disable_raw_mode, enable_raw_mode},
};
use futures::StreamExt;
use ratatui::{
    Frame, Terminal,
    backend::CrosstermBackend,
    layout::Rect,
    style::{Color, Modifier, Style},
    text::Line,
    widgets::Paragraph,
};
use std::{
    io::{IsTerminal, Stdout, stdout},
    sync::{Arc, Mutex},
    time::Duration,
};
use tokio_util::sync::CancellationToken;
use unicode_width::UnicodeWidthChar;

const TABS: [&str; 5] = ["Important", "Routine", "Needs Review", "All", "Search"];
const ACCENT: Color = Color::Cyan;

pub fn validate(stdin_logs: bool) -> Result<(), &'static str> {
    if stdin_logs {
        return Err("--tui cannot read log input from stdin; use file paths or omit --tui");
    }
    if !std::io::stdin().is_terminal()
        || !std::io::stdout().is_terminal()
        || std::env::var("TERM").map_or(true, |t| t.is_empty() || t == "dumb")
    {
        return Err("--tui requires an interactive terminal; omit it for plain or piped output");
    }
    Ok(())
}

/// Restores raw mode, the normal screen, mouse reporting, and cursor on every return/unwind.
pub struct Screen {
    terminal: Terminal<CrosstermBackend<Stdout>>,
}
impl Screen {
    pub fn enter() -> Result<Self, &'static str> {
        enable_raw_mode().map_err(|_| "Cannot enable terminal input")?;
        let result = (|| {
            execute!(stdout(), EnterAlternateScreen, EnableMouseCapture)?;
            let mut terminal = Terminal::new(CrosstermBackend::new(stdout()))?;
            terminal.hide_cursor()?;
            Ok::<_, std::io::Error>(Self { terminal })
        })();
        result.map_err(|_| {
            restore();
            "Cannot initialize terminal UI; check TERM or omit --tui"
        })
    }
}
fn restore() {
    let _ = disable_raw_mode();
    let _ = execute!(stdout(), DisableMouseCapture, LeaveAlternateScreen, Show);
}
impl Drop for Screen {
    fn drop(&mut self) {
        restore();
    }
}

#[derive(Clone)]
struct Row {
    event: Arc<Event>,
    pending: bool,
    group: Option<usize>,
    decision: Option<Decision>,
    count: usize,
}

struct Search {
    window: Arc<search::Window>,
    state: Arc<Mutex<search::State>>,
    stop: CancellationToken,
    task: tokio::task::JoinHandle<()>,
}
impl Drop for Search {
    fn drop(&mut self) {
        self.stop.cancel();
    }
}

struct Editor {
    text: Vec<char>,
    cursor: usize,
    mode: Mode,
}

pub struct Browser {
    tab: usize,
    return_tab: usize,
    selected: [usize; 5],
    anchors: [Option<String>; 5],
    rows: Vec<Row>,
    detail: Option<Row>,
    detail_offset: usize,
    detail_max: usize,
    page_size: usize,
    tabs: Vec<(usize, Rect)>,
    row_hits: Vec<(usize, Rect)>,
    events: Vec<Row>,
    revision: Option<u64>,
    editor: Option<Editor>,
    search: Option<Search>,
    search_instances: Option<usize>,
    cache: Arc<Mutex<search::Cache>>,
    search_client: Option<Jev>,
    search_budget: usize,
    message: String,
    interrupted: bool,
}
impl Browser {
    pub fn new(search_client: Option<Jev>) -> Self {
        Self {
            tab: 0,
            return_tab: 0,
            selected: [0; 5],
            anchors: Default::default(),
            rows: Vec::new(),
            detail: None,
            detail_offset: 0,
            detail_max: 0,
            page_size: 1,
            tabs: Vec::new(),
            row_hits: Vec::new(),
            events: Vec::new(),
            revision: None,
            editor: None,
            search: None,
            search_instances: None,
            cache: Arc::new(Mutex::new(search::Cache::default())),
            search_client,
            search_budget: 16,
            message: String::new(),
            interrupted: false,
        }
    }

    fn refresh(&mut self, progress: &SharedProgress) -> Progress {
        let progress = progress.lock().expect("progress lock");
        if self.revision != Some(progress.revision) {
            self.events = progress
                .events
                .iter()
                .map(|e| (e, false))
                .chain(progress.pending.iter().map(|e| (e, true)))
                .map(|(e, pending)| Row {
                    event: e.clone(),
                    pending,
                    group: None,
                    decision: None,
                    count: 1,
                })
                .collect();
            self.revision = Some(progress.revision);
        }
        // Only counters are used by the renderer; don't clone the event window per frame.
        progress.counters()
    }

    fn ask(&mut self, mode: Mode) {
        if self.tab != 4 {
            self.return_tab = self.tab;
        }
        self.tab = 4;
        self.detail = None;
        if self.search.as_ref().is_some_and(|s| !s.task.is_finished()) {
            self.message = "Search is running; press x to stop it before asking again".into();
            return;
        }
        let text: Vec<_> = self
            .search
            .as_ref()
            .map(|s| s.state.lock().expect("search lock").query.clone())
            .unwrap_or_default()
            .chars()
            .collect();
        self.editor = Some(Editor {
            cursor: text.len(),
            text,
            mode,
        });
        self.message.clear();
    }

    fn submit(&mut self) {
        let editor = self.editor.as_ref().expect("search editor");
        let query = match search::validate(&editor.text.iter().collect::<String>()) {
            Ok(query) => query,
            Err(error) => {
                self.message = error.into();
                return;
            }
        };
        if editor.mode == Mode::Jev && self.search_client.is_none() {
            self.message =
                "Jev search is unavailable in offline mode; Tab selects exact text".into();
            return;
        }
        let window = Arc::new(search::Window::new(
            self.events.iter().map(|r| r.event.clone()),
        ));
        let client = self.search_client.as_ref().map(Jev::search_client);
        let usage = client
            .as_ref()
            .map(|c| c.usage.clone())
            .unwrap_or_else(|| Usage::new(0.0, 0.0));
        let state = Arc::new(Mutex::new(search::State::new(
            query,
            editor.mode,
            window.groups.len(),
            usage,
        )));
        let stop = CancellationToken::new();
        let task = tokio::spawn(search::run(
            window.clone(),
            state.clone(),
            client,
            self.cache.clone(),
            self.search_budget,
            stop.clone(),
        ));
        self.search = Some(Search {
            window,
            state,
            stop,
            task,
        });
        self.editor = None;
        self.search_instances = None;
        self.selected[4] = 0;
        self.anchors[4] = None;
        self.message.clear();
    }

    fn switch(&mut self, tab: usize) {
        if self.tab != 4 && tab == 4 {
            self.return_tab = self.tab;
        }
        self.tab = tab;
        self.detail = None;
        self.editor = None;
        if tab == 4 && self.search.is_none() {
            self.ask(if self.search_client.is_some() {
                Mode::Jev
            } else {
                Mode::Literal
            });
        }
    }

    fn move_by(&mut self, amount: isize) {
        if self.detail.is_some() {
            self.detail_offset = self
                .detail_offset
                .saturating_add_signed(amount)
                .min(self.detail_max);
        } else {
            self.selected[self.tab] = self.selected[self.tab]
                .saturating_add_signed(amount)
                .min(self.rows.len().saturating_sub(1));
            self.anchors[self.tab] = self
                .rows
                .get(self.selected[self.tab])
                .map(|r| r.event.id.clone());
        }
    }

    fn editor_key(&mut self, key: KeyEvent) {
        let editor = self.editor.as_mut().expect("editor");
        match key.code {
            KeyCode::Esc => {
                self.editor = None;
                if self.search.is_none() {
                    self.tab = self.return_tab;
                }
            }
            KeyCode::Enter => self.submit(),
            KeyCode::Left => editor.cursor = editor.cursor.saturating_sub(1),
            KeyCode::Right => editor.cursor = (editor.cursor + 1).min(editor.text.len()),
            KeyCode::Home => editor.cursor = 0,
            KeyCode::End => editor.cursor = editor.text.len(),
            KeyCode::Backspace if editor.cursor > 0 => {
                editor.cursor -= 1;
                editor.text.remove(editor.cursor);
            }
            KeyCode::Delete if editor.cursor < editor.text.len() => {
                editor.text.remove(editor.cursor);
            }
            KeyCode::Tab => {
                editor.mode = if editor.mode == Mode::Jev {
                    Mode::Literal
                } else {
                    Mode::Jev
                }
            }
            KeyCode::Char('u') if key.modifiers.contains(KeyModifiers::CONTROL) => {
                editor.text.clear();
                editor.cursor = 0;
            }
            KeyCode::Char('b') if key.modifiers.contains(KeyModifiers::CONTROL) => {
                self.search_budget = match self.search_budget {
                    4 => 16,
                    16 => 64,
                    _ => 4,
                };
            }
            KeyCode::Char(c)
                if !key
                    .modifiers
                    .intersects(KeyModifiers::CONTROL | KeyModifiers::ALT)
                    && !c.is_control()
                    && editor.text.len() < 500 =>
            {
                editor.text.insert(editor.cursor, c);
                editor.cursor += 1;
            }
            _ => (),
        }
    }

    fn input(&mut self, input: Input) -> bool {
        match input {
            Input::Key(key) if key.kind != KeyEventKind::Release => {
                if key.code == KeyCode::Char('c') && key.modifiers.contains(KeyModifiers::CONTROL) {
                    self.interrupted = true;
                    return false;
                }
                if self.editor.is_some() {
                    self.editor_key(key);
                    return true;
                }
                match key.code {
                    KeyCode::Char('q' | 'Q') => return false,
                    KeyCode::Char('/' | '?') => self.ask(Mode::Jev),
                    KeyCode::Char('f') => self.ask(Mode::Literal),
                    KeyCode::Char('1'..='5') => {
                        if let KeyCode::Char(c) = key.code {
                            self.switch(c as usize - '1' as usize);
                        }
                    }
                    KeyCode::Tab | KeyCode::Right => self.switch((self.tab + 1) % 5),
                    KeyCode::BackTab | KeyCode::Left => self.switch((self.tab + 4) % 5),
                    KeyCode::Up | KeyCode::Char('k') => self.move_by(-1),
                    KeyCode::Down | KeyCode::Char('j') => self.move_by(1),
                    KeyCode::PageUp => self.move_by(-(self.page_size as isize)),
                    KeyCode::PageDown => self.move_by(self.page_size as isize),
                    KeyCode::Home => self.move_by(isize::MIN),
                    KeyCode::End => self.move_by(isize::MAX),
                    KeyCode::Enter if self.detail.is_none() => {
                        self.detail = self.rows.get(self.selected[self.tab]).cloned();
                        self.detail_offset = 0;
                    }
                    KeyCode::Esc => {
                        if self.detail.take().is_none() && self.tab == 4 {
                            if self.search_instances.take().is_some() {
                                self.selected[4] = 0;
                                self.anchors[4] = None;
                            } else {
                                self.switch(self.return_tab);
                            }
                        }
                    }
                    KeyCode::Char('x') if self.tab == 4 => {
                        if let Some(search) = &self.search {
                            search.stop.cancel();
                        }
                    }
                    KeyCode::Char('i') if self.tab == 4 && self.detail.is_none() => {
                        if let Some(row) = self.rows.get(self.selected[4]) {
                            self.search_instances = row.group;
                            self.selected[4] = 0;
                            self.anchors[4] = None;
                        }
                    }
                    _ => (),
                }
            }
            Input::Mouse(mouse) => match mouse.kind {
                MouseEventKind::ScrollUp if self.editor.is_none() => self.move_by(-3),
                MouseEventKind::ScrollDown if self.editor.is_none() => self.move_by(3),
                MouseEventKind::Down(MouseButton::Left) => {
                    let point = (mouse.column, mouse.row).into();
                    if let Some((index, _)) =
                        self.tabs.iter().find(|(_, rect)| rect.contains(point))
                    {
                        self.switch(*index);
                    } else if self.detail.is_none()
                        && self.editor.is_none()
                        && let Some((index, _)) =
                            self.row_hits.iter().find(|(_, rect)| rect.contains(point))
                    {
                        self.selected[self.tab] = *index;
                        self.anchors[self.tab] = Some(self.rows[*index].event.id.clone());
                    }
                }
                _ => (),
            },
            _ => (),
        }
        true
    }

    fn rebuild_rows(&mut self, state: Option<&search::State>) {
        self.rows = if self.tab == 4 {
            let mut rows = Vec::new();
            if let (Some(search), Some(state)) = (&self.search, state) {
                for (i, group) in search.window.groups.iter().enumerate() {
                    if self.search_instances.is_some_and(|g| g != i) {
                        continue;
                    }
                    let Some(decision) = &state.decisions[i] else {
                        continue;
                    };
                    if decision.relevance == Relevance::Unrelated {
                        continue;
                    }
                    let events = if self.search_instances.is_some() {
                        &group.events[..]
                    } else {
                        &group.events[..1]
                    };
                    rows.extend(events.iter().map(|event| Row {
                        event: event.clone(),
                        pending: false,
                        group: Some(i),
                        decision: Some(decision.clone()),
                        count: group.events.len(),
                    }));
                }
            }
            if self.search_instances.is_none() {
                rows.sort_by(|a, b| {
                    let rank = |r: &Row| match r.decision.as_ref().map(|d| d.relevance) {
                        Some(Relevance::Match) => 0,
                        Some(Relevance::Possible) => 1,
                        _ => 2,
                    };
                    rank(a)
                        .cmp(&rank(b))
                        .then_with(|| {
                            b.decision
                                .as_ref()
                                .and_then(|d| d.confidence)
                                .unwrap_or(0.0)
                                .total_cmp(
                                    &a.decision
                                        .as_ref()
                                        .and_then(|d| d.confidence)
                                        .unwrap_or(0.0),
                                )
                        })
                        .then_with(|| b.count.cmp(&a.count))
                        .then_with(|| a.event.id.cmp(&b.event.id))
                });
            }
            rows
        } else {
            self.events
                .iter()
                .filter(|r| matches_tab(r, self.tab))
                .cloned()
                .collect()
        };
        if let Some(anchor) = &self.anchors[self.tab]
            && let Some(index) = self.rows.iter().position(|r| &r.event.id == anchor)
        {
            self.selected[self.tab] = index;
        }
        self.selected[self.tab] = self.selected[self.tab].min(self.rows.len().saturating_sub(1));
    }

    fn draw(&mut self, frame: &mut Frame, progress: &Progress, status: &str, offline: bool) {
        let area = frame.area();
        self.tabs.clear();
        self.row_hits.clear();
        if area.width < 35 || area.height < 10 {
            put(
                frame,
                0,
                0,
                area.width,
                "Enlarge terminal (35x10 minimum)",
                Style::default(),
            );
            put(
                frame,
                1,
                0,
                area.width,
                "q: stop and exit",
                Style::default(),
            );
            return;
        }
        put(
            frame,
            0,
            0,
            area.width,
            &format!(
                "jevernetes | {} | {} | {} retained + {} pending",
                progress.phase,
                if offline { "offline-rules" } else { "jev" },
                self.events.iter().filter(|r| !r.pending).count(),
                self.events.iter().filter(|r| r.pending).count(),
            ),
            Style::default().fg(ACCENT).add_modifier(Modifier::BOLD),
        );
        put(frame, 1, 0, area.width, status, Style::default());
        put(
            frame,
            2,
            0,
            area.width,
            &format!(
                "{} total | {} evicted | {} tokens | ~${:.6} estimated{}",
                progress.total,
                progress.evicted,
                progress.usage.input_tokens + progress.usage.output_tokens,
                progress.usage.estimated_cost_usd,
                if progress.usage.cost_complete {
                    ""
                } else {
                    " (metering incomplete)"
                },
            ),
            Style::default().fg(Color::Gray),
        );
        let state = self
            .search
            .as_ref()
            .map(|s| s.state.lock().expect("search lock").clone());
        let mut counts = [0; 5];
        for (tab, count) in counts.iter_mut().enumerate().take(4) {
            *count = self.events.iter().filter(|r| matches_tab(r, tab)).count();
        }
        counts[4] = state.as_ref().map_or(0, |s| {
            s.decisions
                .iter()
                .flatten()
                .filter(|d| d.relevance != Relevance::Unrelated)
                .count()
        });
        let mut y: u16 = 4;
        let mut x: u16 = 0;
        for (i, tab) in TABS.iter().enumerate() {
            let label = format!(" {} {} ({}) ", i + 1, tab, counts[i]);
            let width = (label.len() as u16).min(area.width);
            if x > 0 && x.saturating_add(width) > area.width {
                y += 1;
                x = 0;
            }
            let rect = Rect::new(x, y, width, 1);
            self.tabs.push((i, rect));
            put(
                frame,
                y,
                x,
                width,
                &label,
                if i == self.tab {
                    Style::default()
                        .fg(Color::Black)
                        .bg(ACCENT)
                        .add_modifier(Modifier::BOLD)
                } else {
                    Style::default().fg(Color::Gray)
                },
            );
            x = x.saturating_add(width + 1);
        }
        y += 2;
        let footer_y = area.height - 1;
        if let Some(editor) = &self.editor {
            put(
                frame,
                y,
                0,
                area.width,
                "Search all retained logs, including Routine and pending.",
                Style::default(),
            );
            put(
                frame,
                y + 1,
                0,
                area.width,
                &format!(
                    "{} | {} new groups max | {} instances",
                    editor.mode.label(),
                    self.search_budget * 8,
                    self.events.len()
                ),
                Style::default().fg(ACCENT),
            );
            let mut text = editor.text.clone();
            text.insert(editor.cursor, '|');
            let lines = wrap_cells(
                &format!("> {}", text.iter().collect::<String>()),
                area.width as usize,
            );
            let cursor_line = wrap_cells(
                &format!(
                    "> {}",
                    editor.text[..editor.cursor].iter().collect::<String>()
                ),
                area.width as usize,
            )
            .len()
            .saturating_sub(1);
            for (offset, line) in lines
                .iter()
                .skip(cursor_line.saturating_sub(1))
                .take(2)
                .enumerate()
            {
                put(
                    frame,
                    y + 3 + offset as u16,
                    0,
                    area.width,
                    line,
                    Style::default().fg(Color::Yellow),
                );
            }
            put(
                frame,
                y + 6,
                0,
                area.width,
                "Exact text stays local. Jev receives redacted query + logs.",
                Style::default(),
            );
            put(
                frame,
                y + 7,
                0,
                area.width,
                "Jev search: $0.01 estimated stop; in-flight work may exceed it.",
                Style::default(),
            );
            put(
                frame,
                footer_y - 1,
                0,
                area.width,
                &self.message,
                Style::default().fg(Color::Yellow),
            );
            put(
                frame,
                footer_y,
                0,
                area.width,
                "Enter search | Tab mode | Ctrl+B budget | Ctrl+U clear | Esc back",
                Style::default().fg(ACCENT),
            );
            return;
        }
        if self.tab == 4
            && let Some(state) = &state
        {
            put(
                frame,
                y,
                0,
                area.width,
                &format!("Search [{}]: {}", state.mode.label(), state.query),
                Style::default().fg(ACCENT),
            );
            let search = self.search.as_ref().expect("search state");
            put(
                frame,
                y + 1,
                0,
                area.width,
                &format!(
                    "{} | {}/{} groups evaluated | {} frozen instances",
                    state.status,
                    state.checked,
                    search.window.groups.len(),
                    search.window.total_events,
                ),
                Style::default(),
            );
            put(
                frame,
                y + 2,
                0,
                area.width,
                &format!(
                    "{} requests | {} cached | ~${:.6}{} | {}",
                    state.usage.request_attempts,
                    state.cache_hits,
                    state.usage.estimated_cost_usd,
                    if state.usage.cost_complete {
                        ""
                    } else {
                        " (metering incomplete)"
                    },
                    state.message,
                ),
                Style::default().fg(if state.partial() {
                    Color::Yellow
                } else {
                    Color::Gray
                }),
            );
            y += 4;
        }
        self.rebuild_rows(state.as_ref());
        if y.saturating_add(3) >= footer_y {
            put(
                frame,
                footer_y - 1,
                0,
                area.width,
                "Enlarge terminal to show events",
                Style::default(),
            );
        } else {
            let height = footer_y - y - 2;
            self.page_size = (height as usize / 2).max(1);
            if let Some(row) = &self.detail {
                let event = &row.event;
                let mut lines = vec![
                    format!(
                        "{} | {}",
                        event.timestamp.as_deref().unwrap_or("-"),
                        source(event)
                    ),
                    format!(
                        "{} | lines {}-{} | truncated: {}",
                        label(row),
                        event.line_start,
                        event.line_end,
                        event.truncated
                    ),
                    format!(
                        "Source: {}",
                        serde_json::to_string(&event.source).unwrap_or_default()
                    ),
                ];
                if let Some(error) = &event.judgment.analysis_error {
                    lines.push(error.clone());
                }
                if let Some(error) = row.decision.as_ref().and_then(|d| d.error.as_ref()) {
                    lines.push(error.clone());
                }
                lines.push(String::new());
                lines.extend(event.text.lines().map(str::to_owned));
                let lines: Vec<_> = lines
                    .iter()
                    .flat_map(|line| wrap_cells(line, area.width as usize))
                    .collect();
                self.detail_max = lines.len().saturating_sub(height as usize);
                self.detail_offset = self.detail_offset.min(self.detail_max);
                for (offset, text) in lines
                    .iter()
                    .skip(self.detail_offset)
                    .take(height as usize)
                    .enumerate()
                {
                    put(
                        frame,
                        y + offset as u16,
                        0,
                        area.width,
                        text,
                        Style::default(),
                    );
                }
            } else {
                let first = self.selected[self.tab] / self.page_size * self.page_size;
                for (i, row) in self
                    .rows
                    .iter()
                    .enumerate()
                    .skip(first)
                    .take(self.page_size)
                {
                    let row_y = y + ((i - first) * 2) as u16;
                    let style = if i == self.selected[self.tab] {
                        Style::default()
                            .bg(Color::DarkGray)
                            .add_modifier(Modifier::BOLD)
                    } else {
                        Style::default()
                    }
                    .fg(color(row));
                    put(
                        frame,
                        row_y,
                        0,
                        area.width,
                        &format!(
                            "{} | {} | {}",
                            row.event.timestamp.as_deref().unwrap_or("-"),
                            label(row),
                            source(&row.event),
                        ),
                        style,
                    );
                    put(
                        frame,
                        row_y + 1,
                        2,
                        area.width - 2,
                        row.event.text.lines().next().unwrap_or(""),
                        Style::default(),
                    );
                    self.row_hits.push((i, Rect::new(0, row_y, area.width, 2)));
                }
                if self.rows.is_empty() {
                    put(
                        frame,
                        y,
                        0,
                        area.width,
                        "No matching events in this window.",
                        Style::default().fg(Color::Gray),
                    );
                }
            }
            put(
                frame,
                footer_y - 2,
                0,
                area.width,
                &format!(
                    "{}: {} {} | row {}",
                    TABS[self.tab],
                    self.rows.len(),
                    if self.tab == 4 && self.search_instances.is_none() {
                        "groups"
                    } else {
                        "instances"
                    },
                    if self.rows.is_empty() {
                        0
                    } else {
                        self.selected[self.tab] + 1
                    },
                ),
                Style::default().fg(Color::Gray),
            );
        }
        put(
            frame,
            footer_y - 1,
            0,
            area.width,
            if self.message.is_empty() {
                if self.tab == 4 {
                    "Search covers a frozen window; / asks again on current logs."
                } else if progress.phase == "Collecting" {
                    "Collecting logs; Enter opens a frozen event detail."
                } else {
                    "Collection finished; browse events or press q to exit."
                }
            } else {
                &self.message
            },
            Style::default().fg(Color::Gray),
        );
        put(
            frame,
            footer_y,
            0,
            area.width,
            if self.tab == 4 {
                "/ ask | f local | i instances | x stop search | Enter detail | Esc back | q quit"
            } else {
                "1-5 / Tab filters | Up/Down scroll | / ask | f find | Enter detail | q quit"
            },
            Style::default().fg(ACCENT),
        );
    }
}

fn matches_tab(row: &Row, tab: usize) -> bool {
    match tab {
        0 => !row.pending && row.event.judgment.importance == Importance::Important,
        1 => !row.pending && row.event.judgment.importance == Importance::Routine,
        2 => {
            !row.pending
                && matches!(
                    row.event.judgment.importance,
                    Importance::Uncertain | Importance::Unknown
                )
        }
        _ => true,
    }
}
fn source(event: &Event) -> String {
    event
        .source
        .get("path")
        .and_then(|v| v.as_str())
        .map(str::to_owned)
        .unwrap_or_else(|| {
            ["namespace", "pod", "container"]
                .iter()
                .filter_map(|key| event.source.get(*key).and_then(|v| v.as_str()))
                .collect::<Vec<_>>()
                .join("/")
        })
}
fn label(row: &Row) -> String {
    if let Some(decision) = &row.decision {
        return format!(
            "{}{} | {} instances",
            match decision.relevance {
                Relevance::Match => "MATCH",
                Relevance::Possible => "POSSIBLE",
                _ => "NOT EVALUATED",
            },
            decision
                .confidence
                .map(|c| format!(" | {:.0}% relevance", c * 100.0))
                .unwrap_or_else(|| if decision.relevance == Relevance::Match {
                    " | local exact".into()
                } else {
                    String::new()
                }),
            row.count,
        );
    }
    if row.pending {
        return "pending".into();
    }
    format!(
        "{}{}",
        match row.event.judgment.importance {
            Importance::Important => "important",
            Importance::Routine => "routine",
            Importance::Uncertain => "uncertain",
            Importance::Unknown => "unknown",
        },
        row.event
            .judgment
            .importance_confidence
            .map(|c| format!(" | {:.0}% confidence", c * 100.0))
            .unwrap_or_default(),
    )
}
fn color(row: &Row) -> Color {
    if row.pending {
        return Color::Gray;
    }
    match row.event.judgment.importance {
        Importance::Important => Color::LightRed,
        Importance::Routine => Color::Green,
        _ => Color::Yellow,
    }
}
fn put(frame: &mut Frame, y: u16, x: u16, width: u16, text: &str, style: Style) {
    let area = frame.area();
    if y < area.height && x < area.width {
        frame.render_widget(
            Paragraph::new(Line::raw(console(text))).style(style),
            Rect::new(x, y, width.min(area.width - x), 1),
        );
    }
}
fn wrap_cells(text: &str, width: usize) -> Vec<String> {
    let mut lines = Vec::new();
    let mut line = String::new();
    let mut used = 0;
    for c in console(text).chars() {
        let cells = c.width().unwrap_or(0);
        if !line.is_empty() && used + cells > width.max(1) {
            lines.push(std::mem::take(&mut line));
            used = 0;
        }
        line.push(c);
        used += cells;
    }
    lines.push(line);
    lines
}

pub async fn browse(
    mut screen: Screen,
    progress: SharedProgress,
    metrics: SharedMetrics,
    search_client: Option<Jev>,
    offline: bool,
    stop: CancellationToken,
    interrupted: CancellationToken,
) -> Result<bool, &'static str> {
    let _cancel_on_exit = stop.clone().drop_guard();
    let mut browser = Browser::new(search_client);
    let mut input = EventStream::new();
    let mut tick = tokio::time::interval(Duration::from_millis(100));
    tick.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
    let result = loop {
        let counters = browser.refresh(&progress);
        let status = {
            let m = metrics.lock().expect("metrics lock");
            format!(
                "streams {} | queue {} (peak {}) | dropped {} | gaps {}",
                m.active_streams, m.queue_depth, m.queue_high_water, m.dropped, m.coverage_gaps
            )
        };
        if screen
            .terminal
            .draw(|f| browser.draw(f, &counters, &status, offline))
            .is_err()
        {
            break Err("Cannot draw terminal UI");
        }
        tokio::select! {
            biased;
            _ = interrupted.cancelled() => break Ok(true),
            event = input.next() => match event {
                Some(Ok(event)) => if !browser.input(event) { break Ok(browser.interrupted); },
                _ => break Err("Terminal input closed"),
            },
            _ = tick.tick() => (),
        }
    };
    stop.cancel();
    if let Some(mut search) = browser.search.take() {
        search.stop.cancel();
        let _ = (&mut search.task).await;
    }
    result
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::events::{Line as LogLine, Parser, Source};
    use ratatui::backend::TestBackend;

    fn events() -> Vec<Event> {
        let mut parser = Parser::new(Source::new());
        let mut events = Vec::new();
        for text in ["ERROR failed", "INFO ready", "WARN retry", "INFO unknown"] {
            events.extend(parser.feed(LogLine {
                bytes: text.as_bytes().to_vec(),
                truncated: false,
                private: false,
            }));
        }
        events.extend(parser.flush());
        for (event, importance) in events.iter_mut().zip([
            Importance::Important,
            Importance::Routine,
            Importance::Uncertain,
            Importance::Unknown,
        ]) {
            event.judgment.importance = importance;
        }
        events
    }
    fn key(browser: &mut Browser, code: KeyCode) -> bool {
        browser.input(Input::Key(KeyEvent::new(code, KeyModifiers::NONE)))
    }
    fn draw(browser: &mut Browser, progress: &SharedProgress, width: u16, height: u16) -> String {
        let counters = browser.refresh(progress);
        let mut terminal = Terminal::new(TestBackend::new(width, height)).unwrap();
        terminal
            .draw(|f| browser.draw(f, &counters, "queue 0 | dropped 0", true))
            .unwrap();
        terminal
            .backend()
            .buffer()
            .content
            .iter()
            .map(|c| c.symbol())
            .collect()
    }

    #[test]
    fn tabs_mouse_selection_and_live_eviction_preserve_the_selected_event() {
        let progress = Progress::new(4, Usage::new(0.0, 0.0));
        progress.lock().unwrap().commit(&events(), None);
        let mut browser = Browser::new(None);
        draw(&mut browser, &progress, 100, 24);
        assert_eq!(browser.rows.len(), 1);
        key(&mut browser, KeyCode::Char('4'));
        draw(&mut browser, &progress, 100, 24);
        assert_eq!(browser.rows.len(), 4);
        key(&mut browser, KeyCode::End);
        let selected = browser.anchors[3].clone();
        let mut event = events().remove(0);
        event.id = "new".into();
        progress.lock().unwrap().commit(&[event], None);
        draw(&mut browser, &progress, 100, 24);
        assert_eq!(
            Some(browser.rows[browser.selected[3]].event.id.clone()),
            selected
        );
        let rect = browser.tabs[1].1;
        browser.input(Input::Mouse(crossterm::event::MouseEvent {
            kind: MouseEventKind::Down(MouseButton::Left),
            column: rect.x,
            row: rect.y,
            modifiers: KeyModifiers::NONE,
        }));
        draw(&mut browser, &progress, 100, 24);
        assert_eq!(browser.tab, 1);
        assert_eq!(browser.rows.len(), 1);
        assert!(!key(&mut browser, KeyCode::Char('q')));
    }

    #[test]
    fn pending_unknown_details_unicode_and_small_resizes_are_safe() {
        let progress = Progress::new(4, Usage::new(0.0, 0.0));
        let mut e = events();
        e[0].text = "ERROR failed\n  at example.rs:12\n界界 café \x1b]52;c;injected\x07".into();
        progress.lock().unwrap().commit(&e, None);
        let mut browser = Browser::new(None);
        draw(&mut browser, &progress, 100, 24);
        key(&mut browser, KeyCode::Enter);
        progress.lock().unwrap().events.clear();
        progress.lock().unwrap().revision += 1;
        let rendered = draw(&mut browser, &progress, 100, 24);
        assert!(rendered.contains("example.rs:12"));
        assert!(!rendered.contains("injected"));
        assert!(rendered.contains("界"));
        assert_eq!(wrap_cells("界界abc", 4), ["界界", "abc"]);
        assert!(draw(&mut browser, &progress, 20, 3).contains("Enlarge terminal"));
        draw(&mut browser, &progress, 35, 10);
        key(&mut browser, KeyCode::Esc);
        progress.lock().unwrap().begin(&e);
        key(&mut browser, KeyCode::Char('4'));
        draw(&mut browser, &progress, 100, 24);
        assert!(browser.rows.iter().all(|r| r.pending));
        key(&mut browser, KeyCode::Char('3'));
        draw(&mut browser, &progress, 100, 24);
        assert!(
            browser.rows.is_empty(),
            "pending events aren't falsely labeled unknown"
        );
    }

    #[tokio::test]
    async fn local_search_and_instances_survive_eviction_and_editor_handles_unicode() {
        let progress = Progress::new(4, Usage::new(0.0, 0.0));
        let mut events = events();
        events[0].text = "GET / client=192.0.2.1 status=200 café".into();
        let mut repeat = events[0].clone();
        repeat.id = "second".into();
        progress
            .lock()
            .unwrap()
            .commit(&[events[0].clone(), repeat, events[1].clone()], None);
        let mut browser = Browser::new(None);
        draw(&mut browser, &progress, 100, 24);
        key(&mut browser, KeyCode::Char('f'));
        for c in "client=192.0.2.1 status=200 café".chars() {
            key(&mut browser, KeyCode::Char(c));
        }
        key(&mut browser, KeyCode::Backspace);
        key(&mut browser, KeyCode::Char('é'));
        key(&mut browser, KeyCode::Tab);
        key(&mut browser, KeyCode::Tab);
        assert_eq!(browser.editor.as_ref().unwrap().mode, Mode::Literal);
        key(&mut browser, KeyCode::Enter);
        (&mut browser.search.as_mut().unwrap().task).await.unwrap();
        progress.lock().unwrap().events.clear();
        progress.lock().unwrap().revision += 1;
        let rendered = draw(&mut browser, &progress, 120, 30);
        assert!(rendered.contains("local exact"));
        assert_eq!(browser.rows.len(), 1);
        assert_eq!(browser.rows[0].count, 2);
        assert_eq!(
            browser
                .search
                .as_ref()
                .unwrap()
                .state
                .lock()
                .unwrap()
                .usage
                .request_attempts,
            0
        );
        key(&mut browser, KeyCode::Char('i'));
        draw(&mut browser, &progress, 120, 30);
        assert_eq!(browser.rows.len(), 2);
        key(&mut browser, KeyCode::Down);
        key(&mut browser, KeyCode::Enter);
        assert_eq!(browser.detail.as_ref().unwrap().event.id, "second");
        key(&mut browser, KeyCode::Esc);
        key(&mut browser, KeyCode::Esc);
        draw(&mut browser, &progress, 120, 30);
        assert_eq!(browser.rows.len(), 1);
    }

    #[test]
    fn ctrl_c_exits_even_during_query_editing_and_stdin_validation_is_explicit() {
        let mut browser = Browser::new(None);
        browser.ask(Mode::Literal);
        assert!(!browser.input(Input::Key(KeyEvent::new(
            KeyCode::Char('c'),
            KeyModifiers::CONTROL
        ))));
        assert!(browser.interrupted);
        assert!(validate(true).unwrap_err().contains("stdin"));
    }
}
