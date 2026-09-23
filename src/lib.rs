//! Bounded event ingestion and advisory analysis. No remediation or cluster writes.
#![forbid(unsafe_code)]
pub mod controller;
pub mod events;
pub mod grouping;
pub mod jev;
pub mod kubernetes;
pub mod report;
pub mod runtime;

#[cfg(test)]
mod test_support;
