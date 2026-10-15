// The cloud transport: every verb that reads or writes over HTTP goes through RequestJSON, which carries
// the Python's four refusals and its two shape checks. It used to be the `sync status` cloud half; the
// route it was written for (`GET {ROOMS_PATH}/{room}/rigspec`) is gone, and this half is what the other
// verbs still need.
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

func remoteFailure(method, path, cause string) error {
	return &clientcmd.CodedError{
		Message: fmt.Sprintf("%s %s failed: %s", method, path, cause),
		Code:    clientcmd.ExitRemote,
	}
}
