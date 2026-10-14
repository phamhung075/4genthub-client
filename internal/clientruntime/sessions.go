// The session list and the two content reads the connector and rigd need, answered by the runtime over its
// own socket.
//
// WHY THIS EXISTS. `connectorverb.go` and `rigd.go` reached the local rig by exec-ing `rig` for facts the
// runtime already held: the session list (`rig ps --json`), a transcript (`rig transcript <session>`) and a
// pane (`rig capture <session>`). Those reaches were DECLARED against this missing interface (DEPLOY-READY
// section 65), and two of them do not work at all on the installed client, which answers
// `4genteam: unknown command transcript` - so this is not the conversion of a working call. It is what makes
// the connector able to read its own machine.
//
// THE KEYS ARE THE CLI'S, deliberately. The consumer parses ONE shape - `canonicalSessionName` first (the
// name a person passes to `--session`), then `rigName`, `logicalId` and `sessionStatus` - and the runtime
// serves exactly those spellings, so no second parser was needed on the client side. The json tags below ARE
// that contract: renaming one without renaming the runtime's serde rename makes every native session
// invisible to the connector while the runtime still looks healthy.
package clientruntime

import "errors"

// SessionEntry is one seat of one hosted room, as the runtime reports it.
type SessionEntry struct {
	CanonicalSessionName string          `json:"canonicalSessionName"`
	SessionName          string          `json:"sessionName"`
	RigName              string          `json:"rigName"`
	LogicalID            string          `json:"logicalId"`
	SessionStatus        string          `json:"sessionStatus"`
	AgentActivity        SessionActivity `json:"agentActivity"`
	Runtime              string          `json:"runtime"`
	Model                *string         `json:"model,omitempty"`
	TranscriptPath       *string         `json:"transcriptPath,omitempty"`
}

// SessionActivity is the seat's activity word, nested where the node table nests it (`agentActivity.state`).
// Source is the seat's own published word, so a reader sees the evidence the state was derived from.
type SessionActivity struct {
	State  string `json:"state"`
	Source string `json:"source,omitempty"`
}

// TranscriptAnswer is a transcript read: the text, and the file it came from.
type TranscriptAnswer struct {
	Session string `json:"session"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

// CaptureAnswer is a pane read: the text, and the seat it came from.
type CaptureAnswer struct {
	Session string `json:"session"`
	Content string `json:"content"`
}

// ListSessions reads the sessions of one room, or of every room the runtime hosts when room is empty.
//
// The room travels as an explicit null when it is empty, for the reason ListAllRooms gives: the frame is
// adjacently tagged, so `payload` is a sibling of `action`, and a dropped key would ask a different question
// than the runtime's own "every room it hosts" case.
func (c *Client) ListSessions(room string) ([]SessionEntry, error) {
	var named any
	if room != "" {
		named = room
	}
	resp, err := c.Call("ListSessions", map[string]any{"room": named})
	if err != nil {
		return nil, err
	}
	var entries []SessionEntry
	if err := decodeAnswer("ListSessions", resp.Data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// Transcript reads a seat's transcript. lines <= 0 reads the file whole, which is what the connector's
// one-shot send wants; rigd asks for its own tail window instead.
func (c *Client) Transcript(session string, lines int) (TranscriptAnswer, error) {
	resp, err := c.Call("Transcript", contentPayload(session, lines))
	if err != nil {
		return TranscriptAnswer{}, err
	}
	var answer TranscriptAnswer
	if err := decodeAnswer("Transcript", resp.Data, &answer); err != nil {
		return TranscriptAnswer{}, err
	}
	if answer.Session == "" {
		return TranscriptAnswer{}, unnamedAnswer("Transcript")
	}
	return answer, nil
}

// Capture reads a seat's pane through the runtime's own terminal manager, so the pane is read by the
// process that owns the tmux server rather than by a second caller racing it.
func (c *Client) Capture(session string, lines int) (CaptureAnswer, error) {
	resp, err := c.Call("Capture", contentPayload(session, lines))
	if err != nil {
		return CaptureAnswer{}, err
	}
	var answer CaptureAnswer
	if err := decodeAnswer("Capture", resp.Data, &answer); err != nil {
		return CaptureAnswer{}, err
	}
	if answer.Session == "" {
		return CaptureAnswer{}, unnamedAnswer("Capture")
	}
	return answer, nil
}

// contentPayload is what both content reads send: the seat, and a line window only when one was asked for.
// An absent `lines` is the runtime's own "read the file whole" case, so a bound is never invented here.
func contentPayload(session string, lines int) map[string]any {
	payload := map[string]any{"session": session}
	if lines > 0 {
		payload["lines"] = lines
	}
	return payload
}

// unnamedAnswer refuses a document that parsed but does not name the seat it is about. It is the same
// refusal class as an unreadable one, and for the same reason: every answer these actions give carries the
// seat, so an answer without one is a shape this build cannot read - and reading it as empty content is
// exactly the lie these actions exist to remove.
func unnamedAnswer(action string) error {
	return unreadableAnswer(action, errors.New("the answer named no session, so it is not a document this build can read"))
}
