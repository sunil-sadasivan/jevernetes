//! One connection at a time, 1 KiB requests, one second deadline, no payload metrics.
use super::Controller;
use std::{sync::atomic::Ordering, time::Duration};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpListener,
};
use tokio_util::sync::CancellationToken;
pub async fn serve(listener: TcpListener, controller: Controller, stop: CancellationToken) {
    loop {
        let accepted = tokio::select! {_=stop.cancelled()=>break,r=listener.accept()=>r};
        let Ok((mut stream, _)) = accepted else {
            break;
        };
        let c = &controller;
        let request = async {
            let mut bytes = [0; 1024];
            let mut n = 0;
            while n < bytes.len() && !bytes[..n].windows(2).any(|w| w == b"\r\n") {
                let read = stream.read(&mut bytes[n..]).await?;
                if read == 0 {
                    break;
                }
                n += read;
            }
            let first = String::from_utf8_lossy(&bytes[..n]);
            let (code, body) = if first.starts_with("GET /livez HTTP/1.") {
                (200, "live\n".into())
            } else if first.starts_with("GET /readyz HTTP/1.") {
                if c.ready.load(Ordering::Acquire) {
                    (200, "ready\n".into())
                } else {
                    (503, "collection stopped or state unavailable\n".into())
                }
            } else if first.starts_with("GET /metrics HTTP/1.") {
                let m = c.metrics.lock().expect("metrics");
                let mut body = String::new();
                let value = serde_json::to_value(&*m).expect("metrics");
                for (key, v) in value.as_object().expect("metrics") {
                    if v.is_number() {
                        body.push_str(&format!("jevernetes_{key} {v}\n"));
                    }
                }
                body.push_str(&format!(
                    "jevernetes_state_ready {}\n",
                    u8::from(c.ready.load(Ordering::Acquire))
                ));
                body.push_str("jevernetes_coverage_complete 0\n");
                (200, body)
            } else {
                (404, "not found\n".into())
            };
            stream.write_all(format!("HTTP/1.1 {code} Response\r\nContent-Type: text/plain; version=0.0.4\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",body.len()).as_bytes()).await
        };
        tokio::select! {_=stop.cancelled()=>break,_=tokio::time::timeout(Duration::from_secs(1),request)=>()}
    }
}
