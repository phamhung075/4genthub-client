package clientseat

import (
	"fmt"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/commpolicy"
)

// THE DECISION ATTACHES TO THE CAPABILITY, not to a checker a seat must remember to run. The architect ruled
// the shape (qitem-20261015050158-c8120a7112fb, 2026-10-15) on the measured hole: commpolicy.AuditRecord was
// written only by `seatcheck send`, whose delivery is a different process's, so an allowed decision line with
// no outcome line after it left the outcome unknown. One path now owns both halves - this one builds the
// sending seat's policy at send time, decides, writes the decision line, delivers, and writes the outcome line.
//
// THE SOURCE IS THE ROOM'S DECLARED LINKS AS THE RUNTIME SERVES THEM, read at send time and never cached: a
// cached source is a stale source.
//
// MEASURED 2026-10-15, BEFORE WIRING: for 4genthub-min that source was EMPTY THEN. The runtime's `spec.json`
// declared no links, the cloud's room record carried none that any client route we had probed could read, and
// the room definition had declared none in any revision. A fail-closed gate over an empty source refuses the
// room's own traffic - which is why the SOURCE ERROR and the DENIAL are two different messages here: a reader
// must be able to tell "this room declares no link for that pair" apart from "the source could not be read", or
// the gate is indistinguishable from a broken wire.
//
// MEASURED AGAIN THE SAME DAY, AFTER THE SOURCE EXISTED, so neither half above is read as today's state: the
// room definition declares its links (scripts/team/4genthub-min/team.json, 23 of them) and the surface does
// read them (GET /rooms/<room>/seats/<seat>/links answers 200 - the architect's measurement at 05:13Z, which
// corrected the "no route can read them" half). GetRoomLinks therefore answers a non-empty list for this room
// and this gate decides real traffic. Read the live room's spec.json - the file the runtime reads - before
// concluding either way, and read these links before declaring any pair licensed.
var (
	sendRoomLinks = func(socket, room string) ([]clientruntime.SeatLink, error) {
		return clientruntime.NewClient(socket).RoomLinks(room)
	}
	sendSessions = func(socket string) ([]clientruntime.SessionEntry, error) {
		return clientruntime.NewClient(socket).ListSessions("")
	}
	// The sender's identity comes from THIS machine's resolver, not from the daemon the message is aimed at:
	// a send through --socket reaches another daemon, whose record does not hold this shell's seat, and the
	// question "which seat is sending" is about this shell.
	sendIdentity = func() (clientruntime.WhoamiResponse, error) {
		return clientruntime.ResolveIdentity("")
	}
	sendStore = func() (string, error) { return clientenv.ResolveSeatStore("") }
	sendAudit = commpolicy.AppendAudit
)

// sendHandle is what a decided send carries to its delivery: where its two lines go, what was decided, and the
// policy hash both lines cite so a reader can pair them.
type sendHandle struct {
	path     string
	from     string
	to       string
	intent   commpolicy.Intent
	policy   string
	decision commpolicy.Decision
}

// outcome writes the line that follows an allowed decision: the same fields plus what happened to the
// delivery.
func (h sendHandle) outcome(outcome string) error {
	return sendAudit(h.path, commpolicy.AuditRecord{
		At: time.Now().UTC(), From: h.from, To: h.to, Intent: h.intent,
		Allowed: h.decision.Allowed, Reason: h.decision.Reason, PolicyHash: h.policy, Outcome: outcome,
	})
}

// senderSeat is the seat key and room of this shell, read from the client's own resolver.
func senderSeat() (string, string, error) {
	who, err := sendIdentity()
	if err != nil {
		return "", "", &clientcmd.CodedError{
			Code: clientcmd.ExitUnavailable,
			Message: fmt.Sprintf("this shell's own seat could not be resolved, and a send is decided by the "+
				"sender's own links: %v; nothing was sent", err),
		}
	}
	room, seat := who.Identity.RoomName, who.Identity.MemberId
	if room == "" || seat == "" {
		return "", "", &clientcmd.CodedError{
			Code:    clientcmd.ExitUnavailable,
			Message: "this shell's own seat has no room or seat key, and a send is decided by the sender's own links; nothing was sent",
		}
	}
	return room, seat, nil
}

// sendRoomLive is the set of seat keys the target room is RUNNING now, which is what 7.11 makes a send's
// liveness. It is a seam for the same reason sendSessions is: a case decides a non-live destination without
// a daemon.
var sendRoomLive = func(socket, room string) (commpolicy.LiveSet, error) {
	seats, err := clientruntime.NewClient(socket).GetRoomStatus(room)
	if err != nil {
		return nil, err
	}
	live := make(commpolicy.LiveSet, len(seats))
	for _, seat := range seats {
		live[seat.Seat] = strings.EqualFold(strings.TrimSpace(seat.State), "running")
	}
	return live, nil
}

// recipientSeat maps the session name a caller typed to the seat key the policy speaks in, by reading the
// SAME daemon the delivery goes to. A session no record lists answers false, and nothing is guessed in its
// place: the room's links name seat keys, so a session that cannot be read back to one cannot be authorized.
func recipientSeat(socket, session string) (room, seat string, ok bool) {
	entries, err := sendSessions(socket)
	if err != nil {
		return "", "", false
	}
	for _, entry := range entries {
		if entry.SessionName != session && entry.CanonicalSessionName != session {
			continue
		}
		if entry.RoomName == "" {
			return "", "", false
		}
		// A logical id is `<pod>.<member>`, and the pod is not always the room's name, so the member is the
		// part after the FIRST dot - the rule cmd/seatcheck's identity view already applies.
		member := entry.LogicalID
		if _, after, found := strings.Cut(entry.LogicalID, "."); found {
			member = after
		}
		if member == "" {
			return "", "", false
		}
		return entry.RoomName, member, true
	}
	return "", "", false
}

// authorizeSend decides one send and writes its decision line, and returns the handle its delivery needs.
//
// senderRoom is the SENDER's room and targetRoom the recipient's, and they are two parameters on purpose: the
// links come from the room the message is addressed INTO, while the audit lines belong to the seat that made
// the decision - one seat's file holds every send it decided, wherever it sent. The lab measured the
// difference: a send from this seat into another room wrote its lines under THAT room's directory.
//
// Every refusal is a CodedError so the verb reports it through one path. The three reasons are kept apart on
// purpose: an unreadable source is not a policy decision, a policy decision is not an audit failure, and the
// failing audit is the one that must stop a send that was otherwise allowed.
func authorizeSend(socket, targetRoom, senderRoom, from, to string, intent commpolicy.Intent) (sendHandle, error) {
	links, err := sendRoomLinks(socket, targetRoom)
	if err != nil {
		return sendHandle{}, &clientcmd.CodedError{
			Code: clientcmd.ExitUnavailable,
			Message: fmt.Sprintf("room %s's declared links could not be read from the runtime at %s: %v - a send "+
				"is decided by that source, so nothing was sent", targetRoom, socket, err),
		}
	}
	policy := commpolicy.Policy{Seat: from, Links: make([]commpolicy.Link, 0, len(links))}
	for _, link := range links {
		policy.Links = append(policy.Links, commpolicy.Link{
			From: link.From, To: link.To, Kind: commpolicy.LinkKind(link.Kind), Allow: true,
		})
	}
	// 7.11: the destination must be LIVE as well as allowed. A seat the room no longer runs can hold a link
	// in the team definition, and words addressed to it reach no reader while the sender waits for a closure
	// that cannot come. The liveness is the runtime's own room status - the same reading `reseat` and the
	// peak gate use - and a read that FAILS refuses: a send decided on a guessed live set is a send decided
	// on nothing.
	live, err := sendRoomLive(socket, targetRoom)
	if err != nil {
		return sendHandle{}, &clientcmd.CodedError{
			Code: clientcmd.ExitUnavailable,
			Message: fmt.Sprintf("room %s's seats could not be read from the runtime at %s: %v - a send is "+
				"delivered to a seat that is running, so nothing was sent", targetRoom, socket, err),
		}
	}
	decision := commpolicy.DecideLive(policy, to, intent, live)
	hash := commpolicy.PolicyHash(policy)

	store, err := sendStore()
	if err != nil {
		return sendHandle{}, &clientcmd.CodedError{
			Code:    clientcmd.ExitUnavailable,
			Message: fmt.Sprintf("no seat store to audit the decision in: %v; nothing was sent", err),
		}
	}
	path := commpolicy.AuditPath(store, senderRoom, from)
	if err := commpolicy.EnsureAuditDir(path); err != nil {
		return sendHandle{}, &clientcmd.CodedError{
			Code:    clientcmd.ExitUnavailable,
			Message: fmt.Sprintf("the seat's audit directory could not be prepared at %s: %v; nothing was sent", path, err),
		}
	}
	handle := sendHandle{path: path, from: from, to: to, intent: intent, policy: hash, decision: decision}
	if err := sendAudit(path, commpolicy.AuditRecord{
		At: time.Now().UTC(), From: from, To: to, Intent: intent,
		Allowed: decision.Allowed, Reason: decision.Reason, PolicyHash: hash,
	}); err != nil {
		return sendHandle{}, &clientcmd.CodedError{
			Code:    clientcmd.ExitUnavailable,
			Message: fmt.Sprintf("the decision could not be audited in %s: %v; nothing was sent", path, err),
		}
	}
	if !decision.Allowed {
		message := fmt.Sprintf("refused by %s's communication policy: %s may not send a %s to %s (%s)",
			targetRoom, from, intent, to, decision.Reason)
		if hint := licenseHint(decision, links, from, to); hint != "" {
			message += "; " + hint
		}
		return sendHandle{}, &clientcmd.CodedError{
			Code:    clientcmd.ExitUsage,
			Message: message + ". The room's own declared links are what license a send; nothing was sent",
		}
	}
	return handle, nil
}

// licenseHint says what a refusal leaves the sender to change, in the two halves a reader needs: the kinds
// that license the intent that was sent, and what this pair's declaration licenses instead. The refusal
// that named neither read as "declare a link", and the room followed it - `reviewer -> go-dev` and
// `reviewer -> fe-dev` were declared `delegates_to` while every verdict row the reviewer sends carries no
// intent at all (the default notice), so the pair refused the traffic it exists to license and the message
// pointed at a declaration that was not the obstacle.
//
// Both halves come from commpolicy's own tables - the kinds for the intent out of the decision that refused
// the send, the intents for a declared kind out of LicensedIntents - so the hint cannot drift from the rule
// that refused it. Empty means there is nothing to add: a self message, an unknown intent and an explicit
// deny are all decided with no would-license kinds, and each of those already names its own cause.
func licenseHint(decision commpolicy.Decision, links []clientruntime.SeatLink, from, to string) string {
	if len(decision.WouldLicense) == 0 {
		return ""
	}
	kinds := make([]string, 0, len(decision.WouldLicense))
	for _, kind := range decision.WouldLicense {
		kinds = append(kinds, string(kind))
	}
	declared := make([]string, 0, len(links))
	seen := make(map[commpolicy.LinkKind]bool, len(links))
	for _, link := range links {
		if link.From != from || link.To != to {
			continue
		}
		kind := commpolicy.LinkKind(link.Kind)
		if seen[kind] {
			continue
		}
		seen[kind] = true
		if intents := commpolicy.LicensedIntents(kind); len(intents) > 0 {
			declared = append(declared, fmt.Sprintf("%s (%s)", joinIntents(intents), kind))
		} else {
			declared = append(declared, fmt.Sprintf("%s, which licenses no send", kind))
		}
	}
	if len(declared) == 0 {
		return fmt.Sprintf("a %s link would license it, and %s declares no link to %s",
			strings.Join(kinds, " or "), from, to)
	}
	return fmt.Sprintf("a %s link would license it, and %s -> %s declares %s",
		strings.Join(kinds, " or "), from, to, strings.Join(declared, " and "))
}

// joinIntents renders an intent list for a message: "task", "escalation or report".
func joinIntents(intents []commpolicy.Intent) string {
	out := make([]string, 0, len(intents))
	for _, intent := range intents {
		out = append(out, string(intent))
	}
	return strings.Join(out, " or ")
}

// authorizeToSeat authorizes a send whose recipient the caller NAMED as a seat key.
func authorizeToSeat(socket, room, seat string, intent commpolicy.Intent) (sendHandle, error) {
	senderRoom, from, err := senderSeat()
	if err != nil {
		return sendHandle{}, err
	}
	return authorizeSend(socket, room, senderRoom, from, seat, intent)
}
