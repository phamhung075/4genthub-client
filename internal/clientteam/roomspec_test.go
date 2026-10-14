package clientteam

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The ONE field where "" and ABSENT must not collapse: an omitted permission_policy means the documented
// unattended default, and a present-but-empty one is refused BY NAME. A plain string with omitempty cannot
// express the difference on the wire, which is why the field is a pointer and the mapping has its own
// reading - and this test asserts BOTH cases explicitly, because a check that only tried one of them could
// not see the difference at all.
func TestLoadRoomSpecKeepsAnEmptyPermissionPolicyDistinctFromAnOmittedOne(t *testing.T) {
	empty := ""
	for _, c := range []struct {
		name string
		seat string
		want *string
	}{
		{"omitted takes the documented default", `{"seat_key":"lead","seat_type":"developer","runtime":"omp"}`, nil},
		{"present but empty is sent as empty", `{"seat_key":"lead","seat_type":"developer","runtime":"omp","permission_policy":""}`, &empty},
		{"a stated policy is sent as stated", `{"seat_key":"lead","seat_type":"developer","runtime":"omp","permission_policy":"builtin:yolo"}`, new("builtin:yolo")},
	} {
		t.Run(c.name, func(t *testing.T) {
			directory := t.TempDir()
			body := `{"room":{"slug":"demo"},"seats":[` + c.seat + `],"links":[],"seat_overlays":{}}`
			if err := os.WriteFile(filepath.Join(directory, "team.json"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			spec, err := LoadRoomSpec(directory)
			if err != nil {
				t.Fatalf("LoadRoomSpec: %v", err)
			}
			got := spec.Seats[0].PermissionPolicy
			if c.want == nil {
				if got != nil {
					t.Fatalf("permission policy = %q, want nil so the key is OMITTED", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("permission policy = nil, want %q on the wire", *c.want)
			}
			if *got != *c.want {
				t.Errorf("permission policy = %q, want %q", *got, *c.want)
			}
		})
	}
}

// The reason the client owns this mapping at all: the runtime's by-name start sends no seats, so the
// local definition has to arrive as the spec, one field at a time.
func TestLoadRoomSpecMapsTheLocalDefinition(t *testing.T) {
	directory := writeTeamDefinition(t, "demo-module", "1.0.0", "developer")

	spec, err := LoadRoomSpec(directory)
	if err != nil {
		t.Fatalf("LoadRoomSpec: %v", err)
	}
	if spec.Room != "demo" {
		t.Errorf("room = %q, want demo", spec.Room)
	}
	if spec.Name == nil || *spec.Name != "Demo" {
		t.Errorf("name = %v, want Demo", spec.Name)
	}
	if len(spec.Seats) != 1 {
		t.Fatalf("seats = %d, want 1", len(spec.Seats))
	}
	seat := spec.Seats[0]
	if seat.SeatKey != "lead" || seat.SeatType != "developer" || seat.Runtime != "omp" {
		t.Errorf("seat = %+v, want lead/developer/omp", seat)
	}
	if seat.PermissionPolicy == nil || *seat.PermissionPolicy != "standard" {
		t.Errorf("permission policy = %v, want standard", seat.PermissionPolicy)
	}
	// team.json writes "model": "" and that means "nothing pinned", not a model named nothing: the
	// runtime's field is an Option, and an empty string there would be a seat asking for a model by that
	// name.
	if seat.Model != nil {
		t.Errorf("model = %q, want nil", *seat.Model)
	}
	if len(spec.Links) != 1 {
		t.Fatalf("links = %+v, want one", spec.Links)
	}
	if link := spec.Links[0]; link.From != "lead" || link.Kind != "delegates_to" || link.To != "lead" {
		t.Errorf("link = %+v", link)
	}
	if got := spec.Overlays["lead"]; len(got) != 1 || got[0] != "demo-module" {
		t.Errorf("overlays = %+v, want lead -> demo-module", spec.Overlays)
	}
}

// A directory that holds no definition, and one whose room has no slug, are caller errors that name the
// directory - because `up` has to print which argument is wrong and where it looked.
func TestLoadRoomSpecRefusesWhatCannotBeStarted(t *testing.T) {
	if _, err := LoadRoomSpec(t.TempDir()); err == nil {
		t.Error("a directory with no team.json loaded")
	}

	directory := t.TempDir()
	body := `{"seats":[{"seat_key":"lead","seat_type":"developer","runtime":"omp"}]}`
	if err := os.WriteFile(filepath.Join(directory, "team.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadRoomSpec(directory)
	if err == nil {
		t.Fatal("a definition with no room slug loaded")
	}
	if !strings.Contains(err.Error(), directory) {
		t.Errorf("error did not name the directory it read: %v", err)
	}
}
