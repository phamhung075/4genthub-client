// The redirect half of the decision: the `.env` lines that point a runtime at this listener, and the
// loopback listeners themselves. A runtime is redirected by ONE variable or file, so the writer here is
// keyed by SURFACE: one line and one port per base-URL variable, however many routes share it. codex is
// absent from the writer on purpose - its surface is `~/.codex/config.toml`, which codex.go owns - but
// its listener is started like any other, because the profile that selects its provider entry needs an
// address that already answers.
package clientllmproxy

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// upstreamPrefix is the `.env` name that carries the provider origin a redirect replaced:
// AGENTHUB_LLM_UPSTREAM_CLAUDE_CODE and AGENTHUB_LLM_UPSTREAM_OMP_ANTHROPIC both hold the origin
// ANTHROPIC_BASE_URL named before this client pointed it at the listener.
const upstreamPrefix = "AGENTHUB_LLM_UPSTREAM_"

// Listener is one loopback listener this client must have running: the surface whose traffic it redacts,
// the fixed port that surface names, and the route key the verb is started with. A listener whose
// Surface is empty is codex's - a config-file surface with no `.env` line.
type Listener struct {
	Surface string
	Port    int
	Route   string
}

// Addr is the loopback address the listener binds, and the address the redirect line names.
func (l Listener) Addr() string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(l.Port)) }

// BaseURL is the value the redirect line holds: loopback only, never a provider.
func (l Listener) BaseURL() string { return "http://" + l.Addr() }

// Name is the listener as a report or a refusal names it: its variable, or the route when its surface is
// a file.
func (l Listener) Name() string {
	if l.Surface != "" {
		return l.Surface
	}
	return l.Route
}

// Routes names every route key this client proxies, in table order. It is what a caller asks for when it
// cannot narrow the set - the start path's own fallback for a room whose record cannot be read.
func Routes() []string {
	names := make([]string, 0, len(routeTable))
	for _, row := range routeTable {
		names = append(names, row.Name)
	}
	return names
}

// RoutesFor names the routes a seat of one runtime sends its model requests through. It is the ONE place a
// runtime name meets the route table, so a listener is started for a runtime a room actually seats rather
// than for every route the table carries.
//
// The spellings this figure narrows on are the ones the runtime itself accepts - `AgentRuntime::from_str_loose`
// in the runtime's types.rs, which is why `claude` answers claude-code's route and not every route. A spelling
// outside that set answers EVERY route, `terminal` included: a room whose spec carries a runtime the runtime
// does not know keeps the whole set rather than being narrowed to nothing by a guess, because a start that
// cannot rule a route out must not leave it unserved and a seat pointed at a closed port fails closed.
// `claude-code`, `omp` and `codex` are proxied; `agy` alone answers NO route, the probe having found no surface.
func RoutesFor(runtime string) []string {
	switch strings.ToLower(strings.TrimSpace(runtime)) {
	case "claude-code", "claude":
		return []string{"claude-code"}
	case "omp":
		return []string{"omp-anthropic"}
	case "codex":
		return []string{"codex"}
	case "agy":
		return nil
	default:
		return Routes()
	}
}

// ListenersFor is one entry per DISTINCT listener among routes, in table order. Routes sharing a base-URL
// variable share an entry, because one variable holds one address and therefore one port - that is what makes
// the line written once and the reuse decision key on the variable rather than on the route key. codex gets
// its own entry: distinct surface, distinct port. A route no seat uses is not in `routes` and starts nothing.
func ListenersFor(routes []string) []Listener {
	wanted := map[string]bool{}
	for _, route := range routes {
		wanted[route] = true
	}
	var listeners []Listener
	seen := map[string]bool{}
	for _, row := range routeTable {
		if !wanted[row.Name] {
			continue
		}
		key := row.EnvVar
		if key == "" {
			key = "file:" + row.Name
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		listeners = append(listeners, Listener{Surface: row.EnvVar, Port: row.Port, Route: row.Name})
	}
	return listeners
}

// UpstreamKey is the `.env` name carrying the origin one route's redirect replaced.
func UpstreamKey(route string) string {
	return upstreamPrefix + strings.ToUpper(strings.ReplaceAll(route, "-", "_"))
}

// EnsureEnv writes the redirect into <stateRoot>/.env and answers the names it wrote, in the order it
// wrote them. A variable already holding this listener's address is left untouched, so a second run
// writes nothing; a variable holding anything else is redirected, and the address it held is kept as
// that route's upstream so the listener knows where to forward. Everything else in the file - other
// variables, comments, the mode - is carried through as it was, because this file holds credentials the
// client has no business rewriting.
func EnsureEnv(stateRoot string) ([]string, error) {
	envPath := filepath.Join(stateRoot, ".env")
	entries, err := readEnvEntries(envPath)
	if err != nil {
		return nil, err
	}
	// First occurrence wins, the rule clientenv.Load applies to the file this process then runs with.
	current := map[string]string{}
	for _, entry := range entries {
		name, value, _ := strings.Cut(entry, "=")
		if _, seen := current[name]; !seen {
			current[name] = value
		}
	}

	// The upstream lines are written BEFORE the base URL is rewritten, so "the URL it replaced" is read
	// from the file as it still is: a second run finds the key present and keeps the first origin.
	var written []string
	for _, row := range routeTable {
		if row.EnvVar == "" {
			continue
		}
		key := UpstreamKey(row.Name)
		if _, present := current[key]; present {
			continue
		}
		upstream := current[row.EnvVar]
		if upstream == "" || upstream == listenerBaseURL(row) {
			upstream = row.Upstream
		}
		if err := clientenv.Set(envPath, key, upstream); err != nil {
			return written, err
		}
		current[key] = upstream
		written = append(written, key)
	}
	for _, listener := range ListenersFor(Routes()) {
		if listener.Surface == "" || current[listener.Surface] == listener.BaseURL() {
			continue
		}
		if err := clientenv.Set(envPath, listener.Surface, listener.BaseURL()); err != nil {
			return written, err
		}
		current[listener.Surface] = listener.BaseURL()
		written = append(written, listener.Surface)
	}
	return written, nil
}

// listenerBaseURL is the redirect value a table row's own port implies, for the staleness check above.
func listenerBaseURL(row routeRow) string {
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(row.Port))
}

// Listening reports whether a listener answers on its own port, answering as a route the SAME listener
// serves: a process serving /__llmproxy for another surface is a stranger on this port, not a pass.
func Listening(listener Listener) bool {
	found, answered := probeListener("127.0.0.1", strconv.Itoa(listener.Port))
	if !answered {
		return false
	}
	if listener.Surface == "" {
		return found == listener.Route
	}
	return envVarOf(found) == listener.Surface
}

// MissingListenersFor names, in table order, the listeners of routes that do not answer. An empty answer is
// the row's only passing verdict; every name here is a port the redirect lines point at with nothing serving
// it. A route no seat uses is not asked about, so an unstarted listener nothing needs never turns the row red.
func MissingListenersFor(routes []string) []string {
	var missing []string
	for _, listener := range ListenersFor(routes) {
		if !Listening(listener) {
			missing = append(missing, listener.Name())
		}
	}
	return missing
}

// WaitListening polls a listener until it answers or the grace elapses, and answers whether it did. The
// caller has just started it, so the only question is whether this run's bind succeeded: the verb exits
// when a stranger holds the port, and this is what turns that into a named refusal.
func WaitListening(listener Listener, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for {
		if Listening(listener) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// String is the listener as a log line names it.
func (l Listener) String() string {
	return fmt.Sprintf("%s on %s", l.Name(), l.Addr())
}
