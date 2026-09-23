//! Bounded event ingestion and advisory analysis. No remediation or cluster writes.
#![forbid(unsafe_code)]
pub mod events;
pub mod grouping;
pub mod jev;
pub mod kubernetes;
pub mod progress;
pub mod report;
pub mod runtime;
pub mod search;
pub mod tui;

#[cfg(test)]
mod test_support;
