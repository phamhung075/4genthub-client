package commpolicy

import (
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
	}{
		{
			name:       "default deny with no links",
			policy:     Policy{Seat: "a"},
			to:         "b",
			intent:     IntentTask,
			wantReason: reasonNoLink,
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
			name:       "wrong kind",
			policy:     Policy{Seat: "a", Links: []Link{{From: "a", To: "b", Kind: KindCollaboratesWith, Allow: true}}},
			to:         "b",
			intent:     IntentTask,
			wantReason: reasonNoLink,
		},
		{
			name:       "other seat's links ignored",
			policy:     Policy{Seat: "a", Links: []Link{{From: "x", To: "b", Kind: KindDelegatesTo, Allow: true}}},
			to:         "b",
			intent:     IntentTask,
			wantReason: reasonNoLink,
		},
		{
			name:       "link to another recipient ignored",
			policy:     Policy{Seat: "a", Links: []Link{taskAllow}},
			to:         "c",
			intent:     IntentTask,
			wantReason: reasonNoLink,
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
		})
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
			if got.Allowed || got.Reason != reasonNoLink {
				t.Errorf("intent %q with kind %q = %+v, want no link", intent, kind, got)
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
		{"case sensitive", "RIG send bob", false},
		{"subcommand prefix", "rig sends bob", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BypassSuspected(known, tc.observed); got != tc.want {
				t.Fatalf("BypassSuspected(%q) = %v, want %v", tc.observed, got, tc.want)
			}
		})
	}
}

func TestBypassSuspectedNilKnown(t *testing.T) {
	if !BypassSuspected(nil, "rig send bob") {
		t.Fatal("nil known list should still flag a direct rig send")
	}
}
