// The idle guard's half of a seat's declared wait (internal/seatwait): each pass ends the waits whose end has
// come, wakes the seat when the end was the event or the expiry, and raises ONE owner alert when the whole room
// waits on the director or the owner.
package clientlifecycle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/seatwait"
)

func unixTime(seconds float64) time.Time {
	return time.Unix(0, int64(seconds*1e9))
}

// passWait runs one pass of the seat's wait: the record and true while it holds, and ended=true when this pass
// ended it. A wake held by the off-peak
// gate keeps the wait in place, so the seat is not nudged in the meantime and is woken once the gate opens.
func (g *IdleGuard) passWait(seat string, idle bool, logged, now float64, rows []clientruntime.QueueRow, rowsOK bool) (seatwait.Record, bool, bool) {
	if g.Root == "" {
		return seatwait.Record{}, false, false
	}
	rec, ok := seatwait.Read(g.Root, g.Room, seat)
	if !ok {
		return seatwait.Record{}, false, false
	}
	at := unixTime(now)
	out := seatwait.Evaluate(&rec, seatwait.Observation{Now: at, Idle: idle, LogMtime: logged, Rows: rows, RowsOK: rowsOK}, g.WaitProbes)
	if !out.End {
		if out.Dirty {
			if err := seatwait.Write(g.Root, rec); err != nil {
				g.Log(fmt.Sprintf("%s: cannot update its wait record: %v", seat, err))
			}
		}
		return rec, true, false
	}
	if out.Wake != "" {
		if ok, why := g.Wake(g.Room); !ok {
			if out.Dirty {
				_ = seatwait.Write(g.Root, rec)
			}
			g.Log(fmt.Sprintf("%s: wait on %s ended (%s), wake held: %s", seat, rec.On(), out.How, why))
			return rec, true, false
		}
		g.Say(g.Room, seat, out.Wake, false)
	}
	if err := seatwait.End(g.Root, rec, at, out.How, out.Reason); err != nil {
		g.Log(fmt.Sprintf("%s: cannot end its wait record: %v", seat, err))
	}
	g.Log(fmt.Sprintf("%s: wait on %s ended after %s (%s: %s)", seat, rec.On(), seatwait.Short(at.Sub(rec.Started)), out.How, out.Reason))
	return seatwait.Record{}, false, true
}

// waitEpisodePath remembers the all-waiting episode already escalated, so a restarted guard does not raise it again.
func waitEpisodePath(root, room string) string {
	return filepath.Join(root, "rooms", room, "wait-escalation.json")
}

// escalateWaits raises one owner-level alert when every seat of the room waits on the director or the owner
// (merge:, answer:), once per such episode. The lead counts as part of the episode when it is idle without a
// wait of its own: a lead has nothing to merge or answer either, and requiring it to declare a wait would hide
// the very episode the alert exists for.
func (g *IdleGuard) escalateWaits(counted int, idle map[string]bool, waits map[string]seatwait.Record, now float64) {
	if g.Root == "" || g.Escalate == nil || counted == 0 || len(waits) == 0 {
		return
	}
	path := waitEpisodePath(g.Root, g.Room)
	blocked := true
	var parts []string
	for seat, isIdle := range idle {
		wait, waiting := waits[seat]
		switch {
		case waiting && wait.OnDirector():
			parts = append(parts, seat+"="+wait.On())
		case !waiting && seat == g.Lead && isIdle:
		default:
			blocked = false
		}
	}
	if !blocked {
		_ = os.Remove(path)
		return
	}
	sort.Strings(parts)
	signature := strings.Join(parts, ",")
	var seen struct {
		Signature string `json:"signature"`
	}
	if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &seen) == nil && seen.Signature == signature {
		return
	}
	oldest := unixTime(now)
	for _, wait := range waits {
		if wait.Started.Before(oldest) {
			oldest = wait.Started
		}
	}
	summary := stuckClip("Room "+g.Room+" is blocked: every seat waits on the director/owner", 120)
	body := fmt.Sprintf("facts: %s\nrule: %s\ndecision: %s\nnext: %s",
		stuckClip("waits: "+strings.ReplaceAll(signature, ",", ", ")+"; oldest for "+seatwait.Short(unixTime(now).Sub(oldest)), 280),
		stuckClip("a room whose every seat waits on merge: or answer: does nothing until they are released", 110),
		stuckClip("one alert per episode; the seats are not nudged while they wait", 100),
		stuckClip("merge or answer the named targets, or hand the room new work", 78))
	if err := g.Escalate(g.Room, summary, body); err != nil {
		g.Log("every seat waits on the director/owner, the owner alert failed: " + err.Error())
		return
	}
	raw, _ := json.Marshal(map[string]string{"signature": signature})
	_ = os.WriteFile(path, raw, 0o600)
	_ = seatwait.AppendLog(g.Root, seatwait.Event{Ts: unixTime(now).UTC().Format(time.RFC3339), Event: "escalate",
		Room: g.Room, Reason: signature})
	g.Log("every seat waits on the director/owner: owner alerted (" + signature + ")")
}

// escalateToOwner is the owner alert path the stuck scan already uses: a rail row to the room's human, which
// the bridge delivers to the cloud as a notification.
func escalateToOwner(room, summary, body string) error {
	priority := "normal"
	_, err := clientruntime.NewClient("").QueueCreate(room, clientruntime.QueueCreateInput{
		SourceSession:      stuckSourceSession(room),
		DestinationSession: stuckOwnerPrefix + room,
		Summary:            summary,
		Body:               body,
		Priority:           &priority,
		Tags:               []string{stuckEscalationTag},
	})
	return err
}
