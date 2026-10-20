package clientbridge

import (
	"reflect"
	"testing"
)

func TestEdgesAreReportedPerRoomAndTheServerRefusableOnesAreDropped(t *testing.T) {
	nodes := []any{map[string]any{"roomId": "eng", "roomName": "eng"}}
	bridge := &Bridge{
		Links: func(room string) ([]roomEdge, error) {
			return []roomEdge{
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
	got := BuildEdges(Rooms(nodes), bridge.readEdges)
	want := []EdgeStatus{
		{"eng", "coder", "reviewer", "delegates_to"},
		{"eng", "reviewer", "coder", "escalates_to"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("edges = %v, want %v", got, want)
	}
}

func TestAnUnreadableRoomHasNoEdges(t *testing.T) {
	got := BuildEdges(Rooms([]any{map[string]any{"roomId": "r1", "roomName": "eng"}}),
		func(string) []roomEdge { return nil })
	if got == nil || len(got) != 0 {
		t.Fatalf("edges = %#v, want an empty non-nil list (the wire needs [] not null)", got)
	}
}
