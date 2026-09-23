//! Read-only, bounded inspection on pod loopback; never on the public health listener.
use super::{Controller, digest, now, store::Store};
use rusqlite::OptionalExtension;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::sync::atomic::Ordering;
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpListener,
};
use tokio_util::sync::CancellationToken;

pub const MAX_RESPONSE: usize = 256 * 1024;
pub const RECENT_LIMIT: usize = 20;

#[derive(Debug, Serialize, Deserialize)]
pub struct Incident {
    pub id: String,
    pub recurrence_count: u32,
    pub escalation_level: u32,
    pub notification_sequence: i64,
    pub last_decision: Option<String>,
    pub touched_at: i64,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct Snapshot {
    pub schema: u32,
    pub sampled_at: i64,
    pub ready: bool,
    pub coverage_complete: bool,
    pub metrics: Value,
    pub incident_count: u64,
    pub outbox_pending: u64,
    pub outbox_dead: u64,
    pub recent_incidents: Vec<Incident>,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct Detail {
    pub schema: u32,
    pub incident: Incident,
    /// Last enqueued notification, not necessarily the latest observation. May be pruned.
    pub last_notification: Option<Value>,
    pub delivery_status: Option<String>,
    pub delivery_attempts: Option<u32>,
}

pub fn valid_id(id: &str) -> bool {
    id.len() == 64 && id.bytes().all(|b| b.is_ascii_hexdigit())
}

fn row(r: &rusqlite::Row<'_>) -> rusqlite::Result<Incident> {
    Ok(Incident {
        id: r.get(0)?,
        recurrence_count: r.get(1)?,
        escalation_level: r.get(2)?,
        notification_sequence: r.get(3)?,
        last_decision: r.get(4)?,
        touched_at: r.get(5)?,
    })
}

impl Store {
    pub(crate) fn inspection(&self) -> Result<(u64, u64, u64, Vec<Incident>), &'static str> {
        let query = || -> rusqlite::Result<_> {
            let count = self
                .conn
                .query_row("SELECT count(*) FROM incidents", [], |r| r.get::<_, u32>(0))?;
            let mut q = self.conn.prepare("SELECT key,count,level,sequence,last_decision,touched FROM incidents ORDER BY touched DESC,key LIMIT 20")?;
            let recent = q
                .query_map([], row)?
                .collect::<rusqlite::Result<Vec<_>>>()?;
            Ok((count, recent))
        };
        let (count, recent) = query().map_err(|_| "Inspection unavailable")?;
        let (pending, dead) = self.counts()?;
        Ok((u64::from(count), pending, dead, recent))
    }

    pub(crate) fn incident_detail(&self, id: &str) -> Result<Option<Detail>, &'static str> {
        let query = || -> rusqlite::Result<_> {
            let incident = self.conn.query_row("SELECT key,count,level,sequence,last_decision,touched FROM incidents WHERE key=?1", [id], row).optional()?;
            let Some(incident) = incident else {
                return Ok(None);
            };
            let notification_id = digest(&(id, incident.notification_sequence));
            // SUBSTR bounds allocation even if the local database is damaged.
            let notification: Option<(String, String, u32)> = self
                .conn
                .query_row(
                    "SELECT substr(payload,1,131073),status,attempts FROM outbox WHERE id=?1",
                    [notification_id],
                    |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
                )
                .optional()?;
            Ok(Some((incident, notification)))
        };
        let Some((incident, notification)) = query().map_err(|_| "Inspection unavailable")? else {
            return Ok(None);
        };
        let (payload, status, attempts) = if let Some((raw, status, attempts)) = notification {
            if raw.len() > 131072 {
                return Err("Inspection payload exceeds limit");
            }
            (
                Some(serde_json::from_str(&raw).map_err(|_| "Invalid persisted notification")?),
                Some(status),
                Some(attempts),
            )
        } else {
            (None, None, None)
        };
        Ok(Some(Detail {
            schema: 1,
            incident,
            last_notification: payload,
            delivery_status: status,
            delivery_attempts: attempts,
        }))
    }
}

async fn response(c: &Controller, path: &str) -> (u16, Value) {
    let id = if path == "/v1/status" {
        None
    } else if let Some(id) = path
        .strip_prefix("/v1/incidents/")
        .filter(|id| valid_id(id))
    {
        Some(id.to_lowercase())
    } else {
        return (404, json!({"error":"Unknown inspection endpoint"}));
    };
    let store = c.store.clone();
    // Inspection contention is reported to the reader; it must not stop ingestion.
    let state = tokio::task::spawn_blocking(move || {
        let store = store.try_lock().map_err(|_| "Controller state busy")?;
        if let Some(id) = id {
            store.incident_detail(&id)?.map(serde_json::to_value).transpose().map_err(|_| "Inspection unavailable")
        } else {
            let (count, pending, dead, recent) = store.inspection()?;
            Ok(Some(json!({"incident_count":count,"outbox_pending":pending,"outbox_dead":dead,"recent_incidents":recent})))
        }
    }).await;
    match state {
        Ok(Ok(Some(mut value))) => {
            if path == "/v1/status" {
                let Ok(metrics) = c.metrics.try_lock() else {
                    return (503, json!({"error":"Controller metrics busy"}));
                };
                value["schema"] = json!(1);
                value["sampled_at"] = json!(now());
                value["ready"] = json!(c.ready.load(Ordering::Acquire));
                value["coverage_complete"] = json!(false);
                value["metrics"] = serde_json::to_value(&*metrics).expect("metrics");
            }
            (200, value)
        }
        Ok(Ok(None)) => (404, json!({"error":"Incident not found"})),
        _ => (
            503,
            json!({"error":"Controller inspection unavailable; retry shortly"}),
        ),
    }
}

pub async fn serve(listener: TcpListener, c: Controller, stop: CancellationToken) {
    // Enforce loopback even when this entry point is used outside the CLI.
    if !listener.local_addr().is_ok_and(|a| a.ip().is_loopback()) {
        return;
    }
    loop {
        let accepted = tokio::select! { _=stop.cancelled()=>break, r=listener.accept()=>r };
        let Ok((mut stream, _)) = accepted else { break };
        let request = async {
            let mut bytes = [0; 1024];
            let mut n = 0;
            while n < bytes.len() && !bytes[..n].windows(4).any(|w| w == b"\r\n\r\n") {
                let read = stream.read(&mut bytes[n..]).await?;
                if read == 0 {
                    break;
                }
                n += read;
            }
            let header = std::str::from_utf8(&bytes[..n]).unwrap_or("");
            let mut first = header.lines().next().unwrap_or("").split_whitespace();
            let (method, path, version) = (first.next(), first.next(), first.next());
            let (status, value) = if method == Some("GET")
                && matches!(version, Some("HTTP/1.0" | "HTTP/1.1"))
                && first.next().is_none()
                && header.contains("\r\n\r\n")
            {
                response(&c, path.unwrap_or("")).await
            } else {
                (400, json!({"error":"Expected bounded GET request"}))
            };
            let body = serde_json::to_vec(&value).expect("inspection JSON");
            if body.len() > MAX_RESPONSE {
                return Ok(());
            }
            stream.write_all(format!("HTTP/1.1 {status} Response\r\nContent-Type: application/json\r\nCache-Control: no-store\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",body.len()).as_bytes()).await?;
            stream.write_all(&body).await
        };
        tokio::select! { _=stop.cancelled()=>break, _=tokio::time::timeout(std::time::Duration::from_secs(2),request)=>() }
    }
}
