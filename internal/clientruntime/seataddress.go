package clientruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// SEAT ADDRESS RESOLUTION: which name a seat's pane actually carries, read from the source that knows it.
//
// The client must never COMPOSE this name. Two names describe one seat - the runtime's native
// `agenthub--<room>--<seat>` and the canonical `<room>-<seat>@<room>` - and for a rig whose name carries a
// dash the canonical string cannot be split back into its halves without guessing: this rig's own
// `4genthub-min-lead@4genthub-min` parses equally as (4genthub, min-lead) and as (4genthub-min, lead). So no
// conversion between the two names is attempted anywhere in this file, and neither is invented from a
// convention: the name is READ as a field from whoever OWNS the pane, and the owner decides the transport.
//
// That second half is not a preference either. The local runtime points its own tmux clients at a server it
// owns (`<state dir>/tmux`, rust/agenthub-runtime/src/main.rs `own_tmux_root`), so it can only address panes
// it created: a canonical name handed to it answers `can't find pane`. The daemon owns the panes of a rig it
// hosts, and delivers to them. The record that knows the name is therefore the record whose transport can
// use it.

// SeatAddress is where one seat's delivery goes, and what proved it.
type SeatAddress struct {
	// SessionName is the name the owning transport can address, exactly as the record spells it. It is never
	// built here; an empty one is a resolution failure, not a name to fill in.
	SessionName string
	// Transport is the transport that owns the pane this name belongs to.
	Transport Transport
	// Source names the record the name was read from, for the report a send owes its caller.
	Source string
}

// seatNameKeys is the order a record's name-bearing fields are read in, MOST CANONICAL FIRST. The same list,
// with the same meaning, is clientsync's `sessionNameKeys`; it is repeated here rather than imported because
// clientsync is built on this package. One of these two should own it.
var seatNameKeys = []string{"canonicalSessionName", "sessionName", "session_name", "name", "id", "logicalId"}

// ResolveSeatAddress answers which session name a rig's seat carries, from the record that knows it.
//
// Two records can answer, in this order:
//
//  1. the room's own runtime - the FIELD path. `AGENTHUB_ROOM`/`AGENTHUB_SEAT` name the room as fields when
//     this shell is a native seat, and the room's runtime answers every seat with its own `session_name`.
//  2. the OpenRig daemon's seat table - the record path, and this rig's shape. `/api/rigs/<id>/nodes` lists
//     each seat with `logicalId` and `canonicalSessionName`, so the seat is matched and named by FIELDS.
//
// When neither answers, the refusal names what was missing rather than falling back to a composed name: a
// guess that works for a room called `leg-solo` and fails for a rig called `4genthub-min` is worse than a
// refusal, because it looks like a working address until the moment it is used.
func ResolveSeatAddress(ctx context.Context, rig, seat string) (SeatAddress, error) {
	return resolveSeatAddress(ctx, rig, seat, false)
}

// ResolveDaemonSeatAddress is ResolveSeatAddress for a send that a daemon-only switch has ALREADY committed to
// the daemon (--verify, --force, --wait-for-idle, --dangerously-interact): the room's own runtime is not
// consulted at all. Asking it would disturb a healthy transport with a question it can never answer for a
// request it can never carry - the runtime runs no handshake those switches promise.
func ResolveDaemonSeatAddress(ctx context.Context, rig, seat string) (SeatAddress, error) {
	return resolveSeatAddress(ctx, rig, seat, true)
}

func resolveSeatAddress(ctx context.Context, rig, seat string, daemonOnly bool) (SeatAddress, error) {
	if !daemonOnly {
		if session := seatAddressFromRuntime(rig, seat); session != "" {
			return SeatAddress{SessionName: session, Transport: TransportRuntime, Source: "the runtime's room record"}, nil
		}
	}
	if openRigDaemonConfigured() {
		session, err := seatAddressFromDaemon(ctx, rig, seat)
		if err != nil {
			return SeatAddress{}, err
		}
		if session != "" {
			return SeatAddress{SessionName: session, Transport: TransportDaemon, Source: "the daemon's seat record"}, nil
		}
	}
	// Neither record lists this seat. Say which doors were knocked on, and say plainly that no name was made
	// up: the caller's next move is to fix the input or the transport, not to trim a string.
	daemon := "this shell names no OpenRig daemon (set " + openRigURLEnv + " or " + openRigPortEnv + ")"
	if base := openRigDaemonURL(); base != "" {
		daemon = "the daemon at " + base + " does not list that seat"
	}
	runtime := fmt.Sprintf("the room's runtime has no room %q", rig)
	if daemonOnly {
		runtime = "the daemon was the only source this send could use, because a switch named it"
	}
	return SeatAddress{}, &clientcmd.CodedError{
		Message: fmt.Sprintf("no record of %s/%s: %s, and %s; no name was composed - "+
			"a seat's session name is read from the record that owns its pane, never built from a convention",
			rig, seat, runtime, daemon),
		Code: clientcmd.ExitUnavailable,
	}
}

// seatAddressFromRuntime is the FIELD path: ask the room's own runtime for the seat's real session name.
//
// The room is `AGENTHUB_ROOM` when this shell is a native seat - the launcher that hosts a seat states the
// room as a field, and the native shape's first positional argument names the same room. A runtime that has
// no such room answers nothing, which is the honest reading for a rig the daemon hosts: this client's
// runtime is the seat's own, not the fleet's.
func seatAddressFromRuntime(rig, seat string) string {
	if seat == "" {
		return ""
	}
	room := strings.TrimSpace(os.Getenv(agentHubRoomEnv))
	if room == "" {
		room = rig
	}
	if room == "" {
		return ""
	}
	statuses, err := NewClient("").GetRoomStatus(room)
	if err != nil {
		return ""
	}
	for _, status := range statuses {
		if status.Seat == seat && strings.TrimSpace(status.SessionName) != "" {
			return strings.TrimSpace(status.SessionName)
		}
	}
	return ""
}

// seatAddressFromDaemon is the record path: the daemon's own seat table.
//
// Two calls, because the seat table hangs off the rig id rather than its name: `/api/ps` maps the rig NAME the
// caller knows to the `rigId` the records use, and `/api/rigs/<id>/nodes` lists the seats. Both matches are
// on FIELDS - `rigName`/`name` for the rig, `logicalId` (`<rig>.<seat>`) for the seat - and the name comes out
// of a name-bearing field, most canonical first. An empty answer means the daemon simply does not list that
// seat; a transport failure is returned as an error rather than read as an empty record.
func seatAddressFromDaemon(ctx context.Context, rig, seat string) (string, error) {
	raw, status, err := openRigCall(ctx, http.MethodGet, "/api/ps", nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", &clientcmd.CodedError{
			Message: fmt.Sprintf("the OpenRig daemon at %s answered %d for its rig list", openRigDaemonURL(), status),
			Code:    clientcmd.ExitRemote,
		}
	}
	var rigs []struct {
		RigID   string `json:"rigId"`
		RigName string `json:"rigName"`
		Name    string `json:"name"`
	}
	if err := json.Unmarshal(raw, &rigs); err != nil {
		return "", &clientcmd.CodedError{Message: fmt.Sprintf("could not read the daemon's rig list: %v", err), Code: clientcmd.ExitRemote}
	}
	rigID := ""
	for _, entry := range rigs {
		if entry.RigName == rig || entry.Name == rig {
			rigID = entry.RigID
			break
		}
	}
	if rigID == "" {
		return "", nil
	}

	raw, status, err = openRigCall(ctx, http.MethodGet, "/api/rigs/"+url.PathEscape(rigID)+"/nodes", nil)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound {
		return "", nil
	}
	if status != http.StatusOK {
		return "", &clientcmd.CodedError{
			Message: fmt.Sprintf("the OpenRig daemon at %s answered %d for %s's seats", openRigDaemonURL(), status, rig),
			Code:    clientcmd.ExitRemote,
		}
	}
	var nodes []map[string]any
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return "", &clientcmd.CodedError{Message: fmt.Sprintf("could not read the daemon's seat records: %v", err), Code: clientcmd.ExitRemote}
	}
	want := rig + "." + seat
	for _, node := range nodes {
		if logicalID, _ := node["logicalId"].(string); logicalID != want {
			continue
		}
		for _, key := range seatNameKeys {
			if name, _ := node[key].(string); strings.TrimSpace(name) != "" {
				return strings.TrimSpace(name), nil
			}
		}
	}
	return "", nil
}
