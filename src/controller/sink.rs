//! Notification transports accept only the redacted, allowlisted outbox payload.
use super::store::Result;
use std::{future::Future, pin::Pin, time::Duration};
use tokio::io::AsyncWriteExt;

pub type Delivery<'a> = Pin<Box<dyn Future<Output = Result<()>> + Send + 'a>>;
pub trait Sink: Send + Sync {
    fn deliver<'a>(&'a self, id: &'a str, payload: &'a str) -> Delivery<'a>;
}
pub struct Stdout;
impl Sink for Stdout {
    fn deliver<'a>(&'a self, _id: &'a str, payload: &'a str) -> Delivery<'a> {
        Box::pin(async move {
            let mut output = tokio::io::stdout();
            output
                .write_all(format!("{payload}\n").as_bytes())
                .await
                .map_err(|_| "Notification stdout failed")?;
            output
                .flush()
                .await
                .map_err(|_| "Notification stdout failed")
        })
    }
}
// Deliberately no Debug/Serialize: URL and token are sensitive, including URL paths.
pub struct Webhook {
    client: reqwest::Client,
    url: reqwest::Url,
    auth: Option<reqwest::header::HeaderValue>,
}
fn secret(name: &str) -> Result<Option<String>> {
    if let Ok(value) = std::env::var(name) {
        if value.len() > 16384 || value.trim().is_empty() {
            return Err("Invalid notification secret");
        }
        return Ok(Some(value.trim().into()));
    }
    if let Ok(path) = std::env::var(format!("{name}_FILE")) {
        use std::io::Read;
        let file = std::fs::File::open(path).map_err(|_| "Cannot read notification secret file")?;
        let mut data = String::new();
        file.take(16385)
            .read_to_string(&mut data)
            .map_err(|_| "Cannot read notification secret file")?;
        if data.len() > 16384 || data.trim().is_empty() {
            return Err("Invalid notification secret file");
        }
        return Ok(Some(data.trim().into()));
    }
    Ok(None)
}
impl Webhook {
    pub fn from_env() -> Result<Self> {
        let url =
            secret("JEV_WEBHOOK_URL")?.ok_or("Set JEV_WEBHOOK_URL or JEV_WEBHOOK_URL_FILE")?;
        Self::new(&url, secret("JEV_WEBHOOK_TOKEN")?.as_deref(), false)
    }
    pub(crate) fn new(url: &str, token: Option<&str>, local_test: bool) -> Result<Self> {
        let url = reqwest::Url::parse(url).map_err(|_| "Invalid webhook URL")?;
        let loopback = url
            .host_str()
            .is_some_and(|h| h == "127.0.0.1" || h == "[::1]");
        if (url.scheme() != "https" && !(local_test && url.scheme() == "http" && loopback))
            || !url.username().is_empty()
            || url.password().is_some()
            || url.fragment().is_some()
        {
            return Err("Webhook requires HTTPS without userinfo or fragment");
        }
        let auth = token
            .map(|s| -> Result<_> {
                let mut value =
                    reqwest::header::HeaderValue::from_str(&format!("Bearer {}", s.trim()))
                        .map_err(|_| "Invalid webhook token")?;
                value.set_sensitive(true);
                Ok(value)
            })
            .transpose()?;
        let client = reqwest::Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .no_proxy()
            .timeout(Duration::from_secs(5))
            .connect_timeout(Duration::from_secs(2))
            .build()
            .map_err(|_| "Cannot initialize webhook client")?;
        Ok(Self { client, url, auth })
    }
}
impl Sink for Webhook {
    fn deliver<'a>(&'a self, id: &'a str, payload: &'a str) -> Delivery<'a> {
        Box::pin(async move {
            if payload.len() > 131072 {
                return Err("Notification exceeds payload bound");
            }
            let mut request = self
                .client
                .post(self.url.clone())
                .header("content-type", "application/json")
                .header("idempotency-key", id)
                .body(payload.to_owned());
            if let Some(auth) = &self.auth {
                request = request.header(reqwest::header::AUTHORIZATION, auth);
            }
            let mut response = request
                .send()
                .await
                .map_err(|_| "Webhook transport failed")?;
            let success = response.status().is_success();
            if response.content_length().is_some_and(|n| n > 8192) {
                return Err("Webhook response exceeds limit");
            }
            let mut bytes = 0;
            while let Some(chunk) = response
                .chunk()
                .await
                .map_err(|_| "Webhook response failed")?
            {
                bytes += chunk.len();
                if bytes > 8192 {
                    return Err("Webhook response exceeds limit");
                }
            }
            if success {
                Ok(())
            } else {
                Err("Webhook rejected notification")
            }
        })
    }
}
