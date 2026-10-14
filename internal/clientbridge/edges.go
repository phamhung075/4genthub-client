package clientbridge

// edgeKinds are OpenRig's five edge kinds; the server's commpolicy.ValidKind is the other copy of
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

// rigEdge is a (from, to, kind) read from one room's links, before filtering.
type rigEdge struct{ from, to, kind string }

// rigRoom pairs a rig id with the room name it reports under.
type rigRoom struct{ id, room string }

// RigRooms lists each distinct rig of the nodes in first-seen order, so the edges report in a
// stable order. A later node of the same rig overwrites the room name, as the Python dict did.
func RigRooms(nodes []any) []rigRoom {
	var rigs []rigRoom
	index := map[string]int{}
	for _, node := range nodes {
		fields := dict(node)
		id, room := str(fields["rigId"]), str(fields["rigName"])
		if at, seen := index[id]; seen {
			rigs[at].room = room
			continue
		}
		index[id] = len(rigs)
		rigs = append(rigs, rigRoom{id, room})
	}
	return rigs
}

// BuildEdges is the reported topology: one entry per distinct, valid, directed link of every rig.
// The server refuses the WHOLE report for one bad edge (an unknown kind, a self edge, a repeated
// link), which reads as the machine going offline, so a bad edge is dropped here.
func BuildEdges(rigs []rigRoom, readEdges func(rigID string) []rigEdge) []EdgeStatus {
	edges := []EdgeStatus{}
	seen := map[EdgeStatus]bool{}
	for _, rig := range rigs {
		for _, edge := range readEdges(rig.id) {
			entry := EdgeStatus{Room: rig.room, From: edge.from, To: edge.to, Kind: edge.kind}
			if edgeKinds[edge.kind] && edge.from != edge.to &&
				namePattern.MatchString(rig.room) &&
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
