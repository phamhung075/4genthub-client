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
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	connectorPath    = "/ws/connector"
	wsGUID           = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	connectorTimeout = 30 * time.Second
)

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
			return nil, io.EOF
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

// Hello is the first exchange on a fresh socket: it names the connector and the server answers ready.
// The server pins that hello CANNOT switch the connector id (ws_connector_test.go:412), so the id is a
// parameter of the connection rather than something a later frame can rewrite.
func (c *connectorConn) Hello(connectorID string) error {
	if connectorID == "" {
		return errors.New("the connector id cannot be empty")
	}
	if err := c.writeJSON(map[string]any{"type": "hello", "connector_id": connectorID}); err != nil {
		return err
	}
	frame, err := c.readFrame()
	if err != nil {
		return err
	}
	if frame["type"] != "ready" || frame["connector_id"] != connectorID {
		return fmt.Errorf("hello was answered %v", frame)
	}
	return nil
}

// RegisterSession declares one local session and returns the session id the cloud assigned it.
func (c *connectorConn) RegisterSession(sessionKey, name string) (string, error) {
	if sessionKey == "" {
		return "", errors.New("the session key cannot be empty")
	}
	if err := c.writeJSON(map[string]any{"type": "session", "session_key": sessionKey, "name": name}); err != nil {
		return "", err
	}
	frame, err := c.readFrame()
	if err != nil {
		return "", err
	}
	if frame["type"] != "session_ack" {
		return "", fmt.Errorf("the session frame was answered %v", frame)
	}
	id, _ := frame["session_id"].(string)
	if id == "" {
		return "", errors.New("the session acknowledgement carried no session id")
	}
	return id, nil
}

// AppendEvents sends one batch and returns the server's last_seq. last_seq is MONOTONE (the server's
// test asserts last_seq == 200*i for a batch of 200), which is what makes it the resume point: the
// client stores it, and deduplication after a reconnect is therefore the CLIENT's job, not the
// server's.
func (c *connectorConn) AppendEvents(sessionKey string, events []map[string]any) (int, error) {
	if err := c.writeJSON(map[string]any{"type": "events", "session_key": sessionKey, "events": events}); err != nil {
		return 0, err
	}
	frame, err := c.readFrame()
	if err != nil {
		return 0, err
	}
	seq, ok := frame["last_seq"].(float64)
	if !ok {
		return 0, fmt.Errorf("the events frame was answered %v", frame)
	}
	return int(seq), nil
}

// MessageEvent wraps a transcript line in the shape the server expects: the connector sends
// {type: "message", payload: ...} entries, which the stream routes render as session events.
func MessageEvent(payload map[string]any) map[string]any {
	return map[string]any{"type": "message", "payload": payload}
}

// Redact removes secret-shaped substrings from a transcript line BEFORE it is uploaded. This is the
// C1 requirement "redact secrets locally" and it runs here, in the client, because after the frame is
// on the wire the secret is already in the cloud.
//
// Each pattern CAPTURES the prefix it must keep - the header word, the variable name and its
// separator, the surrounding space - so the replacement cannot eat a character of real content. The
// first version of this function sliced the match at the separator instead, which silently deleted the
// space after `AGENTHUB_TOKEN: `; the tests caught it, which is why the capture is written out here.
func Redact(text string) string {
	for _, re := range redactionPatterns {
		text = re.ReplaceAllString(text, "${1}[redacted]")
	}
	return text
}

// redactionPatterns are the shapes a secret takes in a transcript, written literally. A redactor that
// guesses is worse than one that misses: a missed shape is visible in review, while a wrong guess
// silently eats real content. GitHub prefixes use an underscore and Slack and OpenAI use a hyphen -
// getting that backwards is what the first version of this list did.
var redactionPatterns = []*regexp.Regexp{
	// An HTTP auth header keeps the scheme and the space.
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._\-]{8,}`),
	// An assignment keeps the variable name and the separator AND any space after it.
	regexp.MustCompile(`(?i)((?:AGENTHUB_TOKEN|AGENTHUB_API_TOKEN|DEEPSEEK_API_KEY|ANTHROPIC_API_KEY|OPENAI_API_KEY|AGENTHUB_PUBLIC_URL)\s*[=:]\s*)\S+`),
	// A provider token is entirely secret, so nothing is kept: these four patterns carry NO capture
	// group, because ${1} expands to the empty string when there is no group and the whole match must
	// go. Wrapping them in parentheses first kept the token and redacted the empty string after it.
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9\-]{10,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{16,}`),
	// A JWT is three base64url segments separated by dots.
	regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`),
}
