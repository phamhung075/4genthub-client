package seatwait

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

const usage = "usage: 4genteam wait [--room ROOM] [--seat SEAT] --on KIND:TARGET [--max DURATION] [--note TEXT] " +
	"[--repo DIR] [--slot-dir DIR] [--state-root DIR] | --clear | --show"

// Commands is this package's verb.
func Commands() []clientcmd.Command { return []clientcmd.Command{waitCommand{}} }

type waitCommand struct{}

func (waitCommand) Name() string { return "wait" }
func (waitCommand) Summary() string {
	return "declare what a blocked seat waits on, so it is not called idle; returns at once"
}

// deps are the verb's readings of the world, as a seam for tests.
type deps struct {
	now     func() time.Time
	getenv  func(string) string
	getwd   func() (string, error)
	rows    func(room string) ([]clientruntime.QueueRow, error)
	repo    func(dir string) string
	sha     func(repo, target string) (string, error)
	peakEnd func(now time.Time) (time.Time, bool)
}

var liveDeps = deps{
	now:    time.Now,
	getenv: os.Getenv,
	getwd:  os.Getwd,
	rows: func(room string) ([]clientruntime.QueueRow, error) {
		return clientruntime.NewClient("").QueueList(room, clientruntime.QueueListFilter{ActiveOnly: true})
	},
	repo:    ResolveRepo,
	sha:     ResolveSHA,
	peakEnd: PeakEnd,
}

func (waitCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	return run(liveDeps, args, stdout, stderr)
}

func run(d deps, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintf(stdout, "%s\n\n%s", usage, help())
		return clientcmd.ExitOK
	}
	rootFlag, args, rootErr := clientenv.StateRootFromFlags(args)
	if rootErr != nil {
		fmt.Fprintf(stderr, "4genteam wait: %v\n", rootErr)
		return clientcmd.ExitUsage
	}
	flags := flag.NewFlagSet("wait", flag.ContinueOnError)
	flags.SetOutput(stderr)
	room := flags.String("room", d.getenv("AGENTHUB_ROOM"), "the room (default $AGENTHUB_ROOM)")
	seat := flags.String("seat", d.getenv("AGENTHUB_SEAT"), "the seat (default $AGENTHUB_SEAT)")
	on := flags.String("on", "", "KIND:TARGET the seat waits on (required)")
	maxWait := flags.Duration("max", 0, "longest wait (default 60m, cap 4h; peak:offpeak defaults to the window end)")
	note := flags.String("note", "", "one line on why")
	repo := flags.String("repo", "", "repo a merge wait watches (default: the repo of the working directory)")
	slotDir := flags.String("slot-dir", d.getenv("AGENTHUB_SLOT_DIR"), "directory of slot* lock files a slot wait watches")
	clear := flags.Bool("clear", false, "end this seat's wait")
	show := flags.Bool("show", false, "print this seat's wait")
	if err := flags.Parse(args); err != nil {
		return clientcmd.ExitUsage
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "4genteam wait: unexpected argument %q\n%s\n", flags.Arg(0), usage)
		return clientcmd.ExitUsage
	}
	*room, *seat = strings.TrimSpace(*room), strings.TrimSpace(*seat)
	if *room == "" || *seat == "" {
		fmt.Fprintf(stderr, "4genteam wait: --room and --seat are required outside a seat (AGENTHUB_ROOM/AGENTHUB_SEAT unset)\n%s\n", usage)
		return clientcmd.ExitUsage
	}
	root, err := clientenv.ResolveStateRoot(rootFlag)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam wait: %v\n", err)
		return clientcmd.ExitUsage
	}
	now := d.now()
	if *show || *clear {
		current, ok := Read(root, *room, *seat)
		if !ok {
			fmt.Fprintf(stdout, "%s: no wait\n", *seat)
			return clientcmd.ExitOK
		}
		if *show {
			fmt.Fprintf(stdout, "%s: %s until %s\n", *seat, current.Status(now), current.Deadline.UTC().Format("15:04Z"))
			return clientcmd.ExitOK
		}
		if err := End(root, current, now, EndCleared, "cleared by the seat"); err != nil {
			fmt.Fprintf(stderr, "4genteam wait: %v\n", err)
			return clientcmd.ExitUnavailable
		}
		fmt.Fprintf(stdout, "%s: wait on %s cleared after %s\n", *seat, current.On(), Short(now.Sub(current.Started)))
		return clientcmd.ExitOK
	}
	kind, target, err := ParseOn(*on)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam wait: %v\n", err)
		return clientcmd.ExitUsage
	}
	life := ClampMax(*maxWait)
	if kind == "peak" {
		end, peak := d.peakEnd(now)
		if !peak {
			fmt.Fprintln(stderr, "4genteam wait: it is off-peak now, so there is nothing to wait for: go on with the work")
			return clientcmd.ExitRefused
		}
		if *maxWait <= 0 {
			life = ClampMax(end.Sub(now) + time.Minute)
		}
	}
	rec := Record{Room: *room, Seat: *seat, Kind: kind, Target: target, Note: strings.TrimSpace(*note),
		Started: now, Deadline: now.Add(life)}
	switch kind {
	case "merge":
		dir := *repo
		if dir == "" {
			dir, _ = d.getwd()
		}
		if rec.Repo = d.repo(dir); rec.Repo != "" {
			if sha, err := d.sha(rec.Repo, target); err == nil && sha != "" {
				rec.SHA = sha
			}
		}
		if rec.SHA == "" {
			fmt.Fprintf(stderr, "4genteam wait: note: %s is not a commit in %q, so its merge cannot be seen; the wait ends on a message or at --max\n", target, dir)
		}
	case "slot":
		rec.SlotDir = *slotDir
		if rec.SlotDir == "" {
			fmt.Fprintln(stderr, "4genteam wait: note: no --slot-dir (or AGENTHUB_SLOT_DIR), so a free slot cannot be seen; the wait ends on a message or at --max")
		}
	}
	// THE RAIL IS READ NOW so a wait never hides claimable work; when it cannot be read the guard's first pass
	// takes the same snapshot and ends the wait if it finds claimable work.
	if rows, err := d.rows(*room); err == nil {
		if err := Snapshot(&rec, rows); err != nil {
			fmt.Fprintf(stderr, "4genteam wait: refused: %v\n", err)
			return clientcmd.ExitRefused
		}
	} else {
		fmt.Fprintf(stderr, "4genteam wait: note: the rail could not be read (%v); the guard checks it on its next pass\n", err)
	}
	if err := Start(root, rec); err != nil {
		fmt.Fprintf(stderr, "4genteam wait: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	fmt.Fprintf(stdout, "%s: waiting on %s until %s (max %s). End your turn now: a message, a new rail row, "+
		"the event itself or the expiry wakes you. Do not poll.\n", *seat, rec.On(), rec.Deadline.UTC().Format("15:04Z"), Short(life))
	return clientcmd.ExitOK
}

func help() string {
	var kinds strings.Builder
	for _, name := range []string{"review", "merge", "answer", "slot", "peak", "seat"} {
		fmt.Fprintf(&kinds, "  %-7s %s\n", name, Kinds[name])
	}
	return "Records that this seat is blocked on one named thing and returns at once: nothing sleeps, nothing polls.\n" +
		"While the wait holds, the idle guard does not nudge the seat, the stuck scan does not call it idle and heal\n" +
		"does not report it quiet. The guard ends the wait at the first of: a new turn of the seat (a message), a new\n" +
		"rail row for the seat, the awaited event when it can be seen (row closed, commit in main, free slot,\n" +
		"off-peak), or --max; the last two wake the seat with one line. A wait is refused while a pending row\n" +
		"other than its target is addressed to the seat: take a backup item first.\n\nKinds:\n" + kinds.String()
}
