package clientruntime

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// THE UNDECODABLE-ANSWER CONTRACT, one case per verb that reads a document out of a frame the daemon
// ANSWERED. Every verb below used to discard the decode error and carry on with a zero value, which is the
// one answer a caller must never be handed: `Call` has already proved the daemon answered (`ok` is true), so
// the value the verb returns IS read as that answer. A discarded failure therefore rendered as a real one -
// an empty identity for Whoami, "no rooms" for ListAllRooms, a row with no id/state/owner for a refused
// write, and (for `up`) a room that looks not-up, which is a decision to START it.
//
// TWO MALFORMED SHAPES, because they enter at two different places:
//
//   - WRONG SHAPE: the frame decodes, and the document under `data` is the wrong JSON KIND for the verb
//     (an object where an array belongs, an array where a row or an object belongs). This is the case the
//     seven sites swallowed, and the case this test file exists to keep refused.
//   - TRUNCATED: the answer line itself arrives cut short mid-document. THIS CANNOT REACH A VERB'S OWN
//     DECODE: `Call` reads one line and parses the envelope before any verb sees `data`, so a truncated
//     body is refused by that envelope guard (ipc.go, the `decode error` branch) and never reaches the
//     seven sites. The test pins the boundary rather than pretending otherwise: the answer is refused and
//     carries no value, and the exit code asserted below is the room contract's mapping of "this answer
//     could not be read", which is what the room verbs do with any daemon failure (reportDaemon /
//     queueDaemonRefusal both return ExitRoomRefused).

// undecodableCall is one affected verb, called the way a caller calls it. It returns the value the caller
// would render or decide on, or an untyped nil when nothing was read - a typed nil inside an `any` would
// hide an empty slice behind the "no value" check, so each closure folds it away.
type undecodableCall struct {
	verb string
	call func(*Client) (any, error)
}

// sliceValue and ptrValue fold an empty/nil answer into an untyped nil, so the assertion reads "no value
// arrived" rather than "a typed nil arrived".
func sliceValue[T any](v []T) any {
	if v == nil {
		return nil
	}
	return v
}

func ptrValue[T any](v *T) any {
	if v == nil {
		return nil
	}
	return v
}

func undecodableCalls() []undecodableCall {
	return []undecodableCall{
		{"StartRoom", func(c *Client) (any, error) {
			statuses, err := c.StartRoom(RoomSpec{Room: queueTestRoom})
			return sliceValue(statuses), err
		}},
		{"RestartSeat", func(c *Client) (any, error) {
			status, err := c.RestartSeat(queueTestRoom, "seat-1")
			return ptrValue(status), err
		}},
		{"GetRoomStatus", func(c *Client) (any, error) {
			statuses, err := c.GetRoomStatus(queueTestRoom)
			return sliceValue(statuses), err
		}},
		{"Whoami", func(c *Client) (any, error) {
			who, err := c.Whoami("agenthub--room-a--seat-1")
			return ptrValue(who), err
		}},
		{"QueueCreate", func(c *Client) (any, error) {
			row, err := c.QueueCreate(queueTestRoom, QueueCreateInput{
				SourceSession:      "sess-src",
				DestinationSession: "sess-dst",
				Summary:            "wire the rail",
			})
			return ptrValue(row), err
		}},
		{"QueueList", func(c *Client) (any, error) {
			rows, err := c.QueueList(queueTestRoom, QueueListFilter{})
			return sliceValue(rows), err
		}},
		{"QueueShow", func(c *Client) (any, error) {
			row, err := c.QueueShow(queueTestRoom, "q-item-7")
			return ptrValue(row), err
		}},
		{"QueueUpdate", func(c *Client) (any, error) {
			note := "still waiting on the schema"
			row, err := c.QueueUpdate(queueTestRoom, QueueUpdateInput{
				QitemID:        "q-item-7",
				ActorSession:   "sess-src",
				TransitionNote: &note,
			})
			return ptrValue(row), err
		}},
		{"QueueClaim", func(c *Client) (any, error) {
			row, err := c.QueueClaim(queueTestRoom, "q-item-7", "sess-dst")
			return ptrValue(row), err
		}},
		{"QueueClose", func(c *Client) (any, error) {
			row, err := c.QueueClose(queueTestRoom, QueueCloseInput{
				QitemID:       "q-item-7",
				ActorSession:  "sess-src",
				ClosureReason: "canceled",
			})
			return ptrValue(row), err
		}},
		{"ListAllRooms", func(c *Client) (any, error) {
			rooms, err := c.ListAllRooms()
			return sliceValue(rooms), err
		}},
	}
}

// arrayAnswerVerbs are the verbs whose answer document IS an array; the other verbs' answers are objects.
// The wrong shape therefore differs per verb, and the helper below names which is which ONCE.
var arrayAnswerVerbs = map[string]bool{
	"StartRoom":     true,
	"GetRoomStatus": true,
	"QueueList":     true,
	"ListAllRooms":  true,
}

// wrongShapeAnswer frames one successful response whose document is a different JSON kind than the verb
// asks for: an object where an array belongs, an array where an object belongs.
func wrongShapeAnswer(verb string) string {
	if arrayAnswerVerbs[verb] {
		return okDataResponse(`{"not":"the array this verb reads"}`)
	}
	return okDataResponse(`["not the object this verb reads"]`)
}

// truncatedAnswer frames the answer as it arrives when the daemon's line was cut short mid-document: the
// frame cannot be read at all, which is what `Call`'s own envelope guard refuses.
func truncatedAnswer(verb string) string {
	if arrayAnswerVerbs[verb] {
		return okDataResponse(`[{"room":"room-a","seat":"seat-1"`)
	}
	return okDataResponse(`{"qitem_id":"q-item-7","state":"pend`)
}

// requestLine reads the request the client wrote, so a refusal is proven to be about the ANSWER and not
// about a request that was never sent.
func requestLine(t *testing.T, requests <-chan string) string {
	t.Helper()

	select {
	case line, ok := <-requests:
		if !ok {
			t.Fatal("the client never wrote a request line")
		}
		return line
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the client's request line")
	}
	return ""
}

// TestUndecodableAnswersAreRefused is the acceptance for the seven sites: two malformed shapes per affected
// verb, each of which must (1) be refused, (2) carry NO value a caller could proceed on, and (3) exit 3 -
// the room-scoped contract's "the daemon did not serve this request". Before the guard, every case below
// returned a nil error that made the zero value look like an answer.
func TestUndecodableAnswersAreRefused(t *testing.T) {
	for _, tc := range undecodableCalls() {
		shapes := []struct {
			name     string
			response string
		}{
			{"wrong-shape", wrongShapeAnswer(tc.verb)},
			{"truncated", truncatedAnswer(tc.verb)},
		}
		for _, shape := range shapes {
			t.Run(tc.verb+"/"+shape.name, func(t *testing.T) {
				client, requests := captureQueueRequest(t, shape.response)

				value, err := tc.call(client)

				if line := requestLine(t, requests); !strings.Contains(line, `"action":"`+tc.verb+`"`) {
					t.Fatalf("request frame = %q, want the %s action", line, tc.verb)
				}
				if err == nil {
					t.Fatalf("an undecodable %s answer was accepted; value = %#v", tc.verb, value)
				}
				if value != nil {
					t.Fatalf("an undecodable %s answer carried a value (%#v) beside its error", tc.verb, value)
				}
				if code := clientcmd.CodeOf(err, clientcmd.ExitRoomRefused); code != clientcmd.ExitRoomRefused {
					t.Fatalf("exit code = %d, want %d (the room contract's refusal)", code, clientcmd.ExitRoomRefused)
				}
			})
		}
	}
}

// TestAWrongShapeRefusalNamesTheAnswerThatCouldNotBeRead pins WHAT the refusal says, for the case that
// reaches a verb's own decode: the verb by name, and the fact that the document's SHAPE is what could not
// be read - not a missing transport, and not a daemon that refused the action.
func TestAWrongShapeRefusalNamesTheAnswerThatCouldNotBeRead(t *testing.T) {
	for _, tc := range undecodableCalls() {
		t.Run(tc.verb, func(t *testing.T) {
			client, requests := captureQueueRequest(t, wrongShapeAnswer(tc.verb))

			_, err := tc.call(client)
			_ = requestLine(t, requests)

			var coded *clientcmd.CodedError
			if !errors.As(err, &coded) {
				t.Fatalf("refusal = %T (%v), want a *clientcmd.CodedError carrying the exit code", err, err)
			}
			if coded.Code != clientcmd.ExitRoomRefused {
				t.Fatalf("refusal code = %d, want %d", coded.Code, clientcmd.ExitRoomRefused)
			}
			for _, want := range []string{tc.verb, "could not be read", "shape"} {
				if !strings.Contains(coded.Message, want) {
					t.Fatalf("refusal %q does not name %q", coded.Message, want)
				}
			}
		})
	}
}

// TestTheZeroValueIsNeverAWrongAnswer names the two zero values that read as real measurements - the ones
// the task's own rule calls out: an undecodable Whoami must never answer with an EMPTY IDENTITY, and an
// undecodable room list must never answer as NO ROOMS. Both are asserted on the value the caller gets,
// which must be absent (nil) rather than empty.
func TestTheZeroValueIsNeverAWrongAnswer(t *testing.T) {
	t.Run("Whoami", func(t *testing.T) {
		client, requests := captureQueueRequest(t, wrongShapeAnswer("Whoami"))

		who, err := client.Whoami("agenthub--room-a--seat-1")
		_ = requestLine(t, requests)

		if who != nil {
			t.Fatalf("an unreadable identity answered with one: %+v", who)
		}
		if err == nil {
			t.Fatal("an unreadable identity answered with a nil error")
		}
	})

	t.Run("ListAllRooms", func(t *testing.T) {
		client, requests := captureQueueRequest(t, wrongShapeAnswer("ListAllRooms"))

		rooms, err := client.ListAllRooms()
		_ = requestLine(t, requests)

		if rooms != nil {
			t.Fatalf("an unreadable room list answered as no rooms: %v", rooms)
		}
		if err == nil {
			t.Fatal("an unreadable room list answered with a nil error")
		}
	})
}

// TestDecodableAnswersStillDecode is the other half of the contract and the reason the guard is a shape
// check rather than a stricter reader: a payload the daemon really sends still decodes to the same value,
// and unknown FIELDS are still tolerated (DisallowUnknownFields is used nowhere in this package). Without
// this, "refuse more" could pass every test above while breaking every working verb.
func TestDecodableAnswersStillDecode(t *testing.T) {
	const seatStatusJSON = `{"room":"room-a","seat":"seat-1","session_name":"agenthub--room-a--seat-1",` +
		`"runtime":"omp","model":null,"pid":42,"state":"running","started_at":null,"token_estimate":7,` +
		`"last_active":null,"transcript_path":null,"alert":null,"a_field_this_build_has_never_heard_of":true}`

	cases := []struct {
		verb     string
		response string
		call     func(*Client) (any, error)
		check    func(*testing.T, any)
	}{
		{
			"StartRoom", okDataResponse("[" + seatStatusJSON + "]"),
			undecodableCalls()[0].call,
			func(t *testing.T, value any) {
				statuses := value.([]SeatStatus)
				if len(statuses) != 1 || statuses[0].Seat != "seat-1" || statuses[0].State != "running" {
					t.Fatalf("statuses = %+v, want one running seat-1", statuses)
				}
				if statuses[0].TokenEstimate != 7 || statuses[0].Pid == nil || *statuses[0].Pid != 42 {
					t.Fatalf("statuses[0] = %+v, want token_estimate 7 and pid 42", statuses[0])
				}
			},
		},
		{
			"RestartSeat", okDataResponse(seatStatusJSON),
			undecodableCalls()[1].call,
			func(t *testing.T, value any) {
				status := value.(*SeatStatus)
				if status.Seat != "seat-1" || status.SessionName != "agenthub--room-a--seat-1" {
					t.Fatalf("status = %+v, want seat-1 and its session name", status)
				}
			},
		},
		{
			"GetRoomStatus", okDataResponse("[" + seatStatusJSON + "]"),
			undecodableCalls()[2].call,
			func(t *testing.T, value any) {
				if statuses := value.([]SeatStatus); len(statuses) != 1 || statuses[0].State != "running" {
					t.Fatalf("statuses = %+v, want one running seat-1", statuses)
				}
			},
		},
		{
			"Whoami", okDataResponse(`{"resolved_by":"room","identity":{"rigName":"room-a","memberId":"m1",` +
				`"runtime":"omp","sessionName":"agenthub--room-a--seat-1","logical_id":"room-a.seat-1",` +
				`"role":"seat"},"peers":[],"edges":{"outgoing":[],"incoming":[]},` +
				`"transcript":{"enabled":false},"a_field_this_build_has_never_heard_of":1}`),
			undecodableCalls()[3].call,
			func(t *testing.T, value any) {
				who := value.(*WhoamiResponse)
				if who.ResolvedBy != "room" || who.Identity.LogicalId != "room-a.seat-1" {
					t.Fatalf("who = %+v, want the daemon's identity", who)
				}
			},
		},
		{
			"QueueCreate", okDataResponse(queueTestRowJSON),
			undecodableCalls()[4].call,
			func(t *testing.T, value any) { assertQueueTestRow(t, value.(*QueueRow)) },
		},
		{
			"QueueList", okDataResponse("[" + queueTestRowJSON + "]"),
			undecodableCalls()[5].call,
			func(t *testing.T, value any) {
				rows := value.([]QueueRow)
				if len(rows) != 1 {
					t.Fatalf("rows = %d, want 1", len(rows))
				}
				assertQueueTestRow(t, &rows[0])
			},
		},
		{
			"QueueShow", okDataResponse(queueTestRowJSON),
			undecodableCalls()[6].call,
			func(t *testing.T, value any) { assertQueueTestRow(t, value.(*QueueRow)) },
		},
		{
			"QueueUpdate", okDataResponse(queueTestRowJSON),
			undecodableCalls()[7].call,
			func(t *testing.T, value any) { assertQueueTestRow(t, value.(*QueueRow)) },
		},
		{
			"QueueClaim", okDataResponse(queueTestRowJSON),
			undecodableCalls()[8].call,
			func(t *testing.T, value any) { assertQueueTestRow(t, value.(*QueueRow)) },
		},
		{
			"QueueClose", okDataResponse(queueTestRowJSON),
			undecodableCalls()[9].call,
			func(t *testing.T, value any) { assertQueueTestRow(t, value.(*QueueRow)) },
		},
		{
			"ListAllRooms", okDataResponse(`["room-a","room-b"]`),
			undecodableCalls()[10].call,
			func(t *testing.T, value any) {
				rooms := value.([]string)
				if len(rooms) != 2 || rooms[0] != "room-a" || rooms[1] != "room-b" {
					t.Fatalf("rooms = %v, want [room-a room-b]", rooms)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			client, requests := captureQueueRequest(t, tc.response)

			value, err := tc.call(client)

			// The action is pinned too, so a table entry wired to the wrong verb's call fails here rather
			// than passing as the wrong verb's decode.
			if line := requestLine(t, requests); !strings.Contains(line, `"action":"`+tc.verb+`"`) {
				t.Fatalf("request frame = %q, want the %s action", line, tc.verb)
			}
			if err != nil {
				t.Fatalf("a decodable %s answer was refused: %v", tc.verb, err)
			}
			if value == nil {
				t.Fatalf("a decodable %s answer decoded to no value", tc.verb)
			}
			tc.check(t, value)
		})
	}
}
