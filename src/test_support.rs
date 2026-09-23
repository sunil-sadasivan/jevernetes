//! Synthetic loopback HTTP fixture. Never loads environment credentials or kubeconfig.
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpListener,
    task::JoinHandle,
};
pub async fn http(responses: Vec<(u16, String)>) -> (String, JoinHandle<Vec<String>>) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let task = tokio::spawn(async move {
        let mut requests = vec![];
        for (status, body) in responses {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut raw = vec![];
            let mut chunk = [0; 8192];
            loop {
                let n = socket.read(&mut chunk).await.unwrap();
                assert!(n > 0);
                raw.extend_from_slice(&chunk[..n]);
                assert!(raw.len() < 2 * 1024 * 1024);
                if let Some(end) = raw.windows(4).position(|w| w == b"\r\n\r\n") {
                    let header = String::from_utf8_lossy(&raw[..end]);
                    let len = header
                        .lines()
                        .find_map(|l| {
                            l.to_lowercase()
                                .strip_prefix("content-length:")
                                .map(|v| v.trim().parse::<usize>().unwrap())
                        })
                        .unwrap_or(0);
                    if raw.len() >= end + 4 + len {
                        break;
                    }
                }
            }
            requests.push(String::from_utf8(raw).unwrap());
            socket.write_all(format!("HTTP/1.1 {status} Synthetic\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",body.len()).as_bytes()).await.unwrap();
        }
        requests
    });
    (format!("http://{address}"), task)
}
pub fn verdict() -> String {
    serde_json::json!({"answers":{"e0_importance":{"type":"choice","choice":"important","confidence":0.99},"e0_severity":{"type":"choice","choice":"impact","confidence":0.9},"e0_category":{"type":"choice","choice":"fraud","confidence":0.8}},"usage":{"input_tokens":100,"output_tokens":0}}).to_string()
}
pub fn pod() -> serde_json::Value {
    serde_json::json!({"apiVersion":"v1","kind":"Pod","metadata":{"name":"synthetic","namespace":"test","uid":"uid1","resourceVersion":"1"},"spec":{"containers":[{"name":"app"}]},"status":{"containerStatuses":[{"name":"app","restartCount":0,"image":"synthetic","imageID":"synthetic","ready":true,"state":{"running":{}}}]}})
}
