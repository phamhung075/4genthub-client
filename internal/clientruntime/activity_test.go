package clientruntime

import "testing"

// THE ALLOW-SET IS THE UNIT: exactly one word releases a wait that may destroy a turn, and every other word - the
// five this client can see today, the empty word, and a word nobody defines - releases nothing.
func TestActivityReleasesTheWaitOnlyOnIdle(t *testing.T) {
	accepts := []string{
		"idle",
		"idle-at-prompt", // the STORED form: normalized deliberately, so the alias releases rather than sitting silent
	}
	refuses := []string{
		"working",     // the opposite answer
		"needs_input", // a seat mid-turn waiting for a person
		"unknown",     // not evidence, by the daemon's own taxonomy comment
		"running",     // the STATUS word the old `!= "running"` test was written around
		"",            // a reading that happened and carried no word
		"drifting",    // a word nobody defines - the arm that fails when the vocabulary grows
	}
	for _, word := range accepts {
		if released, _ := ActivityReleasesTheWait(word); !released {
			t.Errorf("%q did not release the wait", word)
		}
	}
	for _, word := range refuses {
		if released, _ := ActivityReleasesTheWait(word); released {
			t.Errorf("%q released a wait that may destroy a turn: the predicate is answering a list, not the rule", word)
		}
	}
}

// AND THE REFUSAL NAMES ITS WORD, which is what turns the NEXT vocabulary growth into a five-minute fix instead of a
// gate that never opens with nothing said. A word this client recognizes refuses QUIETLY - the refusal is deliberate
// - while a word nobody has seen refuses with the word attached.
func TestTheRefusalNamesTheWordItDidNotRecognize(t *testing.T) {
	cases := []struct {
		word string
		want string
	}{
		{"drifting", "drifting"}, // nobody defines it: the growth arm, and the word must come back
		{"working", ""},          // recognized, deliberately refused
		{"needs_input", ""},      // recognized, deliberately refused
		{"unknown", ""},          // recognized, deliberately refused
		{"running", ""},          // recognized, deliberately refused
		{"", ""},                 // a wordless reading is not an unrecognized word
		{"idle", ""},             // released, so nothing to name
		{"idle-at-prompt", ""},   // the alias is recognized AND releases
	}
	for _, c := range cases {
		if _, unrecognized := ActivityReleasesTheWait(c.word); unrecognized != c.want {
			t.Errorf("word %q: unrecognized=%q, want %q", c.word, unrecognized, c.want)
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

// AND THE RECOGNIZED SET IS NOT THE QUIET SET: it exists so that "refused because I know what that word means" and
// "refused because I have never seen that word" cannot collapse back into one silent outcome.
func TestTheRecognizedSetIsWiderThanTheQuietSet(t *testing.T) {
	if len(knownActivityWords) <= len(quietActivityWords) {
		t.Fatal("the recognized set no longer separates a deliberate refusal from an unrecognized word")
	}
	for word := range quietActivityWords {
		if !knownActivityWords[word] {
			t.Fatalf("the quiet word %q is not in the recognized set", word)
		}
	}
}
