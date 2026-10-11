//! Read-only paginated inventory, API watch discovery and bounded log streams.
use crate::{
    events::{Framer, Line, Parser, Source, hash},
    report::gap,
    runtime::{Sender, ingest_with_read_timeout},
};
use chrono::{DateTime, Utc};
use futures::StreamExt;
use k8s_openapi::api::core::v1::Pod;
use kube::{
    Api, Client, Config,
    api::{ListParams, LogParams},
    config::KubeConfigOptions,
    runtime::watcher,
};
use sha2::{Digest, Sha256};
use std::{
    collections::{HashMap, HashSet},
    time::Duration,
};
use tokio::{io::AsyncReadExt, task::JoinHandle};
use tokio_util::{compat::FuturesAsyncReadCompatExt, sync::CancellationToken};
#[derive(Clone)]
pub struct Options {
    pub context: Option<String>,
    pub namespace: Option<String>,
    pub selector: Option<String>,
    pub since: i64,
    pub tail: i64,
    pub max_bytes: u64,
    pub max_streams: usize,
    pub previous: bool,
    pub in_cluster: bool,
}
#[derive(Clone, Debug)]
pub struct Target {
    pub source: Source,
    pub namespace: String,
    pub pod: String,
    pub uid: String,
    pub container: String,
    pub restart: i32,
    pub previous: bool,
}
impl Target {
    fn id(&self) -> String {
        hash(&self.source)
    }
}
pub fn targets(pod: &Pod, cluster: &str, live: bool, previous: bool) -> Vec<Target> {
    let Some(uid) = pod.metadata.uid.as_ref() else {
        return vec![];
    };
    let Some(name) = pod.metadata.name.as_ref() else {
        return vec![];
    };
    let Some(namespace) = pod.metadata.namespace.as_ref() else {
        return vec![];
    };
    let Some(spec) = pod.spec.as_ref() else {
        return vec![];
    };
    let status = pod.status.clone().unwrap_or_default();
    let mut out = vec![];
    let groups = [
        (
            "container",
            spec.containers
                .iter()
                .map(|c| c.name.clone())
                .collect::<Vec<_>>(),
            status.container_statuses.unwrap_or_default(),
        ),
        (
            "init",
            spec.init_containers
                .as_deref()
                .unwrap_or_default()
                .iter()
                .map(|c| c.name.clone())
                .collect(),
            status.init_container_statuses.unwrap_or_default(),
        ),
        (
            "ephemeral",
            spec.ephemeral_containers
                .as_deref()
                .unwrap_or_default()
                .iter()
                .map(|c| c.name.clone())
                .collect(),
            status.ephemeral_container_statuses.unwrap_or_default(),
        ),
    ];
    for (kind, names, statuses) in groups {
        for container in names {
            let s = statuses.iter().find(|s| s.name == container);
            let restart = s.map_or(0, |s| s.restart_count);
            if live && s.is_none_or(|s| s.state.as_ref().is_none_or(|s| s.running.is_none())) {
                continue;
            }
            for prev in [false, true] {
                if prev && (live || !previous || restart == 0) {
                    continue;
                }
                let source:Source=serde_json::from_value(serde_json::json!({"type":"kubernetes","context":cluster,"namespace":namespace,"pod":name,"pod_uid":uid,"container":container,"kind":kind,"restart_count":restart-i32::from(prev),"previous":prev})).expect("source");
                out.push(Target {
                    source,
                    namespace: namespace.clone(),
                    pod: name.clone(),
                    uid: uid.clone(),
                    container: container.clone(),
                    restart: restart - i32::from(prev),
                    previous: prev,
                });
            }
        }
    }
    out
}
pub async fn connect(options: &Options) -> Result<(Client, String), &'static str> {
    let config = async {
        if options.in_cluster {
            Config::incluster().map_err(|_| "Cannot load in-cluster Kubernetes configuration")
        } else if options.context.is_some() {
            Config::from_kubeconfig(&KubeConfigOptions {
                context: options.context.clone(),
                ..Default::default()
            })
            .await
            .map_err(|_| "Cannot load selected Kubernetes configuration")
        } else {
            Config::infer()
                .await
                .map_err(|_| "Cannot load Kubernetes configuration")
        }
    };
    let mut config = tokio::time::timeout(Duration::from_secs(15), config)
        .await
        .map_err(|_| "Kubernetes configuration timed out")??;
    let cluster = format!(
        "{}:{}",
        options.context.as_deref().unwrap_or(if options.in_cluster {
            "in-cluster"
        } else {
            "inferred"
        }),
        hash(&config.cluster_url.to_string())
    );
    config.connect_timeout = Some(Duration::from_secs(10));
    config.read_timeout = None;
    let client = Client::try_from(config).map_err(|_| "Cannot initialize Kubernetes TLS client")?;
    Ok((client, cluster))
}
fn api(client: Client, namespace: &Option<String>) -> Api<Pod> {
    match namespace {
        Some(ns) => Api::namespaced(client, ns),
        None => Api::all(client),
    }
}
fn params(options: &Options, target: &Target, follow: bool) -> LogParams {
    LogParams {
        container: Some(target.container.clone()),
        follow,
        previous: target.previous,
        timestamps: true,
        since_seconds: Some(options.since),
        tail_lines: Some(options.tail),
        ..Default::default()
    }
}
async fn same_instance(api: &Api<Pod>, target: &Target) -> bool {
    let Ok(Ok(pod)) = tokio::time::timeout(Duration::from_secs(15), api.get(&target.pod)).await
    else {
        return false;
    };
    targets(&pod, "", false, true).iter().any(|t| {
        t.uid == target.uid
            && t.container == target.container
            && t.restart == target.restart
            && t.previous == target.previous
    })
}
pub async fn snapshot(
    client: Client,
    cluster: String,
    options: Options,
    sender: Sender,
    stop: CancellationToken,
) {
    let pods = api(client.clone(), &options.namespace);
    let mut lp = ListParams::default().limit(50);
    if let Some(s) = &options.selector {
        lp = lp.labels(s);
    }
    loop {
        let page = tokio::select! {_=stop.cancelled()=>break,r=tokio::time::timeout(Duration::from_secs(30),pods.list(&lp))=>match r{Ok(Ok(p))=>p,_=>{gap(&sender.metrics,&Source::new(),"error","Kubernetes inventory unavailable; check connectivity and read-only RBAC");break;}}};
        for pod in page.items {
            for target in targets(&pod, &cluster, false, options.previous) {
                if stop.is_cancelled() {
                    return;
                }
                let api = Api::<Pod>::namespaced(client.clone(), &target.namespace);
                let valid = tokio::select! {
                    _ = stop.cancelled() => return,
                    valid = same_instance(&api, &target) => valid,
                };
                if !valid {
                    gap(
                        &sender.metrics,
                        &target.source,
                        "error",
                        "Container identity unavailable or changed before log read",
                    );
                    continue;
                }
                sender.metrics.lock().expect("metrics lock").streams_started += 1;
                // Client-side byte cap preserves the ability to detect excess input.
                let lp = params(&options, &target, false);
                let result = tokio::select! {
                    _ = stop.cancelled() => {
                        gap(&sender.metrics, &target.source, "limited", "Snapshot stopped during log read");
                        return;
                    }
                    result = tokio::time::timeout(Duration::from_secs(30), api.log_stream(&target.pod, &lp)) => result,
                };
                if let Ok(Ok(reader)) = result {
                    // Ingest owns cancellation and flushing. Do not cancel its queue waits
                    // from an outer transport timeout (or a second cancellation select).
                    ingest_with_read_timeout(
                        reader.compat(),
                        target.source.clone(),
                        options.max_bytes,
                        &sender,
                        &stop,
                        Some(Duration::from_secs(30)),
                    )
                    .await;
                } else {
                    gap(
                        &sender.metrics,
                        &target.source,
                        "error",
                        "Kubernetes log read failed or timed out",
                    );
                }
                // Even successful Kubernetes reads cannot certify retention or tail completeness.
                gap(
                    &sender.metrics,
                    &target.source,
                    "limited",
                    "Kubernetes history is restricted by since/tail and server retention",
                );
            }
        }
        let token = page.metadata.continue_.unwrap_or_default();
        if token.is_empty() {
            break;
        }
        lp = lp.continue_token(&token);
    }
}
/// Inclusive timestamp replay counts preserve legitimate repeated identical occurrences.
/// Overflow favors extra evidence over silently dropping unknown occurrences.
pub struct Cursor {
    pub timestamp: Option<String>,
    at: Option<DateTime<Utc>>,
    counts: HashMap<[u8; 32], u64>,
    replay: HashMap<[u8; 32], u64>,
    limit: usize,
    pub overflows: u64,
    pub unstamped: u64,
}
impl Cursor {
    pub fn new(limit: usize) -> Self {
        Self {
            timestamp: None,
            at: None,
            counts: HashMap::new(),
            replay: HashMap::new(),
            limit,
            overflows: 0,
            unstamped: 0,
        }
    }
    pub fn reconnect(&mut self) {
        self.replay = self.counts.clone();
    }
    pub fn accept(&mut self, line: &Line) -> bool {
        let raw = String::from_utf8_lossy(&line.bytes);
        let stamp = raw.split_whitespace().next().unwrap_or("");
        let Ok(at) = DateTime::parse_from_rfc3339(stamp).map(|d| d.with_timezone(&Utc)) else {
            self.unstamped += 1;
            return true;
        };
        if self.at.is_some_and(|last| at < last) {
            return false;
        }
        if self.at.is_none_or(|last| at > last) {
            self.at = Some(at);
            self.timestamp = Some(stamp.into());
            self.counts.clear();
            self.replay.clear();
        }
        if line.truncated {
            self.overflows += 1;
            return true;
        }
        let key: [u8; 32] = Sha256::digest(&line.bytes).into();
        if let Some(n) = self.replay.get_mut(&key)
            && *n > 0
        {
            *n -= 1;
            return false;
        }
        if !self.counts.contains_key(&key) && self.counts.len() >= self.limit {
            self.overflows += 1;
            return true;
        }
        let count = self.counts.entry(key).or_default();
        *count = count.saturating_add(1);
        true
    }
}
fn feed_line(line: Line, cursor: &mut Cursor, parser: &mut Parser, sender: &Sender) {
    let before = cursor.overflows;
    let unstamped = cursor.unstamped;
    if cursor.accept(&line) {
        if let Some(event) = parser.feed(line) {
            sender.emit(event);
        }
    } else {
        sender.metrics.lock().expect("metrics lock").duplicates += 1;
    }
    let mut m = sender.metrics.lock().expect("metrics lock");
    m.dedup_overflows += cursor.overflows - before;
    m.untimestamped_lines += cursor.unstamped - unstamped;
}
async fn follow(
    client: Client,
    target: Target,
    options: Options,
    sender: Sender,
    stop: CancellationToken,
) {
    let api = Api::<Pod>::namespaced(client, &target.namespace);
    let mut cursor = Cursor::new(4096);
    let mut parser = Parser::new(target.source.clone());
    let mut framer = Framer::default();
    let mut attempt = 0;
    while !stop.is_cancelled() {
        let valid = tokio::select! {_=stop.cancelled()=>break,v=same_instance(&api,&target)=>v};
        if !valid {
            gap(
                &sender.metrics,
                &target.source,
                "error",
                "Container identity unavailable or changed; retrying validation while awaiting discovery",
            );
            attempt = (attempt + 1).min(5);
            tokio::select! { _ = stop.cancelled() => break, _ = tokio::time::sleep(Duration::from_secs(1 << attempt)) => () }
            continue;
        }
        let mut lp = params(&options, &target, true);
        if let Some(stamp) = cursor.at {
            // kube-core rounds sinceTime to the nearest second. Floor only the
            // request so subsecond evidence is replayed; keep the precise cursor.
            lp.since_time = stamp
                .to_rfc3339_opts(chrono::SecondsFormat::Secs, true)
                .parse()
                .ok();
            lp.since_seconds = None;
            lp.tail_lines = None;
        }
        cursor.reconnect();
        let result = tokio::select! {_=stop.cancelled()=>break,r=tokio::time::timeout(Duration::from_secs(30),api.log_stream(&target.pod,&lp))=>r};
        if let Ok(Ok(reader)) = result {
            let mut reader = reader.compat();
            let mut buffer = [0; 8192];
            loop {
                tokio::select! {
                    _=stop.cancelled()=>break,
                    _=tokio::time::sleep(Duration::from_millis(500)),if parser.has_pending()=>{if let Some(e)=parser.flush(){sender.emit(e);}},
                    read=reader.read(&mut buffer)=>match read{
                        Ok(0)|Err(_)=>break,
                        Ok(n)=>{attempt=0;for line in framer.push(&buffer[..n]){feed_line(line,&mut cursor,&mut parser,&sender);}}
                    }
                }
            }
        }
        if let Some(mut line) = framer.finish() {
            line.truncated = true;
            feed_line(line, &mut cursor, &mut parser, &sender);
        }
        if let Some(e) = parser.flush() {
            sender.emit(e);
        }
        if stop.is_cancelled() {
            break;
        }
        gap(
            &sender.metrics,
            &target.source,
            "limited",
            "Log stream disconnected; reconnecting, rotation or disconnect may leave gaps",
        );
        sender.metrics.lock().expect("metrics lock").reconnects += 1;
        attempt = (attempt + 1).min(5);
        tokio::select! {_=stop.cancelled()=>break,_=tokio::time::sleep(Duration::from_secs(1<<attempt))=>()}
    }
    if let Some(e) = parser.flush() {
        sender.emit(e);
    }
}
struct Active {
    uid: String,
    pod: String,
    namespace: String,
    token: CancellationToken,
    task: JoinHandle<()>,
    seen: bool,
}
async fn retire(active: &mut HashMap<String, Active>, ids: Vec<String>) {
    for id in ids {
        if let Some(a) = active.remove(&id) {
            a.token.cancel();
            let _ = a.task.await;
        }
    }
}
pub async fn live(
    client: Client,
    cluster: String,
    options: Options,
    sender: Sender,
    stop: CancellationToken,
) {
    let pods = api(client.clone(), &options.namespace);
    let mut active: HashMap<String, Active> = HashMap::new();
    let mut config = watcher::Config::default().page_size(50).timeout(290);
    if let Some(s) = &options.selector {
        config = config.labels(s);
    }
    let mut stream = watcher(pods.clone(), config.clone()).boxed();
    // Periodic paginated resync admits previously omitted streams without retaining all pods.
    let mut resync = tokio::time::interval_at(
        tokio::time::Instant::now() + Duration::from_secs(300),
        Duration::from_secs(300),
    );
    let mut overflow = false;
    let mut initializing = true;
    loop {
        let next = tokio::select! {
            _ = stop.cancelled() => break,
            _ = resync.tick() => {
                initializing = true;
                stream = watcher(pods.clone(), config.clone()).boxed();
                continue;
            }
            _ = tokio::time::sleep(Duration::from_secs(30)), if initializing => {
                gap(&sender.metrics, &Source::new(), "error", "Kubernetes inventory stalled; restarting discovery");
                stream = watcher(pods.clone(), config.clone()).boxed();
                continue;
            }
            event = stream.next() => event,
        };
        match next {
            Some(Ok(watcher::Event::Init)) => {
                initializing = true;
                for a in active.values_mut() {
                    a.seen = false;
                }
                overflow = false;
            }
            Some(Ok(watcher::Event::InitDone)) => {
                initializing = false;
                let ids = active
                    .iter()
                    .filter(|(_, a)| !a.seen)
                    .map(|(id, _)| id.clone())
                    .collect();
                retire(&mut active, ids).await;
            }
            Some(Ok(watcher::Event::Delete(pod))) => {
                let ids = active
                    .iter()
                    .filter(|(_, a)| Some(&a.uid) == pod.metadata.uid.as_ref())
                    .map(|(id, _)| id.clone())
                    .collect();
                retire(&mut active, ids).await;
            }
            Some(Ok(watcher::Event::Apply(pod) | watcher::Event::InitApply(pod))) => {
                let wanted = targets(&pod, &cluster, true, false);
                let wanted_ids: HashSet<_> = wanted.iter().map(Target::id).collect();
                let ids = active
                    .iter()
                    .filter(|(id, a)| {
                        a.task.is_finished()
                            || (Some(&a.pod) == pod.metadata.name.as_ref()
                                && Some(&a.namespace) == pod.metadata.namespace.as_ref()
                                && Some(&a.uid) != pod.metadata.uid.as_ref())
                            || (Some(&a.uid) == pod.metadata.uid.as_ref()
                                && !wanted_ids.contains(*id))
                    })
                    .map(|(id, _)| id.clone())
                    .collect();
                retire(&mut active, ids).await;
                for target in wanted {
                    let id = target.id();
                    if let Some(a) = active.get_mut(&id) {
                        a.seen = true;
                        continue;
                    }
                    if active.len() >= options.max_streams {
                        sender
                            .metrics
                            .lock()
                            .expect("metrics lock")
                            .unfollowed_observations += 1;
                        if !overflow {
                            gap(
                                &sender.metrics,
                                &Source::new(),
                                "limited",
                                "Running containers exceed stream limit; omitted streams reconsidered on updates or five-minute resync",
                            );
                            overflow = true;
                        }
                        continue;
                    }
                    gap(
                        &sender.metrics,
                        &target.source,
                        "limited",
                        "Live initial history is restricted by since/tail; no durable replay guarantee",
                    );
                    sender.metrics.lock().expect("metrics lock").streams_started += 1;
                    let token = stop.child_token();
                    let uid = target.uid.clone();
                    let pod = target.pod.clone();
                    let namespace = target.namespace.clone();
                    let task = tokio::spawn(follow(
                        client.clone(),
                        target,
                        options.clone(),
                        sender.clone(),
                        token.clone(),
                    ));
                    active.insert(
                        id,
                        Active {
                            uid,
                            pod,
                            namespace,
                            token,
                            task,
                            seen: true,
                        },
                    );
                }
            }
            Some(Err(_)) | None => {
                gap(
                    &sender.metrics,
                    &Source::new(),
                    "error",
                    "Kubernetes discovery interrupted; retrying with backoff",
                );
                tokio::select! {_=stop.cancelled()=>break,_=tokio::time::sleep(Duration::from_secs(5))=>()};
                if next.is_none() {
                    stream = watcher(pods.clone(), config.clone()).boxed();
                }
            }
        }
        sender.metrics.lock().expect("metrics lock").active_streams = active.len();
    }
    let ids = active.keys().cloned().collect();
    retire(&mut active, ids).await;
    sender.metrics.lock().expect("metrics lock").active_streams = 0;
}
#[cfg(test)]
mod tests {
    use super::*;
    fn line(s: &str) -> Line {
        Line {
            bytes: s.as_bytes().to_vec(),
            truncated: false,
            private: false,
        }
    }
    #[test]
    fn reconnect_preserves_multiplicity_and_is_bounded() {
        let mut c = Cursor::new(2);
        let a = line("2026-09-20T12:00:00Z same");
        assert!(c.accept(&a));
        assert!(c.accept(&a));
        c.reconnect();
        assert!(!c.accept(&a));
        assert!(!c.accept(&a));
        assert!(c.accept(&a));
        assert!(c.accept(&line("2026-09-20T12:00:00Z other")));
        assert!(c.accept(&line("2026-09-20T12:00:00Z overflow")));
        assert_eq!(c.counts.len(), 2);
        assert_eq!(c.overflows, 1);
        assert!(c.accept(&line("2026-09-20T12:00:00.000000001Z new")));
        assert!(!c.accept(&a));
        assert!(c.accept(&line("without timestamp")));
        assert_eq!(c.unstamped, 1);
    }
    #[test]
    fn discovery_tracks_uid_restart_previous_and_container_kinds() {
        let pod:Pod=serde_json::from_value(serde_json::json!({"metadata":{"name":"synthetic","namespace":"test","uid":"uid1"},"spec":{"containers":[{"name":"app"}],"initContainers":[{"name":"init"}],"ephemeralContainers":[{"name":"debug"}]},"status":{"containerStatuses":[{"name":"app","restartCount":2,"image":"test","imageID":"test","ready":true,"state":{"running":{}}}]}})).unwrap();
        let all = targets(&pod, "cluster", false, true);
        assert_eq!(all.len(), 4);
        let live = targets(&pod, "cluster", true, false);
        assert_eq!(live.len(), 1);
        assert_eq!(live[0].restart, 2);
        let mut changed = pod;
        changed.metadata.uid = Some("uid2".into());
        assert_ne!(
            live[0].id(),
            targets(&changed, "cluster", true, false)[0].id()
        );
    }
    fn options() -> Options {
        Options {
            context: None,
            namespace: Some("test".into()),
            selector: Some("app=synthetic".into()),
            since: 3600,
            tail: 100,
            max_bytes: 1024,
            max_streams: 1,
            previous: true,
            in_cluster: false,
        }
    }
    fn sender() -> (Sender, tokio::sync::mpsc::Receiver<crate::events::Event>) {
        let (tx, rx) = tokio::sync::mpsc::channel(16);
        (
            Sender {
                tx,
                metrics: std::sync::Arc::new(std::sync::Mutex::new(
                    crate::report::Metrics::default(),
                )),
            },
            rx,
        )
    }
    fn client(url: String) -> Client {
        Client::try_from(Config::new(url.parse().unwrap())).unwrap()
    }
    #[tokio::test]
    async fn snapshot_http_contract_is_read_only_and_scoped() {
        let pod = crate::test_support::pod();
        let list=serde_json::json!({"apiVersion":"v1","kind":"PodList","metadata":{"resourceVersion":"1"},"items":[pod.clone()]}).to_string();
        let (url, server) = crate::test_support::http(vec![
            (200, list),
            (200, pod.to_string()),
            (200, "2026-09-20T12:00:00Z ERROR synthetic\n".into()),
        ])
        .await;
        let (sender, mut rx) = sender();
        let metrics = sender.metrics.clone();
        snapshot(
            client(url),
            "synthetic".into(),
            options(),
            sender,
            CancellationToken::new(),
        )
        .await;
        assert_eq!(rx.recv().await.unwrap().text, "ERROR synthetic");
        assert!(rx.recv().await.is_none());
        let requests = server.await.unwrap();
        assert!(requests.iter().all(|r| r.starts_with("GET ")));
        assert!(requests[0].contains("/api/v1/namespaces/test/pods?"));
        assert!(requests[0].contains("limit=50"));
        assert!(requests[0].contains("labelSelector=app%3Dsynthetic"));
        assert!(requests[2].contains("/pods/synthetic/log?"));
        assert!(requests[2].contains("timestamps=true"));
        assert!(requests[2].contains("container=app"));
        assert!(
            requests
                .iter()
                .all(|r| !r.to_lowercase().contains("authorization:"))
        );
        assert_eq!(metrics.lock().unwrap().streams_started, 1);
    }
    #[tokio::test]
    async fn live_http_reconnect_deduplicates_and_shutdown_joins() {
        let pod = crate::test_support::pod();
        let parsed: Pod = serde_json::from_value(pod.clone()).unwrap();
        let target = targets(&parsed, "synthetic", true, false).remove(0);
        let old = "2026-09-20T12:00:00.750Z repeated\n";
        let earlier = "2026-09-20T12:00:00.500Z already seen\n";
        let new = "2026-09-20T12:00:00.800Z next\n";
        let (url, server) = crate::test_support::http(vec![
            (200, pod.to_string()),
            (200, format!("{old}{old}")),
            (200, pod.to_string()),
            (200, format!("{earlier}{old}{old}{old}{new}")),
        ])
        .await;
        let (sender, mut rx) = sender();
        let metrics = sender.metrics.clone();
        let stop = CancellationToken::new();
        let task = tokio::spawn(follow(client(url), target, options(), sender, stop.clone()));
        let requests = tokio::time::timeout(Duration::from_secs(8), server)
            .await
            .unwrap()
            .unwrap();
        let mut events = vec![];
        for _ in 0..4 {
            events.push(
                tokio::time::timeout(Duration::from_secs(2), rx.recv())
                    .await
                    .unwrap()
                    .unwrap(),
            );
        }
        stop.cancel();
        tokio::time::timeout(Duration::from_secs(1), task)
            .await
            .unwrap()
            .unwrap();
        assert_eq!(events.iter().filter(|e| e.text == "repeated").count(), 3);
        assert_eq!(events.iter().filter(|e| e.text == "next").count(), 1);
        assert_eq!(metrics.lock().unwrap().duplicates, 3);
        assert!(
            requests[3].contains("sinceTime=2026-09-20T12%3A00%3A00Z"),
            "{}",
            requests[3]
        );
        assert!(!requests[3].contains("sinceSeconds="));
        assert!(!requests[3].contains("tailLines="));
    }
    #[tokio::test]
    async fn snapshot_backpressure_does_not_expire_transport_deadline() {
        snapshot_with_full_queue(false).await;
    }
    #[tokio::test]
    async fn snapshot_cancellation_accounts_for_buffered_evidence() {
        snapshot_with_full_queue(true).await;
    }
    async fn snapshot_with_full_queue(cancel: bool) {
        let pod = crate::test_support::pod();
        let list = serde_json::json!({"apiVersion":"v1", "kind":"PodList",
            "metadata":{}, "items":[pod.clone()]})
        .to_string();
        let (url, server) = crate::test_support::http(vec![
            (200, list),
            (200, pod.to_string()),
            (200, "one\ntwo\nthree\n".into()),
        ])
        .await;
        let (mut sender, _) = sender();
        let (tx, mut rx) = tokio::sync::mpsc::channel(1);
        sender.tx = tx;
        let metrics = sender.metrics.clone();
        let stop = CancellationToken::new();
        let task = tokio::spawn(snapshot(
            client(url),
            "synthetic".into(),
            options(),
            sender,
            stop.clone(),
        ));
        server.await.unwrap();
        tokio::time::timeout(Duration::from_secs(2), async {
            while rx.is_empty() {
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
        if cancel {
            stop.cancel();
            tokio::time::timeout(Duration::from_secs(1), task)
                .await
                .unwrap()
                .unwrap();
            let m = metrics.lock().unwrap();
            assert_eq!(m.received, 3);
            assert_eq!(m.dropped, 2);
            assert!(
                m.coverage
                    .iter()
                    .any(|c| c.warnings.iter().any(|w| w.contains("queued evidence")))
            );
            return;
        }
        tokio::time::pause();
        tokio::time::advance(Duration::from_secs(31)).await;
        tokio::task::yield_now().await;
        assert!(
            !task.is_finished(),
            "healthy queue pressure must not cancel collection"
        );
        tokio::time::resume();
        let mut texts = Vec::new();
        while let Some(event) = tokio::time::timeout(Duration::from_secs(2), rx.recv())
            .await
            .unwrap()
        {
            texts.push(event.text);
        }
        task.await.unwrap();
        assert_eq!(texts, ["one", "two", "three"]);
        let m = metrics.lock().unwrap();
        assert_eq!(m.dropped, 0);
        assert_eq!(m.queue_high_water, 1);
        assert!(m.coverage.iter().all(|c| c.status != "error"));
    }
    #[tokio::test]
    async fn watch_discovery_starts_bounded_streams_and_cancels() {
        use tokio::io::{AsyncReadExt, AsyncWriteExt};
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}", listener.local_addr().unwrap());
        let finished = CancellationToken::new();
        let server_stop = finished.clone();
        let server = tokio::spawn(async move {
            let mut tasks = tokio::task::JoinSet::new();
            loop {
                tokio::select! {
                    _ = server_stop.cancelled() => break,
                    accepted = listener.accept() => {
                        let (mut socket, _) = accepted.unwrap();
                        let stop = server_stop.clone();
                        tasks.spawn(async move {
                            let mut buffer = [0; 8192];
                            let n = socket.read(&mut buffer).await.unwrap();
                            let request = String::from_utf8_lossy(&buffer[..n]);
                            assert!(request.starts_with("GET "));
                            let pod = crate::test_support::pod();
                            if request.contains("watch=true") {
                                // A quiet watch must not cause a polling loop.
                                stop.cancelled().await;
                                return;
                            }
                            let body = if request.contains("/log?") {
                                String::from("2026-09-20T12:00:00Z ERROR observed\n")
                            } else if request.contains("/pods?") {
                                let mut second = pod.clone();
                                second["metadata"]["name"] = serde_json::json!("omitted");
                                second["metadata"]["uid"] = serde_json::json!("uid2");
                                serde_json::json!({"apiVersion":"v1", "kind":"PodList",
                                    "metadata":{"resourceVersion":"1"}, "items":[pod, second]}).to_string()
                            } else { pod.to_string() };
                            let response = format!("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}", body.len());
                            let _ = socket.write_all(response.as_bytes()).await;
                        });
                    }
                }
            }
            tasks.abort_all();
        });
        let (sender, mut rx) = sender();
        let metrics = sender.metrics.clone();
        let stop = CancellationToken::new();
        let task = tokio::spawn(live(
            client(url),
            "synthetic".into(),
            options(),
            sender,
            stop.clone(),
        ));
        let event = tokio::time::timeout(Duration::from_secs(3), rx.recv())
            .await
            .unwrap()
            .unwrap();
        assert_eq!(event.text, "ERROR observed");
        stop.cancel();
        tokio::time::timeout(Duration::from_secs(2), task)
            .await
            .unwrap()
            .unwrap();
        finished.cancel();
        server.await.unwrap();
        let m = metrics.lock().unwrap();
        assert_eq!(m.streams_started, 1);
        assert_eq!(m.active_streams, 0);
        assert!(
            m.coverage
                .iter()
                .any(|c| c.warnings.iter().any(|w| w.contains("stream limit")))
        );
    }
}
