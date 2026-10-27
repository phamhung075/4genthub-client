package clientbridge

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientdecisions"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/secretscan"
)

// THE OWNER-DECISION CROSSING: the bridge's half of the hub's room decision channel
// (agenthub_go/fastmcp/server/httpapp/room_decisions_mount.go).
//
// An owner ask is a rail row tagged owner-decision, addressed to human@<room>, whose questions
// `room queue create --questions-file` recorded beside the room (internal/clientdecisions). Each cycle,
// for every room holding such a record, the bridge:
//
//  1. mirrors an ask the hub has not seen: POST /api/v2/decisions. The hub keys the ask on its qitem and
//     answers the row it already holds on a repeat, so a retry never asks the owner twice;
//  2. pulls the owner's answers, at most once per decisionPullEvery and only while the room has an ask
//     open on the hub: GET /api/v2/decisions/answers?room=<room>. A room with no open ask costs no request;
//  3. applies each answer to its row: closes it as human@<room>, handed_off_to the asking seat with the
//     chosen labels as the note, so the seat is woken by a pending row carrying the answer; records
//     Applied; and acks it `applied`, which is what resolves the hub's row;
//  4. tells the hub when a row closed another way (the owner answered on the rail): POST
//     /api/v2/decisions/{id}/state superseded, so the panel stops offering it.
//
// EXACTLY ONCE: the close happens only on an active row, the runtime refuses a second close of a row and a
// second handoff successor, and Applied is recorded before the ack, so a bridge that dies between the two
// acks on its next cycle instead of closing again. The hub hands an answer out until it is acked, so an
// answer that could not be applied this cycle is retried on the next pull.

const (
	decisionsPath       = "/api/v2/decisions"
	decisionAnswersPath = "/api/v2/decisions/answers"
	// decisionPullEvery bounds how often one room's answers are asked for while it has an ask open.
	decisionPullEvery = time.Minute
)

// Hub calls one hub route with the bridge's credential and returns the status and the bounded body. A
// transport that never answered is an error, never a status (the Sender rule).
type Hub func(method, path string, body []byte) (int, []byte, error)

// decisionAnswer is one pulled answer, the hub's six keys.
type decisionAnswer struct {
	DecisionID string            `json:"decision_id"`
	QitemID    string            `json:"qitem_id"`
	Room       string            `json:"room"`
	Answers    map[string]string `json:"answers"`
}

// syncDecisions runs the crossing for every hosted room. Like the notification sweep it never fails the
// cycle: each failure is noted and retried on a later cycle.
func (b *Bridge) syncDecisions() {
	if b.Hub == nil {
		return
	}
	for _, room := range b.humanRooms() {
		b.syncRoomDecisions(room)
	}
}

func (b *Bridge) syncRoomDecisions(room string) {
	records, err := clientdecisions.List(b.Root, room)
	if err != nil {
		b.note("decisions "+room, fmt.Sprintf("cannot read the owner asks: %v", err))
		return
	}
	open := map[string]clientdecisions.Record{}
	rows := map[string]*clientruntime.QueueRow{}
	for _, record := range records {
		row, err := b.Show(room, record.QitemID)
		if err != nil {
			b.note("decision-row "+room+"/"+record.QitemID, fmt.Sprintf("cannot read owner ask %s: %v", record.QitemID, err))
			continue
		}
		active := !queueStateTerminal(row.State)
		switch {
		case record.Applied:
			b.ackDecision(room, record, "applied", "")
		case record.DecisionID == "":
			if !active {
				b.forget(room, record)
			} else if record.Refused == "" {
				b.askDecision(room, record, row)
			}
		case !active:
			b.supersedeDecision(room, record)
		default:
			open[record.DecisionID] = record
			rows[record.DecisionID] = row
		}
	}
	if len(open) == 0 {
		return
	}
	now := b.clock()
	if last, ok := b.pulled[room]; ok && now.Sub(last) < decisionPullEvery {
		return
	}
	b.pulled[room] = now
	for _, answer := range b.pullAnswers(room) {
		record, ok := open[answer.DecisionID]
		if !ok || record.QitemID != answer.QitemID {
			b.ackDecision(room, clientdecisions.Record{Room: room, DecisionID: answer.DecisionID}, "dropped", "unknown-qitem")
			continue
		}
		b.applyAnswer(room, record, rows[answer.DecisionID], answer)
	}
}

// askDecision mirrors one ask to the hub and records the id it was given.
func (b *Bridge) askDecision(room string, record clientdecisions.Record, row *clientruntime.QueueRow) {
	summary, _ := secretscan.Redact(row.Summary, b.Known)
	detail, _ := secretscan.Redact(row.Body, b.Known)
	body, err := json.Marshal(map[string]any{
		"room":            room,
		"qitem_id":        record.QitemID,
		"author_seat_key": row.SourceSession,
		"destination":     clientdecisions.Human(room),
		"summary":         summary,
		"detail":          detail,
		"questions":       record.Questions,
		"idempotency_key": "agenthub:decision:" + record.QitemID,
	})
	if err != nil {
		return
	}
	key := "decision-ask " + room + "/" + record.QitemID
	status, answer, err := b.Hub("POST", decisionsPath, body)
	switch {
	case err != nil:
		b.note(key, fmt.Sprintf("owner ask %s not mirrored, the hub did not answer: %v", record.QitemID, err))
	case status >= 200 && status <= 299:
		var accepted struct {
			Decision struct {
				ID string `json:"id"`
			} `json:"decision"`
		}
		if json.Unmarshal(answer, &accepted) != nil || accepted.Decision.ID == "" {
			b.note(key, fmt.Sprintf("owner ask %s: the hub accepted it without a decision id: %s", record.QitemID, TrimForReport(answer)))
			return
		}
		record.DecisionID = accepted.Decision.ID
		b.store(room, record)
	case notifyTerminal(status):
		record.Refused = fmt.Sprintf("HTTP %d: %s", status, TrimForReport(answer))
		b.store(room, record)
		b.note(key, fmt.Sprintf("the hub refused owner ask %s and would refuse it again, so it stays a notification only: %s", record.QitemID, record.Refused))
	default:
		b.note(key, fmt.Sprintf("owner ask %s not mirrored, the hub answered HTTP %d: %s", record.QitemID, status, TrimForReport(answer)))
	}
}

// pullAnswers reads the room's answers the hub has not had acked.
func (b *Bridge) pullAnswers(room string) []decisionAnswer {
	key := "decision-pull " + room
	status, answer, err := b.Hub("GET", decisionAnswersPath+"?room="+url.QueryEscape(room), nil)
	if err != nil {
		b.note(key, fmt.Sprintf("cannot pull the owner's answers for %s: %v", room, err))
		return nil
	}
	if status < 200 || status > 299 {
		b.note(key, fmt.Sprintf("cannot pull the owner's answers for %s: HTTP %d: %s", room, status, TrimForReport(answer)))
		return nil
	}
	var page struct {
		Answers []decisionAnswer `json:"answers"`
	}
	if err := json.Unmarshal(answer, &page); err != nil {
		b.note(key, fmt.Sprintf("cannot read the owner's answers for %s: %v", room, err))
		return nil
	}
	b.note(key, "ok")
	return page.Answers
}

// applyAnswer closes the ask as the room's human, handing the answer to the asking seat, then acks it.
func (b *Bridge) applyAnswer(room string, record clientdecisions.Record, row *clientruntime.QueueRow, answer decisionAnswer) {
	target := row.SourceSession
	err := b.Close(room, clientruntime.QueueCloseInput{
		QitemID:        record.QitemID,
		ActorSession:   clientdecisions.Human(room),
		ClosureReason:  "handed_off_to",
		ClosureTarget:  &target,
		TransitionNote: clientdecisions.AnswerNote(record.Questions, answer.Answers),
	})
	if err != nil {
		b.note("decision-apply "+room+"/"+record.QitemID, fmt.Sprintf("the owner's answer to %s was not applied, retried next pull: %v", record.QitemID, err))
		return
	}
	record.Applied = true
	b.store(room, record)
	b.note("decision-apply "+room+"/"+record.QitemID, fmt.Sprintf("the owner's answer to %s was handed to %s", record.QitemID, target))
	b.ackDecision(room, record, "applied", "")
}

// ackDecision reports what became of one pulled answer. The record is removed once the hub holds the
// outcome, or already held it (409: not awaiting an ack).
func (b *Bridge) ackDecision(room string, record clientdecisions.Record, outcome, reason string) {
	payload := map[string]string{"machine_id": b.MachineID, "outcome": outcome}
	if reason != "" {
		payload["reason"] = reason
	}
	b.closeOnHub(room, record, "/ack", payload)
}

// supersedeDecision tells the hub an ask was closed on the rail without its answer.
func (b *Bridge) supersedeDecision(room string, record clientdecisions.Record) {
	b.closeOnHub(room, record, "/state", map[string]string{
		"machine_id": b.MachineID, "state": "superseded", "reason": "closed-on-the-rail",
	})
}

func (b *Bridge) closeOnHub(room string, record clientdecisions.Record, leg string, payload map[string]string) {
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	key := "decision" + leg + " " + room + "/" + record.DecisionID
	status, answer, err := b.Hub("POST", decisionsPath+"/"+url.PathEscape(record.DecisionID)+leg, body)
	switch {
	case err != nil:
		b.note(key, fmt.Sprintf("decision %s: the hub did not answer, retried next cycle: %v", record.DecisionID, err))
	case status >= 200 && status <= 299, status == 404, status == 409:
		// 2xx: recorded. 404/409: the hub has no open row for it any more - already acked, already
		// closed, or never there - so there is nothing left to tell it.
		if record.QitemID != "" {
			b.forget(room, record)
		}
	default:
		b.note(key, fmt.Sprintf("decision %s: the hub answered HTTP %d, retried next cycle: %s", record.DecisionID, status, TrimForReport(answer)))
	}
}

func (b *Bridge) store(room string, record clientdecisions.Record) {
	if err := clientdecisions.Write(b.Root, record); err != nil {
		b.note("decision-record "+room+"/"+record.QitemID, fmt.Sprintf("cannot record owner ask %s: %v", record.QitemID, err))
	}
}

func (b *Bridge) forget(room string, record clientdecisions.Record) {
	if err := clientdecisions.Remove(b.Root, room, record.QitemID); err != nil {
		b.note("decision-record "+room+"/"+record.QitemID, fmt.Sprintf("cannot remove owner ask %s: %v", record.QitemID, err))
	}
}

func queueStateTerminal(state string) bool {
	for _, terminal := range clientruntime.QueueTerminalStates {
		if strings.EqualFold(state, terminal) {
			return true
		}
	}
	return false
}

// runtimeShow and runtimeClose are the rail seams' defaults: the runtime's own IPC.
func runtimeShow(room, qitem string) (*clientruntime.QueueRow, error) {
	client := clientruntime.NewClient("")
	if !client.Reachable() {
		return nil, errRuntimeAbsent
	}
	return client.QueueShow(room, qitem)
}

func runtimeClose(room string, in clientruntime.QueueCloseInput) error {
	client := clientruntime.NewClient("")
	if !client.Reachable() {
		return errRuntimeAbsent
	}
	_, err := client.QueueClose(room, in)
	return err
}
