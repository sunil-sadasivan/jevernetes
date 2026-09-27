//! Deterministic synthetic classification; no credentials, sockets or cluster access.
use jevernetes::{
    drain::Drain,
    events::{Line, Parser},
    jev::{Category, Importance, Judgment, Severity, Usage},
    report::{Metrics, Report},
};
use std::{
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

fn main() {
    let metrics = Arc::new(Mutex::new(Metrics::default()));
    let mut drain = Drain::new(32, Duration::from_secs(300), metrics.clone()).unwrap();
    let mut report = Report::new(1000);
    let mut representatives = 0;
    let now = Instant::now();
    for i in 1..=1000 {
        let source = serde_json::from_value(serde_json::json!({
            "type":"kubernetes", "context":"synthetic", "namespace":"demo",
            "container":"api", "kind":"container", "pod":"api-synthetic",
            "pod_uid":"uid-synthetic", "restart_count":0
        }))
        .unwrap();
        let mut parser = Parser::new(source);
        parser.feed(Line {
            bytes: format!(
                "INFO served status=200 ip=127.0.0.1 count=1 duration_ms=1 request_id={i:016x}"
            )
            .into_bytes(),
            truncated: false,
            private: false,
        });
        let mut event = parser.flush().unwrap();
        let ticket = drain.prepare(&mut event, now, false);
        if !event.analysis_reused {
            let representative = Drain::representative(&event, ticket.as_ref());
            assert!(representative.text.len() <= 2 * jevernetes::drain::MAX_BYTES + 64);
            representatives += 1;
            // A fixture verdict, not a Jev result or a claim about classification quality.
            event.judgment = Judgment {
                importance: Importance::Routine,
                severity: Severity::Info,
                category: Category::Routine,
                importance_confidence: Some(0.95),
                severity_confidence: Some(0.95),
                category_confidence: Some(0.95),
                analysis_error: None,
            };
            if let Some(ticket) = ticket {
                drain.complete(ticket, &event, now);
            }
        }
        report.record(event);
    }
    assert_eq!(
        (
            report.total,
            representatives,
            report.reused,
            report.events.len()
        ),
        (1000, 2, 998, 1000)
    );
    println!(
        "{}",
        serde_json::json!({
            "mode":"synthetic-offline-drain", "raw_events":report.total,
            "representatives":representatives, "classification_responses":representatives,
            "observation_schedule":"sequential, each response completed before next observation",
            "reuses":report.reused, "retained_events":report.events.len(), "network_calls":0,
            "metrics":report.value(&metrics, &Usage::new(0.0,0.0), serde_json::json!({}), true, false, 0.0)["metrics"]
        })
    );
}
