package clientteam

import (
	"fmt"
	"io"
	"net/http"
)

// runPrune is `team prune`: delete the seats a room holds that team.json does not define. apply only
// creates, so a seat removed from team.json would otherwise stay in the room and keep appearing in
// the room's definition.
func runPrune(args []string, stdout, stderr io.Writer) error {
	const usage = `usage: 4genteam team prune --team DIR [--dry-run]

Delete the seats the cloud room holds that team.json does not define.

options:
  --team DIR   team definition directory (holds team.json); required
  --dry-run    list the seats that would be deleted, delete nothing
`
	flags := newVerbFlags("prune")
	dryRun := flags.Bool("dry-run", false, "list the seats that would be deleted, delete nothing")
	teamDir := flags.String("team", "", "team definition directory (holds team.json)")
	if err := parseVerb(flags, args, func(w io.Writer) { fmt.Fprint(w, usage) }, stdout); err != nil {
		return err
	}
	if *teamDir == "" {
		return usageError("team prune: --team DIR is required (the directory that holds team.json)")
	}
	loaded, err := loadTeam(*teamDir)
	if err != nil {
		return err
	}
	cloud, err := credentials()
	if err != nil {
		return err
	}
	room, _ := loaded.room["slug"].(string)
	seatsPath := fmt.Sprintf("%s/rooms/%s/seats", apiPrefix, room)
	stale, err := cloud.staleSeats(seatsPath, loaded)
	if err != nil {
		return err
	}
	for _, key := range stale {
		if *dryRun {
			fmt.Fprintf(stdout, "plan: delete seat %s (DELETE %s/%s)\n", key, seatsPath, key)
			continue
		}
		status, text, err := cloud.send(http.MethodDelete, seatsPath+"/"+key, nil)
		if err != nil {
			return err
		}
		if status >= 400 {
			return remoteError(fmt.Sprintf("seat %s: DELETE %s/%s: HTTP %d %s", key, seatsPath, key, status, text))
		}
		fmt.Fprintf(stdout, "seat %s: deleted\n", key)
	}
	return nil
}

// staleSeats lists the room's seat keys that team.json does not define, in the cloud's order. An
// unreadable listing is an error: pruning from a guess would delete the wrong seats.
func (c *cloudClient) staleSeats(seatsPath string, loaded *team) ([]string, error) {
	status, payload, err := c.getJSON(seatsPath)
	if err != nil {
		return nil, err
	}
	object, _ := payload.(map[string]any)
	rows, listed := object["seats"].([]any)
	if status >= 400 || !listed {
		return nil, remoteError(fmt.Sprintf("GET %s: no seat listing (HTTP %d)", seatsPath, status))
	}
	defined := map[string]bool{}
	for _, seat := range loaded.seats {
		key, _ := seat["seat_key"].(string)
		defined[key] = true
	}
	stale := []string{}
	for _, row := range rows {
		seat, _ := row.(map[string]any)
		if key := stringField(seat, "seat_key"); key != "" && !defined[key] {
			stale = append(stale, key)
		}
	}
	return stale, nil
}
