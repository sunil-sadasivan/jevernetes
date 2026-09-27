//! One bounded analysis lane: batching, exact reuse, retention, and explicit loss counters.
use crate::{
    drain::{Drain, Strategy},
    events::{Event, Framer, Parser, Source},
    grouping::Cache,
    jev::{Importance, Jev, Judgment},
    report::{Report, SharedMetrics, gap},
};
use std::{
    collections::HashMap,
    num::NonZeroUsize,
    time::{Duration, Instant},
};
use tokio::{
    io::{AsyncRead, AsyncReadExt},
    sync::mpsc,
};
use tokio_util::sync::CancellationToken;

#[derive(Clone)]
pub struct Sender {
    pub tx: mpsc::Sender<Event>,
    pub metrics: SharedMetrics,
}
impl Sender {
    pub fn emit(&self, event: Event) {
        let mut m = self.metrics.lock().expect("metrics lock");
        m.received += 1;
        if self.tx.try_send(event).is_err() {
            m.dropped += 1;
        }
        m.queue_depth = self.tx.max_capacity() - self.tx.capacity();
        m.queue_high_water = m.queue_high_water.max(m.queue_depth);
    }
    pub async fn send(&self, event: Event, stop: &CancellationToken) -> bool {
        let permit = tokio::select! {
            biased;
            _ = stop.cancelled() => None,
            result = self.tx.reserve() => result.ok(),
        };
        let sent = if let Some(permit) = permit {
            permit.send(event);
            true
        } else {
            gap(
                &self.metrics,
                &event.source,
                "limited",
                "Collection stopped before queued evidence could be delivered",
            );
            false
        };
        let mut m = self.metrics.lock().expect("metrics lock");
        m.received += 1;
        m.dropped += u64::from(!sent);
        m.queue_depth = self.tx.max_capacity() - self.tx.capacity();
        m.queue_high_water = m.queue_high_water.max(m.queue_depth);
        sent
    }
}
pub struct AnalyzeOptions {
    pub batch_size: usize,
    pub max_batches: u64,
    pub max_cost: f64,
    pub grouping: Strategy,
    pub drain_capacity: usize,
    pub retain: usize,
    pub max_events: u64,
    pub live: bool,
    pub print_events: bool,
}
pub async fn analyze(
    rx: mpsc::Receiver<Event>,
    sender_metrics: SharedMetrics,
    client: Option<Jev>,
    opts: AnalyzeOptions,
    stop: CancellationToken,
) -> (Report, Option<Jev>) {
    analyze_controlled(rx, sender_metrics, client, opts, stop, None).await
}
pub async fn analyze_controlled(
    mut rx: mpsc::Receiver<Event>,
    sender_metrics: SharedMetrics,
    mut client: Option<Jev>,
    opts: AnalyzeOptions,
    stop: CancellationToken,
    controller: Option<crate::controller::Controller>,
) -> (Report, Option<Jev>) {
    let mut report = Report::new(opts.retain);
    let mut cache = Cache::new(
        NonZeroUsize::new(opts.retain).expect("positive retention"),
        Duration::from_secs(300),
    );
    let mut drain = (opts.grouping == Strategy::Drain)
        .then(|| {
            Drain::new(
                opts.drain_capacity,
                Duration::from_secs(controller.as_ref().map_or(300, |c| c.ttl as u64)),
                sender_metrics.clone(),
            )
            .ok()
        })
        .flatten();
    while let Some(first) = rx.recv().await {
        if !opts.live && report.total >= opts.max_events {
            gap(
                &sender_metrics,
                &Source::new(),
                "limited",
                "Event limit reached; remaining input was not analyzed",
            );
            sender_metrics.lock().expect("metrics lock").dropped += 1 + rx.len() as u64;
            stop.cancel();
            break;
        }
        let batch_limit = if opts.live {
            opts.batch_size
        } else {
            opts.batch_size
                .min((opts.max_events - report.total) as usize)
        };
        let mut batch = vec![first];
        let deadline = tokio::time::Instant::now() + Duration::from_millis(350);
        while batch.len() < batch_limit {
            match tokio::time::timeout_at(deadline, rx.recv()).await {
                Ok(Some(event)) => batch.push(event),
                _ => break,
            }
        }
        sender_metrics.lock().expect("metrics lock").queue_depth = rx.len();
        let mut owned = Vec::new();
        let mut drain_tickets = HashMap::new();
        let mut references = HashMap::new();
        let mut duplicate_of = HashMap::new();
        for (i, event) in batch.iter_mut().enumerate() {
            if opts.grouping == Strategy::Exact
                && let Some(c) = &controller
            {
                match c.lookup(event).await {
                    Ok(Some(j)) => {
                        event.judgment = j;
                        event.analysis_reused = true;
                        continue;
                    }
                    Ok(None) => (),
                    Err(_) => {
                        event.judgment = Judgment::failed("Controller state unavailable");
                        gap(
                            &sender_metrics,
                            &event.source,
                            "error",
                            "Controller state unavailable; collection stopped",
                        );
                        stop.cancel();
                        continue;
                    }
                }
            }
            if client.is_none() {
                event.judgment.importance = if event.baseline.important {
                    Importance::Important
                } else {
                    Importance::Uncertain
                };
                continue;
            }
            if opts.grouping == Strategy::Drain {
                if let Some(drain) = &mut drain {
                    if let Some(ticket) = drain.prepare(
                        event,
                        Instant::now(),
                        controller.as_ref().is_some_and(|c| c.rescore),
                    ) {
                        // Classify each miss independently, including same-template
                        // observations. No verdict is published until the response.
                        drain_tickets.insert(i, ticket);
                    }
                    if event.analysis_reused {
                        continue;
                    }
                } else {
                    sender_metrics.lock().expect("metrics").drain_fallbacks += 1;
                }
            }
            if opts.grouping == Strategy::Exact && !event.truncated {
                if controller.is_none()
                    && let Some((judgment, id)) = cache.get(event, Instant::now())
                {
                    event.judgment = judgment;
                    event.analysis_reused = true;
                    event.analysis_representative_id = Some(id);
                    continue;
                }
                let key = controller
                    .as_ref()
                    .map_or_else(|| event.group_id.clone(), |c| c.contract.key(event));
                if let Some(index) = references.get(&key) {
                    duplicate_of.insert(i, *index);
                    continue;
                }
                references.insert(key, i);
            }
            owned.push(i);
        }
        if !owned.is_empty() {
            let client = client.as_mut().expect("online owned events");
            let error = if stop.is_cancelled() {
                Some("Stopped before classification")
            } else if report.batches >= opts.max_batches {
                Some("AI batch budget exhausted")
            } else if client.usage.estimated_cost_usd >= opts.max_cost {
                Some("Estimated cost threshold reached")
            } else {
                None
            };
            let result = if let Some(error) = error {
                Err(error.to_owned())
            } else {
                report.batches += 1;
                let events: Vec<_> = owned
                    .iter()
                    .map(|&i| Drain::representative(&batch[i], drain_tickets.get(&i)))
                    .collect();
                client.judge(&events, &stop).await
            };
            match result {
                Ok(judgments) => {
                    for (&i, judgment) in owned.iter().zip(judgments) {
                        batch[i].judgment = judgment.conservative(batch[i].truncated);
                        if opts.grouping == Strategy::Exact && controller.is_none() {
                            cache.insert(&batch[i], Instant::now());
                        }
                    }
                }
                Err(error) => {
                    for &i in &owned {
                        batch[i].judgment = Judgment::failed(&error);
                    }
                }
            }
            if opts.live && (error.is_some() || client.usage.estimated_cost_usd >= opts.max_cost) {
                stop.cancel();
            }
        }
        if let Some(drain) = &mut drain {
            for &i in &owned {
                if let Some(ticket) = drain_tickets.remove(&i) {
                    drain.complete(ticket, &batch[i], Instant::now());
                }
            }
        }
        for (i, representative) in duplicate_of {
            batch[i].judgment = batch[representative].judgment.clone();
            if batch[i].judgment.reusable() {
                batch[i].analysis_reused = true;
                batch[i].analysis_representative_id = Some(batch[representative].id.clone());
            }
        }
        if let Some(client) = &client {
            let mut m = sender_metrics.lock().expect("metrics lock");
            m.provider_batches = report.batches;
            m.provider_attempts = client.usage.request_attempts;
            m.provider_input_tokens = client.usage.input_tokens;
            m.provider_output_tokens = client.usage.output_tokens;
            m.provider_unmetered_requests = client.usage.unmetered_requests;
            m.estimated_cost_usd = client.usage.estimated_cost_usd;
        }
        for mut event in batch {
            if let Some(c) = &controller
                && c.apply_with_verdict_cache(&event, opts.grouping != Strategy::Drain)
                    .await
                    .is_err()
            {
                gap(
                    &sender_metrics,
                    &event.source,
                    "error",
                    "Controller state commit failed; evidence requires review",
                );
                event.judgment = Judgment::failed("Controller state commit failed");
                stop.cancel();
            }
            if event.truncated {
                gap(
                    &sender_metrics,
                    &event.source,
                    "limited",
                    "Oversized or incomplete event; classification requires review",
                );
            }
            if opts.print_events {
                eprintln!(
                    "[{}] {}",
                    serde_json::to_value(event.judgment.importance)
                        .expect("enum")
                        .as_str()
                        .expect("enum string"),
                    crate::events::console(&event.text)
                );
            }
            report.record(event);
        }
    }
    sender_metrics.lock().expect("metrics lock").queue_depth = 0;
    (report, client)
}
/// Snapshot/file reader: finite decompressed input, backpressure instead of dropping.
pub async fn ingest<R: AsyncRead + Unpin>(
    reader: R,
    source: Source,
    max_bytes: u64,
    sender: &Sender,
    stop: &CancellationToken,
) {
    ingest_with_read_timeout(reader, source, max_bytes, sender, stop, None).await;
}

async fn read_chunk<R: AsyncRead + Unpin>(
    reader: &mut R,
    buffer: &mut [u8],
    timeout: Option<Duration>,
) -> std::io::Result<usize> {
    match timeout {
        Some(timeout) => tokio::time::timeout(timeout, reader.read(buffer))
            .await
            .unwrap_or_else(|_| Err(std::io::ErrorKind::TimedOut.into())),
        None => reader.read(buffer).await,
    }
}

/// Only time spent reading counts toward this deadline, never queue backpressure.
pub(crate) async fn ingest_with_read_timeout<R: AsyncRead + Unpin>(
    mut reader: R,
    source: Source,
    max_bytes: u64,
    sender: &Sender,
    stop: &CancellationToken,
    read_timeout: Option<Duration>,
) {
    let mut parser = Parser::new(source.clone());
    let mut framer = Framer::default();
    let mut read = 0;
    let mut buffer = [0; 8192];
    let mut interrupted = false;
    let mut incomplete = false;
    loop {
        let want = ((max_bytes - read).min(buffer.len() as u64)) as usize;
        // At the cap, probe one byte for excess input using the same stop/deadline path.
        let n = tokio::select! {
            biased;
            _ = stop.cancelled() => {
                gap(&sender.metrics, &source, "limited", "Collection stopped before end of input");
                interrupted = true;
                break;
            }
            _ = sender.tx.closed() => {
                gap(&sender.metrics, &source, "limited", "Analysis stopped before end of input");
                interrupted = true;
                break;
            }
            result = read_chunk(&mut reader, &mut buffer[..want.max(1)], read_timeout) => match result {
                Ok(n) => n,
                Err(error) => {
                    let warning = if error.kind() == std::io::ErrorKind::TimedOut {
                        "Input read timed out"
                    } else {
                        "Input read failed"
                    };
                    gap(&sender.metrics, &source, "error", warning);
                    incomplete = true;
                    break;
                }
            }
        };
        if n == 0 {
            break;
        }
        if want == 0 {
            gap(
                &sender.metrics,
                &source,
                "limited",
                "Decompressed input byte limit reached",
            );
            incomplete = true;
            break;
        }
        read += n as u64;
        for line in framer.push(&buffer[..n]) {
            if let Some(event) = parser.feed(line) {
                if interrupted {
                    sender.emit(event);
                } else {
                    interrupted = !sender.send(event, stop).await;
                }
            }
        }
        // Finish only the bounded chunk already read; never wait for capacity after stopping.
        if interrupted {
            break;
        }
    }
    if let Some(mut line) = framer.finish() {
        line.truncated |= interrupted || incomplete;
        if let Some(event) = parser.feed(line) {
            if interrupted {
                sender.emit(event);
            } else {
                interrupted = !sender.send(event, stop).await;
            }
        }
    }
    if let Some(mut event) = parser.flush() {
        if interrupted || incomplete {
            event.truncated = true;
            event.group_id = crate::grouping::group_id(&event);
        }
        if interrupted {
            sender.emit(event);
        } else {
            let _ = sender.send(event, stop).await;
        }
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    use crate::report::Metrics;
    use std::sync::{Arc, Mutex};
    async fn interrupted_ingest<R: AsyncRead + Unpin>(reader: R, close: bool, expected: u64) {
        let metrics = Arc::new(Mutex::new(Metrics::default()));
        let (tx, mut rx) = mpsc::channel(1);
        let sender = Sender {
            tx,
            metrics: metrics.clone(),
        };
        let stop = CancellationToken::new();
        let mut ingest = Box::pin(ingest(reader, Source::new(), 1000, &sender, &stop));
        assert!(futures::poll!(&mut ingest).is_pending());
        assert_eq!(rx.len(), 1);
        if close {
            rx.close();
        } else {
            stop.cancel();
        }
        tokio::time::timeout(Duration::from_secs(1), ingest)
            .await
            .unwrap();
        let mut report = Report::new(10);
        while let Ok(mut event) = rx.try_recv() {
            event.judgment.importance = Importance::Uncertain;
            report.record(event);
        }
        assert_eq!(report.total, 1);
        let value = report.value(
            &metrics,
            &crate::jev::Usage::new(0.0, 0.0),
            serde_json::json!({}),
            true,
            false,
            0.0,
        );
        assert_eq!(value["summary"]["complete_within_window"], false);
        let m = metrics.lock().unwrap();
        assert_eq!(m.received, expected);
        assert_eq!(m.dropped, expected - 1);
        assert!(
            m.coverage
                .iter()
                .any(|c| c.status == "limited" && !c.warnings.is_empty())
        );
        assert_eq!(m.queue_high_water, 1);
    }
    #[tokio::test]
    async fn one_slot_cancellation_accounts_for_current_and_buffered_evidence() {
        interrupted_ingest(&b"one\ntwo\nthree\nfour\nfive"[..], false, 5).await;
    }
    #[tokio::test]
    async fn one_slot_closed_consumer_accounts_for_current_and_buffered_evidence() {
        interrupted_ingest(&b"one\ntwo\nthree\nfour\nfive"[..], true, 5).await;
    }
    #[tokio::test]
    async fn cancellation_during_final_flush_records_loss() {
        interrupted_ingest(&b"one\ntwo\n"[..], false, 2).await;
        interrupted_ingest(&b"one\ntwo"[..], false, 2).await;
    }
    #[tokio::test]
    async fn gzip_and_open_stdin_cancellation_account_for_buffered_evidence() {
        use tokio::io::AsyncWriteExt;
        let input = b"one\ntwo\nthree\nfour\nfive";
        let mut encoder = async_compression::tokio::write::GzipEncoder::new(Vec::new());
        encoder.write_all(input).await.unwrap();
        encoder.shutdown().await.unwrap();
        let compressed = encoder.into_inner();
        let decoder = async_compression::tokio::bufread::GzipDecoder::new(&compressed[..]);
        interrupted_ingest(decoder, false, 5).await;
        // An open pipe models stdin without EOF; shutdown must not wait for its writer.
        let (mut writer, reader) = tokio::io::duplex(128);
        writer.write_all(input).await.unwrap();
        interrupted_ingest(reader, false, 5).await;
    }
    #[tokio::test(start_paused = true)]
    async fn read_deadline_flushes_partial_evidence_including_at_byte_cap() {
        use tokio::io::AsyncWriteExt;
        for max_bytes in [1000, 13] {
            let (mut writer, reader) = tokio::io::duplex(128);
            writer.write_all(b"one\n  partial").await.unwrap();
            let (tx, mut rx) = mpsc::channel(1);
            let sender = Sender {
                tx,
                metrics: Arc::new(Mutex::new(Metrics::default())),
            };
            let stop = CancellationToken::new();
            let mut task = Box::pin(ingest_with_read_timeout(
                reader,
                Source::new(),
                max_bytes,
                &sender,
                &stop,
                Some(Duration::from_secs(30)),
            ));
            assert!(futures::poll!(&mut task).is_pending());
            tokio::time::advance(Duration::from_secs(31)).await;
            tokio::time::timeout(Duration::from_secs(1), task)
                .await
                .unwrap();
            let event = rx.try_recv().unwrap();
            assert_eq!(event.text, "one\n  partial");
            assert!(event.truncated);
            let m = sender.metrics.lock().unwrap();
            assert_eq!(m.dropped, 0);
            assert!(
                m.coverage
                    .iter()
                    .any(|c| c.status == "error"
                        && c.warnings.iter().any(|w| w.contains("timed out")))
            );
        }
    }
    #[tokio::test]
    async fn cancellation_at_byte_cap_records_a_gap_and_flushes() {
        use tokio::io::AsyncWriteExt;
        let (mut writer, reader) = tokio::io::duplex(128);
        writer.write_all(b"one\n").await.unwrap();
        let (tx, mut rx) = mpsc::channel(1);
        let sender = Sender {
            tx,
            metrics: Arc::new(Mutex::new(Metrics::default())),
        };
        let stop = CancellationToken::new();
        let mut task = Box::pin(ingest(reader, Source::new(), 4, &sender, &stop));
        assert!(futures::poll!(&mut task).is_pending());
        stop.cancel();
        tokio::time::timeout(Duration::from_secs(1), task)
            .await
            .unwrap();
        assert!(rx.try_recv().unwrap().truncated);
        assert!(sender.metrics.lock().unwrap().coverage_gaps > 0);
    }
    #[tokio::test]
    async fn queue_bounds_drops_and_byte_limits() {
        let metrics = Arc::new(Mutex::new(Metrics::default()));
        let (tx, mut rx) = mpsc::channel(1);
        let sender = Sender {
            tx,
            metrics: metrics.clone(),
        };
        let mut p = Parser::new(Source::new());
        p.feed(crate::events::Line {
            bytes: b"ready".to_vec(),
            truncated: false,
            private: false,
        });
        let event = p.flush().unwrap();
        sender.emit(event.clone());
        sender.emit(event);
        assert_eq!(rx.len(), 1);
        assert_eq!(metrics.lock().unwrap().dropped, 1);
        rx.recv().await;
        ingest(
            &b"\n\n\n\n\n"[..],
            Source::new(),
            3,
            &sender,
            &CancellationToken::new(),
        )
        .await;
        assert_eq!(metrics.lock().unwrap().coverage_gaps, 1);
    }
    #[tokio::test]
    async fn retention_and_offline_batching() {
        let metrics = Arc::new(Mutex::new(Metrics::default()));
        let (tx, rx) = mpsc::channel(4);
        let sender = Sender {
            tx,
            metrics: metrics.clone(),
        };
        let stop = CancellationToken::new();
        ingest(
            &b"ERROR failed\nINFO ready\nWARN slow\n"[..],
            Source::new(),
            1000,
            &sender,
            &stop,
        )
        .await;
        drop(sender);
        let opts = AnalyzeOptions {
            batch_size: 2,
            max_batches: 1,
            max_cost: 1.0,
            grouping: Strategy::Exact,
            drain_capacity: crate::drain::DEFAULT_CAPACITY,
            retain: 2,
            max_events: 100,
            live: false,
            print_events: false,
        };
        let (r, _) = analyze(rx, metrics, None, opts, stop).await;
        assert_eq!(r.total, 3);
        assert_eq!(r.events.len(), 2);
        assert_eq!(r.evicted, 1);
        assert_eq!(r.counts["important"], 2);
    }
    #[tokio::test]
    async fn actual_http_grouping_reuses_across_batches_and_preserves_occurrences() {
        let (url, server) =
            crate::test_support::http(vec![(200, crate::test_support::verdict())]).await;
        let metrics = Arc::new(Mutex::new(Metrics::default()));
        let (tx, rx) = mpsc::channel(4);
        let sender = Sender {
            tx,
            metrics: metrics.clone(),
        };
        let stop = CancellationToken::new();
        ingest(
            &b"ERROR payment failed\nERROR payment failed\nERROR payment failed\n"[..],
            Source::new(),
            1000,
            &sender,
            &stop,
        )
        .await;
        drop(sender);
        let opts = AnalyzeOptions {
            batch_size: 2,
            max_batches: 1,
            max_cost: 1.0,
            grouping: Strategy::Exact,
            drain_capacity: crate::drain::DEFAULT_CAPACITY,
            retain: 4,
            max_events: 100,
            live: true,
            print_events: false,
        };
        let (r, client) =
            analyze(rx, metrics, Some(Jev::test_client(url)), opts, stop.clone()).await;
        assert!(
            !stop.is_cancelled(),
            "Cached repeats remain eligible after the batch budget is used"
        );
        assert_eq!(r.batches, 1);
        assert_eq!(r.reused, 2);
        assert_eq!(r.total, 3);
        let ids: std::collections::HashSet<_> = r.events.iter().map(|e| &e.id).collect();
        assert_eq!(ids.len(), 3);
        assert!(
            r.events
                .iter()
                .all(|e| e.judgment.importance == Importance::Important)
        );
        assert_eq!(client.unwrap().usage.input_tokens, 100);
        let requests = server.await.unwrap();
        assert_eq!(requests.len(), 1);
        let body: serde_json::Value =
            serde_json::from_str(requests[0].split("\r\n\r\n").nth(1).unwrap()).unwrap();
        assert_eq!(body["state"]["events"].as_array().unwrap().len(), 1);
        assert_eq!(body["questions"].as_object().unwrap().len(), 3);
    }
    #[tokio::test]
    async fn failed_judgments_retry_next_batch_and_never_claim_reuse() {
        let (url, server) = crate::test_support::http(vec![
            (401, "synthetic private error".into()),
            (200, crate::test_support::verdict()),
        ])
        .await;
        let metrics = Arc::new(Mutex::new(Metrics::default()));
        let (tx, rx) = mpsc::channel(4);
        let sender = Sender {
            tx,
            metrics: metrics.clone(),
        };
        let stop = CancellationToken::new();
        ingest(
            &b"ERROR failed\nERROR failed\nERROR failed\n"[..],
            Source::new(),
            1000,
            &sender,
            &stop,
        )
        .await;
        drop(sender);
        let opts = AnalyzeOptions {
            batch_size: 2,
            max_batches: 2,
            max_cost: 1.0,
            grouping: Strategy::Exact,
            drain_capacity: crate::drain::DEFAULT_CAPACITY,
            retain: 4,
            max_events: 100,
            live: false,
            print_events: false,
        };
        let (r, _) = analyze(rx, metrics, Some(Jev::test_client(url)), opts, stop).await;
        assert_eq!(r.batches, 2);
        assert_eq!(r.reused, 0);
        assert_eq!(r.counts["unknown"], 2);
        assert_eq!(r.counts["important"], 1);
        assert!(
            r.events
                .iter()
                .all(|e| !format!("{:?}", e.judgment).contains("private"))
        );
        assert_eq!(server.await.unwrap().len(), 2);
    }
    #[tokio::test]
    async fn drain_failed_representatives_retry_and_modes_remain_distinct_offline() {
        use crate::drain::tests::{event, verdict};
        for strategy in [Strategy::Drain, Strategy::Exact, Strategy::Off] {
            let metrics = Arc::new(Mutex::new(Metrics::default()));
            let (tx, rx) = mpsc::channel(8);
            for i in 1..=5 {
                tx.send(event(i)).await.unwrap();
            }
            drop(tx);
            let responses = if strategy == Strategy::Drain { 3 } else { 5 };
            let mut outcomes = vec![Ok(verdict()); responses];
            outcomes[1] = Err("synthetic failure".into());
            let mut client = Jev::test_client("unused-offline".into());
            client.synthetic = Some(outcomes.into());
            let opts = AnalyzeOptions {
                batch_size: 1,
                max_batches: 10,
                max_cost: 1.0,
                grouping: strategy,
                drain_capacity: 4,
                retain: 8,
                max_events: 100,
                live: false,
                print_events: false,
            };
            let (r, client) = analyze(
                rx,
                metrics.clone(),
                Some(client),
                opts,
                CancellationToken::new(),
            )
            .await;
            assert!(client.unwrap().synthetic.unwrap().is_empty());
            assert_eq!(r.total, 5);
            assert_eq!(r.batches, responses as u64);
            assert_eq!(r.reused, if strategy == Strategy::Drain { 2 } else { 0 });
            assert_eq!(r.counts["unknown"], 1);
        }
    }
    #[tokio::test]
    async fn drain_batches_independent_representatives_without_speculative_reuse_offline() {
        use crate::drain::tests::{event, verdict};
        for fail_first_batch in [false, true] {
            let metrics = Arc::new(Mutex::new(Metrics::default()));
            let (tx, rx) = mpsc::channel(24);
            let mut observations = Vec::new();
            for i in 1..=24 {
                let mut e = event(i);
                if i % 2 == 0 {
                    e.text = e.text.replace("count=1", "count=2");
                }
                observations.push(e.clone());
                tx.send(e).await.unwrap();
            }
            drop(tx);
            let outcomes: Vec<_> = (1..=8)
                .map(|i| {
                    let mut j = verdict();
                    if i % 2 == 0 {
                        j.importance = Importance::Routine;
                        j.category = crate::jev::Category::Routine;
                    }
                    j
                })
                .collect();
            let mut responses = Vec::new();
            if fail_first_batch {
                responses.push(Err("synthetic failure".into()));
            }
            responses.extend(outcomes.iter().cloned().map(Ok));
            let mut client = Jev::test_client("unused-offline".into());
            client.synthetic = Some(responses.into());
            let (report, client) = analyze(
                rx,
                metrics.clone(),
                Some(client),
                AnalyzeOptions {
                    batch_size: 8,
                    max_batches: 2,
                    max_cost: 1.0,
                    grouping: Strategy::Drain,
                    drain_capacity: 2,
                    retain: 24,
                    max_events: 24,
                    live: false,
                    print_events: false,
                },
                CancellationToken::new(),
            )
            .await;
            let client = client.unwrap();
            assert!(client.synthetic.unwrap().is_empty());
            assert_eq!(client.usage.request_attempts, 0);
            let classified_start = if fail_first_batch { 8 } else { 0 };
            assert_eq!(report.batches, if fail_first_batch { 2 } else { 1 });
            assert_eq!(report.reused, if fail_first_batch { 8 } else { 16 });
            assert_eq!((report.total, report.events.len()), (24, 24));
            for (i, e) in report.events.iter().enumerate() {
                assert_eq!(e.text, observations[i].text);
                if i < classified_start {
                    assert!(e.judgment.analysis_error.is_some());
                } else {
                    assert_eq!(e.judgment.importance, outcomes[i % 8].importance);
                    assert_eq!(e.judgment.category, outcomes[i % 8].category);
                }
                assert_eq!(e.analysis_reused, i >= classified_start + 8);
            }
            let m = metrics.lock().unwrap();
            assert_eq!(m.drain_templates_created, 2);
            assert_eq!(m.drain_templates_changed, 2);
        }
    }
    #[tokio::test]
    async fn drain_clock_batches_retain_all_events_and_isolate_security_offline() {
        use crate::drain::tests::{clock_event, verdict};
        use crate::jev::{Category, Severity};
        for batch_size in [1, 8] {
            let metrics = Arc::new(Mutex::new(Metrics::default()));
            let (tx, rx) = mpsc::channel(26);
            let mut observations: Vec<_> = (1..=26).map(clock_event).collect();
            for (i, message) in [(24, "forbidden"), (25, "error")] {
                let e = &observations[i];
                let mut parser = Parser::new(e.source.clone());
                parser.feed(crate::events::Line {
                    bytes: format!("{} {message}", e.text).into_bytes(),
                    truncated: false,
                    private: false,
                });
                observations[i] = parser.flush().unwrap();
            }
            for e in &observations {
                tx.send(e.clone()).await.unwrap();
            }
            drop(tx);
            let warmup = if batch_size == 1 { 2 } else { 8 };
            let mut routine = verdict();
            routine.importance = Importance::Routine;
            routine.severity = Severity::Info;
            routine.category = Category::Routine;
            let mut security = verdict();
            security.category = Category::Security;
            let mut fraud = verdict();
            fraud.category = Category::Fraud;
            fraud.importance = Importance::Uncertain;
            let mut outcomes = vec![Ok(routine); warmup];
            outcomes.extend([Ok(security), Ok(fraud)]);
            let mut client = Jev::test_client("unused-offline".into());
            client.synthetic = Some(outcomes.into());
            let (report, client) = analyze(
                rx,
                metrics.clone(),
                Some(client),
                AnalyzeOptions {
                    batch_size,
                    max_batches: if batch_size == 1 { 4 } else { 2 },
                    max_cost: 1.0,
                    grouping: Strategy::Drain,
                    drain_capacity: 4,
                    retain: 26,
                    max_events: 26,
                    live: false,
                    print_events: false,
                },
                CancellationToken::new(),
            )
            .await;
            let client = client.unwrap();
            assert!(client.synthetic.unwrap().is_empty());
            assert_eq!(client.usage.request_attempts, 0);
            assert_eq!(client.usage.estimated_cost_usd, 0.0);
            assert_eq!(report.batches, if batch_size == 1 { 4 } else { 2 });
            assert_eq!((report.total, report.events.len()), (26, 26));
            assert_eq!(report.reused, (24 - warmup) as u64);
            for (i, (before, after)) in observations.iter().zip(&report.events).enumerate() {
                assert_eq!(before.text, after.text);
                assert_eq!(before.source, after.source);
                assert_eq!(before.id, after.id);
                assert_eq!(before.timestamp, after.timestamp);
                assert_eq!(before.group_id, after.group_id);
                assert_eq!(after.analysis_reused, (warmup..24).contains(&i));
            }
            assert_eq!(report.events[24].judgment.category, Category::Security);
            assert_eq!(report.events[25].judgment.category, Category::Fraud);
            assert_eq!(report.events[25].judgment.importance, Importance::Uncertain);
            let m = metrics.lock().unwrap();
            assert_eq!(m.drain_templates_created, 3);
            assert_eq!(m.drain_templates_changed, 1);
            assert_eq!(m.drain_fallbacks, 0);
        }
    }
    #[tokio::test]
    async fn exact_reuses_identical_events_but_off_classifies_each_offline() {
        use crate::drain::tests::{event, verdict};
        for strategy in [Strategy::Exact, Strategy::Off] {
            let metrics = Arc::new(Mutex::new(Metrics::default()));
            let (tx, rx) = mpsc::channel(4);
            for _ in 0..3 {
                tx.send(event(1)).await.unwrap();
            }
            drop(tx);
            let mut client = Jev::test_client("unused-offline".into());
            let count = if strategy == Strategy::Exact { 1 } else { 3 };
            client.synthetic = Some(vec![Ok(verdict()); count].into());
            let opts = AnalyzeOptions {
                batch_size: 1,
                max_batches: 10,
                max_cost: 1.0,
                grouping: strategy,
                drain_capacity: 4,
                retain: 4,
                max_events: 10,
                live: false,
                print_events: false,
            };
            let (r, client) =
                analyze(rx, metrics, Some(client), opts, CancellationToken::new()).await;
            assert_eq!(r.batches, count as u64);
            assert_eq!(r.reused, 3 - count as u64);
            assert!(client.unwrap().synthetic.unwrap().is_empty());
        }
    }
    #[tokio::test]
    async fn snapshots_wait_for_queue_capacity_and_cancellation_unblocks() {
        let metrics = Arc::new(Mutex::new(Metrics::default()));
        let (tx, mut rx) = mpsc::channel(1);
        let sender = Sender { tx, metrics };
        let stop = CancellationToken::new();
        let task = tokio::spawn({
            let sender = sender.clone();
            let stop = stop.clone();
            async move {
                ingest(
                    &b"one\ntwo\nthree\n"[..],
                    Source::new(),
                    100,
                    &sender,
                    &stop,
                )
                .await;
            }
        });
        tokio::time::sleep(Duration::from_millis(20)).await;
        assert!(!task.is_finished());
        assert_eq!(rx.len(), 1);
        assert_eq!(sender.metrics.lock().unwrap().dropped, 0);
        assert!(rx.recv().await.is_some());
        stop.cancel();
        tokio::time::timeout(Duration::from_secs(1), task)
            .await
            .unwrap()
            .unwrap();
    }
}
