//! Byte-bounded framing, multiline events, and best-effort redaction before storage.
use regex::Regex;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use sha2::{Digest, Sha256};
use std::{collections::BTreeMap, sync::LazyLock};

pub const MAX_LINE: usize = 65_536;
pub const MAX_EVENT: usize = 16_000;
pub const MAX_INPUT: u64 = 32 * 1024 * 1024;
pub type Source = BTreeMap<String, Value>;
fn rx(pattern: &str) -> Regex {
    Regex::new(pattern).expect("constant regex")
}
static ANSI: LazyLock<Regex> =
    LazyLock::new(|| rx(r"\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))"));
static STAMP: LazyLock<Regex> =
    LazyLock::new(|| rx(r"^\d{4}-\d\d-\d\d[T ][\d:.]+(?:Z|[+-]\d\d:\d\d)?[ \t]"));
static CONT: LazyLock<Regex> = LazyLock::new(|| {
    rx(
        r"^(?:\s+\S|Caused by:|Suppressed:|Traceback |[\w.]+(?:Error|Exception):|\s*\.\.\. \d+ more)",
    )
});
static FIELD: LazyLock<Regex> = LazyLock::new(|| {
    rx(
        r"(?i)(password|passwd|secret|token|authorization|cookie|api[-_]?key|access[-_]?key(?:[-_]?id)?|private[-_]?key|credential)",
    )
});
static SECRET: LazyLock<Regex> = LazyLock::new(|| {
    rx(
        r#"(?ix)(["']?(?:password|passwd|secret|(?:access_|refresh_)?token|api[-_]?key|access[-_]?key(?:[-_]?id)?|private[-_]?key|authorization|cookie|credentials?)["']?\s*[:=]\s*)("(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*'|[^\s,;]+)"#,
    )
});
static AUTH: LazyLock<Regex> = LazyLock::new(|| rx(r"(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+"));
static URL: LazyLock<Regex> =
    LazyLock::new(|| rx(r"(\b[a-zA-Z][a-zA-Z0-9+.-]{0,31}://)[^\s/@:]+:[^\s/@]+@"));
static JWT: LazyLock<Regex> =
    LazyLock::new(|| rx(r"\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b"));
static PEM: LazyLock<Regex> =
    LazyLock::new(|| rx(r"-----(BEGIN|END) (?:[A-Z0-9]+ )*PRIVATE KEY-----"));
static RULES: LazyLock<Vec<(&str, Regex)>> = LazyLock::new(|| {
    vec![
        (
            "error_or_failure_keyword",
            rx(
                r"(?i)\b(error|fatal|panic|exception|critical|oomkilled|out of memory|crashloopbackoff)\b",
            ),
        ),
        (
            "http_5xx",
            rx(r#"(?i)(?:status["\s:=]+|HTTP/\d(?:\.\d)?["\s]+)5\d\d\b"#),
        ),
        ("warning_level", rx(r"(?i)\b(warn|warning)\b")),
    ]
});

pub fn hash(value: &impl Serialize) -> String {
    let bytes = serde_json::to_vec(value).expect("serializable identity");
    format!("{:x}", Sha256::digest(bytes))[..20].to_owned()
}
pub fn strip_ansi(text: &str) -> String {
    ANSI.replace_all(text, "").into_owned()
}
pub fn console(text: &str) -> String {
    strip_ansi(text)
        .chars()
        .map(|c| if c.is_control() { ' ' } else { c })
        .collect()
}
fn scrub(text: &str) -> String {
    let text = AUTH.replace_all(text, "$1 [REDACTED]");
    let text = URL.replace_all(&text, "$1[REDACTED]@");
    let text = JWT.replace_all(&text, "[REDACTED]");
    SECRET.replace_all(&text, "${1}\"[REDACTED]\"").into_owned()
}
pub fn redact(text: &str) -> String {
    fn clean(value: &mut Value) {
        match value {
            Value::Object(map) => {
                for (key, value) in map {
                    if FIELD.is_match(key) {
                        *value = Value::String("[REDACTED]".into());
                    } else {
                        clean(value);
                    }
                }
            }
            Value::Array(items) => {
                for v in items {
                    clean(v);
                }
            }
            Value::String(s) => *s = scrub(s),
            _ => (),
        }
    }
    let text = strip_ansi(text);
    if let Ok(mut value) = serde_json::from_str::<Value>(&text) {
        clean(&mut value);
        value.to_string()
    } else {
        scrub(&text)
    }
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Event {
    pub id: String,
    pub source: Source,
    pub timestamp: Option<String>,
    pub text: String,
    pub line_start: u64,
    pub line_end: u64,
    pub line_count: u64,
    pub truncated: bool,
    pub group_id: String,
    #[serde(flatten)]
    pub judgment: crate::jev::Judgment,
    pub baseline: Baseline,
    pub analysis_reused: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub analysis_representative_id: Option<String>,
}
#[derive(Clone, Debug, Default, Serialize, Deserialize)]
pub struct Baseline {
    pub important: bool,
    pub signals: Vec<String>,
}
pub fn baseline(text: &str) -> Baseline {
    let signals: Vec<_> = RULES
        .iter()
        .filter(|(_, r)| r.is_match(text))
        .map(|(id, _)| (*id).to_owned())
        .collect();
    Baseline {
        important: !signals.is_empty(),
        signals,
    }
}

#[derive(Debug)]
pub struct Line {
    pub bytes: Vec<u8>,
    pub truncated: bool,
    pub private: bool,
}
/// Retains only MAX_LINE bytes. PEM scanning includes discarded bytes and split markers.
#[derive(Default)]
pub struct Framer {
    buffer: Vec<u8>,
    truncated: bool,
    private: bool,
    in_key: bool,
    marker: String,
}
impl Framer {
    pub fn push(&mut self, bytes: &[u8]) -> Vec<Line> {
        let mut lines = Vec::new();
        for chunk in bytes.split_inclusive(|b| *b == b'\n') {
            self.private |= self.in_key;
            let scan = format!("{}{}", self.marker, String::from_utf8_lossy(chunk));
            for m in PEM.captures_iter(&scan) {
                self.private = true;
                self.in_key = &m[1] == "BEGIN";
            }
            self.marker = scan
                .chars()
                .rev()
                .take(128)
                .collect::<String>()
                .chars()
                .rev()
                .collect();
            let room = MAX_LINE - self.buffer.len();
            self.buffer
                .extend_from_slice(&chunk[..chunk.len().min(room)]);
            self.truncated |= chunk.len() > room;
            if chunk.ends_with(b"\n")
                && let Some(line) = self.finish()
            {
                lines.push(line);
            }
        }
        lines
    }
    pub fn finish(&mut self) -> Option<Line> {
        if self.buffer.is_empty() && !self.truncated {
            return None;
        }
        let line = Line {
            bytes: std::mem::take(&mut self.buffer),
            truncated: self.truncated,
            private: self.private,
        };
        self.truncated = false;
        self.private = self.in_key;
        self.marker.clear();
        Some(line)
    }
}
/// Parser state is retained across stream reconnects, including private-key suppression.
pub struct Parser {
    source: Source,
    pending: Option<Event>,
    line: u64,
}
impl Parser {
    pub fn new(source: Source) -> Self {
        Self {
            source,
            pending: None,
            line: 0,
        }
    }
    pub fn feed(&mut self, line: Line) -> Option<Event> {
        self.line += 1;
        let raw = String::from_utf8_lossy(&line.bytes);
        let text = strip_ansi(raw.trim_end_matches(['\r', '\n']));
        let stamp = STAMP.find(&text);
        let timestamp = stamp.map(|m| m.as_str().trim().to_owned());
        let body = stamp.map_or(text.as_str(), |m| &text[m.end()..]);
        if body.is_empty() && !line.private {
            return None;
        }
        let safe = if line.private {
            "[REDACTED PRIVATE KEY]".to_owned()
        } else {
            redact(body)
        };
        if CONT.is_match(body)
            && let Some(pending) = self.pending.as_mut()
        {
            pending.line_end = self.line;
            pending.line_count += 1;
            pending.text.push('\n');
            pending.text.push_str(&safe);
            pending.truncated |= line.truncated || pending.text.len() > MAX_EVENT;
            truncate(&mut pending.text, MAX_EVENT);
            return None;
        }
        let completed = self.flush();
        let truncated = line.truncated || safe.len() > MAX_EVENT;
        let mut safe = safe;
        truncate(&mut safe, MAX_EVENT);
        self.pending = Some(Event {
            id: String::new(),
            source: self.source.clone(),
            timestamp,
            text: safe,
            line_start: self.line,
            line_end: self.line,
            line_count: 1,
            truncated,
            group_id: String::new(),
            judgment: Default::default(),
            baseline: Default::default(),
            analysis_reused: false,
            analysis_representative_id: None,
        });
        completed
    }
    pub fn flush(&mut self) -> Option<Event> {
        let mut event = self.pending.take()?;
        event.id = hash(&(
            &event.source,
            event.line_start,
            &event.timestamp,
            &event.text,
        ));
        event.group_id = crate::grouping::group_id(&event);
        event.baseline = baseline(&event.text);
        Some(event)
    }
    pub fn has_pending(&self) -> bool {
        self.pending.is_some()
    }
}
fn truncate(s: &mut String, max: usize) {
    let mut end = max.min(s.len());
    while !s.is_char_boundary(end) {
        end -= 1;
    }
    s.truncate(end);
}

#[cfg(test)]
mod tests {
    use super::*;
    pub fn parse(raw: &[u8]) -> Vec<Event> {
        let mut framer = Framer::default();
        let mut parser = Parser::new(Source::new());
        let mut events = vec![];
        for chunk in raw.chunks(13) {
            for line in framer.push(chunk) {
                events.extend(parser.feed(line));
            }
        }
        if let Some(line) = framer.finish() {
            events.extend(parser.feed(line));
        }
        events.extend(parser.flush());
        events
    }
    #[test]
    fn multiline_ids_and_unicode_bounds() {
        let raw = "2026-09-20T12:00:00Z ERROR failed\n  stack frame\nINFO ready\n";
        let e = parse(raw.as_bytes());
        assert_eq!(e.len(), 2);
        assert_eq!(e[0].line_count, 2);
        assert_eq!(e[0].timestamp.as_deref(), Some("2026-09-20T12:00:00Z"));
        assert_eq!(e[0].id, parse(raw.as_bytes())[0].id);
        let e = parse("😀".repeat(MAX_LINE).as_bytes());
        assert!(e[0].truncated);
        assert!(e[0].text.len() <= MAX_EVENT);
    }
    #[test]
    fn credentials_and_streamed_pem() {
        let raw = concat!(
            "\x1b[31mERROR password=fixture-value\x1b[0m\n-----BEGIN ENCRYPTED PRIVATE ",
            "KEY-----\nprivate-fixture\n-----END ENCRYPTED PRIVATE ",
            "KEY-----\nINFO ready\n"
        );
        let e = parse(raw.as_bytes());
        let text = serde_json::to_string(&e).unwrap();
        assert!(!text.contains("fixture-value"));
        assert!(!text.contains("private-fixture"));
        assert!(!text.contains("\\u001b"));
        assert!(text.contains("INFO ready"));
        let safe = redact(
            r#"{"nested":{"access_key_id":"fake-value","secret":{"x":"hidden"}},"a":"Bearer fake-value"}"#,
        );
        assert!(!safe.contains("fake-value"));
        assert!(!safe.contains("hidden"));
        assert!(!redact(r#"message password="a\"b" done"#).contains("a\\"));
    }
    #[test]
    fn key_marker_in_discarded_oversize_tail() {
        let marker = concat!(
            "-----BEGIN PRIVATE ",
            "KEY-----\nprivate-fixture\n-----END PRIVATE ",
            "KEY-----\nready"
        );
        let raw = format!("{}{marker}", "x".repeat(MAX_LINE + 7));
        let e = parse(raw.as_bytes());
        assert!(
            !serde_json::to_string(&e)
                .unwrap()
                .contains("private-fixture")
        );
        assert!(e[0].truncated);
    }
}
