//! Operator-reviewed compiled rules. No proposal/report conversion or activation API.
use crate::{
    controller::{Contract, digest},
    events::{Event, Source},
    jev::Judgment,
    report::SharedMetrics,
    structured::{self, Kind},
};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::{
    collections::{BTreeMap, BTreeSet},
    io::Read,
    path::Path,
    sync::atomic::{AtomicU64, Ordering},
    time::{Duration, Instant},
};
const FILE_LIMIT: usize = 65536;
static NEXT_OWNER: AtomicU64 = AtomicU64::new(0);
const ERROR: &str = "Invalid reviewed template rules";
#[derive(Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Artifact {
    artifact_version: u32,
    schema_version: u32,
    rules: Vec<Rule>,
}
#[derive(Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Rule {
    id: String,
    version: u32,
    expires_at: i64,
    source_scope: Source,
    prefix_identity: PrefixIdentity,
    shape: BTreeMap<String, Kind>,
    required_literals: BTreeMap<String, Value>,
    normalize_paths: Vec<String>,
    protected_literals: BTreeMap<String, Value>,
    review: Review,
}
#[derive(Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Review {
    reviewer: String,
    review_id: String,
    compiler: String,
    reviewed_at: i64,
}
/// Empty strings mean whole-line JSON. Otherwise text contains a zeroed clock,
/// with its exact reviewed grammar retained separately (never an untyped wildcard).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct PrefixIdentity {
    text: String,
    clock_grammar: String,
}
fn prefix_identity(prefix: &str) -> Option<PrefixIdentity> {
    if prefix.is_empty() {
        return Some(PrefixIdentity {
            text: String::new(),
            clock_grammar: String::new(),
        });
    }
    if prefix.len() > 256
        || !prefix.is_ascii()
        || prefix.chars().any(char::is_control)
        || crate::events::redact(prefix) != prefix
        || prefix.to_ascii_lowercase().contains("[redacted")
    {
        return None;
    }
    let mut tokens: Vec<_> = prefix.strip_suffix(' ')?.split(' ').collect();
    if tokens.iter().any(|t| t.is_empty()) {
        return None;
    }
    // Supply the payload position even when the envelope is just clock + level.
    tokens.push("{}");
    let (index, clock_grammar) = crate::drain::logger_clock(&tokens)?;
    tokens.pop();
    for (i, token) in tokens.iter().enumerate() {
        if i == index {
            continue;
        }
        let token = token.strip_suffix(':').unwrap_or(token);
        let token = if token.starts_with('[') {
            token.strip_prefix('[')?.strip_suffix(']')?
        } else {
            token
        };
        // Literal logger names/separators only. No braces, quotes, escapes,
        // extra clocks, control/Unicode whitespace or unbalanced brackets.
        if token.is_empty()
            || !token
                .bytes()
                .all(|b| b.is_ascii_alphanumeric() || b"_.-/=".contains(&b))
        {
            return None;
        }
    }
    let clock: String = tokens[index]
        .chars()
        .map(|c| if c.is_ascii_digit() { '0' } else { c })
        .collect();
    tokens[index] = &clock;
    Some(PrefixIdentity {
        text: format!("{} ", tokens.join(" ")),
        clock_grammar,
    })
}
fn payload(text: &str) -> Option<(PrefixIdentity, Value)> {
    if text.len() > structured::MAX_BYTES || text.contains(['\n', '\r']) {
        return None;
    }
    // The first opening brace is the only candidate; never search/retry later
    // objects. Strict prefix validation rejects any preceding closing brace.
    let start = text.find('{')?;
    let prefix = prefix_identity(&text[..start])?;
    let value = structured::parse(&text.as_bytes()[start..], structured::MAX_BYTES)?;
    value.is_object().then_some((prefix, value))
}
fn scopes_compatible(a: &Source, b: &Source) -> bool {
    a.iter().all(|(k, v)| b.get(k).is_none_or(|w| v == w))
}
/// Validated immutable artifact; all construction goes through strict decoding.
#[derive(Clone)]
pub struct Rules {
    artifact: Artifact,
    digest: String,
}
fn token(s: &str) -> bool {
    !s.is_empty()
        && s.len() <= 64
        && s.bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"_.-".contains(&b))
}
fn scalar(v: &Value) -> bool {
    !v.is_object() && !v.is_array()
}
fn safe_scalar(v: &Value) -> bool {
    scalar(v)
        && v.as_str().is_none_or(|s| {
            s.len() <= 256
                && !s.chars().any(char::is_control)
                && crate::events::redact(s) == s
                && !s.to_ascii_lowercase().contains("[redacted")
        })
}
fn overlap(a: &str, b: &str) -> bool {
    a == b
        || a.strip_prefix(b).is_some_and(|s| s.starts_with('/'))
        || b.strip_prefix(a).is_some_and(|s| s.starts_with('/'))
}
impl Rules {
    /// Regular files including trusted Kubernetes projected symlinks, matching credential policy.
    pub fn load(path: &Path, now: i64) -> Result<Self, &'static str> {
        let meta = std::fs::metadata(path).map_err(|_| ERROR)?;
        if !meta.is_file() || meta.len() > FILE_LIMIT as u64 {
            return Err(ERROR);
        }
        let file = std::fs::File::open(path).map_err(|_| ERROR)?;
        if !file.metadata().map_err(|_| ERROR)?.is_file() {
            return Err(ERROR);
        }
        let mut data = Vec::new();
        file.take((FILE_LIMIT + 1) as u64)
            .read_to_end(&mut data)
            .map_err(|_| ERROR)?;
        Self::decode(&data, now)
    }
    pub fn decode(raw: &[u8], now: i64) -> Result<Self, &'static str> {
        let value = structured::parse(raw, FILE_LIMIT).ok_or(ERROR)?;
        let artifact: Artifact = serde_json::from_value(value).map_err(|_| ERROR)?;
        if artifact.artifact_version != 2
            || artifact.schema_version != 1
            || artifact.rules.is_empty()
            || artifact.rules.len() > 64
        {
            return Err(ERROR);
        }
        let mut ids = BTreeSet::new();
        for r in &artifact.rules {
            if !token(&r.id)
                || r.version == 0
                || !ids.insert(&r.id)
                || r.expires_at <= now
                || r.review.reviewed_at <= 0
                || r.review.reviewed_at > now
                || r.expires_at
                    .checked_sub(r.review.reviewed_at)
                    .is_none_or(|d| d > 366 * 86400)
                || [&r.review.reviewer, &r.review.review_id, &r.review.compiler]
                    .into_iter()
                    .any(|s| !token(s))
                || r.source_scope.is_empty()
                || prefix_identity(&r.prefix_identity.text).as_ref() != Some(&r.prefix_identity)
                || r.source_scope.len() > 16
                || r.source_scope
                    .iter()
                    .any(|(k, v)| !token(k) || !safe_scalar(v))
                || r.shape.is_empty()
                || r.shape.len() > 128
                || r.shape.get("") != Some(&Kind::Object)
                || r.required_literals.is_empty()
                || r.required_literals.len() > 32
                || r.protected_literals.len() > 32
                || r.normalize_paths.is_empty()
                || r.normalize_paths.len() > 32
            {
                return Err(ERROR);
            }
            // Validate complete tree grammar. Arrays have explicit contiguous indices;
            // empty containers are represented, and every node has a typed parent.
            for (path, kind) in &r.shape {
                if !structured::pointer(path) || path.split('/').count() > 9 {
                    return Err(ERROR);
                }
                if path.is_empty() {
                    continue;
                }
                let (parent, name) = path.rsplit_once('/').ok_or(ERROR)?;
                match r.shape.get(parent) {
                    Some(Kind::Object) => (),
                    Some(Kind::Array) => {
                        let i = name.parse::<usize>().map_err(|_| ERROR)?;
                        if i.to_string() != name
                            || i >= 128
                            || (0..i).any(|n| !r.shape.contains_key(&format!("{parent}/{n}")))
                        {
                            return Err(ERROR);
                        }
                    }
                    _ => return Err(ERROR),
                }
                if !matches!(kind, Kind::Array | Kind::Object)
                    && r.shape
                        .keys()
                        .any(|p| p != path && p.starts_with(&format!("{path}/")))
                {
                    return Err(ERROR);
                }
            }
            let mut paths = Vec::new();
            for (path, value) in r.required_literals.iter().chain(&r.protected_literals) {
                if !safe_scalar(value) || r.shape.get(path) != Some(&structured::kind(value)) {
                    return Err(ERROR);
                }
                paths.push(path.as_str());
            }
            for path in &r.normalize_paths {
                if crate::semantic::protected(path)
                    || !matches!(
                        r.shape.get(path),
                        Some(Kind::String | Kind::Number | Kind::Boolean | Kind::Null)
                    )
                {
                    return Err(ERROR);
                }
                paths.push(path);
            }
            for (i, a) in paths.iter().enumerate() {
                if !structured::pointer(a) || paths[..i].iter().any(|b| overlap(a, b)) {
                    return Err(ERROR);
                }
            }
        }
        for (i, a) in artifact.rules.iter().enumerate() {
            for b in &artifact.rules[..i] {
                // Any possible dual match is rejected, regardless of claimed IDs/versions.
                if scopes_compatible(&a.source_scope, &b.source_scope)
                    && a.prefix_identity == b.prefix_identity
                    && a.shape == b.shape
                    && !a
                        .required_literals
                        .iter()
                        .chain(&a.protected_literals)
                        .any(|(p, v)| {
                            b.required_literals
                                .get(p)
                                .or_else(|| b.protected_literals.get(p))
                                .is_some_and(|w| v != w)
                        })
                {
                    return Err(ERROR);
                }
            }
        }
        // Digest the actual bytes, including review/version changes; no claimed digest accepted.
        use sha2::{Digest, Sha256};
        Ok(Self {
            artifact,
            digest: format!("{:x}", Sha256::digest(raw)),
        })
    }
    pub fn digest(&self) -> &str {
        &self.digest
    }
}
#[derive(Serialize)]
pub struct ActiveReport {
    pub mode: &'static str,
    pub artifact_digest: String,
    pub artifact_version: u32,
    pub schema_version: u32,
    rules: Vec<Rule>,
    pub risk_contract: Contract,
    pub capacity: usize,
    pub ttl_seconds: u64,
}
struct Entry {
    rule: usize,
    generation: u64,
    created: Instant,
    verdict: Option<(Judgment, String)>,
    blocked: bool,
}
pub struct Ticket {
    owner: u64,
    key: String,
    generation: u64,
    rule: usize,
}
pub struct Matcher {
    owner: u64,
    rules: Rules,
    contract: Contract,
    entries: BTreeMap<String, Entry>,
    generation: u64,
    capacity: usize,
    ttl: Duration,
    metrics: SharedMetrics,
}
impl Matcher {
    pub fn new(
        rules: Rules,
        contract: Contract,
        capacity: usize,
        ttl: Duration,
        metrics: SharedMetrics,
    ) -> Result<Self, &'static str> {
        if !(1..=256).contains(&capacity) || ttl.is_zero() || ttl > Duration::from_secs(3600) {
            return Err(ERROR);
        }
        Ok(Self {
            owner: NEXT_OWNER
                .fetch_update(Ordering::Relaxed, Ordering::Relaxed, |n| n.checked_add(1))
                .map_err(|_| ERROR)?,
            rules,
            contract,
            entries: BTreeMap::new(),
            generation: 0,
            capacity,
            ttl,
            metrics,
        })
    }
    fn key(&self, event: &Event, wall: i64) -> Option<(String, usize)> {
        if event.truncated
            || event.sensitive
            || event.line_count != 1
            || event.text.contains(['\n', '\r'])
            || event.text.to_ascii_lowercase().contains("[redacted")
            || event.baseline.important
            // Parser-extracted timestamps are outside Event.text and therefore
            // cannot satisfy an explicit reviewed envelope identity.
            || event.timestamp.is_some()
        {
            return None;
        }
        let (prefix, mut value) = payload(&event.text)?;
        // Direct callers receive the same sensitive-data gate as Parser callers.
        let canonical = value.to_string();
        if crate::events::redact(&event.text) != event.text
            || crate::events::redact(&canonical) != canonical
        {
            return None;
        }
        let shape = structured::shape(&value)?;
        if shape.iter().any(|(p, k)| {
            !matches!(k, Kind::Object | Kind::Array)
                && value.pointer(p).is_none_or(|v| !safe_scalar(v))
        }) {
            return None;
        }
        let mut matching = self
            .rules
            .artifact
            .rules
            .iter()
            .enumerate()
            .filter(|(_, r)| {
                r.expires_at > wall
                    && r.review.reviewed_at <= wall
                    && r.source_scope
                        .iter()
                        .all(|(k, v)| event.source.get(k) == Some(v))
                    && r.prefix_identity == prefix
                    && r.shape == shape
                    && r.required_literals
                        .iter()
                        .chain(&r.protected_literals)
                        .all(|(p, v)| value.pointer(p) == Some(v))
            });
        let (index, rule) = matching.next()?;
        if matching.next().is_some() {
            return None;
        }
        for p in &rule.normalize_paths {
            *value.pointer_mut(p)? = Value::Null;
        }
        let mut normalized = event.clone();
        normalized.text = value.to_string();
        normalized.source = rule.source_scope.clone();
        Some((
            digest(&(
                &self.rules.digest,
                self.rules.artifact.artifact_version,
                self.rules.artifact.schema_version,
                &rule.id,
                rule.version,
                &rule.prefix_identity,
                &rule.source_scope,
                &shape,
                self.contract.key(&normalized),
            )),
            index,
        ))
    }
    fn expire(&mut self, now: Instant, wall: i64) {
        let before = self.entries.len();
        self.entries.retain(|_, e| {
            now.saturating_duration_since(e.created) < self.ttl
                && self.rules.artifact.rules[e.rule].expires_at > wall
                && self.rules.artifact.rules[e.rule].review.reviewed_at <= wall
        });
        let mut m = self.metrics.lock().expect("metrics");
        m.reviewed_expirations += (before - self.entries.len()) as u64;
        m.semantic_active_templates = self
            .rules
            .artifact
            .rules
            .iter()
            .filter(|r| r.expires_at > wall && r.review.reviewed_at <= wall)
            .count() as u64;
        m.reviewed_entries = self.entries.len();
    }
    pub fn prepare(
        &mut self,
        event: &mut Event,
        now: Instant,
        wall: i64,
        rescore: bool,
    ) -> Option<Ticket> {
        self.expire(now, wall);
        let Some((key, rule)) = self.key(event, wall) else {
            self.metrics.lock().expect("metrics").reviewed_fallbacks += 1;
            return None;
        };
        if let Some(e) = self.entries.get(&key)
            && !rescore
            && let Some((j, id)) = &e.verdict
        {
            event.judgment = j.clone();
            event.analysis_reused = true;
            event.analysis_representative_id = Some(id.clone());
            self.metrics
                .lock()
                .expect("metrics")
                .semantic_classifications_avoided += 1;
            return None;
        }
        if self.entries.get(&key).is_some_and(|e| e.blocked) {
            self.entries.remove(&key);
        }
        if !self.entries.contains_key(&key) {
            if self.entries.len() >= self.capacity {
                self.metrics
                    .lock()
                    .expect("metrics")
                    .reviewed_capacity_misses += 1;
                return None;
            }
            self.generation = self.generation.checked_add(1)?;
            self.entries.insert(
                key.clone(),
                Entry {
                    rule,
                    generation: self.generation,
                    created: now,
                    verdict: None,
                    blocked: false,
                },
            );
        }
        self.metrics.lock().expect("metrics").reviewed_misses += 1;
        Some(Ticket {
            owner: self.owner,
            generation: self.entries[&key].generation,
            key,
            rule,
        })
    }
    /// Publish only after the whole batch has been classified. An unsafe sibling
    /// invalidates every pending ticket for that fingerprint before any publication.
    pub fn complete_batch(&mut self, items: Vec<(Ticket, &Event)>, now: Instant, wall: i64) {
        self.expire(now, wall);
        for (t, e) in &items {
            if t.owner == self.owner
                && !crate::semantic::routine(e)
                && let Some(slot) = self
                    .entries
                    .get_mut(&t.key)
                    .filter(|s| s.generation == t.generation)
            {
                slot.verdict = None;
                slot.blocked = true;
            }
        }
        for (t, e) in items {
            if t.owner != self.owner {
                self.metrics.lock().expect("metrics").reviewed_stale_tickets += 1;
                continue;
            }
            if e.analysis_reused
                || !crate::semantic::routine(e)
                || self.rules.artifact.rules[t.rule].expires_at <= wall
                || self.key(e, wall).is_none_or(|(key, _)| key != t.key)
            {
                continue;
            }
            if let Some(slot) = self
                .entries
                .get_mut(&t.key)
                .filter(|s| s.generation == t.generation && !s.blocked)
            {
                if slot.verdict.is_none() {
                    slot.verdict = Some((e.judgment.clone(), e.id.clone()));
                    self.metrics.lock().expect("metrics").reviewed_publications += 1;
                }
            } else {
                self.metrics.lock().expect("metrics").reviewed_stale_tickets += 1;
            }
        }
        self.metrics.lock().expect("metrics").reviewed_entries = self.entries.len();
    }
    pub fn report(mut self, now: Instant, wall: i64) -> ActiveReport {
        self.expire(now, wall);
        ActiveReport {
            mode: "reviewed-session-only",
            artifact_digest: self.rules.digest,
            artifact_version: self.rules.artifact.artifact_version,
            schema_version: self.rules.artifact.schema_version,
            rules: self.rules.artifact.rules,
            risk_contract: self.contract,
            capacity: self.capacity,
            ttl_seconds: self.ttl.as_secs(),
        }
    }
}

#[cfg(test)]
mod tests;
