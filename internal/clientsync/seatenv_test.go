package clientsync

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// test_the_store_and_state_root_do_not_follow_home pins the Python test of that name: the store and the
// state root are resolved from the ACCOUNT home, so a decoy $HOME - what the rig orchestrator gives a seat - does not
// move either of them.
func TestTheStoreAndStateRootDoNotFollowHome(t *testing.T) {
	decoy := t.TempDir()
	t.Setenv("HOME", decoy)

	home, err := clientenv.RealHome()
	if err != nil {
		t.Skipf("no account home on this machine: %v", err)
	}
	store, err := clientenv.ResolveSeatStore("")
	if err != nil {
		t.Fatalf("ResolveSeatStore: %v", err)
	}
	if want := filepath.Join(home, ".config", "4genthub", "seats"); store != want {
		t.Errorf("the store followed $HOME: %q, want %q", store, want)
	}
	root, err := clientenv.ResolveStateRoot("")
	if err != nil {
		t.Fatalf("ResolveStateRoot: %v", err)
	}
	if want := filepath.Join(home, ".config", "4genthub"); root != want {
		t.Errorf("the state root followed $HOME: %q, want %q", root, want)
	}
	if strings.HasPrefix(store, decoy) || strings.HasPrefix(root, decoy) {
		t.Errorf("a resolved path is under the decoy home: store=%q root=%q", store, root)
	}
}

// Every sync verb that resolves a store here takes --out, so its refusal names the flag. The machine
// cannot resolve a home on demand, so the seam supplies the failure and the composition under test is
// the real one.
//
// THE VERB NAMED HERE CHANGED WITH THE CUT, not the claim: `sync status` was the verb this read, and it is
// gone with the rigspec route, so the same refusal is read through `sync pull`, which resolves the store
// the same way (resolveSeatStore is the one seam both call).
func TestTheStoreRefusalNamesTheOutFlagTheSyncVerbsTake(t *testing.T) {
	original := seatStore
	seatStore = func(string) (string, error) {
		return "", &clientcmd.CodedError{Message: clientenv.SeatStoreMessage, Code: clientcmd.ExitUsage}
	}
	t.Cleanup(func() { seatStore = original })

	var out, errOut bytes.Buffer
	code := RunPullVerb(context.Background(), []string{"room1", "seat1"}, &out, &errOut)
	if code != clientcmd.ExitUsage {
		t.Fatalf("exit %d, want %d: %s", code, clientcmd.ExitUsage, errOut.String())
	}
	if !strings.Contains(errOut.String(), clientenv.SeatStoreOutFlag) {
		t.Errorf("the refusal does not name the flag the verb accepts: %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), clientenv.SeatStoreMessage) {
		t.Errorf("the refusal dropped the reason: %q", errOut.String())
	}
}
