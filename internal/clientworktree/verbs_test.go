package clientworktree

import (
	"context"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// The verb's surface: one command, no room tool needed, help wherever it is asked, and a usage error
// rather than a guessed room when the room name is missing.
func TestWorktreeVerbRegistersAndAnswersHelp(t *testing.T) {
	commands := Commands()
	if len(commands) != 1 || commands[0].Name() != "worktree" {
		t.Fatalf("Commands() = %+v, want ONE verb named worktree", commands)
	}
	if commands[0].Summary() == "" {
		t.Error("the verb carries no summary for `4genteam help`")
	}

	var out, errOut strings.Builder
	if code := commands[0].Run(context.Background(), []string{"--help"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("worktree --help exited %d, want %d (stderr %q)", code, clientcmd.ExitOK, errOut.String())
	}
	for _, want := range []string{"worktree <verb> ROOM", "  list ", "  prune ", "--main DIR"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("worktree --help does not mention %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	errOut.Reset()
	if code := commands[0].Run(context.Background(), []string{"frobnicate"}, &out, &errOut); code != clientcmd.ExitUsage {
		t.Errorf("an unknown verb exited %d, want %d", code, clientcmd.ExitUsage)
	}

	out.Reset()
	errOut.Reset()
	if code := commands[0].Run(context.Background(), nil, &out, &errOut); code != clientcmd.ExitUsage {
		t.Errorf("no verb exited %d, want %d", code, clientcmd.ExitUsage)
	}
}

func TestWorktreeVerbsNeedARoomName(t *testing.T) {
	commands := Commands()
	for _, verb := range []string{"list", "prune"} {
		var out, errOut strings.Builder
		code := commands[0].Run(context.Background(), []string{verb}, &out, &errOut)
		if code != clientcmd.ExitUsage {
			t.Errorf("worktree %s with no room exited %d, want %d", verb, code, clientcmd.ExitUsage)
		}
		if !strings.Contains(errOut.String(), "room name is required") {
			t.Errorf("worktree %s did not say a room name is required: %q", verb, errOut.String())
		}
	}
}

// Each verb answers its own --help, so help is help wherever the flag sits.
func TestWorktreeVerbHelpIsPerVerb(t *testing.T) {
	commands := Commands()
	for _, verb := range []string{"list", "prune"} {
		var out, errOut strings.Builder
		code := commands[0].Run(context.Background(), []string{verb, "--help"}, &out, &errOut)
		if code != clientcmd.ExitOK {
			t.Fatalf("worktree %s --help exited %d, want %d (stderr %q)", verb, code, clientcmd.ExitOK, errOut.String())
		}
		if !strings.Contains(out.String(), "usage: 4genteam worktree "+verb+" ROOM") {
			t.Errorf("worktree %s --help printed the wrong usage:\n%s", verb, out.String())
		}
	}
}
