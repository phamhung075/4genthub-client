//! The park record a budget safeguard leaves in a seat's own directory, and the refusal it earns.
//!
//! A park is written by the CLIENT's `roombudget` when a seat crosses one of the room's ceilings, into
//! `<state dir>/rooms/<room>/seats/<seat>/parked.json`, and the client refuses every route of its own that
//! would hand that seat work. A client-tier gate is only as strong as its callers, though: the runtime is
//! the tier every caller crosses - `SendText`, `RestartSeat`, a room start, the rail nudge - so the record is
//! read HERE too and a caller that forgets the rule cannot exist above it.
//!
//! THE RECORD'S EXISTENCE IS THE PARK, not the record's readability. A file that exists but cannot be parsed
//! is still a seat somebody parked, so it refuses and says out loud that its reason could not be read; the
//! alternative - reading a half-written or foreign file as "not parked" - is how a park quietly stops
//! meaning anything. (The client's `roombudget.ReadParked` treats an unreadable record as no park at all.
//! The two tiers then disagree on a garbled file, and the airtight tier errs the other way on purpose; the
//! client's own reader is where that asymmetry would be closed.)
use std::path::{Path, PathBuf};

use serde::Deserialize;

/// The record the client's `roombudget` writes into a seat's own directory.
pub const PARKED_FILE: &str = "parked.json";

/// The fields of that record the refusal speaks in. A field a newer writer adds is ignored, and a field it
/// omits is read as unknown rather than as an error - the fact is the file's existence.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct Parked {
    #[serde(default)]
    pub reason: String,
    #[serde(default)]
    pub at: String,
    #[serde(default)]
    pub cost_usd: f64,
    #[serde(default)]
    pub calls: u64,
}

/// Where one seat's park record lives under a state root: the seat's OWN directory, the same one the seat
/// runs in, and never the seat store.
pub fn parked_path(state_dir: &Path, room: &str, seat: &str) -> PathBuf {
    state_dir
        .join("rooms")
        .join(room)
        .join("seats")
        .join(seat)
        .join(PARKED_FILE)
}

/// The seat's park record, or `None` when the seat carries none.
pub fn parked(state_dir: &Path, room: &str, seat: &str) -> Option<Parked> {
    let raw = std::fs::read_to_string(parked_path(state_dir, room, seat)).ok()?;
    match serde_json::from_str(&raw) {
        Ok(record) => Some(record),
        // Present and unreadable: a park whose reason cannot be spoken, which is not the same fact as a seat
        // nobody parked. `Default` here carries the empty reason the sentence reports.
        Err(_) => Some(Parked::default()),
    }
}

/// The refusal a runtime route gets from a parked seat: `Ok(())` when the seat may be worked, `Err` the ONE
/// sentence it gets instead. `action` names what did not happen, so the caller's own truth is in the text
/// rather than inferred ("Nothing was sent" / "nothing was restarted" / "the rail prompt was not delivered").
pub fn refuse(state_dir: &Path, room: &str, seat: &str, action: &str) -> Result<(), String> {
    let Some(record) = parked(state_dir, room, seat) else {
        return Ok(());
    };
    let reason = if record.reason.trim().is_empty() {
        "a ceiling breach whose reason could not be read from the record".to_string()
    } else {
        record.reason.clone()
    };
    let at = if record.at.trim().is_empty() {
        "an unknown time".to_string()
    } else {
        record.at.clone()
    };
    Err(format!(
        "seat {seat} of room {room} is PARKED: a budget safeguard stopped it for {reason} \
         (cost_usd={:.2}, calls={}, at {at}), and a parked seat is not handed work; {action}. {} is the \
         record - removing it lets the seat take work again once the cause is understood",
        record.cost_usd,
        record.calls,
        parked_path(state_dir, room, seat).display()
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A scratch state dir under the system temp dir, unique per test, removed first so a rerun is clean.
    fn scratch(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("agenthub-park-{name}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(dir.join("rooms").join("lab").join("seats").join("dev"))
            .expect("scratch dir");
        dir
    }

    #[test]
    fn a_seat_with_no_record_is_not_refused() {
        let dir = scratch("none");
        assert!(parked(&dir, "lab", "dev").is_none());
        assert_eq!(refuse(&dir, "lab", "dev", "nothing was sent"), Ok(()));
    }

    #[test]
    fn the_record_refuses_and_the_sentence_names_the_seat_the_number_and_the_file() {
        let dir = scratch("some");
        std::fs::write(
            parked_path(&dir, "lab", "dev"),
            r#"{"seat":"dev","reason":"spent 1.50 USD in the last 24h, over the 1.00 USD ceiling","at":"2026-10-16T17:23:51Z","cost_usd":1.5,"calls":1}"#,
        )
        .expect("record");

        let err = refuse(&dir, "lab", "dev", "nothing was sent").expect_err("a park refuses");
        for want in [
            "seat dev of room lab is PARKED",
            "spent 1.50 USD in the last 24h, over the 1.00 USD ceiling",
            "cost_usd=1.50",
            "calls=1",
            "at 2026-10-16T17:23:51Z",
            "nothing was sent",
            "parked.json is the record",
        ] {
            assert!(err.contains(want), "{err:?} does not carry {want:?}");
        }
    }

    #[test]
    fn a_record_that_cannot_be_read_is_still_a_park() {
        let dir = scratch("garbled");
        std::fs::write(parked_path(&dir, "lab", "dev"), "{not json").expect("record");

        let err = refuse(&dir, "lab", "dev", "nothing was sent")
            .expect_err("an unreadable record is a park");
        assert!(
            err.contains("PARKED") && err.contains("reason could not be read"),
            "an unreadable record must refuse and say which half is missing: {err}"
        );
    }
}
