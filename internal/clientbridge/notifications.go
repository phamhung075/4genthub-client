package clientbridge

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/secretscan"
)

// D3 - THE PRODUCER HALF OF AGENT-TO-HUMAN NOTIFICATIONS.
//
// The cloud already knows how to carry an agent-to-human message: the dashboard stores and toasts a
// frame whose payload.entity is 'notification', and the server replays one from the
// missed-notification store on reconnect. What did not exist is a producer. This file is it: the
// bridge reads the room orchestrator rows ADDRESSED TO A HUMAN and posts one notification frame per row.
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
	// humanDestinationPrefix addresses one room's human. The daemon's own destination form is
	// "human@<room name>" - measured against a live store, which carries one such destination per room,
	// each naming the room it belongs to.
	humanDestinationPrefix = "human@"
	// notifyKind is the free-form kind the payload documents, for example 'notice'.
	notifyKind = "notice"
	// notifyMessageLimit bounds the toast text: a queue body can be a paragraph, and the frame is
	// read by a human at a glance.
	notifyMessageLimit = 300
	// notifyRowsPerRoom bounds one read, so a room with a long human queue cannot make a cycle unbounded.
	notifyRowsPerRoom = 100
	// notifyPostsPerCycle bounds one cycle's posts. A first run against a room holding dozens of human
	// rows walks them over several cycles instead of posting all of them at once.
	notifyPostsPerCycle = 20
	// notifyStateLimit bounds the sent-id record, so a bridge running for months cannot grow its own
	// state file without end.
	notifyStateLimit = 500
)

// HumanRow is one the room orchestrator queue row addressed to a human, reduced to what a notification needs.
type HumanRow struct {
	ID       string
	Room     string
	Source   string
	State    string
	Summary  string
	BodyText string
}

// HumanDestination is the queue destination that addresses one room's human.
func HumanDestination(roomName string) string {
	return humanDestinationPrefix + roomName
}

// parseItems unwraps a JSON listing: a list, or an object carrying `items`. Both shapes are what the
// room tool prints, and one helper keeps the two readers from disagreeing about it.
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
func HumanRowFrom(item any, roomName string) (HumanRow, bool) {
	row := dict(item)
	id := str(row["qitemId"])
	destination := str(row["destinationSession"])
	if id == "" || !strings.HasPrefix(destination, humanDestinationPrefix) {
		return HumanRow{}, false
	}
	return HumanRow{
		ID:       id,
		Room:     roomName,
		Source:   str(row["sourceSession"]),
		State:    str(row["state"]),
		Summary:  str(row["summary"]),
		BodyText: str(row["body"]),
	}, true
}

// NotificationBody builds the frame for one human row. It returns false when the row carries no text
// to show: the consumer drops a frame with an empty message, so posting one would be a silent no-op.
//
// known is the bridge's secretscan.KnownValuesFromEnv map, and it is here because THIS PATH IS AN
// EGRESS: the report redacts its seat detail with the same map (payload.go, secretscan.Redact), and the
// notification was the one path that copied the row's text into the cloud's notification store raw.
func NotificationBody(row HumanRow, known map[string]string) ([]byte, bool) {
	text := notifyText(row, known)
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
	if row.Room != "" {
		data["room"] = row.Room
	}
	body, err := json.Marshal(frame)
	if err != nil {
		return nil, false
	}
	return body, true
}

// notifyText is what the human reads: the row's summary when it has one, otherwise the first non-empty
// line of its body, REDACTED, and only then bounded to notifyMessageLimit.
//
// The order is load-bearing. Redact replaces a known value with a marker that names the variable, so a
// bound applied first could cut a secret in half and leave the half it did not replace; applying it
// after the redaction means the cut can only ever land on already-clean text. Redact fails closed - it
// answers an empty text when it cannot finish - so a scanner failure posts nothing and the row is
// announced once it has text again, rather than being posted raw.
func notifyText(row HumanRow, known map[string]string) string {
	text := strings.TrimSpace(row.Summary)
	if text == "" {
		for _, line := range strings.Split(row.BodyText, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				text = line
				break
			}
		}
	}
	text, _ = secretscan.Redact(text, known)
	if len(text) > notifyMessageLimit {
		text = strings.TrimRight(text[:notifyMessageLimit], " ") + "..."
	}
	return text
}

// notifyKey is the row's identity in the sent-id record: the room plus the row id, because two rooms
// address their own humans.
func notifyKey(row HumanRow) string {
	return row.Room + "/" + row.ID
}

// humanRooms lists the rooms this machine's runtime hosts, sorted so a cycle's work is deterministic.
func (b *Bridge) humanRooms() []string {
	rooms, err := b.Rooms()
	if err != nil {
		b.note("notify-rooms", fmt.Sprintf("runtime unavailable: %v", err))
		return nil
	}
	sort.Strings(rooms)
	return rooms
}

// humanRows reads the rows addressed to one room's human. The listing is the rail's active rows, so closed
// rows are not notified: a row the human has already answered is not news.
func (b *Bridge) humanRows(room string) []HumanRow {
	destination := HumanDestination(room)
	items, err := b.Queue(room)
	if err != nil {
		b.note("notify-queue "+destination, fmt.Sprintf("runtime unavailable: %v", err))
		return nil
	}
	rows := make([]HumanRow, 0, len(items))
	for _, item := range items {
		row, ok := HumanRowFrom(item, room)
		if ok {
			rows = append(rows, row)
		}
	}
	return rows
}

// runtimeRooms lists the rooms the runtime hosts.
func runtimeRooms() ([]string, error) {
	client := clientruntime.NewClient("")
	if !client.Reachable() {
		return nil, errRuntimeAbsent
	}
	return client.ListAllRooms()
}

// runtimeHumanQueue reads the active rows of one room's rail addressed to its human, in the item shape
// HumanRowFrom reads.
func runtimeHumanQueue(room string) ([]any, error) {
	client := clientruntime.NewClient("")
	if !client.Reachable() {
		return nil, errRuntimeAbsent
	}
	destination, limit := HumanDestination(room), int64(notifyRowsPerRoom)
	rows, err := client.QueueList(room, clientruntime.QueueListFilter{
		DestinationSession: &destination, ActiveOnly: true, Limit: &limit,
	})
	if err != nil {
		return nil, err
	}
	items := make([]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, map[string]any{
			"qitemId": row.QitemID, "state": row.State, "summary": row.Summary, "body": row.Body,
			"sourceSession": row.SourceSession, "destinationSession": row.DestinationSession,
		})
	}
	return items, nil
}

// notifyHumans posts one frame for each human-addressed row that has not been posted yet.
//
// It NEVER fails the cycle. The report is this bridge's primary job, and a notification that could not
// go out is retried on the next cycle: an id is recorded only after the cloud ACCEPTED the frame, or
// after it refused the frame in a way no further attempt can change (notifyTerminal) - so a transport
// failure or a 5xx is neither silently dropped nor counted as delivered, while a row the cloud would
// refuse identically forever is not posted again on every cycle. The record is therefore "handled",
// not strictly "delivered", and the note beside a terminal refusal carries the status and the answer.
func (b *Bridge) notifyHumans() {
	if b.Notify == nil {
		return
	}
	posted, changed := 0, false
	for _, roomName := range b.humanRooms() {
		if posted >= notifyPostsPerCycle {
			break
		}
		for _, row := range b.humanRows(roomName) {
			if posted >= notifyPostsPerCycle {
				break
			}
			key := notifyKey(row)
			if _, sent := b.notified[key]; sent {
				continue
			}
			body, ok := NotificationBody(row, b.Known)
			if !ok {
				b.note("notify-text:"+key, "the row carries no text to notify with yet")
				continue
			}
			status, answer, err := b.Notify(body)
			switch {
			case err != nil:
				b.note("notify", fmt.Sprintf("the notification transport did not answer: %v", err))
			case notifyTerminal(status):
				// The cloud will refuse THIS row identically on every cycle, so retrying it is not a
				// retry: it is the same 4xx posted forever, once per cycle, into the record and into
				// the cloud's log. The id is recorded like a delivered one so the sweep moves on, and
				// the note carries the answer, which is what a reader needs to see what was dropped.
				b.notified[key] = b.clock().UTC().Format(time.RFC3339)
				changed = true
				b.note("notify:"+key, fmt.Sprintf(
					"the cloud refused the notification with HTTP %d and would refuse it again, so it is not retried: %s",
					status, TrimForReport(answer)))
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

// notifyTerminal reports whether an answer means THIS request will never be accepted: a 4xx other than
// the ones that ask for another attempt.
//
// The three exceptions are the point of the rule. 408 (the request timed out) and 429 (too many
// requests) are the cloud asking for the same request again, and 401/403 name a CREDENTIAL problem:
// that is the bridge's own token, the report path fails loudly on it, and a human's row must not be
// thrown away in the meantime because the token was wrong for an hour - the row is delivered once the
// token is fixed. Everything else in the 4xx range names the REQUEST, and this request will not change:
// the same post from the same row would earn the same answer on every cycle.
func notifyTerminal(status int) bool {
	if status < 400 || status > 499 {
		return false
	}
	switch status {
	case 401, 403, 408, 429:
		return false
	}
	return true
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
