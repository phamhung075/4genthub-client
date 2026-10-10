// Package commpolicy answers whether one seat may send a message to another.
// Default is deny: a send is allowed only when an explicit link permits it.
package commpolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type LinkKind string

// Edge kinds accepted by OpenRig. Only delegates_to and spawned_by affect
// launch order, and edges never route messages.
const (
	KindDelegatesTo      LinkKind = "delegates_to"
	KindSpawnedBy        LinkKind = "spawned_by"
	KindCanObserve       LinkKind = "can_observe"
	KindCollaboratesWith LinkKind = "collaborates_with"
	KindEscalatesTo      LinkKind = "escalates_to"
)

type Intent string

const (
	IntentTask       Intent = "task"
	IntentEscalation Intent = "escalation"
	IntentReport     Intent = "report"
	IntentQuestion   Intent = "question"
	IntentNotice     Intent = "notice"
)

type Link struct {
	From  string
	To    string
	Kind  LinkKind
	Allow bool
}

// Policy is the snapshot carried with one sending seat. Links whose From is
// not Seat are ignored by Decide.
type Policy struct {
	Seat  string
	Links []Link
}

type Decision struct {
	Allowed bool
	Reason  string
	Link    *Link
}

const (
	reasonSelfMessage   = "self message"
	reasonUnknownIntent = "unknown intent"
	reasonNoLink        = "no link"
	reasonExplicitDeny  = "explicit deny"
	reasonAllowed       = "allowed"
)

type AuditRecord struct {
	At         time.Time
	From       string
	To         string
	Intent     Intent
	Allowed    bool
	Reason     string
	PolicyHash string
	// Outcome is empty on the decision line, written before delivery, and "delivered" or
	// "delivery_failed" on the line that follows an allowed one. An allowed decision line with
	// no outcome line after it means the delivery outcome is unknown: seatcheck stopped (or could
	// not write the outcome) between starting the delivery and recording it, so the message may
	// have been delivered.
	Outcome string `json:",omitempty"`
}

// Outcomes of an allowed send.
const (
	OutcomeDelivered      = "delivered"
	OutcomeDeliveryFailed = "delivery_failed"
)

// allowedKinds maps a message intent to the link kinds that authorize it.
// spawned_by and can_observe never authorize a send; can_observe grants reading
// output via rig capture/transcript, not sending.
var allowedKinds = map[Intent][]LinkKind{
	IntentTask:       {KindDelegatesTo},
	IntentEscalation: {KindEscalatesTo},
	IntentReport:     {KindEscalatesTo},
	IntentQuestion:   {KindCollaboratesWith},
	IntentNotice:     {KindCollaboratesWith},
}

// ValidKind reports whether kind is one of the five OpenRig edge kinds.
func ValidKind(kind LinkKind) bool {
	switch kind {
	case KindDelegatesTo, KindSpawnedBy, KindCanObserve, KindCollaboratesWith, KindEscalatesTo:
		return true
	default:
		return false
	}
}

func kindAllowed(kinds []LinkKind, kind LinkKind) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// Decide resolves one send. An explicit deny on any allowed kind always beats an allow.
func Decide(policy Policy, to string, intent Intent) Decision {
	if to == policy.Seat {
		return Decision{Reason: reasonSelfMessage}
	}
	kinds, ok := allowedKinds[intent]
	if !ok {
		return Decision{Reason: reasonUnknownIntent}
	}

	var allow *Link
	for i := range policy.Links {
		link := &policy.Links[i]
		if link.From != policy.Seat || link.To != to || !kindAllowed(kinds, link.Kind) {
			continue
		}
		if !link.Allow {
			return Decision{Reason: reasonExplicitDeny, Link: link}
		}
		if allow == nil {
			allow = link
		}
	}
	if allow == nil {
		return Decision{Reason: reasonNoLink}
	}
	return Decision{Allowed: true, Reason: reasonAllowed, Link: allow}
}

// ParsePolicy decodes a policy strictly: unknown fields, unknown link kinds,
// foreign links, duplicate triples and trailing data are all errors.
func ParsePolicy(data []byte) (Policy, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var policy Policy
	if err := dec.Decode(&policy); err != nil {
		return Policy{}, err
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Policy{}, errors.New("trailing data after policy")
	}

	if policy.Seat == "" {
		return Policy{}, errors.New("policy seat is empty")
	}

	type linkKey struct {
		from string
		to   string
		kind LinkKind
	}
	seen := make(map[linkKey]bool, len(policy.Links))
	for _, link := range policy.Links {
		if !ValidKind(link.Kind) {
			return Policy{}, fmt.Errorf("unknown link kind %q", link.Kind)
		}
		if link.From != policy.Seat {
			return Policy{}, fmt.Errorf("link from %q does not match seat %q", link.From, policy.Seat)
		}
		key := linkKey{from: link.From, to: link.To, kind: link.Kind}
		if seen[key] {
			return Policy{}, fmt.Errorf("duplicate link %s -> %s (%s)", link.From, link.To, link.Kind)
		}
		seen[key] = true
	}
	return policy, nil
}

// PolicyHash hashes a canonical encoding of the policy: identical policies hash
// the same regardless of link order.
func PolicyHash(policy Policy) string {
	links := append([]Link(nil), policy.Links...)
	sort.Slice(links, func(i, j int) bool {
		if links[i].To != links[j].To {
			return links[i].To < links[j].To
		}
		if links[i].Kind != links[j].Kind {
			return links[i].Kind < links[j].Kind
		}
		if links[i].From != links[j].From {
			return links[i].From < links[j].From
		}
		return !links[i].Allow && links[j].Allow
	})

	h := sha256.New()
	writeField(h, policy.Seat)
	writeUint(h, uint64(len(links)))
	for _, link := range links {
		writeField(h, link.From)
		writeField(h, link.To)
		writeField(h, string(link.Kind))
		if link.Allow {
			writeUint(h, 1)
		} else {
			writeUint(h, 0)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeField(h hash.Hash, s string) {
	writeUint(h, uint64(len(s)))
	h.Write([]byte(s))
}

func writeUint(h hash.Hash, v uint64) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], v)
	h.Write(n[:])
}

// BypassSuspected reports whether an observed command is a direct send that
// skipped the checker. known lists wrapper commands that are never flagged.
func BypassSuspected(known []string, observed string) bool {
	fields := strings.Fields(observed)
	i := 0
	for i < len(fields) && isEnvAssignment(fields[i]) {
		i++
	}
	if i+1 >= len(fields) {
		return false
	}

	cmd := filepath.Base(fields[i])
	for _, name := range known {
		if name == cmd {
			return false
		}
	}

	sub := fields[i+1]
	switch cmd {
	case "rig":
		return sub == "send" || sub == "queue" || sub == "broadcast"
	case "tmux":
		return sub == "send-keys" || sub == "paste-buffer"
	default:
		return false
	}
}

func isEnvAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	for i, r := range tok[:eq] {
		switch {
		case r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
