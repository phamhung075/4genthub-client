// The idle guard: when every seat of a room is idle it asks Jev whether the room should carry on and tells
// the lead the answer; a seat that is meant to be working but has gone silent is told to continue.
package clientlifecycle

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientjev"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/seatlog"
)

const (
	// stallNudges is how many times a silent seat is told to continue before the lead is told instead.
	stallNudges = 3
	// verdictQuestion is the one question the room is asked when it has gone quiet.
	verdictQuestion = "go"
	idleRunUsage    = "usage: 4genteam idle-run --room RIG [--lead SEAT] [--quiet SECONDS] [--stall SECONDS] [--every SECONDS] [--once] [--state-root DIR]"
)

const stallText = "You went quiet in the middle of a job. Continue where you stopped: check the working tree for what " +
	"is already done, then carry on without waiting for me. If you are blocked, say what blocks you."

// stallState is what one seat's pass remembers: when it was last told to continue and how often.
type stallState struct {
	nudged float64
	count  int
	told   bool
}

// IdleGuard is one `idle-run`. Every external reading is a field, so one pass is testable with no rig, no
// tmux, no Jev and no waiting.
type IdleGuard struct {
	Rig   string
	Lead  string
	Quiet float64 // every seat idle this long: the room has stopped
	Stall float64 // a seat that is not idle but logs nothing this long: it is stuck

	Now       func() float64
	Seats     func(rig string) []string
	SeatLog   func(rig, seat string) (string, string)
	Modified  func(path string) float64
	Idle      func(rig, seat string) bool
	Say       func(rig, seat, text string, raw bool)
	Interrupt func(rig, seat string)
	Active    func(rig string) ([]clientruntime.QueueRow, error)
	Ask       func(ctx context.Context, summary, detail string, q clientjev.Question) (clientjev.Selection, float64, error)
	// Wake is the off-peak gate every automatic message to a seat passes; false holds the message back.
	Wake func(rig string) (bool, string)
	Log  func(text string)

	State   map[string]*stallState
	decided float64 // the newest log time of the quiet already decided, so one quiet is decided once
	// asked is the open rail the last verdict was asked about. The guard's own message wakes the lead, whose
	// log then makes a new "quiet" five minutes later; with the same rail the answer cannot change, so the
	// room is not asked again until the rail does.
	asked    string
	hasAsked bool
}

// NewIdleGuard is a guard wired to the real world.
func NewIdleGuard(rig, lead string, quiet, stall int, stateRoot string, out io.Writer) *IdleGuard {
	g := &IdleGuard{Rig: rig, Lead: lead, Quiet: float64(quiet), Stall: float64(stall), State: map[string]*stallState{}}
	g.Now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
	g.Seats = func(rig string) []string { return seatlog.RigSeats(stateRoot, rig) }
	g.SeatLog = func(r, seat string) (string, string) { return seatlog.SeatLog(stateRoot, r, seat) }
	g.Modified = modTime
	g.Idle = rigIdle
	g.Say = rigSay
	g.Interrupt = func(rig, seat string) { tmuxInterrupt(stateRoot, rig, seat) }
	g.Active = func(rig string) ([]clientruntime.QueueRow, error) {
		return clientruntime.NewClient("").QueueList(rig, clientruntime.QueueListFilter{ActiveOnly: true})
	}
	g.Ask = askJev
	g.Wake = func(rig string) (bool, string) {
		nodes, ok, err := rigNodes(rig)
		if err != nil {
			return false, "the room's node records could not be read: " + err.Error()
		}
		if !ok {
			return true, ""
		}
		proceed, message, err := clientoffpeak.Check(false, nodes, os.Getenv)
		if err != nil {
			return false, "off-peak gate: " + err.Error()
		}
		return proceed, message
	}
	g.Log = func(text string) { fmt.Fprintf(out, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), text) }
	return g
}

// askJev is the live question: one choice, logged to the Jev decision log like a `decide` is.
func askJev(ctx context.Context, summary, detail string, q clientjev.Question) (clientjev.Selection, float64, error) {
	cfg, err := clientjev.FromEnv()
	if err != nil {
		return clientjev.Selection{}, 0, err
	}
	picks, askErr := cfg.SelectChoices(ctx, summary, detail, []clientjev.Question{q})
	pick := picks[q.ID]
	entry := clientjev.LogEntry{Timestamp: time.Now(), Kind: "decision", Summary: summary, Detail: detail,
		Choices: picks, Questions: []clientjev.Question{q}, Tier: "room", Category: "idle-room", Outcome: "decided"}
	if askErr != nil || pick.OptionID == "" || pick.Confidence < cfg.Threshold {
		entry.Outcome = "escalated-to-lead"
	}
	if askErr != nil {
		entry.Error = askErr.Error()
	}
	_ = cfg.Log(entry)
	return pick, cfg.Threshold, askErr
}

// Step is one pass: first the stuck seats, then the room as a whole.
func (g *IdleGuard) Step(ctx context.Context) {
	now := g.Now()
	idleAll, newest, counted := true, 0.0, 0
	silence := map[string]float64{}
	for _, seat := range g.Seats(g.Rig) {
		path, _ := g.SeatLog(g.Rig, seat)
		if path == "" {
			continue
		}
		counted++
		modified := g.Modified(path)
		silence[seat] = now - modified
		if modified > newest {
			newest = modified
		}
		state := g.State[seat]
		if state == nil {
			state = &stallState{}
			g.State[seat] = state
		}
		if silence[seat] < g.Stall {
			state.count, state.told = 0, false
		}
		if g.Idle(g.Rig, seat) {
			continue
		}
		idleAll = false
		if silence[seat] >= g.Stall {
			g.nudge(seat, silence[seat], now, state)
		}
	}
	if idleAll && counted > 0 && now-newest >= g.Quiet && newest > g.decided {
		g.decide(ctx, silence, newest)
	}
	if err := clientservices.WriteHeartbeat(clientservices.RoomGuard, g.Rig); err != nil {
		g.Log("cannot write the heartbeat: " + err.Error())
	}
}

// nudge tells a stuck seat to continue, a few times; after that the lead is told, once, and the seat is left.
func (g *IdleGuard) nudge(seat string, silent, now float64, state *stallState) {
	if now-state.nudged < g.Stall {
		return
	}
	if ok, why := g.Wake(g.Rig); !ok {
		g.Log(fmt.Sprintf("%s: stuck %.0fs, nudge held: %s", seat, silent, why))
		return
	}
	state.nudged = now
	if state.count >= stallNudges {
		if !state.told && seat != g.Lead {
			g.Say(g.Rig, g.Lead, fmt.Sprintf("%s has logged nothing for %.0f minutes and ignored %d nudges. "+
				"Look at it: reseat it, or reassign its work.", seat, silent/60, stallNudges), false)
			state.told = true
			g.Log(fmt.Sprintf("%s: stuck %.0fs after %d nudges, lead told", seat, silent, stallNudges))
		}
		return
	}
	g.Interrupt(g.Rig, seat)
	g.Say(g.Rig, seat, stallText, false)
	state.count++
	g.Log(fmt.Sprintf("%s: stuck %.0fs, told to continue (%d of %d)", seat, silent, state.count, stallNudges))
}

// decide asks Jev, once per quiet, whether the room should carry on, and tells the lead the answer.
func (g *IdleGuard) decide(ctx context.Context, silence map[string]float64, newest float64) {
	g.decided = newest
	rows, err := g.Active(g.Rig)
	if err != nil {
		g.Log("room quiet, the rail could not be read: " + err.Error())
		return
	}
	signature := railSignature(rows)
	if g.hasAsked && signature == g.asked {
		g.Log("room still idle, the open rail is unchanged since the last verdict: Jev not asked again")
		return
	}
	g.asked, g.hasAsked = signature, true
	var detail strings.Builder
	fmt.Fprintf(&detail, "Every seat of room %s is idle; the newest activity was %.0f minutes ago.\n", g.Rig, (g.Now()-newest)/60)
	fmt.Fprintf(&detail, "Open rail items (pending, in-progress, blocked): %d\n", len(rows))
	for i, row := range rows {
		if i == 10 {
			fmt.Fprintf(&detail, "- ... and %d more\n", len(rows)-10)
			break
		}
		fmt.Fprintf(&detail, "- %s [%s] to %s: %s\n", row.QitemID, row.State, row.DestinationSession, row.Summary)
	}
	for seat, quiet := range silence {
		fmt.Fprintf(&detail, "seat %s idle %.0f minutes\n", seat, quiet/60)
	}
	recommended := "stop"
	if len(rows) > 0 {
		recommended = "continue"
	}
	question := clientjev.Question{ID: verdictQuestion, Question: "Every seat of the room is idle. Should the room carry on working?",
		Options: []clientjev.Option{
			{ID: "continue", Label: "Continue: open work remains and the lead should assign the next item", Recommended: recommended == "continue"},
			{ID: "stop", Label: "Stop: the work is finished or waits on the owner, so start nothing new", Recommended: recommended == "stop"},
		}}
	pick, threshold, askErr := g.Ask(ctx, "room "+g.Rig+" has gone idle", detail.String(), question)
	var message string
	switch {
	case askErr != nil:
		message = fmt.Sprintf("The room went idle and Jev could not be asked (%v). Do not start new work: ask the owner whether to continue.", askErr)
	case pick.OptionID == "" || pick.Confidence < threshold:
		message = fmt.Sprintf("The room went idle and Jev is not sure (%s at %.2f, below %.2f). Do not start new work: ask the owner whether to continue.\n%s",
			pick.OptionID, pick.Confidence, threshold, detail.String())
	case pick.OptionID == "continue":
		message = fmt.Sprintf("The room went idle. Jev decided to CONTINUE (confidence %.2f). Take the next open item, assign it to an idle seat and brief that seat.\n%s",
			pick.Confidence, detail.String())
	default:
		message = fmt.Sprintf("The room went idle. Jev decided to STOP (confidence %.2f). Start no new work: report the result to the owner.\n%s",
			pick.Confidence, detail.String())
	}
	if ok, why := g.Wake(g.Rig); !ok {
		g.Log("room idle, message to the lead held: " + why)
		return
	}
	g.Say(g.Rig, g.Lead, message, false)
	g.Log(fmt.Sprintf("room idle: lead told (%s, %.2f)", pick.OptionID, pick.Confidence))
}

// railSignature identifies a set of open rail items by their ids, whatever order the rail lists them in.
func railSignature(rows []clientruntime.QueueRow) string {
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.QitemID + ":" + row.State
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

type idleRunCommand struct{}

func (idleRunCommand) Name() string { return "idle-run" }
func (idleRunCommand) Summary() string {
	return "the idle-room guard loop: ask Jev when every seat is idle, nudge a stuck seat"
}

func (idleRunCommand) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, idleRunUsage,
			"When every seat of the room has been idle for --quiet seconds, asks Jev whether the room should carry on and\n"+
				"sends the answer, with the open rail items, to the lead. A seat that is not idle but has logged nothing for\n"+
				"--stall seconds is interrupted and told to continue, up to three times, then the lead is told.")
		return clientcmd.ExitOK
	}
	flags := flag.NewFlagSet("idle-run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	rig := flags.String("room", "", "the rig to guard (required)")
	lead := flags.String("lead", "lead", "the seat told what Jev decided")
	quiet := flags.Int("quiet", 300, "seconds every seat must be idle before Jev is asked")
	stall := flags.Int("stall", 900, "seconds a working seat may log nothing before it is told to continue")
	every := flags.Int("every", 30, "seconds between passes")
	once := flags.Bool("once", false, "run one pass and exit")
	stateRoot := flags.String("state-root", "", "where the client keeps its state: the flag, else ~/.config/4genthub")
	if err := flags.Parse(args); err != nil {
		return clientcmd.ExitUsage
	}
	if strings.TrimSpace(*rig) == "" {
		fmt.Fprintf(stderr, "4genteam idle-run: --room is required - there is no default rig. %s\n", idleRunUsage)
		return clientcmd.ExitUsage
	}
	root, err := clientenv.ResolveStateRoot(*stateRoot)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam idle-run: %v\n", err)
		return clientcmd.ExitUsage
	}
	guard := NewIdleGuard(*rig, *lead, *quiet, *stall, root, stdout)
	guard.Log(fmt.Sprintf("guarding %s: lead %s, quiet %ds, stall %ds", *rig, *lead, *quiet, *stall))
	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt)
	for {
		guard.Step(ctx)
		if *once {
			return clientcmd.ExitOK
		}
		select {
		case <-interrupted:
			return clientcmd.ExitOK
		case <-time.After(time.Duration(*every) * time.Second):
		}
	}
}

// IdleLogPath is the guard's log for one rig.
func IdleLogPath(rig string) (string, error) {
	dir, err := LogDir()
	if err != nil {
		return "", err
	}
	return dir + "/idle-guard-" + rig + ".log", nil
}
