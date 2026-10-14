package clientruntime

import (
	"strings"
	"testing"
)

// sessionRow is one ListSessions row carrying the activity report, in the shape the runtime serves it.
func sessionRow(logicalID, canonical, word string) string {
	return `{"logicalId":"` + logicalID + `","sessionName":"` + canonical + `","canonicalSessionName":"` + canonical + `",` +
		`"agentActivity":{"state":"` + word + `","source":"runtime_hook"}}`
}

// sessionRowWithoutActivity is a seat the runtime lists with NO activity field at all.
func sessionRowWithoutActivity(logicalID, canonical string) string {
	return `{"logicalId":"` + logicalID + `","sessionName":"` + canonical + `","canonicalSessionName":"` + canonical + `"}`
}

func sessionList(rows ...string) string { return "[" + strings.Join(rows, ",") + "]" }

// TestRoomNodesAnswersFromTheRuntimesRoomStatus pins the keys the node rows carry: sessionStatus alongside
// lifecycleState (the SAME fact under both keys), pid, and the seat half of the logical id.
func TestRoomNodesAnswersFromTheRuntimesRoomStatus(t *testing.T) {
	hermeticSeat(t)
	sock, requests := fakeRuntime(t, func(line string) string {
		if !strings.Contains(line, "GetRoomStatus") {
			t.Errorf("the runtime was asked something else: %s", line)
		}
		return okDataResponse(`[{"room":"room-a","seat":"seat-1","session_name":"room-a-seat-1@room-a",` +
			`"runtime":"omp","model":"deepseek/deepseek-flash","pid":4242,"state":"running"}]`)
	})
	t.Setenv(RuntimeSocketEnv, sock)

	nodes, ok, err := RoomNodes("room-a")
	if err != nil || !ok {
		t.Fatalf("RoomNodes = (%v, %v, %v), want the runtime's rows", nodes, ok, err)
	}
	if len(nodes) != 1 {
		t.Fatalf("rows = %d, want 1 (%v)", len(nodes), nodes)
	}
	row := nodes[0]
	for key, want := range map[string]any{
		"rigName": "room-a", "logicalId": "room-a.seat-1", "seat": "seat-1", "runtime": "omp",
		"sessionStatus": "running", "lifecycleState": "running",
	} {
		if row[key] != want {
			t.Errorf("row[%q] = %v, want %v", key, row[key], want)
		}
	}
	if model, ok := row["model"].(string); !ok || model != "deepseek/deepseek-flash" {
		t.Errorf("row[model] = %v, want the seat's model", row["model"])
	}
	if pid, ok := row["pid"].(*uint32); !ok || pid == nil || *pid != 4242 {
		t.Errorf("row[pid] = %v, want the runtime's pid", row["pid"])
	}
	_ = awaitRequest(t, requests)
}

// TestRoomNodesTellsNoRecordsFromAFailedRead: a room with no seats is no records; a runtime that cannot be
// asked is an error. The two must not collapse into one answer.
func TestRoomNodesTellsNoRecordsFromAFailedRead(t *testing.T) {
	t.Run("a room with no seats is no records", func(t *testing.T) {
		hermeticSeat(t)
		sock, _ := fakeRuntime(t, func(string) string { return okDataResponse(`[]`) })
		t.Setenv(RuntimeSocketEnv, sock)

		nodes, ok, err := RoomNodes("room-a")
		if nodes != nil || ok || err != nil {
			t.Errorf("RoomNodes = (%v, %v, %v), want (nil, false, nil)", nodes, ok, err)
		}
	})

	t.Run("a room the runtime has no spec for is no records, not a failure", func(t *testing.T) {
		hermeticSeat(t)
		sock, _ := fakeRuntime(t, func(string) string {
			return `{"ok":false,"error":"cannot read room spec: No such file or directory (os error 2)","data":null}` + "\n"
		})
		t.Setenv(RuntimeSocketEnv, sock)

		nodes, ok, err := RoomNodes("room-not-started-yet")
		if nodes != nil || ok || err != nil {
			t.Errorf("RoomNodes = (%v, %v, %v), want (nil, false, nil): a room that is not started has no records", nodes, ok, err)
		}
	})

	t.Run("a refusal that is not about the room spec is a failure", func(t *testing.T) {
		hermeticSeat(t)
		sock, _ := fakeRuntime(t, func(string) string {
			return `{"ok":false,"error":"the runtime is shutting down","data":null}` + "\n"
		})
		t.Setenv(RuntimeSocketEnv, sock)

		if _, ok, err := RoomNodes("room-a"); ok || err == nil {
			t.Errorf("RoomNodes ok=%v err=%v, want an error", ok, err)
		}
	})

	t.Run("no runtime is a failure", func(t *testing.T) {
		hermeticSeat(t)
		noRuntimeSocket(t)

		nodes, ok, err := RoomNodes("room-a")
		if nodes != nil || ok || err == nil {
			t.Errorf("RoomNodes = (%v, %v, %v), want an error and no rows", nodes, ok, err)
		}
	})
}

// TestReadSeatActivityAsksTheRuntimeAndRefusesByDefault: the word arrives from the runtime's own session
// list, and only `idle` (and its stored synonym) releases a destroyer; nothing else may.
func TestReadSeatActivityAsksTheRuntimeAndRefusesByDefault(t *testing.T) {
	const rig, seat = "rigx", "lead"
	logical := rig + "." + seat
	canonical := SeatSessionName(rig, seat)
	elsewhere := "rigx.other"

	for _, tc := range []struct {
		name        string
		rows        string
		wantRead    bool
		wantPresent bool
		wantWord    string
		wantRelease bool
	}{
		{"idle releases", sessionList(sessionRow(logical, canonical, "idle")), true, true, "idle", true},
		{"the stored synonym releases too", sessionList(sessionRow(logical, canonical, "idle-at-prompt")), true, true, "idle-at-prompt", true},
		{"running forbids", sessionList(sessionRow(logical, canonical, "running")), true, true, "running", false},
		{"working forbids", sessionList(sessionRow(logical, canonical, "working")), true, true, "working", false},
		{"needs_input forbids", sessionList(sessionRow(logical, canonical, "needs_input")), true, true, "needs_input", false},
		{"unknown is not evidence", sessionList(sessionRow(logical, canonical, "unknown")), true, true, "unknown", false},
		{"a word nobody has seen forbids", sessionList(sessionRow(logical, canonical, "drifting")), true, true, "drifting", false},
		{"no activity field at all is its own answer", sessionList(sessionRowWithoutActivity(logical, canonical)), true, false, "", false},
		{"a list without this seat cannot answer", sessionList(sessionRow(elsewhere, SeatSessionName(rig, "other"), "idle")), false, false, "", false},
		{"an empty list cannot answer", sessionList(), false, false, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hermeticSeat(t)
			sock, requests := fakeRuntime(t, func(string) string { return okDataResponse(tc.rows) })
			t.Setenv(RuntimeSocketEnv, sock)

			read := ReadSeatActivity(rig, seat)

			if read.Read != tc.wantRead {
				t.Fatalf("Read = %v, want %v (Word %q, Why %q)", read.Read, tc.wantRead, read.Word, read.Why)
			}
			if read.Present != tc.wantPresent {
				t.Errorf("Present = %v, want %v", read.Present, tc.wantPresent)
			}
			if read.Word != tc.wantWord {
				t.Errorf("Word = %q, want %q", read.Word, tc.wantWord)
			}
			if read.Read {
				if releases, _ := ActivityReleasesTheWait(read.Word); releases != tc.wantRelease {
					t.Errorf("ActivityReleasesTheWait(%q) = %v, want %v - only idle may release a destroyer",
						read.Word, releases, tc.wantRelease)
				}
			} else if read.Why == "" {
				t.Error("a read that could not answer must say why: an unreadable activity is not the answer 'not running'")
			}
			if got := awaitRequest(t, requests); got != `{"action":"ListSessions","payload":{"room":"`+rig+`"}}` {
				t.Errorf("frame = %q, want the room's session list", got)
			}
		})
	}
}

// With no runtime the read fails closed, with words: the readers keep waiting rather than reading an
// unanswerable question as "not running".
func TestReadSeatActivityRefusesWhenNoRuntimeAnswers(t *testing.T) {
	hermeticSeat(t)
	noRuntimeSocket(t)

	read := ReadSeatActivity("rigx", "lead")

	if read.Read {
		t.Fatalf("a read with no runtime answered: %+v", read)
	}
	if !strings.Contains(read.Why, "could not ask the runtime") {
		t.Errorf("the refusal %q does not say the runtime could not be asked", read.Why)
	}
}
