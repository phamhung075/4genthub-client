package clientruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// RuntimeSocketEnv names the override for the runtime's Unix socket. It exists because a client built from a
// branch has to reach a daemon built from the SAME branch while the owner's daemon keeps serving the default
// path - the only way the cutover gets tested before it happens. The NAME IS A CONTRACT rather than a discovery:
// an owner's harness and a branch client meet on exactly this string, so it is exported and pinned by a test.
const RuntimeSocketEnv = "OPENRIG_RUNTIME_SOCKET"

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

// Reachable reports whether something answers on this client's socket. It is the reading EnsureDaemon
// polls, named once because a caller that starts the daemon ITSELF - with a state dir and a log file of
// its own - has to poll the same question, and three copies of one dial would be three definitions of "up".
func (c *Client) Reachable() bool {
	conn, err := net.DialTimeout("unix", c.SocketPath, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// EnsureDaemon checks if the runtime daemon is reachable; if not, starts it.
func (c *Client) EnsureDaemon(ctx context.Context) error {
	if c.Reachable() {
		return nil
	}

	// Not reachable: start agenthub-runtime daemon
	runtimeBin, err := exec.LookPath("agenthub-runtime")
	if err != nil {
		home := os.Getenv("HOME")
		localBin := filepath.Join(home, ".local", "bin", "agenthub-runtime")
		if _, statErr := os.Stat(localBin); statErr == nil {
			runtimeBin = localBin
		} else {
			return errors.New("agenthub-runtime binary not found on PATH or in ~/.local/bin")
		}
	}

	cmd := exec.Command(runtimeBin, "--socket", c.SocketPath, "daemon")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start agenthub-runtime daemon: %w", err)
	}

	// Poll until ready, up to 3 seconds
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c.Reachable() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return errors.New("agenthub-runtime daemon started but socket was not ready within 3 seconds")
}

// callDeadline is how long an ordinary request waits: a verb that reads or writes an answer settles in
// milliseconds, so a socket gone quiet is a fault worth naming in ten seconds.
const callDeadline = 10 * time.Second

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
	return c.CallWithDeadline(action, payload, callDeadline)
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
		return nil, fmt.Errorf("decode error: %w", err)
	}

	if !resp.Ok {
		return nil, errors.New(resp.Error)
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

func (c *Client) RestartSeat(room, seat string) (*SeatStatus, error) {
	resp, err := c.CallWithDeadline("RestartSeat", map[string]string{"room": room, "seat": seat}, seatStartDeadline)
	if err != nil {
		return nil, err
	}
	var status SeatStatus
	if err := decodeAnswer("RestartSeat", resp.Data, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

func (c *Client) SendText(session, text string) error {
	_, err := c.Call("SendText", map[string]string{"session": session, "text": text})
	return err
}

func (c *Client) SendTextToSeat(room, seat, text string) error {
	return c.SendText(SeatSessionName(room, seat), text)
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
