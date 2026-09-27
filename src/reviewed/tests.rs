use super::*;
use crate::{
    drain::Strategy,
    events::{Line, Parser},
    jev::{Category, Importance, ProviderClient, Severity},
    report::Metrics,
    runtime::{self, AnalyzeOptions, Templates},
};
use serde_json::json;
use std::sync::{Arc, Mutex};
use tokio_util::sync::CancellationToken;
fn artifact(wall: i64) -> Value {
    json!({"artifact_version":1,"schema_version":1,"rules":[{
        "id":"synthetic-session","version":1,"expires_at":wall+300,
        "source_scope":{"type":"file","path":"-"},
        "shape":{"":"object","/operation":"string","/request_id":"string","/payload_id":"string","/status":"number","/outcome":"string"},
        "required_literals":{"/operation":"getPhoneCalloutSessionDetails"},
        "normalize_paths":["/request_id","/payload_id"],
        "protected_literals":{"/status":200,"/outcome":"success"},
        "review":{"reviewer":"synthetic-reviewer","review_id":"synthetic-review-1","compiler":"manual-v1","reviewed_at":wall-1}
    }]})
}
fn rules(wall: i64) -> Rules {
    Rules::decode(artifact(wall).to_string().as_bytes(), wall).unwrap()
}
fn event(i: u32) -> Event {
    let source = serde_json::from_value(json!({"type":"file","path":"-"})).unwrap();
    let mut p = Parser::new(source);
    p.feed(Line{bytes:json!({"operation":"getPhoneCalloutSessionDetails","request_id":format!("{i:032x}"),"payload_id":format!("payload-{i}"),"status":200,"outcome":"success"}).to_string().into_bytes(),truncated:false,private:false});
    let mut e = p.flush().unwrap();
    e.judgment = crate::semantic::tests::event(i).judgment;
    e
}
fn matcher(wall: i64, capacity: usize, ttl: u64) -> Matcher {
    Matcher::new(
        rules(wall),
        Contract::jev("synthetic".into()),
        capacity,
        Duration::from_secs(ttl),
        Arc::new(Mutex::new(Metrics::default())),
    )
    .unwrap()
}
fn decode(v: Value) -> bool {
    Rules::decode(v.to_string().as_bytes(), 1000).is_ok()
}
#[test]
fn strict_artifact_contract_bounds_and_ambiguity() {
    let v = artifact(1000);
    assert!(decode(v.clone()));
    for (path, value) in [
        ("/artifact_version", json!(2)),
        ("/schema_version", json!(0)),
        ("/extra", json!(1)),
        ("/rules", json!([])),
        ("/rules", json!(vec![v["rules"][0].clone(); 65])),
        ("/rules/0/version", json!(0)),
        ("/rules/0/expires_at", json!(1000)),
        ("/rules/0/id", json!("x".repeat(65))),
        ("/rules/0/review/reviewer", json!("x".repeat(65))),
        ("/rules/0/review/reviewed_at", json!(1001)),
        ("/rules/0/review/extra", json!(true)),
        ("/rules/0/source_scope", json!({})),
        ("/rules/0/source_scope", json!({"type":[]})),
        ("/rules/0/source_scope", json!({"type":"x".repeat(257)})),
        ("/rules/0/required_literals", json!({})),
        (
            "/rules/0/required_literals",
            json!({"/operation":"x".repeat(257)}),
        ),
        ("/rules/0/normalize_paths", json!(["/status"])),
        ("/rules/0/normalize_paths", json!(["/operation"])),
        (
            "/rules/0/normalize_paths",
            json!(["/request_id", "/request_id"]),
        ),
        ("/rules/0/normalize_paths", json!(["/missing"])),
        ("/rules/0/normalize_paths", json!(vec!["/request_id"; 33])),
        ("/rules/0/normalize_paths", json!(["/bad~2pointer"])),
        (
            "/rules/0/protected_literals",
            json!({"/operation":"getPhoneCalloutSessionDetails"}),
        ),
        (
            "/rules/0/protected_literals",
            json!({"/request_id":"anything"}),
        ),
        (
            "/rules/0/shape",
            json!({"":"object","/child/leaf":"string"}),
        ),
        (
            "/rules/0/shape",
            json!({"":"object","/a":"array","/a/01":"string"}),
        ),
        (
            "/rules/0/shape",
            json!({"":"object","/a":"array","/a/1":"string"}),
        ),
    ] {
        let mut bad = v.clone();
        if path == "/extra" {
            bad["extra"] = value;
        } else if path.ends_with("/extra") {
            bad["rules"][0]["review"]["extra"] = value;
        } else {
            *bad.pointer_mut(path).unwrap() = value;
        }
        assert!(!decode(bad), "{path}");
    }
    for key in ["artifact_version", "schema_version", "rules"] {
        let mut bad = v.clone();
        bad.as_object_mut().unwrap().remove(key);
        assert!(!decode(bad));
    }
    for key in [
        "id",
        "version",
        "expires_at",
        "source_scope",
        "shape",
        "required_literals",
        "normalize_paths",
        "protected_literals",
        "review",
    ] {
        let mut bad = v.clone();
        bad["rules"][0].as_object_mut().unwrap().remove(key);
        assert!(!decode(bad));
    }
    for same_id in [true, false] {
        let mut bad = v.clone();
        let mut b = bad["rules"][0].clone();
        if !same_id {
            b["id"] = json!("different-id");
        }
        bad["rules"].as_array_mut().unwrap().push(b);
        assert!(!decode(bad));
    }
    let mut disjoint = v.clone();
    let mut b = disjoint["rules"][0].clone();
    b["id"] = json!("different-id");
    b["required_literals"]["/operation"] = json!("another-operation");
    disjoint["rules"].as_array_mut().unwrap().push(b);
    assert!(decode(disjoint));
    for bytes in [
        b"invalid".to_vec(),
        vec![b' '; 65537],
        v.to_string()
            .replace(
                "\"artifact_version\":1",
                "\"artifact_version\":1,\"artifact_version\":1",
            )
            .into_bytes(),
    ] {
        assert!(Rules::decode(&bytes, 1000).is_err());
    }
    assert!(
        Rules::decode(
            serde_json::to_vec(&crate::semantic::tests::proposal())
                .unwrap()
                .as_slice(),
            1000
        )
        .is_err()
    );
    let mut claimed = v.clone();
    claimed["digest"] = json!("claimed");
    assert!(!decode(claimed));
}
#[test]
fn protected_names_at_any_depth_and_scalar_normalization_only() {
    for field in [
        "outcome",
        "status",
        "code",
        "error",
        "auth",
        "authorization",
        "security",
        "fraud",
        "category",
        "importance",
        "severity",
        "level",
        "result",
        "success",
        "denied",
        "permission",
        "role",
        "AuthResult",
        "status_code",
        "s-t-a-t-u-s",
    ] {
        let mut v = artifact(1000);
        let r = &mut v["rules"][0];
        r["shape"]["/nested"] = json!("object");
        let path = format!("/nested/{field}");
        r["shape"][&path] = json!("string");
        r["normalize_paths"] = json!([path]);
        assert!(!decode(v), "{field}");
    }
    for kind in ["object", "array"] {
        let mut v = artifact(1000);
        v["rules"][0]["shape"]["/request_id"] = json!(kind);
        assert!(!decode(v));
    }
}
#[test]
fn bounded_regular_file_loading_and_computed_digest() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("rules.json");
    let raw = artifact(1000).to_string();
    std::fs::write(&path, &raw).unwrap();
    let a = Rules::load(&path, 1000).unwrap();
    std::fs::write(&path, format!("{raw}\n")).unwrap();
    let b = Rules::load(&path, 1000).unwrap();
    assert_ne!(a.digest(), b.digest());
    std::fs::write(&path, vec![b' '; 65537]).unwrap();
    assert!(Rules::load(&path, 1000).is_err());
    assert!(Rules::load(dir.path(), 1000).is_err());
    assert!(Rules::load(&dir.path().join("missing"), 1000).is_err());
    #[cfg(unix)]
    {
        std::fs::write(&path, raw).unwrap();
        let link = dir.path().join("projected");
        std::os::unix::fs::symlink(&path, &link).unwrap();
        assert!(Rules::load(&link, 1000).is_ok());
    }
}
#[test]
fn publication_ttl_capacity_expiry_rescore_restart_stale_and_digest_identity() {
    let now = Instant::now();
    let mut m = matcher(1000, 1, 10);
    let mut a = event(1);
    let ticket = m.prepare(&mut a, now, 1000, false).unwrap();
    let mut b = event(2);
    let sibling = m.prepare(&mut b, now, 1000, false).unwrap();
    assert!(!b.analysis_reused);
    m.complete_batch(vec![(ticket, &a), (sibling, &b)], now, 1000);
    let mut c = event(3);
    assert!(m.prepare(&mut c, now, 1000, false).is_none());
    assert!(c.analysis_reused);
    assert_eq!(c.analysis_representative_id, Some(a.id.clone()));
    assert!(m.prepare(&mut event(4), now, 1000, true).is_some());
    assert!(
        matcher(1000, 1, 10)
            .prepare(&mut event(4), now, 1000, false)
            .is_some()
    );
    let stale = m.prepare(&mut event(5), now, 1000, true).unwrap();
    let later = now + Duration::from_secs(10);
    let fresh = m.prepare(&mut event(6), later, 1010, false).unwrap();
    m.complete_batch(vec![(stale, &event(5))], later, 1010);
    assert!(m.entries.values().all(|e| e.verdict.is_none()));
    m.complete_batch(vec![(fresh, &event(6))], later, 1010);
    assert!(m.prepare(&mut event(7), later, 1300, false).is_none());
    assert!(!event(7).analysis_reused);
    let mut v = artifact(1000);
    v["rules"][0]["protected_literals"] = json!({});
    let rules = Rules::decode(v.to_string().as_bytes(), 1000).unwrap();
    let mut m = Matcher::new(
        rules.clone(),
        Contract::jev("synthetic".into()),
        1,
        Duration::from_secs(10),
        m.metrics.clone(),
    )
    .unwrap();
    let mut a = event(1);
    let t = m.prepare(&mut a, now, 1000, false).unwrap();
    m.complete_batch(vec![(t, &a)], now, 1000);
    let mut changed = event(2);
    changed.text = changed.text.replace("200", "403");
    assert!(m.prepare(&mut changed, now, 1000, false).is_none());
    assert!(!changed.analysis_reused);
    assert_eq!(m.metrics.lock().unwrap().reviewed_capacity_misses, 1);
    let key = m.key(&a, 1000).unwrap().0;
    v["rules"][0]["version"] = json!(2);
    let changed = Rules::decode(v.to_string().as_bytes(), 1000).unwrap();
    let mut n = Matcher::new(
        changed,
        Contract::jev("synthetic".into()),
        1,
        Duration::from_secs(10),
        m.metrics.clone(),
    )
    .unwrap();
    assert_ne!(key, n.key(&a, 1000).unwrap().0);
    for provider in [
        crate::provider::ProviderKind::Typesafe,
        crate::provider::ProviderKind::Openai,
        crate::provider::ProviderKind::Anthropic,
    ] {
        n.contract = Contract::provider(provider, "different-model".into());
        assert_ne!(key, n.key(&a, 1000).unwrap().0);
    }
}
#[test]
fn failed_uncertain_noncacheable_and_same_batch_unsafe_never_seed() {
    for variant in [
        "failed",
        "uncertain",
        "confidence",
        "security",
        "fraud",
        "important",
    ] {
        let now = Instant::now();
        let mut m = matcher(1000, 4, 300);
        let mut a = event(1);
        let t = m.prepare(&mut a, now, 1000, false).unwrap();
        let mut b = event(2);
        let u = m.prepare(&mut b, now, 1000, false).unwrap();
        match variant {
            "failed" => a.judgment = Judgment::failed("synthetic"),
            "uncertain" => a.judgment.importance = Importance::Uncertain,
            "confidence" => a.judgment.category_confidence = None,
            "security" => a.judgment.category = Category::Security,
            "fraud" => a.judgment.category = Category::Fraud,
            _ => a.judgment.importance = Importance::Important,
        }
        m.complete_batch(vec![(u, &b), (t, &a)], now, 1000);
        assert!(m.entries.values().all(|e| e.verdict.is_none()), "{variant}");
        let mut retry = event(3);
        let t = m.prepare(&mut retry, now, 1000, false).unwrap();
        assert!(!retry.analysis_reused);
        m.complete_batch(vec![(t, &retry)], now, 1000);
        let mut hit = event(4);
        m.prepare(&mut hit, now, 1000, false);
        assert!(hit.analysis_reused);
    }
}
#[test]
fn literal_shape_source_and_ineligible_variants_never_collide() {
    let now = Instant::now();
    let mut m = matcher(1000, 64, 300);
    let mut a = event(1);
    let t = m.prepare(&mut a, now, 1000, false).unwrap();
    m.complete_batch(vec![(t, &a)], now, 1000);
    for mutation in [
        "status",
        "outcome",
        "error",
        "authorization",
        "security",
        "fraud",
        "unknown",
        "type",
        "source",
        "truncated",
        "private",
        "redacted",
        "multiline",
        "unparseable",
        "duplicate",
    ] {
        let mut e = event(2);
        let mut v: Value = serde_json::from_str(&e.text).unwrap();
        match mutation {
            "status" => v["status"] = json!(403),
            "outcome" => v["outcome"] = json!("denied"),
            "type" => v["request_id"] = json!(42),
            "source" => {
                e.source.insert("pod".into(), json!("other"));
            }
            "truncated" => e.truncated = true,
            "private" => e.sensitive = true,
            "redacted" => v["payload_id"] = json!("[REDACTED]"),
            "multiline" => e.line_count = 2,
            "unparseable" | "duplicate" => (),
            _ => v[mutation] = json!("changed"),
        }
        e.text = v.to_string();
        if mutation == "unparseable" {
            e.text.pop();
        }
        if mutation == "duplicate" {
            e.text = e
                .text
                .replace("\"status\":200", "\"status\":200,\"status\":403");
        }
        assert!(m.prepare(&mut e, now, 1000, false).is_none(), "{mutation}");
        assert!(!e.analysis_reused, "{mutation}");
    }
    // Protected fields stay literal even when accidentally omitted from the list.
    let mut v = artifact(1000);
    v["rules"][0]["protected_literals"] = json!({});
    let r = Rules::decode(v.to_string().as_bytes(), 1000).unwrap();
    let n = Matcher::new(
        r,
        Contract::jev("synthetic".into()),
        4,
        Duration::from_secs(300),
        m.metrics.clone(),
    )
    .unwrap();
    let mut b = event(2);
    b.text = b.text.replace("200", "403");
    assert_ne!(n.key(&a, 1000).unwrap().0, n.key(&b, 1000).unwrap().0);
}

#[tokio::test]
async fn files_snapshot_and_controller_keep_all_evidence_and_notify_variants() {
    use crate::controller::{Controller, policy::Policy, store::Store};
    for mode in ["files", "snapshot", "controller"] {
        for batch_size in [1, 8] {
            let dir = tempfile::tempdir().unwrap();
            let wall = crate::controller::now();
            let metrics = Arc::new(Mutex::new(Metrics::default()));
            let controller = (mode == "controller").then(|| {
                Controller::new(
                    Store::open(&dir.path().join("state.db")).unwrap(),
                    Contract::jev("synthetic-model".into()),
                    Policy::default(),
                    300,
                    false,
                    metrics.clone(),
                )
                .unwrap()
            });
            let mut artifact = artifact(wall);
            let source: Source = if mode == "files" {
                event(1).source
            } else {
                serde_json::from_value(json!({"type":"kubernetes","namespace":"synthetic","pod":"synthetic-pod","pod_uid":"synthetic-uid","container":"synthetic-container","kind":"current"})).unwrap()
            };
            artifact["rules"][0]["source_scope"] = serde_json::to_value(&source).unwrap();
            let rules = Rules::decode(artifact.to_string().as_bytes(), wall).unwrap();
            let (tx, rx) = tokio::sync::mpsc::channel(512);
            let mut texts = Vec::new();
            let mut judgments = Vec::new();
            for i in 1..=309 {
                let mut e = event(i);
                e.source = source.clone();
                if i > 300 {
                    let mut v: Value = serde_json::from_str(&e.text).unwrap();
                    match i {
                        301 => v["status"] = json!(403),
                        302 => v["outcome"] = json!("denied"),
                        303 => v["error"] = json!("synthetic-error"),
                        304 => v["authorization"] = json!("denied"),
                        305 => v["security"] = json!("changed"),
                        306 => v["fraud"] = json!(true),
                        307 => v["unknown"] = json!(true),
                        308 => v["request_id"] = json!(42),
                        _ => {
                            e.source.insert("namespace".into(), json!("other"));
                        }
                    }
                    e.text = v.to_string();
                    e.judgment.importance = Importance::Important;
                    e.judgment.category = if i % 2 == 0 {
                        Category::Security
                    } else {
                        Category::Fraud
                    };
                    e.judgment.severity = Severity::Impact;
                }
                if i <= batch_size as u32 || i > 300 {
                    judgments.push(Ok(e.judgment.clone()));
                }
                texts.push(e.text.clone());
                tx.send(e).await.unwrap();
            }
            drop(tx);
            let mut client = ProviderClient::test_client("unused-offline".into());
            client.synthetic = Some(judgments.into());
            let (report, client) = runtime::analyze_with_templates(
                rx,
                metrics.clone(),
                Some(client),
                AnalyzeOptions {
                    batch_size,
                    max_batches: 64,
                    max_cost: 1.0,
                    grouping: Strategy::Semantic,
                    drain_capacity: 4,
                    retain: 400,
                    max_events: 400,
                    live: mode == "controller",
                    print_events: false,
                },
                CancellationToken::new(),
                controller,
                Templates {
                    learner: None,
                    reviewed: Some((rules, 64, Duration::from_secs(300))),
                },
            )
            .await;
            assert!(
                client.unwrap().synthetic.unwrap().is_empty(),
                "{mode}/{batch_size}"
            );
            assert_eq!(report.total, 309);
            assert_eq!(report.events.len(), 309);
            assert_eq!(report.reused, 300 - batch_size as u64);
            for (i, (e, text)) in report.events.iter().zip(texts).enumerate() {
                assert_eq!(e.text, text);
                assert_eq!(e.analysis_reused, (batch_size..300).contains(&i));
                if e.analysis_reused {
                    assert!(e.analysis_representative_id.is_some());
                }
            }
            assert!(report.learning.is_none());
            assert_eq!(report.reviewed.unwrap().mode, "reviewed-session-only");
            let m = metrics.lock().unwrap();
            assert_eq!(m.reviewed_publications, 1);
            assert_eq!(m.semantic_classifications_avoided, 300 - batch_size as u64);
            assert_eq!(m.template_provider_attempts, 0);
            if mode == "controller" {
                assert_eq!(m.policy_notify, 9);
                assert_eq!(m.notifications_enqueued, 9);
                assert_eq!(m.verdict_hits, 0);
            }
        }
    }
}

#[tokio::test]
async fn snapshot_ingest_reaches_the_same_matcher_without_network() {
    let wall = crate::controller::now();
    let metrics = Arc::new(Mutex::new(Metrics::default()));
    let (tx, rx) = tokio::sync::mpsc::channel(512);
    let sender = runtime::Sender {
        tx,
        metrics: metrics.clone(),
    };
    let stop = CancellationToken::new();
    let raw = (1..=300)
        .map(|i| format!("{}\n", event(i).text))
        .collect::<String>();
    runtime::ingest(raw.as_bytes(), event(1).source, 1024 * 1024, &sender, &stop).await;
    drop(sender);
    let mut client = ProviderClient::test_client("unused-offline".into());
    client.synthetic = Some(vec![Ok(event(1).judgment)].into());
    let (report, client) = runtime::analyze_with_templates(
        rx,
        metrics.clone(),
        Some(client),
        AnalyzeOptions {
            batch_size: 1,
            max_batches: 2,
            max_cost: 1.0,
            grouping: Strategy::Semantic,
            drain_capacity: 4,
            retain: 300,
            max_events: 300,
            live: false,
            print_events: false,
        },
        stop,
        None,
        Templates {
            learner: None,
            reviewed: Some((rules(wall), 4, Duration::from_secs(300))),
        },
    )
    .await;
    assert_eq!(report.total, 300);
    assert_eq!(report.reused, 299);
    assert_eq!(report.batches, 1);
    assert_eq!(report.events.len(), 300);
    assert!(client.unwrap().synthetic.unwrap().is_empty());
}

#[test]
fn nested_arrays_empty_containers_and_all_literal_values_are_preserved() {
    let wall = 1000;
    let now = Instant::now();
    let mut a = event(1);
    let mut v: Value = serde_json::from_str(&a.text).unwrap();
    v["items"] = json!([{"request_id":"one","value":true},null,[],{}]);
    a.text = v.to_string();
    let mut artifact = artifact(wall);
    let r = &mut artifact["rules"][0];
    r["shape"] = serde_json::to_value(structured::shape(&v).unwrap()).unwrap();
    r["normalize_paths"] = json!(["/request_id", "/payload_id", "/items/0/request_id"]);
    let rules = Rules::decode(artifact.to_string().as_bytes(), wall).unwrap();
    let m = Matcher::new(
        rules,
        Contract::jev("synthetic".into()),
        4,
        Duration::from_secs(300),
        Arc::new(Mutex::new(Metrics::default())),
    )
    .unwrap();
    let key = m.key(&a, wall).unwrap().0;
    v["items"][0]["request_id"] = json!("two");
    a.text = v.to_string();
    assert_eq!(key, m.key(&a, wall).unwrap().0);
    v["items"][0]["value"] = json!(false);
    a.text = v.to_string();
    assert_ne!(key, m.key(&a, wall).unwrap().0);
    v["items"][2] = json!([1]);
    a.text = v.to_string();
    assert!(m.key(&a, wall).is_none());
    let mut restarted = matcher(wall, 4, 300);
    let mut original = matcher(wall, 4, 300);
    let ticket = original.prepare(&mut event(1), now, wall, false).unwrap();
    restarted.prepare(&mut event(1), now, wall, false);
    restarted.complete_batch(vec![(ticket, &event(1))], now, wall);
    assert!(restarted.entries.values().all(|e| e.verdict.is_none()));
}

#[test]
fn additional_string_path_scope_count_and_expiry_bounds() {
    for (field, map) in [
        (
            "shape",
            (0..129)
                .map(|i| (format!("/p{i}"), json!("string")))
                .collect::<serde_json::Map<_, _>>(),
        ),
        (
            "source_scope",
            (0..17).map(|i| (format!("p{i}"), json!("scope"))).collect(),
        ),
        (
            "required_literals",
            (0..33)
                .map(|i| (format!("/p{i}"), json!("literal")))
                .collect(),
        ),
    ] {
        let mut v = artifact(1000);
        v["rules"][0][field] = Value::Object(map);
        assert!(!decode(v), "{field}");
    }
    for path in [
        format!("/{}", "x".repeat(128)),
        "/bad~1pointer".into(),
        "//empty".into(),
        "relative".into(),
    ] {
        let mut v = artifact(1000);
        v["rules"][0]["shape"][&path] = json!("string");
        assert!(!decode(v));
    }
    let mut v = artifact(1000);
    v["rules"][0]["expires_at"] = json!(1000 + 367 * 86400);
    assert!(!decode(v));
    let now = Instant::now();
    let mut m = matcher(1000, 1, 300);
    let ticket = m.prepare(&mut event(1), now, 1000, false).unwrap();
    m.complete_batch(vec![(ticket, &event(1))], now, 1300);
    assert!(m.entries.is_empty());
    let mut e = event(2);
    assert!(m.prepare(&mut e, now, 1300, false).is_none());
    assert!(!e.analysis_reused);
    for text in [
        "x".repeat(257),
        "embedded\nline".into(),
        "[REDACTED PRIVATE KEY]".into(),
        "[redacted]".into(),
    ] {
        let mut e = event(1);
        let mut v: Value = serde_json::from_str(&e.text).unwrap();
        v["payload_id"] = json!(text);
        e.text = v.to_string();
        assert!(m.key(&e, 1000).is_none());
    }
}

#[tokio::test]
async fn controller_rescore_retry_and_recurrence_are_preserved() {
    use crate::controller::{Controller, policy::Policy, store::Store};
    for rescore in [false, true] {
        let dir = tempfile::tempdir().unwrap();
        let metrics = Arc::new(Mutex::new(Metrics::default()));
        let wall = crate::controller::now();
        let controller = Controller::new(
            Store::open(&dir.path().join("state.db")).unwrap(),
            Contract::jev("synthetic-model".into()),
            Policy::default(),
            7,
            rescore,
            metrics.clone(),
        )
        .unwrap();
        let (tx, rx) = tokio::sync::mpsc::channel(8);
        // Identical evidence still records each raw occurrence and policy transaction.
        for _ in 0..4 {
            tx.send(event(1)).await.unwrap();
        }
        let mut security = event(1);
        security.text = security.text.replace("success", "denied");
        security.judgment.importance = Importance::Important;
        security.judgment.category = Category::Security;
        security.judgment.severity = Severity::Impact;
        for _ in 0..3 {
            tx.send(security.clone()).await.unwrap();
        }
        drop(tx);
        let mut client = ProviderClient::test_client("unused-offline".into());
        let mut judgments = vec![Err("synthetic failure".into()), Ok(event(1).judgment)];
        if rescore {
            judgments.extend([Ok(event(1).judgment), Ok(event(1).judgment)]);
        }
        judgments.extend(vec![Ok(security.judgment); 3]);
        client.synthetic = Some(judgments.into());
        let (report, client) = runtime::analyze_with_templates(
            rx,
            metrics.clone(),
            Some(client),
            AnalyzeOptions {
                batch_size: 1,
                max_batches: 8,
                max_cost: 1.0,
                grouping: Strategy::Semantic,
                drain_capacity: 4,
                retain: 8,
                max_events: 8,
                live: true,
                print_events: false,
            },
            CancellationToken::new(),
            Some(controller.clone()),
            Templates {
                learner: None,
                reviewed: Some((rules(wall), 4, Duration::from_secs(300))),
            },
        )
        .await;
        assert_eq!(report.total, 7);
        assert_eq!(report.events.len(), 7);
        assert_eq!(report.reused, if rescore { 0 } else { 2 });
        assert_eq!(report.reviewed.unwrap().ttl_seconds, 7);
        assert!(client.unwrap().synthetic.unwrap().is_empty());
        let m = metrics.lock().unwrap();
        assert_eq!(m.policy_ignore, 3);
        assert_eq!(m.policy_review, 1);
        assert_eq!(m.policy_notify, 3);
        drop(m);
        let store = controller.store.lock().unwrap();
        let mut statement = store
            .conn
            .prepare("SELECT count FROM incidents ORDER BY count")
            .unwrap();
        let counts = statement
            .query_map([], |r| r.get::<_, i64>(0))
            .unwrap()
            .collect::<Result<Vec<_>, _>>()
            .unwrap();
        // Routine/Ignore does not increase incident recurrence; all three
        // independently classified security occurrences do, as in the existing policy.
        assert_eq!(counts, vec![1, 3]);
        let verdicts: i64 = store
            .conn
            .query_row("SELECT count(*) FROM verdicts", [], |r| r.get(0))
            .unwrap();
        assert_eq!(verdicts, 0);
    }
}
