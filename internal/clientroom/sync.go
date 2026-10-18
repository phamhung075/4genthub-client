// The `room sync` sub-verb: one room's RECORD against the LIVE pin, read from the cloud.
//
// IT IS THE ONE CLOUD SUB-VERB OF THIS GROUP. Every other verb here speaks the runtime's own IPC and prints
// the pair it ran against with a socket; this one is a token-scoped GET of
// `/api/v2/rooms/{room}/sync`, the route the handoff row qitem-20261018223119 carries (hub
// `30fa9bd9`, documented at `9c3e3013`), so there is no state dir and no socket in its answer. The header it
// prints says where the answer came from - `<room>@<url>` - because a reader who saw `<room>@<state-dir>` on
// this verb would look for a daemon that was never asked.
//
// WHAT THE ROUTE ANSWERS, and nothing more is invented here: one row per seat a machine reports for the
// room, each carrying the cloud's expected hash, the running pin the bridge reported, the machine that
// reported it, when, and the verdict `in_sync`/`drift`/`unknown`. A seat no machine reports is ABSENT -
// absence is never in_sync - and the full roster is `GET /api/v2/rooms/{room}/seats`, which this verb does
// not read. The rows arrive sorted by seat, newest report winning across machines; the client renders the
// order it is given rather than deriving a second one.
//
// A DRIFT IS DATA, NOT A FAILURE: exit stays 0 for every 2xx answer, whatever the verdicts say. The exit
// code is about whether the question was answered, and an operator who wanted a gate on drift has `--json`.
package clientroom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// syncUsage is what `4genteam room sync --help` prints.
const syncUsage = `usage: 4genteam room sync [ROOM] [--json] [--url BASE]

Read one room's record-vs-live seat state from the cloud: GET /api/v2/rooms/{room}/sync.

  ROOM    Optional. Defaults to $AGENTHUB_ROOM, the room the runtime launched this seat into.
  --json  Print the server's answer verbatim instead of the table.
  --url   Optional. Defaults to $AGENTHUB_URL. The route path is appended.

One row per seat a machine reports for the room: SEAT SYNC EXPECTED PINNED MACHINE REPORTED, where SYNC is
in_sync, drift or unknown, EXPECTED is the cloud's resolved hash for the seat and PINNED is the running pin
the bridge last reported. A seat no machine reports is ABSENT from the answer - absence is never in_sync -
and the full roster is GET /api/v2/rooms/{room}/seats.

Credential: $AGENTHUB_TOKEN, sent as a bearer token. It is never printed.
Exit: 0 the room was answered (a drift included), 1 the request failed or the answer was refused, 2 a missing
URL or credential, 3 a usage error.
`

// syncHTTPTimeout follows the client's one HTTP convention (internal/clientfeedback, internal/clientsync).
const syncHTTPTimeout = 60 * time.Second

// syncHTTPClient is the seam the suite replaces with an in-process transport, so the verb is tested with no
// network and without a cloud env.
var syncHTTPClient = &http.Client{Timeout: syncHTTPTimeout}

// syncBodyLimit bounds what is read from the server. The answer is one row per reported seat, so a ceiling
// this far above any room's size only ever catches a wrong endpoint.
const syncBodyLimit = 4 << 20

// syncRow is one seat of the wire's `seats` array. Field names are the route's own.
type syncRow struct {
	Seat         string `json:"seat"`
	ExpectedHash string `json:"expected_hash"`
	PinnedHash   string `json:"pinned_hash"`
	MachineID    string `json:"machine_id"`
	ReportedAt   string `json:"reported_at"`
	Sync         string `json:"sync"`
}

// syncAnswer is the route's body: success, the room it answered for and the rows.
type syncAnswer struct {
	Success bool      `json:"success"`
	Room    string    `json:"room"`
	Seats   []syncRow `json:"seats"`
}

// runSync is the sub-verb. The ROOM is the first positional and defaults to $AGENTHUB_ROOM, the same default
// the queue sub-verbs take; every refusal names the variable rather than guessing a room.
func runSync(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		_, _ = io.WriteString(stdout, syncUsage)
		return clientcmd.ExitOK
	}
	opts, rest, err := clientruntime.SplitFlags(args, []clientruntime.FlagSpec{
		{Name: "json"},
		{Name: "url", Value: true},
	})
	if err != nil {
		fmt.Fprintf(stderr, "4genteam room sync: %v. %s\n", err, syncUsage)
		return clientcmd.ExitRoomRefused
	}
	if len(rest) > 1 {
		fmt.Fprintf(stderr, "4genteam room sync: it takes one room at most, and %d were named. %s\n", len(rest), syncUsage)
		return clientcmd.ExitRoomRefused
	}
	room := ""
	if len(rest) == 1 {
		room = rest[0]
	} else {
		room = os.Getenv("AGENTHUB_ROOM")
	}
	if strings.TrimSpace(room) == "" {
		fmt.Fprintln(stderr, "4genteam room sync: ROOM was left out and $AGENTHUB_ROOM is unset, so there is no "+
			"room to default to; name the room, or run this from a seat the runtime launched")
		return clientcmd.ExitRoomRefused
	}

	base := opts["url"]
	if base == "" {
		base = os.Getenv("AGENTHUB_URL")
	}
	if base == "" {
		fmt.Fprintln(stderr, "4genteam room sync: no server URL: set AGENTHUB_URL or pass --url")
		return clientcmd.ExitUsage
	}
	token := os.Getenv("AGENTHUB_TOKEN")
	if token == "" {
		fmt.Fprintln(stderr, "4genteam room sync: AGENTHUB_TOKEN is not set, so the room's sync cannot be read")
		return clientcmd.ExitUsage
	}

	route := strings.TrimRight(base, "/") + "/api/v2/rooms/" + url.PathEscape(room) + "/sync"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, route, nil)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam room sync: %v\n", err)
		return clientcmd.ExitUsage
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := syncHTTPClient.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam room sync: the request to %s failed: %v\n", base, err)
		return clientcmd.ExitRemote
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, syncBodyLimit))
	if err != nil {
		fmt.Fprintf(stderr, "4genteam room sync: the answer could not be read: %v\n", err)
		return clientcmd.ExitRemote
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		fmt.Fprintf(stderr, "4genteam room sync: the server answered HTTP %d%s\n", resp.StatusCode, detailSuffix(raw))
		return clientcmd.ExitRemote
	}
	if opts["json"] == "true" {
		_, _ = stdout.Write(append(bytes.TrimRight(raw, "\n"), '\n'))
		return clientcmd.ExitOK
	}
	var answer syncAnswer
	if err := json.Unmarshal(raw, &answer); err != nil {
		fmt.Fprintf(stderr, "4genteam room sync: the server's answer is not the sync shape: %v\n", err)
		return clientcmd.ExitRemote
	}
	fmt.Fprintf(stdout, "room: %s@%s\n", answer.Room, strings.TrimRight(base, "/"))
	if len(answer.Seats) == 0 {
		fmt.Fprintf(stdout, "no machine reports a seat for this room; absence is never in_sync. "+
			"The roster is GET /api/v2/rooms/%s/seats\n", answer.Room)
		return clientcmd.ExitOK
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SEAT\tSYNC\tEXPECTED\tPINNED\tMACHINE\tREPORTED")
	for _, row := range answer.Seats {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			row.Seat, row.Sync, row.ExpectedHash, row.PinnedHash, row.MachineID, row.ReportedAt)
	}
	_ = w.Flush()
	return clientcmd.ExitOK
}

// detailSuffix renders the server's `detail` for a refusal, when it named one, so the refusal is reported in
// the remote's own words instead of only by status code.
func detailSuffix(raw []byte) string {
	var body struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || strings.TrimSpace(body.Detail) == "" {
		return ""
	}
	return ": " + body.Detail
}
