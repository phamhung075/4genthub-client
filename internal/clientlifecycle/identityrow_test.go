package clientlifecycle

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// stubIdentityNodes replaces the room's node list, so a case drives attribution, an unset node and an
// unreadable list without a daemon.
func stubIdentityNodes(t *testing.T, nodes []map[string]any) {
	t.Helper()
	original := identityNodes
	identityNodes = func(string) []map[string]any { return nodes }
	t.Cleanup(func() { identityNodes = original })
}

// A room whose nodes are all named by the daemon is the healthy row, and it still reports each identity it
// read and the standing fact that an identity is a CLAIM: a row read as proof of authorship is worse than
// no row at all.
func TestTheIdentityRowNamesEveryAttributedNode(t *testing.T) {
	stubIdentityNodes(t, []map[string]any{
		{"canonicalSessionName": "demo-lead@demo", "logicalId": "demo.lead", "runtime": "omp"},
		{"canonicalSessionName": "demo-owner@demo", "logicalId": "demo.owner", "runtime": "terminal"},
	})

	row := identityRowOf("demo")
	if row.Row.State != clientservices.Running {
		t.Errorf("state = %q, want %q", row.Row.State, clientservices.Running)
	}
	if len(identityReasonLines(row)) != 0 {
		t.Errorf("reasons = %v, want none while every node is named", identityReasonLines(row))
	}
	lines := strings.Join(identityLines(row), "\n")
	for _, want := range []string{"demo-lead@demo", "demo-owner@demo", "terminal", "never who sent it"} {
		if !strings.Contains(lines, want) {
			t.Errorf("lines = %q, want %q", lines, want)
		}
	}
}

// The ruling's failure: a node the daemon cannot name at all sends as `<unknown sender>` forever.
func TestANodeWithNoIdentityIsTheFailingRow(t *testing.T) {
	stubIdentityNodes(t, []map[string]any{
		{"canonicalSessionName": "demo-lead@demo", "logicalId": "demo.lead", "runtime": "omp"},
		{"runtime": "terminal"},
	})

	row := identityRowOf("demo")
	if row.Row.State != clientservices.IdentityUnset {
		t.Errorf("state = %q, want %q", row.Row.State, clientservices.IdentityUnset)
	}
	reasons := identityReasonLines(row)
	if len(reasons) != 1 {
		t.Fatalf("reasons = %v, want one line for the unnamed node", reasons)
	}
	if !strings.Contains(reasons[0], clientservices.IdentityUnset) || !strings.Contains(reasons[0], "<unknown sender>") {
		t.Errorf("reason = %q, want the code and what its messages arrive as", reasons[0])
	}
	if len(row.Sent) != 1 || !strings.Contains(row.Sent[0], "demo-lead@demo") {
		t.Errorf("sent = %v, want the one node the daemon can name", row.Sent)
	}
}

// A logical id the daemon resolves by is an identity too: the node is named even when no session name was
// stamped, which is the `resolvedBy: node_id` case.
func TestANodeWithOnlyALogicalIdIsAttributed(t *testing.T) {
	stubIdentityNodes(t, []map[string]any{{"logicalId": "demo.owner", "runtime": "terminal"}})

	row := identityRowOf("demo")
	if row.Row.State != clientservices.Running {
		t.Errorf("state = %q, want %q", row.Row.State, clientservices.Running)
	}
	if len(row.Sent) != 1 || !strings.Contains(row.Sent[0], "demo.owner") {
		t.Errorf("sent = %v, want the node named by its logical id", row.Sent)
	}
}

// A node list this client could not read is a NOTE, never a failure, like every other check row.
func TestAnUnreadableNodeListIsANote(t *testing.T) {
	stubIdentityNodes(t, nil)

	row := identityRowOf("demo")
	if row.Row.State != clientservices.Running {
		t.Errorf("state = %q, want %q: a row that could not look must not refuse", row.Row.State, clientservices.Running)
	}
	if !strings.Contains(row.Note, "not observed") {
		t.Errorf("note = %q, want it to say the row was not observed", row.Note)
	}
}

// The wiring: doctor prints the row and the unnamed node beside it on both surfaces, and refuses while a
// node is unnamed.
func TestDoctorPrintsTheIdentityRowAndItsNode(t *testing.T) {
	rows, orphans, identities := rowsFor, orphanRowsFor, identityRowFor
	rowsFor = func(string, string) []clientservices.Health { return nil }
	orphanRowsFor = func(string) []orphanRow { return nil }
	identityRowFor = func(string) []identityRow {
		return []identityRow{{
			Row: clientservices.Health{
				Rig: "demo", Safeguard: clientservices.Identity,
				State: clientservices.IdentityUnset, AgeS: clientservices.UnknownAge,
			},
			Failures: []identityFinding{{
				Node: "demo-owner@demo", Code: clientservices.IdentityUnset,
				Detail: "the node record names no sender identity, so its messages arrive as <unknown sender>",
			}},
		}}
	}
	t.Cleanup(func() { rowsFor, orphanRowsFor, identityRowFor = rows, orphans, identities })

	var stdout, stderr bytes.Buffer
	if code := runDoctor("demo", t.TempDir(), false, &stdout, &stderr); code == 0 {
		t.Error("doctor exited 0 although a node has no sender identity")
	}
	for what, got := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String()} {
		if !strings.Contains(got, "demo-owner@demo") {
			t.Errorf("%s does not name the node:\n%s", what, got)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := runDoctor("demo", t.TempDir(), true, &stdout, &stderr); code == 0 {
		t.Error("doctor --json exited 0 although a node has no sender identity")
	}
	var decoded []struct {
		clientservices.Health
		Reasons []string `json:"reasons"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("--json is not the rows: %v", err)
	}
	found := false
	for _, row := range decoded {
		if row.Safeguard != clientservices.Identity {
			continue
		}
		found = true
		if !strings.Contains(strings.Join(row.Reasons, "\n"), "demo-owner@demo") {
			t.Errorf("the JSON row's reasons = %v, want the unnamed node", row.Reasons)
		}
	}
	if !found {
		t.Errorf("--json rows do not carry the identity row:\n%s", stdout.String())
	}
}
