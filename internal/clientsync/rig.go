// The rig path's validation half: a rigspec's shape, and the name rule every seat and room name obeys.
// Both are read before anything is written, which is what lets `rig` refuse with nothing half-applied.
package clientsync

import (
	"context"
	"fmt"
	"regexp"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// namePattern is NAME_RE: a room or seat name becomes a directory name, so it may not contain a
// separator, a space or anything else that would make it mean more than one path component.
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// ValidateName ports validate_name. The kind ("room", "seat") is in the message because the same rule
// is applied to both and an operator needs to know which name was refused.
func ValidateName(kind, value string) (string, error) {
	if !namePattern.MatchString(value) {
		return "", &clientcmd.CodedError{
			Message: fmt.Sprintf("invalid %s name: %q (expected [a-zA-Z0-9][a-zA-Z0-9_-]*)", kind, value),
			Code:    clientcmd.ExitUsage,
		}
	}
	return value, nil
}

// Rigspec is a room's rig as the cloud serves it: the YAML the rig root is written from, and the seats
// with the snapshot hash each one is pinned to.
type Rigspec struct {
	Name  string
	YAML  string
	Seats []RigspecSeat
}

// RigspecSeat is one entry of the rigspec's seat list.
type RigspecSeat struct {
	Seat string
	Hash string
}

// ReadRigspec ports the shape half of cmd_rig: fetch the rigspec, then SIX checks, every one of them a
// message the Python has and an operator can act on. The name checks are EXIT_USAGE (the caller named a
// room or a seat badly) while a malformed ANSWER is EXIT_REMOTE (the cloud is wrong) - the same split the
// file path guards keep, and the reason a retry loop can tell the two apart.
//
// The room is validated BEFORE the fetch, so a bad room name never reaches the network; that ordering is
// the Python's (validate_name("room", ...) is its first statement).
func ReadRigspec(ctx context.Context, baseURL, token, room string) (Rigspec, error) {
	var spec Rigspec
	validRoom, err := ValidateName("room", room)
	if err != nil {
		return spec, err
	}
	rigspec, err := FetchRigspec(ctx, baseURL, token, validRoom)
	if err != nil {
		return spec, err
	}
	if name, _ := rigspec["name"].(string); name != validRoom {
		return spec, remoteFailure("GET", RoomsPath+"/"+validRoom+"/rigspec", "returned a rigspec for a different room")
	}
	yamlText, ok := rigspec["yaml"].(string)
	if !ok {
		return spec, remoteFailure("GET", RoomsPath+"/"+validRoom+"/rigspec", "rigspec has no yaml text")
	}
	listed, ok := rigspec["seats"].([]any)
	if !ok || len(listed) == 0 {
		return spec, remoteFailure("GET", RoomsPath+"/"+validRoom+"/rigspec", "rigspec has no seats")
	}

	seats := make([]RigspecSeat, 0, len(listed))
	seen := map[string]bool{}
	for _, entry := range listed {
		row, isObject := entry.(map[string]any)
		if !isObject {
			return spec, remoteFailure("GET", RoomsPath+"/"+validRoom+"/rigspec", "rigspec contains a malformed seat entry")
		}
		seatName, seatIsString := row["seat"].(string)
		if !seatIsString {
			return spec, remoteFailure("GET", RoomsPath+"/"+validRoom+"/rigspec", "rigspec contains a malformed seat entry")
		}
		hash, hashIsString := row["hash"].(string)
		if !hashIsString {
			return spec, remoteFailure("GET", RoomsPath+"/"+validRoom+"/rigspec", "rigspec seat entry has no hash")
		}
		validSeat, err := ValidateName("seat", seatName)
		if err != nil {
			return spec, err
		}
		if seen[validSeat] {
			return spec, remoteFailure("GET", RoomsPath+"/"+validRoom+"/rigspec",
				fmt.Sprintf("rigspec lists seat %q twice", validSeat))
		}
		seen[validSeat] = true
		seats = append(seats, RigspecSeat{Seat: validSeat, Hash: hash})
	}
	return Rigspec{Name: validRoom, YAML: yamlText, Seats: seats}, nil
}
