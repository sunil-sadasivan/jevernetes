//! Exact source-scoped session reuse; never normalize numbers, IDs, or stack traces.
use crate::{
    events::{Event, hash},
    jev::Judgment,
};
use lru::LruCache;
use std::{
    num::NonZeroUsize,
    time::{Duration, Instant},
};
pub fn group_id(event: &Event) -> String {
    hash(&(
        &event.source,
        &event.text,
        event.truncated,
        if event.truncated {
            Some(&event.id)
        } else {
            None
        },
    ))
}
pub struct Cache {
    entries: LruCache<String, (Instant, Judgment, String)>,
    ttl: Duration,
}
impl Cache {
    pub fn new(limit: NonZeroUsize, ttl: Duration) -> Self {
        Self {
            entries: LruCache::new(limit),
            ttl,
        }
    }
    pub fn get(&mut self, event: &Event, now: Instant) -> Option<(Judgment, String)> {
        if event.truncated {
            return None;
        }
        let key = group_id(event);
        let (at, judgment, id) = self.entries.get(&key)?;
        if now.duration_since(*at) >= self.ttl {
            self.entries.pop(&key);
            None
        } else {
            Some((judgment.clone(), id.clone()))
        }
    }
    pub fn insert(&mut self, event: &Event, now: Instant) {
        if !event.truncated && event.judgment.reusable() {
            self.entries.put(
                group_id(event),
                (now, event.judgment.clone(), event.id.clone()),
            );
        }
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn event() -> Event {
        let mut p = crate::events::Parser::new(Default::default());
        p.feed(crate::events::Line {
            bytes: b"ERROR count=1".to_vec(),
            truncated: false,
            private: false,
        });
        let mut e = p.flush().unwrap();
        e.judgment.importance = crate::jev::Importance::Important;
        e
    }
    #[test]
    fn exact_scope_limits_ttl_and_failures() {
        let mut c = Cache::new(NonZeroUsize::new(1).unwrap(), Duration::from_secs(300));
        let now = Instant::now();
        let a = event();
        let mut b = a.clone();
        b.id = "other".into();
        b.timestamp = Some("later".into());
        assert_eq!(group_id(&a), group_id(&b));
        c.insert(&a, now);
        assert!(c.get(&b, now).is_some());
        assert!(c.get(&b, now + Duration::from_secs(300)).is_none());
        for changed in ["number", "source", "truncated", "stack"] {
            let mut b = a.clone();
            match changed {
                "number" => b.text = "ERROR count=2".into(),
                "source" => {
                    b.source.insert("restart_count".into(), 1.into());
                }
                "truncated" => b.truncated = true,
                _ => b.text.push_str("\n at elsewhere"),
            };
            assert_ne!(group_id(&a), group_id(&b));
        }
        b.text = "different".into();
        c.insert(&a, now);
        c.insert(&b, now);
        assert!(c.get(&a, now).is_none());
        b.truncated = true;
        c.insert(&b, now);
        assert!(c.get(&b, now).is_none());
        let mut failed = a.clone();
        failed.judgment = Judgment::failed("synthetic failure");
        c.insert(&failed, now);
        assert!(c.get(&failed, now).is_none());
    }
}
