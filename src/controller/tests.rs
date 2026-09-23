use super::*;
use crate::{
    events::{Line, Parser},
    report::Metrics,
};
use sink::Sink;
use store::{Lookup, RETENTION};
fn event(text: &str, stamp: u32) -> Event {
    let mut p=Parser::new(serde_json::from_value(serde_json::json!({"type":"kubernetes","namespace":"synthetic","pod":"demo","pod_uid":"uid","container":"app","context":"private-context","path":"private-path","restart_count":0})).unwrap());
    p.feed(Line {
        bytes: format!("2026-09-20T00:00:{stamp:02}Z {text}").into_bytes(),
        truncated: false,
        private: false,
    });
    let mut e = p.flush().unwrap();
    e.judgment = Judgment {
        importance: Importance::Important,
        severity: Severity::Impact,
        category: Category::Security,
        importance_confidence: Some(0.95),
        severity_confidence: Some(0.95),
        category_confidence: Some(0.95),
        analysis_error: None,
    };
    e
}
fn controller(dir: &tempfile::TempDir) -> Controller {
    Controller::new(
        Store::open(&dir.path().join("state.db")).unwrap(),
        Contract::jev("synthetic-model".into()),
        Policy::default(),
        300,
        false,
        Arc::new(Mutex::new(Metrics::default())),
    )
    .unwrap()
}

#[tokio::test]
async fn inspection_is_bounded_read_only_and_preserves_sanitized_evidence() {
    let dir = tempfile::tempdir().unwrap();
    let c = controller(&dir);
    for i in 0..25 {
        c.apply(&event(
            &format!("security evidence {i} password=synthetic-private"),
            i,
        ))
        .await
        .unwrap();
    }
    let (id, changes) = {
        let store = c.store.lock().unwrap();
        let changes = store.conn.total_changes();
        let (count, pending, dead, recent) = store.inspection().unwrap();
        assert_eq!((count, pending, dead), (25, 25, 0));
        assert_eq!(recent.len(), inspect::RECENT_LIMIT);
        let id = recent[0].id.clone();
        let detail = store.incident_detail(&id).unwrap().unwrap();
        assert_eq!(detail.delivery_status.as_deref(), Some("pending"));
        assert_eq!(detail.delivery_attempts, Some(0));
        let raw = serde_json::to_string(&detail).unwrap();
        assert!(!raw.contains("synthetic-private"));
        assert!(!raw.contains("private-context"));
        assert!(!raw.contains("private-path"));
        assert_eq!(
            store.conn.total_changes(),
            changes,
            "inspection must not claim or update rows"
        );
        (id, changes)
    };
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let stop = CancellationToken::new();
    let server = tokio::spawn(inspect::serve(listener, c.clone(), stop.clone()));
    let client = reqwest::Client::builder().no_proxy().build().unwrap();
    let status: inspect::Snapshot = client
        .get(format!("http://{addr}/v1/status"))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    assert_eq!(status.incident_count, 25);
    assert!(!status.coverage_complete);
    let detail: inspect::Detail = client
        .get(format!("http://{addr}/v1/incidents/{id}"))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    assert_eq!(detail.incident.id, id);
    assert!(detail.last_notification.is_some());
    assert_eq!(
        client
            .post(format!("http://{addr}/v1/status"))
            .send()
            .await
            .unwrap()
            .status(),
        400
    );
    assert_eq!(
        client
            .get(format!("http://{addr}/v1/incidents/{}", "f".repeat(64)))
            .send()
            .await
            .unwrap()
            .status(),
        404
    );
    assert_eq!(
        client
            .get(format!("http://{addr}/v1/incidents/not-an-id"))
            .send()
            .await
            .unwrap()
            .status(),
        404
    );
    assert_eq!(c.store.lock().unwrap().conn.total_changes(), changes);
    stop.cancel();
    tokio::time::timeout(Duration::from_secs(1), server)
        .await
        .unwrap()
        .unwrap();

    // The public health listener must never serve payloads.
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let stop = CancellationToken::new();
    let server = tokio::spawn(health::serve(listener, c.clone(), stop.clone()));
    assert_eq!(
        client
            .get(format!("http://{addr}/v1/status"))
            .send()
            .await
            .unwrap()
            .status(),
        404
    );
    stop.cancel();
    server.await.unwrap();

    let store = c.store.lock().unwrap();
    store
        .conn
        .execute("DELETE FROM outbox WHERE incident=?1", [&id])
        .unwrap();
    let detail = store.incident_detail(&id).unwrap().unwrap();
    assert!(detail.last_notification.is_none());
    assert_eq!(detail.incident.notification_sequence, 1);
}

#[tokio::test]
async fn inspection_refuses_non_loopback_listener() {
    let dir = tempfile::tempdir().unwrap();
    let listener = tokio::net::TcpListener::bind("0.0.0.0:0").await.unwrap();
    tokio::time::timeout(
        Duration::from_secs(1),
        inspect::serve(listener, controller(&dir), CancellationToken::new()),
    )
    .await
    .unwrap();
}
#[test]
fn persistence_contract_expiry_and_unsafe_reuse() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("db");
    let c = Contract::jev("synthetic".into());
    let p = Policy::default();
    let e = event("security evidence", 1);
    let mut s = Store::open(&path).unwrap();
    assert!(Store::open(&path).is_err(), "single writer enforced");
    s.apply(&e, &c, &p, 100, 300).unwrap();
    drop(s);
    let mut s = Store::open(&path).unwrap();
    assert!(matches!(
        s.lookup(&e, &c, 101, 300, false).unwrap(),
        Lookup::Hit(_)
    ));
    let mut replay = e.clone();
    replay.analysis_reused = true;
    assert!(s.apply(&replay, &c, &p, 101, 300).unwrap().duplicate);
    for field in ["model", "provider", "version"] {
        let mut changed = c.clone();
        match field {
            "model" => changed.model.push('x'),
            "provider" => changed.provider.push('x'),
            _ => changed.version.push('x'),
        };
        assert!(matches!(
            s.lookup(&e, &changed, 101, 300, false).unwrap(),
            Lookup::Miss
        ));
    }
    let mut changed = e.clone();
    changed.source.insert("restart_count".into(), 1.into());
    assert!(matches!(
        s.lookup(&changed, &c, 101, 300, false).unwrap(),
        Lookup::Miss
    ));
    changed = e.clone();
    changed.text.push('1');
    assert!(matches!(
        s.lookup(&changed, &c, 101, 300, false).unwrap(),
        Lookup::Miss
    ));
    assert!(matches!(
        s.lookup(&e, &c, 101, 300, true).unwrap(),
        Lookup::Miss
    ));
    assert!(matches!(
        s.lookup(&e, &c, 400, 300, false).unwrap(),
        Lookup::Expired
    ));
    assert!(matches!(
        s.lookup(&e, &c, 150, 50, false).unwrap(),
        Lookup::Expired
    ));
    assert!(matches!(
        s.lookup(&e, &c, 99, 300, false).unwrap(),
        Lookup::Expired
    ));
    assert!(s.prune(400).unwrap() > 0);
    assert!(matches!(
        s.lookup(&e, &c, 400, 300, false).unwrap(),
        Lookup::Miss
    ));
    for variant in 0..6 {
        let mut bad = e.clone();
        bad.text = format!("bad-{variant}");
        match variant {
            0 => bad.truncated = true,
            1 => bad.judgment = Judgment::failed("synthetic"),
            2 => bad.judgment.category = Category::Unknown,
            3 => bad.judgment.importance = Importance::Unknown,
            4 => bad.judgment.severity_confidence = None,
            _ => bad.judgment.importance_confidence = Some(f64::NAN),
        };
        assert!(!cacheable(&bad));
        s.apply(&bad, &c, &p, 500, 300).unwrap();
        assert!(matches!(
            s.lookup(&bad, &c, 501, 300, false).unwrap(),
            Lookup::Miss
        ));
    }
}
#[test]
fn policy_thresholds_and_abstention_are_replayable() {
    let mut p = Policy::default();
    let mut e = event("synthetic", 1);
    assert_eq!(p.decide(&e), Decision::Notify);
    e.judgment.category_confidence = Some(0.85);
    assert_eq!(p.decide(&e), Decision::Notify);
    e.judgment.category_confidence = Some(0.849);
    assert_eq!(p.decide(&e), Decision::Review);
    p.abstention = policy::Abstention::Record;
    assert_eq!(p.decide(&e), Decision::Abstain);
    e.judgment.category_confidence = Some(0.99);
    e.judgment.category = Category::Capacity;
    assert_eq!(p.decide(&e), Decision::Ignore);
    e.judgment.category = Category::Security;
    e.judgment.severity = Severity::Info;
    assert_eq!(p.decide(&e), Decision::Ignore);
    e.truncated = true;
    assert_eq!(p.decide(&e), Decision::Abstain);
    let replay: Policy = serde_json::from_str(&serde_json::to_string(&p).unwrap()).unwrap();
    assert_eq!(replay.decide(&e), p.decide(&e));
    p.min_confidence = f64::NAN;
    assert!(p.validate().is_err());
    p.min_confidence = 0.9;
    p.recurrence_count = 1;
    assert!(p.validate().is_err());
    assert!(serde_json::from_str::<Policy>(r#"{"auto_remediate":true}"#).is_err());
}
#[test]
fn cooldown_escalation_restart_and_atomic_outbox() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("db");
    let c = Contract::jev("synthetic".into());
    let p = Policy {
        recurrence_count: 3,
        ..Policy::default()
    };
    let mut s = Store::open(&path).unwrap();
    assert!(
        s.apply(&event("same", 1), &c, &p, 100, 300)
            .unwrap()
            .enqueued
    );
    assert!(
        !s.apply(&event("same", 2), &c, &p, 101, 300)
            .unwrap()
            .enqueued
    );
    assert!(
        s.apply(&event("same", 3), &c, &p, 102, 300)
            .unwrap()
            .enqueued
    );
    drop(s);
    let mut s = Store::open(&path).unwrap();
    assert_eq!(s.counts().unwrap(), (2, 0));
    assert!(
        s.apply(&event("same", 3), &c, &p, 103, 300)
            .unwrap()
            .duplicate
    );
    assert!(
        s.apply(&event("same", 4), &c, &p, 1001, 300)
            .unwrap()
            .enqueued
    );
    let levels: Vec<u32> = s
        .conn
        .prepare("SELECT payload FROM outbox ORDER BY updated")
        .unwrap()
        .query_map([], |r| r.get::<_, String>(0))
        .unwrap()
        .map(|r| {
            serde_json::from_str::<serde_json::Value>(&r.unwrap()).unwrap()["escalation_level"]
                .as_u64()
                .unwrap() as u32
        })
        .collect();
    assert_eq!(levels, vec![0, 1, 1]);
    // Abort the outbox insert: verdict, novelty and incident updates must roll back too.
    s.conn.execute_batch("CREATE TRIGGER fail_outbox BEFORE INSERT ON outbox BEGIN SELECT RAISE(ABORT,'synthetic'); END;").unwrap();
    let fresh = event("different", 5);
    assert!(s.apply(&fresh, &c, &p, 1002, 300).is_err());
    assert!(matches!(
        s.lookup(&fresh, &c, 1003, 300, false).unwrap(),
        Lookup::Miss
    ));
    s.conn.execute_batch("DROP TRIGGER fail_outbox").unwrap();
    assert!(s.apply(&fresh, &c, &p, 1003, 300).unwrap().enqueued);
}
#[test]
fn outbox_crash_retry_rate_limit_dead_letter_and_pruning() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("db");
    let c = Contract::jev("synthetic".into());
    let p = Policy::default();
    let mut s = Store::open(&path).unwrap();
    s.apply(&event("one", 1), &c, &p, 100, 300).unwrap();
    s.apply(&event("two", 2), &c, &p, 100, 300).unwrap();
    let first = s.claim(100, 3, 10).unwrap().unwrap();
    assert_eq!(first.attempts, 1);
    assert!(s.claim(109, 3, 10).unwrap().is_none());
    drop(s);
    let mut s = Store::open(&path).unwrap();
    let other = s.claim(110, 3, 10).unwrap().unwrap();
    assert_ne!(first.id, other.id);
    s.finish(&other, 110, true, 3, 0).unwrap();
    assert!(s.claim(129, 3, 10).unwrap().is_none());
    let retry = s.claim(130, 3, 10).unwrap().unwrap();
    assert_eq!(first.id, retry.id);
    assert_eq!(retry.attempts, 2);
    s.finish(&retry, 130, false, 3, 0).unwrap();
    assert!(s.claim(134, 3, 10).unwrap().is_none());
    let last = s.claim(140, 3, 10).unwrap().unwrap();
    assert_eq!(last.attempts, 3);
    // Crash during the final attempt: lease expires into dead letter, never silently delivered.
    drop(s);
    let mut s = Store::open(&path).unwrap();
    assert!(s.claim(170, 3, 10).unwrap().is_none());
    assert_eq!(s.counts().unwrap(), (0, 1));
    s.prune(RETENTION + 1000).unwrap();
    assert_eq!(s.counts().unwrap(), (0, 1));
    let delivered: i64 = s
        .conn
        .query_row(
            "SELECT count(*) FROM outbox WHERE status='delivered'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(delivered, 0);
    let incidents: i64 = s
        .conn
        .query_row("SELECT count(*) FROM incidents", [], |r| r.get(0))
        .unwrap();
    assert_eq!(incidents, 2, "monotonic sequence/level tombstones retained");
}
#[test]
fn bounded_prune_and_response_payload_metadata() {
    let dir = tempfile::tempdir().unwrap();
    let mut s = Store::open(&dir.path().join("db")).unwrap();
    let tx = s.conn.transaction().unwrap();
    for n in 0..300 {
        tx.execute("INSERT INTO verdicts VALUES (?1,'{}',0,1)", [n.to_string()])
            .unwrap();
    }
    tx.commit().unwrap();
    assert_eq!(s.prune(2).unwrap(), store::PRUNE_BATCH as usize);
    let e = event("password=synthetic-secret suspicious grant", 1);
    let n = Notification::new(
        "id",
        "incident",
        &e,
        &Policy::default(),
        Decision::Notify,
        0,
        1,
        100,
    );
    let raw = serde_json::to_string(&n).unwrap();
    assert!(!raw.contains("synthetic-secret"));
    assert!(!raw.contains("private-context"));
    assert!(!raw.contains("private-path"));
}
#[tokio::test]
async fn database_failure_is_visible_and_fatal() {
    let dir = tempfile::tempdir().unwrap();
    let c = controller(&dir);
    c.db(|s| {
        s.conn.execute_batch("PRAGMA query_only=ON").unwrap();
        Ok(())
    })
    .await
    .unwrap();
    assert!(c.apply(&event("one", 1)).await.is_err());
    assert!(!c.ready.load(Ordering::Acquire));
    assert!(c.fault.is_cancelled());
    assert_eq!(c.metrics.lock().unwrap().store_failures, 1);
}
#[tokio::test]
async fn webhook_sanitized_request_status_and_bounds() {
    let (url, server) = crate::test_support::http(vec![
        (204, "".into()),
        (500, "synthetic-private-response".into()),
        (200, "x".repeat(8193)),
    ])
    .await;
    let sink = sink::Webhook::new(&url, Some("synthetic-token"), true).unwrap();
    let e = event("password=synthetic-secret grant", 1);
    let n = Notification::new(
        "id",
        "incident",
        &e,
        &Policy::default(),
        Decision::Notify,
        0,
        1,
        100,
    );
    let body = serde_json::to_string(&n).unwrap();
    assert!(sink.deliver("id", &body).await.is_ok());
    let err = sink.deliver("id", &body).await.unwrap_err();
    assert!(!err.contains("private"));
    assert!(sink.deliver("id", &body).await.is_err());
    let requests = server.await.unwrap();
    assert!(requests[0].contains("idempotency-key: id"));
    assert!(requests[0].contains("authorization: Bearer synthetic-token"));
    assert!(!requests[0].contains("synthetic-secret"));
    assert!(!requests[0].contains("private-context"));
    assert!(sink.deliver("id", &"x".repeat(131073)).await.is_err());
    assert!(sink::Webhook::new("http://127.0.0.1", None, false).is_err());
    assert!(
        sink::Webhook::new(
            concat!("https://user:", "synthetic", "@example.invalid"),
            None,
            false
        )
        .is_err()
    );
    assert!(sink::Webhook::new("https://example.invalid", Some("bad\nheader"), false).is_err());
}
#[tokio::test]
async fn webhook_never_follows_redirects() {
    use tokio::{
        io::{AsyncReadExt, AsyncWriteExt},
        net::TcpListener,
    };
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let server = tokio::spawn(async move {
        let (mut s, _) = listener.accept().await.unwrap();
        let mut b = [0; 4096];
        assert!(s.read(&mut b).await.unwrap() > 0);
        s.write_all(format!("HTTP/1.1 302 Found\r\nLocation: http://{addr}/redirect-secret\r\nContent-Length: 0\r\nConnection: close\r\n\r\n").as_bytes()).await.unwrap();
        assert!(
            tokio::time::timeout(Duration::from_millis(100), listener.accept())
                .await
                .is_err()
        );
    });
    let sink = sink::Webhook::new(&format!("http://{addr}"), None, true).unwrap();
    assert!(sink.deliver("id", "{}").await.is_err());
    server.await.unwrap();
}
#[tokio::test]
async fn offline_controller_smoke_and_shutdown() {
    // No Kubernetes, Jev HTTP server, environment credentials or kubeconfig. A known
    // synthetic verdict is seeded, then the real bounded analysis lane reuses it.
    let dir = tempfile::tempdir().unwrap();
    let c = controller(&dir);
    let e = event("password=synthetic-secret suspicious privilege grant", 1);
    let c1 = c.contract.clone();
    let e1 = e.clone();
    c.db(move |s| {
        s.apply(&e1, &c1, &Policy::default(), now(), 300)?;
        Ok(())
    })
    .await
    .unwrap();
    let (url, server) = crate::test_support::http(vec![(204, "".into())]).await;
    let sink = Arc::new(sink::Webhook::new(&url, None, true).unwrap());
    let stop = CancellationToken::new();
    let worker = tokio::spawn({
        let c = c.clone();
        let stop = stop.clone();
        async move { c.deliver(sink, stop).await }
    });
    let (tx, rx) = tokio::sync::mpsc::channel(2);
    tx.send(e.clone()).await.unwrap();
    let mut second = e.clone();
    second.timestamp = Some("2026-09-20T00:01:00Z".into());
    tx.send(second).await.unwrap();
    drop(tx);
    let opts = crate::runtime::AnalyzeOptions {
        batch_size: 2,
        max_batches: 1,
        max_cost: 1.0,
        grouping: true,
        retain: 2,
        max_events: 10,
        live: true,
        print_events: false,
    };
    let (report, _) = crate::runtime::analyze_controlled(
        rx,
        c.metrics.clone(),
        None,
        opts,
        CancellationToken::new(),
        Some(c.clone()),
    )
    .await;
    assert_eq!(report.reused, 2);
    assert_eq!(report.batches, 0);
    assert_eq!(report.total, 2);
    let requests = tokio::time::timeout(Duration::from_secs(3), server)
        .await
        .unwrap()
        .unwrap();
    assert_eq!(requests.len(), 1);
    assert!(!requests[0].contains("synthetic-secret"));
    // Wait for durable delivery checkpoint, not an arbitrary sleep.
    tokio::time::timeout(Duration::from_secs(3), async {
        loop {
            if c.db(|s| s.counts()).await.unwrap().0 == 0 {
                break;
            }
            tokio::task::yield_now().await;
        }
    })
    .await
    .unwrap();
    stop.cancel();
    tokio::time::timeout(Duration::from_secs(1), worker)
        .await
        .unwrap()
        .unwrap();
    assert_eq!(c.metrics.lock().unwrap().verdict_hits, 2);
    assert_eq!(c.metrics.lock().unwrap().novelty_duplicates, 1);
}
#[tokio::test]
async fn health_readiness_is_local_and_shutdown_interrupts_idle() {
    let dir = tempfile::tempdir().unwrap();
    let c = controller(&dir);
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let stop = CancellationToken::new();
    let task = tokio::spawn(health::serve(listener, c.clone(), stop.clone()));
    let http = reqwest::Client::new();
    assert_eq!(
        http.get(format!("http://{addr}/readyz"))
            .send()
            .await
            .unwrap()
            .status(),
        200
    );
    c.metrics.lock().unwrap().notification_failures = 1;
    assert_eq!(
        http.get(format!("http://{addr}/readyz"))
            .send()
            .await
            .unwrap()
            .status(),
        200
    );
    c.ready.store(false, Ordering::Release);
    assert_eq!(
        http.get(format!("http://{addr}/readyz"))
            .send()
            .await
            .unwrap()
            .status(),
        503
    );
    assert_eq!(
        http.get(format!("http://{addr}/livez"))
            .send()
            .await
            .unwrap()
            .status(),
        200
    );
    let metrics = http
        .get(format!("http://{addr}/metrics"))
        .send()
        .await
        .unwrap()
        .text()
        .await
        .unwrap();
    assert!(metrics.contains("jevernetes_coverage_complete 0"));
    assert!(metrics.contains("jevernetes_notification_failures 1"));
    stop.cancel();
    tokio::time::timeout(Duration::from_secs(1), task)
        .await
        .unwrap()
        .unwrap();
}

#[test]
fn changed_decisions_do_not_manufacture_recurrence() {
    let dir = tempfile::tempdir().unwrap();
    let mut s = Store::open(&dir.path().join("db")).unwrap();
    let c = Contract::jev("synthetic".into());
    let p = Policy::default();
    let mut e = event("same", 1);
    e.judgment.category_confidence = Some(0.5);
    s.apply(&e, &c, &p, 100, 300).unwrap();
    e.judgment.category_confidence = Some(0.99);
    assert!(s.apply(&e, &c, &p, 101, 300).unwrap().enqueued);
    e.judgment.category_confidence = Some(0.98);
    assert!(s.apply(&e, &c, &p, 102, 300).unwrap().duplicate);
    let count: u32 = s
        .conn
        .query_row("SELECT count FROM incidents", [], |r| r.get(0))
        .unwrap();
    assert_eq!(count, 1);
}
struct StalledSink {
    entered: Arc<tokio::sync::Notify>,
}
impl Sink for StalledSink {
    fn deliver<'a>(&'a self, _id: &'a str, _payload: &'a str) -> sink::Delivery<'a> {
        Box::pin(async move {
            self.entered.notify_one();
            std::future::pending().await
        })
    }
}
#[tokio::test]
async fn notification_outage_does_not_block_ingestion_and_shutdown_keeps_intent() {
    let dir = tempfile::tempdir().unwrap();
    let c = controller(&dir);
    let e = event("one", 1);
    c.apply(&e).await.unwrap();
    let entered = Arc::new(tokio::sync::Notify::new());
    let stop = CancellationToken::new();
    let task = tokio::spawn({
        let c = c.clone();
        let stop = stop.clone();
        let entered = entered.clone();
        async move { c.deliver(Arc::new(StalledSink { entered }), stop).await }
    });
    tokio::time::timeout(Duration::from_secs(1), entered.notified())
        .await
        .unwrap();
    tokio::time::timeout(Duration::from_secs(1), c.apply(&event("two", 2)))
        .await
        .unwrap()
        .unwrap();
    assert_eq!(c.db(|s| s.counts()).await.unwrap().0, 2);
    stop.cancel();
    tokio::time::timeout(Duration::from_secs(1), task)
        .await
        .unwrap()
        .unwrap();
    assert_eq!(c.db(|s| s.counts()).await.unwrap().0, 2);
}
#[tokio::test]
async fn persistent_lookup_precedes_jev_and_only_new_groups_are_batched() {
    let dir = tempfile::tempdir().unwrap();
    let c = controller(&dir);
    let first = event("cached", 1);
    c.apply(&first).await.unwrap();
    let (url, server) =
        crate::test_support::http(vec![(200, crate::test_support::verdict())]).await;
    let (tx, rx) = tokio::sync::mpsc::channel(3);
    tx.send(first).await.unwrap();
    tx.send(event("novel", 2)).await.unwrap();
    tx.send(event("novel", 3)).await.unwrap();
    drop(tx);
    let opts = crate::runtime::AnalyzeOptions {
        batch_size: 3,
        max_batches: 1,
        max_cost: 1.0,
        grouping: true,
        retain: 3,
        max_events: 10,
        live: true,
        print_events: false,
    };
    let (r, _) = crate::runtime::analyze_controlled(
        rx,
        c.metrics.clone(),
        Some(crate::jev::Jev::test_client(url)),
        opts,
        CancellationToken::new(),
        Some(c.clone()),
    )
    .await;
    assert_eq!(r.batches, 1);
    assert_eq!(r.reused, 2);
    let requests = server.await.unwrap();
    assert_eq!(requests.len(), 1);
    let body: serde_json::Value =
        serde_json::from_str(requests[0].split("\r\n\r\n").nth(1).unwrap()).unwrap();
    assert_eq!(body["state"]["events"].as_array().unwrap().len(), 1);
    assert_eq!(body["state"]["events"][0]["line"], "novel");
}

#[test]
fn attempt_cap_holds_when_expired_leases_exceed_maintenance_batch() {
    let dir = tempfile::tempdir().unwrap();
    let mut s = Store::open(&dir.path().join("db")).unwrap();
    let tx = s.conn.transaction().unwrap();
    for n in 0..300 {
        tx.execute(
            "INSERT INTO outbox VALUES (?1,'incident','{}','pending',8,0,0)",
            [n.to_string()],
        )
        .unwrap();
    }
    tx.commit().unwrap();
    assert!(s.claim(100, 8, 1).unwrap().is_none());
    assert_eq!(s.counts().unwrap(), (44, 256));
    assert!(s.claim(101, 8, 1).unwrap().is_none());
    assert_eq!(s.counts().unwrap(), (0, 300));
}

#[test]
fn full_outbox_refuses_new_evidence_atomically() {
    let dir = tempfile::tempdir().unwrap();
    let mut s = Store::open(&dir.path().join("state.db")).unwrap();
    let tx = s.conn.transaction().unwrap();
    for n in 0..store::OUTBOX_LIMIT {
        tx.execute(
            "INSERT INTO outbox VALUES (?1,'incident','{}','pending',0,0,0)",
            [n.to_string()],
        )
        .unwrap();
    }
    tx.commit().unwrap();
    let c = Contract::jev("synthetic".into());
    let e = event("capacity", 1);
    assert_eq!(
        s.apply(&e, &c, &Policy::default(), 100, 300).err(),
        Some("Controller outbox capacity exhausted")
    );
    assert!(matches!(
        s.lookup(&e, &c, 101, 300, false).unwrap(),
        Lookup::Miss
    ));
    assert_eq!(
        s.conn
            .query_row::<i64, _, _>("SELECT count(*) FROM seen", [], |r| r.get(0))
            .unwrap(),
        0
    );
}

#[test]
fn review_to_notify_bypasses_cooldown_after_restart_and_replay() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("state.db");
    let contract = Contract::jev("synthetic".into());
    let policy = Policy::default();
    let mut review = event("same incident", 1);
    review.judgment.category_confidence = Some(0.5);
    let mut s = Store::open(&path).unwrap();
    assert!(
        s.apply(&review, &contract, &policy, 100, 300)
            .unwrap()
            .enqueued
    );
    let row = s.claim(100, 8, 1).unwrap().unwrap();
    s.finish(&row, 100, true, 8, 0).unwrap();
    drop(s);
    let mut s = Store::open(&path).unwrap();
    let notify = event("same incident", 2);
    assert!(
        s.apply(&notify, &contract, &policy, 101, 300)
            .unwrap()
            .enqueued
    );
    drop(s);
    let mut s = Store::open(&path).unwrap();
    assert!(
        s.apply(&notify, &contract, &policy, 102, 300)
            .unwrap()
            .duplicate
    );
    assert!(
        !s.apply(&event("same incident", 3), &contract, &policy, 103, 300)
            .unwrap()
            .enqueued
    );
    assert_eq!(s.counts().unwrap(), (1, 0));
    let (count, level, sequence): (u32, u32, u32) = s
        .conn
        .query_row("SELECT count,level,sequence FROM incidents", [], |r| {
            Ok((r.get(0)?, r.get(1)?, r.get(2)?))
        })
        .unwrap();
    assert_eq!((count, level, sequence), (3, 0, 2));
}

#[test]
fn stdout_backpressure_does_not_occupy_tokio_blocking_pool() {
    // Isolate real stdout in a child whose pipe is deliberately never drained.
    // The injected-writer tests additionally exercise retries and cancellation.
    const CHILD: &str = "JEV_SYNTHETIC_STDOUT_CHILD";
    if std::env::var_os(CHILD).is_some() {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .max_blocking_threads(1)
            .build()
            .unwrap();
        let available = runtime.block_on(async {
            let sink = sink::Stdout::new().unwrap();
            let payload = "x".repeat(128 * 1024);
            assert!(
                tokio::time::timeout(Duration::from_millis(100), sink.deliver("id", &payload))
                    .await
                    .is_err()
            );
            tokio::time::timeout(
                Duration::from_millis(200),
                tokio::task::spawn_blocking(|| ()),
            )
            .await
            .is_ok()
        });
        runtime.shutdown_timeout(Duration::from_millis(10));
        // Avoid libtest flushing the intentionally blocked stdout on exit.
        std::process::exit(if available { 0 } else { 1 });
    }
    let mut child = std::process::Command::new(std::env::current_exe().unwrap())
        .env_clear()
        .env(CHILD, "1")
        .args([
            "--exact",
            "controller::tests::stdout_backpressure_does_not_occupy_tokio_blocking_pool",
            "--nocapture",
        ])
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::null())
        .spawn()
        .unwrap();
    let deadline = std::time::Instant::now() + Duration::from_secs(5);
    let status = loop {
        if let Some(status) = child.try_wait().unwrap() {
            break status;
        }
        if std::time::Instant::now() >= deadline {
            child.kill().unwrap();
            child.wait().unwrap();
            panic!("blocked stdout prevented process shutdown");
        }
        std::thread::sleep(Duration::from_millis(10));
    };
    assert!(
        status.success(),
        "blocked stdout starved Tokio's only blocking worker"
    );
}

#[test]
fn promotion_rolls_back_seen_class_and_sequence_with_outbox() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("db");
    let c = Contract::jev("synthetic".into());
    let p = Policy {
        recurrence_count: 2,
        ..Policy::default()
    };
    let mut review = event("same", 1);
    review.judgment.category_confidence = Some(0.5);
    let mut s = Store::open(&path).unwrap();
    s.apply(&review, &c, &p, 100, 300).unwrap();
    s.conn.execute_batch("CREATE TRIGGER fail_outbox BEFORE INSERT ON outbox BEGIN SELECT RAISE(ABORT,'synthetic'); END;").unwrap();
    let notify = event("same", 2);
    assert!(s.apply(&notify, &c, &p, 101, 300).is_err());
    let state: (String, u32, u32, u32) = s
        .conn
        .query_row(
            "SELECT last_decision,count,level,sequence FROM incidents",
            [],
            |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?)),
        )
        .unwrap();
    assert_eq!(state, ("review".into(), 1, 0, 1));
    s.conn.execute_batch("DROP TRIGGER fail_outbox").unwrap();
    drop(s);
    let mut s = Store::open(&path).unwrap();
    assert!(s.apply(&notify, &c, &p, 102, 300).unwrap().enqueued);
    assert!(s.apply(&notify, &c, &p, 103, 300).unwrap().duplicate);
    let state: (String, u32, u32, u32) = s
        .conn
        .query_row(
            "SELECT last_decision,count,level,sequence FROM incidents",
            [],
            |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?)),
        )
        .unwrap();
    assert_eq!(state, ("notify".into(), 2, 1, 2));
    // A suppressed review must not overwrite the last enqueued class.
    review.timestamp = Some("2026-09-20T00:00:03Z".into());
    assert!(!s.apply(&review, &c, &p, 104, 300).unwrap().enqueued);
    let last: String = s
        .conn
        .query_row("SELECT last_decision FROM incidents", [], |r| r.get(0))
        .unwrap();
    assert_eq!(last, "notify");
}

fn downgrade_fixture_to_v1(s: &Store) {
    // Exactly the schema-1 layout, retaining all original table contents/indexes.
    s.conn
        .execute_batch("ALTER TABLE incidents DROP COLUMN last_decision; PRAGMA user_version=1;")
        .unwrap();
}

#[test]
fn schema_one_migration_recovers_last_enqueued_class_and_suppressed_replay() {
    for status in ["pending", "delivered", "dead", "pruned"] {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("db");
        let c = Contract::jev("synthetic".into());
        let p = Policy::default();
        let mut review = event("same", 1);
        review.judgment.category_confidence = Some(0.5);
        let mut s = Store::open(&path).unwrap();
        s.apply(&review, &c, &p, 100, 300).unwrap();
        if status == "pruned" {
            s.conn.execute("DELETE FROM outbox", []).unwrap();
        } else {
            s.conn
                .execute("UPDATE outbox SET status=?1", [status])
                .unwrap();
        }
        // Schema 1 could persist the seen signature but suppress this Notify.
        let notify = event("same", 2);
        let seen_key = digest(&(
            &notify.source,
            &notify.text,
            notify.truncated,
            &notify.timestamp,
        ));
        s.conn
            .execute(
                "INSERT INTO seen VALUES (?1,101,?2)",
                rusqlite::params![seen_key, digest(&(&c, &p, Decision::Notify))],
            )
            .unwrap();
        s.conn.execute("UPDATE incidents SET count=2", []).unwrap();
        downgrade_fixture_to_v1(&s);
        drop(s);
        let mut s = Store::open(&path).unwrap();
        assert_eq!(
            s.conn
                .query_row::<i64, _, _>("PRAGMA user_version", [], |r| r.get(0))
                .unwrap(),
            store::SCHEMA_VERSION
        );
        let prior: Option<String> = s
            .conn
            .query_row("SELECT last_decision FROM incidents", [], |r| r.get(0))
            .unwrap();
        assert_eq!(
            prior.as_deref(),
            if status == "pruned" {
                None
            } else {
                Some("review")
            }
        );
        assert!(s.apply(&notify, &c, &p, 102, 300).unwrap().enqueued);
        drop(s);
        let mut s = Store::open(&path).unwrap();
        assert!(s.apply(&notify, &c, &p, 103, 300).unwrap().duplicate);
        let state: (u32, u32, u32) = s
            .conn
            .query_row("SELECT count,level,sequence FROM incidents", [], |r| {
                Ok((r.get(0)?, r.get(1)?, r.get(2)?))
            })
            .unwrap();
        assert_eq!(
            state,
            (2, 0, 2),
            "migration/replay must not manufacture recurrence"
        );
    }
}

#[test]
fn schema_migration_uses_sequence_and_preserves_notify_cooldown() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("db");
    let c = Contract::jev("synthetic".into());
    let p = Policy::default();
    let mut e = event("same", 1);
    e.judgment.category_confidence = Some(0.5);
    let mut s = Store::open(&path).unwrap();
    s.apply(&e, &c, &p, 100, 300).unwrap();
    s.conn
        .execute("UPDATE outbox SET updated=9999", [])
        .unwrap();
    e.judgment.category_confidence = Some(0.99);
    s.apply(&e, &c, &p, 101, 300).unwrap();
    downgrade_fixture_to_v1(&s);
    drop(s);
    let mut s = Store::open(&path).unwrap();
    assert!(s.apply(&e, &c, &p, 102, 300).unwrap().duplicate);
    assert!(
        !s.apply(&event("same", 2), &c, &p, 103, 300)
            .unwrap()
            .enqueued
    );
    assert_eq!(s.counts().unwrap(), (2, 0));
}

#[test]
fn migration_failure_rolls_back_and_unknown_versions_are_untouched() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("db");
    let mut s = Store::open(&path).unwrap();
    s.apply(
        &event("same", 1),
        &Contract::jev("synthetic".into()),
        &Policy::default(),
        100,
        300,
    )
    .unwrap();
    downgrade_fixture_to_v1(&s);
    s.conn
        .execute("UPDATE outbox SET payload='invalid'", [])
        .unwrap();
    drop(s);
    assert!(Store::open(&path).is_err());
    let conn = rusqlite::Connection::open(&path).unwrap();
    assert_eq!(
        conn.query_row::<i64, _, _>("PRAGMA user_version", [], |r| r.get(0))
            .unwrap(),
        1
    );
    assert!(conn.prepare("SELECT last_decision FROM incidents").is_err());
    for version in [0, 3, 999] {
        conn.pragma_update(None, "user_version", version).unwrap();
        let before = std::fs::read(&path).unwrap();
        assert!(Store::open(&path).is_err());
        assert_eq!(std::fs::read(&path).unwrap(), before);
    }
}

struct WriterGate(Arc<(Mutex<bool>, std::sync::Condvar)>);
impl WriterGate {
    fn new() -> Self {
        Self(Arc::new((Mutex::new(false), std::sync::Condvar::new())))
    }
}
impl Drop for WriterGate {
    fn drop(&mut self) {
        *self.0.0.lock().unwrap() = true;
        self.0.1.notify_all();
    }
}
struct GatedWriter {
    gate: Arc<(Mutex<bool>, std::sync::Condvar)>,
    entered: Arc<tokio::sync::Notify>,
    writes: Arc<std::sync::atomic::AtomicUsize>,
    block_flush: bool,
}
impl GatedWriter {
    fn wait(&self) {
        self.entered.notify_one();
        let (lock, wake) = &*self.gate;
        let mut released = lock.lock().unwrap();
        while !*released {
            released = wake.wait(released).unwrap();
        }
    }
}
impl std::io::Write for GatedWriter {
    fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
        self.writes.fetch_add(1, Ordering::Relaxed);
        if !self.block_flush {
            self.wait();
        }
        Ok(bytes.len())
    }
    fn flush(&mut self) -> std::io::Result<()> {
        if self.block_flush {
            self.wait();
        }
        Ok(())
    }
}

#[test]
fn stuck_writer_retries_cancellation_state_and_runtime_shutdown_are_independent() {
    for block_flush in [false, true] {
        let dir = tempfile::tempdir().unwrap();
        let gate = WriterGate::new();
        let entered = Arc::new(tokio::sync::Notify::new());
        let writes = Arc::new(std::sync::atomic::AtomicUsize::new(0));
        let sink = Arc::new(
            sink::Stdout::with_writer(GatedWriter {
                gate: gate.0.clone(),
                entered: entered.clone(),
                writes: writes.clone(),
                block_flush,
            })
            .unwrap(),
        );
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .max_blocking_threads(1)
            .build()
            .unwrap();
        runtime.block_on(async {
            let c = controller(&dir);
            c.apply(&event("one", 1)).await.unwrap();
            let stop = CancellationToken::new();
            let worker = tokio::spawn({
                let c = c.clone();
                let sink = sink.clone();
                let stop = stop.clone();
                async move { c.deliver(sink, stop).await }
            });
            tokio::time::timeout(Duration::from_secs(1), entered.notified())
                .await
                .unwrap();
            // Shutdown cancels the delivery future, leaving the reserved intent pending.
            stop.cancel();
            tokio::time::timeout(Duration::from_secs(1), worker)
                .await
                .unwrap()
                .unwrap();
            assert_eq!(c.db(|s| s.counts()).await.unwrap(), (1, 0));
            for _ in 0..32 {
                assert!(
                    tokio::time::timeout(Duration::from_millis(100), sink.deliver("retry", "{}"))
                        .await
                        .unwrap()
                        .is_err()
                );
            }
            tokio::time::timeout(Duration::from_secs(1), c.apply(&event("two", 2)))
                .await
                .unwrap()
                .unwrap();
            // Drive persisted leases/retries without wall-clock sleeps. Busy writes consume
            // the ordinary attempt budget; they never get marked delivered or spawn jobs.
            for attempt in 0..16 {
                let at = now() + 30 + attempt * 400;
                let row = c.db(move |s| s.claim(at, 8, 1)).await.unwrap();
                if let Some(row) = row {
                    assert!(sink.deliver(&row.id, &row.payload).await.is_err());
                    c.db(move |s| s.finish(&row, at, false, 8, 0))
                        .await
                        .unwrap();
                }
            }
            assert_eq!(c.db(|s| s.counts()).await.unwrap(), (0, 2));
            assert!(c.ready.load(Ordering::Acquire));
            assert_eq!(writes.load(Ordering::Relaxed), 1);
        });
        drop(sink);
        // Neither resource drop nor runtime shutdown joins the blocked writer.
        let start = std::time::Instant::now();
        drop(runtime);
        assert!(start.elapsed() < Duration::from_secs(1));
        assert_eq!(writes.load(Ordering::Relaxed), 1);
        drop(gate);
        let reopened = Store::open(&dir.path().join("state.db")).unwrap();
        assert_eq!(reopened.counts().unwrap(), (0, 2));
    }
}

#[tokio::test]
async fn writer_timeout_holds_permit_until_completion_then_allows_retry() {
    let gate = WriterGate::new();
    let entered = Arc::new(tokio::sync::Notify::new());
    let writes = Arc::new(std::sync::atomic::AtomicUsize::new(0));
    let sink = Arc::new(
        sink::Stdout::with_writer(GatedWriter {
            gate: gate.0.clone(),
            entered: entered.clone(),
            writes: writes.clone(),
            block_flush: false,
        })
        .unwrap(),
    );
    let attempt = tokio::spawn({
        let sink = sink.clone();
        async move { tokio::time::timeout(Duration::from_millis(100), sink.deliver("id", "{}")).await }
    });
    tokio::time::timeout(Duration::from_secs(1), entered.notified())
        .await
        .unwrap();
    assert!(attempt.await.unwrap().is_err());
    for _ in 0..16 {
        assert!(sink.deliver("id", "{}").await.is_err());
    }
    assert_eq!(writes.load(Ordering::Relaxed), 1);
    drop(gate);
    tokio::time::timeout(Duration::from_secs(1), async {
        loop {
            if sink.deliver("id", "{}").await.is_ok() {
                break;
            }
            tokio::task::yield_now().await;
        }
    })
    .await
    .unwrap();
    assert_eq!(writes.load(Ordering::Relaxed), 2);
    assert!(sink.deliver("id", &"x".repeat(131073)).await.is_err());
}

#[tokio::test]
async fn writer_errors_release_permit_and_success_flushes_jsonl() {
    struct Writer {
        failed: bool,
        bytes: Arc<Mutex<Vec<u8>>>,
        flushed: Arc<AtomicBool>,
    }
    impl std::io::Write for Writer {
        fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
            if !self.failed {
                self.failed = true;
                return Err(std::io::Error::other("synthetic"));
            }
            self.bytes.lock().unwrap().extend(bytes);
            Ok(bytes.len())
        }
        fn flush(&mut self) -> std::io::Result<()> {
            self.flushed.store(true, Ordering::Release);
            Ok(())
        }
    }
    let bytes = Arc::new(Mutex::new(Vec::new()));
    let flushed = Arc::new(AtomicBool::new(false));
    let sink = sink::Stdout::with_writer(Writer {
        failed: false,
        bytes: bytes.clone(),
        flushed: flushed.clone(),
    })
    .unwrap();
    assert!(sink.deliver("id", "{}").await.is_err());
    sink.deliver("id", "{}").await.unwrap();
    assert_eq!(*bytes.lock().unwrap(), b"{}\n");
    assert!(flushed.load(Ordering::Acquire));
}
