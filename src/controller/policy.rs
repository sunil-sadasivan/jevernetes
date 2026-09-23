//! Provider-independent, replayable advisory decisions. No executable model actions.
use crate::{
    events::Event,
    jev::{Category, Importance, Severity},
};
use serde::{Deserialize, Serialize};

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Policy {
    pub categories: Vec<Category>,
    pub severities: Vec<Severity>,
    pub min_confidence: f64,
    pub cooldown_seconds: i64,
    pub recurrence_window_seconds: i64,
    pub recurrence_count: u32,
    pub max_escalation: u32,
    pub abstention: Abstention,
}
#[derive(Clone, Copy, Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum Abstention {
    Record,
    Review,
}
#[derive(Clone, Copy, Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum Decision {
    Ignore,
    Abstain,
    Review,
    Notify,
}
impl Default for Policy {
    fn default() -> Self {
        Self {
            categories: vec![Category::Security, Category::Fraud],
            severities: vec![Severity::Degraded, Severity::Impact, Severity::Outage],
            min_confidence: 0.85,
            cooldown_seconds: 300,
            recurrence_window_seconds: 900,
            recurrence_count: 5,
            max_escalation: 8,
            abstention: Abstention::Review,
        }
    }
}
impl Policy {
    pub fn validate(&self) -> Result<(), &'static str> {
        if self.categories.len() > 9
            || self.severities.len() > 5
            || self.categories.is_empty()
            || self.categories.contains(&Category::Unknown)
            || self.severities.is_empty()
            || self.severities.contains(&Severity::Unknown)
            || !self.min_confidence.is_finite()
            || !(0.0..=1.0).contains(&self.min_confidence)
            || !(1..=86400).contains(&self.cooldown_seconds)
            || !(1..=604800).contains(&self.recurrence_window_seconds)
            || !(2..=100000).contains(&self.recurrence_count)
            || !(1..=32).contains(&self.max_escalation)
        {
            return Err("Invalid controller policy");
        }
        Ok(())
    }
    pub fn decide(&self, event: &Event) -> Decision {
        let j = &event.judgment;
        let review = match self.abstention {
            Abstention::Record => Decision::Abstain,
            Abstention::Review => Decision::Review,
        };
        if event.truncated || !super::cacheable(event) || j.importance == Importance::Uncertain {
            return review;
        }
        if [
            j.importance_confidence,
            j.category_confidence,
            j.severity_confidence,
        ]
        .into_iter()
        .any(|c| c.is_none_or(|c| c < self.min_confidence))
        {
            return review;
        }
        if j.importance == Importance::Important
            && self.categories.contains(&j.category)
            && self.severities.contains(&j.severity)
        {
            Decision::Notify
        } else {
            Decision::Ignore
        }
    }
}
