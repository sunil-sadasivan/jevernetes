//! Clean-room, bounded online Drain-style templates. No event is removed.
use crate::{controller::cacheable, events::Event, jev::Judgment, report::SharedMetrics};
use std::{
    collections::HashMap,
    time::{Duration, Instant},
};

pub const MAX_BYTES: usize = 2048;
pub const MAX_TOKENS: usize = 128;
pub const DEFAULT_CAPACITY: usize = 256;
pub const MAX_CAPACITY: usize = 1024;

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, clap::ValueEnum)]
pub enum Strategy {
    Off,
    #[default]
    Exact,
    Drain,
}

struct Partition {
    id: u64,
    // None is a wildcard, introduced only at an allowlisted opaque-ID position.
    tokens: Vec<Option<String>>,
    version: String,
    verdict: Option<(Instant, Judgment, String)>,
}
/// A version snapshot: responses can never populate a different template version.
pub struct Ticket {
    key: String,
    template_id: u64,
    version: String,
}
pub struct Drain {
    partitions: HashMap<String, Partition>,
    capacity: usize,
    next_id: u64,
    ttl: Duration,
    metrics: SharedMetrics,
}

/// Only named opaque IDs can vary. UUIDs use the canonical 8-4-4-4-12
/// syntax; hex IDs use common 64/128/256-bit widths. All telemetry stays literal.
fn variable(token: &str) -> Option<String> {
    let (name, value) = token.split_once('=')?;
    if !matches!(
        name,
        "request_id" | "request-id" | "trace_id" | "span_id" | "correlation_id"
    ) {
        return None;
    }
    let hex = matches!(value.len(), 16 | 32 | 64) && value.bytes().all(|b| b.is_ascii_hexdigit());
    let uuid = value.len() == 36
        && value.bytes().enumerate().all(|(i, b)| {
            if matches!(i, 8 | 13 | 18 | 23) {
                b == b'-'
            } else {
                b.is_ascii_hexdigit()
            }
        });
    if hex {
        Some(format!("{name}:hex{}", value.len()))
    } else if uuid {
        Some(format!("{name}:uuid"))
    } else {
        None
    }
}

fn identity(event: &Event) -> Option<(String, Vec<Option<String>>)> {
    if event.judgment.analysis_error.is_some()
        || event.truncated
        || event.sensitive
        || event.text.contains("[REDACTED")
        || event.text.contains("<*>")
        || event.text.len() > MAX_BYTES
        || event.text.chars().any(char::is_control)
        || event.line_count != 1
    {
        return None;
    }
    let tokens: Vec<_> = event.text.split_whitespace().collect();
    if tokens.is_empty() || tokens.len() > MAX_TOKENS || tokens.join(" ") != event.text {
        return None;
    }
    let shape: Vec<_> = tokens.iter().map(|t| variable(t)).collect();
    // Retain exact literal tokens; a variable is tagged separately from a literal.
    let skeleton: Vec<_> = tokens
        .iter()
        .zip(&shape)
        .map(|(t, v)| (v, if v.is_none() { Some(t) } else { None }))
        .collect();
    let scope = if event.source.get("type").and_then(|v| v.as_str()) == Some("kubernetes") {
        let mut scope = crate::events::Source::new();
        for name in ["type", "context", "namespace", "container", "kind"] {
            let value = event.source.get(name)?.as_str()?;
            if value.is_empty() || value.len() > 253 {
                return None;
            }
            scope.insert(name.into(), value.into());
        }
        scope
    } else {
        // Files and other sources retain the entire source identity.
        event.source.clone()
    };
    let key = serde_json::to_string(&(scope, &event.baseline, skeleton)).ok()?;
    (key.len() <= 8192).then_some((key, shape))
}

impl Drain {
    pub fn new(
        capacity: usize,
        ttl: Duration,
        metrics: SharedMetrics,
    ) -> Result<Self, &'static str> {
        if !(1..=MAX_CAPACITY).contains(&capacity)
            || ttl.is_zero()
            || ttl > Duration::from_secs(604800)
        {
            return Err("Drain capacity must be 1..1024 and TTL must be >0 and <=604800 seconds");
        }
        Ok(Self {
            partitions: HashMap::new(),
            capacity,
            next_id: 0,
            ttl,
            metrics,
        })
    }
    fn fallback(&self, capacity: bool) {
        let mut m = self.metrics.lock().expect("metrics");
        m.drain_fallbacks += 1;
        m.drain_capacity_fallbacks += u64::from(capacity);
    }
    /// Apply an unchanged cached verdict, or return a ticket for an ordinary
    /// classification. None without analysis_reused means a safe fallback miss.
    pub fn prepare(&mut self, event: &mut Event, now: Instant, rescore: bool) -> Option<Ticket> {
        event.analysis_reused = false;
        event.analysis_representative_id = None;
        let Some((key, shape)) = identity(event) else {
            self.fallback(false);
            return None;
        };
        let tokens: Vec<_> = event.text.split(' ').collect();
        let created = !self.partitions.contains_key(&key);
        if created {
            if self.partitions.len() >= self.capacity {
                self.fallback(true);
                return None;
            }
            // One cluster per exact source/baseline/literal shape and token count.
            // IDs remain stable across generalization and are never recycled.
            let Some(next_id) = self.next_id.checked_add(1) else {
                self.fallback(true);
                return None;
            };
            self.next_id = next_id;
            self.partitions.insert(
                key.clone(),
                Partition {
                    id: next_id,
                    tokens: tokens.iter().map(|t| Some((*t).to_owned())).collect(),
                    version: event.text.clone(),
                    verdict: None,
                },
            );
            self.metrics.lock().expect("metrics").drain_active_templates = self.partitions.len();
        }
        let p = self.partitions.get_mut(&key).expect("partition inserted");
        let valid = p.tokens.len() == tokens.len()
            && p.tokens
                .iter()
                .zip(&tokens)
                .zip(&shape)
                .all(|((t, raw), variable)| t.as_deref() == Some(*raw) || variable.is_some());
        if !valid {
            self.partitions.remove(&key);
            let mut m = self.metrics.lock().expect("metrics");
            m.drain_evictions += 1;
            m.drain_active_templates = self.partitions.len();
            drop(m);
            self.fallback(false);
            return None;
        }
        let mut changed = false;
        for (token, raw) in p.tokens.iter_mut().zip(&tokens) {
            if token.as_deref().is_some_and(|t| t != *raw) {
                *token = None;
                changed = true;
            }
        }
        let mut m = self.metrics.lock().expect("metrics");
        if created {
            m.drain_templates_created += 1;
        } else if changed {
            m.drain_templates_changed += 1;
        } else {
            m.drain_templates_matched += 1;
        }
        if changed {
            p.version = p
                .tokens
                .iter()
                .map(|t| t.as_deref().unwrap_or("<*>"))
                .collect::<Vec<_>>()
                .join(" ");
            p.verdict = None;
        } else if !rescore && let Some((at, judgment, id)) = &p.verdict {
            if now.saturating_duration_since(*at) < self.ttl {
                event.judgment = judgment.clone();
                event.analysis_reused = true;
                event.analysis_representative_id = Some(id.clone());
                m.drain_verdict_reuses += 1;
                m.drain_classifications_avoided += 1;
                return None;
            }
            p.verdict = None;
            m.drain_expirations += 1;
        }
        Some(Ticket {
            key,
            template_id: p.id,
            version: p.version.clone(),
        })
    }
    /// Full current evidence is already bounded by MAX_BYTES for eligible events.
    /// Add the current template only to the remote representation, never the Event.
    pub fn representative(event: &Event, ticket: Option<&Ticket>) -> Event {
        let mut representative = event.clone();
        if let Some(ticket) = ticket {
            representative.text = format!(
                "Drain template: {}\nCurrent observation: {}",
                ticket.version, event.text
            );
        }
        representative
    }
    pub fn complete(&mut self, ticket: Ticket, event: &Event, now: Instant) {
        if let Some(p) = self.partitions.get_mut(&ticket.key)
            && p.id == ticket.template_id
            && p.version == ticket.version
        {
            p.verdict = if cacheable(event)
                && event.judgment.importance != crate::jev::Importance::Uncertain
                && !matches!(
                    event.judgment.category,
                    crate::jev::Category::Security | crate::jev::Category::Fraud
                )
                && event.id.len() <= 128
                && !event.sensitive
                && !event.text.contains("[REDACTED")
            {
                Some((now, event.judgment.clone(), event.id.clone()))
            } else {
                None
            };
        }
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use crate::{
        events::{Line, Parser},
        jev::{Category, Importance, Severity},
        report::Metrics,
    };
    use std::sync::{Arc, Mutex};
    pub(crate) fn event(i: u32) -> Event {
        let mut p = Parser::new(serde_json::from_value(serde_json::json!({
            "type":"kubernetes", "context":"test", "namespace":"demo", "container":"api", "kind":"container",
            "pod":format!("api-{i}"), "pod_uid":format!("uid-{i}"), "restart_count":i
        })).unwrap());
        p.feed(Line {
            bytes: format!(
                "INFO served status=200 ip=127.0.0.1 count=1 duration_ms=1 request_id={i:016x}"
            )
            .into_bytes(),
            truncated: false,
            private: false,
        });
        p.flush().unwrap()
    }
    pub(crate) fn verdict() -> Judgment {
        Judgment {
            importance: Importance::Important,
            severity: Severity::Impact,
            category: Category::Dependency,
            importance_confidence: Some(0.99),
            severity_confidence: Some(0.99),
            category_confidence: Some(0.99),
            analysis_error: None,
        }
    }
    fn miner(capacity: usize) -> Drain {
        Drain::new(
            capacity,
            Duration::from_secs(300),
            Arc::new(Mutex::new(Metrics::default())),
        )
        .unwrap()
    }
    fn classify(d: &mut Drain, e: &mut Event, now: Instant) {
        let t = d.prepare(e, now, false).expect("representative");
        assert!(!e.analysis_reused);
        e.judgment = verdict();
        d.complete(t, e, now);
    }
    #[test]
    fn collapse_generalization_unchanged_reuse_and_non_sliding_ttl() {
        let mut d = miner(4);
        let now = Instant::now();
        classify(&mut d, &mut event(1), now);
        classify(&mut d, &mut event(2), now);
        for i in 3..100 {
            let mut e = event(i);
            assert!(
                d.prepare(&mut e, now + Duration::from_secs(i.into()), false)
                    .is_none()
            );
            assert!(e.analysis_reused);
        }
        let m = d.metrics.lock().unwrap();
        assert_eq!(
            (
                m.drain_templates_created,
                m.drain_templates_changed,
                m.drain_verdict_reuses
            ),
            (1, 1, 97)
        );
        drop(m);
        classify(&mut d, &mut event(100), now + Duration::from_secs(300));
        assert_eq!(d.metrics.lock().unwrap().drain_expirations, 1);
        assert!(
            d.prepare(&mut event(101), now + Duration::from_secs(301), true)
                .is_some()
        );
    }
    #[test]
    fn opaque_id_grammar_and_literal_telemetry() {
        for name in [
            "request_id",
            "request-id",
            "trace_id",
            "span_id",
            "correlation_id",
        ] {
            for value in [
                "0123456789abcdef",
                "0123456789abcdef0123456789abcdef",
                "01234567-89ab-cdef-0123-456789abcdef",
            ] {
                assert!(variable(&format!("{name}={value}")).is_some());
            }
            for value in [
                "--------",
                "deadbeef",
                "123-456789abcdef0",
                "0123456789abcdeg",
                "0123456789abcdef/path",
                "0123456789abcdef=other",
                "01234567_89ab-cdef-0123-456789abcdef",
            ] {
                assert!(variable(&format!("{name}={value}")).is_none());
            }
        }
        for (a, b) in [
            ("ip=127.0.0.1", "ip=0.0.0.0"),
            ("client_ip=::1", "client_ip=::"),
            ("peer_ip=127.0.0.1", "peer_ip=8.8.8.8"),
            ("duration_ms=1", "duration_ms=86400000"),
            ("latency_ms=1", "latency_ms=99999"),
            ("count=1", "count=100"),
            ("bytes=1", "bytes=9999"),
            ("attempt=1", "attempt=100"),
            ("status=200", "status=401"),
            ("1", "9999"),
            ("/a", "/b"),
            ("allow", "deny"),
            ("user_id=0123456789abcdef", "user_id=fedcba9876543210"),
        ] {
            let mut first = event(1);
            first.text = format!("INFO {a}");
            let mut second = first.clone();
            second.text = format!("INFO {b}");
            assert_ne!(
                identity(&first).unwrap().0,
                identity(&second).unwrap().0,
                "{a} vs {b}"
            );
        }
    }
    #[test]
    fn stable_id_survives_generalization_and_stale_tickets_cannot_seed_recreated_partition() {
        let mut d = miner(1);
        let now = Instant::now();
        let old = d.prepare(&mut event(1), now, false).unwrap();
        let changed = d.prepare(&mut event(2), now, false).unwrap();
        assert_eq!(old.template_id, changed.template_id);
        assert_ne!(old.version, changed.version);
        d.partitions.values_mut().next().unwrap().tokens.clear();
        assert!(d.prepare(&mut event(1), now, false).is_none());
        let recreated = d.prepare(&mut event(1), now, false).unwrap();
        assert_ne!(old.template_id, recreated.template_id);
        let mut e = event(1);
        e.judgment = verdict();
        d.complete(old, &e, now);
        assert!(d.prepare(&mut event(1), now, false).is_some());
    }
    #[test]
    fn scope_baseline_and_literal_safety() {
        let a = event(1);
        assert_eq!(identity(&a).unwrap().0, identity(&event(2)).unwrap().0);
        for name in ["context", "namespace", "container", "kind"] {
            let mut b = a.clone();
            b.source.insert(name.into(), "other".into());
            assert_ne!(identity(&a).unwrap().0, identity(&b).unwrap().0);
        }
        let mut b = a.clone();
        b.source.remove("kind");
        assert!(identity(&b).is_none());
        for changed in [
            "INFO denied status=200",
            "INFO served status=401",
            "ERROR served status=200",
        ] {
            let mut b = a.clone();
            b.text = b.text.replace("INFO served status=200", changed);
            assert_ne!(identity(&a).unwrap().0, identity(&b).unwrap().0);
        }
        let mut b = a.clone();
        b.baseline.important = true;
        assert_ne!(identity(&a).unwrap().0, identity(&b).unwrap().0);
        b = a.clone();
        b.baseline.signals.push("local-risk".into());
        assert_ne!(identity(&a).unwrap().0, identity(&b).unwrap().0);
        b = a.clone();
        b.text = b.text.replace("count=1", "count=0");
        assert_ne!(identity(&a).unwrap().0, identity(&b).unwrap().0);
        let mut a = a;
        a.source = Default::default();
        a.source.insert("path".into(), "a.log".into());
        b = a.clone();
        b.source.insert("path".into(), "b.log".into());
        assert_ne!(identity(&a).unwrap().0, identity(&b).unwrap().0);
    }
    #[test]
    fn unsafe_input_failure_capacity_and_stale_version_fall_back() {
        let mut d = miner(1);
        let now = Instant::now();
        let mut a = event(1);
        let old = d.prepare(&mut a, now, false).unwrap();
        let mut b = event(2);
        let current = d.prepare(&mut b, now, false).unwrap();
        a.judgment = verdict();
        d.complete(old, &a, now);
        assert!(
            d.prepare(&mut event(3), now, false).is_some(),
            "stale response discarded"
        );
        b.judgment = Judgment::failed("synthetic");
        d.complete(current, &b, now);
        assert!(d.prepare(&mut event(3), now, false).is_some());
        for invalid in [
            "truncated",
            "private",
            "redacted",
            "multiline",
            "oversize",
            "tokens",
            "wildcard",
        ] {
            let mut e = event(3);
            match invalid {
                "truncated" => e.truncated = true,
                "private" => e.sensitive = true,
                "redacted" => e.text = "password=[REDACTED]".into(),
                "multiline" => e.text.push_str("\n stack"),
                "oversize" => e.text = "x".repeat(MAX_BYTES + 1),
                "tokens" => e.text = vec!["x"; MAX_TOKENS + 1].join(" "),
                _ => e.text.push_str(" <*>"),
            }
            assert!(d.prepare(&mut e, now, false).is_none());
            assert!(!e.analysis_reused);
        }
        for i in 0..100 {
            let mut e = event(3);
            e.source
                .insert("namespace".into(), format!("ns-{i}").into());
            assert!(d.prepare(&mut e, now, false).is_none());
            assert!(!e.analysis_reused);
        }
        assert_eq!(d.partitions.len(), 1);
        assert_eq!(d.metrics.lock().unwrap().drain_capacity_fallbacks, 100);
        assert!(Drain::new(0, Duration::from_secs(1), d.metrics.clone()).is_err());
        assert!(Drain::new(MAX_CAPACITY + 1, Duration::from_secs(1), d.metrics.clone()).is_err());
        assert!(Drain::new(1, Duration::ZERO, d.metrics.clone()).is_err());
    }
    #[test]
    fn inconsistent_template_state_evicts_and_falls_back() {
        let now = Instant::now();
        let mut d = miner(1);
        classify(&mut d, &mut event(1), now);
        let p = d.partitions.values_mut().next().unwrap();
        // Simulate a corrupt token-count partition. No cached verdict may survive.
        p.tokens.push(Some("unexpected".into()));
        let mut e = event(1);
        assert!(d.prepare(&mut e, now, false).is_none());
        assert!(!e.analysis_reused);
        assert!(d.partitions.is_empty());
        let m = d.metrics.lock().unwrap();
        assert_eq!(
            (
                m.drain_evictions,
                m.drain_fallbacks,
                m.drain_active_templates
            ),
            (1, 1, 0)
        );
    }
    #[test]
    fn unknown_invalid_confidence_and_restart_never_reuse() {
        let now = Instant::now();
        for bad in 0..7 {
            let mut d = miner(1);
            let mut e = event(1);
            let t = d.prepare(&mut e, now, false).unwrap();
            e.judgment = verdict();
            match bad {
                0 => e.judgment.category = Category::Unknown,
                1 => e.judgment.importance_confidence = None,
                2 => e.judgment.severity_confidence = Some(f64::NAN),
                3 => e.judgment.importance = Importance::Uncertain,
                4 => e.judgment.category = Category::Security,
                5 => e.judgment.category = Category::Fraud,
                _ => e.judgment = Judgment::default(),
            }
            d.complete(t, &e, now);
            assert!(d.prepare(&mut event(1), now, false).is_some());
        }
        let mut d = miner(1);
        classify(&mut d, &mut event(1), now);
        let mut restarted = miner(1);
        assert!(restarted.prepare(&mut event(1), now, false).is_some());
    }
}
