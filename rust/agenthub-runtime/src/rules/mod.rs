use crate::types::PermissionPolicy;
use regex::RegexSet;

pub struct PolicyEngine {
    common_bash_deny_set: RegexSet,
    non_lead_bash_deny_set: RegexSet,
    non_lead_tools: Vec<&'static str>,
    reviewer_denied_tools: Vec<&'static str>,
}

impl Default for PolicyEngine {
    fn default() -> Self {
        Self::new()
    }
}

impl PolicyEngine {
    pub fn new() -> Self {
        // Patterns anchored or converted to regex
        let common_patterns = [
            r"^git\s+push",
            r"^git\s+reset\s+--hard",
            r"^git\s+clean",
            r"^git\s+checkout\s+--",
            r"^git\s+stash\s+drop",
            r"^git\s+stash\s+clear",
            r"^git\s+commit.*--amend",
            r"^git\s+add\s+-A",
            r"^git\s+add\s+--all",
            r"^git\s+add\s+\.",
            r"^rm\s+(-rf|-fr|-[a-zA-Z]*r[a-zA-Z]*f|-[a-zA-Z]*f[a-zA-Z]*r)",
            r"^sudo\s+",
            r"^ssh\s+",
            r"^scp\s+",
            r"^docker\s+",
            r"^tmux\s+kill-",
            r"^pkill\s+",
            r"^killall\s+",
            r"^printenv",
            r"cat.*deepseek/env",
            r"^rig\s+down",
            r"^rig\s+up",
            r"^rig\s+remove",
        ];

        let non_lead_patterns = [
            r"^rig\s+launch",
            r"^rig\s+seat\s+stop",
            r"^rig\s+seat\s+launch",
            r"^rig\s+seat\s+clean",
            r"^rig\s+seat\s+set-",
        ];

        let common_bash_deny_set = RegexSet::new(common_patterns).expect("valid regexes");
        let non_lead_bash_deny_set = RegexSet::new(non_lead_patterns).expect("valid regexes");

        let non_lead_tools = vec![
            "mcp__agenthub_http_manage_agent",
            "mcp__agenthub_http_manage_connection",
            "mcp__agenthub_http_manage_seat",
        ];

        let reviewer_denied_tools = vec!["edit", "ast_edit"];

        Self {
            common_bash_deny_set,
            non_lead_bash_deny_set,
            non_lead_tools,
            reviewer_denied_tools,
        }
    }

    /// Check if a bash command is permitted for a seat given its role and policy.
    pub fn check_bash_command(
        &self,
        command: &str,
        role: &str,
        policy: PermissionPolicy,
    ) -> Result<(), &'static str> {
        let trimmed = command.trim();

        // Locked seats may not run any bash commands unattended
        if policy == PermissionPolicy::Locked {
            return Err("seat policy is locked: no shell commands permitted");
        }

        // Common denies apply even in standard and open
        if self.common_bash_deny_set.is_match(trimmed) {
            return Err("command violates repository commonBashDeny safety rail");
        }

        // Sub-commands inside compound chains e.g. "cd foo && git push"
        for subcmd in trimmed.split("&&").chain(trimmed.split(';')).chain(trimmed.split("||")) {
            let sub = subcmd.trim();
            if self.common_bash_deny_set.is_match(sub) {
                return Err("sub-command violates repository commonBashDeny safety rail");
            }
        }

        // Non-lead bash deny
        if role != "lead" && self.non_lead_bash_deny_set.is_match(trimmed) {
            return Err("seat role lacks permission for rig management commands (nonLeadBashDeny)");
        }

        Ok(())
    }

    /// Check if a tool invocation is permitted for a seat.
    pub fn check_tool_call(
        &self,
        tool: &str,
        role: &str,
        policy: PermissionPolicy,
    ) -> Result<(), &'static str> {
        if policy == PermissionPolicy::Locked {
            return Err("seat policy is locked: all tool calls denied");
        }

        if role != "lead" && self.non_lead_tools.contains(&tool) {
            return Err("tool is restricted to lead seat (nonLeadToolDeny)");
        }

        if role == "reviewer" && self.reviewer_denied_tools.contains(&tool) {
            return Err("reviewer role is read-only and may not modify code (reviewerToolDeny)");
        }

        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_common_bash_denies() {
        let engine = PolicyEngine::new();
        assert!(engine.check_bash_command("git push origin main", "dev", PermissionPolicy::Standard).is_err());
        assert!(engine.check_bash_command("git add -A", "dev", PermissionPolicy::Standard).is_err());
        assert!(engine.check_bash_command("git add .", "dev", PermissionPolicy::Standard).is_err());
        assert!(engine.check_bash_command("rm -rf /", "dev", PermissionPolicy::Standard).is_err());
        assert!(engine.check_bash_command("cd /foo && git push", "dev", PermissionPolicy::Standard).is_err());
        assert!(engine.check_bash_command("git status", "dev", PermissionPolicy::Standard).is_ok());
        assert!(engine.check_bash_command("cargo test", "dev", PermissionPolicy::Standard).is_ok());
    }

    #[test]
    fn test_role_tool_denies() {
        let engine = PolicyEngine::new();
        assert!(engine.check_tool_call("mcp__agenthub_http_manage_seat", "dev", PermissionPolicy::Standard).is_err());
        assert!(engine.check_tool_call("mcp__agenthub_http_manage_seat", "lead", PermissionPolicy::Standard).is_ok());
        assert!(engine.check_tool_call("edit", "reviewer", PermissionPolicy::Standard).is_err());
        assert!(engine.check_tool_call("edit", "dev", PermissionPolicy::Standard).is_ok());
    }
}
