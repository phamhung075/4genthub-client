// Command seatcheck checks a pinned communication policy before a seat sends a
// message, and records the outcome. It is what a seat runs instead of calling
// `rig send` directly.
//
// Every send is audited in two steps: a decision line before delivery and, for an allowed
// message, an outcome line after it. A decision line with no outcome line means the delivery
// outcome is unknown (seatcheck was interrupted in between); the message may have been delivered.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/commpolicy"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: seatcheck <send|audit-scan> ...")
		return 2
	}
	switch args[0] {
	case "send":
		return runSend(args[1:], stdout, stderr)
	case "audit-scan":
		return runAuditScan(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "seatcheck: unknown subcommand %q\n", args[0])
		return 2
	}
}

var intents = map[string]commpolicy.Intent{
	"task":       commpolicy.IntentTask,
	"escalation": commpolicy.IntentEscalation,
	"report":     commpolicy.IntentReport,
	"question":   commpolicy.IntentQuestion,
	"notice":     commpolicy.IntentNotice,
}

// Exit codes of send, besides 0 (delivered) and the usage/policy error 2.
const (
	exitAuditFailed    = 1
	exitDenied         = 3
	exitDeliveryFailed = 5
)

// Package-level seams, replaced by tests.
var (
	// pinsDir returns the pinned seats directory.
	pinsDir = defaultPins
	// identify returns who the calling seat is.
	identify = rigWhoami
	// deliver sends text to the session named target and returns the exit code.
	deliver = rigSend
)

// installedPinsDir is the pins directory baked in at BUILD time by whoever built this binary:
// scripts/openrig_seat_sync.py install-checker passes its --out store here with
// `-ldflags -X main.installedPinsDir=<store>`. The seat cannot choose it - this command has no
// flag and reads no environment variable at all, so what the guard reads and audits is decided
// before it runs rather than by the process it constrains. Empty means the binary was built
// without the seam, and the store then falls back to the account's home, which is where the
// client's own default resolves on a machine with no override.
var installedPinsDir = ""

// defaultPins is where the pinned seat directories live: the CLIENT's own store, through the one
// implementation that clientenv owns - the account home's .openrig/agenthub-seats, never $HOME, so the
// guard and the client cannot disagree about where a policy was installed. This used to be a second copy
// of that resolution inside the same module, which is the kind of duplication that let a week-stale
// binary pass as current.
func defaultPins() (string, error) {
	if installedPinsDir != "" {
		return installedPinsDir, nil
	}
	return clientenv.ResolveSeatStore("")
}

func runSend(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	to := fs.String("to", "", "recipient seat")
	intentName := fs.String("intent", "", "message intent")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: seatcheck send --to <seat> --intent <task|escalation|report|question|notice> -- <message words...>")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	words := fs.Args()
	if *to == "" || *intentName == "" || len(words) == 0 {
		fmt.Fprintln(stderr, "seatcheck send: --to, --intent and a message are required")
		return 2
	}
	intent, ok := intents[*intentName]
	if !ok {
		fmt.Fprintf(stderr, "seatcheck send: unknown intent %q\n", *intentName)
		return 2
	}
	pins, err := pinsDir()
	if err != nil {
		fmt.Fprintf(stderr, "seatcheck send: pins directory: %v\n", err)
		return 2
	}
	who, err := identify()
	if err != nil {
		fmt.Fprintf(stderr, "seatcheck send: identity: %v\n", err)
		return 2
	}
	if !safePathSegment(who.Rig) || !safePathSegment(who.Member) {
		fmt.Fprintf(stderr, "seatcheck send: identity %q/%q is not a usable directory name\n", who.Rig, who.Member)
		return 2
	}
	seatDir := filepath.Join(pins, who.Rig, who.Member)

	data, err := os.ReadFile(filepath.Join(seatDir, "policy.json"))
	if err != nil {
		fmt.Fprintf(stderr, "seatcheck send: read policy: %v\n", err)
		return 2
	}
	policy, err := commpolicy.ParsePolicy(data)
	if err != nil {
		fmt.Fprintf(stderr, "seatcheck send: parse policy: %v\n", err)
		return 2
	}
	if policy.Seat != who.Member {
		fmt.Fprintf(stderr, "seatcheck send: the policy in %s belongs to seat %q, not %q\n", seatDir, policy.Seat, who.Member)
		return 2
	}

	// The policy and the roster speak in seat keys; a reply to a message header carries a full
	// session name (<pod>-<member>@<rig>). Map it to its member before the policy check.
	recipient, session, known := resolveRecipient(who, policy, *to)
	if !known {
		recipient = *to
	}
	decision := commpolicy.Decide(policy, recipient, intent)
	record := commpolicy.AuditRecord{
		At:         time.Now().UTC(),
		From:       policy.Seat,
		To:         recipient,
		Intent:     intent,
		Allowed:    decision.Allowed,
		Reason:     decision.Reason,
		PolicyHash: commpolicy.PolicyHash(policy),
	}
	auditPath := filepath.Join(seatDir, "audit.jsonl")
	if err := appendAudit(auditPath, record); err != nil {
		fmt.Fprintf(stderr, "seatcheck send: audit: %v\n", err)
		return exitAuditFailed
	}
	if !decision.Allowed {
		fmt.Fprintf(stderr, "denied: %s\n", decision.Reason)
		if !known {
			fmt.Fprintf(stderr, "unknown recipient %q; %s\n", *to, recipientHint(who, policy, intent, *intentName))
		}
		return exitDenied
	}

	// rig send resolves only full session names (<pod>-<member>@<rig>), which the roster of
	// rig whoami carries. The decision line above is on disk; a delivery that cannot start or
	// fails is recorded after it.
	code := deliverTo(who, recipient, session, strings.Join(words, " "), stdout, stderr)
	record.Outcome = commpolicy.OutcomeDelivered
	if code != 0 {
		record.Outcome = commpolicy.OutcomeDeliveryFailed
	}
	// A failed outcome line must not change the exit code: after a delivery that succeeded, a
	// non-zero exit would make a retrying caller send the message twice.
	if err := appendAudit(auditPath, record); err != nil {
		fmt.Fprintf(stderr, "seatcheck send: warning: the message was handled (%s) but its outcome could not be audited: %v\n", record.Outcome, err)
	}
	if code != 0 {
		return exitDeliveryFailed
	}
	return 0
}

// resolveRecipient maps --to to a seat key. A name that is a seat key (the calling seat, a
// roster member or a seat the policy links) is used as is; otherwise a full roster session name
// maps to its member and is also returned as the session to deliver to. known is false when
// the name is neither; the send is then denied by the policy (no link) and audited, and the
// caller is told which seat keys exist.
func resolveRecipient(who identity, policy commpolicy.Policy, to string) (recipient, session string, known bool) {
	for _, key := range seatKeys(who, policy) {
		if key == to {
			return to, "", true
		}
	}
	for member, sessions := range who.Peers {
		for _, s := range sessions {
			if s == to {
				return member, s, true
			}
		}
	}
	return "", "", false
}

// seatKeys lists the seat keys a message can be addressed to, sorted: the calling seat, the
// roster members and the seats the policy links.
func seatKeys(who identity, policy commpolicy.Policy) []string {
	set := map[string]bool{policy.Seat: true}
	for member := range who.Peers {
		set[member] = true
	}
	for _, link := range policy.Links {
		set[link.From], set[link.To] = true, true
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// recipientHint tells the caller which seat keys its policy allows for this intent, and only
// those: naming a seat the caller may not message would reveal nothing it is allowed to use.
func recipientHint(who identity, policy commpolicy.Policy, intent commpolicy.Intent, intentName string) string {
	var allowed []string
	for _, key := range seatKeys(who, policy) {
		if commpolicy.Decide(policy, key, intent).Allowed {
			allowed = append(allowed, key)
		}
	}
	if len(allowed) == 0 {
		return fmt.Sprintf("your policy allows no recipient for intent %q", intentName)
	}
	return "use a seat key: " + strings.Join(allowed, ", ")
}

// deliverTo delivers to session when it is known, otherwise to the roster session of the seat
// key to; any failure is a non-zero code that runSend maps to exitDeliveryFailed, so it never
// reads as a policy result (2, 3, 4).
func deliverTo(who identity, to, session, text string, stdout, stderr io.Writer) int {
	if session == "" {
		sessions := who.Peers[to]
		switch len(sessions) {
		case 0:
			fmt.Fprintf(stderr, "seatcheck send: %q is not a seat of rig %q\n", to, who.Rig)
			return 1
		case 1:
			session = sessions[0]
		default:
			fmt.Fprintf(stderr, "seatcheck send: %q is ambiguous in rig %q: %s\n", to, who.Rig, strings.Join(sessions, " and "))
			return 1
		}
	}
	if code := deliver(session, text, stdout, stderr); code != 0 {
		fmt.Fprintf(stderr, "seatcheck send: delivery failed (rig send exit %d)\n", code)
		return code
	}
	return 0
}

// safePathSegment reports whether name can be one directory name under the pins directory.
func safePathSegment(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

// identity is the calling seat: its rig, its member name and the session names of the other
// members of the rig, keyed by member name (more than one when a member name is repeated
// across pods; sending to such a name is ambiguous, sending to any other peer is not).
type identity struct {
	Rig, Member string
	Peers       map[string][]string
}

// rigWhoami reads the calling seat's identity from `rig whoami --json`.
func rigWhoami() (identity, error) {
	out, err := exec.Command("rig", "whoami", "--json").Output()
	if err != nil {
		return identity{}, fmt.Errorf("rig whoami: %w", err)
	}
	return parseWhoami(out)
}

func parseWhoami(data []byte) (identity, error) {
	var who struct {
		Identity struct {
			RigName  string `json:"rigName"`
			MemberID string `json:"memberId"`
		} `json:"identity"`
		Peers []struct {
			LogicalID   string `json:"logicalId"`
			SessionName string `json:"sessionName"`
		} `json:"peers"`
	}
	if err := json.Unmarshal(data, &who); err != nil {
		return identity{}, fmt.Errorf("parse rig whoami: %w", err)
	}
	rig, member := who.Identity.RigName, who.Identity.MemberID
	if rig == "" || member == "" {
		return identity{}, errors.New("rig whoami returned no rig or member")
	}
	// A logical id is <pod>.<member>; the member is the part after the first dot (the pod is
	// not always the rig name).
	peers := make(map[string][]string, len(who.Peers))
	for _, p := range who.Peers {
		_, name, ok := strings.Cut(p.LogicalID, ".")
		if !ok || name == "" || p.SessionName == "" {
			continue
		}
		peers[name] = append(peers[name], p.SessionName)
	}
	return identity{Rig: rig, Member: member, Peers: peers}, nil
}

// appendAudit appends one JSON line and syncs it to disk, so a line written before delivery
// survives a crash. The file must not be readable or writable by others.
func appendAudit(path string, record commpolicy.AuditRecord) (err error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s has mode %v, want 0600", path, info.Mode().Perm())
	}
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(line, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// rigSend delivers text to the session target with `rig send`. The `--` ends option parsing,
// so a message such as "--rig=x" is text, never an option that widens the recipients, and
// stdin is detached so rig cannot read the text from the seat's input. Its exit code is
// returned unchanged; runSend maps any failure to its own code.
func rigSend(target, text string, stdout, stderr io.Writer) int {
	cmd := exec.Command("rig", "send", "--", target, text)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "seatcheck send: deliver: %v\n", err)
		return 1
	}
	return 0
}

func runAuditScan(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("audit-scan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	known := fs.String("known", "", "comma separated wrapper command names")
	file := fs.String("file", "", "file of observed command lines, one per line")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: seatcheck audit-scan --known <wrapper names comma separated> --file <path to a text file of observed command lines, one per line>")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file == "" {
		fmt.Fprintln(stderr, "seatcheck audit-scan: --file is required")
		return 2
	}

	var names []string
	for _, name := range strings.Split(*known, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}

	data, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintf(stderr, "seatcheck audit-scan: read file: %v\n", err)
		return 2
	}
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		if commpolicy.BypassSuspected(names, line) {
			fmt.Fprintf(stdout, "bypass-suspected: %s\n", line)
			found = true
		}
	}
	if found {
		return 4
	}
	return 0
}
