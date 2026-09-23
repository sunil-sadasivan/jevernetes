//! One bounded analysis lane: batching, exact reuse, retention, and explicit loss counters.
use crate::{
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
    pub grouping: bool,
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
        let mut references = HashMap::new();
        let mut duplicate_of = HashMap::new();
        for (i, event) in batch.iter_mut().enumerate() {
            if let Some(c) = &controller {
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
            if opts.grouping && !event.truncated {
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
                let events: Vec<_> = owned.iter().map(|&i| batch[i].clone()).collect();
                client.judge(&events, &stop).await
            };
            match result {
                Ok(judgments) => {
                    for (&i, judgment) in owned.iter().zip(judgments) {
                        batch[i].judgment = judgment.conservative(batch[i].truncated);
                        if opts.grouping && controller.is_none() {
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
                && c.apply(&event).await.is_err()
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
            grouping: true,
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
            grouping: true,
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
            grouping: true,
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
