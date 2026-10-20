// Package clientfeedback is the `4genteam feedback` verb: the submission path for a seat whose
// runtime has no MCP.
//
// IT IS ONE WRITER CONTRACT WITH THE MCP TOOL. The route posted to here is the same one the MCP tool
// `submit_feedback` calls, and the JSON body is the same five fields, so the friction channel has a
// single shape no matter which door a seat walked through. This package is a plain HTTP client: it
// never touches storage or the service directly.
//
// IT IS NATIVE GO, NOT A SCRIPT. The Python client shipped `seat_feedback.sh`, which reached the
// route with curl and borrowed python3 for the JSON. A compiled client cannot rely on either being
// present, so the whole wire contract is reimplemented here: the flags are parsed in-process, the body
// is marshalled by encoding/json, and the request is sent with net/http. No `sh -c`, no `python3`, no
// os/exec of a helper.
//
// PORTED FROM agenthub_client/src/agenthub_client/seat_feedback.sh. Two deliberate deviations, both
// named here rather than hidden:
//
//   - The script's URL fallback chain was $AGENTHUB_MCP_URL (minus its /mcp suffix), then
//     $AGENTHUB_PUBLIC_URL. Neither is this client's contract; the client's ONE server variable is
//     $AGENTHUB_URL (see .env.sample and internal/clientenv), so that is the default and `--url` is
//     the override.
//   - The script accepted exactly HTTP 200 as stored. This client accepts any 2xx, which is the
//     acceptance rule for the port.
package clientfeedback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// feedbackPath is the ONE route the friction channel writes to: the same path the MCP tool
// `submit_feedback` posts to.
const feedbackPath = "/api/v2/feedback"

// feedbackLayers is the closed vocabulary the server accepts. It is checked BEFORE any request is
// made, so a wrong layer costs no dial and no credential.
var feedbackLayers = []string{"runtime", "client", "cloud", "seat-context", "workspace", "other"}

// httpTimeout is the client's HTTP convention (the same 60s internal/clientsync gives RequestJSON),
// named so a reader can see it is a constant rather than a guess.
const httpTimeout = 60 * time.Second

// feedbackClient is the seam the push path is pinned through: a test replaces it with a client whose
// Transport answers in-process, so the reason the transport gave is asserted verbatim with NO network.
// This is a push path, and the suite cannot run it against a cloud.
var feedbackClient = &http.Client{Timeout: httpTimeout}

// feedbackUsage is what `4genteam feedback --help` prints. It names the verb, as the Python door's
// help test requires.
const feedbackUsage = `usage: 4genteam feedback --layer <layer> --text <text> [--room R] [--seat S] [--session X] [--url BASE]

Report friction from a seat whose runtime has no MCP: one HTTP POST to the feedback route,
the same payload the MCP tool ` + "`submit_feedback`" + ` sends. The owner reads the reports grouped by layer.

  --layer    REQUIRED. One of: runtime, client, cloud, seat-context, workspace, other
  --text     REQUIRED. What happened, and what you expected instead.
  --room     Optional. Defaults to the room name this seat's identity chain resolves.
  --seat     Optional. Defaults to the member id this seat's identity chain resolves.
  --session  Optional. Defaults to the session name this seat's identity chain resolves.
  --url      Optional. Defaults to $AGENTHUB_URL. The route path is appended.

Credential: $AGENTHUB_TOKEN, sent as a bearer token. It is never printed.
Exit: 0 stored, 1 the request failed or the answer refused it, 2 usage or a missing value.
`

// identityMessage is the ONE refusal when the seat identity cannot be read: it names the chain that was
// tried and the three flags that make it unnecessary. The chain is the client's own - the runtime socket,
// then an rig daemon, then this shell's seat environment - so the message no longer names a `rig`
// command, which is not on that chain.
const identityMessage = "cannot read the seat identity (no runtime, no room daemon and no seat environment names this seat); pass --room, --seat and --session"

// feedbackBody is the wire payload. Its field order is the script's dict order so the bytes on the
// wire are the same ones json.dumps would have produced.
type feedbackBody struct {
	Room    string `json:"room"`
	Seat    string `json:"seat"`
	Session string `json:"session"`
	Layer   string `json:"layer"`
	Text    string `json:"text"`
}

// feedbackOptions is the parsed command line, before validation.
type feedbackOptions struct {
	layer   string
	text    string
	room    string
	seat    string
	session string
	url     string
}

// complete reports whether every identity field was given, in which case room is never consulted.
func (o feedbackOptions) complete() bool {
	return o.room != "" && o.seat != "" && o.session != ""
}

// Commands is what the dispatcher registers from this package: one verb, `feedback`.
func Commands() []clientcmd.Command {
	return []clientcmd.Command{feedbackCommand{}}
}

type feedbackCommand struct{}

func (feedbackCommand) Name() string    { return "feedback" }
func (feedbackCommand) Summary() string { return "report friction to the owner" }

func (feedbackCommand) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, help, err := parseFeedbackArgs(args)
	if help {
		_, _ = io.WriteString(stdout, feedbackUsage)
		return clientcmd.ExitOK
	}
	if err != nil {
		return feedbackUsageError(stderr, err.Error())
	}
	if opts.layer == "" {
		return feedbackUsageError(stderr, "--layer is required")
	}
	if opts.text == "" {
		return feedbackUsageError(stderr, "--text is required")
	}
	if !validLayer(opts.layer) {
		return feedbackUsageError(stderr, fmt.Sprintf("--layer %q is not one of %s", opts.layer, strings.Join(feedbackLayers, ", ")))
	}

	baseURL := opts.url
	if baseURL == "" {
		baseURL = os.Getenv("AGENTHUB_URL")
	}

	// Identity defaults come from the client itself, which is the only authority on which seat this
	// is. They are read only when one of the three is missing, so a fully-specified call reads nothing.
	if !opts.complete() {
		room, seat, session, err := readIdentity()
		if err != nil {
			return feedbackUsageError(stderr, identityMessage)
		}
		if opts.room == "" {
			opts.room = room
		}
		if opts.seat == "" {
			opts.seat = seat
		}
		if opts.session == "" {
			opts.session = session
		}
	}

	if baseURL == "" {
		return feedbackUsageError(stderr, "no server URL: set AGENTHUB_URL or pass --url")
	}
	token := os.Getenv("AGENTHUB_TOKEN")
	if token == "" {
		return feedbackUsageError(stderr, "AGENTHUB_TOKEN is not set, so the report cannot be authenticated")
	}
	return postFeedback(ctx, baseURL, token, opts, stdout, stderr)
}

// parseFeedbackArgs is the script's flag grammar: every flag takes a value, an unknown argument is a
// usage error, and -h/--help stops the parse. A flag whose value is missing leaves the field empty,
// which the required checks then name - the script's own `${2:-}` behaviour.
func parseFeedbackArgs(args []string) (feedbackOptions, bool, error) {
	var opts feedbackOptions
	i := 0
	value := func() string {
		if i+1 < len(args) {
			i++
			return args[i]
		}
		return ""
	}
	for ; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			return opts, true, nil
		case "--layer":
			opts.layer = value()
		case "--text":
			opts.text = value()
		case "--room":
			opts.room = value()
		case "--seat":
			opts.seat = value()
		case "--session":
			opts.session = value()
		case "--url":
			opts.url = value()
		default:
			return opts, false, fmt.Errorf("unknown argument: %s", args[i])
		}
	}
	return opts, false, nil
}

// validLayer is the closed set, checked before the request is built.
func validLayer(layer string) bool {
	for _, known := range feedbackLayers {
		if layer == known {
			return true
		}
	}
	return false
}

// readIdentity reads the three defaults from the client's own identity chain - the runtime socket first,
// then this shell's own seat environment. That is the chain `whoami --json` prints and the chain the send
// path (internal/clientseat) asks.
//
// The failure keeps the same shape it had when room supplied the answer: the chain's own refusal, which
// the caller turns into its usage error. An identity the chain resolves to empty fields is the script's
// `who.get("identity") or {}` - empty defaults, not a failure.
func readIdentity() (room, seat, session string, err error) {
	resolved, err := clientruntime.ResolveIdentity("")
	if err != nil {
		return "", "", "", err
	}
	identity := resolved.Identity
	return identity.RoomName, identity.MemberId, identity.SessionName, nil
}

// postFeedback sends the report and relays the answer: the raw body goes to stdout and a non-2xx goes
// to stderr as a failure. The token is sent in the header and never written anywhere.
func postFeedback(ctx context.Context, baseURL, token string, opts feedbackOptions, stdout, stderr io.Writer) int {
	url := strings.TrimRight(baseURL, "/") + feedbackPath
	body, err := json.Marshal(feedbackBody{
		Room:    opts.room,
		Seat:    opts.seat,
		Session: opts.session,
		Layer:   opts.layer,
		Text:    opts.text,
	})
	if err != nil {
		fmt.Fprintf(stderr, "4genteam feedback: cannot encode the report: %v\n", err)
		return clientcmd.ExitRemote
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(stderr, "4genteam feedback: the request to %s failed: %v\n", url, err)
		return clientcmd.ExitRemote
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := feedbackClient.Do(req)
	if err != nil {
		// The REASON is named here, because the shell client this replaced showed one: `curl -sS`
		// prints its own diagnostic before the script's line, so a compiled replacement that printed
		// only the line would tell the operator less than the door it replaced. The text carries the
		// URL and the transport's round trip; the credential is in the header, so it cannot be in it.
		fmt.Fprintf(stderr, "4genteam feedback: the request to %s failed: %v\n", url, err)
		return clientcmd.ExitRemote
	}
	defer func() { _ = resp.Body.Close() }()

	payload, _ := io.ReadAll(resp.Body)
	_, _ = stdout.Write(payload)
	_, _ = io.WriteString(stdout, "\n")

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		fmt.Fprintf(stderr, "4genteam feedback: the server answered HTTP %d\n", resp.StatusCode)
		return clientcmd.ExitRemote
	}
	return clientcmd.ExitOK
}

// feedbackUsageError is the ONE shape of a usage refusal: a line naming what is wrong, then the one
// line of usage, on stderr, at exit 2.
func feedbackUsageError(stderr io.Writer, message string) int {
	fmt.Fprintf(stderr, "4genteam feedback: %s\n", message)
	_, _ = io.WriteString(stderr, "usage: 4genteam feedback --layer <layer> --text <text> [--room R] [--seat S] [--session X] [--url BASE]\n")
	return clientcmd.ExitUsage
}
