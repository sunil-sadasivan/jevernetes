//! Durable probabilistic monitoring: classification is advisory; policy owns decisions.
pub mod health;
pub mod policy;
pub mod sink;
pub mod store;
use crate::{
    events::{Event, Source},
    jev::{Category, Importance, Judgment, Severity},
    report::SharedMetrics,
};
use policy::{Decision, Policy};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::{
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, Ordering},
    },
    time::{Duration, SystemTime, UNIX_EPOCH},
};
use store::{Lookup, Result, Store};
use tokio_util::sync::CancellationToken;

pub const CONTRACT_VERSION: &str = "systemone-choice-v1-redaction-v1-truncation-review-v1";
#[derive(Clone, Serialize, Deserialize)]
pub struct Contract {
    pub provider: String,
    pub model: String,
    pub version: String,
}
impl Contract {
    pub fn jev(model: String) -> Self {
        Self {
            provider: "https://api.typesafe.ai/v1/systemone".into(),
            model,
            version: CONTRACT_VERSION.into(),
        }
    }
    pub fn key(&self, event: &Event) -> String {
        // Include actual prompt/taxonomy and full source/text, not the short session group ID.
        digest(&(
            self,
            crate::jev::build_request(std::slice::from_ref(event), &self.model),
            event.truncated,
        ))
    }
}
pub fn digest(value: &impl Serialize) -> String {
    format!(
        "{:x}",
        Sha256::digest(serde_json::to_vec(value).expect("serializable identity"))
    )
}
pub fn now() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs()
        .min(i64::MAX as u64) as i64
}
pub fn cacheable(e: &Event) -> bool {
    let j = &e.judgment;
    !e.truncated
        && j.analysis_error.is_none()
        && j.importance != Importance::Unknown
        && j.category != Category::Unknown
        && j.severity != Severity::Unknown
        && [
            j.importance_confidence,
            j.category_confidence,
            j.severity_confidence,
        ]
        .into_iter()
        .all(|c| c.is_some_and(|v| v.is_finite() && (0.0..=1.0).contains(&v)))
}
#[derive(Serialize)]
pub struct Notification {
    schema: u32,
    pub notification_id: String,
    pub incident_id: String,
    pub decision: Decision,
    pub escalation_level: u32,
    pub recurrence_count: u32,
    observed_at: i64,
    evidence_timestamp: Option<String>,
    pub evidence: String,
    pub source: Source,
    truncated: bool,
    judgment: Judgment,
    policy: Policy,
    advisory_only: bool,
}
impl Notification {
    #[allow(clippy::too_many_arguments)]
    fn new(
        id: &str,
        key: &str,
        event: &Event,
        policy: &Policy,
        decision: Decision,
        level: u32,
        count: u32,
        at: i64,
    ) -> Self {
        let mut source = Source::new();
        for name in [
            "type",
            "namespace",
            "pod",
            "pod_uid",
            "container",
            "kind",
            "restart_count",
            "previous",
        ] {
            if let Some(v) = event.source.get(name) {
                let safe = v.as_str().is_some_and(|s| {
                    s.len() <= 253
                        && s.chars()
                            .all(|c| c.is_ascii_alphanumeric() || "-_.".contains(c))
                }) || v.is_boolean()
                    || v.as_i64().is_some();
                if safe {
                    source.insert(name.into(), v.clone());
                }
            }
        }
        let mut judgment = event.judgment.clone();
        if judgment.analysis_error.is_some() {
            judgment.analysis_error = Some("Classification unavailable".into());
        }
        Self {
            schema: 1,
            notification_id: id.into(),
            incident_id: key.into(),
            decision,
            escalation_level: level,
            recurrence_count: count,
            observed_at: at,
            evidence_timestamp: event
                .timestamp
                .as_deref()
                .and_then(|s| chrono::DateTime::parse_from_rfc3339(s).ok())
                .map(|t| t.to_rfc3339()),
            evidence: event.text.clone(),
            source,
            truncated: event.truncated,
            judgment,
            policy: policy.clone(),
            advisory_only: true,
        }
    }
}
#[derive(Clone)]
pub struct Controller {
    pub(crate) store: Arc<Mutex<Store>>,
    pub contract: Contract,
    pub policy: Policy,
    pub ttl: i64,
    pub rescore: bool,
    pub fault: CancellationToken,
    pub metrics: SharedMetrics,
    pub ready: Arc<AtomicBool>,
    pub wake: Arc<tokio::sync::Notify>,
}
impl Controller {
    pub fn new(
        store: Store,
        contract: Contract,
        policy: Policy,
        ttl: i64,
        rescore: bool,
        metrics: SharedMetrics,
    ) -> Result<Self> {
        policy.validate()?;
        if !(1..=604800).contains(&ttl) {
            return Err("Verdict TTL must be 1..604800 seconds");
        }
        Ok(Self {
            fault: CancellationToken::new(),
            store: Arc::new(Mutex::new(store)),
            contract,
            policy,
            ttl,
            rescore,
            metrics,
            ready: Arc::new(AtomicBool::new(true)),
            wake: Arc::new(tokio::sync::Notify::new()),
        })
    }
    pub async fn db<T: Send + 'static>(
        &self,
        f: impl FnOnce(&mut Store) -> Result<T> + Send + 'static,
    ) -> Result<T> {
        let store = self.store.clone();
        let result = tokio::task::spawn_blocking(move || {
            f(&mut *store.lock().map_err(|_| "Controller state lock failed")?)
        })
        .await
        .unwrap_or(Err("Controller state worker failed"));
        if let Err(error) = &result {
            if self.ready.swap(false, Ordering::AcqRel) {
                eprintln!("[controller] {error}; readiness false");
            }
            self.fault.cancel();
            self.metrics.lock().expect("metrics").store_failures += 1;
        }
        result
    }
    pub async fn lookup(&self, event: &Event) -> Result<Option<Judgment>> {
        let (e, c, t, r) = (event.clone(), self.contract.clone(), self.ttl, self.rescore);
        let lookup = self.db(move |s| s.lookup(&e, &c, now(), t, r)).await?;
        let mut m = self.metrics.lock().expect("metrics");
        match lookup {
            Lookup::Hit(j) => {
                m.verdict_hits += 1;
                Ok(Some(j))
            }
            Lookup::Miss => {
                m.verdict_misses += 1;
                Ok(None)
            }
            Lookup::Expired => {
                m.verdict_expirations += 1;
                m.verdict_misses += 1;
                Ok(None)
            }
        }
    }
    pub async fn apply(&self, event: &Event) -> Result<()> {
        let (e, c, p, t) = (
            event.clone(),
            self.contract.clone(),
            self.policy.clone(),
            self.ttl,
        );
        let result = self.db(move |s| s.apply(&e, &c, &p, now(), t)).await?;
        let mut m = self.metrics.lock().expect("metrics");
        m.novelty_duplicates += u64::from(result.duplicate);
        m.notifications_enqueued += u64::from(result.enqueued);
        match result.decision {
            Some(Decision::Review) => m.policy_review += 1,
            Some(Decision::Abstain) => m.policy_abstain += 1,
            Some(Decision::Notify) => m.policy_notify += 1,
            Some(Decision::Ignore) => m.policy_ignore += 1,
            None => (),
        }
        if result.enqueued {
            self.wake.notify_one();
        }
        Ok(())
    }
    pub async fn deliver(&self, sink: Arc<dyn sink::Sink>, stop: CancellationToken) {
        let mut maintenance = 0;
        loop {
            if stop.is_cancelled() {
                break;
            }
            let at = now();
            if at >= maintenance {
                match self.db(move |s| s.prune(at)).await {
                    Ok(n) => self.metrics.lock().expect("metrics").state_pruned += n as u64,
                    Err(_) => {
                        eprintln!("[controller] state maintenance failed; readiness false");
                    }
                }
                maintenance = at + 60;
            }
            let row = self.db(move |s| s.claim(at, 8, 1)).await;
            if let Ok(Some(row)) = row {
                self.metrics.lock().expect("metrics").notification_attempts += 1;
                let success = tokio::select! { _=stop.cancelled()=>break, result=tokio::time::timeout(Duration::from_secs(6),sink.deliver(&row.id,&row.payload))=>matches!(result,Ok(Ok(()))) };
                if !success {
                    eprintln!(
                        "[controller] notification delivery failed; retry/dead-letter recorded"
                    );
                }
                {
                    let mut m = self.metrics.lock().expect("metrics");
                    m.notification_delivered += u64::from(success);
                    m.notification_failures += u64::from(!success);
                }
                // Stable per-notification jitter makes schedules deterministic and spreads retries.
                let jitter = u64::from_str_radix(row.id.get(..8).unwrap_or("0"), 16).unwrap_or(0)
                    ^ u64::from(row.attempts);
                if self
                    .db(move |s| s.finish(&row, now(), success, 8, jitter))
                    .await
                    .is_err()
                {
                    eprintln!("[controller] delivery checkpoint failed; notification may repeat");
                }
            } else if row.is_err() {
                eprintln!("[controller] notification state unavailable; readiness false");
            }
            if let Ok((pending, dead)) = self.db(|s| s.counts()).await {
                let mut m = self.metrics.lock().expect("metrics");
                m.outbox_pending = pending;
                m.outbox_dead = dead;
            }
            let due = self.db(move |s| s.next_due(at)).await.unwrap_or(at + 5);
            tokio::select! { _=stop.cancelled()=>break, _=self.wake.notified()=>(), _=tokio::time::sleep(Duration::from_secs((due-now()).max(1) as u64))=>() }
        }
    }
}
#[cfg(test)]
mod tests;
