package clientoutbox

import "testing"

// The two maps hold exactly the server's ENFORCEMENT list - the four kinds a client may push
// (agenthub_go `IntentKinds`). The bound matters in one direction only: a kind the server refuses is a
// row it answers unsupported_kind and the drain then drops, so a map entry the wire does not carry
// expresses a push that cannot happen. The six ledger kinds that are written by their own routes
// (evidence_submitted, context_updated) or are not in the vocabulary at all (assigned, claimed,
// delivered, context_loaded, escalated, human_decision) are not a client's to name.
func TestKindMapsHoldExactlyThePushableKinds(t *testing.T) {
	facts := []string{"progress", "handover"}
	decisions := []string{"status_changed", "gate_verdict"}

	if len(FactKinds) != len(facts) || len(DecisionKinds) != len(decisions) {
		t.Fatalf("FactKinds holds %d kinds and DecisionKinds %d, want %d and %d: the maps mirror IntentKinds, not a wider set",
			len(FactKinds), len(DecisionKinds), len(facts), len(decisions))
	}

	for _, k := range facts {
		if !IsFact(k) {
			t.Errorf("%q is a pushable fact but FactKinds does not name it", k)
		}
		if IsDecision(k) {
			t.Errorf("%q is a fact and DecisionKinds sorts it as one too", k)
		}
	}
	for _, k := range decisions {
		if !IsDecision(k) {
			t.Errorf("%q is a pushable decision but DecisionKinds does not name it", k)
		}
		if IsFact(k) {
			t.Errorf("%q is a decision and FactKinds sorts it as a fact too", k)
		}
	}

	for _, k := range []string{
		"evidence_submitted", "context_updated",
		"assigned", "claimed", "delivered", "context_loaded", "escalated", "human_decision",
	} {
		if IsFact(k) || IsDecision(k) {
			t.Errorf("%q is ordered as a pushable kind, but the ledger refuses it: evidence_submitted and context_updated have their own routes, and the rest are not in the vocabulary", k)
		}
	}
}
