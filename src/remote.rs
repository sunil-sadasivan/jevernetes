//! Kubernetes-authenticated inspection of an existing controller; no local analysis.
use crate::{
    controller::inspect::{Detail, MAX_RESPONSE, RECENT_LIMIT, Snapshot},
    events::console,
    kubernetes::{self, Options},
};
use k8s_openapi::api::core::v1::Pod;
use kube::{
    Api,
    api::{ListParams, Portforwarder},
};
use serde_json::Value;
use std::{fmt::Write as _, time::Duration};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};
use tokio_util::sync::CancellationToken;

#[derive(clap::Args)]
pub struct Args {
    #[arg(long)]
    pub context: Option<String>,
    #[arg(long)]
    pub namespace: String,
    /// Exact controller pod. Without this, select one running app=jevernetes pod.
    #[arg(long, conflicts_with = "selector")]
    pub pod: Option<String>,
    #[arg(long)]
    pub selector: Option<String>,
    #[arg(long,default_value="9091",value_parser=clap::value_parser!(u16).range(1..))]
    pub port: u16,
    /// Show retained evidence, judgment and delivery state for a 64-character incident ID.
    #[arg(long,value_parser=incident_id)]
    pub incident: Option<String>,
    #[arg(long)]
    pub watch: bool,
    #[arg(long,default_value="5",value_parser=clap::value_parser!(u64).range(2..=3600))]
    pub interval: u64,
}

fn incident_id(value: &str) -> Result<String, String> {
    if crate::controller::inspect::valid_id(value) {
        Ok(value.to_ascii_lowercase())
    } else {
        Err("Incident ID must contain exactly 64 hexadecimal characters".into())
    }
}

struct Forward(Portforwarder);
impl Drop for Forward {
    fn drop(&mut self) {
        self.0.abort();
    }
}

async fn request(
    stream: &mut (impl AsyncRead + AsyncWrite + Unpin),
    path: &str,
) -> Result<Value, &'static str> {
    stream
        .write_all(
            format!("GET {path} HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
                .as_bytes(),
        )
        .await
        .map_err(|_| "Cannot send controller inspection request")?;
    let mut bytes = Vec::new();
    stream
        .take((MAX_RESPONSE + 8193) as u64)
        .read_to_end(&mut bytes)
        .await
        .map_err(|_| "Cannot read controller inspection response")?;
    decode(&bytes)
}

fn decode(bytes: &[u8]) -> Result<Value, &'static str> {
    let offset = bytes
        .windows(4)
        .position(|w| w == b"\r\n\r\n")
        .filter(|n| *n <= 8192)
        .ok_or("Invalid controller HTTP response")?;
    let header =
        std::str::from_utf8(&bytes[..offset]).map_err(|_| "Invalid controller HTTP headers")?;
    let first = header.lines().next().unwrap_or("");
    let status = first.split_whitespace().nth(1);
    match status {
        Some("200") => (),
        Some("404") => {
            return Err(
                "Inspection endpoint or incident not found; check controller version and incident ID",
            );
        }
        Some("503") => return Err("Controller inspection busy or unavailable; retry shortly"),
        _ => return Err("Controller inspection request failed"),
    }
    let body = &bytes[offset + 4..];
    if body.len() > MAX_RESPONSE {
        return Err("Controller response exceeds limit");
    }
    let lengths: Vec<_> = header
        .lines()
        .skip(1)
        .filter_map(|line| {
            let (name, value) = line.split_once(':')?;
            name.eq_ignore_ascii_case("content-length")
                .then(|| value.trim().parse::<usize>().ok())
        })
        .collect();
    if lengths.as_slice() != [Some(body.len())]
        || header.lines().any(|l| {
            l.split_once(':')
                .is_some_and(|(n, _)| n.eq_ignore_ascii_case("transfer-encoding"))
        })
    {
        return Err("Incomplete or unsupported controller response");
    }
    let value: Value = serde_json::from_slice(body).map_err(|_| "Invalid controller JSON")?;
    if value["schema"] != 1 {
        return Err("Unsupported controller inspection schema");
    }
    Ok(value)
}

async fn select_pod(api: &Api<Pod>, args: &Args) -> Result<String, &'static str> {
    if let Some(name) = &args.pod {
        let pod = api.get(name).await.map_err(
            |_| "Cannot read controller pod; check context, namespace, pod and get permission",
        )?;
        if pod.metadata.deletion_timestamp.is_some()
            || pod.status.as_ref().and_then(|s| s.phase.as_deref()) != Some("Running")
        {
            return Err("Controller pod is not running or is terminating");
        }
        return Ok(name.clone());
    }
    let list = api
        .list(
            &ListParams::default()
                .labels(args.selector.as_deref().unwrap_or("app=jevernetes"))
                .fields("status.phase=Running")
                .limit(2),
        )
        .await
        .map_err(
            |_| "Cannot discover controller pod; check context, namespace and list permission",
        )?;
    if list.items.len() != 1
        || list
            .metadata
            .continue_
            .as_ref()
            .is_some_and(|s| !s.is_empty())
    {
        return Err("Expected one running controller pod; use --pod or narrow --selector");
    }
    let pod = &list.items[0];
    if pod.metadata.deletion_timestamp.is_some() {
        return Err("Controller pod is terminating; retry shortly");
    }
    pod.metadata
        .name
        .clone()
        .ok_or("Controller pod has no name")
}

async fn sample(api: &Api<Pod>, args: &Args) -> Result<(String, Value), &'static str> {
    let pod = select_pod(api, args).await?;
    let forward = api
        .portforward(&pod, &[args.port])
        .await
        .map_err(|_| "Cannot port-forward controller; check pods/portforward permission")?;
    let mut forward = Forward(forward);
    let mut stream = forward
        .0
        .take_stream(args.port)
        .ok_or("Controller port-forward stream unavailable")?;
    let path = args
        .incident
        .as_ref()
        .map_or_else(|| "/v1/status".into(), |id| format!("/v1/incidents/{id}"));
    let value = request(&mut stream, &path).await?;
    Ok((pod, value))
}

pub fn render(value: &Value, details: bool) -> Result<String, &'static str> {
    let mut out = String::new();
    if details {
        let d: Detail =
            serde_json::from_value(value.clone()).map_err(|_| "Invalid incident detail schema")?;
        writeln!(
            out,
            "Incident {} | {} | count {} | level {} | notification sequence {}",
            console(&d.incident.id),
            console(d.incident.last_decision.as_deref().unwrap_or("unknown")),
            d.incident.recurrence_count,
            d.incident.escalation_level,
            d.incident.notification_sequence
        )
        .unwrap();
        writeln!(
            out,
            "Delivery: {} | attempts {}",
            console(d.delivery_status.as_deref().unwrap_or("not retained")),
            d.delivery_attempts.unwrap_or(0)
        )
        .unwrap();
        if let Some(n) = d.last_notification {
            writeln!(
                out,
                "Last enqueued notification (may precede the latest observation):\n{}",
                serde_json::to_string_pretty(&n).expect("JSON")
            )
            .unwrap();
        } else {
            out.push_str(
                "Notification evidence is no longer retained. Incident counters remain durable.\n",
            );
        }
    } else {
        let s: Snapshot = serde_json::from_value(value.clone())
            .map_err(|_| "Invalid controller status schema")?;
        if s.recent_incidents.len() > RECENT_LIMIT {
            return Err("Controller incident list exceeds limit");
        }
        let m = |key: &str| s.metrics[key].to_string();
        writeln!(
            out,
            "Controller: {} | sampled at {} | coverage partial",
            if s.ready { "ready" } else { "NOT READY" },
            s.sampled_at
        )
        .unwrap();
        writeln!(
            out,
            "Logs: {} received | {} streams | queue {} | {} dropped | {} coverage gaps",
            m("received"),
            m("active_streams"),
            m("queue_depth"),
            m("dropped"),
            m("coverage_gaps")
        )
        .unwrap();
        writeln!(
            out,
            "Verdicts: {} reused | {} misses | {} replay duplicates",
            m("verdict_hits"),
            m("verdict_misses"),
            m("novelty_duplicates")
        )
        .unwrap();
        writeln!(
            out,
            "Policy: {} notify | {} review | {} abstain | {} ignore",
            m("policy_notify"),
            m("policy_review"),
            m("policy_abstain"),
            m("policy_ignore")
        )
        .unwrap();
        writeln!(
            out,
            "Delivery: {} pending | {} dead | {} delivered | {} failures",
            s.outbox_pending,
            s.outbox_dead,
            m("notification_delivered"),
            m("notification_failures")
        )
        .unwrap();
        writeln!(
            out,
            "Provider: {} batches | estimated ${} | {} unmetered requests",
            m("provider_batches"),
            m("estimated_cost_usd"),
            m("provider_unmetered_requests")
        )
        .unwrap();
        writeln!(
            out,
            "State: {} incidents | {} failures",
            s.incident_count,
            m("store_failures")
        )
        .unwrap();
        out.push_str("Recent incidents (ID | decision | count | level | sequence):\n");
        for i in s.recent_incidents {
            writeln!(
                out,
                "{} | {} | {} | {} | {}",
                console(&i.id),
                console(i.last_decision.as_deref().unwrap_or("unknown")),
                i.recurrence_count,
                i.escalation_level,
                i.notification_sequence
            )
            .unwrap();
        }
        out.push_str("Use --incident ID for retained evidence and delivery details.\n");
    }
    Ok(out)
}

pub async fn run(
    args: &Args,
    json: bool,
    output: Option<&std::path::Path>,
    stop: CancellationToken,
) -> Result<i32, &'static str> {
    let options = Options {
        context: args.context.clone(),
        namespace: Some(args.namespace.clone()),
        selector: None,
        since: 0,
        tail: 0,
        max_bytes: 1,
        max_streams: 1,
        previous: false,
        in_cluster: false,
    };
    let (client, _) =
        tokio::select! {_=stop.cancelled()=>return Ok(130),r=kubernetes::connect(&options)=>r?};
    let api = Api::<Pod>::namespaced(client, &args.namespace);
    loop {
        let result = tokio::select! {_=stop.cancelled()=>return Ok(130),r=tokio::time::timeout(Duration::from_secs(20),sample(&api,args))=>r.unwrap_or(Err("Controller inspection timed out; check the pod and inspection port"))};
        match result {
            Ok((pod, value)) => {
                // Validate typed responses even in JSON mode.
                let rendered = render(&value, args.incident.is_some())?;
                let report = serde_json::json!({"sampled_at":crate::controller::now(),"target":{"namespace":args.namespace,"pod":pod},"controller":value});
                if let Some(path) = output {
                    crate::report::write_report(path, &report)?;
                }
                let text = if json {
                    format!("{report}\n")
                } else {
                    format!(
                        "\n{}/{}\n{rendered}",
                        console(&args.namespace),
                        console(&pod)
                    )
                };
                use std::io::Write;
                if std::io::stdout().lock().write_all(text.as_bytes()).is_err() {
                    return Err("Cannot write remote inspection output");
                }
            }
            Err(e) if args.watch => eprintln!("[remote] {e}; previous output is stale; retrying"),
            Err(e) => return Err(e),
        }
        if !args.watch {
            return Ok(0);
        }
        tokio::select! {_=stop.cancelled()=>return Ok(130),_=tokio::time::sleep(Duration::from_secs(args.interval))=>()}
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn rejects_truncated_oversized_and_unsupported_responses() {
        assert!(decode(b"HTTP/1.1 200 OK\r\nContent-Length: 99\r\n\r\n{}").is_err());
        assert!(decode(b"HTTP/1.1 200 OK\r\nContent-Length: 12\r\n\r\n{\"schema\":2}").is_err());
        let body = vec![b' '; MAX_RESPONSE + 1];
        let mut response =
            format!("HTTP/1.1 200 OK\r\nContent-Length: {}\r\n\r\n", body.len()).into_bytes();
        response.extend(body);
        assert!(decode(&response).is_err());
        assert_eq!(
            decode(b"HTTP/1.1 503 Busy\r\n\r\nprivate response").unwrap_err(),
            "Controller inspection busy or unavailable; retry shortly"
        );
        assert!(incident_id("../metrics").is_err());
    }
    #[tokio::test]
    async fn reads_fragmented_http_without_a_local_listener() {
        let (mut client, mut server) = tokio::io::duplex(1024);
        let worker = tokio::spawn(async move {
            let mut buf = [0; 256];
            let n = server.read(&mut buf).await.unwrap();
            assert!(
                std::str::from_utf8(&buf[..n])
                    .unwrap()
                    .starts_with("GET /v1/status HTTP/1.1")
            );
            server
                .write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 12\r\n\r\n")
                .await
                .unwrap();
            server.write_all(b"{\"schema\":1}").await.unwrap();
        });
        assert_eq!(
            request(&mut client, "/v1/status").await.unwrap()["schema"],
            1
        );
        worker.await.unwrap();
    }
}
