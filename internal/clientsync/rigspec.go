// The cloud half of `sync status`: what the cloud would serve now, seat by seat.
package clientsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// RoomsPath is ROOMS_PATH in agenthub_client/src/agenthub_client/seat_sync.py.
const RoomsPath = "/api/v2/rooms"

// HTTPTimeout is the Python's urlopen timeout (60 seconds), named rather than inlined so a reader can
// see it is a ported constant and not a guess.
const HTTPTimeout = 60 * time.Second

// errorDetailLimit is the Python's `err.read().decode(...)[:300]` on an HTTP error body.
const errorDetailLimit = 300

// RequestJSON ports request_json. EVERY failure carries EXIT_REMOTE and the Python's own message shape,
// which is the half of this function a port drops - the four refusals and the two shape checks are why
// the tests below exist rather than a single happy-path case:
//
//	HTTP status outside 2xx -> "<method> <path> failed: HTTP <code> <detail[:300]>"
//	network or timeout      -> "<method> <path> failed: <cause>"
//	body is not JSON        -> "<method> <path> returned invalid JSON: <cause>"
//	body is JSON but not an object -> "<method> <path> returned a malformed response"
//
// A JSON LIST unmarshals fine and is then refused as malformed - the Python's json.load succeeds there
// and its isinstance check refuses it - so the decode goes through `any` and checks the shape, rather
// than decoding into a map and reporting the wrong refusal for a list.
func RequestJSON(ctx context.Context, method, baseURL, token, path string, payload any) (map[string]any, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, remoteFailure(method, path, fmt.Sprintf("cannot encode payload: %v", err))
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+path, body)
	if err != nil {
		return nil, remoteFailure(method, path, err.Error())
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: HTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, remoteFailure(method, path, err.Error())
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, remoteFailure(method, path, err.Error())
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		detail := string(raw)
		if len(detail) > errorDetailLimit {
			detail = detail[:errorDetailLimit]
		}
		return nil, remoteFailure(method, path, fmt.Sprintf("HTTP %d %s", resp.StatusCode, detail))
	}

	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, &clientcmd.CodedError{
			Message: fmt.Sprintf("%s %s returned invalid JSON: %v", method, path, err),
			Code:    clientcmd.ExitRemote,
		}
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, &clientcmd.CodedError{
			Message: fmt.Sprintf("%s %s returned a malformed response", method, path),
			Code:    clientcmd.ExitRemote,
		}
	}
	return object, nil
}

// FetchRigspec ports fetch_rigspec: GET {ROOMS_PATH}/{room}/rigspec, then the `success is not True`
// check and the `rigspec` shape check, each with the Python's message at EXIT_REMOTE.
func FetchRigspec(ctx context.Context, baseURL, token, room string) (map[string]any, error) {
	path := RoomsPath + "/" + room + "/rigspec"
	body, err := RequestJSON(ctx, "GET", baseURL, token, path, nil)
	if err != nil {
		return nil, err
	}
	if success, isBool := body["success"].(bool); !isBool || !success {
		return nil, remoteFailure("GET", path, "returned an error response")
	}
	rigspec, ok := body["rigspec"].(map[string]any)
	if !ok {
		return nil, remoteFailure("GET", path, "response has no rigspec")
	}
	return rigspec, nil
}

// CloudHashes is cloud_hashes: seat -> the hash of the snapshot the cloud would serve now, plus the
// cloud's own listing ORDER.
//
// THE ORDER IS RETURNED RATHER THAN LOST, and that is the one deliberate difference from the Python:
// `{entry["seat"]: entry["hash"] for entry in rigspec["seats"]}` builds a dict whose order the Python
// keeps and Go maps cannot, so the order travels beside the map and the status core takes it as an
// argument. Without it the rendered table would be in Go's map order, which is not the Python's.
func CloudHashes(ctx context.Context, baseURL, token, room string) ([]string, map[string]string, error) {
	rigspec, err := FetchRigspec(ctx, baseURL, token, room)
	if err != nil {
		return nil, nil, err
	}
	seats, _ := rigspec["seats"].([]any)
	order := make([]string, 0, len(seats))
	hashes := make(map[string]string, len(seats))
	for _, entry := range seats {
		row, ok := entry.(map[string]any)
		if !ok {
			return nil, nil, cloudEntryFailure("is not an object")
		}
		seat, seatIsString := row["seat"].(string)
		hash, hashIsString := row["hash"].(string)
		if !seatIsString || !hashIsString {
			return nil, nil, cloudEntryFailure("has no seat/hash strings")
		}
		order = append(order, seat)
		hashes[seat] = hash
	}
	return order, hashes, nil
}

// cloudEntryFailure is the Python's path for a malformed entry: its dict comprehension raises, the
// caller's generic handler catches it, and main() maps anything that is not EXIT_USAGE to EXIT_FAILED -
// which is 3, the code this client calls ExitUnavailable. Reported rather than skipped, because
// skipping would answer "nothing is behind" for a rigspec the cloud could not have meant.
func cloudEntryFailure(why string) error {
	return &clientcmd.CodedError{
		Message: fmt.Sprintf("rigspec seat entry %s", why),
		Code:    clientcmd.ExitUnavailable,
	}
}

func remoteFailure(method, path, cause string) error {
	return &clientcmd.CodedError{
		Message: fmt.Sprintf("%s %s failed: %s", method, path, cause),
		Code:    clientcmd.ExitRemote,
	}
}
