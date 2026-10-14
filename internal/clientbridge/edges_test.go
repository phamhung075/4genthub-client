package clientbridge

import (
	"reflect"
	"testing"
)

func TestEdgesAreReportedPerRoomAndTheServerRefusableOnesAreDropped(t *testing.T) {
	nodes := []any{map[string]any{"rigId": "eng", "rigName": "eng"}}
	bridge := &Bridge{
		Links: func(room string) ([]rigEdge, error) {
			return []rigEdge{
				{"coder", "reviewer", "delegates_to"},
				{"reviewer", "coder", "escalates_to"},
				{"coder", "reviewer", "delegates_to"}, // a repeat
				{"coder", "reviewer", "telepathy"},    // an unknown kind
				{"coder", "coder", "delegates_to"},    // a self edge
				{"bad name", "coder", "delegates_to"}, // an invalid name
			}, nil
		},
		notes: map[string]string{},
	}
	got := BuildEdges(RigRooms(nodes), bridge.readEdges)
	want := []EdgeStatus{
		{"eng", "coder", "reviewer", "delegates_to"},
		{"eng", "reviewer", "coder", "escalates_to"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("edges = %v, want %v", got, want)
	}
}

func TestAnUnreadableRoomHasNoEdges(t *testing.T) {
	got := BuildEdges(RigRooms([]any{map[string]any{"rigId": "r1", "rigName": "eng"}}),
		func(string) []rigEdge { return nil })
	if got == nil || len(got) != 0 {
		t.Fatalf("edges = %#v, want an empty non-nil list (the wire needs [] not null)", got)
	}
}
