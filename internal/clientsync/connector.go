// The connector CLIENT for /ws/connector: the one piece of the session-stream feature that did not
// exist in either language. The consumer (the Sessions page) and the server (ws_mount.go:69 mounts the
// route, session_stream writes the rows) were both delivered; nothing connected to the wire.
//
// IT LIVES HERE, BESIDE THE OTHER SYNC VERBS, because the owner's directive is ONE CLIENT for the
// local-to-cloud bridge: a connector in a second client would be the split that directive exists to
// prevent. It runs `rig` as a CHILD PROCESS for the same reason every other verb interaction does.
//
// IT CARRIES NO THIRD-PARTY WEBSOCKET LIBRARY, and that is not an omission. The repository speaks
// WebSocket by hand on both sides already - wsUpgrade (ws_mount.go:704) on the server, and the
// hand-rolled dialer in ws_mount_test.go:49 on the client - so a library here would be a second
// convention for a protocol this tree already implements. What is needed is one text-frame client,
// which is what this file is.
//
// THE TOKEN TRAVELS IN A HEADER, NEVER IN THE QUERY. The route accepts ?token= (the tests use it), and
// the server therefore accepts Bearer too; the header is chosen here because a URL is the thing that
// ends up in logs, error strings and process listings.
package clientsync

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
)

const (
	connectorPath    = "/ws/connector"
	wsGUID           = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	connectorTimeout = 30 * time.Second
)

// The 2.2 vocabulary that is not a frame name: the protocol version, the two capabilities a phase-1
// client advertises, the two states and two sources a `session` frame carries, and the two close codes
// that are NOT retried.
const (
	// ClientProtocol is the protocol version this client speaks. A hello without it means 1, and ready
	// answers the version the server will speak.
	ClientProtocol = 1
	// CapabilityIngest is phase 1's ingest capability: the `session` and `events` frames.
	CapabilityIngest = "ingest"
	// CapabilitySafeguards is 7.6's addition. A `safeguard` frame is sent ONLY after ready echoes this
	// one, which is what keeps a frame from a later phase impossible against this server.
	CapabilitySafeguards = "safeguards"
	// CapabilityCommands is phase 5's remote execution capability (D4/F5): command execution over /ws/connector.
	CapabilityCommands = "commands"

	// SessionRunning and SessionStopped are the two values of `session.state`.
	SessionRunning = "running"
	SessionStopped = "stopped"
	// SourceTranscript and SourceCapture are the two values of `session.source`.
	SourceTranscript = "transcript"
	SourceCapture    = "capture"

	// CloseCodePolicyViolation is the code the server closes with when the token lacks the scope (1008)
	// and CloseCodeAuthFailed the one for an authentication it refuses (4401). Neither is retried:
	// retrying a refused credential is load, not recovery.
	CloseCodePolicyViolation = 1008
	CloseCodeAuthFailed      = 4401
)

// ConnectorClosed is a close frame WITH its code, so a caller can tell a refused credential from a
// dropped network. Before this existed the payload was discarded and every close read as io.EOF, which
// made "no retry on 1008 or 4401" impossible to implement.
type ConnectorClosed struct {
	Code   int
	Reason string
}

func (e *ConnectorClosed) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("the connector closed with code %d", e.Code)
	}
	return fmt.Sprintf("the connector closed with code %d: %s", e.Code, e.Reason)
}

// Refused reports whether the CREDENTIAL itself was refused, in which case the client reports and stops
// rather than reconnecting (2.2).
func (e *ConnectorClosed) Refused() bool {
	return e.Code == CloseCodePolicyViolation || e.Code == CloseCodeAuthFailed
}

// ConnectorSocket is the socket the long-running verb drives: frames out, frames in. It is the seam the
// daemon's tests replace, because a fake socket is the only way to prove the reconnect, the stop-and-wait
// ordering and the close-code rules without a server.
type ConnectorSocket interface {
	WriteFrame(v any) error
	ReadFrame() (map[string]any, error)
	// SetDeadline bounds one read. The daemon needs it for the stop-and-wait ack timeout and for its
	// non-blocking drain, without which a silent server would freeze the loop.
	SetDeadline(t time.Time) error
	Close() error
}

// dialConnector is the one place the daemon opens a socket, as a variable so a test drives a scripted
// one.
var dialConnector = func(ctx context.Context, baseURL, token string) (ConnectorSocket, error) {
	return DialConnector(ctx, baseURL, token)
}

// connectorTLS is the TLS seam for a wss:// dial: production verifies the peer against the system roots
// and the host name it dialled, and a test replaces this to trust the certificate its own listener
// presents. There is no insecure mode - nothing here, and nothing in production, sets
// InsecureSkipVerify.
var connectorTLS = func(host string) *tls.Config { return &tls.Config{ServerName: host} }

// opcodes this client writes and reads. Continuations are accepted on read (a server is free to
// fragment) but never produced on write: every frame this client sends is one small JSON object.
const (
	opContinuation = 0x0
	opText         = 0x1
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// connectorConn is one live /ws/connector socket.
type connectorConn struct {
	conn net.Conn
	r    *bufio.Reader
}

// DialConnector opens the connector socket. baseURL is the cloud's http(s) URL - the same one every
// other verb takes - and is translated to ws(s) here rather than accepted as a second flag, so a
// caller cannot point the verbs at one host and the connector at another.
func DialConnector(ctx context.Context, baseURL, token string) (*connectorConn, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("cannot parse the cloud URL: %w", err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return nil, fmt.Errorf("the cloud URL must be http or https, got %q", u.Scheme)
	}
	u.Path = connectorPath
	u.RawQuery = ""

	host := u.Host
	if !strings.Contains(host, ":") {
		if u.Scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	d := net.Dialer{Timeout: connectorTimeout}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", host, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(connectorTimeout))
	}

	// A wss:// base URL means the upgrade goes INSIDE TLS. Writing it onto the raw TCP socket - which is
	// what this dial did - put plaintext on a TLS port, so every handshake ended at the terminator's own
	// refusal (`400 The plain HTTP request was sent to HTTPS port`) instead of at the application, and no
	// session was ever registered. The deadline above covers the handshake as well as the request, and a
	// ws:// base URL keeps the plaintext path unchanged.
	if u.Scheme == "wss" {
		tlsConn := tls.Client(conn, connectorTLS(u.Hostname()))
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("cannot complete TLS with %s: %w", host, err)
		}
		conn = tlsConn
	}

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("cannot generate the websocket key: %w", err)
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	req := "GET " + u.RequestURI() + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Authorization: Bearer " + token + "\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("cannot send the upgrade request: %w", err)
	}

	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("cannot read the upgrade response: %w", err)
	}
	if !strings.Contains(status, "101") {
		// The server refuses a bad or scoped-out token by completing the handshake and closing with
		// 1008 (wsRejectUpgrade), so anything else here is a genuine handshake failure. The reason is
		// reported WITHOUT the request line, which carries the Authorization header.
		_ = conn.Close()
		return nil, fmt.Errorf("the connector refused the upgrade: %s", strings.TrimSpace(status))
	}
	accept := ""
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("cannot read the upgrade headers: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "Sec-WebSocket-Accept") {
			accept = strings.TrimSpace(value)
		}
	}
	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	if want := base64.StdEncoding.EncodeToString(h.Sum(nil)); accept != want {
		_ = conn.Close()
		return nil, errors.New("the connector answered a handshake this client cannot accept")
	}

	_ = conn.SetDeadline(time.Time{})
	return &connectorConn{conn: conn, r: br}, nil
}

func (c *connectorConn) Close() error { return c.conn.Close() }

// SetDeadline bounds the next read, which is how the daemon gets an ack timeout and a drain that cannot
// block a tick.
func (c *connectorConn) SetDeadline(t time.Time) error { return c.conn.SetDeadline(t) }

// writeText sends one masked text frame. A client MUST mask; the server rejects unmasked frames.
func (c *connectorConn) writeText(payload []byte) error {
	header := []byte{0x80 | opText}
	n := len(payload)
	switch {
	case n < 126:
		header = append(header, byte(n)|0x80)
	case n <= 0xFFFF:
		header = append(header, 126|0x80, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(n))
	default:
		header = append(header, 127|0x80, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(header[len(header)-8:], uint64(n))
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return fmt.Errorf("cannot generate a frame mask: %w", err)
	}
	header = append(header, mask...)
	masked := make([]byte, n)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	_, err := c.conn.Write(masked)
	return err
}

// readMessage reads one complete message, answering pings and tolerating fragmentation.
func (c *connectorConn) readMessage() ([]byte, error) {
	var message []byte
	for {
		var head [2]byte
		if _, err := io.ReadFull(c.r, head[:]); err != nil {
			return nil, err
		}
		fin := head[0]&0x80 != 0
		opcode := head[0] & 0x0F
		masked := head[1]&0x80 != 0
		length := uint64(head[1] & 0x7F)
		switch length {
		case 126:
			var ext [2]byte
			if _, err := io.ReadFull(c.r, ext[:]); err != nil {
				return nil, err
			}
			length = uint64(binary.BigEndian.Uint16(ext[:]))
		case 127:
			var ext [8]byte
			if _, err := io.ReadFull(c.r, ext[:]); err != nil {
				return nil, err
			}
			length = binary.BigEndian.Uint64(ext[:])
		}
		// Bounded, and the bound is the point: the server counts its cap in characters and bounds
		// reads in bytes (ws_connector_test.go:330,356), so a client that buffered without limit
		// would undo that on its own side.
		if length > 4<<20 {
			return nil, fmt.Errorf("the connector sent a %d-byte frame; this client reads at most 4 MiB", length)
		}
		var maskKey [4]byte
		if masked {
			if _, err := io.ReadFull(c.r, maskKey[:]); err != nil {
				return nil, err
			}
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(c.r, payload); err != nil {
			return nil, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= maskKey[i%4]
			}
		}
		switch opcode {
		case opPing:
			pong := []byte{0x80 | opPong, 0x80, 0, 0, 0, 0}
			if _, err := c.conn.Write(pong); err != nil {
				return nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			// The CODE is the whole point of a close: 1008 means the token lacks the scope and 4401 that
			// it failed authentication, and neither may be retried. An empty payload carries no code, and
			// 1005 is the code RFC6455 reserves for exactly that.
			closed := &ConnectorClosed{Code: 1005}
			if len(payload) >= 2 {
				closed.Code = int(binary.BigEndian.Uint16(payload[:2]))
				closed.Reason = strings.TrimSpace(string(payload[2:]))
			}
			return nil, closed
		case opText, opContinuation:
			message = append(message, payload...)
			if fin {
				return message, nil
			}
		default:
			return nil, fmt.Errorf("the connector sent an unsupported frame opcode %#x", opcode)
		}
	}
}

func (c *connectorConn) writeJSON(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("cannot encode a connector frame: %w", err)
	}
	return c.writeText(payload)
}

func (c *connectorConn) readFrame() (map[string]any, error) {
	message, err := c.readMessage()
	if err != nil {
		return nil, err
	}
	var frame map[string]any
	if err := json.Unmarshal(message, &frame); err != nil {
		return nil, fmt.Errorf("the connector sent a frame this client cannot read: %w", err)
	}
	return frame, nil
}

// WriteFrame and ReadFrame are the socket's two operations under the names the long-running verb uses, so
// it drives a ConnectorSocket rather than this concrete type.
func (c *connectorConn) WriteFrame(v any) error { return c.writeJSON(v) }

// ReadFrame reads one frame.
func (c *connectorConn) ReadFrame() (map[string]any, error) { return c.readFrame() }

// Ready is the server's answer to hello (2.2): the protocol the server will speak, how often it wants a
// ping, and WHICH of the client's capabilities it enables. The capability list is what gates frame kinds:
// a kind whose capability is absent is never sent.
type Ready struct {
	Protocol     int
	HeartbeatS   int
	Capabilities []string
}

// Has reports whether the server enabled one capability.
func (r Ready) Has(capability string) bool {
	for _, enabled := range r.Capabilities {
		if enabled == capability {
			return true
		}
	}
	return false
}

// Hello is the first exchange on a fresh socket: it names the connector and the server answers ready.
// The server pins that hello CANNOT switch the connector id (ws_connector_test.go:412), so the id is a
// parameter of the connection rather than something a later frame can rewrite.
func (c *connectorConn) Hello(connectorID string) error {
	_, err := c.HelloWith(connectorID, ClientVersion, []string{CapabilityIngest})
	return err
}

// HelloWith sends hello with 2.2's protocol, client version and capabilities, and returns the server's
// ready. A server that does not know `protocol` answers 1 - the missing field means 1 on both sides - so
// this does not refuse an old server; it reports what the server will speak.
func (c *connectorConn) HelloWith(connectorID, clientVersion string, capabilities []string) (Ready, error) {
	if connectorID == "" {
		return Ready{}, errors.New("the connector id cannot be empty")
	}
	if err := c.WriteFrame(helloFrame(connectorID, clientVersion, capabilities)); err != nil {
		return Ready{}, err
	}
	frame, err := c.ReadFrame()
	if err != nil {
		return Ready{}, err
	}
	if frame["type"] != "ready" || frame["connector_id"] != connectorID {
		return Ready{}, fmt.Errorf("hello was answered %v", frame)
	}
	return readyOf(frame), nil
}

// helloFrame is the first frame of every connection, in one place: the connector id, the protocol, this
// client's version, and the capabilities it asks the server to enable.
func helloFrame(connectorID, clientVersion string, capabilities []string) map[string]any {
	return map[string]any{
		"type":           "hello",
		"connector_id":   connectorID,
		"protocol":       ClientProtocol,
		"client_version": clientVersion,
		"capabilities":   capabilities,
	}
}

// readyOf reads a ready frame: the protocol the server will speak, its heartbeat interval, and WHICH of
// the client's capabilities it enabled. It is the ONE parser of that frame, so a field added later cannot
// be read by one caller and ignored by another.
func readyOf(frame map[string]any) Ready {
	ready := Ready{Protocol: ClientProtocol, HeartbeatS: defaultHeartbeatS}
	if protocol, ok := frame["protocol"].(float64); ok && int(protocol) > 0 {
		ready.Protocol = int(protocol)
	}
	if heartbeat, ok := frame["heartbeat_s"].(float64); ok && int(heartbeat) > 0 {
		ready.HeartbeatS = int(heartbeat)
	}
	for _, value := range anyList(frame["capabilities"]) {
		if name, ok := value.(string); ok && name != "" {
			ready.Capabilities = append(ready.Capabilities, name)
		}
	}
	return ready
}

// defaultHeartbeatS is the ping interval the server documents (2.2). A server that answers no
// heartbeat_s gets this one rather than a client-chosen faster rate.
const defaultHeartbeatS = 25

// anyList reads a JSON array field whatever its element type, so a malformed capabilities list is an
// empty list rather than a decode failure that costs the whole socket.
func anyList(value any) []any {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	return list
}

// SessionRegistration is one `session` frame (2.2): the registration of a local session. It is re-sent on
// every reconnect and whenever `rig ps --json` shows a state change, and a seat that leaves `rig ps` is
// sent once with state stopped.
type SessionRegistration struct {
	Key    string
	Name   string
	Room   string
	Seat   string
	State  string
	Source string
}

// SessionAck is the server's answer to a registration. Cursor is the stored client position, or "" when
// none was stored: an empty cursor means "start from the source's beginning, bounded by this client's own
// backfill limit" (2.2).
type SessionAck struct {
	SessionID string
	LastSeq   int
	Cursor    string
}

// RegisterSession declares one local session and returns what the cloud assigned it.
//
// room and seat are the seat identity the connector observed: the rig that runs the session, and the
// member inside it. THEY TRAVEL AS A PAIR OR NOT AT ALL - a frame naming a room without a seat, or a
// seat without a room, is one the server would have to refuse or guess about, so the pair is sent
// whole or omitted whole, and a session from a machine that cannot name its seat is still a session.
func (c *connectorConn) RegisterSession(reg SessionRegistration) (SessionAck, error) {
	var ack SessionAck
	if reg.Key == "" {
		return ack, errors.New("the session key cannot be empty")
	}
	frame := sessionFrame(reg)
	if err := c.WriteFrame(frame); err != nil {
		return ack, err
	}
	answer, err := c.ReadFrame()
	if err != nil {
		return ack, err
	}
	if answer["type"] != "session_ack" {
		return ack, fmt.Errorf("the session frame was answered %v", answer)
	}
	ack.SessionID, _ = answer["session_id"].(string)
	if ack.SessionID == "" {
		return ack, errors.New("the session acknowledgement carried no session id")
	}
	if seq, ok := answer["last_seq"].(float64); ok {
		ack.LastSeq = int(seq)
	}
	ack.Cursor, _ = answer["cursor"].(string)
	return ack, nil
}

// sessionFrame is the session frame's body: the key and the name always, the seat identity only when
// BOTH halves are known, and the state and the source 2.2 adds. A transport that names no state leaves
// the field out rather than asserting one the client does not know: `seat_state` on the Sessions page
// distinguishes "no agent has reported a state" (null) from a state, and a guess would erase that.
func sessionFrame(reg SessionRegistration) map[string]any {
	frame := map[string]any{"type": "session", "session_key": reg.Key, "name": reg.Name}
	if reg.Room != "" && reg.Seat != "" {
		frame["room"] = reg.Room
		frame["seat"] = reg.Seat
	}
	if reg.State != "" {
		frame["state"] = reg.State
	}
	if reg.Source != "" {
		frame["source"] = reg.Source
	}
	return frame
}

// EventsAck is the server's answer to one batch: the seq it assigned and the cursor it STORED. The cursor
// and the events commit together (2.1), which is what makes resuming from the acked cursor exact even
// when an ack was lost.
type EventsAck struct {
	LastSeq int
	Cursor  string
}

// MaxEventsPerBatch is the server's own cap (session_stream.MaxEventsPerBatch): a batch of 200. The
// client bounds its batches by the same number rather than discovering the cap from a refusal.
const MaxEventsPerBatch = 200

// AppendEvents sends one batch and returns the server's answer. cursor is this client's opaque position
// in its source, at most 512 bytes: it is stored with the events, so the stored value and "resumable
// from" cannot disagree.
//
// ONE EVENTS FRAME PER SESSION AT A TIME is the caller's rule (2.2, stop-and-wait); this method is the
// single frame, and the daemon is what holds the ordering.
func (c *connectorConn) AppendEvents(sessionKey string, events []map[string]any, cursor string) (EventsAck, error) {
	var ack EventsAck
	frame := map[string]any{"type": "events", "session_key": sessionKey, "events": events}
	if cursor != "" {
		frame["cursor"] = cursor
	}
	if err := c.WriteFrame(frame); err != nil {
		return ack, err
	}
	answer, err := c.ReadFrame()
	if err != nil {
		return ack, err
	}
	if answer["type"] != "events_ack" {
		return ack, fmt.Errorf("the events frame was answered %v", answer)
	}
	if seq, ok := answer["last_seq"].(float64); ok {
		ack.LastSeq = int(seq)
	}
	ack.Cursor, _ = answer["cursor"].(string)
	return ack, nil
}

// MessageEvent wraps a transcript line in the shape the server expects: the connector sends
// {type: "message", payload: ...} entries, which the stream routes render as session events.
func MessageEvent(payload map[string]any) map[string]any {
	return map[string]any{"type": "message", "payload": payload}
}
