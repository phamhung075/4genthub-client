package commpolicy

import (
	"slices"
	"strings"
	"testing"
)

func TestDecide(t *testing.T) {
	taskAllow := Link{From: "a", To: "b", Kind: KindDelegatesTo, Allow: true}
	taskDeny := Link{From: "a", To: "b", Kind: KindDelegatesTo, Allow: false}

	cases := []struct {
		name        string
		policy      Policy
		to          string
		intent      Intent
		wantAllowed bool
		wantReason  string
		// wantLicense is what the refusal must name as the kind that would have licensed this intent.
		// Only the link-caused refusals carry it - none declared, or declared but licensing another
		// intent: the other causes are not a link, and naming a kind there would send the sender to a
		// declaration that is not the obstacle.
		wantLicense []LinkKind
	}{
		{
			name:        "default deny with no links",
			policy:      Policy{Seat: "a"},
			to:          "b",
			intent:      IntentTask,
			wantReason:  reasonNoLink,
			wantLicense: []LinkKind{KindDelegatesTo},
		},
		{
			name:        "allow delegates to",
			policy:      Policy{Seat: "a", Links: []Link{taskAllow}},
			to:          "b",
			intent:      IntentTask,
			wantAllowed: true,
			wantReason:  reasonAllowed,
		},
		{
			name:       "explicit deny",
			policy:     Policy{Seat: "a", Links: []Link{taskDeny}},
			to:         "b",
			intent:     IntentTask,
			wantReason: reasonExplicitDeny,
		},
		{
			name:       "explicit deny wins over allow before it",
			policy:     Policy{Seat: "a", Links: []Link{taskAllow, taskDeny}},
			to:         "b",
			intent:     IntentTask,
			wantReason: reasonExplicitDeny,
		},
		{
			name:       "explicit deny wins over allow after it",
			policy:     Policy{Seat: "a", Links: []Link{taskDeny, taskAllow}},
			to:         "b",
			intent:     IntentTask,
			wantReason: reasonExplicitDeny,
		},
		{
			name:        "wrong kind",
			policy:      Policy{Seat: "a", Links: []Link{{From: "a", To: "b", Kind: KindCollaboratesWith, Allow: true}}},
			to:          "b",
			intent:      IntentTask,
			wantReason:  reasonIntentNotLicensed,
			wantLicense: []LinkKind{KindDelegatesTo},
		},
		{
			// The measured case (review 3 line 5, 2026-10-18): a notice sent along a declared
			// escalates_to edge. The pair declares a link, so the refusal must not say `no link`.
			name:        "declared escalates_to does not license a notice",
			policy:      Policy{Seat: "a", Links: []Link{{From: "a", To: "b", Kind: KindEscalatesTo, Allow: true}}},
			to:          "b",
			intent:      IntentNotice,
			wantReason:  reasonIntentNotLicensed,
			wantLicense: []LinkKind{KindCollaboratesWith},
		},
		{
			name:        "other seat's links ignored",
			policy:      Policy{Seat: "a", Links: []Link{{From: "x", To: "b", Kind: KindDelegatesTo, Allow: true}}},
			to:          "b",
			intent:      IntentTask,
			wantReason:  reasonNoLink,
			wantLicense: []LinkKind{KindDelegatesTo},
		},
		{
			name:        "link to another recipient ignored",
			policy:      Policy{Seat: "a", Links: []Link{taskAllow}},
			to:          "c",
			intent:      IntentTask,
			wantReason:  reasonNoLink,
			wantLicense: []LinkKind{KindDelegatesTo},
		},
		{
			name:       "self message denied",
			policy:     Policy{Seat: "a", Links: []Link{{From: "a", To: "a", Kind: KindDelegatesTo, Allow: true}}},
			to:         "a",
			intent:     IntentTask,
			wantReason: reasonSelfMessage,
		},
		{
			name:       "unknown intent denied",
			policy:     Policy{Seat: "a", Links: []Link{taskAllow}},
			to:         "b",
			intent:     Intent("shout"),
			wantReason: reasonUnknownIntent,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.policy, tc.to, tc.intent)
			if got.Allowed != tc.wantAllowed {
				t.Fatalf("Allowed = %v, want %v", got.Allowed, tc.wantAllowed)
			}
			if got.Reason != tc.wantReason {
				t.Fatalf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if !slices.Equal(got.WouldLicense, tc.wantLicense) {
				t.Errorf("WouldLicense = %v, want %v", got.WouldLicense, tc.wantLicense)
			}
		})
	}
}

// TestLicensedIntents pins the inverse table, which is what a refusal says a pair's declaration DOES
// license. It is written as expectations rather than as a round trip through Decide: the two agree by
// construction, and what a reader needs pinned is the meaning, not the agreement.
func TestLicensedIntents(t *testing.T) {
	cases := []struct {
		kind LinkKind
		want []Intent
	}{
		{KindDelegatesTo, []Intent{IntentTask}},
		{KindEscalatesTo, []Intent{IntentEscalation, IntentReport}},
		{KindCollaboratesWith, []Intent{IntentQuestion, IntentNotice}},
		{KindSpawnedBy, nil},
		{KindCanObserve, nil},
		{LinkKind("invented"), nil},
	}
	for _, tc := range cases {
		got := LicensedIntents(tc.kind)
		if !slices.Equal(got, tc.want) {
			t.Errorf("LicensedIntents(%q) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

func TestDecideIntentKindMapping(t *testing.T) {
	rightKind := map[Intent]LinkKind{
		IntentTask:       KindDelegatesTo,
		IntentEscalation: KindEscalatesTo,
		IntentReport:     KindEscalatesTo,
		IntentQuestion:   KindCollaboratesWith,
		IntentNotice:     KindCollaboratesWith,
	}
	allKinds := []LinkKind{KindDelegatesTo, KindSpawnedBy, KindCanObserve, KindCollaboratesWith, KindEscalatesTo}
	for intent, want := range rightKind {
		for _, kind := range allKinds {
			policy := Policy{Seat: "a", Links: []Link{{From: "a", To: "b", Kind: kind, Allow: true}}}
			got := Decide(policy, "b", intent)
			if kind == want {
				if !got.Allowed || got.Reason != reasonAllowed {
					t.Errorf("intent %q with kind %q = %+v, want allowed", intent, kind, got)
				}
				continue
			}
			if got.Allowed || got.Reason != reasonIntentNotLicensed {
				t.Errorf("intent %q with kind %q = %+v, want the declaration refused as not licensing this intent", intent, kind, got)
			}
		}
	}
}

// TestDecideStructuralKindsNeverAllow pins that the two structural edges cannot
// authorize a send for any intent.
func TestDecideStructuralKindsNeverAllow(t *testing.T) {
	intents := []Intent{IntentTask, IntentEscalation, IntentReport, IntentQuestion, IntentNotice}
	for _, kind := range []LinkKind{KindSpawnedBy, KindCanObserve} {
		for _, intent := range intents {
			policy := Policy{Seat: "a", Links: []Link{{From: "a", To: "b", Kind: kind, Allow: true}}}
			if got := Decide(policy, "b", intent); got.Allowed {
				t.Errorf("intent %q authorized by structural kind %q: %+v", intent, kind, got)
			}
		}
	}
}

func TestValidKind(t *testing.T) {
	for _, kind := range []LinkKind{KindDelegatesTo, KindSpawnedBy, KindCanObserve, KindCollaboratesWith, KindEscalatesTo} {
		if !ValidKind(kind) {
			t.Errorf("ValidKind(%q) = false, want true", kind)
		}
	}
	for _, kind := range []LinkKind{"", "reports_to", "consults", "notifies", "shouts"} {
		if ValidKind(kind) {
			t.Errorf("ValidKind(%q) = true, want false", kind)
		}
	}
}

func TestDecideReturnsDecidingLink(t *testing.T) {
	allow := Link{From: "a", To: "b", Kind: KindCollaboratesWith, Allow: true}
	decision := Decide(Policy{Seat: "a", Links: []Link{allow}}, "b", IntentNotice)
	if decision.Link == nil || *decision.Link != allow {
		t.Fatalf("allowed decision link = %+v, want %+v", decision.Link, allow)
	}

	deny := Link{From: "a", To: "b", Kind: KindCollaboratesWith, Allow: false}
	decision = Decide(Policy{Seat: "a", Links: []Link{allow, deny}}, "b", IntentNotice)
	if decision.Link == nil || *decision.Link != deny {
		t.Fatalf("denied decision link = %+v, want %+v", decision.Link, deny)
	}

	if got := Decide(Policy{Seat: "a"}, "b", IntentNotice); got.Link != nil {
		t.Fatalf("no-link decision carried a link: %+v", got.Link)
	}
}

func TestParsePolicyValid(t *testing.T) {
	data := []byte(`{"Seat":"a","Links":[
		{"From":"a","To":"b","Kind":"delegates_to","Allow":true},
		{"From":"a","To":"c","Kind":"collaborates_with","Allow":false}
	]}`)
	policy, err := ParsePolicy(data)
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	if policy.Seat != "a" || len(policy.Links) != 2 {
		t.Fatalf("unexpected policy: %+v", policy)
	}
	if policy.Links[0].Kind != KindDelegatesTo || !policy.Links[0].Allow {
		t.Fatalf("unexpected first link: %+v", policy.Links[0])
	}
}

func TestParsePolicyAcceptsAllFiveKinds(t *testing.T) {
	for _, kind := range []LinkKind{KindDelegatesTo, KindSpawnedBy, KindCanObserve, KindCollaboratesWith, KindEscalatesTo} {
		data := []byte(`{"Seat":"a","Links":[{"From":"a","To":"b","Kind":"` + string(kind) + `","Allow":true}]}`)
		if _, err := ParsePolicy(data); err != nil {
			t.Errorf("ParsePolicy(%s): %v", kind, err)
		}
	}
}

func TestParsePolicyErrors(t *testing.T) {
	cases := map[string]string{
		"empty":                     ``,
		"malformed":                 `{"Seat":`,
		"missing seat":              `{"Links":[]}`,
		"empty seat":                `{"Seat":"","Links":[]}`,
		"unknown top field":         `{"Seat":"a","Bogus":1}`,
		"unknown link field":        `{"Seat":"a","Links":[{"From":"a","To":"b","Kind":"delegates_to","Allow":true,"Extra":1}]}`,
		"unknown link kind":         `{"Seat":"a","Links":[{"From":"a","To":"b","Kind":"shouts","Allow":true}]}`,
		"legacy reports_to":         `{"Seat":"a","Links":[{"From":"a","To":"b","Kind":"reports_to","Allow":true}]}`,
		"legacy consults":           `{"Seat":"a","Links":[{"From":"a","To":"b","Kind":"consults","Allow":true}]}`,
		"legacy notifies":           `{"Seat":"a","Links":[{"From":"a","To":"b","Kind":"notifies","Allow":true}]}`,
		"foreign link":              `{"Seat":"a","Links":[{"From":"x","To":"b","Kind":"delegates_to","Allow":true}]}`,
		"duplicate triple":          `{"Seat":"a","Links":[{"From":"a","To":"b","Kind":"delegates_to","Allow":true},{"From":"a","To":"b","Kind":"delegates_to","Allow":true}]}`,
		"duplicate differing allow": `{"Seat":"a","Links":[{"From":"a","To":"b","Kind":"delegates_to","Allow":true},{"From":"a","To":"b","Kind":"delegates_to","Allow":false}]}`,
		"trailing data":             `{"Seat":"a","Links":[]} extra`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePolicy([]byte(data)); err == nil {
				t.Fatalf("ParsePolicy(%s) succeeded, want error", data)
			}
		})
	}
}

func TestParsePolicyAllowsNullAndEmptyLinks(t *testing.T) {
	for _, data := range []string{`{"Seat":"a","Links":null}`, `{"Seat":"a"}`, `{"Seat":"a","Links":[]}`} {
		policy, err := ParsePolicy([]byte(data))
		if err != nil {
			t.Fatalf("ParsePolicy(%s): %v", data, err)
		}
		if policy.Seat != "a" || len(policy.Links) != 0 {
			t.Fatalf("ParsePolicy(%s) = %+v", data, policy)
		}
	}
}

func TestPolicyHashOrderIndependent(t *testing.T) {
	first := Policy{Seat: "a", Links: []Link{
		{From: "a", To: "b", Kind: KindDelegatesTo, Allow: true},
		{From: "a", To: "c", Kind: KindCollaboratesWith, Allow: true},
		{From: "a", To: "d", Kind: KindSpawnedBy, Allow: false},
	}}
	reversed := Policy{Seat: "a", Links: []Link{
		{From: "a", To: "d", Kind: KindSpawnedBy, Allow: false},
		{From: "a", To: "c", Kind: KindCollaboratesWith, Allow: true},
		{From: "a", To: "b", Kind: KindDelegatesTo, Allow: true},
	}}
	if PolicyHash(first) != PolicyHash(reversed) {
		t.Fatalf("hash depends on link order: %s != %s", PolicyHash(first), PolicyHash(reversed))
	}
	if PolicyHash(first) != PolicyHash(first) {
		t.Fatal("hash is not deterministic")
	}
	hash := PolicyHash(first)
	if len(hash) != 64 || strings.ToLower(hash) != hash {
		t.Fatalf("hash %q is not lowercase hex", hash)
	}
}

func TestPolicyHashChanges(t *testing.T) {
	base := Policy{Seat: "a", Links: []Link{{From: "a", To: "b", Kind: KindDelegatesTo, Allow: true}}}

	changed := map[string]Policy{
		"seat":  {Seat: "z", Links: base.Links},
		"allow": {Seat: "a", Links: []Link{{From: "a", To: "b", Kind: KindDelegatesTo, Allow: false}}},
		"to":    {Seat: "a", Links: []Link{{From: "a", To: "c", Kind: KindDelegatesTo, Allow: true}}},
		"kind":  {Seat: "a", Links: []Link{{From: "a", To: "b", Kind: KindCollaboratesWith, Allow: true}}},
		"extra": {Seat: "a", Links: append(base.Links, Link{From: "a", To: "z", Kind: KindSpawnedBy, Allow: true})},
		"empty": {Seat: "a"},
	}
	baseHash := PolicyHash(base)
	for name, policy := range changed {
		if PolicyHash(policy) == baseHash {
			t.Fatalf("hash did not change for %s", name)
		}
	}
}

func TestBypassSuspected(t *testing.T) {
	known := []string{"agenthub-send"}
	cases := []struct {
		name     string
		observed string
		want     bool
	}{
		{"rig send", "rig send bob hello", true},
		{"rig queue", "rig queue bob", true},
		{"rig broadcast", "rig broadcast hello", true},
		{"tmux send-keys", "tmux send-keys -t s hello Enter", true},
		{"tmux paste-buffer", "tmux paste-buffer -t s", true},
		{"leading env", "FOO=1 rig send bob", true},
		{"multiple leading env", "FOO=1 BAR=2 /usr/bin/rig queue bob", true},
		{"env value with colon", "PATH=/usr/bin:/bin rig broadcast", true},
		{"leading path", "/usr/bin/rig send bob", true},
		{"collapsed spaces", "rig   send bob", true},
		{"known wrapper", "agenthub-send rig send bob", false},
		{"known wrapper with path", "/usr/local/bin/agenthub-send x", false},
		{"known wrapper after env", "FOO=1 agenthub-send x", false},
		{"rig status", "rig status", false},
		{"rig alone", "rig", false},
		{"tmux list-sessions", "tmux list-sessions", false},
		{"tmux alone", "tmux", false},
		{"prefix only", "echo rig send bob", false},
		{"empty", "", false},
		{"case sensitive", "ROOM send bob", false},
		{"subcommand prefix", "room sends bob", false},

		// The native client, which is what a seat on this host actually types:
		// the retired `rig` spellings above no longer name a file the client owns.
		// `send` is the AUDITED path and `room queue create` is admitted, so neither is a bypass;
		// `input` writes straight into a pane and still is.
		{"native send to a session", "4genteam send agenthub--r--bob hello", false},
		{"native send by room and seat", "4genteam send 4genthub-min bob hello", false},
		{"native send absolute", "/usr/local/bin/4genteam send bob hello", false},
		{"native send after env", "AGENTHUB_URL=http://x 4genteam send bob hello", false},
		{"native collapsed spaces", "4genteam   send bob hello", false},
		{"native input sends to a seat", "4genteam input --seat bob", true},
		{"native rail create", "4genteam room queue create 4genthub-min --to agenthub--r--bob --summary s", false},
		{"native rail create collapsed", "4genteam room   queue   create 4genthub-min --to s", false},
		{"native seatcheck is the checked path", "4genteam seatcheck send bob hello", false},
		{"native seatcheck audit-scan reads, it does not send", "4genteam seatcheck audit-scan --file obs.txt", false},
		{"native rail list is not a send", "4genteam room queue list 4genthub-min", false},
		{"native rail claim is not a send", "4genteam room queue claim 4genthub-min qitem-1", false},
		{"native room stop is not a send", "4genteam room stop 4genthub-min", false},
		{"native verb that reads", "4genteam ps 4genthub-min --nodes --json", false},
		{"native alone", "4genteam", false},
		{"native truncated rail", "4genteam room queue", false},
		{"prefix only, native", "echo 4genteam send bob", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BypassSuspected(known, tc.observed); got != tc.want {
				t.Fatalf("BypassSuspected(%q) = %v, want %v", tc.observed, got, tc.want)
			}
		})
	}
}

// A known entry that names a SHAPE allows that shape alone: the checker's own
// spelling must never become a licence for the send beside it (the same hole the
// retired-verb pin hit, where an allowlist entry matched the verb's own form).
func TestBypassSuspectedKnownShapeIsNotALicence(t *testing.T) {
	known := []string{"4genteam seatcheck", "seatcheck"}
	if BypassSuspected(known, "4genteam seatcheck send bob hello") {
		t.Fatal("the checker's own spelling must not be flagged")
	}
	if BypassSuspected(known, "4genteam send bob hello") {
		t.Fatal("the audited path is never flagged, whoever is named")
	}
	if !BypassSuspected(known, "4genteam input --seat bob") {
		t.Fatal("naming 4genteam seatcheck must not allow 4genteam input")
	}
	if BypassSuspected(known, "4genteam room queue create 4genthub-min --to s") {
		t.Fatal("the rail's create is admitted, whoever is named")
	}
	// A bare program name keeps its old, whole-program meaning.
	if BypassSuspected([]string{"4genteam"}, "4genteam send bob hello") {
		t.Fatal("a bare known program name allows every invocation of it")
	}
	// And an entry for another program's shape does not touch this one: `4genteam input` is still
	// unaudited even with `seatcheck` named.
	if !BypassSuspected([]string{"seatcheck"}, "4genteam input --seat bob") {
		t.Fatal("a known entry for seatcheck must not allow 4genteam input")
	}
}

func TestBypassSuspectedNilKnown(t *testing.T) {
	if !BypassSuspected(nil, "rig send bob") {
		t.Fatal("nil known list should still flag a direct rig send")
	}
	if BypassSuspected(nil, "4genteam send bob hello") {
		t.Fatal("the audited path is never flagged, even with no known list")
	}
	if !BypassSuspected(nil, "4genteam input --seat bob") {
		t.Fatal("nil known list should still flag raw pane input")
	}
}

// 7.11: a destination the policy allows but the live set does not hold is refused with not_live; every
// verdict the policy itself reaches is returned unchanged, and a nil live set means liveness was not
// consulted at all.
func TestDecideLiveAddsOneReasonBesideDecide(t *testing.T) {
	allowed := Policy{Seat: "a", Links: []Link{{From: "a", To: "b", Kind: KindDelegatesTo, Allow: true}}}
	denied := Policy{Seat: "a", Links: []Link{{From: "a", To: "b", Kind: KindDelegatesTo, Allow: false}}}
	cases := []struct {
		name        string
		policy      Policy
		to          string
		intent      Intent
		live        LiveSet
		wantAllowed bool
		wantReason  string
	}{
		{"live destination", allowed, "b", IntentTask, LiveSet{"b": true}, true, reasonAllowed},
		{"destination not in the live set", allowed, "b", IntentTask, LiveSet{"c": true}, false, reasonNotLive},
		{"empty live set", allowed, "b", IntentTask, LiveSet{}, false, reasonNotLive},
		{"nil live set leaves the policy verdict", allowed, "b", IntentTask, nil, true, reasonAllowed},
		{"explicit deny outranks liveness", denied, "b", IntentTask, LiveSet{"b": true}, false, reasonExplicitDeny},
		{"no link outranks liveness", Policy{Seat: "a"}, "b", IntentTask, LiveSet{"b": true}, false, reasonNoLink},
		{"self message outranks liveness", allowed, "a", IntentTask, LiveSet{"a": true}, false, reasonSelfMessage},
		{"unknown intent outranks liveness", allowed, "b", Intent("gossip"), LiveSet{"b": true}, false, reasonUnknownIntent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideLive(tc.policy, tc.to, tc.intent, tc.live)
			if got.Allowed != tc.wantAllowed || got.Reason != tc.wantReason {
				t.Fatalf("DecideLive = {allowed %v, reason %q}, want {%v, %q}",
					got.Allowed, got.Reason, tc.wantAllowed, tc.wantReason)
			}
		})
	}
}

// A not_live refusal keeps the link the policy allowed, so the audit line and the refusal can name the
// same edge the decision used.
func TestDecideLiveKeepsTheAllowedLinkOnARefusal(t *testing.T) {
	policy := Policy{Seat: "a", Links: []Link{{From: "a", To: "b", Kind: KindDelegatesTo, Allow: true}}}
	got := DecideLive(policy, "b", IntentTask, LiveSet{})
	if got.Link == nil || got.Link.To != "b" || got.Link.Kind != KindDelegatesTo {
		t.Fatalf("link = %+v, want the delegates_to link to b", got.Link)
	}
}
