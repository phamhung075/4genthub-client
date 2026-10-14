package clientoutbox

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// Commands returns the outbox and ledger commands.
func Commands() []clientcmd.Command {
	return []clientcmd.Command{
		outboxCommand{},
		ledgerCommand{},
	}
}

// DefaultOutboxPath returns the default location for outbox.json.
func DefaultOutboxPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "4genthub", "ledger", "outbox.json"), nil
}

type outboxCommand struct{}

func (outboxCommand) Name() string    { return "outbox" }
func (outboxCommand) Summary() string { return "manage local ledger outbox (status, drain)" }

func (outboxCommand) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) || len(args) == 0 {
		fmt.Fprintln(stdout, "usage: 4genteam outbox <status|drain|prefetch|cached> [--path <outbox.json>]")
		return clientcmd.ExitOK
	}

	action := args[0]
	flags := flag.NewFlagSet("outbox", flag.ContinueOnError)
	pathFlag := flags.String("path", "", "path to outbox state file")
	if err := flags.Parse(args[1:]); err != nil {
		return clientcmd.ExitUsage
	}

	path := *pathFlag
	if path == "" {
		var err error
		path, err = DefaultOutboxPath()
		if err != nil {
			fmt.Fprintf(stderr, "4genteam outbox: %v\n", err)
			return clientcmd.ExitUnavailable
		}
	}

	store, err := NewStore(path)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam outbox: %v\n", err)
		return clientcmd.ExitUnavailable
	}

	baseURL := os.Getenv("AGENTHUB_URL")
	token := os.Getenv("AGENTHUB_TOKEN")
	mgr := NewManager(store, baseURL, token, clientruntime.NewClient(""))

	switch action {
	case "status":
		intents := store.Intents()
		fmt.Fprintf(stdout, "Outbox Path: %s\nCursor:      %d\nPending:     %d\n", path, store.Cursor(), len(intents))
		for i, it := range intents {
			fmt.Fprintf(stdout, "  [%d] %s (%s) task=%s\n", i+1, it.ClientEventID, it.Kind, it.TaskID)
		}
		return clientcmd.ExitOK

	case "drain":
		answers, err := mgr.Drain(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam outbox drain: %v\n", err)
			return clientcmd.ExitUnavailable
		}
		fmt.Fprintf(stdout, "Drained %d intents.\n", len(answers))
		for _, ans := range answers {
			if ans.Accepted {
				fmt.Fprintf(stdout, "  ✓ Accepted (user_seq: %d, seq: %d)\n", ans.UserSeq, ans.Seq)
			} else {
				fmt.Fprintf(stdout, "  ✗ Rejected: %s (task_seq: %d)\n", ans.Reason, ans.TaskSeq)
			}
		}
		return clientcmd.ExitOK

	case "prefetch":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "usage: 4genteam outbox prefetch <task_id>")
			return clientcmd.ExitUsage
		}
		taskID := args[1]
		brief, err := mgr.Prefetch(ctx, taskID)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam outbox prefetch: %v\n", err)
			return clientcmd.ExitUnavailable
		}
		fmt.Fprintf(stdout, "Prefetched & cached brief for task %s\n", brief.TaskID)
		return clientcmd.ExitOK

	case "cached":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "usage: 4genteam outbox cached <task_id>")
			return clientcmd.ExitUsage
		}
		taskID := args[1]
		brief, err := GetCachedBrief(taskID)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam outbox cached: %v\n", err)
			return clientcmd.ExitUnavailable
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(brief)
		return clientcmd.ExitOK

	default:
		fmt.Fprintf(stderr, "unknown outbox action: %s\n", action)
		return clientcmd.ExitUsage
	}
}

type ledgerCommand struct{}

func (ledgerCommand) Name() string { return "ledger" }
func (ledgerCommand) Summary() string {
	return "sync and pull events from 4genthub ledger (pull, sync)"
}

func (ledgerCommand) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) || len(args) == 0 {
		fmt.Fprintln(stdout, "usage: 4genteam ledger <pull|sync> [--path <outbox.json>]")
		return clientcmd.ExitOK
	}

	action := args[0]
	flags := flag.NewFlagSet("ledger", flag.ContinueOnError)
	pathFlag := flags.String("path", "", "path to outbox state file")
	if err := flags.Parse(args[1:]); err != nil {
		return clientcmd.ExitUsage
	}

	path := *pathFlag
	if path == "" {
		var err error
		path, err = DefaultOutboxPath()
		if err != nil {
			fmt.Fprintf(stderr, "4genteam ledger: %v\n", err)
			return clientcmd.ExitUnavailable
		}
	}

	store, err := NewStore(path)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam ledger: %v\n", err)
		return clientcmd.ExitUnavailable
	}

	baseURL := os.Getenv("AGENTHUB_URL")
	token := os.Getenv("AGENTHUB_TOKEN")
	mgr := NewManager(store, baseURL, token, clientruntime.NewClient(""))

	switch action {
	case "pull":
		events, err := mgr.Pull(ctx, 100)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam ledger pull: %v\n", err)
			return clientcmd.ExitUnavailable
		}
		fmt.Fprintf(stdout, "Pulled %d events. Current Cursor: %d\n", len(events), store.Cursor())
		for _, e := range events {
			fmt.Fprintf(stdout, "  [user_seq=%d] %s (%s) task=%s actor=%s/%s\n",
				e.UserSeq, e.Kind, e.CreatedAt, e.TaskID, e.ActorKind, e.ActorID)
		}
		return clientcmd.ExitOK

	case "sync":
		answers, events, err := mgr.Sync(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam ledger sync: %v\n", err)
			return clientcmd.ExitUnavailable
		}
		fmt.Fprintf(stdout, "Synced: %d intents drained, %d ledger events pulled. Cursor: %d\n",
			len(answers), len(events), store.Cursor())
		return clientcmd.ExitOK

	case "work":
		machine := ""
		if len(args) > 1 {
			machine = args[1]
		}
		if machine == "" {
			machine = "4genthub-min"
		}
		work, err := mgr.FetchWork(ctx, machine)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam ledger work: %v\n", err)
			return clientcmd.ExitUnavailable
		}
		fmt.Fprintf(stdout, "Found %d work items for room %s:\n", len(work), machine)
		for _, w := range work {
			claimed := "unclaimed"
			if w.ClaimedBy != nil && *w.ClaimedBy != "" {
				claimed = "claimed by " + *w.ClaimedBy
			}
			fmt.Fprintf(stdout, "  [%s] task %s: %s (assignee: %s, %s)\n", w.Status, w.ID, w.Title, w.Assignee, claimed)
		}
		if err := mgr.WakeSeatsForWork(ctx, machine, work); err != nil {
			fmt.Fprintf(stderr, "4genteam ledger work wake: %v\n", err)
		}
		return clientcmd.ExitOK

	default:
		fmt.Fprintf(stderr, "unknown ledger action: %s\n", action)
		return clientcmd.ExitUsage
	}
}
