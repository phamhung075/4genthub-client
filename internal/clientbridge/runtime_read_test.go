package clientbridge

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// fakeRuntimeSocket answers the runtime socket with handler(line) for each request line, and ignores the
// reachability probe (a dial that closes with no line). The socket is short-pathed: a long temp path
// overflows the Unix socket limit.
func fakeRuntimeSocket(t *testing.T, handler func(line string) string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "brt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err != nil {
					return
				}
				_, _ = conn.Write([]byte(handler(line)))
			}(conn)
		}
	}()
	t.Setenv(clientruntime.RuntimeSocketEnv, sock)
}

// TestRuntimeNodesAreTheSeatsEveryHostedRoomReports: the room is both the rig id and the rig name, the
// seat's own diagnostic text arrives as the node's latestError, and the activity word is the node's.
func TestRuntimeNodesAreTheSeatsEveryHostedRoomReports(t *testing.T) {
	fakeRuntimeSocket(t, func(string) string {
		return `{"ok":true,"data":[{"canonicalSessionName":"lab-dev@lab","sessionName":"agenthub--lab--dev",` +
			`"rigName":"lab","logicalId":"lab.dev","runtime":"omp","sessionStatus":"running",` +
			`"agentActivity":{"state":"needs_input"},"detail":"waiting on a dialog"}]}` + "\n"
	})
	nodes, err := runtimeNodes()
	if err != nil {
		t.Fatalf("runtimeNodes: %v", err)
	}
	seats, invalid, _ := BuildSeats(nodes, nil, t.TempDir(), false)
	if invalid != 0 || len(seats) != 1 {
		t.Fatalf("seats = %v invalid = %d, want one valid seat", seats, invalid)
	}
	got := seats[0]
	if got.Room != "lab" || got.Seat != "dev" || got.State != "blocked" || got.Runtime != "omp" || got.Detail != "waiting on a dialog" {
		t.Fatalf("seat = %+v, want lab/dev blocked on omp carrying its detail", got)
	}
	if rooms := RigRooms(nodes); !reflect.DeepEqual(rooms, []rigRoom{{"lab", "lab"}}) {
		t.Fatalf("rooms = %v, want the room as its own rig id", rooms)
	}
}

// TestRuntimeLinksAreTheRoomsDeclaredEdges: the frame names the room, and each link becomes an edge.
func TestRuntimeLinksAreTheRoomsDeclaredEdges(t *testing.T) {
	var asked string
	fakeRuntimeSocket(t, func(line string) string {
		asked = line
		return `{"ok":true,"data":[{"from":"dev","kind":"delegates_to","to":"qa"}]}` + "\n"
	})
	edges, err := runtimeLinks("lab")
	if err != nil {
		t.Fatalf("runtimeLinks: %v", err)
	}
	if !strings.Contains(asked, `"GetRoomLinks"`) || !strings.Contains(asked, `"room":"lab"`) {
		t.Fatalf("frame = %q, want GetRoomLinks for room lab", asked)
	}
	if want := []rigEdge{{"dev", "qa", "delegates_to"}}; !reflect.DeepEqual(edges, want) {
		t.Fatalf("edges = %v, want %v", edges, want)
	}
}

// TestAMachineWithoutARuntimeReportsNoSeatsAndSaysWhy: an absent runtime is an empty report with one note,
// not a failed cycle.
func TestAMachineWithoutARuntimeReportsNoSeatsAndSaysWhy(t *testing.T) {
	t.Setenv(clientruntime.RuntimeSocketEnv, filepath.Join(t.TempDir(), "none.sock"))
	var notes []string
	bridge := NewBridge("pc-1", 0, nil, nil, nil, t.TempDir(), filepath.Join(t.TempDir(), "sync.json"),
		func(line string) { notes = append(notes, line) })
	bridge.Runner = func(_ context.Context, argv ...string) (string, error) { return "", errors.New("absent") }
	payload := bridge.BuildPayload()
	if len(payload.Seats) != 0 || len(payload.Edges) != 0 {
		t.Fatalf("payload = %+v, want no seats and no edges", payload)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "the agenthub runtime is not running") {
		t.Fatalf("notes = %v, want the absent runtime named", notes)
	}
}

// TestRuntimeHumanQueueAsksForTheActiveRowsAddressedToTheRoomsHuman: the frame names the room, the human's
// destination, and the active-only bound; each rail row comes back in the item shape HumanRowFrom reads.
func TestRuntimeHumanQueueAsksForTheActiveRowsAddressedToTheRoomsHuman(t *testing.T) {
	var asked string
	fakeRuntimeSocket(t, func(line string) string {
		asked = line
		return `{"ok":true,"data":[{"qitem_id":"q1","state":"pending","summary":"need a key","body":"b",` +
			`"source_session":"lab-lead@lab","destination_session":"human@lab","chain_of_record":[],` +
			`"priority":"normal","tags":[],"ts_created":"t","ts_updated":"t","transitions":[]}]}` + "\n"
	})
	items, err := runtimeHumanQueue("lab")
	if err != nil {
		t.Fatalf("runtimeHumanQueue: %v", err)
	}
	for _, want := range []string{`"QueueList"`, `"room":"lab"`, `"destination_session":"human@lab"`, `"active_only":true`, `"limit":100`} {
		if !strings.Contains(asked, want) {
			t.Errorf("frame %q is missing %s", asked, want)
		}
	}
	row, ok := HumanRowFrom(items[0], "lab")
	if !ok || row.ID != "q1" || row.Summary != "need a key" || row.Source != "lab-lead@lab" {
		t.Fatalf("row = %+v ok = %v, want the rail row read as a human row", row, ok)
	}
}

// TestRuntimeRoomsAreTheRoomsTheRuntimeHosts: a runtime that is not running hosts none, and says why.
func TestRuntimeRoomsAreTheRoomsTheRuntimeHosts(t *testing.T) {
	fakeRuntimeSocket(t, func(string) string { return `{"ok":true,"data":["lab","dev"]}` + "\n" })
	rooms, err := runtimeRooms()
	if err != nil || !reflect.DeepEqual(rooms, []string{"lab", "dev"}) {
		t.Fatalf("rooms = %v err = %v, want lab and dev", rooms, err)
	}
	t.Setenv(clientruntime.RuntimeSocketEnv, filepath.Join(t.TempDir(), "none.sock"))
	if _, err := runtimeRooms(); !errors.Is(err, errRuntimeAbsent) {
		t.Fatalf("err = %v, want the absent runtime named", err)
	}
}
