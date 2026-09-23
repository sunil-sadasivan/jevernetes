use crate::{
    events::{Event, Source},
    jev::{Category, Importance, Severity, Usage},
};
use serde::Serialize;
use serde_json::{Value, json};
use std::{
    collections::{BTreeMap, VecDeque},
    io::Write,
    path::Path,
    sync::{Arc, Mutex},
};
#[derive(Clone, Debug, Serialize)]
pub struct Coverage {
    pub source: Source,
    pub status: String,
    pub warnings: Vec<String>,
}
#[derive(Default, Serialize)]
pub struct Metrics {
    pub verdict_hits: u64,
    pub verdict_misses: u64,
    pub verdict_expirations: u64,
    pub novelty_duplicates: u64,
    pub store_failures: u64,
    pub state_pruned: u64,
    pub notifications_enqueued: u64,
    pub notification_attempts: u64,
    pub notification_delivered: u64,
    pub notification_failures: u64,
    pub outbox_pending: u64,
    pub outbox_dead: u64,
    pub policy_notify: u64,
    pub policy_review: u64,
    pub policy_abstain: u64,
    pub policy_ignore: u64,
    pub provider_batches: u64,
    pub provider_attempts: u64,
    pub provider_input_tokens: u64,
    pub provider_output_tokens: u64,
    pub provider_unmetered_requests: u64,
    pub estimated_cost_usd: f64,
    pub received: u64,
    pub dropped: u64,
    pub reconnects: u64,
    pub duplicates: u64,
    pub dedup_overflows: u64,
    pub untimestamped_lines: u64,
    pub streams_started: u64,
    pub unfollowed_observations: u64,
    pub queue_high_water: usize,
    pub queue_depth: usize,
    pub active_streams: usize,
    pub coverage_gaps: u64,
    pub coverage_records_omitted: u64,
    #[serde(skip)]
    pub coverage: VecDeque<Coverage>,
}
pub type SharedMetrics = Arc<Mutex<Metrics>>;
pub fn gap(metrics: &SharedMetrics, source: &Source, status: &str, warning: &str) {
    let mut m = metrics.lock().expect("metrics lock");
    if status != "ok" {
        m.coverage_gaps += 1;
    }
    if m.coverage.len() == 500 {
        m.coverage.pop_front();
        m.coverage_records_omitted += 1;
    }
    m.coverage.push_back(Coverage {
        source: source.clone(),
        status: status.into(),
        warnings: if warning.is_empty() {
            vec![]
        } else {
            vec![warning.into()]
        },
    });
}
#[derive(Serialize)]
struct ImportantGroup {
    id: String,
    source: Source,
    text: String,
    severity: Severity,
    category: Category,
    count: u64,
    event_ids: Vec<String>,
}
fn severity_rank(severity: Severity) -> u8 {
    match severity {
        Severity::Outage => 5,
        Severity::Impact => 4,
        Severity::Degraded => 3,
        Severity::Info => 2,
        Severity::Noise => 1,
        Severity::Unknown => 0,
    }
}
pub struct Report {
    pub events: VecDeque<Event>,
    pub counts: BTreeMap<String, u64>,
    pub total: u64,
    pub lines: u64,
    pub reused: u64,
    pub evicted: u64,
    pub limit: usize,
    pub batches: u64,
}
impl Report {
    pub fn new(limit: usize) -> Self {
        Self {
            events: VecDeque::new(),
            counts: BTreeMap::new(),
            total: 0,
            lines: 0,
            reused: 0,
            evicted: 0,
            limit,
            batches: 0,
        }
    }
    pub fn record(&mut self, event: Event) {
        self.total += 1;
        self.lines += event.line_count;
        self.reused += u64::from(event.analysis_reused);
        let key = serde_json::to_value(event.judgment.importance)
            .expect("enum")
            .as_str()
            .expect("enum string")
            .to_owned();
        *self.counts.entry(key).or_default() += 1;
        if self.events.len() == self.limit {
            self.events.pop_front();
            self.evicted += 1;
        }
        self.events.push_back(event);
    }
    pub fn value(
        &self,
        metrics: &SharedMetrics,
        usage: &Usage,
        scope: Value,
        offline: bool,
        live: bool,
        elapsed: f64,
    ) -> Value {
        let m = metrics.lock().expect("metrics lock");
        let mut groups: BTreeMap<String, ImportantGroup> = BTreeMap::new();
        for e in self
            .events
            .iter()
            .filter(|e| e.judgment.importance == Importance::Important)
        {
            let group = groups
                .entry(e.group_id.clone())
                .or_insert_with(|| ImportantGroup {
                    id: e.group_id.clone(),
                    source: e.source.clone(),
                    text: e.text.clone(),
                    severity: e.judgment.severity,
                    category: e.judgment.category,
                    count: 0,
                    event_ids: Vec::new(),
                });
            group.count += 1;
            group.event_ids.push(e.id.clone());
        }
        let mut groups: Vec<_> = groups.into_values().collect();
        groups.sort_by_key(|g| {
            (
                std::cmp::Reverse(severity_rank(g.severity)),
                std::cmp::Reverse(g.count),
                g.id.clone(),
            )
        });
        let complete = !live
            && m.coverage_gaps == 0
            && m.dropped == 0
            && self.evicted == 0
            && self.counts.get("unknown").copied().unwrap_or(0) == 0;
        json!({"schema_version":2,"runtime":"rust","created_at":chrono::Utc::now().to_rfc3339(),"mode":if offline{"offline-rules"}else{"jev"},"scope":scope,
            "summary":{"events":self.total,"lines":self.lines,"important":self.counts.get("important").unwrap_or(&0),"routine":self.counts.get("routine").unwrap_or(&0),"uncertain":self.counts.get("uncertain").unwrap_or(&0),"unknown":self.counts.get("unknown").unwrap_or(&0),"streams":m.streams_started,"coverage_gaps":m.coverage_gaps,"api_requests":usage.request_attempts,"reused_events":self.reused,"elapsed_seconds":elapsed,"complete_within_window":complete,"retained_events":self.events.len(),"evicted_events":self.evicted},
            "usage":usage,"coverage":m.coverage,"metrics":&*m,"batches":self.batches,"events":self.events,"important_groups":groups})
    }
}
pub fn write_report(path: &Path, value: &Value) -> Result<(), &'static str> {
    let parent = path
        .parent()
        .filter(|p| !p.as_os_str().is_empty())
        .unwrap_or(Path::new("."));
    let mut builder = std::fs::DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o700);
    }
    builder
        .create(parent)
        .map_err(|_| "Cannot create report directory")?;
    let mut file =
        tempfile::NamedTempFile::new_in(parent).map_err(|_| "Cannot create private report file")?;
    serde_json::to_writer_pretty(&mut file, value).map_err(|_| "Cannot serialize report")?;
    file.write_all(b"\n")
        .and_then(|()| file.as_file().sync_all())
        .map_err(|_| "Cannot write report")?;
    file.persist(path).map_err(|_| "Cannot replace report")?;
    Ok(())
}
