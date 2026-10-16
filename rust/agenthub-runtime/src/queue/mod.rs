//! The room's durable rail: one `queue.sqlite` per room, owned by the room daemon and reachable only
//! over the room socket (design §3). The client renders it; it never opens the file — two writers is the
//! defect class this team has already paid for.
//!
//! Semantics that were load-bearing on the live rig and are preserved here:
//!
//! * `state ∈ {pending, in-progress, blocked, handed-off, done}`; `handed-off` is intermediate
//!   (acceptance pending), `done` is terminal.
//! * closure takes a reason from exactly one set: `handed_off_to, blocked_on, denied, canceled,
//!   no-follow-on, escalation, superseded`, and `handed_off_to` requires a target.
//! * `chain_of_record` (lineage) and `reply_to` (thread) — rows are only legible because of them.
//! * an append-only transitions table (who, when, from-state, to-state, note) — the audit trail.
//! * wake fields (`blocked_on`, `wake_after_seconds`) so the idle-seat alert has something to stand on.
//!
//! Deliberately NOT ported: the rig orchestrator table's human-notification, identity-provenance and bridge
//! columns. Sender identity stays on the wire and is stamped by the transport, not stored as history.
//!
//! Two anti-lying rules, because both are defects this rig has already seen:
//!
//! * a write returns the row **re-read from the database**, so the state a caller is told is the state
//!   the next read shows (the shim's stub answered success over a no-op);
//! * a read returns whatever rows exist — never a canned empty answer for a store it did not read.
//!
//! `handed-off` being intermediate is why `close` with reason `handed_off_to` records two transitions
//! (…→handed-off, handed-off→done): the row ends `done`, and the trail still shows who it was handed to.

use serde::{Deserialize, Serialize};
use std::path::Path;

/// The five states a row may occupy.
pub const STATES: [&str; 5] = ["pending", "in-progress", "blocked", "handed-off", "done"];

/// The closure reasons, exactly the set the rig daemon enforced.
pub const CLOSURE_REASONS: [&str; 7] = [
    "handed_off_to",
    "blocked_on",
    "denied",
    "canceled",
    "no-follow-on",
    "escalation",
    "superseded",
];

/// The states a row rests in for good (`queue-repository.js:38`, `TERMINAL_QUEUE_STATES`). A handoff's
/// source rests at `handed-off`; the work continues in the successor row it creates, so the source is
/// never reopened and never closable again.
pub const TERMINAL_STATES: [&str; 2] = ["done", "handed-off"];

/// One row of the rail.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Row {
    pub qitem_id: String,
    pub state: String,
    pub summary: String,
    pub body: String,
    pub source_session: String,
    pub destination_session: String,
    pub chain_of_record: Vec<String>,
    pub reply_to: Option<String>,
    pub blocked_on: Option<String>,
    pub wake_after_seconds: Option<i64>,
    pub closure_reason: Option<String>,
    pub closure_target: Option<String>,
    pub priority: String,
    pub tags: Vec<String>,
    pub tier: Option<String>,
    pub ts_created: String,
    pub ts_updated: String,
    pub transitions: Vec<Transition>,
}

/// One append-only transition: who moved the row, when, from where to where, and why.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Transition {
    pub transition_id: i64,
    pub qitem_id: String,
    pub ts: String,
    pub state: String,
    pub from_state: Option<String>,
    pub actor_session: String,
    pub transition_note: String,
    pub closure_reason: Option<String>,
    pub closure_target: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct CreateInput {
    pub source_session: String,
    pub destination_session: String,
    pub summary: String,
    #[serde(default)]
    pub body: String,
    #[serde(default)]
    pub priority: Option<String>,
    #[serde(default)]
    pub tags: Vec<String>,
    #[serde(default)]
    pub tier: Option<String>,
    #[serde(default)]
    pub reply_to: Option<String>,
    #[serde(default)]
    pub chain_of_record: Vec<String>,
    #[serde(default)]
    pub blocked_on: Option<String>,
    #[serde(default)]
    pub wake_after_seconds: Option<i64>,
    /// Retry identity: reuse for the same create after an unknown outcome; generated when absent.
    #[serde(default)]
    pub qitem_id: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct ListFilter {
    #[serde(default)]
    pub destination_session: Option<String>,
    #[serde(default)]
    pub source_session: Option<String>,
    #[serde(default)]
    pub state: Vec<String>,
    /// The states an owner still owes something on: pending, in-progress, blocked.
    #[serde(default)]
    pub active_only: bool,
    #[serde(default)]
    pub limit: Option<usize>,
    /// The seat asking. Set, the read is rate-limited (`list_rate_limited`); unset (the daemon's own
    /// reads), it is not.
    #[serde(default)]
    pub requester_session: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct UpdateInput {
    pub qitem_id: String,
    pub actor_session: String,
    #[serde(default)]
    pub state: Option<String>,
    /// A note with no state is a state-preserving append.
    #[serde(default)]
    pub transition_note: Option<String>,
    #[serde(default)]
    pub closure_reason: Option<String>,
    #[serde(default)]
    pub closure_target: Option<String>,
    #[serde(default)]
    pub blocked_on: Option<String>,
    #[serde(default)]
    pub wake_after_seconds: Option<i64>,
    #[serde(default)]
    pub summary: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CloseInput {
    pub qitem_id: String,
    pub actor_session: String,
    pub closure_reason: String,
    #[serde(default)]
    pub closure_target: Option<String>,
    #[serde(default)]
    pub transition_note: String,
}

/// The states an owner still owes something on.
const ACTIVE_STATES: [&str; 3] = ["pending", "in-progress", "blocked"];

/// Column list shared by every row read, in `map_row` order.
const ROW_COLUMNS: &str = "qitem_id, state, summary, body, source_session, destination_session, \
     chain_of_record, reply_to, blocked_on, wake_after_seconds, closure_reason, closure_target, \
     priority, tags, tier, ts_created, ts_updated";

/// The priorities a row may carry. `high` is reserved for production breakage (`PROD_BREAK_TAG`).
const PRIORITIES: [&str; 3] = ["low", "normal", "high"];
const PROD_BREAK_TAG: &str = "prod-break";
const SUMMARY_MAX_CHARS: usize = 120;
const BODY_MAX_CHARS: usize = 600;
/// Lowercase-kebab tags a row may carry: the kinds of row the rail is used for.
const ALLOWED_TAGS: [&str; 18] = [
    "prod-break",
    "gate",
    "ruling",
    "status",
    "owner-decision",
    "report",
    "finding",
    "guide",
    "docs",
    "defect",
    "skill-check",
    "landing",
    "ack",
    "assignment",
    "handoff",
    "board",
    "checkpoint",
    "reseat",
];
/// A repeated identical `list` by one session inside this window, with no new row, is answered "no change".
const LIST_REPEAT_WINDOW_SECS: i64 = 300;

const SCHEMA: &str = r#"
CREATE TABLE IF NOT EXISTS queue_rows (
    qitem_id            TEXT PRIMARY KEY,
    state               TEXT NOT NULL,
    summary             TEXT NOT NULL,
    body                TEXT NOT NULL,
    source_session      TEXT NOT NULL,
    destination_session TEXT NOT NULL,
    chain_of_record     TEXT NOT NULL,
    reply_to            TEXT,
    blocked_on          TEXT,
    wake_after_seconds  INTEGER,
    closure_reason      TEXT,
    closure_target      TEXT,
    priority            TEXT NOT NULL,
    tags                TEXT NOT NULL,
    tier                TEXT,
    ts_created          TEXT NOT NULL,
    ts_updated          TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS queue_transitions (
    transition_id   INTEGER PRIMARY KEY AUTOINCREMENT,
    qitem_id        TEXT NOT NULL,
    ts              TEXT NOT NULL,
    state           TEXT NOT NULL,
    from_state      TEXT,
    actor_session   TEXT NOT NULL,
    transition_note TEXT NOT NULL,
    closure_reason  TEXT,
    closure_target  TEXT
);
CREATE TABLE IF NOT EXISTS queue_list_marks (
    session       TEXT NOT NULL,
    filter_key    TEXT NOT NULL,
    listed_at     INTEGER NOT NULL,
    newest_qitem  TEXT NOT NULL,
    row_count     INTEGER NOT NULL,
    PRIMARY KEY (session, filter_key)
);
CREATE INDEX IF NOT EXISTS idx_queue_rows_destination ON queue_rows (destination_session, ts_created);
CREATE INDEX IF NOT EXISTS idx_queue_rows_source ON queue_rows (source_session, ts_created);
CREATE INDEX IF NOT EXISTS idx_queue_rows_state ON queue_rows (state, ts_created);
CREATE INDEX IF NOT EXISTS idx_queue_transitions_qitem ON queue_transitions (qitem_id, transition_id);
"#;

/// Fixed-width UTC stamp: lexicographic order is chronological order, so `ts_created DESC` is
/// newest-first without a second sort key.
fn now_ts() -> String {
    chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Micros, true)
}

/// `qitem-<UTC YYYYMMDDHHMMSS>-<12 lowercase hex>`.
fn new_qitem_id() -> String {
    let stamp = chrono::Utc::now().format("%Y%m%d%H%M%S").to_string();
    let hex = uuid::Uuid::new_v4().simple().to_string();
    format!("qitem-{}-{}", stamp, &hex[..12])
}

/// The typed-row rules, checked before anything is written. Each refusal is one line naming the rule and its cap.
fn validate_typed_fields(input: &CreateInput) -> Result<String, String> {
    let priority = input.priority.as_deref().unwrap_or("normal");
    if !PRIORITIES.contains(&priority) {
        return Err(format!(
            "priority rule: '{}' is not one of {}",
            priority,
            PRIORITIES.join(", ")
        ));
    }
    for tag in &input.tags {
        if !ALLOWED_TAGS.contains(&tag.as_str()) {
            return Err(format!(
                "tag rule: '{}' is not an allowed tag; allowed (lowercase kebab): {}",
                tag,
                ALLOWED_TAGS.join(", ")
            ));
        }
    }
    if priority == "high" && !input.tags.iter().any(|t| t == PROD_BREAK_TAG) {
        return Err(format!(
            "priority rule: 'high' requires tag '{}'",
            PROD_BREAK_TAG
        ));
    }
    check_summary(&input.summary)?;
    let body_chars = input.body.chars().count();
    if body_chars > BODY_MAX_CHARS {
        return Err(format!(
            "body cap: {} chars exceeds the {}-char cap",
            body_chars, BODY_MAX_CHARS
        ));
    }
    Ok(priority.to_string())
}

fn check_summary(summary: &str) -> Result<(), String> {
    let chars = summary.chars().count();
    if chars > SUMMARY_MAX_CHARS {
        return Err(format!(
            "summary cap: {} chars exceeds the {}-char cap",
            chars, SUMMARY_MAX_CHARS
        ));
    }
    Ok(())
}

fn unknown_state(value: &str) -> String {
    format!(
        "unknown state '{}'; valid states: {}",
        value,
        STATES.join(", ")
    )
}

fn unknown_reason(value: &str) -> String {
    format!(
        "unknown closure_reason '{}'; valid reasons: {}",
        value,
        CLOSURE_REASONS.join(", ")
    )
}

/// JSON array column -> `Vec<String>`; a corrupt cell is a read error, not a panic.
fn json_list(r: &rusqlite::Row<'_>, idx: usize) -> rusqlite::Result<Vec<String>> {
    let raw: String = r.get(idx)?;
    if raw.trim().is_empty() {
        return Ok(Vec::new());
    }
    serde_json::from_str(&raw).map_err(|e| {
        rusqlite::Error::FromSqlConversionFailure(idx, rusqlite::types::Type::Text, Box::new(e))
    })
}

fn map_row(r: &rusqlite::Row<'_>) -> rusqlite::Result<Row> {
    Ok(Row {
        qitem_id: r.get(0)?,
        state: r.get(1)?,
        summary: r.get(2)?,
        body: r.get(3)?,
        source_session: r.get(4)?,
        destination_session: r.get(5)?,
        chain_of_record: json_list(r, 6)?,
        reply_to: r.get(7)?,
        blocked_on: r.get(8)?,
        wake_after_seconds: r.get(9)?,
        closure_reason: r.get(10)?,
        closure_target: r.get(11)?,
        priority: r.get(12)?,
        tags: json_list(r, 13)?,
        tier: r.get(14)?,
        ts_created: r.get(15)?,
        ts_updated: r.get(16)?,
        transitions: Vec::new(),
    })
}

#[allow(clippy::too_many_arguments)]
fn append_transition(
    tx: &rusqlite::Transaction<'_>,
    qitem_id: &str,
    ts: &str,
    from_state: Option<&str>,
    state: &str,
    actor_session: &str,
    note: &str,
    closure_reason: Option<&str>,
    closure_target: Option<&str>,
) -> Result<(), String> {
    tx.execute(
        "INSERT INTO queue_transitions \
         (qitem_id, ts, state, from_state, actor_session, transition_note, closure_reason, closure_target) \
         VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)",
        rusqlite::params![
            qitem_id,
            ts,
            state,
            from_state,
            actor_session,
            note,
            closure_reason,
            closure_target
        ],
    )
    .map(|_| ())
    .map_err(|e| format!("cannot append transition for '{}': {}", qitem_id, e))
}

impl QueueStore {
    /// Opens (creating if absent) `<room_dir>/queue.sqlite` and applies the schema. Idempotent.
    pub fn open(room_dir: &Path) -> Result<Self, String> {
        std::fs::create_dir_all(room_dir)
            .map_err(|e| format!("cannot create room dir '{}': {}", room_dir.display(), e))?;
        let path = room_dir.join("queue.sqlite");
        let conn = rusqlite::Connection::open(&path)
            .map_err(|e| format!("cannot open queue store '{}': {}", path.display(), e))?;
        conn.busy_timeout(std::time::Duration::from_secs(5))
            .map_err(|e| format!("cannot configure queue store '{}': {}", path.display(), e))?;
        conn.execute_batch(SCHEMA)
            .map_err(|e| format!("cannot apply queue schema to '{}': {}", path.display(), e))?;
        Ok(Self { conn })
    }

    pub fn create(&self, input: CreateInput) -> Result<Row, String> {
        if input.source_session.trim().is_empty() {
            return Err("create requires a non-empty source_session".into());
        }
        if input.destination_session.trim().is_empty() {
            return Err("create requires a non-empty destination_session".into());
        }

        let qitem_id = match input.qitem_id.as_deref() {
            Some(id) if !id.trim().is_empty() => id.to_string(),
            _ => new_qitem_id(),
        };
        let ts = now_ts();
        let chain = serde_json::to_string(&input.chain_of_record)
            .map_err(|e| format!("cannot encode chain_of_record: {}", e))?;
        let tags =
            serde_json::to_string(&input.tags).map_err(|e| format!("cannot encode tags: {}", e))?;
        let priority = validate_typed_fields(&input)?;

        let tx = self
            .conn
            .unchecked_transaction()
            .map_err(|e| format!("cannot begin create transaction: {}", e))?;
        let inserted = tx
            .execute(
                "INSERT OR IGNORE INTO queue_rows \
                 (qitem_id, state, summary, body, source_session, destination_session, chain_of_record, \
                  reply_to, blocked_on, wake_after_seconds, closure_reason, closure_target, priority, tags, \
                  tier, ts_created, ts_updated) \
                 VALUES (?1, 'pending', ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, NULL, NULL, ?10, ?11, ?12, ?13, ?13)",
                rusqlite::params![
                    qitem_id,
                    input.summary,
                    input.body,
                    input.source_session,
                    input.destination_session,
                    chain,
                    input.reply_to,
                    input.blocked_on,
                    input.wake_after_seconds,
                    priority,
                    tags,
                    input.tier,
                    ts
                ],
            )
            .map_err(|e| format!("cannot create queue row '{}': {}", qitem_id, e))?;

        if inserted == 0 {
            // Retry identity: the caller reused a qitem_id it already created; return that row.
            tx.commit()
                .map_err(|e| format!("cannot commit create for '{}': {}", qitem_id, e))?;
            return self.show(&qitem_id);
        }

        append_transition(
            &tx,
            &qitem_id,
            &ts,
            None,
            "pending",
            &input.source_session,
            "created",
            None,
            None,
        )?;
        tx.commit()
            .map_err(|e| format!("cannot commit create for '{}': {}", qitem_id, e))?;
        self.show(&qitem_id)
    }

    /// `list` for a seat: the same identical read by the same session within five minutes, with no row
    /// created since, is refused with the newest row's id instead of re-sending the rail.
    pub fn list_rate_limited(
        &self,
        session: &str,
        filter: &ListFilter,
    ) -> Result<Vec<Row>, String> {
        self.list_rate_limited_at(session, filter, chrono::Utc::now().timestamp())
    }

    fn list_rate_limited_at(
        &self,
        session: &str,
        filter: &ListFilter,
        now: i64,
    ) -> Result<Vec<Row>, String> {
        let key = serde_json::to_string(&ListFilter {
            requester_session: None,
            ..filter.clone()
        })
        .map_err(|e| format!("cannot encode list filter: {}", e))?;
        let (newest, count): (Option<String>, i64) = self
            .conn
            .query_row(
                "SELECT (SELECT qitem_id FROM queue_rows ORDER BY ts_created DESC, qitem_id DESC LIMIT 1), \
                 COUNT(*) FROM queue_rows",
                [],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )
            .map_err(|e| format!("cannot read queue head: {}", e))?;
        let newest = newest.unwrap_or_default();
        let mark: Option<(i64, String, i64)> = self
            .conn
            .query_row(
                "SELECT listed_at, newest_qitem, row_count FROM queue_list_marks \
                 WHERE session = ?1 AND filter_key = ?2",
                rusqlite::params![session, key],
                |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
            )
            .ok();
        if let Some((at, prev_newest, prev_count)) = mark {
            if !newest.is_empty()
                && now - at < LIST_REPEAT_WINDOW_SECS
                && prev_newest == newest
                && prev_count == count
            {
                return Err(format!(
                    "list rate limit: no change since {} (same list by this session within {} min, no new rows)",
                    newest,
                    LIST_REPEAT_WINDOW_SECS / 60
                ));
            }
        }
        let rows = self.list(filter)?;
        self.conn
            .execute(
                "INSERT OR REPLACE INTO queue_list_marks (session, filter_key, listed_at, newest_qitem, row_count) \
                 VALUES (?1, ?2, ?3, ?4, ?5)",
                rusqlite::params![session, key, now, newest, count],
            )
            .map_err(|e| format!("cannot record list mark: {}", e))?;
        Ok(rows)
    }

    pub fn list(&self, filter: &ListFilter) -> Result<Vec<Row>, String> {
        let mut sql = format!("SELECT {} FROM queue_rows", ROW_COLUMNS);
        let mut clauses: Vec<String> = Vec::new();
        let mut args: Vec<rusqlite::types::Value> = Vec::new();

        if let Some(destination) = &filter.destination_session {
            args.push(destination.clone().into());
            clauses.push(format!("destination_session = ?{}", args.len()));
        }
        if let Some(source) = &filter.source_session {
            args.push(source.clone().into());
            clauses.push(format!("source_session = ?{}", args.len()));
        }
        if !filter.state.is_empty() {
            let mut holders = Vec::new();
            for state in &filter.state {
                args.push(state.clone().into());
                holders.push(format!("?{}", args.len()));
            }
            clauses.push(format!("state IN ({})", holders.join(", ")));
        }
        if filter.active_only {
            let holders: Vec<String> = ACTIVE_STATES
                .iter()
                .map(|s| format!("'{}'", s))
                .collect();
            clauses.push(format!("state IN ({})", holders.join(", ")));
        }
        if !clauses.is_empty() {
            sql.push_str(" WHERE ");
            sql.push_str(&clauses.join(" AND "));
        }
        sql.push_str(" ORDER BY ts_created DESC, qitem_id DESC");
        if let Some(limit) = filter.limit {
            args.push((limit as i64).into());
            sql.push_str(&format!(" LIMIT ?{}", args.len()));
        }

        let mut stmt = self
            .conn
            .prepare(&sql)
            .map_err(|e| format!("cannot prepare queue list: {}", e))?;
        let rows = stmt
            .query_map(rusqlite::params_from_iter(args.iter()), map_row)
            .map_err(|e| format!("cannot list queue rows: {}", e))?;

        let mut out = Vec::new();
        for row in rows {
            let mut row = row.map_err(|e| format!("cannot read queue row: {}", e))?;
            row.transitions = self.transitions_for(&row.qitem_id)?;
            out.push(row);
        }
        Ok(out)
    }

    /// One row **with its transitions** — the surface a note is readable on.
    pub fn show(&self, qitem_id: &str) -> Result<Row, String> {
        let mut row = self.row_by_id(qitem_id)?;
        row.transitions = self.transitions_for(qitem_id)?;
        Ok(row)
    }

    pub fn update(&self, input: UpdateInput) -> Result<Row, String> {
        if input.qitem_id.trim().is_empty() {
            return Err("update requires a non-empty qitem_id".into());
        }
        let current = self.row_by_id(&input.qitem_id)?;

        let requested_state = match &input.state {
            Some(state) => {
                if !STATES.contains(&state.as_str()) {
                    return Err(unknown_state(state));
                }
                Some(state.clone())
            }
            None => None,
        };
        if let Some(reason) = &input.closure_reason {
            if !CLOSURE_REASONS.contains(&reason.as_str()) {
                return Err(unknown_reason(reason));
            }
        }

        let note = input.transition_note.clone().unwrap_or_default();
        let effective_state = requested_state
            .clone()
            .unwrap_or_else(|| current.state.clone());
        let effective_reason = input
            .closure_reason
            .clone()
            .or_else(|| current.closure_reason.clone());
        let effective_target = input
            .closure_target
            .clone()
            .or_else(|| current.closure_target.clone());

        if effective_state == "done" && effective_reason.is_none() {
            return Err(format!(
                "state 'done' requires a closure_reason; valid reasons: {}",
                CLOSURE_REASONS.join(", ")
            ));
        }
        if effective_reason.as_deref() == Some("handed_off_to")
            && effective_target
                .as_deref()
                .map_or(true, |target| target.trim().is_empty())
        {
            return Err("closure_reason 'handed_off_to' requires a non-empty closure_target".into());
        }

        if let Some(summary) = &input.summary {
            check_summary(summary)?;
        }
        let has_fields = input.summary.is_some()
            || input.blocked_on.is_some()
            || input.wake_after_seconds.is_some()
            || input.closure_reason.is_some()
            || input.closure_target.is_some();
        if requested_state.is_none() && note.trim().is_empty() && !has_fields {
            return Err(
                "state_or_note_required: update needs a state, a transition_note, or a field change"
                    .into(),
            );
        }

        let ts = now_ts();
        let tx = self
            .conn
            .unchecked_transaction()
            .map_err(|e| format!("cannot begin update transaction: {}", e))?;
        tx.execute(
            "UPDATE queue_rows SET state = ?1, summary = ?2, blocked_on = ?3, wake_after_seconds = ?4, \
             closure_reason = ?5, closure_target = ?6, ts_updated = ?7 WHERE qitem_id = ?8",
            rusqlite::params![
                effective_state,
                input.summary.clone().unwrap_or(current.summary),
                input.blocked_on.clone().or(current.blocked_on),
                input.wake_after_seconds.or(current.wake_after_seconds),
                effective_reason,
                effective_target,
                ts,
                input.qitem_id
            ],
        )
        .map_err(|e| format!("cannot update queue row '{}': {}", input.qitem_id, e))?;

        // A state move appends from->to; a state-preserving write appends current->current so the
        // note lands on the audit trail even while the state stands still.
        append_transition(
            &tx,
            &input.qitem_id,
            &ts,
            Some(current.state.as_str()),
            &effective_state,
            &input.actor_session,
            &note,
            input.closure_reason.as_deref(),
            input.closure_target.as_deref(),
        )?;
        tx.commit()
            .map_err(|e| format!("cannot commit update for '{}': {}", input.qitem_id, e))?;
        self.show(&input.qitem_id)
    }

    pub fn claim(&self, qitem_id: &str, actor_session: &str) -> Result<Row, String> {
        let current = self.row_by_id(qitem_id)?;
        if current.state != "pending" {
            return Err(format!(
                "cannot claim '{}': current state is '{}'",
                qitem_id, current.state
            ));
        }

        let ts = now_ts();
        let tx = self
            .conn
            .unchecked_transaction()
            .map_err(|e| format!("cannot begin claim transaction: {}", e))?;
        tx.execute(
            "UPDATE queue_rows SET state = 'in-progress', ts_updated = ?1 WHERE qitem_id = ?2",
            rusqlite::params![ts, qitem_id],
        )
        .map_err(|e| format!("cannot claim queue row '{}': {}", qitem_id, e))?;
        append_transition(
            &tx,
            qitem_id,
            &ts,
            Some("pending"),
            "in-progress",
            actor_session,
            "claimed",
            None,
            None,
        )?;
        tx.commit()
            .map_err(|e| format!("cannot commit claim for '{}': {}", qitem_id, e))?;
        self.show(qitem_id)
    }

    pub fn close(&self, input: CloseInput) -> Result<Row, String> {
        if input.qitem_id.trim().is_empty() {
            return Err("close requires a non-empty qitem_id".into());
        }
        let reason = input.closure_reason.trim().to_string();
        if reason.is_empty() {
            return Err(format!(
                "close requires a closure_reason; valid reasons: {}",
                CLOSURE_REASONS.join(", ")
            ));
        }
        if !CLOSURE_REASONS.contains(&reason.as_str()) {
            return Err(unknown_reason(&reason));
        }
        let target = input
            .closure_target
            .clone()
            .filter(|t| !t.trim().is_empty());
        if reason == "handed_off_to" && target.is_none() {
            return Err("closure_reason 'handed_off_to' requires a non-empty closure_target".into());
        }

        let current = self.row_by_id(&input.qitem_id)?;
        if TERMINAL_STATES.contains(&current.state.as_str()) {
            return Err(format!(
                "queue row '{}' is already in terminal state '{}'",
                input.qitem_id, current.state
            ));
        }
        let note = if input.transition_note.trim().is_empty() {
            "closed".to_string()
        } else {
            input.transition_note.clone()
        };
        let closure_target = target.or_else(|| current.closure_target.clone());

        let ts = now_ts();
        let tx = self
            .conn
            .unchecked_transaction()
            .map_err(|e| format!("cannot begin close transaction: {}", e))?;
        if reason == "handed_off_to" {
            // A handoff is terminal for THIS row and lives on in the successor the same transaction
            // creates: the source rests at `handed-off` with the target recorded, and the target gets its
            // own `pending` row carrying the chain. (queue-repository.js:1263-1300 — the source row is
            // NOT set to done; closing it `done` would leave the receiver nothing to close, which is the
            // exact shape the four-hop acceptance checks.)
            let to = closure_target.clone().ok_or_else(|| {
                "closure_reason 'handed_off_to' requires a non-empty closure_target".to_string()
            })?;
            append_transition(
                &tx,
                &input.qitem_id,
                &ts,
                Some(current.state.as_str()),
                "handed-off",
                &input.actor_session,
                &note,
                Some(reason.as_str()),
                Some(to.as_str()),
            )?;
            tx.execute(
                "UPDATE queue_rows SET state = 'handed-off', closure_reason = 'handed_off_to', \
                 closure_target = ?1, ts_updated = ?2 WHERE qitem_id = ?3",
                rusqlite::params![to, ts, input.qitem_id],
            )
            .map_err(|e| format!("cannot hand off queue row '{}': {}", input.qitem_id, e))?;

            let successor = new_qitem_id();
            let mut chain = current.chain_of_record.clone();
            chain.push(current.qitem_id.clone());
            let chain_json = serde_json::to_string(&chain)
                .map_err(|e| format!("cannot encode chain_of_record: {}", e))?;
            let tags_json = serde_json::to_string(&current.tags)
                .map_err(|e| format!("cannot encode tags: {}", e))?;
            tx.execute(
                "INSERT INTO queue_rows \
                 (qitem_id, state, summary, body, source_session, destination_session, chain_of_record, \
                  reply_to, blocked_on, wake_after_seconds, closure_reason, closure_target, priority, tags, \
                  tier, ts_created, ts_updated) \
                 VALUES (?1, 'pending', ?2, ?3, ?4, ?5, ?6, NULL, NULL, NULL, NULL, NULL, ?7, ?8, ?9, ?10, ?10)",
                rusqlite::params![
                    successor,
                    current.summary,
                    current.body,
                    input.actor_session,
                    to,
                    chain_json,
                    current.priority,
                    tags_json,
                    current.tier,
                    ts
                ],
            )
            .map_err(|e| format!("cannot create handoff successor '{}': {}", successor, e))?;
            append_transition(
                &tx,
                &successor,
                &ts,
                None,
                "pending",
                &input.actor_session,
                &format!("handoff from {}", input.qitem_id),
                None,
                None,
            )?;
        } else {
            append_transition(
                &tx,
                &input.qitem_id,
                &ts,
                Some(current.state.as_str()),
                "done",
                &input.actor_session,
                &note,
                Some(reason.as_str()),
                closure_target.as_deref(),
            )?;
            tx.execute(
                "UPDATE queue_rows SET state = 'done', closure_reason = ?1, closure_target = ?2, \
                 ts_updated = ?3 WHERE qitem_id = ?4",
                rusqlite::params![reason, closure_target, ts, input.qitem_id],
            )
            .map_err(|e| format!("cannot close queue row '{}': {}", input.qitem_id, e))?;
        }
        tx.commit()
            .map_err(|e| format!("cannot commit close for '{}': {}", input.qitem_id, e))?;
        self.show(&input.qitem_id)
    }

    fn row_by_id(&self, qitem_id: &str) -> Result<Row, String> {
        let mut stmt = self
            .conn
            .prepare(&format!(
                "SELECT {} FROM queue_rows WHERE qitem_id = ?1",
                ROW_COLUMNS
            ))
            .map_err(|e| format!("cannot prepare queue read: {}", e))?;
        let mut rows = stmt
            .query_map(rusqlite::params![qitem_id], map_row)
            .map_err(|e| format!("cannot read queue row '{}': {}", qitem_id, e))?;
        match rows.next() {
            Some(Ok(row)) => Ok(row),
            Some(Err(e)) => Err(format!("cannot read queue row '{}': {}", qitem_id, e)),
            None => Err(format!("unknown qitem_id '{}'", qitem_id)),
        }
    }

    fn transitions_for(&self, qitem_id: &str) -> Result<Vec<Transition>, String> {
        let mut stmt = self
            .conn
            .prepare(
                "SELECT transition_id, qitem_id, ts, state, from_state, actor_session, \
                 transition_note, closure_reason, closure_target FROM queue_transitions \
                 WHERE qitem_id = ?1 ORDER BY transition_id ASC",
            )
            .map_err(|e| format!("cannot prepare transition read: {}", e))?;
        let rows = stmt
            .query_map(rusqlite::params![qitem_id], |r| {
                Ok(Transition {
                    transition_id: r.get(0)?,
                    qitem_id: r.get(1)?,
                    ts: r.get(2)?,
                    state: r.get(3)?,
                    from_state: r.get(4)?,
                    actor_session: r.get(5)?,
                    transition_note: r.get(6)?,
                    closure_reason: r.get(7)?,
                    closure_target: r.get(8)?,
                })
            })
            .map_err(|e| format!("cannot read transitions for '{}': {}", qitem_id, e))?;

        let mut out = Vec::new();
        for row in rows {
            out.push(row.map_err(|e| format!("cannot read transition for '{}': {}", qitem_id, e))?);
        }
        Ok(out)
    }
}

/// A handle on one room's rail.
///
/// Opening is per use, never cached: the file can be replaced under a live daemon (a rebuilt room, a
/// restore, an instrument's own `rm -f queue.sqlite`), so a handle that outlives the request that opened it
/// is a handle on a file that may no longer be there - and a write into a deleted inode is answered
/// `attempt to write a readonly database` while a read keeps serving the old rows as fact.
pub struct QueueStore {
    conn: rusqlite::Connection,
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;
    use std::time::Duration;

    /// A fresh room dir per test: nothing in these tests may see another test's rows.
    fn temp_store() -> (PathBuf, QueueStore) {
        let dir = std::env::temp_dir().join(uuid::Uuid::new_v4().to_string());
        std::fs::create_dir_all(&dir).expect("temp room dir");
        let store = QueueStore::open(&dir).expect("open store");
        (dir, store)
    }

    fn new_row(store: &QueueStore, source: &str, destination: &str, summary: &str) -> Row {
        store
            .create(CreateInput {
                source_session: source.into(),
                destination_session: destination.into(),
                summary: summary.into(),
                ..Default::default()
            })
            .expect("create row")
    }

    fn close_input(qitem_id: &str, reason: &str, target: Option<&str>) -> CloseInput {
        CloseInput {
            qitem_id: qitem_id.into(),
            actor_session: "seat-closer".into(),
            closure_reason: reason.into(),
            closure_target: target.map(|t| t.into()),
            transition_note: String::new(),
        }
    }

    #[test]
    fn create_claim_close_round_trip() {
        let (_dir, store) = temp_store();
        let row = new_row(&store, "seat-a", "seat-b", "hand me the build log");
        assert_eq!(row.state, "pending");
        assert_eq!(row.transitions.len(), 1);
        assert_eq!(row.transitions[0].from_state, None);
        assert_eq!(row.transitions[0].state, "pending");
        assert_eq!(row.transitions[0].actor_session, "seat-a");
        assert_eq!(row.transitions[0].transition_note, "created");

        let claimed = store.claim(&row.qitem_id, "seat-b").expect("claim");
        assert_eq!(claimed.state, "in-progress");
        assert_eq!(claimed.transitions.len(), 2);

        let closed = store
            .close(CloseInput {
                qitem_id: row.qitem_id.clone(),
                actor_session: "seat-c".into(),
                closure_reason: "handed_off_to".into(),
                closure_target: Some("seat-d".into()),
                transition_note: "over to seat-d".into(),
            })
            .expect("close");
        // A handoff is terminal for the source and rests at `handed-off` — NOT `done`: the receiver's row
        // is the successor the same act creates, and it is the one that can be closed. Setting the source
        // `done` (as this store first did) left the receiver nothing to close.
        assert_eq!(closed.state, "handed-off");
        assert_eq!(closed.closure_reason.as_deref(), Some("handed_off_to"));
        assert_eq!(closed.closure_target.as_deref(), Some("seat-d"));

        // the row the caller was told is the row the next read shows
        let reread = store.show(&row.qitem_id).expect("show");
        assert_eq!(reread, closed);
        assert_eq!(reread.transitions.len(), 3);
        let trail: Vec<(Option<String>, String, String)> = reread
            .transitions
            .iter()
            .map(|t| (t.from_state.clone(), t.state.clone(), t.actor_session.clone()))
            .collect();
        assert_eq!(
            trail,
            vec![
                (None, "pending".into(), "seat-a".into()),
                (
                    Some("pending".into()),
                    "in-progress".into(),
                    "seat-b".into()
                ),
                (
                    Some("in-progress".into()),
                    "handed-off".into(),
                    "seat-c".into()
                ),
            ]
        );
        assert!(reread
            .transitions
            .windows(2)
            .all(|w| w[0].transition_id < w[1].transition_id));

        // the successor belongs to the target, carries the chain, and is the row that closes
        let successors = store
            .list(&ListFilter {
                destination_session: Some("seat-d".into()),
                ..Default::default()
            })
            .expect("list for the target");
        assert_eq!(successors.len(), 1, "one successor for the target: {successors:?}");
        let successor = &successors[0];
        assert_ne!(successor.qitem_id, row.qitem_id);
        assert_eq!(successor.state, "pending");
        assert_eq!(successor.source_session, "seat-c");
        assert_eq!(successor.summary, "hand me the build log");
        assert_eq!(successor.chain_of_record, vec![row.qitem_id.clone()]);
        assert_eq!(successor.transitions.len(), 1);
        assert_eq!(
            successor.transitions[0].transition_note,
            format!("handoff from {}", row.qitem_id)
        );

        // the source is terminal now: it is not closable again
        let err = store
            .close(CloseInput {
                qitem_id: row.qitem_id.clone(),
                actor_session: "seat-d".into(),
                closure_reason: "no-follow-on".into(),
                closure_target: None,
                transition_note: String::new(),
            })
            .unwrap_err();
        assert!(err.contains("terminal"), "{}", err);

        let done = store
            .close(CloseInput {
                qitem_id: successor.qitem_id.clone(),
                actor_session: "seat-d".into(),
                closure_reason: "no-follow-on".into(),
                closure_target: None,
                transition_note: String::new(),
            })
            .expect("the receiver closes its own row");
        assert_eq!(done.state, "done");
        assert_eq!(done.transitions.len(), 2);
        assert_eq!(
            (done.transitions[1].from_state.as_deref(), done.transitions[1].state.as_str()),
            (Some("pending"), "done")
        );
    }

    #[test]
    fn close_without_reason_or_target_is_refused_and_changes_nothing() {
        let (_dir, store) = temp_store();
        let row = new_row(&store, "seat-a", "seat-b", "x");

        let err = store
            .close(close_input(&row.qitem_id, "   ", None))
            .unwrap_err();
        assert!(err.contains("closure_reason"), "{}", err);

        let err = store
            .close(close_input(&row.qitem_id, "handed_off_to", None))
            .unwrap_err();
        assert!(err.contains("closure_target"), "{}", err);

        let after = store.show(&row.qitem_id).expect("show");
        assert_eq!(after.state, "pending");
        assert!(after.closure_reason.is_none());
        assert!(after.closure_target.is_none());
        assert_eq!(after.transitions.len(), 1);
    }

    #[test]
    fn unknown_state_and_unknown_closure_reason_list_valid_values() {
        let (_dir, store) = temp_store();
        let row = new_row(&store, "seat-a", "seat-b", "x");

        let err = store
            .update(UpdateInput {
                qitem_id: row.qitem_id.clone(),
                actor_session: "seat-a".into(),
                state: Some("wat".into()),
                ..Default::default()
            })
            .unwrap_err();
        assert!(err.contains("wat"), "{}", err);
        for state in STATES {
            assert!(err.contains(state), "{} missing from: {}", state, err);
        }

        let err = store
            .close(close_input(&row.qitem_id, "bogus", None))
            .unwrap_err();
        assert!(err.contains("bogus"), "{}", err);
        for reason in CLOSURE_REASONS {
            assert!(err.contains(reason), "{} missing from: {}", reason, err);
        }

        // done without a reason names the missing piece
        let err = store
            .update(UpdateInput {
                qitem_id: row.qitem_id.clone(),
                actor_session: "seat-a".into(),
                state: Some("done".into()),
                ..Default::default()
            })
            .unwrap_err();
        assert!(err.contains("closure_reason"), "{}", err);

        assert_eq!(store.show(&row.qitem_id).unwrap().state, "pending");
    }

    #[test]
    fn note_only_update_appends_state_preserving_transition() {
        let (_dir, store) = temp_store();
        let row = new_row(&store, "seat-a", "seat-b", "x");

        let updated = store
            .update(UpdateInput {
                qitem_id: row.qitem_id.clone(),
                actor_session: "seat-b".into(),
                transition_note: Some("ping: still on it".into()),
                ..Default::default()
            })
            .expect("note-only update");
        assert_eq!(updated.state, "pending");
        assert_eq!(updated.transitions.len(), 2);
        let last = updated.transitions.last().unwrap();
        assert_eq!(last.from_state.as_deref(), Some("pending"));
        assert_eq!(last.state, "pending");
        assert_eq!(last.actor_session, "seat-b");
        assert_eq!(last.transition_note, "ping: still on it");

        // the note is readable through show
        let shown = store.show(&row.qitem_id).expect("show");
        assert_eq!(shown.state, "pending");
        assert_eq!(shown.transitions.len(), 2);
        assert_eq!(
            shown.transitions.last().unwrap().transition_note,
            "ping: still on it"
        );
    }

    #[test]
    fn noop_update_is_refused() {
        let (_dir, store) = temp_store();
        let row = new_row(&store, "seat-a", "seat-b", "x");

        let err = store
            .update(UpdateInput {
                qitem_id: row.qitem_id.clone(),
                actor_session: "seat-a".into(),
                ..Default::default()
            })
            .unwrap_err();
        assert!(err.contains("state_or_note_required"), "{}", err);

        let err = store
            .update(UpdateInput {
                qitem_id: row.qitem_id.clone(),
                actor_session: "seat-a".into(),
                transition_note: Some("   ".into()),
                ..Default::default()
            })
            .unwrap_err();
        assert!(err.contains("state_or_note_required"), "{}", err);
        assert_eq!(store.show(&row.qitem_id).unwrap().transitions.len(), 1);
    }

    #[test]
    fn claim_of_a_claimable_but_not_pending_row_is_refused() {
        let (_dir, store) = temp_store();
        let row = new_row(&store, "seat-a", "seat-b", "x");
        store.claim(&row.qitem_id, "seat-b").expect("first claim");

        let err = store.claim(&row.qitem_id, "seat-c").unwrap_err();
        assert!(err.contains(&row.qitem_id), "{}", err);
        assert!(err.contains("in-progress"), "{}", err);
        assert_eq!(store.show(&row.qitem_id).unwrap().transitions.len(), 2);
    }

    #[test]
    fn blocked_on_and_wake_after_seconds_round_trip() {
        let (_dir, store) = temp_store();
        let row = store
            .create(CreateInput {
                source_session: "seat-a".into(),
                destination_session: "seat-b".into(),
                summary: "blocked on ci".into(),
                blocked_on: Some("ci green".into()),
                wake_after_seconds: Some(300),
                ..Default::default()
            })
            .expect("create");
        assert_eq!(row.blocked_on.as_deref(), Some("ci green"));
        assert_eq!(row.wake_after_seconds, Some(300));

        let updated = store
            .update(UpdateInput {
                qitem_id: row.qitem_id.clone(),
                actor_session: "seat-a".into(),
                blocked_on: Some("review".into()),
                wake_after_seconds: Some(600),
                summary: Some("blocked on review".into()),
                ..Default::default()
            })
            .expect("update");
        assert_eq!(updated.blocked_on.as_deref(), Some("review"));
        assert_eq!(updated.wake_after_seconds, Some(600));
        assert_eq!(updated.summary, "blocked on review");
        assert_eq!(store.show(&row.qitem_id).unwrap(), updated);
    }

    #[test]
    fn chain_of_record_and_reply_to_round_trip() {
        let (_dir, store) = temp_store();
        let row = store
            .create(CreateInput {
                source_session: "seat-a".into(),
                destination_session: "seat-b".into(),
                summary: "lineage".into(),
                chain_of_record: vec!["qitem-1".into(), "qitem-2".into()],
                reply_to: Some("qitem-2".into()),
                tags: vec!["status".into(), "prod-break".into()],
                priority: Some("high".into()),
                tier: Some("T1".into()),
                ..Default::default()
            })
            .expect("create");

        let shown = store.show(&row.qitem_id).expect("show");
        assert_eq!(shown.chain_of_record, vec!["qitem-1", "qitem-2"]);
        assert_eq!(shown.reply_to.as_deref(), Some("qitem-2"));
        assert_eq!(shown.tags, vec!["status", "prod-break"]);
        assert_eq!(shown.priority, "high");
        assert_eq!(shown.tier.as_deref(), Some("T1"));
    }

    #[test]
    fn generated_id_shape_and_retry_identity() {
        let (_dir, store) = temp_store();
        let row = new_row(&store, "seat-a", "seat-b", "x");

        let rest = row
            .qitem_id
            .strip_prefix("qitem-")
            .expect("qitem- prefix");
        let (stamp, hex) = rest.split_once('-').expect("stamp-hex split");
        assert_eq!(stamp.len(), 14, "{}", row.qitem_id);
        assert!(stamp.chars().all(|c| c.is_ascii_digit()));
        assert_eq!(hex.len(), 12);
        assert!(hex
            .chars()
            .all(|c| c.is_ascii_hexdigit() && !c.is_ascii_uppercase()));

        // reuse of the id is the retry path: same row, no second row, no second transition
        let again = store
            .create(CreateInput {
                source_session: "seat-a".into(),
                destination_session: "seat-b".into(),
                summary: "x".into(),
                qitem_id: Some(row.qitem_id.clone()),
                ..Default::default()
            })
            .expect("retry create");
        assert_eq!(again.qitem_id, row.qitem_id);
        assert_eq!(again.transitions.len(), 1);
        assert_eq!(store.list(&ListFilter::default()).unwrap().len(), 1);
    }

    #[test]
    fn create_rejects_empty_sessions() {
        let (_dir, store) = temp_store();
        let err = store
            .create(CreateInput {
                source_session: "  ".into(),
                destination_session: "seat-b".into(),
                summary: "x".into(),
                ..Default::default()
            })
            .unwrap_err();
        assert!(err.contains("source_session"), "{}", err);

        let err = store
            .create(CreateInput {
                source_session: "seat-a".into(),
                destination_session: String::new(),
                summary: "x".into(),
                ..Default::default()
            })
            .unwrap_err();
        assert!(err.contains("destination_session"), "{}", err);
        assert!(store.list(&ListFilter::default()).unwrap().is_empty());
    }

    #[test]
    fn list_filters_and_limit() {
        let (_dir, store) = temp_store();
        let r1 = new_row(&store, "seat-a", "seat-b", "one");
        std::thread::sleep(Duration::from_millis(2));
        let r2 = new_row(&store, "seat-a", "seat-c", "two");
        std::thread::sleep(Duration::from_millis(2));
        let r3 = new_row(&store, "seat-z", "seat-b", "three");
        store.claim(&r2.qitem_id, "seat-c").expect("claim");
        store
            .close(close_input(&r3.qitem_id, "denied", None))
            .expect("close");

        // a store with rows never answers empty
        let all = store.list(&ListFilter::default()).expect("list all");
        assert_eq!(all.len(), 3);
        assert!(all.iter().all(|r| !r.transitions.is_empty()));
        // newest-first by ts_created
        assert_eq!(all[0].qitem_id, r3.qitem_id);
        assert_eq!(all[1].qitem_id, r2.qitem_id);
        assert_eq!(all[2].qitem_id, r1.qitem_id);
        assert_eq!(all[0].state, "done");
        assert_eq!(all[0].transitions.last().unwrap().transition_note, "closed");
        assert_eq!(
            all[0].transitions.last().unwrap().closure_reason.as_deref(),
            Some("denied")
        );

        let limited = store
            .list(&ListFilter {
                limit: Some(2),
                ..Default::default()
            })
            .expect("list limit");
        assert_eq!(limited.len(), 2);
        assert_eq!(limited[0].qitem_id, r3.qitem_id);

        let by_destination = store
            .list(&ListFilter {
                destination_session: Some("seat-b".into()),
                ..Default::default()
            })
            .expect("list by destination");
        assert_eq!(by_destination.len(), 2);
        assert!(by_destination
            .iter()
            .all(|r| r.destination_session == "seat-b"));

        let by_source = store
            .list(&ListFilter {
                source_session: Some("seat-a".into()),
                ..Default::default()
            })
            .expect("list by source");
        assert_eq!(by_source.len(), 2);

        let by_state = store
            .list(&ListFilter {
                state: vec!["done".into()],
                ..Default::default()
            })
            .expect("list by state");
        assert_eq!(by_state.len(), 1);
        assert_eq!(by_state[0].qitem_id, r3.qitem_id);

        let active = store
            .list(&ListFilter {
                active_only: true,
                ..Default::default()
            })
            .expect("list active");
        assert_eq!(active.len(), 2);
        assert!(active.iter().all(|r| r.state != "done"));
        assert!(active
            .iter()
            .all(|r| ACTIVE_STATES.contains(&r.state.as_str())));
    }

    #[test]
    fn unknown_ids_are_named_in_errors() {
        let (_dir, store) = temp_store();
        let err = store.show("qitem-does-not-exist").unwrap_err();
        assert!(err.contains("qitem-does-not-exist"), "{}", err);

        let err = store.claim("qitem-nope", "seat-a").unwrap_err();
        assert!(err.contains("qitem-nope"), "{}", err);

        let err = store
            .close(close_input("qitem-nope", "denied", None))
            .unwrap_err();
        assert!(err.contains("qitem-nope"), "{}", err);

        let err = store
            .update(UpdateInput {
                qitem_id: "qitem-nope".into(),
                actor_session: "seat-a".into(),
                state: Some("blocked".into()),
                ..Default::default()
            })
            .unwrap_err();
        assert!(err.contains("qitem-nope"), "{}", err);
    }

    #[test]
    fn second_open_on_the_same_room_sees_the_first_stores_rows() {
        let (dir, first) = temp_store();
        let row = new_row(&first, "seat-a", "seat-b", "shared rail");

        let second = QueueStore::open(&dir).expect("second open");
        let seen = second.list(&ListFilter::default()).expect("list");
        assert_eq!(seen.len(), 1);
        assert_eq!(seen[0].qitem_id, row.qitem_id);
        assert_eq!(seen[0].summary, "shared rail");

        second.claim(&row.qitem_id, "seat-b").expect("claim");
        let from_first = first.show(&row.qitem_id).expect("show");
        assert_eq!(from_first.state, "in-progress");
        assert_eq!(from_first.transitions.len(), 2);
    }

    fn typed(priority: Option<&str>, tags: &[&str], summary: &str, body: &str) -> CreateInput {
        CreateInput {
            source_session: "seat-a".into(),
            destination_session: "seat-b".into(),
            summary: summary.into(),
            body: body.into(),
            priority: priority.map(|p| p.into()),
            tags: tags.iter().map(|t| t.to_string()).collect(),
            ..Default::default()
        }
    }

    #[test]
    fn priority_outside_the_enum_is_refused_naming_the_rule() {
        let (_dir, store) = temp_store();
        let err = store.create(typed(Some("urgent"), &[], "s", "")).unwrap_err();
        assert!(err.contains("priority rule") && err.contains("low, normal, high"), "{err}");
        assert!(store.list(&ListFilter::default()).unwrap().is_empty());
    }

    #[test]
    fn high_requires_the_prod_break_tag() {
        let (_dir, store) = temp_store();
        let err = store.create(typed(Some("high"), &["status"], "s", "")).unwrap_err();
        assert!(err.contains("'high' requires tag 'prod-break'"), "{err}");
        let row = store
            .create(typed(Some("high"), &["prod-break"], "s", ""))
            .expect("high with prod-break");
        assert_eq!(row.priority, "high");
    }

    #[test]
    fn summary_over_120_chars_is_refused() {
        let (_dir, store) = temp_store();
        store.create(typed(None, &[], &"a".repeat(120), "")).expect("120 fits");
        let err = store.create(typed(None, &[], &"a".repeat(121), "")).unwrap_err();
        assert!(err.contains("summary cap") && err.contains("120"), "{err}");
    }

    #[test]
    fn summary_update_over_120_chars_is_refused() {
        let (_dir, store) = temp_store();
        let row = new_row(&store, "seat-a", "seat-b", "short");
        let err = store
            .update(UpdateInput {
                qitem_id: row.qitem_id,
                actor_session: "seat-a".into(),
                summary: Some("a".repeat(121)),
                ..Default::default()
            })
            .unwrap_err();
        assert!(err.contains("summary cap"), "{err}");
    }

    #[test]
    fn body_over_600_chars_is_refused() {
        let (_dir, store) = temp_store();
        store.create(typed(None, &[], "s", &"b".repeat(600))).expect("600 fits");
        let err = store.create(typed(None, &[], "s", &"b".repeat(601))).unwrap_err();
        assert!(err.contains("body cap") && err.contains("600"), "{err}");
    }

    #[test]
    fn tags_outside_the_allowed_kebab_set_are_refused() {
        let (_dir, store) = temp_store();
        for bad in ["urgent", "GATE", "Owner_Decision"] {
            let err = store.create(typed(None, &[bad], "s", "")).unwrap_err();
            assert!(err.contains("tag rule") && err.contains(bad), "{err}");
        }
        store.create(typed(None, &["gate", "owner-decision"], "s", "")).expect("allowed tags");
    }

    #[test]
    fn repeated_identical_list_with_no_new_row_is_answered_no_change() {
        let (_dir, store) = temp_store();
        let first = new_row(&store, "seat-a", "seat-b", "one");
        let filter = ListFilter::default();
        assert_eq!(store.list_rate_limited_at("seat-x", &filter, 1000).unwrap().len(), 1);
        let err = store.list_rate_limited_at("seat-x", &filter, 1100).unwrap_err();
        assert!(err.contains(&format!("no change since {}", first.qitem_id)), "{err}");
        // another session, another filter, and a later window are each answered in full
        assert!(store.list_rate_limited_at("seat-y", &filter, 1100).is_ok());
        let other = ListFilter { active_only: true, ..Default::default() };
        assert!(store.list_rate_limited_at("seat-x", &other, 1100).is_ok());
        assert!(store.list_rate_limited_at("seat-x", &filter, 1000 + LIST_REPEAT_WINDOW_SECS).is_ok());
        // a new row ends the limit
        new_row(&store, "seat-a", "seat-b", "two");
        assert_eq!(store.list_rate_limited_at("seat-x", &filter, 1400).unwrap().len(), 2);
    }
}
