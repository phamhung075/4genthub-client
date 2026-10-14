package clientrestore

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// fakeRuntime answers the runtime socket and records each request line. The socket path is short: a long
// temp path overflows the Unix socket limit.
func fakeRuntime(t *testing.T, handler func(line string) string) *[]string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rvt")
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
	var mu sync.Mutex
	seen := &[]string{}
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
				mu.Lock()
				*seen = append(*seen, line)
				mu.Unlock()
				_, _ = conn.Write([]byte(handler(line)))
			}(conn)
		}
	}()
	t.Setenv(clientruntime.RuntimeSocketEnv, sock)
	return seen
}

// A room with one seat running and one stopped restarts only the stopped one: a restart cycles a live agent,
// and `continue` on a room that is partly up must not touch the part that is.
func TestReviveRestartsOnlyTheStoppedSeats(t *testing.T) {
	seen := fakeRuntime(t, func(line string) string {
		switch {
		case strings.Contains(line, `"GetRoomStatus"`):
			return `{"ok":true,"data":[` +
				`{"room":"lab","seat":"dev","session_name":"lab-dev@lab","runtime":"omp","state":"running"},` +
				`{"room":"lab","seat":"qa","session_name":"lab-qa@lab","runtime":"omp","state":"stopped"}]}` + "\n"
		case strings.Contains(line, `"RestartSeat"`):
			return `{"ok":true,"data":{"room":"lab","seat":"qa","session_name":"lab-qa@lab","runtime":"omp","state":"running"}}` + "\n"
		}
		return `{"ok":false,"error":"unexpected"}` + "\n"
	})

	if err := reviveRoom(context.Background(), "lab"); err != nil {
		t.Fatalf("reviveRoom: %v", err)
	}

	var restarts []string
	for _, line := range *seen {
		if strings.Contains(line, `"RestartSeat"`) {
			restarts = append(restarts, line)
		}
	}
	if len(restarts) != 1 || !strings.Contains(restarts[0], `"seat":"qa"`) {
		t.Errorf("restarts = %v, want exactly one, for qa", restarts)
	}
}

// A room the runtime does not know is its refusal, returned and not turned into a start.
func TestReviveReturnsTheRuntimesRefusalForAnUnknownRoom(t *testing.T) {
	seen := fakeRuntime(t, func(string) string { return `{"ok":false,"error":"no such room"}` + "\n" })

	err := reviveRoom(context.Background(), "ghost")

	if err == nil || !strings.Contains(err.Error(), "no such room") {
		t.Fatalf("err = %v, want the runtime's refusal", err)
	}
	for _, line := range *seen {
		if strings.Contains(line, `"RestartSeat"`) || strings.Contains(line, `"StartRoom"`) {
			t.Errorf("a start was sent for an unknown room: %s", line)
		}
	}
}

// A seat that does not start is named, so the log says which one stayed down.
func TestReviveNamesTheSeatsThatDidNotStart(t *testing.T) {
	fakeRuntime(t, func(line string) string {
		if strings.Contains(line, `"GetRoomStatus"`) {
			return `{"ok":true,"data":[{"room":"lab","seat":"qa","session_name":"lab-qa@lab","runtime":"omp","state":"stopped"}]}` + "\n"
		}
		return `{"ok":false,"error":"no key"}` + "\n"
	})

	err := reviveRoom(context.Background(), "lab")

	if err == nil || !strings.Contains(err.Error(), "qa") || !strings.Contains(err.Error(), "no key") {
		t.Fatalf("err = %v, want it to name qa and why", err)
	}
}
