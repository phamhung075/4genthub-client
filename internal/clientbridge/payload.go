// Package clientbridge is the Go home of scripts/openrig_bridge.py: it reports THIS machine and its
// rigs to the cloud. It is the half of the one-client consolidation that talks outward, and it is
// deliberately built the same way the Python was - one allow-list builder, one payload shape, one
// place that starts a local tool - so the parity test can compare them field by field.
//
// WHAT LEAVES THIS MACHINE IS AN ALLOW-LIST, NOT A COPY. Every outgoing object is built by naming its
// fields here; nothing is marshalled straight off the OpenRig structures, so a new field in
// `rig ps` cannot become a new field in a report by accident.
package clientbridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/secretscan"
)

// The states and runtimes a report may carry. Anything else reads `unknown`, so an OpenRig value
// this client has not seen is reported as unknown rather than passed through.
var (
	seatStates    = map[string]bool{"running": true, "idle": true, "blocked": true, "stopped": true, "unknown": true}
	runtimes      = map[string]bool{"claude-code": true, "codex": true, "agy": true, "omp": true, "terminal": true, "unknown": true}
	agentStatuses = map[string]bool{"idle": true, "working": true, "blocked": true, "done": true, "unknown": true}
)

// The shapes a name, a hash and a pane id may take. A value failing its pattern drops the seat or the
// agent rather than being reported, which is why the counts are reported too.
var (
	namePattern = regexp.MustCompile(`\A[a-zA-Z0-9][a-zA-Z0-9_-]*\z`)
	hashPattern = regexp.MustCompile(`\A[A-Za-z0-9][A-Za-z0-9._-]{0,127}\z`)
	panePattern = regexp.MustCompile(`\A[A-Za-z0-9][A-Za-z0-9:_-]{0,31}\z`)
)

// SeatStatus is one reported seat: the seven fields the cloud accepts, and no others.
type SeatStatus struct {
	Room     string `json:"room"`
	Seat     string `json:"seat"`
	State    string `json:"state"`
	Runtime  string `json:"runtime"`
	Hash     string `json:"pinned_hash"`
	Detail   string `json:"detail"`
	Redacted bool   `json:"redacted"`
}

// AgentStatus is one reported herdr agent.
type AgentStatus struct {
	Agent  string `json:"agent"`
	Status string `json:"status"`
	PaneID string `json:"pane_id"`
}

// Payload is what a report carries. LocalRecord is DUMP-ONLY: the Python adds it for `once --print`
// and the server refuses unknown report fields, so it is nil on the wire and filled for the dump.
type Payload struct {
	MachineID   string              `json:"machine_id"`
	ReportedAt  string              `json:"reported_at"`
	Seats       []SeatStatus        `json:"seats"`
	Agents      []AgentStatus       `json:"agents"`
	Edges       []EdgeStatus        `json:"edges"`
	LocalRecord map[string]SeatNote `json:"local_record,omitempty"`
}

// SeatNote is what this machine can say about a seat from its OWN record, and nothing more: it
// answers only whether the seat has changed since the cloud last CONFIRMED it in sync, so the words
// are unchanged/changed/unknown and never the cloud's in_sync. The cloud can move while this machine
// does not, and only the answer to a report knows the cloud's current expectation.
type SeatNote struct {
	Running         string `json:"running"`
	LastInSync      string `json:"last_in_sync"`
	SinceLastInSync string `json:"since_last_in_sync"`
}

// str / dict / mapOf mirror the Python helpers: a value of the wrong shape reads as empty rather
// than failing the report.
func str(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func dict(value any) map[string]any {
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// SeatState maps one OpenRig node to the four states the cloud accepts. The rules are the Python
// ones, kept whole because each had a measurement behind it:
//
//   - a stopped or exited session, or a detached/recoverable lifecycle, is stopped;
//   - a live session whose runtime hook is gone reads stopped (agent died outside `rig seat stop`),
//     which the reason discriminates - and the reason is runtime-dependent, which is why the
//     respawn path requires the reading to hold rather than acting on one sample;
//   - `attention_required` is deliberately NOT blocked: OpenRig uses it for a dead agent AND a busy
//     seat, so it cannot tell them apart and the agent's own activity is the truth source;
//   - needs_input, or a failed/attention-required startup, is blocked.
func SeatState(node map[string]any) string {
	session := str(node["sessionStatus"])
	life := str(node["lifecycleState"])
	startup := str(node["startupStatus"])
	activityObj := dict(node["agentActivity"])
	activity := str(activityObj["state"])
	if session == "stopped" || session == "exited" || life == "detached" || life == "recoverable" {
		return "stopped"
	}
	if activity == "unknown" && str(activityObj["reason"]) == "no_runtime_hook" {
		return "stopped"
	}
	if activity == "needs_input" || startup == "attention_required" || startup == "failed" {
		return "blocked"
	}
	if activity == "running" || activity == "idle" {
		return activity
	}
	return "unknown"
}

// SeatName is the member id of a node: OpenRig ids never contain a dot, so "<pod>.<member>" splits
// at the first one.
func SeatName(node map[string]any) string {
	logical := str(node["logicalId"])
	_, member, found := strings.Cut(logical, ".")
	if !found || member == "" {
		return logical
	}
	return member
}

// SeatPod is the pod id of a node, or empty when the id carries none.
func SeatPod(node map[string]any) string {
	logical := str(node["logicalId"])
	pod, _, found := strings.Cut(logical, ".")
	if !found {
		return ""
	}
	return pod
}

// SeatDetail is the diagnostic text a report may carry, before redaction.
func SeatDetail(node map[string]any) string {
	parts := []string{
		str(node["latestError"]),
		str(node["heldReason"]),
		str(dict(node["agentActivity"])["reason"]),
	}
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "; ")
}

// PinnedHash reads the hash the client pinned for a seat, or empty. A pin exists only once the client
// has pulled that seat, so an empty hash is the honest answer for a seat no client has adopted - and
// a parity fixture with no pinned seat would exercise this field as always-empty.
func PinnedHash(pinsDir, room, seat string) string {
	raw, err := os.ReadFile(filepath.Join(pinsDir, room, seat, "pinned.json"))
	if err != nil {
		return ""
	}
	var lock struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		return ""
	}
	if !hashPattern.MatchString(lock.Hash) {
		return ""
	}
	return lock.Hash
}

// DuplicateMessage names the pods a duplicated seat key was found in, in the Python's wording, so an
// operator reading either client sees the same sentence.
func DuplicateMessage(room, seat string, pods []string) string {
	seen := map[string]bool{}
	names := make([]string, 0, len(pods))
	for _, pod := range pods {
		label := pod
		if label == "" {
			label = "(no pod)"
		}
		if !seen[label] {
			seen[label] = true
			names = append(names, label)
		}
	}
	sortStrings(names)
	if len(names) == 1 {
		return fmt.Sprintf("seat '%s' in rig %s is listed twice in pod %s; rename one", seat, room, names[0])
	}
	return fmt.Sprintf("seat '%s' in rig %s exists in pods %s and %s; rename one",
		seat, room, strings.Join(names[:len(names)-1], ", "), names[len(names)-1])
}

// BuildSeats turns OpenRig nodes into reported seats. It returns the seats, how many nodes had a name
// this client will not report, and one message per duplicated seat key - the duplicates are warned
// about rather than dropped, because a seat listed twice in a rig is something a human must fix.
//
// Detail is redacted here, with the same call the server-side scanner shares: stripDetail is the
// Python's degradation after a 422, where the cloud refused the text as secret-bearing.
func BuildSeats(nodes []any, known map[string]string, pinsDir string, stripDetail bool) ([]SeatStatus, int, []string) {
	seats := make([]SeatStatus, 0, len(nodes))
	podsOf := map[string][]string{}
	invalid := 0
	for _, raw := range nodes {
		node := dict(raw)
		room, seat := str(node["rigName"]), SeatName(node)
		if !namePattern.MatchString(room) || !namePattern.MatchString(seat) {
			invalid++
			continue
		}
		key := room + "/" + seat
		if _, seen := podsOf[key]; seen {
			podsOf[key] = append(podsOf[key], SeatPod(node))
			continue
		}
		podsOf[key] = []string{SeatPod(node)}
		detail, redacted := "", true
		if !stripDetail {
			detail, redacted = secretscan.Redact(SeatDetail(node), known)
		}
		runtime := str(node["runtime"])
		if !runtimes[runtime] {
			runtime = "unknown"
		}
		seats = append(seats, SeatStatus{
			Room: room, Seat: seat, State: SeatState(node), Runtime: runtime,
			Hash: PinnedHash(pinsDir, room, seat), Detail: detail, Redacted: redacted,
		})
	}
	duplicates := make([]string, 0)
	for key, pods := range podsOf {
		if len(pods) > 1 {
			room, seat, _ := strings.Cut(key, "/")
			duplicates = append(duplicates, DuplicateMessage(room, seat, pods))
		}
	}
	sortStrings(duplicates)
	return seats, invalid, duplicates
}

// BuildAgents turns raw herdr agents into reported ones, dropping any whose name or pane id fails its
// pattern rather than reporting a field the cloud would have to defend against.
func BuildAgents(raw []any) []AgentStatus {
	agents := make([]AgentStatus, 0, len(raw))
	for _, entry := range raw {
		item := dict(entry)
		name, pane := str(item["agent"]), str(item["pane_id"])
		if !namePattern.MatchString(name) || !panePattern.MatchString(pane) {
			continue
		}
		status := str(item["agent_status"])
		if !agentStatuses[status] {
			status = "unknown"
		}
		agents = append(agents, AgentStatus{Agent: name, Status: status, PaneID: pane})
	}
	return agents
}

// ParseRigNodes reads `rig ps --json --nodes` output: a list, or an object carrying `items`.
func ParseRigNodes(output string) ([]any, error) {
	var decoded any
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		return nil, err
	}
	switch value := decoded.(type) {
	case []any:
		return value, nil
	case map[string]any:
		if items, ok := value["items"].([]any); ok {
			return items, nil
		}
	}
	return nil, fmt.Errorf("unexpected rig ps JSON shape")
}

// ParseHerdrAgents reads `herdr api snapshot` output: result.snapshot.agents.
func ParseHerdrAgents(output string) ([]any, error) {
	var decoded any
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		return nil, err
	}
	snapshot := dict(dict(dict(decoded)["result"])["snapshot"])
	if agents, ok := snapshot["agents"].([]any); ok {
		return agents, nil
	}
	return nil, fmt.Errorf("unexpected herdr snapshot JSON shape")
}

// SanitizeMachineID turns a hostname into an id the server accepts, in the Python's exact steps.
func SanitizeMachineID(name string) string {
	cleaned := regexp.MustCompile(`[^a-zA-Z0-9_-]`).ReplaceAllString(name, "-")
	cleaned = strings.TrimLeft(cleaned, "-_")
	if cleaned == "" {
		return "machine"
	}
	return cleaned
}

// UTCNow is the report timestamp, in the one format the Python emits.
func UTCNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

// sortStrings keeps every list deterministic, which is what makes a payload byte-comparable in the
// parity test and a rebuild not a diff.
func sortStrings(values []string) {
	sort.Strings(values)
}
