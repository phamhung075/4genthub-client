// Package clientps is `4genteam ps`: what a room's seats are doing right now, read from local tmux
// sessions and each seat's own transcript. It needs no rig daemon.
package clientps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientwatch"
)

const usage = "usage: 4genteam ps [--state-root DIR] [RIG] [--nodes | --compact | --grid] [--json]"

// quotaMarkers are the provider messages a seat prints when it cannot answer; any of them in the
// transcript tail means the seat is waiting at its prompt for a reason that is not its own.
var quotaMarkers = []string{"Individual quota reached", "Out of credits", "rate limit", "usage limit"}

// Seat is one tmux-backed seat of a rig.
type Seat struct {
	Rig     string `json:"rig"`
	Member  string `json:"member"`
	Session string `json:"session"`
	// The node-record keys every other verb reads from `ps --nodes --room R --json`: a tmux session
	// that exists is a running session, whatever the seat's own state (Status) says about its provider.
	RigName       string `json:"rigName"`
	LogicalID     string `json:"logicalId"`
	CanonicalName string `json:"canonicalSessionName"`
	SessionStatus string `json:"sessionStatus"`
	StartupStatus string `json:"startupStatus"`
	Runtime       string `json:"runtime"`
	Model         string `json:"model"`
	Status        string `json:"status"`
	Attached      bool   `json:"attached"`
	Uptime        string `json:"uptime"`
	Tokens        int    `json:"contextTokens"`
	Limit         int    `json:"compactLimit"`
	Activity      string `json:"activity"`
	Error         string `json:"error"`
	LogPath       string `json:"log"`
	startedAt     time.Time
}

// RigRow is the per-rig rollup.
type RigRow struct {
	Rig     string `json:"rig"`
	Seats   int    `json:"seats"`
	Attn    int    `json:"attention"`
	Status  string `json:"status"`
	Uptime  string `json:"uptime"`
	started time.Time
}

type command struct{}

// Commands registers `ps`.
func Commands() []clientcmd.Command { return []clientcmd.Command{command{}} }

func (command) Name() string { return "ps" }
func (command) Summary() string {
	return "the seats of a room: summary, --nodes, --compact, --grid, --json"
}

func (command) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, usage)
		fmt.Fprintln(stdout, "\nRead from local tmux sessions and the seats' transcripts. A seat whose transcript tail "+
			"shows a provider quota or credit message is reported as `quota`.")
		return clientcmd.ExitOK
	}
	rootFlag, rest, err := clientenv.StateRootFromFlags(args)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam ps: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitUnavailable)
	}
	args = rest
	stateRoot, err := clientenv.ResolveStateRoot(rootFlag)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam ps: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitUnavailable)
	}
	view, asJSON, rig, wantRig := "summary", false, "", false
	for _, arg := range args {
		switch arg {
		case "--nodes":
			view = "nodes"
		case "--compact":
			view = "compact"
		case "--grid":
			view = "grid"
		case "--json":
			asJSON = true
		case "--room":
			wantRig = true
		default:
			if wantRig {
				rig, wantRig = arg, false
				continue
			}
			if strings.HasPrefix(arg, "-") || rig != "" {
				fmt.Fprintf(stderr, "4genteam ps: unexpected argument %q\n%s\n", arg, usage)
				return clientcmd.ExitUsage
			}
			rig = arg
		}
	}
	sessions, err := listSessions(ctx, stateRoot)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam ps: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	if len(sessions) == 0 {
		// An empty answer is an answer only when it says what was looked at: the server this process would use.
		fmt.Fprintln(stderr, "4genteam ps: no tmux sessions on the server this process would use")
	}
	seats := collect(stateRoot, sessions, rig)
	if reason := emptyReason(rig, len(sessions), len(seats)); reason != "" {
		fmt.Fprintf(stderr, "4genteam ps: %s\n", reason)
	}
	if asJSON {
		return writeJSON(stdout, stderr, view, seats)
	}
	switch view {
	case "nodes":
		renderNodes(stdout, seats)
	case "compact":
		renderCompact(stdout, seats)
	case "grid":
		renderGrid(stdout, seats)
	default:
		renderSummary(stdout, rollup(seats))
	}
	return clientcmd.ExitOK
}

type session struct {
	name     string
	created  time.Time
	attached bool
}

func listSessions(ctx context.Context, root string) ([]session, error) {
	argv, err := clientenv.TmuxArgvAt(root, "list-sessions", "-F", "#{session_name}\t#{session_created}\t#{session_attached}")
	if err != nil {
		return nil, err
	}
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if err != nil {
		// A tmux server that is not there is NOT a failed read: it is the fact "no sessions", the same
		// reading clientrestore takes for the same command, and tmux spells that fact two ways -
		// "no server running on ..." for a live socket dir and "error connecting to ... (No such file or
		// directory)" for a root where no server ever started. A failed read has output; this one has none.
		if len(bytes.TrimSpace(out)) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("tmux list-sessions failed: %w", err)
	}
	var sessions []session
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			continue
		}
		epoch, _ := strconv.ParseInt(parts[1], 10, 64)
		sessions = append(sessions, session{name: parts[0], created: time.Unix(epoch, 0), attached: parts[2] != "0"})
	}
	return sessions, nil
}

func collect(stateRoot string, sessions []session, only string) []Seat {
	var seats []Seat
	for _, s := range sessions {
		rig, member, ok := clientruntime.ParseSeatSessionName(s.name)
		if !ok || (only != "" && rig != only) {
			continue
		}
		snap := clientwatch.SeatSnapshot(stateRoot, rig, member)
		seat := Seat{
			Rig: rig, Member: member, Session: s.name, Runtime: snap.Runtime, Model: snap.Model,
			Attached: s.attached, Uptime: since(s.created), Tokens: snap.ContextTokens,
			Limit: snap.CompactLimit, LogPath: snap.LogPath, startedAt: s.created, Status: "running",
			RigName: rig, LogicalID: rig + "." + member, CanonicalName: s.name,
			SessionStatus: "running", StartupStatus: "ready",
		}
		if marker := quotaIn(snap.LogPath); marker != "" {
			seat.Status, seat.Error = "quota", marker
		}
		seat.Activity = activity(snap.LogPath)
		seats = append(seats, seat)
	}
	sort.Slice(seats, func(i, j int) bool {
		if seats[i].Rig != seats[j].Rig {
			return seats[i].Rig < seats[j].Rig
		}
		return seats[i].Member < seats[j].Member
	})
	return seats
}

func quotaIn(path string) string {
	tail := tailOf(path, 4096)
	for _, marker := range quotaMarkers {
		if strings.Contains(tail, marker) {
			return marker
		}
	}
	return ""
}

// activity is how long ago the seat's log last changed: idle seats are the interesting ones.
func activity(path string) string {
	if path == "" {
		return "-"
	}
	info, err := os.Stat(path)
	if err != nil {
		return "-"
	}
	return since(info.ModTime()) + " ago"
}

func tailOf(path string, n int64) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	offset := info.Size() - n
	if offset < 0 {
		offset = 0
	}
	buf := make([]byte, info.Size()-offset)
	read, _ := f.ReadAt(buf, offset)
	return strings.ToValidUTF8(string(buf[:read]), "")
}

func since(t time.Time) string {
	d := time.Since(t).Round(time.Minute)
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours())/24)
}

func rollup(seats []Seat) []RigRow {
	byRig := map[string]*RigRow{}
	var order []string
	for _, s := range seats {
		row, ok := byRig[s.Rig]
		if !ok {
			row = &RigRow{Rig: s.Rig, Status: "running", started: s.startedAt}
			byRig[s.Rig] = row
			order = append(order, s.Rig)
		}
		row.Seats++
		if s.Status != "running" {
			row.Attn++
			row.Status = "attention"
		}
		if s.startedAt.Before(row.started) {
			row.started = s.startedAt
		}
	}
	sort.Strings(order)
	rows := make([]RigRow, 0, len(order))
	for _, rig := range order {
		byRig[rig].Uptime = since(byRig[rig].started)
		rows = append(rows, *byRig[rig])
	}
	return rows
}

// emptyReason is what a caller is told when a named room matched nothing while sessions DID exist. An
// empty list and a verb that cannot see the room read identically on stdout, and that is the difference
// F-A was reported over: `ps <room> --nodes --json` answering `null` while the room was alive. The list
// is never `null` now, so the reason has to travel on stderr or the reader still cannot tell the two
// apart. Silence is the answer when there is nothing to distinguish: no room was named, the server had
// no sessions at all (Run already says so), or the room's seats are right there in the list.
func emptyReason(rig string, sessions, seats int) string {
	if rig == "" || sessions == 0 || seats > 0 {
		return ""
	}
	return fmt.Sprintf("no seats for rig %s on the server this process would use (%d session(s) there, none named %s-*)",
		rig, sessions, rig)
}

func writeJSON(stdout, stderr io.Writer, view string, seats []Seat) int {
	if seats == nil {
		// A documented list is never `null`: callers parse this output, and a room this verb cannot see used to
		// come back as `null`, which reads as a bug rather than as an empty answer. `Run` says on stderr what it
		// looked at; here the shape of the answer is fixed.
		seats = []Seat{}
	}
	var payload any = seats
	if view == "summary" {
		payload = rollup(seats)
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "4genteam ps: cannot print the rows: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	fmt.Fprintln(stdout, string(body))
	return clientcmd.ExitOK
}

// cell pads to width and truncates with an ellipsis, so one long value cannot shift its neighbours.
func cell(value string, width int) string {
	runes := []rune(value)
	if value == "" {
		runes, value = []rune("-"), "-"
	}
	if len(runes) > width {
		return string(runes[:width-1]) + "…"
	}
	return value + strings.Repeat(" ", width-len(runes))
}

func row(widths []int, values ...string) string {
	var b strings.Builder
	for i, v := range values {
		b.WriteString(cell(v, widths[i]))
		b.WriteByte(' ')
	}
	return strings.TrimRight(b.String(), " ")
}

func renderSummary(w io.Writer, rows []RigRow) {
	seats, attn := 0, 0
	for _, r := range rows {
		seats += r.Seats
		attn += r.Attn
	}
	fmt.Fprintf(w, "%d rig(s) · %d seat(s) · %d need(s) attention\n\n", len(rows), seats, attn)
	widths := []int{30, 6, 5, 11, 11}
	fmt.Fprintln(w, row(widths, "RIG", "SEATS", "ATTN", "STATUS", "UPTIME"))
	for _, r := range rows {
		fmt.Fprintln(w, row(widths, r.Rig, strconv.Itoa(r.Seats), strconv.Itoa(r.Attn), r.Status, r.Uptime))
	}
}

func ctxCell(s Seat) string {
	if s.Tokens == 0 {
		return "-"
	}
	return fmt.Sprintf("%dk", s.Tokens/1000)
}

// renderNodes prints the node table. Columns whose source (the rig orchestrator registry) no longer exists are
// not invented: they were dropped, and the columns that remain are the ones a local read can fill.
func renderNodes(w io.Writer, seats []Seat) {
	widths := []int{22, 14, 34, 8, 22, 9, 11, 9, 7, 12, 30}
	fmt.Fprintln(w, row(widths, "RIG", "MEMBER", "SESSION", "RUNTIME", "MODEL", "STATUS", "ATTACHED", "UPTIME", "CTX", "ACTIVITY", "ERROR"))
	for _, s := range seats {
		fmt.Fprintln(w, row(widths, s.Rig, s.Member, s.Session, s.Runtime, s.Model, s.Status,
			strconv.FormatBool(s.Attached), s.Uptime, ctxCell(s), s.Activity, s.Error))
	}
}

func renderCompact(w io.Writer, seats []Seat) {
	widths := []int{22, 38, 9, 14, 7, 30}
	fmt.Fprintln(w, row(widths, "RIG", "SESSION", "STATUS", "ACTIVITY", "CTX", "REASON"))
	for _, s := range seats {
		fmt.Fprintln(w, row(widths, s.Rig, s.Session, s.Status, s.Activity, ctxCell(s), s.Error))
	}
}

func meter(tokens, limit, width int) string {
	if limit <= 0 {
		return strings.Repeat("░", width)
	}
	filled := tokens * width / limit
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

func renderGrid(w io.Writer, seats []Seat) {
	const inner = 44
	line := func(text string) string { return "│ " + cell(text, inner) + " │" }
	for _, s := range seats {
		fmt.Fprintln(w, "┌─"+strings.Repeat("─", inner)+"─┐")
		fmt.Fprintln(w, line(s.Member+"  ["+s.Status+"]"))
		fmt.Fprintln(w, line(s.Rig+" · "+s.Runtime+" · "+s.Model))
		fmt.Fprintln(w, line(fmt.Sprintf("ctx %s %s/%s", meter(s.Tokens, s.Limit, 20), ctxCell(s), fmt.Sprintf("%dk", s.Limit/1000))))
		fmt.Fprintln(w, line("up "+s.Uptime+" · active "+s.Activity))
		if s.Error != "" {
			fmt.Fprintln(w, line("✗ "+s.Error))
		}
		fmt.Fprintln(w, "└─"+strings.Repeat("─", inner)+"─┘")
	}
}
