// The budget guard: a room's answer to a seat that costs more than the room agreed. It measures every running
// seat of one room against the room's own limits file and parks the seat that crosses a ceiling - StopSeat, a
// park record in the seat's own directory, and ONE row on the rail naming the seat and the number. The limits
// SHIP OFF, so the guard measures nothing until an owner turns them on with `4genteam room limits`.
package clientlifecycle

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/roombudget"
)

const (
	// budgetTicks is how many consecutive passes a RATE breach must hold before a seat is parked. The rate is
	// read over a minute, so one pass over the ceiling can be a healthy seat in a burst; two passes is half a
	// window of sustained hammering. The USD ceiling gets no such patience: that money is already spent.
	budgetTicks = 2
	// budgetRowTag is the rail tag a park carries. The store keeps a closed list of tags and this one is on
	// it; a park is a statement about the room's state, which is what the tag says.
	budgetRowTag   = "status"
	budgetRunUsage = "usage: 4genteam budget-run --room RIG [--lead SEAT] [--every SECONDS] [--once] [--state-root DIR]"
)

// BudgetGuard is one `budget-run`. Every reading, every stop and every write is a field, so one pass is
// testable with no rig, no socket, no transcript and no clock.
type BudgetGuard struct {
	Rig  string
	Lead string // the seat told, by its seat key: the rail's destination is that seat's session name

	Now    func() time.Time
	Seats  func(rig string) []string
	Limits func(rig string) (roombudget.Limits, error)
	Usage  func(rig, seat string) (roombudget.Usage, bool)
	// SeatDir is the seat's own directory, where its guard log and its park record live. Every path below
	// resolves through it, so one field moves the whole guard's files in a test.
	SeatDir func(rig, seat string) string
	Park    func(rig, seat string) error
	Record  func(rig, seat string, parked roombudget.Parked) error
	// Parked reads the record back. It is what makes a park idempotent: the record outlives this process, so a
	// guard that restarted must not stop and file the same seat again.
	Parked func(rig, seat string) (roombudget.Parked, bool)
	Row    func(rig string, row clientruntime.QueueCreateInput) error
	Log    func(text string)

	over     map[string]int
	reported string
}

// NewBudgetGuard is a guard wired to the real world: the room's limits file, each seat's own logs, the
// runtime's StopSeat, and the room's rail.
func NewBudgetGuard(rig, lead, stateRoot string, out io.Writer) *BudgetGuard {
	g := &BudgetGuard{Rig: rig, Lead: lead, over: map[string]int{}}
	g.Now = func() time.Time { return time.Now() }
	g.Seats = func(r string) []string {
		// The RUNNING seats, from the runtime that runs them: a seat the runtime does not list cannot be
		// spending, and one it lists as stopped cannot be stopped again. The list is what StopSeat accepts,
		// so measuring a seat the daemon does not know would end in a refusal on every pass.
		statuses, err := clientruntime.NewClient("").GetRoomStatus(r)
		if err != nil {
			g.Log("the room's seats could not be listed, nothing is measured: " + err.Error())
			return nil
		}
		var running []string
		for _, status := range statuses {
			if strings.EqualFold(strings.TrimSpace(status.State), "running") {
				running = append(running, status.Seat)
			}
		}
		return running
	}
	g.Limits = func(r string) (roombudget.Limits, error) {
		return roombudget.Load(filepath.Join(stateRoot, "rooms", r))
	}
	g.SeatDir = func(r, seat string) string { return clientruntime.SeatDir(stateRoot, r, seat) }
	g.Usage = func(r, seat string) (roombudget.Usage, bool) {
		dir := g.SeatDir(r, seat)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return roombudget.Usage{}, false
		}
		return roombudget.Measure(dir, g.Now()), true
	}
	g.Park = func(r, seat string) error { return clientruntime.NewClient("").StopSeat(r, seat) }
	g.Record = func(r, seat string, parked roombudget.Parked) error {
		return roombudget.WriteParked(g.SeatDir(r, seat), parked)
	}
	g.Parked = func(r, seat string) (roombudget.Parked, bool) {
		return roombudget.ReadParked(g.SeatDir(r, seat))
	}
	g.Row = func(r string, row clientruntime.QueueCreateInput) error {
		_, err := clientruntime.NewClient("").QueueCreate(r, row)
		return err
	}
	g.Log = func(text string) { fmt.Fprintf(out, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), text) }
	return g
}

// Step is one pass: read the limits, measure each running seat, park what crossed a ceiling.
func (g *BudgetGuard) Step(ctx context.Context) {
	_ = ctx
	limits, err := g.Limits(g.Rig)
	if err != nil {
		g.Log("the room's limits could not be read, nothing is measured: " + err.Error())
		g.beat()
		return
	}
	// The state of the limits is logged when it CHANGES and not once a pass: a guard that printed its own
	// inertness every fifteen seconds would bury the one line that says a seat was parked.
	if state := g.limitsState(limits); state != g.reported {
		g.reported = state
		g.Log(state)
	}
	if !limits.Enabled {
		g.beat()
		return
	}
	for _, seat := range g.Seats(g.Rig) {
		usage, ok := g.Usage(g.Rig, seat)
		if !ok {
			continue
		}
		kind, reason := roombudget.Breach(limits, usage)
		switch {
		case kind == "":
			delete(g.over, seat)
		case kind == roombudget.KindRate:
			g.over[seat]++
			if g.over[seat] >= budgetTicks {
				g.park(seat, usage, reason)
			}
		default:
			g.park(seat, usage, reason)
		}
	}
	g.beat()
}

// park records the park, stops the seat and files the row that names it. THE RECORD IS THE PARK: the client's
// routes refuse a parked seat work by reading that file (seatpark's gate, and the row a client prints), so the
// record is written FIRST and the seat is only stopped once it exists. The order is the contract, both ways:
//
//   - A record that could not be written leaves the seat RUNNING, with no row, and the next pass tries again.
//     Stopping it anyway would leave a seat no route refuses and nobody can explain, and - the reason this is
//     the first step rather than the second - a recorded park is what a restart and every later pass read back.
//   - A record that landed IS a park, whatever the stop does next: a refused StopSeat is logged and the row is
//     still filed, because the park is already real: the seat is refused work either way.
//
// A seat that already carries a record is left alone. The record outlives this process, so a guard that
// restarted must not stop and file the same seat again on its first pass.
func (g *BudgetGuard) park(seat string, usage roombudget.Usage, reason string) {
	if _, ok := g.Parked(g.Rig, seat); ok {
		delete(g.over, seat)
		return
	}
	parked := roombudget.Parked{Seat: seat, Reason: reason, At: g.Now().UTC().Format(time.RFC3339),
		Cost: usage.Cost, Calls: usage.Calls}
	if err := g.Record(g.Rig, seat, parked); err != nil {
		g.Log(fmt.Sprintf("%s is over its ceiling (%s) and its park record could not be written, so it was NOT "+
			"stopped - the record is the park, and the next pass tries again: %v", seat, reason, err))
		return
	}
	delete(g.over, seat)
	if err := g.Park(g.Rig, seat); err != nil {
		g.Log(fmt.Sprintf("%s: parked and recorded, and the stop was refused (%v); the record refuses it work "+
			"until it is removed", seat, err))
	}
	if err := g.Row(g.Rig, g.row(seat, usage, reason)); err != nil {
		g.Log(fmt.Sprintf("%s: parked and recorded, and the rail row was refused: %v", seat, err))
		return
	}
	g.Log(fmt.Sprintf("%s: PARKED (%s)", seat, reason))
}

// row is the ONE row a park files: the seat and the number, to the lead, with the ceiling it crossed and how
// to lift it. The rail's own caps bound the two strings (summary 120, body 600), so they stay short enough to
// be read in a list.
func (g *BudgetGuard) row(seat string, usage roombudget.Usage, reason string) clientruntime.QueueCreateInput {
	return clientruntime.QueueCreateInput{
		SourceSession:      "budget@" + g.Rig,
		DestinationSession: clientruntime.SeatSessionName(g.Rig, g.Lead),
		Summary:            fmt.Sprintf("BUDGET: seat %s parked - %s", seat, reason),
		Body: fmt.Sprintf("The budget safeguard parked seat %s: it %s.\nMeasured %s: %.2f USD over 24h, %d tool calls in the last minute.\n"+
			"Its park record is %s, and the record IS the park: the client's routes that would hand the seat work read it "+
			"and refuse (send, the restore brief, a revive), so a bare `4genteam room reseat %s %s` restarts it while the "+
			"record still refuses it work. Remove the record once the cause is understood; `4genteam room limits %s` changes "+
			"the ceiling.",
			seat, reason, g.Now().UTC().Format(time.RFC3339), usage.Cost, usage.Calls,
			roombudget.ParkedPath(g.SeatDir(g.Rig, seat)), g.Rig, seat, g.Rig),
		Tags: []string{budgetRowTag},
	}
}

// beat is the safeguard's heartbeat, the same one idle-run writes: agent reads it to tell a guard that is
// working from a guard that died.
func (g *BudgetGuard) beat() {
	if err := clientservices.WriteHeartbeat(clientservices.Budget, g.Rig); err != nil {
		g.Log("cannot write the heartbeat: " + err.Error())
	}
}

// limitsState is the one line an operator reads to know what the guard is doing, and it is a sentence rather
// than a rendering because it is logged only when it changes.
func (g *BudgetGuard) limitsState(limits roombudget.Limits) string {
	if !limits.Enabled {
		return "limits are OFF: nothing is measured (turn them on with `4genteam room limits " + g.Rig + " --on`)"
	}
	return fmt.Sprintf("limits are ON: %.2f USD/24h and %d calls/min per seat", limits.USDPerSeat24h, limits.CallsPerSeatMin)
}

type budgetRunCommand struct{}

func (budgetRunCommand) Name() string { return "budget-run" }
func (budgetRunCommand) Summary() string {
	return "the room's budget guard loop: park a seat that crosses the room's own spend or call ceiling"
}

func (budgetRunCommand) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, budgetRunUsage,
			"Measures every running seat of a room against the room's limits file (rooms/<room>/limits.json, written by\n"+
				"`4genteam room limits`) and parks the seat that crosses a ceiling: StopSeat, a record in the seat's own\n"+
				"directory, and one row on the rail naming the seat and the number. The USD ceiling is checked over 24h and\n"+
				"the call ceiling per minute, held for two passes. Limits SHIP OFF: nothing is measured until they are on.")
		return clientcmd.ExitOK
	}
	flags := flag.NewFlagSet("budget-run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	rig := flags.String("room", "", "the rig to guard (required)")
	lead := flags.String("lead", "lead", "the seat told when a seat is parked")
	every := flags.Int("every", int(clientservices.BudgetEvery/time.Second), "seconds between passes")
	once := flags.Bool("once", false, "run one pass and exit")
	stateRoot := flags.String("state-root", "", "where the client keeps its state: the flag, else ~/.config/4genthub")
	if err := flags.Parse(args); err != nil {
		return clientcmd.ExitUsage
	}
	if strings.TrimSpace(*rig) == "" {
		fmt.Fprintf(stderr, "4genteam budget-run: --room is required - there is no default rig. %s\n", budgetRunUsage)
		return clientcmd.ExitUsage
	}
	root, err := clientenv.ResolveStateRoot(*stateRoot)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam budget-run: %v\n", err)
		return clientcmd.ExitUsage
	}
	guard := NewBudgetGuard(*rig, *lead, root, stdout)
	guard.Log(fmt.Sprintf("guarding %s's budget: lead %s, every %ds", *rig, *lead, *every))
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

// BudgetLogPath is the guard's log for one rig.
func BudgetLogPath(rig string) (string, error) {
	dir, err := LogDir()
	if err != nil {
		return "", err
	}
	return dir + "/budget-guard-" + rig + ".log", nil
}
