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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientdecisions"
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
	idleRunUsage    = "usage: 4genteam idle-run --room ROOM [--lead SEAT] [--quiet SECONDS] [--stall SECONDS] [--every SECONDS] [--once] [--state-root DIR]"
)

const stallText = "You went quiet in the middle of a job. Continue where you stopped: check the working tree for what " +
	"is already done, then carry on without waiting for me. If you are blocked, say what blocks you."

// stallState is what one seat's pass remembers: when it was last told to continue and how often.
type stallState struct {
	nudged float64
	count  int
	told   bool
}

// IdleGuard is one `idle-run`. Every external reading is a field, so one pass is testable with no room, no
// tmux, no Jev and no waiting.
type IdleGuard struct {
	Room string
	Lead string
	// Root is the state root the verb resolved: the heartbeat and this guard's log live under it.
	Root  string
	Quiet float64 // every seat idle this long: the room has stopped
	Stall float64 // a seat that is not idle but logs nothing this long: it is stuck

	Now      func() float64
	Seats    func(room string) []string
	SeatLog  func(room, seat string) (string, string)
	Modified func(path string) float64
	// Started is when the seat's current session began (unix seconds), 0 when it cannot be read. A seat's
	// silence is never counted from before its start: a fresh seat's log is the PREVIOUS session's until
	// it takes its first turn, and counting from that file nudged every seat of a room the second it came up
	// (room aa2-rs-peak, 2026-10-29 06:52:27: "peak-dev: stuck 75927s, told to continue", two seconds after
	// `up`, while agy was still starting - the nudge and then the brief sat in agy's queue).
	Started   func(room, seat string) float64
	Idle      func(room, seat string) bool
	Say       func(room, seat, text string, raw bool)
	Interrupt func(room, seat string)
	Active    func(room string) ([]clientruntime.QueueRow, error)
	Ask       func(ctx context.Context, summary, detail string, q clientdecisions.Question) (clientjev.Selection, float64, error)
	// Wake is the off-peak gate every automatic message to a seat passes; false holds the message back.
	Wake func(room string) (bool, string)
	Log  func(text string)

	State   map[string]*stallState
	decided float64 // the newest log time of the quiet already decided, so one quiet is decided once
	// asked is the open rail the last verdict was asked about. The guard's own message wakes the lead, whose
	// log then makes a new "quiet" five minutes later; with the same rail the answer cannot change, so the
	// room is not asked again until the rail does.
	asked    string
	hasAsked bool
	reminded float64
}

// NewIdleGuard is a guard wired to the real world.
func NewIdleGuard(room, lead string, quiet, stall int, stateRoot string, out io.Writer) *IdleGuard {
	g := &IdleGuard{Room: room, Lead: lead, Root: stateRoot, Quiet: float64(quiet), Stall: float64(stall), State: map[string]*stallState{}}
	g.Now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
	g.Seats = func(room string) []string { return seatlog.RoomSeats(stateRoot, room) }
	// seatlog.SeatLog is the ONE resolver: it reads the newer of the omp session and the agy transcript,
	// so the stall clock cannot count an agy seat from the omp-era file it left behind (the false alarm
	// raised three times on 2026-10-17).
	g.SeatLog = func(r, seat string) (string, string) { return seatlog.SeatLog(stateRoot, r, seat) }
	g.Modified = modTime
	g.Started = func(room, seat string) float64 { return sessionCreated(stateRoot, room, seat) }
	g.Idle = roomIdle
	// The idle guard's nudges are latched and retried by its own clock, so a transport refusal is not this
	// guard's signal: the supervisor's resume is the one send whose loss must keep its witness.
	g.Say = func(room, seat, text string, raw bool) { _ = roomSay(room, seat, text, raw) }
	g.Interrupt = func(room, seat string) { tmuxInterrupt(stateRoot, room, seat) }
	g.Active = func(room string) ([]clientruntime.QueueRow, error) {
		return clientruntime.NewClient("").QueueList(room, clientruntime.QueueListFilter{ActiveOnly: true})
	}
	g.Ask = askJev
	g.Wake = func(room string) (bool, string) {
		nodes, ok, err := roomNodes("", room)
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
func askJev(ctx context.Context, summary, detail string, q clientdecisions.Question) (clientjev.Selection, float64, error) {
	cfg, err := clientjev.FromEnv()
	if err != nil {
		return clientjev.Selection{}, 0, err
	}
	picks, askErr := cfg.SelectChoices(ctx, summary, detail, []clientdecisions.Question{q})
	pick := picks[q.ID]
	entry := clientjev.LogEntry{Timestamp: time.Now(), Kind: "decision", Summary: summary, Detail: detail,
		Choices: picks, Questions: []clientdecisions.Question{q}, Tier: "room", Category: "idle-room", Outcome: "decided"}
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
	rows, _ := g.Active(g.Room)
	seatHasAssignedWork := map[string]bool{}
	for _, row := range rows {
		if row.State == "in-progress" {
			dest := row.DestinationSession
			if idx := strings.LastIndex(dest, "--"); idx != -1 {
				dest = dest[idx+2:]
			}
			seatHasAssignedWork[dest] = true
		}
	}
	for _, seat := range g.Seats(g.Room) {
		path, _ := g.SeatLog(g.Room, seat)
		if path == "" {
			continue
		}
		counted++
		modified := g.Modified(path)
		if g.Started != nil {
			if started := g.Started(g.Room, seat); started > modified {
				modified = started
			}
		}
		silence[seat] = now - modified
		if modified > newest {
			newest = modified
		}
		state := g.State[seat]
		if state == nil {
			state = &stallState{}
			g.State[seat] = state
		}
		if silence[seat] < float64(g.Stall) {
			state.count, state.told = 0, false
		}
		if g.Idle(g.Room, seat) {
			if seatHasAssignedWork[seat] && silence[seat] >= float64(g.Stall) {
				g.nudge(seat, silence[seat], now, state)
			}
			continue
		}
		idleAll = false
		if silence[seat] >= float64(g.Stall) {
			g.nudge(seat, silence[seat], now, state)
		}
	}
	if idleAll && counted > 0 && now-newest >= float64(g.Quiet) && (newest > g.decided || now-g.decided >= float64(g.Quiet*2)) {
		g.decide(ctx, silence, newest)
	}
	if err := clientservices.WriteHeartbeat(g.Root, clientservices.RoomGuard, g.Room); err != nil {
		g.Log("cannot write the heartbeat: " + err.Error())
	}
}

// nudge tells a stuck seat to continue, a few times; after that the lead is told, once, and the seat is left.
func (g *IdleGuard) nudge(seat string, silent, now float64, state *stallState) {
	if now-state.nudged < g.Stall {
		return
	}
	if ok, why := g.Wake(g.Room); !ok {
		g.Log(fmt.Sprintf("%s: stuck %.0fs, nudge held: %s", seat, silent, why))
		return
	}
	state.nudged = now
	if state.count >= stallNudges {
		if !state.told && seat != g.Lead {
			g.Say(g.Room, g.Lead, fmt.Sprintf("%s has logged nothing for %.0f minutes and ignored %d nudges. "+
				"Look at it: reseat it, or reassign its work.", seat, silent/60, stallNudges), false)
			state.told = true
			g.Log(fmt.Sprintf("%s: stuck %.0fs after %d nudges, lead told", seat, silent, stallNudges))
		}
		return
	}
	g.Interrupt(g.Room, seat)
	g.Say(g.Room, seat, stallText, false)
	state.count++
	g.Log(fmt.Sprintf("%s: stuck %.0fs, told to continue (%d of %d)", seat, silent, state.count, stallNudges))
}

// decide asks Jev, once per quiet, whether the room should carry on, and tells the lead the answer.
func (g *IdleGuard) decide(ctx context.Context, silence map[string]float64, newest float64) {
	g.decided = newest
	rows, err := g.Active(g.Room)
	if err != nil {
		g.Log("room quiet, the rail could not be read: " + err.Error())
		return
	}
	signature := railSignature(rows)
	var detail strings.Builder
	fmt.Fprintf(&detail, "Every seat of room %s is idle; the newest activity was %.0f minutes ago.\n", g.Room, (g.Now()-newest)/60)
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

	if g.hasAsked && signature == g.asked {
		if len(rows) > 0 && g.reminded > 0 && g.Now()-g.reminded >= float64(g.Quiet*2) {
			g.reminded = g.Now()
			if ok, why := g.Wake(g.Room); ok {
				g.Log(fmt.Sprintf("room still idle with %d open rail item(s), re-signaling lead", len(rows)))
				g.Say(g.Room, g.Lead, fmt.Sprintf("The room is still idle with %d open rail items. Lead: wake the assigned seats or assign the next pending item.\n%s", len(rows), detail.String()), false)
			} else {
				g.Log("room still idle, reminder to lead held: " + why)
			}
		} else {
			g.Log("room still idle, the open rail is unchanged since the last verdict: Jev not asked again")
		}
		return
	}
	g.asked, g.hasAsked = signature, true
	g.reminded = g.Now()
	recommended := "stop"
	if len(rows) > 0 {
		recommended = "continue"
	}
	question := clientdecisions.Question{ID: verdictQuestion, Question: "Every seat of the room is idle. Should the room carry on working?",
		Options: []clientdecisions.Option{
			{ID: "continue", Label: "Continue: open work remains and the lead should assign the next item", Recommended: recommended == "continue"},
			{ID: "stop", Label: "Stop: the work is finished or waits on the owner, so start nothing new", Recommended: recommended == "stop"},
		}}
	pick, threshold, askErr := g.Ask(ctx, "room "+g.Room+" has gone idle", detail.String(), question)
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
	if ok, why := g.Wake(g.Room); !ok {
		g.Log("room idle, message to the lead held: " + why)
		return
	}
	g.Say(g.Room, g.Lead, message, false)
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
	// THE ROOT IS READ THROUGH THE ONE PARSER, so this loop's flag set does not name it: the root is
	// stripped wherever it sits and the runtime's spelling is refused before a pass runs.
	rootFlag, args, rootErr := clientenv.StateRootFromFlags(args)
	if rootErr != nil {
		fmt.Fprintf(stderr, "4genteam idle-run: %v\n", rootErr)
		return clientcmd.ExitUsage
	}
	defaultQuiet := 60
	if envQ := os.Getenv("AGENTHUB_IDLE_QUIET"); envQ != "" {
		if v, err := strconv.Atoi(envQ); err == nil && v > 0 {
			defaultQuiet = v
		}
	}
	defaultStall := 120
	if envS := os.Getenv("AGENTHUB_IDLE_STALL"); envS != "" {
		if v, err := strconv.Atoi(envS); err == nil && v > 0 {
			defaultStall = v
		}
	}
	flags := flag.NewFlagSet("idle-run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	room := flags.String("room", "", "the room to guard (required)")
	lead := flags.String("lead", "lead", "the seat told what Jev decided")
	quiet := flags.Int("quiet", defaultQuiet, "seconds every seat must be idle before Jev is asked")
	stall := flags.Int("stall", defaultStall, "seconds a working seat may log nothing before it is told to continue")
	every := flags.Int("every", 30, "seconds between passes")
	once := flags.Bool("once", false, "run one pass and exit")
	if err := flags.Parse(args); err != nil {
		return clientcmd.ExitUsage
	}
	if strings.TrimSpace(*room) == "" {
		fmt.Fprintf(stderr, "4genteam idle-run: --room is required - there is no default room. %s\n", idleRunUsage)
		return clientcmd.ExitUsage
	}
	root, err := clientenv.ResolveStateRoot(rootFlag)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam idle-run: %v\n", err)
		return clientcmd.ExitUsage
	}
	guard := NewIdleGuard(*room, *lead, *quiet, *stall, root, stdout)
	guard.Log(fmt.Sprintf("guarding %s: lead %s, quiet %ds, stall %ds", *room, *lead, *quiet, *stall))
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

// IdleLogPath is the guard's log for one room.
func IdleLogPath(room, root string) string {
	return filepath.Join(LogDir(root), "idle-guard-"+room+".log")
}

// sessionCreated is when the seat's tmux session was created, from the runtime's own tmux server, or 0 when
// it cannot be read. The runtime makes a new session for every seat start (a reseat included).
func sessionCreated(stateRoot, room, seat string) float64 {
	cmd, err := clientenv.TmuxCommandAt(stateRoot, "display-message", "-p", "-t",
		clientruntime.SeatSessionName(room, seat), "#{session_created}")
	if err != nil {
		return 0
	}
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	created, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return created
}
