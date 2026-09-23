//! One local writer; every policy transition and notification intent commits atomically.
use super::{Contract, Decision, Notification, Policy, cacheable, digest};
use crate::{events::Event, jev::Judgment};
use rusqlite::{Connection, OptionalExtension, params};
use std::{fs::File, path::Path, time::Duration};

pub const ROW_LIMIT: i64 = 100_000;
pub const OUTBOX_LIMIT: i64 = 10_000;
pub const RETENTION: i64 = 604_800;
pub const PRUNE_BATCH: i64 = 256;
pub type Result<T> = std::result::Result<T, &'static str>;
fn db<T>(r: rusqlite::Result<T>) -> Result<T> {
    r.map_err(|_| "Controller state operation failed")
}

pub struct Store {
    pub(crate) conn: Connection,
    _lock: File,
}
#[derive(Debug)]
pub enum Lookup {
    Hit(Judgment),
    Miss,
    Expired,
}
#[derive(Debug)]
pub struct Pending {
    pub id: String,
    pub payload: String,
    pub attempts: u32,
}
#[derive(Default)]
pub struct Applied {
    pub duplicate: bool,
    pub enqueued: bool,
    pub decision: Option<Decision>,
}
impl Store {
    pub fn open(path: &Path) -> Result<Self> {
        // Directory must be provisioned privately by the operator; never chmod an existing directory.
        let mut options = std::fs::OpenOptions::new();
        options.read(true).write(true).create(true).truncate(false);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o600);
        }
        options
            .open(path)
            .map_err(|_| "Cannot create controller state")?;
        // Canonical absolute paths prevent SQLite URI/:memory: interpretation and
        // ensure symlink aliases contend for the same writer lock.
        let path = std::fs::canonicalize(path).map_err(|_| "Cannot resolve controller state")?;
        let mut lock_path = path.as_os_str().to_os_string();
        lock_path.push(".lock");
        let lock = options
            .open(lock_path)
            .map_err(|_| "Cannot open controller lock")?;
        lock.try_lock()
            .map_err(|_| "Controller state already locked or locking unsupported")?;
        let conn = db(Connection::open(path))?;
        db(conn.busy_timeout(Duration::from_millis(100)))?;
        // Serialized connection, rollback journal, FULL sync. No network filesystem support.
        db(conn.execute_batch(
            "PRAGMA page_size=4096; PRAGMA journal_mode=DELETE; PRAGMA synchronous=FULL;
            PRAGMA max_page_count=65536; PRAGMA journal_size_limit=1048576;",
        ))?;
        let page_size: i64 = db(conn.query_row("PRAGMA page_size", [], |r| r.get(0)))?;
        if page_size != 4096 {
            return Err("Controller state requires 4096-byte SQLite pages");
        }
        let version: i64 = db(conn.query_row("PRAGMA user_version", [], |r| r.get(0)))?;
        if version != 0 && version != 1 {
            return Err("Unsupported controller state schema");
        }
        db(conn.execute_batch("BEGIN IMMEDIATE;
            CREATE TABLE IF NOT EXISTS verdicts (key TEXT PRIMARY KEY, judgment TEXT NOT NULL, created INTEGER NOT NULL, expires INTEGER NOT NULL);
            CREATE INDEX IF NOT EXISTS verdict_expiry ON verdicts(expires);
            CREATE TABLE IF NOT EXISTS seen (key TEXT PRIMARY KEY, at INTEGER NOT NULL, decision TEXT NOT NULL);
            CREATE INDEX IF NOT EXISTS seen_time ON seen(at);
            CREATE TABLE IF NOT EXISTS incidents (key TEXT PRIMARY KEY, window INTEGER NOT NULL, count INTEGER NOT NULL, level INTEGER NOT NULL, last INTEGER NOT NULL, sequence INTEGER NOT NULL, touched INTEGER NOT NULL);
            CREATE INDEX IF NOT EXISTS incident_time ON incidents(touched);
            CREATE TABLE IF NOT EXISTS outbox (id TEXT PRIMARY KEY, incident TEXT NOT NULL, payload TEXT NOT NULL, status TEXT NOT NULL, attempts INTEGER NOT NULL, due INTEGER NOT NULL, updated INTEGER NOT NULL);
            CREATE INDEX IF NOT EXISTS outbox_due ON outbox(status,due);
            CREATE INDEX IF NOT EXISTS outbox_time ON outbox(status,updated);
            CREATE TABLE IF NOT EXISTS delivery_clock (id INTEGER PRIMARY KEY CHECK(id=1), next INTEGER NOT NULL);
            INSERT OR IGNORE INTO delivery_clock VALUES (1,0);
            PRAGMA user_version=1; COMMIT;"))?;
        Ok(Self { conn, _lock: lock })
    }
    pub fn lookup(
        &self,
        event: &Event,
        contract: &Contract,
        now: i64,
        ttl: i64,
        rescore: bool,
    ) -> Result<Lookup> {
        if event.truncated || rescore {
            return Ok(Lookup::Miss);
        }
        let row: Option<(String, i64, i64)> = db(self
            .conn
            .query_row(
                "SELECT judgment,created,expires FROM verdicts WHERE key=?1",
                [contract.key(event)],
                |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
            )
            .optional())?;
        let Some((raw, created, expires)) = row else {
            return Ok(Lookup::Miss);
        };
        if now < created || now >= expires.min(created.saturating_add(ttl)) {
            return Ok(Lookup::Expired);
        }
        let judgment: Judgment =
            serde_json::from_str(&raw).map_err(|_| "Invalid persisted judgment")?;
        let mut checked = event.clone();
        checked.judgment = judgment.clone();
        if !cacheable(&checked) {
            return Ok(Lookup::Miss);
        }
        Ok(Lookup::Hit(judgment))
    }
    pub fn apply(
        &mut self,
        event: &Event,
        contract: &Contract,
        policy: &Policy,
        now: i64,
        ttl: i64,
    ) -> Result<Applied> {
        if event.text.len() > crate::events::MAX_EVENT {
            return Err("Controller evidence exceeds limit");
        }
        let tx = db(self.conn.transaction())?;
        if cacheable(event) && !event.analysis_reused {
            let count: i64 = db(tx.query_row("SELECT count(*) FROM verdicts", [], |r| r.get(0)))?;
            let exists: bool = db(tx.query_row(
                "SELECT EXISTS(SELECT 1 FROM verdicts WHERE key=?1)",
                [contract.key(event)],
                |r| r.get(0),
            ))?;
            if !exists && count >= ROW_LIMIT {
                return Err("Controller verdict capacity exhausted");
            }
            db(tx.execute("INSERT INTO verdicts VALUES (?1,?2,?3,?4) ON CONFLICT(key) DO UPDATE SET judgment=excluded.judgment,created=excluded.created,expires=excluded.expires", params![contract.key(event), serde_json::to_string(&event.judgment).map_err(|_| "Invalid judgment")?, now, now.saturating_add(ttl)]))?;
        }
        let decision = policy.decide(event);
        // Timestamped exact replays do not manufacture recurrence. Missing timestamps are
        // deliberately treated as new observations; a process cannot prove their identity.
        let seen_key = digest(&(
            &event.source,
            &event.text,
            event.truncated,
            &event.timestamp,
        ));
        let decision_signature = digest(&(contract, policy, decision));
        let mut new_evidence = true;
        if event.timestamp.is_some() {
            let prior: Option<String> = db(tx
                .query_row(
                    "SELECT decision FROM seen WHERE key=?1 AND at>?2",
                    params![seen_key, now - RETENTION],
                    |r| r.get(0),
                )
                .optional())?;
            new_evidence = prior.is_none();
            if prior.as_ref() == Some(&decision_signature) {
                db(tx.commit())?;
                return Ok(Applied {
                    duplicate: true,
                    ..Default::default()
                });
            }
            let count: i64 = db(tx.query_row("SELECT count(*) FROM seen", [], |r| r.get(0)))?;
            if new_evidence && count >= ROW_LIMIT {
                return Err("Controller novelty capacity exhausted");
            }
            db(tx.execute(
                "INSERT OR REPLACE INTO seen VALUES (?1,?2,?3)",
                params![seen_key, now, decision_signature],
            ))?;
        }
        let mut result = Applied {
            decision: Some(decision),
            ..Default::default()
        };
        if matches!(decision, Decision::Notify | Decision::Review) {
            let key = digest(&("incident-v1", &event.source, &event.text, event.truncated));
            let old: Option<(i64, u32, u32, i64, i64)> = db(tx
                .query_row(
                    "SELECT window,count,level,last,sequence FROM incidents WHERE key=?1",
                    [&key],
                    |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?, r.get(4)?)),
                )
                .optional())?;
            let (mut window, mut count, mut level, last, mut sequence) =
                old.unwrap_or((now, 0, 0, now - policy.cooldown_seconds, 0));
            if now < window || now - window >= policy.recurrence_window_seconds {
                window = now;
                count = 0;
            }
            count = count.saturating_add(u32::from(new_evidence));
            let escalate = new_evidence
                && count > 0
                && count.is_multiple_of(policy.recurrence_count)
                && level < policy.max_escalation;
            if escalate {
                level += 1;
            }
            let send = old.is_none()
                || !new_evidence
                || escalate
                || now < last
                || now.saturating_sub(last) >= policy.cooldown_seconds;
            if old.is_none() {
                let n: i64 = db(tx.query_row("SELECT count(*) FROM incidents", [], |r| r.get(0)))?;
                if n >= OUTBOX_LIMIT {
                    return Err("Controller incident capacity exhausted");
                }
            }
            if send {
                let n: i64 = db(tx.query_row("SELECT count(*) FROM outbox", [], |r| r.get(0)))?;
                if n >= OUTBOX_LIMIT {
                    return Err("Controller outbox capacity exhausted");
                }
                sequence += 1;
                let id = digest(&(&key, sequence));
                let payload =
                    Notification::new(&id, &key, event, policy, decision, level, count, now);
                db(tx.execute(
                    "INSERT INTO outbox VALUES (?1,?2,?3,'pending',0,?4,?4)",
                    params![
                        id,
                        key,
                        serde_json::to_string(&payload).map_err(|_| "Invalid notification")?,
                        now
                    ],
                ))?;
                result.enqueued = true;
            }
            db(tx.execute(
                "INSERT OR REPLACE INTO incidents VALUES (?1,?2,?3,?4,?5,?6,?7)",
                params![
                    key,
                    window,
                    count,
                    level,
                    if send { now } else { last },
                    sequence,
                    now
                ],
            ))?;
        }
        db(tx.commit())?;
        Ok(result)
    }
    /// Reserve the attempt and rate slot before network I/O. An interrupted attempt is
    /// eligible again after its lease; IDs never change. One delivery worker only.
    pub fn claim(&mut self, now: i64, max_attempts: u32, interval: i64) -> Result<Option<Pending>> {
        let tx = db(self.conn.transaction())?;
        db(tx.execute("UPDATE outbox SET status='dead',updated=?1 WHERE id IN (SELECT id FROM outbox WHERE status='pending' AND due<=?1 AND attempts>=?2 LIMIT 256)", params![now,max_attempts]))?;
        let next: i64 = db(
            tx.query_row("SELECT next FROM delivery_clock WHERE id=1", [], |r| {
                r.get(0)
            }),
        )?;
        if now < next {
            db(tx.commit())?;
            return Ok(None);
        }
        let row = db(tx.query_row("SELECT id,payload,attempts FROM outbox WHERE status='pending' AND due<=?1 AND attempts<?2 ORDER BY due,id LIMIT 1", params![now,max_attempts], |r| Ok(Pending{id:r.get(0)?,payload:r.get(1)?,attempts:r.get(2)?})).optional())?;
        if let Some(ref row) = row {
            db(tx.execute(
                "UPDATE outbox SET attempts=attempts+1,due=?2,updated=?3 WHERE id=?1",
                params![row.id, now + 30, now],
            ))?;
            db(tx.execute(
                "UPDATE delivery_clock SET next=?1 WHERE id=1",
                [now + interval],
            ))?;
        }
        db(tx.commit())?;
        Ok(row.map(|mut r| {
            r.attempts += 1;
            r
        }))
    }
    pub fn finish(
        &self,
        row: &Pending,
        now: i64,
        success: bool,
        max_attempts: u32,
        jitter: u64,
    ) -> Result<()> {
        let status = if success {
            "delivered"
        } else if row.attempts >= max_attempts {
            "dead"
        } else {
            "pending"
        };
        let delay = ((1i64 << row.attempts.min(8)) + (jitter % 4) as i64).min(300);
        db(self.conn.execute(
            "UPDATE outbox SET status=?2,due=?3,updated=?4 WHERE id=?1",
            params![row.id, status, now + delay, now],
        ))?;
        Ok(())
    }
    pub fn next_due(&self, now: i64) -> Result<i64> {
        let due: Option<i64> = db(self.conn.query_row(
            "SELECT min(due) FROM outbox WHERE status='pending'",
            [],
            |r| r.get(0),
        ))?;
        let rate: i64 =
            db(self
                .conn
                .query_row("SELECT next FROM delivery_clock WHERE id=1", [], |r| {
                    r.get(0)
                }))?;
        Ok(due
            .map_or(now + 60, |d| d.max(rate).max(now + 1))
            .min(now + 60))
    }
    pub fn counts(&self) -> Result<(u64, u64)> {
        db(self.conn.query_row(
            "SELECT coalesce(sum(status='pending'),0),coalesce(sum(status='dead'),0) FROM outbox",
            [],
            |r| Ok((r.get::<_, u32>(0)? as u64, r.get::<_, u32>(1)? as u64)),
        ))
    }
    pub fn prune(&self, now: i64) -> Result<usize> {
        let mut total = 0;
        for sql in [
            "DELETE FROM verdicts WHERE key IN (SELECT key FROM verdicts WHERE expires<=?1 LIMIT 256)",
            "DELETE FROM seen WHERE key IN (SELECT key FROM seen WHERE at<=?1-604800 LIMIT 256)",
            "DELETE FROM outbox WHERE id IN (SELECT id FROM outbox WHERE status='delivered' AND updated<=?1-604800 LIMIT 256)",
        ] {
            total += db(self.conn.execute(sql, [now]))?;
        }
        Ok(total)
    }
}
