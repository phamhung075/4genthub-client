package clientenv

import (
	"errors"
	"reflect"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// The root flag is taken WHEREVER it sits, including after the positionals the old lookahead allowed, and a
// repeated flag resolves to the LAST one - the way every other repeated flag of this CLI does. This is the
// property every converted verb rests on: one reader, one answer.
func TestTheRootFlagIsTakenWhereverItSitsAndTheLastOneWins(t *testing.T) {
	value, rest, err := StateRootFromFlags([]string{"room-a", "--state-root", "/one", "--socket", "/s", "--state-root=/two"})
	if err != nil {
		t.Fatalf("refused a well-formed invocation: %v", err)
	}
	if value != "/two" {
		t.Errorf("value = %q, want the LAST spelling (/two)", value)
	}
	if want := []string{"room-a", "--socket", "/s"}; !reflect.DeepEqual(rest, want) {
		t.Errorf("rest = %q, want %q", rest, want)
	}

	// The value of the root is never mistaken for a positional, and a call with no flag is not an error.
	value, rest, err = StateRootFromFlags([]string{"--state-root", "room-a"})
	if err != nil {
		t.Fatalf("refused a well-formed invocation: %v", err)
	}
	if value != "room-a" || len(rest) != 0 {
		t.Errorf("value = %q rest = %q, want the next word to be the value and no positionals", value, rest)
	}
	if value, rest, err := StateRootFromFlags(nil); err != nil || value != "" || len(rest) != 0 {
		t.Errorf("no arguments = (%q, %q, %v), want the empty flag and no positionals", value, rest, err)
	}
}

// THE RUNTIME'S SPELLING IS REFUSED HERE, in both forms and wherever it sits, with the CLI-wide caller-error
// code and ONE sentence: a script that carried it must be told to change the word, not handed this group's
// own code. The refusal is distinguishable from every other error (IsStateDirRetired), which is what lets a
// verb that numbers its own bad arguments differently still exit 2 for exactly this one.
func TestTheRuntimeSpellingIsRefusedWithTheOneSentence(t *testing.T) {
	for _, args := range [][]string{
		{"--state-dir", "/x"},
		{"--state-dir=/x"},
		{"room-a", "--state-root", "/one", "--state-dir", "/x"},
	} {
		_, _, err := StateRootFromFlags(args)
		if err == nil {
			t.Fatalf("%q was accepted; the runtime's spelling is not a client flag", args)
		}
		if !IsStateDirRetired(err) {
			t.Errorf("%q: IsStateDirRetired(err) = false", args)
		}
		if got := err.Error(); got != StateRootRetiredMessage {
			t.Errorf("%q: the refusal says %q, want the one sentence %q", args, got, StateRootRetiredMessage)
		}
		if code := clientcmd.CodeOf(err, clientcmd.ExitOK); code != clientcmd.ExitUsage {
			t.Errorf("%q: exit %d, want %d", args, code, clientcmd.ExitUsage)
		}
	}
	if IsStateDirRetired(errors.New(StateRootRetiredMessage)) {
		t.Error("a plain error carrying the sentence is not the typed refusal")
	}
}
