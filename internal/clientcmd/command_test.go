package clientcmd

import "testing"

// TestHelpRequestedIsHelpWhereverItAppears: ONE function decides what help is, so a verb that parses
// its own flags cannot treat --help as an unknown flag - which is the defect this replaced.
func TestHelpRequestedIsHelpWhereverItAppears(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"--help"}, {"room", "--help"}, {"--help", "room"}, {"--out", "x", "-h"}} {
		if !HelpRequested(args) {
			t.Errorf("HelpRequested(%v) = false, want true", args)
		}
	}
	for _, args := range [][]string{nil, {}, {"room", "seat"}, {"--update"}, {"-"}} {
		if HelpRequested(args) {
			t.Errorf("HelpRequested(%v) = true, want false", args)
		}
	}
}
