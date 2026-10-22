// The `4genteam llmproxy` verb: the loopback listener a route's model requests go through. It owns flag
// parsing, the fixed-port/reuse decision and the server's lifetime; the scanning and forwarding itself
// is proxy.go and the codex config-file surface is codex.go, so the rules that decide what leaves this
// machine are testable without a verb.
package clientllmproxy

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

const llmproxyUsage = `usage: 4genteam llmproxy [--runtime ROUTE] [--upstream URL] [--listen 127.0.0.1:PORT] [--state-root DIR] [--json]

One loopback listener per redirected VARIABLE (or, for codex, per config-file surface): every model
request body is scanned, its known secret values become [REDACTED:<VARIABLE NAME>], and a body the scan
cannot vouch for is REFUSED (400 {"error":"redaction_withheld","reason":"...","log":"<path>"}) and
recorded under <state-root>/llm-withheld/<route>/ with the body withheld. A refused request forwards
nothing.

--runtime takes a provider ROUTE, because the probe measured one CLI answering differently on two routes.
The routes with a verified surface, and the fixed port each listener uses:
  claude-code    ANTHROPIC_BASE_URL   127.0.0.1:7391
  omp-anthropic  ANTHROPIC_BASE_URL   127.0.0.1:7391   (same variable: ONE listener and one .env line)
  codex          ~/.codex/config.toml 127.0.0.1:7392   proxied by file, inert until selected
                 model_providers.<id>.base_url         (the verb writes [model_providers.agenthub-egress]
                                                       and the [profiles.agenthub-egress] profile that
                                                       selects it; the global model_provider is untouched)

The port belongs to the VARIABLE, not the route: ANTHROPIC_BASE_URL can hold one address, so both routes
on it share the listener and the .env line. Unproxied, with no row and no port: omp-deepseek
(OPENAI_BASE_URL was measured reaching no listener) and agy (its binary names no base-URL variable).
Until a seat runs under codex's profile, codex is unproxied (provider entry present, not selected).

The listener binds 127.0.0.1 only. A port already serving GET /__llmproxy for the SAME VARIABLE is this
client's own listener and is reused (exit 0) - even when the route key differs, because one variable is
one listener. Any other binder refuses, and the port is never re-picked: the state-root .env names this
port for every seat, so moving it would make that file a lie.

--upstream is the URL the route would otherwise call. Absent, the route's verified provider origin from
the table is used, and each request keeps its own path and query. The body bound is 32 MiB; a larger body
is refused too_large. Response bodies, including SSE streams, pass through untouched.

--json prints {"listen":"http://127.0.0.1:<port>","runtime":"..."} on stdout once the listener is ready,
and nothing else. The field keeps the name runtime; its value is the route key that was asked for.
`

// Commands is what the dispatcher registers from this package: one top-level verb.
func Commands() []clientcmd.Command { return []clientcmd.Command{llmproxyCommand{}} }

type llmproxyCommand struct{}

func (llmproxyCommand) Name() string { return "llmproxy" }
func (llmproxyCommand) Summary() string {
	return "the loopback listener one route's model requests go through, redacted before they leave"
}

func (llmproxyCommand) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		_, _ = io.WriteString(stdout, llmproxyUsage)
		return clientcmd.ExitOK
	}
	flags := flag.NewFlagSet("llmproxy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runtimeName := flags.String("runtime", routeTable[0].Name,
		"the provider route whose model requests this listener fronts (one of: "+routeNames()+")")
	upstreamFlag := flags.String("upstream", "",
		"the URL the route would otherwise call (default: the route's verified provider origin)")
	listenFlag := flags.String("listen", "", "the loopback address to bind (default: 127.0.0.1:<the route's port>)")
	stateRootFlag := flags.String("state-root", "", "the client's state root (default: this machine's)")
	asJSON := flags.Bool("json", false, "print the listener's address as JSON, for a supervisor to parse")
	if err := flags.Parse(args); err != nil {
		return clientcmd.ExitUsage
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "4genteam llmproxy: unexpected argument %q\n%s\n", flags.Arg(0), llmproxyUsage)
		return clientcmd.ExitUsage
	}

	row, ok := lookupRoute(*runtimeName)
	if !ok {
		fmt.Fprintf(stderr, "4genteam llmproxy: %q is not a route this listener carries; the routes are "+
			"%s.\nThe unproxied routes are %s: omp-deepseek's provider ignores OPENAI_BASE_URL (zero listener\n"+
			"hits), and agy's binary names no base-URL variable. codex is proxied by file, inert until selected:\n"+
			"its surface is model_providers.<id>.base_url in ~/.codex/config.toml, which this verb writes\n"+
			"under --runtime codex. A route is a row only after its probe\n"+
			"(PROBE-2026-10-21-llm-egress.md).\n",
			*runtimeName, routeNames(), strings.Join(unproxiedRouteNames, ", "))
		return clientcmd.ExitUsage
	}
	upstream, err := targetUpstream(*upstreamFlag, row)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam llmproxy: %v\n", err)
		return clientcmd.ExitUsage
	}

	addr := *listenFlag
	if addr == "" {
		addr = fmt.Sprintf("127.0.0.1:%d", row.Port)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam llmproxy: --listen %q is not HOST:PORT: %v\n", addr, err)
		return clientcmd.ExitUsage
	}
	if !loopbackHost(host) {
		fmt.Fprintf(stderr, "4genteam llmproxy: --listen %q is not a loopback address; this listener binds\n"+
			"127.0.0.1 only, because a seat's requests are the only traffic it should ever see\n", addr)
		return clientcmd.ExitUsage
	}

	listener, listenErr := net.Listen("tcp", addr)
	if listenErr != nil {
		return reuseOrRefuse(host, port, addr, row, listenErr, *asJSON, stdout, stderr)
	}
	// The file-surface route is written only now: the listener is bound, so the config cannot name an
	// address nothing serves. A failure here closes the socket and refuses; a half-written codex config
	// is a reason not to claim the listener is ready.
	if code := ensureSurface(row, listener.Addr().String(), stderr); code != clientcmd.ExitOK {
		_ = listener.Close()
		return code
	}
	// The state root is resolved only once this process really is the listener: a start that reuses an
	// existing listener needs no record directory, and must not refuse for a reason that does not apply.
	stateRoot, err := clientenv.ResolveStateRoot(*stateRootFlag)
	if err != nil {
		_ = listener.Close()
		fmt.Fprintf(stderr, "4genteam llmproxy: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitUsage)
	}
	return serve(ctx, listener, newProxy(row, upstream, stateRoot, os.Environ, stderr), row, *asJSON, stdout, stderr)
}

// reuseOrRefuse is the "port already bound" branch, and it is where the reuse exception lives: only a
// port whose health path answers for the SAME SURFACE is this client's own listener. The surface is the
// base-URL VARIABLE, not the route key, so claude-code and omp-anthropic - one ANTHROPIC_BASE_URL, one
// port - reuse each other's listener instead of fighting over it. Anything else - a stranger, or our
// health path answering for a route on another surface - is a refusal that names the port, because a
// silent re-pick would leave the .env pointing at nothing.
func reuseOrRefuse(host, port, addr string, row routeRow, listenErr error, asJSON bool, stdout, stderr io.Writer) int {
	found, answered := probeListener(host, port)
	switch {
	case answered && sharesListener(found, row):
		// The config file is ensured on this path too: a restart of a file-surface route must still
		// leave codex pointed at the listener that is already serving.
		if code := ensureSurface(row, net.JoinHostPort(host, port), stderr); code != clientcmd.ExitOK {
			return code
		}
		if asJSON {
			return writeListenReport(stdout, net.JoinHostPort(host, port), row, stderr)
		}
		fmt.Fprintf(stdout, "4genteam llmproxy: route %s is already listening on http://%s for the same %s; "+
			"reused it\n", found, net.JoinHostPort(host, port), surfaceOf(row))
		return clientcmd.ExitOK
	case answered:
		fmt.Fprintf(stderr, "4genteam llmproxy: port %s is held by a listener for route %q, which does not "+
			"share %s's surface; the port is fixed so a seat's .env keeps naming the right one, and a port "+
			"this verb cannot use is a refusal rather than a silent re-pick\n", addr, found, surfaceOf(row))
		return clientcmd.ExitUnavailable
	}
	fmt.Fprintf(stderr, "4genteam llmproxy: cannot bind %s and no listener there answers %s, so the port is\n"+
		"held by something else: %v. The port is fixed for %s; nothing here re-picks it.\n",
		addr, healthPath, listenErr, surfaceOf(row))
	return clientcmd.ExitUnavailable
}

// surfaceOf names what a route is redirected by, for the messages that have to say which one a port
// belongs to.
func surfaceOf(row routeRow) string {
	if row.EnvVar != "" {
		return row.EnvVar
	}
	return "codex's config file"
}

// ensureSurface writes a file-surface route's pointer at this listener: codex's provider entry and the
// profile that selects it (codex.go). listenAddr is the address the listener REALLY serves - the bound
// address, not the row's default - so a --listen override cannot leave codex pointed at a port nothing
// answers. An environment-variable route needs nothing here: its pointer is the .env line its seats are
// handed.
func ensureSurface(row routeRow, listenAddr string, stderr io.Writer) int {
	if row.Status != statusProxiedByFile {
		return clientcmd.ExitOK
	}
	_, portText, err := net.SplitHostPort(listenAddr)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam llmproxy: %s has no port to write into codex's config: %v\n", listenAddr, err)
		return clientcmd.ExitUnavailable
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam llmproxy: %s has no port to write into codex's config: %v\n", listenAddr, err)
		return clientcmd.ExitUnavailable
	}
	path, err := codexConfigPath(os.Environ)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam llmproxy: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	changed, err := ensureCodexProvider(path, port)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam llmproxy: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	if changed {
		fmt.Fprintf(stderr, "4genteam llmproxy: wrote [model_providers.%s] with base_url http://%s "+
			"and the [profiles.%s] profile that selects it into %s; the global model_provider is untouched\n",
			codexProviderID, listenAddr, codexProfileName, path)
	}
	return clientcmd.ExitOK
}

// targetUpstream resolves where a request goes. With no --upstream the route's verified provider origin
// from the table is the target: that is the URL the route's surface replaced, and each request keeps its
// own path and query (NewSingleHostReverseProxy's join).
func targetUpstream(raw string, row routeRow) (*url.URL, error) {
	if raw == "" {
		return url.Parse(row.Upstream)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("--upstream %q is not a URL: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("--upstream %q is not an http or https URL", raw)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("--upstream %q names no host", raw)
	}
	return parsed, nil
}

// loopbackHost is the bind rule, checked on the address the operator asked for rather than assumed from
// the default: a listener on a routable address would accept requests from outside this machine.
func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// probeListener asks a bound port who it is. It is the only way the reuse exception can be honest: the
// health answer names the route, so "our own listener" is measured rather than hoped. A port that does
// not answer, or answers something else, is not ours.
func probeListener(host, port string) (runtime string, answered bool) {
	target := "http://" + net.JoinHostPort(host, port) + healthPath
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(target)
	if err != nil {
		return "", false
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", false
	}
	var answer struct {
		Runtime string `json:"runtime"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&answer); err != nil || answer.Runtime == "" {
		return "", false
	}
	return answer.Runtime, true
}

// listenReport is the --json shape, and its field order is the contract: a supervisor parses it, and the
// decision spells it exactly `{"listen":"http://127.0.0.1:<port>","runtime":"..."}`. The field keeps the
// name runtime; its value is the route key that was asked for, even when the listener was reused from
// the other route on the same variable.
type listenReport struct {
	Listen  string `json:"listen"`
	Runtime string `json:"runtime"`
}

// writeListenReport prints the ready report. It runs AFTER the listener is bound (or the reuse probe
// answered), so a caller that sees this line knows the address is already accepting connections.
func writeListenReport(stdout io.Writer, addr string, row routeRow, stderr io.Writer) int {
	report := listenReport{Listen: "http://" + addr, Runtime: row.Name}
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		fmt.Fprintf(stderr, "4genteam llmproxy: cannot print the listener report: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	return clientcmd.ExitOK
}

// serve runs the listener until the context ends or the process is signalled, then shuts it down. The
// report is printed before the wait, so its promise - the address is ready - is true when it is read.
func serve(ctx context.Context, listener net.Listener, handler http.Handler, row routeRow, asJSON bool, stdout, stderr io.Writer) int {
	server := &http.Server{Handler: handler}
	addr := listener.Addr().String()
	if asJSON {
		if code := writeListenReport(stdout, addr, row, stderr); code != clientcmd.ExitOK {
			_ = listener.Close()
			return code
		}
	} else {
		fmt.Fprintf(stdout, "4genteam llmproxy: route %s listening on http://%s (%s); requests are redacted "+
			"and a body the scan cannot vouch for is refused\n", row.Name, addr, surfaceOf(row))
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return clientcmd.ExitOK
		}
		fmt.Fprintf(stderr, "4genteam llmproxy: the listener on %s stopped: %v\n", addr, err)
		return clientcmd.ExitUnavailable
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		// Shutdown closes the listeners Serve registered; this close covers the window before Serve
		// starts, so a cancelled start never leaves a bound socket behind.
		_ = listener.Close()
		return clientcmd.ExitOK
	}
}
