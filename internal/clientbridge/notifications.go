package clientbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// D3 - THE PRODUCER HALF OF AGENT-TO-HUMAN NOTIFICATIONS.
//
// The cloud already knows how to carry an agent-to-human message: the dashboard stores and toasts a
// frame whose payload.entity is 'notification', and the server replays one from the
// missed-notification store on reconnect. What did not exist is a producer. This file is it: the
// bridge reads the OpenRig rows ADDRESSED TO A HUMAN and posts one notification frame per row.
//
// THE FRAME CONTRACT IS FIXED BY THE CONSUMER, NOT CHOSEN HERE (each line read, 2026-10-10):
//   - POST /api/v2/broadcast/notify requires event_type, entity_type and entity_id, and takes the
//     TARGET USER FROM THE CALLER'S TOKEN: the route is inside authed(...) and builds UserID from the
//     token, so no user id is sent here and a body user_id would be ignored
//     (agenthub_go/fastmcp/server/httpapp/routes_mount.go:173-186).
//   - the server copies the body's `data` object into payload.data.primary and its entity_id into
//     metadata (agenthub_go/fastmcp/server/routes/websocket_routes.go:442-470).
//   - the client deduplicates on metadata.entity_id and DROPS a frame whose primary.message is empty,
//     so entity_id must be stable for a row and the message must carry text
//     (agenthub-frontend/src/hooks/useRealtimeSync.ts:911-931, store/notifications.ts:add).
//   - a notification addressed to an offline user is stored and replayed on that user's next connect,
//     at most 100 per connect, each marked delivered when it was sent (stored at
//     websocket_routes.go:580-616, replayed at httpapp/ws_mount.go:220-240).
//
// WHAT IS DELIBERATELY NOT SENT: a user id (the token is the target), and any metadata of our own - the
// route passes the body's metadata through into the frame, and the store path reads metadata.user_id as
// an ADDITIONAL offline target, so sending metadata would be a way to address someone else's inbox.
const (
	// humanDestinationPrefix addresses one rig's human. The daemon's own destination form is
	// "human@<rig name>" - measured against a live store, which carries one such destination per rig,
	// each naming the rig it belongs to.
	humanDestinationPrefix = "human@"
	// notifyKind is the free-form kind the payload documents, for example 'notice'.
	notifyKind = "notice"
	// notifyMessageLimit bounds the toast text: a queue body can be a paragraph, and the frame is
	// read by a human at a glance.
	notifyMessageLimit = 300
	// notifyRowsPerRig bounds one read, so a rig with a long human queue cannot make a cycle unbounded.
	notifyRowsPerRig = 100
	// notifyPostsPerCycle bounds one cycle's posts. A first run against a rig holding dozens of human
	// rows walks them over several cycles instead of posting all of them at once.
	notifyPostsPerCycle = 20
	// notifyStateLimit bounds the sent-id record, so a bridge running for months cannot grow its own
	// state file without end.
	notifyStateLimit = 500
)

// HumanRow is one OpenRig queue row addressed to a human, reduced to what a notification needs.
type HumanRow struct {
	ID       string
	Rig      string
	Source   string
	State    string
	Summary  string
	BodyText string
}

// HumanDestination is the queue destination that addresses one rig's human.
func HumanDestination(rigName string) string {
	return humanDestinationPrefix + rigName
}

// ParseRigNames reads `rig ps --json`: the rigs this machine can see. Only the name is used, because
// the human destination is `human@<name>`.
func ParseRigNames(output string) ([]any, error) {
	return parseItems(output, "rig ps")
}

// ParseHumanRows reads `rig queue list --destination human@<rig> --json`.
func ParseHumanRows(output string) ([]any, error) {
	return parseItems(output, "rig queue list")
}

// parseItems unwraps a JSON listing: a list, or an object carrying `items`. Both shapes are what the
// rig tool prints, and one helper keeps the two readers from disagreeing about it.
func parseItems(output, what string) ([]any, error) {
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
	return nil, fmt.Errorf("unexpected %s JSON shape", what)
}

// HumanRowFrom reduces one raw queue item. It returns false for an item with no id, and for one whose
// destination is NOT a human: the read is already destination-filtered, and checking it here means a
// change to the filter cannot silently turn another seat's rows into the human's notifications.
func HumanRowFrom(item any, rigName string) (HumanRow, bool) {
	row := dict(item)
	id := str(row["qitemId"])
	destination := str(row["destinationSession"])
	if id == "" || !strings.HasPrefix(destination, humanDestinationPrefix) {
		return HumanRow{}, false
	}
	return HumanRow{
		ID:       id,
		Rig:      rigName,
		Source:   str(row["sourceSession"]),
		State:    str(row["state"]),
		Summary:  str(row["summary"]),
		BodyText: str(row["body"]),
	}, true
}

// NotificationBody builds the frame for one human row. It returns false when the row carries no text
// to show: the consumer drops a frame with an empty message, so posting one would be a silent no-op.
func NotificationBody(row HumanRow) ([]byte, bool) {
	text := notifyText(row)
	if text == "" {
		return nil, false
	}
	frame := map[string]any{
		"event_type":  "notification",
		"entity_type": "notification",
		"entity_id":   row.ID,
		"data": map[string]any{
			"message": text,
			"kind":    notifyKind,
		},
	}
	data := frame["data"].(map[string]any)
	if row.Source != "" {
		// from is a seat key, and it is the first half of the toast the human reads.
		data["from"] = row.Source
		if seat, _, found := strings.Cut(row.Source, "@"); found {
			data["seat"] = seat
		}
	}
	if row.Rig != "" {
		data["room"] = row.Rig
	}
	body, err := json.Marshal(frame)
	if err != nil {
		return nil, false
	}
	return body, true
}

// notifyText is what the human reads: the row's summary when it has one, otherwise the first non-empty
// line of its body, bounded to notifyMessageLimit.
func notifyText(row HumanRow) string {
	text := strings.TrimSpace(row.Summary)
	if text == "" {
		for _, line := range strings.Split(row.BodyText, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				text = line
				break
			}
		}
	}
	if len(text) > notifyMessageLimit {
		text = strings.TrimRight(text[:notifyMessageLimit], " ") + "..."
	}
	return text
}

// notifyKey is the row's identity in the sent-id record: the rig plus the row id, because two rigs
// address their own humans.
func notifyKey(row HumanRow) string {
	return row.Rig + "/" + row.ID
}

// readRigTool runs one `rig` subcommand through the clientcmd.Rig contract when the dispatcher resolved
// the rig tool, and through the Runner when a caller supplied one instead (the tests, and any
// environment where rig is reached another way) - the same two paths readRig and readEdges take.
func (b *Bridge) readRigTool(rig *clientcmd.Rig, args ...string) (string, error) {
	if rig != nil {
		return rig.Run(context.Background(), args...)
	}
	return b.Runner(context.Background(), append([]string{"rig"}, args...)...)
}

// humanRigs lists the rig names this machine can see, sorted so a cycle's work is deterministic.
func (b *Bridge) humanRigs(rig *clientcmd.Rig) []string {
	output, err := b.readRigTool(rig, "ps", "--json")
	if err != nil {
		b.note("notify-rigs", fmt.Sprintf("rig unavailable: %v", err))
		return nil
	}
	items, err := ParseRigNames(output)
	if err != nil {
		b.note("notify-rigs", "rig ps was not readable")
		return nil
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		if name := str(dict(item)["rigName"]); name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// humanRows reads the rows addressed to one rig's human. The listing is the daemon's default view, so
// closed rows are not notified: a row the human has already answered is not news.
func (b *Bridge) humanRows(rig *clientcmd.Rig, rigName string) []HumanRow {
	destination := HumanDestination(rigName)
	output, err := b.readRigTool(rig, "queue", "list", "--destination", destination, "--json",
		"--limit", strconv.Itoa(notifyRowsPerRig))
	if err != nil {
		b.note("notify-queue "+destination, fmt.Sprintf("rig unavailable: %v", err))
		return nil
	}
	items, err := ParseHumanRows(output)
	if err != nil {
		b.note("notify-queue "+destination, "the queue listing was not readable")
		return nil
	}
	rows := make([]HumanRow, 0, len(items))
	for _, item := range items {
		row, ok := HumanRowFrom(item, rigName)
		if ok {
			rows = append(rows, row)
		}
	}
	return rows
}

// notifyHumans posts one frame for each human-addressed row that has not been posted yet.
//
// It NEVER fails the cycle. The report is this bridge's primary job, and a notification that could not
// go out is retried on the next cycle - an id is recorded only after the cloud accepted it, so a
// refused or unreachable post is not silently dropped and not silently counted as delivered.
func (b *Bridge) notifyHumans(rig *clientcmd.Rig) {
	if b.Notify == nil {
		return
	}
	posted, changed := 0, false
	for _, rigName := range b.humanRigs(rig) {
		if posted >= notifyPostsPerCycle {
			break
		}
		for _, row := range b.humanRows(rig, rigName) {
			if posted >= notifyPostsPerCycle {
				break
			}
			key := notifyKey(row)
			if _, sent := b.notified[key]; sent {
				continue
			}
			body, ok := NotificationBody(row)
			if !ok {
				b.note("notify-text:"+key, "the row carries no text to notify with yet")
				continue
			}
			status, answer, err := b.Notify(body)
			switch {
			case err != nil:
				b.note("notify", fmt.Sprintf("the notification transport did not answer: %v", err))
			case status < 200 || status > 299:
				b.note("notify:"+key, fmt.Sprintf(
					"the cloud refused the notification with HTTP %d: %s", status, TrimForReport(answer)))
			default:
				b.notified[key] = b.clock().UTC().Format(time.RFC3339)
				posted++
				changed = true
			}
		}
	}
	if changed {
		pruneNotified(b.notified, notifyStateLimit)
		WriteSyncState(b.StatePath, b.MachineID, b.state, b.notified, b.Note)
	}
}

// pruneNotified keeps the newest notifyStateLimit entries. The values are RFC3339 stamps, so the
// newest are the largest strings, and dropping the rest is what bounds the state file.
func pruneNotified(sent map[string]string, limit int) {
	if len(sent) <= limit {
		return
	}
	keys := make([]string, 0, len(sent))
	for key := range sent {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if sent[keys[i]] != sent[keys[j]] {
			return sent[keys[i]] > sent[keys[j]]
		}
		return keys[i] < keys[j]
	})
	for _, key := range keys[limit:] {
		delete(sent, key)
	}
}
