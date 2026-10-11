//! Optional bounded terminal view. Readers share immutable events, never block ingestion on I/O.
use crate::{events::Event, jev::Usage};
use std::{
    collections::VecDeque,
    sync::{Arc, Mutex},
};

pub type SharedProgress = Arc<Mutex<Progress>>;

#[derive(Clone)]
pub struct Progress {
    pub events: VecDeque<Arc<Event>>,
    pub pending: Vec<Arc<Event>>,
    pub total: u64,
    pub evicted: u64,
    pub usage: Usage,
    pub phase: &'static str,
    pub revision: u64,
    limit: usize,
}

impl Progress {
    pub fn new(limit: usize, usage: Usage) -> SharedProgress {
        Arc::new(Mutex::new(Self {
            events: VecDeque::new(),
            pending: Vec::new(),
            total: 0,
            evicted: 0,
            usage,
            phase: "Collecting",
            revision: 0,
            limit,
        }))
    }

    pub fn begin(&mut self, events: &[Event]) {
        self.pending = events.iter().cloned().map(Arc::new).collect();
        self.revision += 1;
    }

    pub fn commit(&mut self, events: &[Event], usage: Option<&Usage>) {
        self.pending.clear();
        for event in events {
            if self.events.len() == self.limit {
                self.events.pop_front();
                self.evicted += 1;
            }
            self.events.push_back(Arc::new(event.clone()));
            self.total += 1;
        }
        if let Some(usage) = usage {
            self.usage = usage.clone();
        }
        self.revision += 1;
    }

    pub fn finish(&mut self, phase: &'static str) {
        self.phase = phase;
        self.revision += 1;
    }

    pub fn counters(&self) -> Self {
        Self {
            events: VecDeque::new(),
            pending: Vec::new(),
            total: self.total,
            evicted: self.evicted,
            usage: self.usage.clone(),
            phase: self.phase,
            revision: self.revision,
            limit: self.limit,
        }
    }
}
