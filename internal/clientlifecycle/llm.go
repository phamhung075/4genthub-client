// The model-egress row and the one place a listener is started: `up` runs it before the room's seats, so
// no seat's first request leaves before there is something to redact it, and `agent` runs it on its pass,
// so a listener that died is back on the same fixed port - the port `.env` names - without `up` again.
package clientlifecycle

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientllmproxy"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/clientteam"
)

// The seams the tests replace, for the same reason clientservices.Live is one: a listener is a process on
// a fixed port, and no unit test may bind 7391 or read this machine's live listeners. llmMissing is
// doctor's verdict, llmWait is the start grace, and llmEnsureEnv is the `.env` writer. The listener set is a
// parameter rather than a global: only the routes a room's seats use are started, so a route no seat uses
// can never refuse `up`.
var (
	llmListeners = clientllmproxy.ListenersFor
	llmListening = clientllmproxy.Listening
	llmMissing   = clientllmproxy.MissingListenersFor
	llmWait      = clientllmproxy.WaitListening
	llmEnsureEnv = clientllmproxy.EnsureEnv
)

const (
	// llmStartGrace is how long `up` waits for a listener it just started to answer. The verb binds
	// before it prints, so this covers process start rather than a slow bind; a refusal after it names
	// the port and the log, which is 7.5's shape for a safeguard that did not come up.
	llmStartGrace = 5 * time.Second
	// llmCheckEvery is how often agent re-asks whether the listeners answer. A pass a second would spawn
	// against a port that is merely slow to answer; the interval is short enough that a listener which
	// died is back before a seat notices more than one refused call - and a refused call fails closed.
	llmCheckEvery = 10 * time.Second
)

// llmRow is doctor's row about model egress, with the detail line the decision names beneath it.
type llmRow struct {
	Row clientservices.Health
	// Detail is clientllmproxy.Report: the route counts and today's withheld count, rendered by the
	// package that owns the table so a row and the table it describes cannot drift apart.
	Detail string
	// Missing names the listeners that answer nothing, in table order; each is a reason line, so a red
	// row says WHICH port is unserved rather than only that model egress is down.
	Missing []string
}

// llmRowsFor is the row as `doctor` prints it - a seam, like every other machine row's, so a test decides
// whether the listeners answer. llmRows is the computation: one row, machine-scoped, `running` only while
// every listener a route THIS ROOM's seats use answers. A listener no seat's runtime calls is not asked
// about, so a route this room does not use cannot turn the row red. It carries no age - a listener writes no
// beat file, and 7.4's silence rule measures a heartbeat - so its age prints `unknown` and its verdict is the
// probe.
var llmRowsFor = llmRows

func llmRows(room, root string) []llmRow {
	missing := llmMissing(llmRoomRoutes(root, room))
	state := clientservices.Running
	if len(missing) > 0 {
		state = clientservices.Stopped
	}
	return []llmRow{{
		Row:     clientservices.Health{Safeguard: clientservices.Llm, State: state, AgeS: clientservices.UnknownAge},
		Detail:  clientllmproxy.Report(root),
		Missing: missing,
	}}
}

// llmRoutes is the routes a room's seats actually use: every seat's runtime mapped through the table's own
// figure (clientllmproxy.RoutesFor) and unioned in spec order. It is the ONE decision the start path and the
// row share, so the listeners started and the listeners the row waits on cannot drift apart - a route no seat
// uses is neither started nor waited on.
func llmRoutes(spec clientruntime.RoomSpec) []string {
	seen := map[string]bool{}
	var routes []string
	for _, seat := range spec.Seats {
		for _, route := range clientllmproxy.RoutesFor(seat.Runtime) {
			if seen[route] {
				continue
			}
			seen[route] = true
			routes = append(routes, route)
		}
	}
	return routes
}

// llmRoomRoutes is llmRoutes for a caller that holds only the room's name - agent's pass and doctor's row. It
// reads the room's OWN record, the one its start writes, and a record it cannot read keeps EVERY route: this
// figure decides that a listener may be left unstarted, so an unknown room starts what it always did rather
// than being narrowed by a guess.
func llmRoomRoutes(root, room string) []string {
	spec, err := clientteam.LoadLiveRoomSpec(root, room)
	if err != nil {
		return clientllmproxy.Routes()
	}
	return llmRoutes(spec)
}

// llmLines is the row's reasons, detail line first: the route counts, then each unserved port by name.
func llmLines(row llmRow) []string {
	lines := []string{row.Detail}
	for _, name := range row.Missing {
		lines = append(lines, fmt.Sprintf("llm: %s: nothing answers on its port, so the routes on that surface fail closed", name))
	}
	return lines
}

// llmListenerLogPath is where a listener's own output goes: <root>/logs/llmproxy-<route>.log, one file per
// listener, named by the route that starts it.
func llmListenerLogPath(root string, listener clientllmproxy.Listener) string {
	return filepath.Join(clientservices.LogDir(root), "llmproxy-"+strings.ToLower(listener.Route)+".log")
}

// llmListenerArgv is the command line `up` and agent start a listener with. The port is named explicitly
// rather than left to the verb's default: the redirect line names that same port, and the two are read
// from one table only if the argv carries it.
func llmListenerArgv(root string, listener clientllmproxy.Listener) []string {
	return []string{"llmproxy", "--runtime", listener.Route, "--listen", listener.Addr(), "--state-root", root}
}

// ensureLlmListeners is the one start path for model egress: write the redirect lines into the state
// root's `.env`, then for each listener of a route the room's seats use that does not answer, start it and
// wait for its own answer. A listener already answering - the other room's `up`, or agent's own earlier
// start - is REUSED, which is the decision's reuse rule and the reason a taken port is not fought over.
//
// A route NO seat uses gets no listener and therefore refuses nothing: `up` for a room whose seats all run one
// runtime starts that runtime's route alone, so another runtime's provider configuration - codex's
// `~/.codex/config.toml` selecting no provider, for one - cannot block the room.
//
// The `.env` is written even when every listener answers: a seat may have been started before a line was
// added, and the file is the only surface a running supervisor reads per start.
func ensureLlmListeners(root string, routes []string) error {
	if _, err := llmEnsureEnv(root); err != nil {
		return fmt.Errorf("the redirect lines in %s were not written: %w", filepath.Join(root, ".env"), err)
	}
	for _, listener := range llmListeners(routes) {
		if llmListening(listener) {
			continue
		}
		logPath := llmListenerLogPath(root, listener)
		if _, err := spawnChild(llmListenerArgv(root, listener), logPath); err != nil {
			return fmt.Errorf("the %s listener was not started: %w (log %s)", listener, err, logPath)
		}
		if !llmWait(listener, llmStartGrace) {
			return fmt.Errorf("the %s listener answers nothing on %s after %s, so the routes on it fail closed (log %s)",
				listener, listener.Addr(), llmStartGrace, logPath)
		}
	}
	return nil
}

// ensureLlm is agent's turn at the same start path, throttled: the pass runs every second and the probe
// every llmCheckEvery, so a crash loop is restarted on a bounded schedule rather than once a second. The
// routes are the union of every room agent supervises (llmRoutesOf): a listener serves a port, not a room.
func (m *agentManager) ensureLlm(now time.Time, routes []string) {
	if !m.lastLlm.IsZero() && now.Sub(m.lastLlm) < llmCheckEvery {
		return
	}
	m.lastLlm = now
	if err := ensureLlmListeners(m.root, routes); err != nil {
		m.log("llm: " + err.Error())
	}
}

// llmRoutesOf unions the routes of every room agent supervises, so each listener a supervised room uses is
// started and restarted on its own. A room whose record cannot be read keeps every route (llmRoomRoutes'
// fallback), which is the safe direction: a listener started for nothing costs a probe, one left unstarted
// costs every seat's model call on that route.
func (m *agentManager) llmRoutesOf(rooms []string) []string {
	seen := map[string]bool{}
	var routes []string
	for _, room := range rooms {
		for _, route := range llmRoomRoutes(m.root, room) {
			if seen[route] {
				continue
			}
			seen[route] = true
			routes = append(routes, route)
		}
	}
	return routes
}
