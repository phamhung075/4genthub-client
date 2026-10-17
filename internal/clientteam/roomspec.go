package clientteam

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// LoadRoomSpec reads a team definition directory into the spec the runtime starts a room from.
//
// WHY THE CLIENT OWNS THIS AT ALL: the runtime cannot invent seats. `StartRoom` sends the whole spec,
// seats included, and its by-name form sends only {room, cwd} - so a room that does not already exist
// can only be started by a caller that brought the seats with it. team.json already carries exactly the
// fields SeatSpec wants (seat_key, seat_type, runtime, model, permission_policy, env) and SeatLink's
// (from, kind, to), which is what makes the local definition the WHOLE source: a room starts with no
// cloud round trip. The mapping lives here, next to loadTeam, because team.json has ONE reader.
//
// A definition that cannot be read, or that has no room slug or a seat with no key, is a usage error
// naming the directory - the same shape every other team verb reports, because the caller's argument is
// what is wrong, not the machine.
func LoadRoomSpec(teamDir string) (clientruntime.RoomSpec, error) {
	loaded, err := loadTeam(teamDir)
	if err != nil {
		return clientruntime.RoomSpec{}, err
	}
	room := stringField(loaded.room, "slug")
	if room == "" {
		return clientruntime.RoomSpec{}, usageError(fmt.Sprintf(
			"cannot load team definition from %s: the room needs a slug", teamDir))
	}
	spec := clientruntime.RoomSpec{
		Room:  room,
		Name:  optionalField(loaded.room, "name"),
		Cwd:   optionalField(loaded.room, "cwd"),
		Seats: make([]clientruntime.SeatSpec, 0, len(loaded.seats)),
		Links: make([]clientruntime.SeatLink, 0, len(loaded.links)),
	}
	for _, seat := range loaded.seats {
		key := stringField(seat, "seat_key")
		if key == "" {
			return clientruntime.RoomSpec{}, usageError(fmt.Sprintf(
				"cannot load team definition from %s: every seat needs a seat_key", teamDir))
		}
		spec.Seats = append(spec.Seats, clientruntime.SeatSpec{
			SeatKey:          key,
			SeatType:         stringField(seat, "seat_type"),
			Runtime:          stringField(seat, "runtime"),
			Model:            optionalField(seat, "model"),
			PermissionPolicy: definedField(seat, "permission_policy"),
			Cwd:              optionalField(seat, "cwd"),
			AgentDir:         optionalField(seat, "agent_dir"),
			Env:              stringMap(seat["env"]),
		})
	}
	for _, link := range loaded.links {
		spec.Links = append(spec.Links, clientruntime.SeatLink{From: link.from, Kind: link.kind, To: link.to})
	}
	for _, overlay := range loaded.seatOverlays {
		if spec.Overlays == nil {
			spec.Overlays = make(map[string][]string, len(loaded.seatOverlays))
		}
		spec.Overlays[overlay.seat] = overlay.slugs
	}
	return spec, nil
}

// definedField is the one field where a missing key and an empty value must NOT collapse: an omitted
// permission_policy means the documented unattended default, and a present-but-empty one is refused by name.
// optionalField folds "" into "absent", which is right for model and wrong here, so this field gets its own
// reading: nil when the key is absent, a pointer to whatever it holds ("" included) when it is present.
func definedField(row map[string]any, name string) *string {
	value, present := row[name]
	if !present {
		return nil
	}
	text, _ := value.(string)
	return &text
}

// optionalField is the difference between "the definition says nothing" and "the definition says the
// empty string". The runtime's own fields are Option<String>, and a team.json that writes "model": ""
// means the first: a seat with no model pinned, not a seat whose model is named nothing.
func optionalField(row map[string]any, name string) *string {
	value := stringField(row, name)
	if value == "" {
		return nil
	}
	return &value
}

// LoadLiveRoomSpec reads the room's OWN record - the spec.json the runtime wrote when it last started the
// room - into the same shape a definition yields.
//
// WHY IT EXISTS: once a room exists, its spec.json is the authority on its seats (the runtime wrote it from
// the definition, and it carries the per-seat cwds the definition did not). `up` with no --team starts the
// room from here, which is the invocation heal's safeguard repair runs: before it, that repair was refused
// by up's own usage gate every time, because the only way to name a team was a definition directory.
// A definition still names a team that has no record yet - it is what CREATES the record - so this loader
// refuses a missing file with the --team hint rather than starting an empty room.
func LoadLiveRoomSpec(root, room string) (clientruntime.RoomSpec, error) {
	path := clientruntime.RoomSpecPath(root, room)
	raw, err := os.ReadFile(path)
	if err != nil {
		return clientruntime.RoomSpec{}, usageError(fmt.Sprintf(
			"room %s has no record to start from: %s is unreadable (%v). A room's first start needs --team DIR, the definition it is created from",
			room, path, err))
	}
	var spec clientruntime.RoomSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return clientruntime.RoomSpec{}, usageError(fmt.Sprintf(
			"cannot read the room's own spec %s: %v", path, err))
	}
	if spec.Room != "" && spec.Room != room {
		return clientruntime.RoomSpec{}, usageError(fmt.Sprintf(
			"%s records the room %q, not %q - a room's own spec can only start that room", path, spec.Room, room))
	}
	if len(spec.Seats) == 0 {
		return clientruntime.RoomSpec{}, usageError(fmt.Sprintf(
			"%s lists no seats - a room with no record of its seats is created from --team DIR", path))
	}
	spec.Room = room
	return spec, nil
}

// stringMap reads a seat's env object. A definition is written by hand and the runtime takes a map of
// strings, so anything that is not one is left out rather than stringified into an env var.
func stringMap(value any) map[string]string {
	raw, ok := value.(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for key, entry := range raw {
		if text, ok := entry.(string); ok {
			out[key] = text
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
