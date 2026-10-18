// Package clientevidence is the `4genteam evidence` verb: the client's half of O3 (NEXT_GEN.md), the
// write door onto /api/v2/tasks/{id}/evidence.
//
// IT SENDS DESCRIPTIONS, NOT CONTENTS. The route stores ONE `evidence_submitted` task event, and the
// box's privacy rule is that the payload carries only the diff's numstat lines, the two commit SHAs and
// the test block - never a file's contents and never a command's output. The test command's output is
// read here, its failing test names are extracted from it, and the output itself is dropped: nothing but
// base_sha, head_sha, numstat, command, exit_code and the names ever reaches the wire.
//
// IT REUSES THE CLIENT'S ONE CREDENTIAL rather than inventing a second one: $AGENTHUB_URL and
// $AGENTHUB_TOKEN through the same bearer-header path `4genteam feedback` uses, because the route is
// guarded exactly like the read path (GET .../events) - the ordinary authed wrapper over the caller's
// token. There is no machine token, and the token is never printed.
//
// IT IS NATIVE GO, NOT A SCRIPT: git and the test command are exec'd with an ARGUMENT LIST, the body is
// marshalled by encoding/json and sent by net/http. The --test line is split in-process (quoting is
// honoured) instead of being handed to a shell, which is this client's no-shell-strings rule; the price
// is named in the usage text - pipes, redirects and globs are not interpreted.
package clientevidence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientsync"
)

// evidencePath is the ONE route this verb writes to, `{id}` being the task the evidence belongs to. It
// is the write sibling of the read path the client already knows (GET /api/v2/tasks/{id}/events).
const evidencePath = "/api/v2/tasks/%s/evidence"

// taskPath is the board row a stored submission closes, read back for the done report. The route answers
// `{"success": true, "task": {...}}` (task_user_routes.go), which is the envelope readTaskRow checks.
const taskPath = "/api/v2/tasks/%s"

// httpTimeout is the client's HTTP convention (the same 60s internal/clientsync and
// internal/clientfeedback use), named so a reader can see it is a constant rather than a guess.
const httpTimeout = 60 * time.Second

// exitTestFailed is this verb's own exit code for the one honest half-success: the evidence WAS stored,
// and the test command it reports on exited non-zero. A plain 0 would swallow the failure the submission
// exists to describe, and ExitRemote would claim the submission failed when it did not, so the code is
// distinct and the usage text publishes it.
const exitTestFailed = 5

// shaPattern is what the wire contract accepts for base_sha and head_sha: a full 40-hex commit SHA.
var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// evidenceUsage is what `4genteam evidence --help` prints. Like every verb's help it names the verb.
const evidenceUsage = `usage: 4genteam evidence --task <id> --base <sha> [--test "<cmd>"] [--url BASE]

Submit what a change did, as evidence on a task: one HTTP POST to /api/v2/tasks/{id}/evidence carrying
the diff's numstat lines, the two commit SHAs, and the test run's exit code and failing test names. NO
file contents and NO command output are sent - only those fields are stored.

  --task   REQUIRED. The task id this evidence belongs to.
  --base   REQUIRED. The commit the change is measured from. It is resolved to a full 40-hex SHA.
  --test   Optional. The test command to run. The line is split into arguments in-process and quotes
           are honoured, but NO SHELL is used: pipes, redirects and globs are NOT interpreted. Its exit
           code and the failing test names found in its output are reported; the output stays local. A
           failing run whose output names no test carries an explicit marker, never an empty list.
           Without it the submission carries an empty command, exit 0 and no failures.
  --url    Optional. Defaults to $AGENTHUB_URL. The route path is appended.

Credential: $AGENTHUB_TOKEN, sent as a bearer token. It is never printed.
Exit: 0 stored, 1 the request or the answer failed, 2 usage or a missing value,
      5 stored, but the test command exited non-zero.
`

// evidenceClient is the HTTP seam, the same package-variable convention internal/clientfeedback uses: a
// test replaces it (through evidenceClient.Transport) so the answer is exercised in-process with NO
// network, because this is a push path and the suite cannot run it against a cloud.
var evidenceClient = &http.Client{Timeout: httpTimeout}

// gitOutput is the seam over `git`. It returns stdout VERBATIM - the numstat bytes ARE the payload, so
// the caller must not reformat, trim or re-encode them - and wraps the command's own failure text so a
// refusal can name what git said. A test replaces it so the verb's argv is asserted without a repository.
var gitOutput = func(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("git %s failed: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// runTestCommand is the seam over the --test command: it returns the command's COMBINED output (stdout
// and stderr interleaved as the terminal would show them, which is what a failing-test line is printed
// on) and its exit code. An error means the command could not be STARTED at all, which is not a test
// result and must not be submitted as one.
var runTestCommand = func(ctx context.Context, argv []string) (string, int, error) {
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var combined bytes.Buffer
	command.Stdout, command.Stderr = &combined, &combined
	err := command.Run()
	if err == nil {
		return combined.String(), 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return combined.String(), exit.ExitCode(), nil
	}
	return combined.String(), 0, err
}

// evidenceTest is the test block. It carries the command LINE as typed, the exit code, and the failing
// test NAMES - never output text.
type evidenceTest struct {
	Command  string   `json:"command"`
	ExitCode int      `json:"exit_code"`
	Failed   []string `json:"failed"`
}

// evidenceBody is the wire payload, field for field as the route's contract fixes it. There is no field
// for a file's contents and none for a command's output; the numstat text is `git diff --numstat`'s own
// lines, which name files and count lines rather than quote them.
type evidenceBody struct {
	BaseSHA string       `json:"base_sha"`
	HeadSHA string       `json:"head_sha"`
	Numstat string       `json:"numstat"`
	Test    evidenceTest `json:"test"`
}

// evidenceOptions is the parsed command line, before validation.
type evidenceOptions struct {
	task string
	base string
	test string
	url  string
}

// Commands is what the dispatcher registers from this package: one verb, `evidence`.
func Commands() []clientcmd.Command {
	return []clientcmd.Command{evidenceCommand{}}
}

type evidenceCommand struct{}

func (evidenceCommand) Name() string    { return "evidence" }
func (evidenceCommand) Summary() string { return "submit evidence for a task" }

func (evidenceCommand) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, help, err := parseEvidenceArgs(args)
	if help {
		_, _ = io.WriteString(stdout, evidenceUsage)
		return clientcmd.ExitOK
	}
	if err != nil {
		return evidenceUsageError(stderr, err.Error())
	}
	if opts.task == "" {
		return evidenceUsageError(stderr, "--task is required")
	}
	if opts.base == "" {
		return evidenceUsageError(stderr, "--base is required")
	}

	// The --test line is parsed BEFORE any git call or dial, so a typo in a quoted line costs nothing.
	if opts.test != "" {
		argv, err := splitCommand(opts.test)
		if err != nil {
			return evidenceUsageError(stderr, err.Error())
		}
		if len(argv) == 0 {
			return evidenceUsageError(stderr, "--test names no command")
		}
	}

	baseURL := opts.url
	if baseURL == "" {
		baseURL = os.Getenv("AGENTHUB_URL")
	}
	if baseURL == "" {
		return evidenceUsageError(stderr, "no server URL: set AGENTHUB_URL or pass --url")
	}
	token := os.Getenv("AGENTHUB_TOKEN")
	if token == "" {
		return evidenceUsageError(stderr, "AGENTHUB_TOKEN is not set, so the evidence cannot be authenticated")
	}

	// Everything below is collected BEFORE the dial, and any refusal here costs no request.
	body, failure := Collect(ctx, opts.base, opts.test)
	if failure != nil {
		if failure.Usage {
			return evidenceUsageError(stderr, failure.Message)
		}
		fmt.Fprintf(stderr, "4genteam evidence: %s\n", failure.Message)
		return clientcmd.ExitRemote
	}
	return postEvidence(ctx, baseURL, token, opts.task, body, stdout, stderr)
}

// Body is the evidence payload: the diff's numstat, the two SHAs and the test block.
type Body = evidenceBody

// CollectFailure is a refusal while collecting evidence. Usage marks a caller error (a ref that names
// no commit, a test line that does not parse); anything else is a failure of git or of the command.
type CollectFailure struct {
	Usage   bool
	Message string
}

// Collect gathers the evidence for the change from `base` to HEAD: it resolves both SHAs, reads the
// numstat and runs the test line (split in-process, no shell; empty means no test run). Nothing is sent.
func Collect(ctx context.Context, base, test string) (Body, *CollectFailure) {
	var testArgv []string
	if test != "" {
		argv, err := splitCommand(test)
		if err != nil {
			return Body{}, &CollectFailure{Usage: true, Message: err.Error()}
		}
		if len(argv) == 0 {
			return Body{}, &CollectFailure{Usage: true, Message: "--test names no command"}
		}
		testArgv = argv
	}
	baseSHA, err := resolveSHA(ctx, base)
	if err != nil {
		return Body{}, &CollectFailure{Usage: true, Message: fmt.Sprintf("--base %s does not name a commit: %v", base, err)}
	}
	headSHA, err := resolveSHA(ctx, "HEAD")
	if err != nil {
		return Body{}, &CollectFailure{Message: fmt.Sprintf("cannot resolve HEAD: %v", err)}
	}
	// The range is expressed with the RESOLVED SHAs so the text is exactly what the route recomputes for
	// the SHAs it receives, rather than a string the local refs happen to spell the same way today.
	numstat, err := gitOutput(ctx, "diff", "--numstat", baseSHA+".."+headSHA)
	if err != nil {
		return Body{}, &CollectFailure{Message: err.Error()}
	}

	result := evidenceTest{Command: test, Failed: []string{}}
	if len(testArgv) > 0 {
		output, code, err := runTestCommand(ctx, testArgv)
		if err != nil {
			// The command never started, so there is no exit code and no honest evidence to send.
			return Body{}, &CollectFailure{Message: fmt.Sprintf("the test command could not run: %v", err)}
		}
		result.ExitCode = code
		if code != 0 {
			result.Failed = extractFailedTests(output)
			if len(result.Failed) == 0 {
				result.Failed = []string{unnamedFailure}
			}
		}
	}
	return Body{BaseSHA: baseSHA, HeadSHA: headSHA, Numstat: numstat, Test: result}, nil
}

// Post stores the evidence on the task with the same answers and exit codes as `4genteam evidence`:
// 0 stored, 5 stored with a failing test run, ExitRemote refused or failed.
func Post(ctx context.Context, baseURL, token, taskID string, body Body, stdout, stderr io.Writer) int {
	return postEvidence(ctx, baseURL, token, taskID, body, stdout, stderr)
}

// resolveSHA turns a ref into the full 40-hex commit SHA the wire contract requires. `^{commit}` makes
// rev-parse follow a tag to its commit and refuse a tree or a blob, and --end-of-options keeps a ref that
// begins with `-` from being read as a flag.
func resolveSHA(ctx context.Context, ref string) (string, error) {
	out, err := gitOutput(ctx, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(out)
	if !shaPattern.MatchString(sha) {
		return "", fmt.Errorf("git resolved it to %q, which is not a full 40-hex commit SHA", sha)
	}
	return sha, nil
}

// parseEvidenceArgs is this verb's flag grammar, the same one every flag-taking verb here uses: every
// flag takes a value, an unknown argument is a usage error, and -h/--help stops the parse. A flag whose
// value is missing leaves the field empty, which the required checks then name.
func parseEvidenceArgs(args []string) (evidenceOptions, bool, error) {
	var opts evidenceOptions
	i := 0
	value := func() string {
		if i+1 < len(args) {
			i++
			return args[i]
		}
		return ""
	}
	for ; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			return opts, true, nil
		case "--task":
			opts.task = value()
		case "--base":
			opts.base = value()
		case "--test":
			opts.test = value()
		case "--url":
			opts.url = value()
		default:
			return opts, false, fmt.Errorf("unknown argument: %s", args[i])
		}
	}
	return opts, false, nil
}

// shellMetachars are the characters a shell would act on and this splitter would not: unquoted, they
// are prose or a script the verb cannot honour, so splitCommand refuses them instead of running a
// command line that means something else than what was typed.
const shellMetachars = "()|&;<>`"

// splitCommand splits a command LINE into an argument list without a shell. Quotes are honoured ('...'
// is literal, "..." honours a backslash escape) and the rules are the Unix ones, because the seat's own
// shell is where the user typed the line. A command that needs a pipe, a redirect or a glob must be
// wrapped by the caller in a shell it names itself: this verb will not hand a string to sh -c or cmd /c,
// which is the client's no-shell-strings rule and the reason the same code can run on Windows.
//
// An UNQUOTED shell metacharacter is REFUSED, not passed on. Without a shell `(` `)` `;` `|` `&` `<` `>`
// and a backtick are ordinary argument characters, so a sentence typed into --test - the measured case,
// `go test ./... (green at 1daa0e6d; repoguards 0.2s)` - reached the command as `(green` and friends,
// the command exited non-zero for a reason that was never a test failure, and the gate read D3 "the test
// run failed" over a green tree. The refusal names the splitting and the quoted form that still works.
func splitCommand(line string) ([]string, error) {
	runes := []rune(line)
	var args []string
	var current strings.Builder
	var quote rune // 0 outside quotes, otherwise '\'' or '"'
	flush := func() {
		if current.Len() > 0 {
			args = append(args, current.String())
			current.Reset()
		}
	}
	escaped := false
	for _, r := range runes {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		switch {
		case quote == 0 && unicode.IsSpace(r):
			flush()
		case quote == 0 && strings.ContainsRune(shellMetachars, r):
			return nil, fmt.Errorf("--test is split into an argument list and never read by a shell, so the unquoted %q "+
				"would be passed to the command as its own argument: pass the bare command, or put the whole line "+
				"inside a shell you name yourself, e.g. sh -c '...'", r)
		case r == '\'' && quote != '"':
			if quote == '\'' {
				quote = 0
			} else {
				quote = '\''
			}
		case r == '"' && quote != '\'':
			if quote == '"' {
				quote = 0
			} else {
				quote = '"'
			}
		case r == '\\' && quote != '\'':
			escaped = true
		default:
			current.WriteRune(r)
		}
	}
	if escaped {
		// A trailing backslash escapes nothing, so it is kept as itself rather than dropped.
		current.WriteRune('\\')
	}
	if quote != 0 {
		return nil, errors.New("--test has an unterminated quote")
	}
	flush()
	return args, nil
}

// failedPatterns are the failing-test lines this verb recognises. Extraction is HEURISTIC by nature -
// a command's output is free text - so the set is small and written out here instead of guessed at each
// call site, and every match is a NAME, not a line of output:
//
//   - `--- FAIL: TestName (0.00s)`  the `go test -v` shape; the name is the first field.
//   - `FAILED tests/test_x.py::test_y`  pytest's short summary. The node id is REQUIRED, because pytest
//     always spells `file::test`, and demanding the `::` is what keeps a program's OWN line that merely
//     starts with the word - `FAILED READ {"status":503}`, which stored 'READ' as a failing test on
//     2026-10-16 - out of the list.
//   - `  ✘  2 [chromium] › e2e/x.spec.ts:12:7 › a group › the title (5ms)`  Playwright's list reporter,
//     as measured on this repository's own config: the ✘ marker, the project in brackets, the test's
//     location, then its title path and duration. The location is kept - as pytest's node id is - so the
//     name identifies the test and not only its title.
//   - `not ok 1 - a name`  TAP.
//
// A PASSING run names no failures at all: extraction runs only when the command exited non-zero, so a
// program's own output cannot name a failed test in evidence that reports a green run.
//
// `go test` WITHOUT -v prints only package-level `FAIL\tpkg\t0.1s` lines, which name no test, so nothing
// is extracted from them: the caller who wants names passes -v, and the exit code is reported either way.
var failedPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^\s*--- FAIL:\s*(\S+)`),
	regexp.MustCompile(`^FAILED\s+(\S+::\S+)`),
	regexp.MustCompile(`^\s*✘\s+\d+\s+\[[^\]]*\]\s+›\s+(.+?)\s+\([^()]*\)(?:\s+\([^()]*\))*\s*$`),
	regexp.MustCompile(`^not ok\s+\d+\s*-\s*(.+?)\s*$`),
}

// unnamedFailure is what the test block carries when the command exited non-zero and its output named no
// failing test: a reporter that prints no marker, a run that died before its summary. The failure is
// REAL and its NAME is not in the output, and the marker says exactly that - an empty list reads as
// "nothing failed" to the room, which is the other half of the wrong name 2026-10-16 stored.
const unnamedFailure = "(no failing test name in the output)"

// extractFailedTests pulls the failing test names out of a command's output, in the order the command
// printed them and once each. It never returns nil, so the wire always carries a list.
func extractFailedTests(output string) []string {
	seen := map[string]bool{}
	names := []string{}
	for _, line := range strings.Split(output, "\n") {
		for _, pattern := range failedPatterns {
			match := pattern.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			name := strings.TrimSpace(match[1])
			if name == "" || seen[name] {
				break
			}
			seen[name] = true
			names = append(names, name)
			break
		}
	}
	return names
}

// postEvidence sends the submission and relays the answer honestly. A 2xx relays the created event to
// stdout; a 409 reads as a REFUSAL naming the head_sha that already has evidence, never as a success;
// any other non-2xx is a failure with the status named. The token is in the header and nowhere else.
func postEvidence(ctx context.Context, baseURL, token, taskID string, body evidenceBody, stdout, stderr io.Writer) int {
	route := strings.TrimRight(baseURL, "/") + fmt.Sprintf(evidencePath, url.PathEscape(taskID))
	payload, err := json.Marshal(body)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam evidence: cannot encode the submission: %v\n", err)
		return clientcmd.ExitRemote
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, route, bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintf(stderr, "4genteam evidence: the request to %s failed: %v\n", route, err)
		return clientcmd.ExitRemote
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := evidenceClient.Do(req)
	if err != nil {
		// The REASON is named, as `4genteam feedback` names it: the text carries the URL and the
		// transport's round trip, and the credential is in the header, so it cannot be in it.
		fmt.Fprintf(stderr, "4genteam evidence: the request to %s failed: %v\n", route, err)
		return clientcmd.ExitRemote
	}
	defer func() { _ = resp.Body.Close() }()

	answer, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode <= 299:
		_, _ = stdout.Write(answer)
		_, _ = io.WriteString(stdout, "\n")
		// THE DONE REPORT BELONGS TO A STORED SUBMISSION AND TO NOTHING ELSE: a refusal must print nothing on
		// stdout and must cost no second request, so the row is read only here, after the server accepted.
		printDoneReport(ctx, baseURL, token, taskID, body, stdout)
		if body.Test.ExitCode != 0 {
			fmt.Fprintf(stderr, "4genteam evidence: stored, but the test command exited %d%s\n",
				body.Test.ExitCode, failingSuffix(body.Test.Failed))
			return exitTestFailed
		}
		return clientcmd.ExitOK
	case resp.StatusCode == http.StatusConflict:
		fmt.Fprintf(stderr, "4genteam evidence: refused (HTTP 409): head_sha %s already has evidence on this "+
			"task, so nothing was stored; the earlier submission stands\n", body.HeadSHA)
		relayRefusal(stderr, answer)
		return clientcmd.ExitRemote
	default:
		fmt.Fprintf(stderr, "4genteam evidence: the server answered HTTP %d\n", resp.StatusCode)
		relayRefusal(stderr, answer)
		return clientcmd.ExitRemote
	}
}

// printDoneReport is item 6's done report, assembled at item close: the row this run closes, read back from
// the board; the size of the diff being submitted; the doc-drift line; and the tests the submission names. It
// never refuses - this is a report, not a gate, and the reviewer still decides. A row it cannot read is NAMED
// and the report goes on, so a reader can tell "the row says nothing" from "the row was not read".
func printDoneReport(ctx context.Context, baseURL, token, taskID string, body evidenceBody, stdout io.Writer) {
	fmt.Fprintf(stdout, "done report for task %s\n", taskID)
	if row, err := readTaskRow(ctx, baseURL, token, taskID); err != nil {
		fmt.Fprintf(stdout, "  row: not read: %v\n", err)
	} else {
		fmt.Fprintf(stdout, "  row: %s [%s]\n", rowField(row, "title"), rowField(row, "status"))
	}
	paths := numstatPaths(body.Numstat)
	fmt.Fprintf(stdout, "  diff %s..%s: %d file(s) changed\n", shortSHA(body.BaseSHA), shortSHA(body.HeadSHA), len(paths))
	fmt.Fprintf(stdout, "  doc drift: %s\n", docDrift(paths))
	fmt.Fprintf(stdout, "  tests: %s\n", testLine(body.Test))
}

// readTaskRow is the row a done report closes: `GET /api/v2/tasks/{id}`, the route GetUserTask serves. It
// answers `{"success": true, "task": {...}}` and this reads it the same way the other readers of this API do -
// success checked first, then the payload key - so two readers cannot disagree about the envelope. This one
// once read a `data` wrapper (the MCP shape) and printed `row: unknown [unknown]` against the live route.
func readTaskRow(ctx context.Context, baseURL, token, taskID string) (map[string]any, error) {
	path := fmt.Sprintf(taskPath, url.PathEscape(taskID))
	answer, err := clientsync.RequestJSON(ctx, http.MethodGet, baseURL, token, path, nil)
	if err != nil {
		return nil, err
	}
	if success, isBool := answer["success"].(bool); !isBool || !success {
		return nil, fmt.Errorf("GET %s returned an error response", path)
	}
	task, ok := answer["task"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("GET %s response has no task", path)
	}
	return task, nil
}

// rowField reads one field the report names out of the row. A field that is absent or empty reads as
// "unknown" rather than as a blank a reader would take for a real value.
func rowField(row map[string]any, key string) string {
	if value, ok := row[key].(string); ok && value != "" {
		return value
	}
	return "unknown"
}

// testLine is the test half of the report, from the block already in the submission. No test named is said
// plainly rather than left blank, because a blank would read as "the tests passed".
func testLine(tests evidenceTest) string {
	if tests.Command == "" {
		return "none named (the submission carries an empty test block)"
	}
	line := fmt.Sprintf("%s exit %d", tests.Command, tests.ExitCode)
	switch len(tests.Failed) {
	case 0:
		return line
	case 1:
		return line + ", 1 failing: " + tests.Failed[0]
	default:
		return fmt.Sprintf("%s, %d failing: %s", line, len(tests.Failed), strings.Join(tests.Failed, ", "))
	}
}

// shortSHA is the 7-character form every report here names a commit by.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// numstatPaths is the file list of a `git diff --numstat` reading: one line per file,
// `<added>\t<deleted>\t<path>`, so the PATH is the last tab-separated field - a rename prints `old => new`
// inside that field and is named the way git spells it. The counts are deliberately not parsed: the path is the
// only field this report uses, and the numstat bytes themselves are what went on the wire.
func numstatPaths(numstat string) []string {
	var paths []string
	for _, line := range strings.Split(numstat, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		path := strings.TrimSpace(fields[len(fields)-1])
		if path == "" {
			continue
		}
		paths = append(paths, path)
	}
	return paths
}

// docDrift answers the plan's doc-drift line: "code changed but no doc or changelog entry" when the range
// moved files that are not documentation and moved no markdown at all, and "none" when markdown moved too, or
// when no file moved. The rule is written down rather than guessed at each call site: a markdown file is
// documentation, and every other file is code for this purpose.
func docDrift(paths []string) string {
	docs := 0
	for _, path := range paths {
		if strings.HasSuffix(path, ".md") {
			docs++
		}
	}
	switch {
	case len(paths) == 0:
		return "none (the range changed no file)"
	case docs > 0:
		return fmt.Sprintf("none (%d of %d file(s) are markdown)", docs, len(paths))
	default:
		return "code changed but no doc or changelog entry"
	}
}

// relayRefusal writes the server's own words about a refusal to STDERR. It is deliberately not stdout:
// stdout carries the stored event on success, and a caller that read a refused answer there would take
// the refusal for a stored submission.
func relayRefusal(stderr io.Writer, answer []byte) {
	text := strings.TrimSpace(string(answer))
	if text == "" {
		return
	}
	_, _ = io.WriteString(stderr, "4genteam evidence: the server said: "+text+"\n")
}

// failingSuffix names the extracted failures beside the exit code, or nothing when the command's output
// named none.
func failingSuffix(failed []string) string {
	if len(failed) == 0 {
		return ""
	}
	return "; failing: " + strings.Join(failed, ", ")
}

// evidenceUsageError is the ONE shape of a usage refusal: a line naming what is wrong, then the one line
// of usage, on stderr, at exit 2.
func evidenceUsageError(stderr io.Writer, message string) int {
	fmt.Fprintf(stderr, "4genteam evidence: %s\n", message)
	_, _ = io.WriteString(stderr, "usage: 4genteam evidence --task <id> --base <sha> [--test \"<cmd>\"] [--url BASE]\n")
	return clientcmd.ExitUsage
}
