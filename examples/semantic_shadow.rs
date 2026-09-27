//! Synthetic, socket-free candidate replay. No model confidence/accuracy claim.
use jevernetes::{
    events::{Line, Parser, Source},
    jev::{Category, Importance, Judgment, Severity},
    report::{Metrics, Report},
    semantic::{Explanation, Registry, TemplateProposal},
};
use serde_json::json;
use std::{
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};
fn main() {
    let metrics = Arc::new(Mutex::new(Metrics::default()));
    let mut registry = Registry::new(4, 4, Duration::from_secs(300), metrics.clone()).unwrap();
    let mut report = Report::new(304);
    let now = Instant::now();
    for i in 1..=304 {
        let security = i > 300;
        let mut parser = Parser::new(Source::new());
        parser.feed(Line {bytes:json!({"operation":"getSessionDetails","request_id":format!("{i:032x}"),"status":if security{403}else{200},"outcome":if security{"denied"}else{"success"}}).to_string().into_bytes(),truncated:false,private:false});
        let mut event = parser.flush().unwrap();
        event.judgment = Judgment {
            importance: if security {
                Importance::Important
            } else {
                Importance::Routine
            },
            severity: if security {
                Severity::Impact
            } else {
                Severity::Info
            },
            category: if security {
                Category::Security
            } else {
                Category::Routine
            },
            importance_confidence: Some(0.95),
            severity_confidence: Some(0.95),
            category_confidence: Some(0.95),
            analysis_error: None,
        };
        if let Some((ticket, _)) = registry.observe(&event, now) {
            registry.complete(
                ticket,
                Ok(TemplateProposal {
                    name: "synthetic_session".into(),
                    version: 1,
                    required_paths: vec!["/operation".into()],
                    normalize_paths: vec!["/request_id".into()],
                    protected_paths: vec!["/status".into(), "/outcome".into()],
                    cacheable: true,
                    confidence: 0.95,
                    explanation: Explanation::OpaqueIdentifiers,
                }),
                now,
            );
        }
        report.record(event);
    }
    let candidates = registry.candidates(now);
    let m = metrics.lock().unwrap();
    assert_eq!(report.total, 304);
    assert_eq!(report.events.len(), 304);
    assert_eq!(m.semantic_shadow_matches, 296);
    assert_eq!(report.reused, 0);
    assert!(!candidates[0].replay_passed);
    println!(
        "304 retained events; 304 synthetic classifications; 296 shadow matches; 4 security variants; 0 avoided classifications; 0 network calls. Candidate quarantined after security evidence."
    );
}
