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
        tokio::select! { biased; _=stop.cancelled()=>false, result=self.tx.reserve()=>{
            if let Ok(permit)=result {permit.send(event);let mut m=self.metrics.lock().expect("metrics lock");m.received+=1;m.queue_depth=self.tx.max_capacity()-self.tx.capacity();m.queue_high_water=m.queue_high_water.max(m.queue_depth);true}else{false}
        }}
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
    mut rx: mpsc::Receiver<Event>,
    sender_metrics: SharedMetrics,
    mut client: Option<Jev>,
    opts: AnalyzeOptions,
    stop: CancellationToken,
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
            if client.is_none() {
                event.judgment.importance = if event.baseline.important {
                    Importance::Important
                } else {
                    Importance::Uncertain
                };
                continue;
            }
            if opts.grouping && !event.truncated {
                if let Some((judgment, id)) = cache.get(event, Instant::now()) {
                    event.judgment = judgment;
                    event.analysis_reused = true;
                    event.analysis_representative_id = Some(id);
                    continue;
                }
                if let Some(index) = references.get(&event.group_id) {
                    duplicate_of.insert(i, *index);
                    continue;
                }
                references.insert(event.group_id.clone(), i);
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
                        if opts.grouping {
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
        for event in batch {
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
    mut reader: R,
    source: Source,
    max_bytes: u64,
    sender: &Sender,
    stop: &CancellationToken,
) {
    let mut parser = Parser::new(source.clone());
    let mut framer = Framer::default();
    let mut read = 0;
    let mut buffer = [0; 8192];
    loop {
        let want = ((max_bytes - read).min(buffer.len() as u64)) as usize;
        if want == 0 {
            let mut probe = [0];
            let more = tokio::select! {_=stop.cancelled()=>false,r=reader.read(&mut probe)=>!matches!(r,Ok(0))};
            if more {
                gap(
                    &sender.metrics,
                    &source,
                    "limited",
                    "Decompressed input byte limit reached",
                );
            }
            break;
        }
        let n = tokio::select! {_=stop.cancelled()=>{gap(&sender.metrics,&source,"limited","Collection stopped before end of input");break;},r=reader.read(&mut buffer[..want])=>match r{Ok(n)=>n,Err(_)=>{gap(&sender.metrics,&source,"error","Input read failed");break;}}};
        if n == 0 {
            break;
        }
        read += n as u64;
        for line in framer.push(&buffer[..n]) {
            if let Some(event) = parser.feed(line)
                && !sender.send(event, stop).await
            {
                return;
            }
        }
    }
    if let Some(mut line) = framer.finish() {
        line.truncated |= read == max_bytes || stop.is_cancelled();
        if let Some(event) = parser.feed(line) {
            if stop.is_cancelled() {
                sender.emit(event);
            } else {
                let _ = sender.send(event, stop).await;
            }
        }
    }
    if let Some(mut event) = parser.flush() {
        if stop.is_cancelled() {
            event.truncated = true;
            event.group_id = crate::grouping::group_id(&event);
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
