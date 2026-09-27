//! Deterministic synthetic judgments; no network, credentials, or model accuracy claim.
use jevernetes::{
    controller::Contract,
    events::{Line, Parser, Source},
    jev::{Category, Importance, Judgment, Severity},
    report::{Metrics, Report},
    reviewed::{Matcher, Rules},
};
use serde_json::json;
use std::{
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};
fn main() {
    replay(false);
    replay(true);
}
fn replay(envelope: bool) {
    // Fixed synthetic replay time deliberately inside the example review/expiry window.
    let wall = 1790000000;
    let rules = Rules::decode(include_bytes!("reviewed-rules.synthetic.json"), wall).unwrap();
    let metrics = Arc::new(Mutex::new(Metrics::default()));
    let mut matcher = Matcher::new(
        rules,
        Contract::jev("synthetic-model".into()),
        4,
        Duration::from_secs(300),
        metrics.clone(),
    )
    .unwrap();
    let source: Source = serde_json::from_value(if envelope {
        json!({"type":"kubernetes","context":"synthetic","namespace":"demo","container":"api","kind":"container","previous":false,"restart_count":0})
    } else { json!({"type":"file","path":"-"}) }).unwrap();
    let mut report = Report::new(300);
    let now = Instant::now();
    let mut classifications = 0;
    let total = if envelope { 200 } else { 300 };
    for i in 1..=total {
        let security = envelope && i > 198;
        let mut source = source.clone();
        let mut value = json!({"operation":"getPhoneCalloutSessionDetails","request_id":format!("{i:032x}"),"payload_id":format!("payload-{i}"),"status":200,"outcome":"success"});
        if security {
            value["finding"] =
                json!({"category":"security","denied":true,"details":[],"context":{}});
        }
        let text = if envelope {
            source.insert("pod".into(), json!(format!("replica-{}", i % 2)));
            source.insert("pod_uid".into(), json!(format!("uid-{}", i % 2)));
            let prefix = format!("[07:08:09.{:03}] INFO synthetic.session.log ", i % 159);
            assert_eq!(prefix.len(), 42);
            format!("{prefix}{value}")
        } else {
            value.to_string()
        };
        let mut parser = Parser::new(source);
        parser.feed(Line {
            bytes: text.clone().into_bytes(),
            truncated: false,
            private: false,
        });
        let mut e = parser.flush().unwrap();
        let ticket = matcher.prepare(&mut e, now, wall, false);
        if !e.analysis_reused {
            classifications += 1;
            e.judgment = Judgment {
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
                // Synthetic uncertainty between non-escalating Info/Noise.
                severity_confidence: Some(if security { 0.95 } else { 0.0 }),
                category_confidence: Some(0.95),
                analysis_error: None,
            };
            matcher.complete_batch(ticket.into_iter().map(|t| (t, &e)).collect(), now, wall);
        }
        assert_eq!(e.text, text);
        assert!(!security || !e.analysis_reused);
        report.record(e);
    }
    assert_eq!(classifications, if envelope { 3 } else { 1 });
    assert_eq!(report.reused, if envelope { 197 } else { 299 });
    assert_eq!(report.events.len(), total);
    println!(
        "{total} retained events; {classifications} synthetic classifications; {} reviewed-rule reuses; envelope={envelope}; 0 network calls.",
        report.reused
    );
}
