package clientruntime

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeActivityDaemon serves the two routes ReadSeatActivity reads - the rigs list and one rig's node table - and
// returns the paths it was asked for, so a test can pin WHICH PROGRAM answered as well as what the answer was. The
// whole point of the seam is that no program is resolved any more: the read is this client's own transport.
func fakeActivityDaemon(t *testing.T, rigID, rigName, nodes string) (*httptest.Server, *[]string) {
	t.Helper()

	asked := &[]string{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, _ *http.Request) {
		*asked = append(*asked, "/api/ps")
		_, _ = io.WriteString(w, `[{"rigId":"`+rigID+`","name":"`+rigName+`","rigName":"`+rigName+`"}]`)
	})
	mux.HandleFunc("/api/rigs/"+rigID+"/nodes", func(w http.ResponseWriter, _ *http.Request) {
		*asked = append(*asked, "/api/rigs/"+rigID+"/nodes")
		_, _ = io.WriteString(w, nodes)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, asked
}

// nodeWithActivity is one node row carrying the activity report, in the shape the daemon serves it.
func nodeWithActivity(logicalID, canonical, word string) string {
	return `{"logicalId":"` + logicalID + `","canonicalSessionName":"` + canonical + `",` +
		`"agentActivity":{"state":"` + word + `","reason":"agent_end","evidenceSource":"runtime_hook"}}`
}

// nodeWithoutActivity is the row the shim-shaped read used to match: a node, and NO activity field at all.
func nodeWithoutActivity(logicalID, canonical string) string {
	return `{"logicalId":"` + logicalID + `","canonicalSessionName":"` + canonical + `"}`
}

func nodesTable(rows ...string) string { return "[" + strings.Join(rows, ",") + "]" }

// TestTheDaemonNodeReadAnswersInTheCLIsShape is the acceptance of the daemon rung: the rows come from
// the daemon's own table, they carry the keys every consumer of `rig ps --nodes --json` already parses, the
// seat half is derived from the logical id because the daemon does not send it, and pid is absent - the one
// key this read is deliberately narrower in, so a consumer that ever needs it has to ask for it by name.
func TestTheDaemonNodeReadAnswersInTheCLIsShape(t *testing.T) {
	const rig = "rigx"
	rows := nodesTable(
		`{"logicalId":"rigx.lead","canonicalSessionName":"rigx-lead@rigx","rigName":"rigx",`+
			`"runtime":"omp","sessionStatus":"running","startupStatus":"ready","model":"deepseek/deepseek-flash"}`,
		`{"logicalId":"rigx.dev","canonicalSessionName":"rigx-dev@rigx","rigName":"rigx",`+
			`"runtime":"omp","sessionStatus":"stopped","startupStatus":"stopped","model":"anthropic/claude-sonnet-4-5"}`,
	)
	srv, asked := fakeActivityDaemon(t, "RID-1", rig, rows)
	t.Setenv(openRigURLEnv, srv.URL)
	t.Setenv(openRigPortEnv, "")

	got, ok, err := daemonNodes(context.Background(), rig)
	if err != nil {
		t.Fatalf("the read refused a served table: %v", err)
	}
	if !ok {
		t.Fatal("a served table came back as no records")
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2 (%v)", len(got), got)
	}
	if len(*asked) != 2 || (*asked)[0] != "/api/ps" {
		t.Errorf("the read asked %v, want the rig list first", *asked)
	}
	first := got[0]
	for key, want := range map[string]any{
		"logicalId": "rigx.lead", "seat": "lead", "canonicalSessionName": "rigx-lead@rigx",
		"rigName": "rigx", "runtime": "omp", "sessionStatus": "running",
		"startupStatus": "ready", "model": "deepseek/deepseek-flash",
	} {
		if first[key] != want {
			t.Errorf("row[%q] = %v, want %v", key, first[key], want)
		}
	}
	if _, present := first["pid"]; present {
		t.Error("pid appeared in a daemon row: this read is documented as narrower than rig ps --nodes --json in exactly that key")
	}
}

// TestTheNodeReadTellsNoRecordsFromAFailedRead: a caller must be able to refuse on a failed read without
// refusing a room that simply has no records yet, so the two must not collapse into one answer. The
// distinction is carried by ok and err rather than by an empty reason string, because a caller that reads
// "nothing said" as success is the exact hole this pass closes.
func TestTheNodeReadTellsNoRecordsFromAFailedRead(t *testing.T) {
	t.Run("a rig the daemon does not know is no records, not a failure", func(t *testing.T) {
		srv, _ := fakeActivityDaemon(t, "RID-1", "otherrig", nodesTable())
		t.Setenv(openRigURLEnv, srv.URL)
		t.Setenv(openRigPortEnv, "")

		got, ok, err := daemonNodes(context.Background(), "rigx")
		if got != nil || ok || err != nil {
			t.Errorf("daemonNodes = (%v, %v, %v), want (nil, false, nil)", got, ok, err)
		}
	})

	t.Run("no daemon named at all is the same absence", func(t *testing.T) {
		t.Setenv(openRigURLEnv, "")
		t.Setenv(openRigPortEnv, "")

		got, ok, err := daemonNodes(context.Background(), "rigx")
		if got != nil || ok || err != nil {
			t.Errorf("daemonNodes = (%v, %v, %v), want (nil, false, nil)", got, ok, err)
		}
	})

	t.Run("a daemon that fails the read is a failure, in words", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"the table is unavailable"}`)
		}))
		t.Cleanup(srv.Close)
		t.Setenv(openRigURLEnv, srv.URL)
		t.Setenv(openRigPortEnv, "")

		got, ok, err := daemonNodes(context.Background(), "rigx")
		if got != nil || ok {
			t.Errorf("a failed read answered with rows: (%v, %v)", got, ok)
		}
		if err == nil {
			t.Fatal("a read that could not answer returned no error: an unreadable reading is not 'this room has no seats'")
		}
		if !strings.Contains(err.Error(), "the node read asked /api/ps") || !strings.Contains(err.Error(), "HTTP 500") {
			t.Errorf("the failure does not say which read failed and with what: %v", err)
		}
	})
}

// TestRoomNodesPrefersTheRuntimeThenTheDaemon is the acceptance of the ONE seam: the runtime answers first
// when it hosts the room, the daemon answers when it does not, and neither being there is no records rather
// than a failure. It also pins the keys the runtime rung adds, which the daemon's table does not carry:
// sessionStatus alongside lifecycleState (the SAME fact under both keys, so a consumer never has to know
// which transport answered), pid, and the seat half derived from the logical id.
func TestRoomNodesPrefersTheRuntimeThenTheDaemon(t *testing.T) {
	t.Run("a runtime that hosts the room answers, and the daemon is not asked", func(t *testing.T) {
		hermeticSeat(t)
		sock, requests := fakeRuntime(t, func(line string) string {
			if !strings.Contains(line, "GetRoomStatus") {
				t.Errorf("the runtime was asked something else: %s", line)
			}
			return okDataResponse(`[{"room":"room-a","seat":"seat-1","session_name":"room-a-seat-1@room-a",` +
				`"runtime":"omp","model":"deepseek/deepseek-flash","pid":4242,"state":"running"}]`)
		})
		t.Setenv(RuntimeSocketEnv, sock)
		// A daemon that would fail the read, so a fallback past the runtime cannot pass by accident.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		t.Setenv(openRigURLEnv, srv.URL)

		nodes, ok, err := RoomNodes(context.Background(), "room-a")
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
		if model, ok := row["model"].(*string); !ok || model == nil || *model != "deepseek/deepseek-flash" {
			t.Errorf("row[model] = %v, want the seat's model", row["model"])
		}
		if pid, ok := row["pid"].(*uint32); !ok || pid == nil || *pid != 4242 {
			t.Errorf("row[pid] = %v, want the runtime's pid - the runtime rung CAN carry it, unlike the daemon's", row["pid"])
		}
		select {
		case line := <-requests:
			if !strings.Contains(line, "GetRoomStatus") {
				t.Errorf("the first runtime frame was %q, want the room status call", line)
			}
		default:
			t.Error("the runtime was never asked: the rows came from somewhere else")
		}
	})

	t.Run("with no runtime, the daemon's table answers", func(t *testing.T) {
		hermeticSeat(t)
		noRuntimeSocket(t)
		const rig = "rigx"
		srv, _ := fakeActivityDaemon(t, "RID-1", rig, nodesTable(
			`{"logicalId":"rigx.lead","canonicalSessionName":"rigx-lead@rigx","rigName":"rigx",`+
				`"runtime":"omp","sessionStatus":"running","lifecycleState":"running","model":"deepseek/deepseek-flash"}`))
		t.Setenv(openRigURLEnv, srv.URL)

		nodes, ok, err := RoomNodes(context.Background(), rig)
		if err != nil || !ok || len(nodes) != 1 {
			t.Fatalf("RoomNodes = (%v, %v, %v), want the daemon's one row", nodes, ok, err)
		}
		if nodes[0]["seat"] != "lead" {
			t.Errorf("row[seat] = %v, want the seat derived from the logical id", nodes[0]["seat"])
		}
	})

	t.Run("neither transport knowing the room is no records, not a failure", func(t *testing.T) {
		hermeticSeat(t)
		noRuntimeSocket(t)

		nodes, ok, err := RoomNodes(context.Background(), "room-a")
		if nodes != nil || ok || err != nil {
			t.Errorf("RoomNodes = (%v, %v, %v), want (nil, false, nil)", nodes, ok, err)
		}
	})
}

// TestReadSeatActivityAsksTheDaemonAndRefusesByDefault is the acceptance of the seam: the word arrives from the
// daemon's own node table, and the predicate's answer over the words the daemon actually serves is the one the
// ruling fixes - only `idle` releases a destroyer, and nothing else may, including a word nobody has seen and the
// empty word an absent field yields.
func TestReadSeatActivityAsksTheDaemonAndRefusesByDefault(t *testing.T) {
	const rig, seat = "rigx", "lead"
	logical := rig + "." + seat
	canonical := SeatSessionName(rig, seat)
	elsewhere := "rigx.other"

	for _, tc := range []struct {
		name        string
		nodes       string
		wantRead    bool
		wantPresent bool
		wantWord    string
		wantRelease bool
	}{
		{"idle releases", nodesTable(nodeWithActivity(logical, canonical, "idle")), true, true, "idle", true},
		{"the stored synonym releases too", nodesTable(nodeWithActivity(logical, canonical, "idle-at-prompt")), true, true, "idle-at-prompt", true},
		{"running forbids", nodesTable(nodeWithActivity(logical, canonical, "running")), true, true, "running", false},
		{"working forbids", nodesTable(nodeWithActivity(logical, canonical, "working")), true, true, "working", false},
		{"needs_input forbids", nodesTable(nodeWithActivity(logical, canonical, "needs_input")), true, true, "needs_input", false},
		{"unknown is not evidence", nodesTable(nodeWithActivity(logical, canonical, "unknown")), true, true, "unknown", false},
		{"the empty word forbids", nodesTable(nodeWithActivity(logical, canonical, "")), true, true, "", false},
		{"a word nobody has seen forbids", nodesTable(nodeWithActivity(logical, canonical, "drifting")), true, true, "drifting", false},
		{"no activity field at all is its own answer", nodesTable(nodeWithoutActivity(logical, canonical)), true, false, "", false},
		{"a table without this seat cannot answer", nodesTable(nodeWithActivity(elsewhere, SeatSessionName(rig, "other"), "idle")), false, false, "", false},
		{"an empty table cannot answer", nodesTable(), false, false, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, asked := fakeActivityDaemon(t, "RID-1", rig, tc.nodes)
			t.Setenv(openRigURLEnv, srv.URL)
			t.Setenv(openRigPortEnv, "")

			read := ReadSeatActivity(context.Background(), rig, seat)

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
				if !read.Present && read.Word != "" {
					t.Error("a reading with no activity field must not carry a word")
				}
			} else if read.Why == "" {
				t.Error("a read that could not answer must say why: an unreadable activity is not the answer 'not running'")
			}
			// WHICH PROGRAM ANSWERED: the daemon, through this client's own transport - both routes, in order, and no
			// `rig` resolved anywhere.
			if len(*asked) != 2 || (*asked)[0] != "/api/ps" || (*asked)[1] != "/api/rigs/RID-1/nodes" {
				t.Fatalf("the read asked %v, want the rigs list then this rig's nodes", *asked)
			}
		})
	}
}

// AND WITH NO DAEMON NAMED THE READ FAILS CLOSED, saying which variable would name one. This is the state a bare
// machine is in, and the direction matters: the readers keep waiting rather than reading an unanswerable question as
// "not running".
func TestReadSeatActivityRefusesWhenNoDaemonIsNamed(t *testing.T) {
	t.Setenv(openRigURLEnv, "")
	t.Setenv(openRigPortEnv, "")

	read := ReadSeatActivity(context.Background(), "rigx", "lead")

	if read.Read {
		t.Fatalf("a read with no daemon named answered: %+v", read)
	}
	for _, want := range []string{openRigURLEnv, openRigPortEnv} {
		if !strings.Contains(read.Why, want) {
			t.Errorf("the refusal %q does not name %s", read.Why, want)
		}
	}
}

// AND A RIG THE LIST DOES NOT CARRY REFUSES BY NAME rather than answering for some other rig: the node route is keyed
// on an id resolved from the list, so a list that does not carry this rig means nothing can be asked about it.
func TestReadSeatActivityRefusesARigTheListDoesNotCarry(t *testing.T) {
	srv, _ := fakeActivityDaemon(t, "RID-1", "some-other-rig", nodesTable())
	t.Setenv(openRigURLEnv, srv.URL)
	t.Setenv(openRigPortEnv, "")

	read := ReadSeatActivity(context.Background(), "rigx", "lead")

	if read.Read {
		t.Fatalf("a rig absent from the list answered: %+v", read)
	}
	if !strings.Contains(read.Why, "no rig named rigx") {
		t.Errorf("the refusal %q does not name the rig it could not find", read.Why)
	}
}
