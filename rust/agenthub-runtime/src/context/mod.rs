
pub const DEFAULT_LIMIT_TOKENS: usize = 150_000;
pub const DEFAULT_WARN_TOKENS: usize = 250_000;
pub const DEFAULT_HARD_TOKENS: usize = 300_000;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum CompactionAction {
    Normal,
    SafePointNeeded,
    UrgentCompact,
    HardForceCompact,
}

pub struct ContextTracker {
    limit_tokens: usize,
    warn_tokens: usize,
    hard_tokens: usize,
}

impl Default for ContextTracker {
    fn default() -> Self {
        Self {
            limit_tokens: DEFAULT_LIMIT_TOKENS,
            warn_tokens: DEFAULT_WARN_TOKENS,
            hard_tokens: DEFAULT_HARD_TOKENS,
        }
    }
}

impl ContextTracker {
    pub fn new(limit: Option<usize>, warn: Option<usize>, hard: Option<usize>) -> Self {
        Self {
            limit_tokens: limit.unwrap_or(DEFAULT_LIMIT_TOKENS),
            warn_tokens: warn.unwrap_or(DEFAULT_WARN_TOKENS),
            hard_tokens: hard.unwrap_or(DEFAULT_HARD_TOKENS),
        }
    }

    /// Fast token estimator (~3.8 - 4 characters per token average for code/english).
    pub fn estimate_tokens(text: &str) -> usize {
        let bytes = text.len();
        if bytes == 0 {
            return 0;
        }
        // Count whitespace words + punctuation
        let word_count = text.split_whitespace().count();
        // Weighted heuristic: average between byte count / 4 and word count * 1.33
        let byte_est = bytes / 4;
        let word_est = (word_count * 4) / 3;
        (byte_est + word_est) / 2
    }

    /// Assess current token count against compaction limits.
    pub fn assess_threshold(&self, tokens: usize) -> CompactionAction {
        if tokens >= self.hard_tokens {
            CompactionAction::HardForceCompact
        } else if tokens >= self.warn_tokens {
            CompactionAction::UrgentCompact
        } else if tokens >= self.limit_tokens {
            CompactionAction::SafePointNeeded
        } else {
            CompactionAction::Normal
        }
    }

    /// Assemble 4-tier context hierarchy.
    pub fn assemble_context(
        company_overlay: Option<&str>,
        room_guide: Option<&str>,
        seat_role: Option<&str>,
        task_context: Option<&str>,
    ) -> String {
        let mut out = String::new();
        if let Some(co) = company_overlay {
            out.push_str("<!-- Tier 1: Company Overlay -->\n");
            out.push_str(co.trim());
            out.push_str("\n\n");
        }
        if let Some(rg) = room_guide {
            out.push_str("<!-- Tier 2: Room Guidelines -->\n");
            out.push_str(rg.trim());
            out.push_str("\n\n");
        }
        if let Some(sr) = seat_role {
            out.push_str("<!-- Tier 3: Seat Role -->\n");
            out.push_str(sr.trim());
            out.push_str("\n\n");
        }
        if let Some(tc) = task_context {
            out.push_str("<!-- Tier 4: Task Context -->\n");
            out.push_str(tc.trim());
            out.push_str("\n");
        }
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_token_estimation() {
        let text = "Hello world! This is a test of token estimation in Rust.";
        let tokens = ContextTracker::estimate_tokens(text);
        assert!(tokens > 5 && tokens < 25);
    }

    #[test]
    fn test_threshold_assessments() {
        let tracker = ContextTracker::default();
        assert_eq!(tracker.assess_threshold(50_000), CompactionAction::Normal);
        assert_eq!(tracker.assess_threshold(160_000), CompactionAction::SafePointNeeded);
        assert_eq!(tracker.assess_threshold(260_000), CompactionAction::UrgentCompact);
        assert_eq!(tracker.assess_threshold(310_000), CompactionAction::HardForceCompact);
    }

    #[test]
    fn test_context_assembly() {
        let ctx = ContextTracker::assemble_context(
            Some("Company Policy"),
            Some("Room Rules"),
            Some("Developer Role"),
            Some("Fix Bug #123"),
        );
        assert!(ctx.contains("Tier 1: Company Overlay"));
        assert!(ctx.contains("Tier 2: Room Guidelines"));
        assert!(ctx.contains("Tier 3: Seat Role"));
        assert!(ctx.contains("Tier 4: Task Context"));
    }
}
