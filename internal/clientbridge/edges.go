package clientbridge

// edgeKinds are the room orchestrator's five edge kinds; the server's commpolicy.ValidKind is the other copy of
// this list.
var edgeKinds = map[string]bool{
	"delegates_to":      true,
	"spawned_by":        true,
	"can_observe":       true,
	"collaborates_with": true,
	"escalates_to":      true,
}

// EdgeStatus is one directed link between two seats of a room, as the topology view draws it.
type EdgeStatus struct {
	Room string `json:"room"`
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

// roomEdge is a (from, to, kind) read from one room's links, before filtering.
type roomEdge struct{ from, to, kind string }

// reportedRoom pairs a room id with the room name it reports under.
type reportedRoom struct{ id, room string }

// Rooms lists each distinct room of the nodes in first-seen order, so the edges report in a
// stable order. A later node of the same room overwrites the room name, as the Python dict did.
func Rooms(nodes []any) []reportedRoom {
	var rooms []reportedRoom
	index := map[string]int{}
	for _, node := range nodes {
		fields := dict(node)
		id, room := str(fields["roomId"]), str(fields["roomName"])
		if at, seen := index[id]; seen {
			rooms[at].room = room
			continue
		}
		index[id] = len(rooms)
		rooms = append(rooms, reportedRoom{id, room})
	}
	return rooms
}

// BuildEdges is the reported topology: one entry per distinct, valid, directed link of every room.
// The server refuses the WHOLE report for one bad edge (an unknown kind, a self edge, a repeated
// link), which reads as the machine going offline, so a bad edge is dropped here.
func BuildEdges(rooms []reportedRoom, readEdges func(roomID string) []roomEdge) []EdgeStatus {
	edges := []EdgeStatus{}
	seen := map[EdgeStatus]bool{}
	for _, room := range rooms {
		for _, edge := range readEdges(room.id) {
			entry := EdgeStatus{Room: room.room, From: edge.from, To: edge.to, Kind: edge.kind}
			if edgeKinds[edge.kind] && edge.from != edge.to &&
				namePattern.MatchString(room.room) &&
				namePattern.MatchString(edge.from) &&
				namePattern.MatchString(edge.to) &&
				!seen[entry] {
				seen[entry] = true
				edges = append(edges, entry)
			}
		}
	}
	return edges
}
