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
    let source: Source = serde_json::from_value(json!({"type":"file","path":"-"})).unwrap();
    let mut report = Report::new(300);
    let now = Instant::now();
    let mut classifications = 0;
    for i in 1..=300 {
        let mut parser = Parser::new(source.clone());
        parser.feed(Line{bytes:json!({"operation":"getPhoneCalloutSessionDetails","request_id":format!("{i:032x}"),"payload_id":format!("payload-{i}"),"status":200,"outcome":"success"}).to_string().into_bytes(),truncated:false,private:false});
        let mut e = parser.flush().unwrap();
        let ticket = matcher.prepare(&mut e, now, wall, false);
        if !e.analysis_reused {
            classifications += 1;
            e.judgment = Judgment {
                importance: Importance::Routine,
                severity: Severity::Info,
                category: Category::Routine,
                importance_confidence: Some(0.95),
                severity_confidence: Some(0.95),
                category_confidence: Some(0.95),
                analysis_error: None,
            };
            matcher.complete_batch(ticket.into_iter().map(|t| (t, &e)).collect(), now, wall);
        }
        report.record(e);
    }
    assert_eq!(classifications, 1);
    assert_eq!(report.reused, 299);
    assert_eq!(report.events.len(), 300);
    println!(
        "300 retained events; 1 synthetic classification; 299 reviewed-rule reuses; 0 network calls."
    );
}
