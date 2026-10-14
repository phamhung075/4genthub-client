package clientruntime

import "testing"

// THE ALLOW-SET IS THE UNIT: exactly one word releases a wait that may destroy a turn, and every other word - the
// five this client can see today, the empty word, and a word nobody defines - releases nothing.
func TestActivityReleasesTheWaitOnlyOnIdle(t *testing.T) {
	accepts := []string{"idle"}
	refuses := []string{
		"working",        // the opposite answer
		"needs_input",    // a seat mid-turn waiting for a person
		"unknown",        // not evidence, by the daemon's own taxonomy comment
		"running",        // the STATUS word the old `!= "running"` test was written around
		"",               // a reading that happened and carried no word
		"idle-at-prompt", // the stored form: if the daemon ever stops aliasing it to `idle`, this must be a decision
		"drifting",       // a word nobody defines - the arm that fails when the vocabulary grows
	}
	for _, word := range accepts {
		if !ActivityReleasesTheWait(word) {
			t.Errorf("%q did not release the wait", word)
		}
	}
	for _, word := range refuses {
		if ActivityReleasesTheWait(word) {
			t.Errorf("%q released a wait that may destroy a turn: the predicate is answering a list, not the rule", word)
		}
	}
}

// AND THE SET IS SMALL ON PURPOSE - this is the test a future implementer meets when adding a member. Widening it
// means asserting that the new word, too, cannot be a seat mid-turn.
func TestTheQuietSetNamesExactlyOneWord(t *testing.T) {
	if len(quietActivityWords) != 1 {
		t.Fatalf("the quiet set has %d words; each one is a claim that a wait ending in a reseat may proceed", len(quietActivityWords))
	}
}
