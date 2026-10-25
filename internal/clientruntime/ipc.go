package clientruntime

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// RuntimeSocketEnv names the override for the runtime's Unix socket. It exists because a client built from a
// branch has to reach a daemon built from the SAME branch while the owner's daemon keeps serving the default
// path - the only way the cutover gets tested before it happens. The NAME IS A CONTRACT rather than a discovery:
// an owner's harness and a branch client meet on exactly this string, so it is exported and pinned by a test.
const RuntimeSocketEnv = "AGENTHUB_RUNTIME_SOCKET"

// DefaultSocketPath resolves the runtime Unix domain socket: the override when it is set, else this machine's own
// location. THIS IS THE ONE RESOLUTION POINT - NewClient("") is its only caller - so one socket is named by exactly
// ONE input: a set override REPLACES the derivation rather than joining it, which is what keeps a branch client and
// the branch daemon it starts on the same path instead of two.
//
// A blank value counts as unset, the rule this client's other environment variables already use: a copied .env that
// blanked a switch is a normal thing to find, and a blank path would resolve to a socket in the working directory.
// The derivation below is byte-for-byte unchanged for every machine that does not set the override.
func DefaultSocketPath() string {
	if explicit := strings.TrimSpace(os.Getenv(RuntimeSocketEnv)); explicit != "" {
		return explicit
	}
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "agenthub.sock")
	}
	home := os.Getenv("HOME")
	if home == "" {
		home = "."
	}
	return filepath.Join(home, ".config", "4genthub", "run", "runtime.sock")
}

// Client talks to agenthub-runtime over the Unix domain socket.
type Client struct {
	SocketPath string
}

// NewClient creates a new client.
func NewClient(socketPath string) *Client {
	if socketPath == "" {
		socketPath = DefaultSocketPath()
	}
	return &Client{SocketPath: socketPath}
}

// Reachable reports whether something answers on this client's socket. The daemon is started in ONE place,
// clientlifecycle.EnsureDaemon, which polls this same question: two copies of one dial would be two
// definitions of "up".
func (c *Client) Reachable() bool {
	conn, err := net.DialTimeout("unix", c.SocketPath, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// CallDeadline is how long an ordinary request waits: a verb that reads or writes an answer settles in
// milliseconds, so a socket gone quiet is a fault worth naming in ten seconds. Exported because a caller
// that reports a timeout has to name it, and the runtime's own reply bound (8 s) is kept under it.
const CallDeadline = 10 * time.Second

// seatStartDeadline is how long the client waits for a verb that STARTS A SEAT and therefore waits for that
// seat to prove itself. It MUST outlast the runtime's own readiness window - `SEAT_READY_WINDOW_SECS = 60` in
// rust/agenthub-runtime/src/supervisor/mod.rs - and it did not, twice over: measured, a cold start returned
// `read error: i/o timeout` at 10.15 s while the runtime was still legitimately waiting, so a verdict that
// would have landed at 15 s could never reach the client and read as a transport error instead of a verdict.
// The window then grew to 60 s to cover a first-ever launch in a fresh cache, and this grew with it. The two
// constants cannot be read from one another across the language boundary, so the runtime's number is named
// here and a test pins that this one stays larger.
const seatStartDeadline = 90 * time.Second

// Call sends a request and reads a response.
func (c *Client) Call(action string, payload any) (*IpcResponse, error) {
	return c.CallWithDeadline(action, payload, CallDeadline)
}

// CallWithDeadline is Call for a verb whose answer legitimately takes longer than an ordinary request.
func (c *Client) CallWithDeadline(action string, payload any, timeout time.Duration) (*IpcResponse, error) {
	conn, err := net.DialTimeout("unix", c.SocketPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to agenthub-runtime at %s: %w", c.SocketPath, err)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(timeout))

	req := IpcRequest{Action: action, Payload: payload}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	reqBytes = append(reqBytes, '\n')

	if _, err := conn.Write(reqBytes); err != nil {
		return nil, fmt.Errorf("write error: %w", err)
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read error: %w", err)
	}

	var resp IpcResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		// AN ANSWER NOBODY CAN READ IS A REFUSAL, not a failure of the transport: the daemon answered on the socket -
		// something wrote a line back - and what it wrote cannot be used. Typed here, at the one place that sees the
		// raw line, so the caller that must not guess (`up`) can tell "the daemon does not know this room" from
		// "nothing about this room could be read".
		return nil, unreadableAnswer(action, fmt.Errorf("decode error: %w", err))
	}

	if !resp.Ok {
		// A FRAMING FAILURE IS NOT A VERB'S REFUSAL, and it must not be shown as one: an older runtime lists the
		// actions it knows and rejects everything else in serde's own words ("invalid request JSON: unknown
		// variant `QueueList`, expected one of ..."), which as the verb's answer reads as something about the
		// ROOM - and nothing about the room was asked or answered. Typed here, where the envelope is read.
		if IsFramingFailure(resp.Error) {
			return nil, runtimeTooOld(action, c.SocketPath, resp.Error)
		}
		// THE DAEMON'S OWN REFUSAL, typed because it is the one failure that says something about the ROOM: the daemon
		// answered, and it will not serve this request - for a status read, a room it does not know.
		return nil, &DaemonRefused{Reason: resp.Error}
	}

	return &resp, nil
}

// decodeAnswer reads ONE verb's answer out of a frame the daemon answered: the document under `data`,
// re-encoded and read into this client's own shape. A document that cannot be read is a REFUSAL rather
// than a zero value, and that is the whole point of this function existing.
//
// WHY A ZERO VALUE IS FORBIDDEN. `Call` has already proved the daemon ANSWERED (its envelope guard passed
// and `ok` is true), so whatever the verb returns is read as that answer. Discarding a decode failure
// therefore returns a value the caller cannot tell from a real one: an unreadable Whoami would report an
// EMPTY IDENTITY, an unreadable room list would report NO ROOMS, and `up` would decide the room is not up
// and start it - cycling live sessions - from an answer nobody could read. Nothing here may ever return
// "nothing" as if it were a measurement.
//
// WHAT IS REFUSED, PRECISELY: the SHAPE. Unknown FIELDS stay tolerated exactly as they were - this is a
// plain json.Unmarshal, and DisallowUnknownFields is deliberately used nowhere in this package, so a
// runtime that learns a key before this client does costs nothing. Only a document this client cannot read
// at all (a different type where a row, an array or an object belongs; truncated JSON) is refused.
//
// The code is clientcmd.ExitRoomRefused (3), the room-scoped contract's number for a request the daemon did
// not serve (design §2.5), so a caller reading the exit code is told "this was not answered", never "this
// was answered and the answer is empty".
func decodeAnswer(action string, data any, out any) error {
	// AN ANSWER WITH NO DATA IS NOT AN EMPTY ANSWER. json.Unmarshal("null", &rows) succeeds into a nil slice, so a
	// bare `{"ok":true}` was read as a successful empty list - `queue list` printed "0 pending" at exit 0 against a
	// runtime that had answered nothing about the queue. That is the same lie as printing blanks, and it is the
	// shape this unit exists to remove: a document nobody could read must refuse, and an answer that carries no
	// document must not be mistaken for one that carries an empty one.
	if data == nil {
		return unreadableAnswer(action, errors.New("the answer carried no data at all, so there is nothing to read"))
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return unreadableAnswer(action, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return unreadableAnswer(action, err)
	}
	return nil
}

// unreadableAnswer is the refusal decodeAnswer raises: the verb NAMED, the failure NAMED as a document
// whose shape is not the one this verb asks for, and the decoder's own words kept so a reader can see which
// shape arrived. It carries exit 3 (see decodeAnswer), so the verb's caller cannot mistake it for a
// transport that was merely absent.
// DaemonRefused is the runtime's OWN refusal: a frame arrived, parsed as an envelope, and said ok=false. It is a
// TYPE rather than a bare error because "the daemon answered and refused" and "this client could not read an
// answer" are different facts about the world, and one caller has to act on the difference: `up` may send a room's
// whole definition when the daemon does not know the room (the definition is then the only source of its seats),
// and must never send it when nothing was learned about what is running - StartRoom kills and re-creates a session
// it finds running, so a blind start cycles a room of live agents.
type DaemonRefused struct{ Reason string }

func (e *DaemonRefused) Error() string { return e.Reason }

// UnreadableAnswer is the refusal decodeAnswer raises, and it is a TYPE rather than only a code so that a caller can
// tell the two kinds of error apart: the runtime ANSWERED and refused (a room it does not know, say - `up` must still
// send the definition, because the definition is then the only source of its seats), versus nobody could READ the
// answer (`up` must refuse, because starting from a definition nobody could check is a blind start). Both reach a
// caller as an error and both leave with the room contract's exit 3; only one of them may be acted on, so the code
// they share cannot be the thing that decides.
type UnreadableAnswer struct {
	*clientcmd.CodedError
	Action string
}

// Unwrap keeps CodeOf working through the wrapper: an unreadable answer is still a coded one.
func (e *UnreadableAnswer) Unwrap() error { return e.CodedError }

func unreadableAnswer(action string, err error) error {
	return &UnreadableAnswer{
		Action: action,
		CodedError: &clientcmd.CodedError{
			Message: fmt.Sprintf(
				"the runtime's %s answer could not be read (the document it sent is not the shape this verb asks for): %v",
				action, err),
			Code: clientcmd.ExitRoomRefused,
		},
	}
}

// RuntimeTooOld is a FRAMING failure read as what it is: the runtime answered on the socket and did not
// understand the REQUEST. It is a different fact from DaemonRefused in both directions - nothing about the room
// was asked, so nothing about it was answered - and it is a different fact from UnreadableAnswer too, because
// the runtime's own words came back in a frame this client reads perfectly.
//
// It leaves with ExitUnavailable: this client is correct, and the thing it is talking to cannot serve it. A verb
// may print the reason as its whole output; nothing may be retried and nothing falls back (the ruling's R4).
//
// WHY THE CLIENT MATCHES TEXT HERE instead of the runtime marking its own framing failures with a kind: the
// runtime this must survive is OLDER than this build - that is the entire case - so a marker added to the
// runtime now cannot be in the answer that arrives. A kind would only help a future runtime, and a future
// runtime is the case where the install discipline (C1/C4: a matching build beside the client) already wins. The
// markers are named here and pinned by a test, so a wording drift is visible rather than silent.
//
// WHAT IT MUST NEVER DO: rewrite a verb's own refusal. `unknown qitem_id 'x'` is the runtime refusing WORK, and
// reporting that as "unimplemented" would convert a real refusal into a lie about versions. Only the framing
// markers below convert, and every other ok=false passes through as DaemonRefused, untouched.
type RuntimeTooOld struct {
	*clientcmd.CodedError
	Action string
	Socket string
	Reason string
}

// Unwrap keeps CodeOf working through the wrapper: an older runtime is still a coded refusal.
func (e *RuntimeTooOld) Unwrap() error { return e.CodedError }

// framingMarkers are serde's own words for a request the runtime could not frame at all. Measured against the
// installed runtime, which answers an action it does not know with `invalid request JSON: unknown variant
// \`QueueList\`, expected one of StartRoom, StopRoom, ...` - the same complaint for every verb added after it was
// built, which is why this is a boundary rather than an accident.
var framingMarkers = []string{"invalid request JSON", "unknown variant"}

// IsFramingFailure reports whether the runtime's refusal is about the REQUEST rather than about the work.
func IsFramingFailure(reason string) bool {
	for _, marker := range framingMarkers {
		if strings.Contains(reason, marker) {
			return true
		}
	}
	return false
}

// noAnswerMarkers are the runtime's own words for a runner that did not answer within the daemon's reply
// bound (REPLY_TIMEOUT, rust/agenthub-runtime/src/runner/mod.rs). That is NOT a refusal of the work: the
// daemon wrote the frame to the runner's mailbox, so the text may be held for the seat's next turn, and a
// verb that printed "nothing was sent" for it would state something nobody measured - the U3 gate caught
// exactly that sentence against a delivery the runner had HELD (task 83a39aa5). A marker rather than a kind
// for the reason IsFramingFailure gives: the runtime that must be read may be older than this client, so
// the words are the only channel both builds share.
var noAnswerMarkers = []string{"no answer from the runner for"}

// IsNoAnswer reports whether the runtime's refusal is its own reply bound elapsing rather than a refusal of
// the work.
func IsNoAnswer(reason string) bool {
	for _, marker := range noAnswerMarkers {
		if strings.Contains(reason, marker) {
			return true
		}
	}
	return false
}

func runtimeTooOld(action, socket, reason string) error {
	return &RuntimeTooOld{
		Action: action,
		Socket: socket,
		Reason: reason,
		CodedError: &clientcmd.CodedError{
			Message: fmt.Sprintf(
				"%s is not implemented by the runtime at %s (it is older than this client; it answered %q). "+
					"The runtime and this binary are different builds: restart the runtime for this seat (client %s) or run the matching binary.",
				action, socket, strings.TrimSpace(reason), clientcmd.Version),
			Code: clientcmd.ExitUnavailable,
		},
	}
}

func (c *Client) StartRoom(spec RoomSpec) ([]SeatStatus, error) {
	resp, err := c.CallWithDeadline("StartRoom", spec, seatStartDeadline)
	if err != nil {
		return nil, err
	}
	var statuses []SeatStatus
	if err := decodeAnswer("StartRoom", resp.Data, &statuses); err != nil {
		return nil, err
	}
	return statuses, nil
}

func (c *Client) StopRoom(room string) error {
	_, err := c.Call("StopRoom", map[string]string{"room": room})
	return err
}

func (c *Client) StopSeat(room, seat string) error {
	_, err := c.Call("StopSeat", map[string]string{"room": room, "seat": seat})
	return err
}

// RestartSeat stops the seat and starts it again from the room's own spec.
//
// policyDocument is the seat's config document when the CALLER has a fresh one - the client fetched the
// hub's render for this restart, which is what a reseat is for - and nil when it has nothing newer than the
// room's record. It is omitted from the frame rather than sent as null in that case, so a runtime that has
// the field keeps whatever the room's spec already holds.
func (c *Client) RestartSeat(room, seat string, policyDocument *string) (*SeatStatus, error) {
	return c.restartSeat(room, seat, false, policyDocument)
}

// RestartSeatFresh is RestartSeat with the seat's saved agent sessions dropped before the start, so the agent that
// comes up has none of the previous one's context. The plain RestartSeat wire is unchanged: `fresh` is only sent here.
func (c *Client) RestartSeatFresh(room, seat string, policyDocument *string) (*SeatStatus, error) {
	return c.restartSeat(room, seat, true, policyDocument)
}

// restartSeat is the ONE builder of the RestartSeat frame, so the two verbs above cannot drift in what they
// send. Every key is added only when it is set: `fresh` false is the wire older callers already write, and a
// document the caller does not have must not be sent as an empty one.
func (c *Client) restartSeat(room, seat string, fresh bool, policyDocument *string) (*SeatStatus, error) {
	payload := map[string]any{"room": room, "seat": seat}
	if fresh {
		payload["fresh"] = true
	}
	if policyDocument != nil {
		payload["policy_document"] = *policyDocument
	}
	resp, err := c.CallWithDeadline("RestartSeat", payload, seatStartDeadline)
	if err != nil {
		return nil, err
	}
	var status SeatStatus
	if err := decodeAnswer("RestartSeat", resp.Data, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// SetSeatModel records the model a seat starts with in the room's own spec. A running agent keeps the model it was
// spawned with, so the change applies at the seat's next start.
func (c *Client) SetSeatModel(room, seat, model string) error {
	_, err := c.Call("SetSeatModel", map[string]string{"room": room, "seat": seat, "model": model})
	return err
}

// SendText delivers one message to one session. `verb` is the client verb that CARRIED the frame - the room's
// say, its abort, the wake - and it is stamped onto the frame so a text a mid-turn seat cannot take is handed
// back to this session naming the way it arrived. Blank means `send`.
func (c *Client) SendText(session, text, verb string) error {
	_, err := c.Call("SendText", SendTextArgs(session, text, ownSession(), verb))
	return err
}

func (c *Client) SendTextToSeat(room, seat, text, verb string) error {
	return c.SendText(SeatSessionName(room, seat), text, verb)
}

func (c *Client) StartRoomByName(room, cwd string) ([]SeatStatus, error) {
	var cwdPtr *string
	if cwd != "" {
		cwdPtr = &cwd
	}
	return c.StartRoom(RoomSpec{
		Room: room,
		Cwd:  cwdPtr,
	})
}

func (c *Client) GetRoomStatus(room string) ([]SeatStatus, error) {
	resp, err := c.Call("GetRoomStatus", map[string]string{"room": room})
	if err != nil {
		return nil, err
	}
	var statuses []SeatStatus
	if err := decodeAnswer("GetRoomStatus", resp.Data, &statuses); err != nil {
		return nil, err
	}
	return statuses, nil
}

func (c *Client) Whoami(session string) (*WhoamiResponse, error) {
	resp, err := c.Call("Whoami", map[string]string{"session": session})
	if err != nil {
		return nil, err
	}
	var who WhoamiResponse
	if err := decodeAnswer("Whoami", resp.Data, &who); err != nil {
		return nil, err
	}
	return &who, nil
}

func (c *Client) ForceCompact(session string) (string, error) {
	resp, err := c.Call("ForceCompact", map[string]string{"session": session})
	if err != nil {
		return "", err
	}
	m, ok := resp.Data.(map[string]any)
	if ok && m["result"] != nil {
		return fmt.Sprintf("%v", m["result"]), nil
	}
	return "ok", nil
}
