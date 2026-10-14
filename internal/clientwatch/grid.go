package clientwatch

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// The compaction limits the token bar is drawn against. Each is read from the environment so it can be
// changed without editing code, and a BLANK one means the default rather than a failed parse.
const (
	defaultCompactLimit = 150_000
	defaultWarnLimit    = 250_000
	defaultHardLimit    = 300_000
)

func compactLimit() int {
	value, err := clientenv.Limit("COMPACT_LIMIT_TOKENS", defaultCompactLimit)
	if err != nil {
		return defaultCompactLimit
	}
	return value
}

func warnLimit() int {
	value, err := clientenv.Limit("COMPACT_WARN_TOKENS", defaultWarnLimit)
	if err != nil {
		return defaultWarnLimit
	}
	return value
}

func hardLimit() int {
	value, err := clientenv.Limit("COMPACT_HARD_TOKENS", defaultHardLimit)
	if err != nil {
		return defaultHardLimit
	}
	return value
}

// The input panes are the only way this package writes to a seat, and they do it through `rig send`,
// the same route any other message takes - never a raw `tmux attach` pane.
const (
	inputSuffix    = " > input"
	inputRowsRatio = 0.65
	leadRowsRatio  = 0.8
)

// rigSeats is the seats with a live tmux session; the omp state dirs outlive removed seats.
func rigSeats(ctx context.Context, rig string) ([]string, error) {
	argv, err := clientenv.TmuxArgv("list-sessions", "-F", "#{session_name}")
	if err != nil {
		return nil, err
	}
	out, err := execCommand(ctx, argv[0], argv[1:]...)
	if err != nil {
		return nil, fmt.Errorf("tmux list-sessions failed: %w", err)
	}
	seats := []string{}
	for _, name := range strings.Fields(out) {
		if room, seat, ok := clientruntime.ParseSeatSessionName(name); ok && room == rig {
			seats = append(seats, seat)
		}
	}
	sort.Strings(seats)
	return seats, nil
}

// seatCommand is the line a grid pane runs: this seat's own feed.
func seatCommand(rig, seat, feedArgs string) string {
	return fmt.Sprintf("feed --rig %s --seat %s %s", rig, seat, feedArgs)
}

// closeWorkspaces closes every workspace with this label, so a relaunch leaves one wall, not a stack.
func closeWorkspaces(ctx context.Context, label string) error {
	result, err := herdr(ctx, "workspace", "list")
	if err != nil {
		return err
	}
	for _, entry := range objectList(result, "workspaces") {
		if textOf(entry, "label") != label {
			continue
		}
		if _, err := herdr(ctx, "workspace", "close", textOf(entry, "workspace_id")); err != nil {
			return err
		}
	}
	return nil
}

func objectList(result map[string]any, key string) []map[string]any {
	raw, ok := result[key].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		if object, ok := entry.(map[string]any); ok {
			out = append(out, object)
		}
	}
	return out
}

func textOf(object map[string]any, key string) string {
	text, _ := object[key].(string)
	return text
}

// runGrid opens one pane per seat. Columns are split first, then each column into equal rows.
func runGrid(ctx context.Context, s shared, out io.Writer) error {
	seats, err := rigSeats(ctx, s.rig)
	if err != nil {
		return err
	}
	if err := closeWorkspaces(ctx, s.rig+" grid"); err != nil {
		return err
	}
	created, err := herdr(ctx, "workspace", "create", "--cwd", s.workingDir(), "--label", s.rig+" grid")
	if err != nil {
		return err
	}
	root := paneID(created)
	if root == "" {
		return unavailableError("herdr created no pane for the grid workspace")
	}

	split := func(pane, direction string, ratio float64) (string, error) {
		result, err := herdr(ctx, "pane", "split", "--pane", pane, "--direction", direction,
			"--ratio", fmt.Sprintf("%.3f", ratio), "--cwd", s.workingDir())
		if err != nil {
			return "", err
		}
		id := paneID(result)
		if id == "" {
			return "", unavailableError("herdr split a pane but named no new pane")
		}
		return id, nil
	}

	heads := []string{root}
	for left := s.cols - 1; left > 0; left-- {
		created, err := split(heads[len(heads)-1], "right", 1/float64(left+1))
		if err != nil {
			return err
		}
		heads = append(heads, created)
	}
	perColumn := (len(seats) + s.cols - 1) / s.cols
	panes := []string{}
	for _, head := range heads {
		current := head
		panes = append(panes, current)
		for left := perColumn - 1; left > 0; left-- {
			created, err := split(current, "down", 1/float64(left+1))
			if err != nil {
				return err
			}
			current = created
			panes = append(panes, current)
		}
	}
	for index, pane := range panes {
		if index >= len(seats) {
			break
		}
		seat := seats[index]
		if _, err := herdr(ctx, "pane", "rename", pane, seat); err != nil {
			return err
		}
		if _, err := herdr(ctx, "pane", "send-text", pane, paneCommand(seatCommand(s.rig, seat, s.gridCommandArgs()))); err != nil {
			return err
		}
		if _, err := herdr(ctx, "pane", "send-keys", pane, "Enter"); err != nil {
			return err
		}
	}
	workspace := root
	if head, _, found := strings.Cut(root, ":"); found {
		workspace = head
	}
	if _, err := herdr(ctx, "workspace", "focus", workspace); err != nil {
		return err
	}
	fmt.Fprintf(out, "grid: %d seats in %d columns\n", len(seats), s.cols)
	return nil
}

// gridWorkspace is the id of the newest "<rig> grid" workspace.
func gridWorkspace(ctx context.Context, rig string) (string, error) {
	result, err := herdr(ctx, "workspace", "list")
	if err != nil {
		return "", err
	}
	found := ""
	for _, entry := range objectList(result, "workspaces") {
		if textOf(entry, "label") == rig+" grid" {
			found = textOf(entry, "workspace_id")
		}
	}
	if found == "" {
		return "", unavailableError(fmt.Sprintf("no '%s grid' workspace: run grid first", rig))
	}
	return found, nil
}

func gridPanes(ctx context.Context, rig string) ([]map[string]any, error) {
	workspace, err := gridWorkspace(ctx, rig)
	if err != nil {
		return nil, err
	}
	result, err := herdr(ctx, "pane", "list")
	if err != nil {
		return nil, err
	}
	panes := []map[string]any{}
	for _, entry := range objectList(result, "panes") {
		if textOf(entry, "workspace_id") == workspace {
			panes = append(panes, entry)
		}
	}
	return panes, nil
}

// runInputs opens or hides the input pane under every seat pane of the grid.
func runInputs(ctx context.Context, s shared, action string, out io.Writer) error {
	panes, err := gridPanes(ctx, s.rig)
	if err != nil {
		return err
	}
	seats := map[string]bool{}
	if len(s.seats) > 0 {
		for _, seat := range s.seats {
			seats[seat] = true
		}
	} else {
		live, err := rigSeats(ctx, s.rig)
		if err != nil {
			return err
		}
		for _, seat := range live {
			seats[seat] = true
		}
	}
	if action == "hide" {
		closed := 0
		for _, pane := range panes {
			if !strings.HasSuffix(textOf(pane, "label"), inputSuffix) {
				continue
			}
			if _, err := herdr(ctx, "pane", "close", textOf(pane, "pane_id")); err != nil {
				return err
			}
			closed++
		}
		fmt.Fprintf(out, "inputs: hid %d\n", closed)
		return nil
	}
	openFor := map[string]bool{}
	for _, pane := range panes {
		if label := textOf(pane, "label"); strings.HasSuffix(label, inputSuffix) {
			openFor[label] = true
		}
	}
	opened := 0
	for _, pane := range panes {
		seat := textOf(pane, "label")
		if !seats[seat] || openFor[seat+inputSuffix] {
			continue
		}
		result, err := herdr(ctx, "pane", "split", "--pane", textOf(pane, "pane_id"),
			"--direction", "down", "--ratio", fmt.Sprintf("%.3f", inputRowsRatio), "--cwd", s.workingDir())
		if err != nil {
			return err
		}
		created := paneID(result)
		if _, err := herdr(ctx, "pane", "rename", created, seat+inputSuffix); err != nil {
			return err
		}
		if _, err := herdr(ctx, "pane", "send-text", created,
			paneCommand(fmt.Sprintf("input --rig %s --seat %s", s.rig, seat))); err != nil {
			return err
		}
		if _, err := herdr(ctx, "pane", "send-keys", created, "Enter"); err != nil {
			return err
		}
		opened++
	}
	fmt.Fprintf(out, "inputs: opened %d\n", opened)
	return nil
}

// sendLine delivers one typed line to its seat, in process. A var so the tests drive the loop without a
// daemon or a runtime.
//
// The seat's session name is READ from the runtime's room record rather than composed from a convention, and
// the sender is preserved: a line typed at the grid is written BY that seat's watcher, so it is stamped with
// the watcher's own session.
//
// The three calls below are the send verb's own primitives, and this is the THIRD writer of that shape the
// `saySender` comment in clientlifecycle names as the moment to consolidate it into the runtime client.
// Consolidating means touching the gated send path, so it is left to its own unit rather than smuggled in
// here.
var sendLine = func(ctx context.Context, rig, seat, text string) error {
	address, err := clientruntime.ResolveSeatAddress(rig, seat)
	if err != nil {
		return err
	}
	_, err = clientruntime.RuntimeSendText(address.SessionName, text, clientruntime.SendOptions{Sender: os.Getenv("AGENTHUB_SESSION_NAME")})
	return err
}

// runInputLoop reads lines and sends each to its seat, the same route any other message takes. An empty
// line skips; Ctrl-D or /hide leaves.
func runInputLoop(ctx context.Context, rig, seat string, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprintf(out, "%s > ", seat)
		if !scanner.Scan() {
			return nil
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "/hide" {
			return nil
		}
		if line == "" {
			continue
		}
		if err := sendLine(ctx, rig, seat, line); err != nil {
			message := err.Error()
			if len(message) > 200 {
				message = message[:200]
			}
			fmt.Fprintf(out, "failed: %s\n", message)
			continue
		}
		fmt.Fprintln(out, "sent")
	}
}

// runLeadWindow opens a workspace for the lead alone: its detailed feed on top, an input pane under it.
func runLeadWindow(ctx context.Context, s shared) error {
	if err := closeWorkspaces(ctx, s.rig+" lead"); err != nil {
		return err
	}
	created, err := herdr(ctx, "workspace", "create", "--cwd", s.workingDir(),
		"--label", s.rig+" lead", "--no-focus")
	if err != nil {
		return err
	}
	root := paneID(created)
	split, err := herdr(ctx, "pane", "split", "--pane", root, "--direction", "down",
		"--ratio", fmt.Sprintf("%.3f", leadRowsRatio), "--cwd", s.workingDir())
	if err != nil {
		return err
	}
	below := paneID(split)
	if _, err := herdr(ctx, "pane", "rename", below, "lead"+inputSuffix); err != nil {
		return err
	}
	commands := []struct {
		pane    string
		command string
	}{
		{root, fmt.Sprintf("feed --rig %s --seat lead --back 40 --width 0 --lines 25 --detail", s.rig)},
		{below, fmt.Sprintf("input --rig %s --seat lead", s.rig)},
	}
	for _, entry := range commands {
		if _, err := herdr(ctx, "pane", "send-text", entry.pane, paneCommand(entry.command)); err != nil {
			return err
		}
		if _, err := herdr(ctx, "pane", "send-keys", entry.pane, "Enter"); err != nil {
			return err
		}
	}
	return nil
}

// runWatch is everything needed to watch a rig work: the seat grid and the lead window.
func runWatch(ctx context.Context, s shared, out io.Writer) error {
	if err := runGrid(ctx, s, out); err != nil {
		return err
	}
	return runLeadWindow(ctx, s)
}
