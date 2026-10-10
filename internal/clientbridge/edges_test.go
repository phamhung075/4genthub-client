package clientbridge

import (
	"context"
	"reflect"
	"testing"
)

// exportYAML is the Python test's `rig export` fixture: valid edges, a repeat, an unknown kind, a
// self edge, an invalid name, and the trailing status line the CLI prints.
const exportYAML = `version: "0.2"
name: eng
pods:
  - id: dev
    members:
      - id: coder
      - id: reviewer
    edges:
      - {kind: delegates_to, from: coder, to: reviewer}
      - {kind: escalates_to, from: reviewer, to: coder}
      - {kind: delegates_to, from: coder, to: reviewer}
      - {kind: telepathy, from: coder, to: reviewer}
      - {kind: delegates_to, from: coder, to: coder}
      - {kind: delegates_to, from: "bad name", to: coder}
edges: []
Exported to /dev/stdout
`

func TestParseRigEdgesIgnoresTheTrailingStatusLine(t *testing.T) {
	edges, err := ParseRigEdges(exportYAML)
	if err != nil {
		t.Fatal(err)
	}
	want := []rigEdge{{"coder", "reviewer", "delegates_to"}, {"reviewer", "coder", "escalates_to"}}
	if !reflect.DeepEqual(edges[:2], want) {
		t.Fatalf("first edges = %v, want %v", edges[:2], want)
	}
}

func TestParseRigEdgesRejectsASpecWithoutPods(t *testing.T) {
	if _, err := ParseRigEdges("name: eng\n"); err == nil {
		t.Fatal("a spec without pods must be an error")
	}
}

func TestEdgesAreReportedPerRoomAndTheServerRefusableOnesAreDropped(t *testing.T) {
	nodes := []any{map[string]any{"rigId": "r1", "rigName": "eng"}}
	bridge := &Bridge{
		Runner: func(_ context.Context, argv ...string) (string, error) { return exportYAML, nil },
		notes:  map[string]string{},
	}
	got := BuildEdges(RigRooms(nodes), func(id string) []rigEdge { return bridge.readEdges(nil, id) })
	want := []EdgeStatus{
		{"eng", "coder", "reviewer", "delegates_to"},
		{"eng", "reviewer", "coder", "escalates_to"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("edges = %v, want %v", got, want)
	}
}

func TestAnUnexportableRigHasNoEdges(t *testing.T) {
	got := BuildEdges(RigRooms([]any{map[string]any{"rigId": "r1", "rigName": "eng"}}),
		func(string) []rigEdge { return nil })
	if got == nil || len(got) != 0 {
		t.Fatalf("edges = %#v, want an empty non-nil list (the wire needs [] not null)", got)
	}
}
