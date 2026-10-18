---
name: verification-before-completion
description: Use when about to claim work is complete, fixed, or passing, before committing or creating PRs - requires running verification commands and confirming output before making any success claims; evidence before assertions always
metadata:
  agenthub:
    vendored_from: "Obra Superpowers (https://github.com/obra/superpowers)"
    vendoring_pattern: modify-the-file
    last_upstream_check: "2026-05-13 (historical upstream comparison; a local harness section on services and long jobs added 2026-10-17)"
---

# Verification Before Completion

## Overview

Claiming work is complete without verification is dishonesty, not efficiency.

**Core principle:** Evidence before claims, always.

**This rule serves shipping — it is not a ritual that outranks it.** It governs one thing: *claims that a verifiable result holds* ("tests pass", "build succeeds", "bug fixed") — run the check before you assert it, and scale the check to the stakes (a one-line copy tweak is not a test-suite ceremony). Don't dodge it with synonyms; equally, don't inflate it into a tax on the word "done" or a reason to keep re-proving instead of shipping the working thing.

## The Iron Law

```
NO COMPLETION CLAIMS WITHOUT FRESH VERIFICATION EVIDENCE
```

If you haven't run the verification command in this message, you cannot claim it passes.

## The Gate Function

```
BEFORE claiming any status or expressing satisfaction:

1. IDENTIFY: What command proves this claim?
2. RUN: Execute the FULL command (fresh, complete)
3. READ: Full output, check exit code, count failures
4. VERIFY: Does output confirm the claim?
   - If NO: State actual status with evidence
   - If YES: State claim WITH evidence
5. ONLY THEN: Make the claim

Skip any step = lying, not verifying
```

## Common Failures

| Claim | Requires | Not Sufficient |
|-------|----------|----------------|
| Tests pass | Test command output: 0 failures | Previous run, "should pass" |
| Linter clean | Linter output: 0 errors | Partial check, extrapolation |
| Build succeeds | Build command: exit 0 | Linter passing, logs look good |
| Bug fixed | Test original symptom: passes | Code changed, assumed fixed |
| Regression test works | Red-green cycle verified | Test passes once |
| Agent completed | VCS diff shows changes | Agent reports "success" |
| Requirements met | Line-by-line checklist | Tests passing |

## Red Flags - STOP

- Using "should", "probably", "seems to"
- Claiming a specific result is *proven* ("tests pass", "it's fixed", "build's green") before running the check in this message — enthusiasm itself is fine; an unverified **result claim** is the flag
- About to commit/push/PR without verification
- Trusting agent success reports
- Relying on partial verification
- Thinking "just this once"
- Tired and wanting work over
- **ANY wording implying success without having run verification**

## Rationalization Prevention

| Excuse | Reality |
|--------|---------|
| "Should work now" | RUN the verification |
| "I'm confident" | Confidence ≠ evidence |
| "Just this once" | No exceptions |
| "Linter passed" | Linter ≠ compiler |
| "Agent said success" | Verify independently |
| "I'm tired" | Exhaustion ≠ excuse |
| "Partial check is enough" | Partial proves nothing |
| "Different words so rule doesn't apply" | Spirit over letter |

## Key Patterns

**Tests:**
```
✅ [Run test command] [See: 34/34 pass] "All tests pass"
❌ "Should pass now" / "Looks correct"
```

**Regression tests (TDD Red-Green):**
```
✅ Write → Run (pass) → Revert fix → Run (MUST FAIL) → Restore → Run (pass)
❌ "I've written a regression test" (without red-green verification)
```

**Build:**
```
✅ [Run build] [See: exit 0] "Build passes"
❌ "Linter passed" (linter doesn't check compilation)
```

**Requirements:**
```
✅ Re-read plan → Create checklist → Verify each → Report gaps or completion
❌ "Tests pass, phase complete"
```

**Agent delegation:**
```
✅ Agent reports success → Check VCS diff → Verify changes → Report actual state
❌ Trust agent report
```

## Running the thing: services and long jobs

Most verifications start something — a daemon, a dev server, the built binary. The `bash` tool has one shape for that, and two of its refusals are call-shape errors: the command never ran, so repeating the same arguments only repeats the refusal.

- **`ready` is an object, never a boolean.** Keys: `log` (a regex the output must match), `port` (a port that must open), `host` (default `127.0.0.1`), `timeout` (that wait, in seconds, default 30). Give `log`, `port`, or both; the call returns once they hold. `ready: true` is refused verbatim: `ready: ready must be an object (was boolean)`.
- **A long-lived service is a call that carries a `name`**, and there the rule inverts: **no `async`, no `timeout` call argument** — the wait belongs in `ready.timeout`. The process is left running, its output is read at `proc://<name>`, and `proc://<name>/kill` stops it. Refused verbatim: `Service mode does not accept async or timeout; use ready.timeout for readiness.`
- **A one-shot command carries no `name`**: it takes the plain `timeout` (seconds), and `timeout: 0` only for a watcher you mean to leave up.

A server on port 3000 — `name` selects service mode, the wait lives inside `ready`:

```bash
bash(command="cd app && go run ./cmd/server", name="app-server",
     ready={"log": "listening on", "port": 3000, "host": "127.0.0.1", "timeout": 30})
```

## When the check itself lies — content over shape

Running a verification is necessary but not sufficient: a check can exit clean and still prove nothing. These are the named ways a green check lies — the failure siblings behind "evidence before claims." (Deeper treatment of *what* you verify against: `reference-first-verification`.)

- **Assert on CONTENT, never shape or count.** Output that *looks* like success — a non-zero count, a clean exit, no error text — can carry failure as its content. Before trusting a check, ask what its output looks like *when it fails*: if failure and success are indistinguishable at a glance, the check proves nothing. (`[1 lines]` and `No matches found` both read as "a match" to a line-count.)
- **Compare the returned value to the value you PASSED — never to a description of it.** A confident label (`evidenceRef NOW SET:` printed above the untouched original) is indistinguishable from a verification in a transcript. Diff the actual result against your actual input, not against a claim about it.
- **Find the untested axis.** Thorough coverage of ONE axis reads as thorough coverage overall. Ask what every one of your tests shares — a healthy manifest, a default option value, the happy path — and treat that shared assumption as the dimension you never tested.
- **A syntax check proves parseability and nothing else.** A clean `node --check` / lint / parse pass is the *covered* axis; boot and runtime are the untested one. Parseable ≠ sound.
- **A failure whose shape is a HANG reports nothing.** A bare `await` against a stated deadline that never re-checks cannot fail loudly. Race it against a timer so failure has a shape you can see.
- **A completeness inventory read through a truncating display is worthless.** When deciding *which occurrences to change*, count with `grep -c` / `grep -o` / full-line output — never `cut`, `head`, or a pager, which can hide a match *past* the cut. Distinguish *"I found N"* (a count) from *"I saw N"* (a display). Audit multiline prose **by paragraph, not by line** — a line filter both misses long-line matches and false-flags paragraphs whose companion text is on the next line. Then re-audit with a **different method** than the one that built the inventory — same-method re-checking reproduces the blind spot. (Empty/short output is a claim about your COMMAND until re-proven a second way.)

## What a completion gate can settle

`4genteam complete` sends the acceptance criteria to a counts-only judge, so a criterion must name something
the judge is SHOWN. Paths are redacted to `<path>` and test names to `<test>` at the transport, and exactly
seven keys travel: test_command_ran, test_exit_code, tests_failed, files_changed, lines_changed, base_sha,
head_sha. A criterion is admitted only when it names the test command, the run's exit code, a path this run's
numstat lists, or a base/head sha prefix of at least 7 (`clientcomplete/gate.go`, `register.carries`); there is
no line-count branch, so a count alone is refused before the judge is called.

- **Admissible and informative:** the sha prefix plus the totals - "between 2c34515 and 216a89d, files_changed
  is 3 and lines_changed is 21". Measured bands: a path-only criterion is admitted then judged blind
  (0.16-0.34); a count alone is refused (exit 6); sha plus totals settles at 0.93-0.97.
- **The base is the commit the change LEFT the line at:** the second parent of a merge tip, otherwise the
  merge-base. The merge-base shortcut reports main-only commits reversed - measured: 3 files/72 lines where
  the true change is 3 files/21 lines.
- **A claim no fact can carry** - a screenshot, a rendered page, "reachable by tap" - has no admissible
  wording: the command must produce the proof, or the claim goes in the details.
- **A gitlink-only diff has no prose.** Name its path and the two counts; if the judge still cannot settle it,
  the closure goes to the lead (numstat plus `git ls-tree`), never a criterion reworded to pass.
- **An item with no criteria cannot close at all** - the gate answers `uncertain`. Add criteria first.

## Done is committed, not said

A change is done when it is in the versioned source on the line the room reads - a commit whose sha you can
name. Not when a file sits in a seat dir, a row says so, or a message was relayed. Three shapes, three
verdicts:

- **Committed**: the sha names the change, `git show --numstat <sha> -- <path>` lists it. Done.
- **Prepared, not committed** (a patch in a seat dir, a worktree edit, a design note): NOT done. Say
  "prepared, not committed", name the path, and keep the item open.
- **Lead-gated** (a docs-only path set, a push, a deploy): not yours to call done; the lead lands it. The item stays in
  progress naming the sha the lead must produce; closing it on the strength of the patch is the false claim
  this skill exists to stop.

The same rule for a skill: a skill that exists only in a worktree has reached no seat. It is delivered when
the commit lands AND the reading seat was reseated - a running seat does not re-read its skills tree.
Measured 2026-10-18: the lead's installed tree held 22 entries where the other seats held 25-27, and nothing
reported it.

## Why This Matters

From 24 failure memories:
- your human partner said "I don't believe you" - trust broken
- Undefined functions shipped - would crash
- Missing requirements shipped - incomplete features
- Time wasted on false completion → redirect → rework
- Violates: "Honesty is a core value. If you lie, you'll be replaced."

## When To Apply

**ALWAYS before:**
- ANY variation of success/completion claims
- ANY expression of satisfaction
- ANY positive statement about work state
- Committing, PR creation, task completion
- Moving to next task
- Delegating to agents

**Rule applies to:**
- Exact phrases
- Paraphrases and synonyms
- Implications of success
- ANY communication suggesting completion/correctness

## The Bottom Line

**No shortcuts for verification.**

Run the command. Read the output. THEN claim the result.

This is non-negotiable.
