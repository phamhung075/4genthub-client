package clientteam

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// The API prefix every path in this package is relative to.
const apiPrefix = "/api/v2"

// apiPathSeatTypes is the one seat-types path, named once because both the seed decision and the plan
// refer to it.
const apiPathSeatTypes = apiPrefix + "/seat-types"

// Exit codes, the Python team_setup's. ExitRemote and ExitUsage are clientcmd's shared numbers;
// ExitDrift is named here because a drifted tree is its own exit condition, separate from a network
// error, even though it shares the number 1.
const (
	ExitOK     = clientcmd.ExitOK
	ExitRemote = clientcmd.ExitRemote
	ExitUsage  = clientcmd.ExitUsage
	// ExitDrift is drift-check's "there is at least one finding".
	ExitDrift = 1
)

// The versions a first publish uses, and the three outcomes import-project reports per module. SKIP is
// also the pseudo-method a resolved skip step carries into runSteps, so a skip never becomes a request.
const (
	importVersion  = "1.0.0"
	publishVersion = "1.0.0"

	actionSkip       = "SKIP"
	actionPush       = "PUSH"
	actionNewVersion = "NEW-VERSION"
)

// module is one module version to publish, with the content bytes the cloud stores.
type module struct {
	slug    string
	kind    string
	version string
	content string
}

// team is team.json plus each module's text read from its file.
type team struct {
	room           map[string]any
	modules        []module
	seats          []map[string]any
	links          []teamLink
	companyOverlay []string
	seatOverlays   []seatOverlay
}

type teamLink struct {
	from string
	kind string
	to   string
}

// seatOverlay is one seat's overlay, as an ordered pair: the plan prints and sends the seats in the
// order team.json lists them, and a map would lose that.
type seatOverlay struct {
	seat  string
	slugs []string
}

var semverRe = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// loadTeam reads <teamDir>/team.json and attaches each module's text from its file. A definition that
// cannot be read or is missing a field is a usage error, not a crash.
func loadTeam(teamDir string) (*team, error) {
	raw, err := os.ReadFile(filepath.Join(teamDir, "team.json"))
	if err != nil {
		return nil, usageError(fmt.Sprintf("cannot load team definition from %s: %v", teamDir, err))
	}
	var parsed struct {
		Room           map[string]any   `json:"room"`
		Modules        []map[string]any `json:"modules"`
		Seats          []map[string]any `json:"seats"`
		Links          []map[string]any `json:"links"`
		CompanyOverlay []string         `json:"company_overlay"`
		SeatOverlays   json.RawMessage  `json:"seat_overlays"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, usageError(fmt.Sprintf("cannot load team definition from %s: %v", teamDir, err))
	}
	overlays, err := decodeSeatOverlays(parsed.SeatOverlays)
	if err != nil {
		return nil, usageError(fmt.Sprintf("cannot load team definition from %s: %v", teamDir, err))
	}
	loaded := &team{
		room:           parsed.Room,
		links:          make([]teamLink, 0, len(parsed.Links)),
		companyOverlay: parsed.CompanyOverlay,
		seatOverlays:   overlays,
	}
	for _, entry := range parsed.Modules {
		slug, _ := entry["slug"].(string)
		kind, _ := entry["kind"].(string)
		version, _ := entry["version"].(string)
		file, _ := entry["file"].(string)
		if slug == "" || file == "" {
			return nil, usageError(fmt.Sprintf(
				"cannot load team definition from %s: every module needs a slug and a file", teamDir))
		}
		text, err := os.ReadFile(filepath.Join(teamDir, file))
		if err != nil {
			return nil, usageError(fmt.Sprintf("cannot load team definition from %s: %v", teamDir, err))
		}
		loaded.modules = append(loaded.modules, module{slug: slug, kind: kind, version: version, content: string(text)})
	}
	for _, seat := range parsed.Seats {
		loaded.seats = append(loaded.seats, seat)
	}
	for _, link := range parsed.Links {
		from, _ := link["from"].(string)
		kind, _ := link["kind"].(string)
		to, _ := link["to"].(string)
		loaded.links = append(loaded.links, teamLink{from: from, kind: kind, to: to})
	}
	if len(loaded.seats) > 1 && len(loaded.links) == 0 {
		return nil, usageError(fmt.Sprintf(
			"cannot load team definition from %s: a room of %d seats needs its links; with none, no seat can message another and the topology has no edges", teamDir, len(loaded.seats)))
	}
	return loaded, nil
}

// decodeSeatOverlays reads the seat_overlays object in file order, so the plan is the same list the
// Python built from its dict.
func decodeSeatOverlays(raw json.RawMessage) ([]seatOverlay, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("seat_overlays must be an object")
	}
	var overlays []seatOverlay
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		seat, _ := keyToken.(string)
		var slugs []string
		if err := decoder.Decode(&slugs); err != nil {
			return nil, err
		}
		overlays = append(overlays, seatOverlay{seat: seat, slugs: slugs})
	}
	return overlays, nil
}

// overlayBody is the PUT body for a seat's or the account's overlay: one "add" op per slug, whose
// version comes from the team's module table. An unknown slug is a usage error, as in the Python.
func overlayBody(loaded *team, slugs []string) (map[string]any, error) {
	versions := map[string]string{}
	for _, m := range loaded.modules {
		versions[m.slug] = m.version
	}
	ops := make([]map[string]any, 0, len(slugs))
	for _, slug := range slugs {
		version, known := versions[slug]
		if !known {
			return nil, usageError(fmt.Sprintf("overlay references unknown module %q", slug))
		}
		ops = append(ops, map[string]any{"kind": "add", "slug": slug, "version": version, "content": ""})
	}
	return map[string]any{"ops": ops}, nil
}

// nextPatch is the version with its patch incremented; it mirrors the backend's NextPatchVersion, and
// refuses a version that is not a concrete x.y.z.
func nextPatch(version string) (string, error) {
	if !semverRe.MatchString(version) {
		return "", usageError(fmt.Sprintf(
			"version %q must be a concrete semver x.y.z to advance to the next patch", version))
	}
	parts := strings.SplitN(version, ".", 3)
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", usageError(fmt.Sprintf(
			"version %q must be a concrete semver x.y.z to advance to the next patch", version))
	}
	return fmt.Sprintf("%s.%s.%d", parts[0], parts[1], patch+1), nil
}

// step is one plan entry: (label, method, path, body, tolerateExists) in the Python's order.
type step struct {
	label          string
	method         string
	path           string
	body           any
	tolerateExists bool
}

// buildPlan returns the ordered steps apply sends. storedSeatTypes is nil when the plan is built
// without credentials, and then the seed runs: seeding is what creates the seat types a team names.
func buildPlan(loaded *team, storedSeatTypes map[string]bool) ([]step, error) {
	room, _ := loaded.room["slug"].(string)
	seatsPath := fmt.Sprintf("%s/rooms/%s/seats", apiPrefix, room)
	needed := map[string]bool{}
	for _, seat := range loaded.seats {
		seatType, _ := seat["seat_type"].(string)
		needed[seatType] = true
	}
	plan := []step{}
	if storedSeatTypes == nil || !containsAll(storedSeatTypes, needed) {
		plan = append(plan, step{
			label: "seat types (seed)", method: "POST",
			path: apiPathSeatTypes + "/seed", body: map[string]any{},
		})
	}
	for _, m := range loaded.modules {
		plan = append(plan, moduleStep(m.slug, m.kind, m.version, m.content))
	}
	plan = append(plan, step{
		label: fmt.Sprintf("room %s", room), method: "POST", path: apiPrefix + "/rooms",
		body: loaded.room, tolerateExists: true,
	})
	for _, seat := range loaded.seats {
		key, _ := seat["seat_key"].(string)
		plan = append(plan, step{
			label: fmt.Sprintf("seat %s", key), method: "POST", path: seatsPath,
			body: seat, tolerateExists: true,
		})
	}
	for _, link := range loaded.links {
		plan = append(plan, step{
			label: fmt.Sprintf("link %s %s %s", link.from, link.kind, link.to), method: "PUT",
			path: fmt.Sprintf("%s/%s/links", seatsPath, link.from),
			body: map[string]any{"to_seat": link.to, "kind": link.kind, "allow": true},
		})
	}
	if len(loaded.companyOverlay) > 0 {
		body, err := overlayBody(loaded, loaded.companyOverlay)
		if err != nil {
			return nil, err
		}
		plan = append(plan, step{label: "overlay company", method: "PUT", path: apiPrefix + "/overlay", body: body})
	}
	for _, overlay := range loaded.seatOverlays {
		body, err := overlayBody(loaded, overlay.slugs)
		if err != nil {
			return nil, err
		}
		plan = append(plan, step{
			label: fmt.Sprintf("overlay seat %s", overlay.seat), method: "PUT",
			path: fmt.Sprintf("%s/%s/overlay", seatsPath, overlay.seat), body: body,
		})
	}
	return plan, nil
}

// moduleStep is one module-version PUT: the single shape both apply and import-project push.
func moduleStep(slug, kind, version, content string) step {
	return step{
		label: fmt.Sprintf("module %s@%s", slug, version), method: "PUT",
		path: fmt.Sprintf("%s/modules/%s/versions/%s", apiPrefix, slug, version),
		body: map[string]any{"kind": kind, "content": content},
	}
}

func containsAll(have, need map[string]bool) bool {
	for key := range need {
		if !have[key] {
			return false
		}
	}
	return true
}

// resolved is one module classified against what the cloud stores.
type resolvedEntry struct {
	module    module
	action    string
	version   string
	requested string
}

// resolvedSteps turns the classification into steps: a PUT for each push, a SKIP marker for each skip.
func resolvedSteps(entries []resolvedEntry) []step {
	steps := make([]step, 0, len(entries))
	for _, entry := range entries {
		if entry.action == actionSkip {
			steps = append(steps, step{
				label:  fmt.Sprintf("module %s@%s", entry.module.slug, entry.version),
				method: actionSkip,
			})
			continue
		}
		single := moduleStep(entry.module.slug, entry.module.kind, entry.version, entry.module.content)
		if entry.action == actionNewVersion {
			single.label = fmt.Sprintf("%s (new version; %s stored with different content)",
				single.label, entry.requested)
		}
		steps = append(steps, single)
	}
	return steps
}

// planLine is the per-module dry-run line once the server has classified the module.
func planLine(entry resolvedEntry) string {
	label := fmt.Sprintf("plan: module %s@%s", entry.module.slug, entry.version)
	switch entry.action {
	case actionSkip:
		return label + ": SKIP (identical content already stored)"
	case actionNewVersion:
		return fmt.Sprintf("%s: NEW-VERSION (%s stored with different content)", label, entry.requested)
	default:
		return label + ": PUSH"
	}
}

// unresolvedPlanLine is the per-module dry-run line when no server is configured to classify against.
func unresolvedPlanLine(m module, version string) string {
	label := fmt.Sprintf("plan: module %s@%s", m.slug, version)
	if !semverRe.MatchString(version) {
		return label + ": PUSH"
	}
	next, err := nextPatch(version)
	if err != nil {
		return label + ": PUSH"
	}
	return fmt.Sprintf("%s: PUSH unless stored content is identical (then SKIP); if stored content differs, NEW-VERSION %s",
		label, next)
}

// runSteps prints a dry-run plan, or sends every non-skip step in order and reports each outcome.
//
// With collectFailures, a failing step does not abort the run: every remaining step is attempted and
// the failures come back as (label, reason) pairs. That is for callers whose steps are independent,
// where stopping at the first error would leave the rest unattempted and unnamed - a 52-block publish
// that dies at block 30 looks like a 30-block inventory.
func runSteps(cloud *cloudClient, steps []step, dryRun, collectFailures bool, stdout, stderr io.Writer) ([]failedStep, error) {
	if dryRun {
		for _, single := range steps {
			fmt.Fprintf(stdout, "plan: %s (%s %s)\n", single.label, single.method, single.path)
		}
		return nil, nil
	}
	var failures []failedStep
	for _, single := range steps {
		if single.method == actionSkip {
			fmt.Fprintf(stdout, "%s: skipped (identical content already stored)\n", single.label)
			continue
		}
		status, text, err := cloud.send(single.method, single.path, single.body)
		var result string
		if err == nil {
			result, err = outcome(single.method, status, text, single.tolerateExists)
		}
		if err != nil {
			wrapped := fmt.Errorf("%s: %s %s: %w", single.label, single.method, single.path, err)
			if !collectFailures {
				return nil, wrapped
			}
			failures = append(failures, failedStep{label: single.label, reason: fmt.Sprintf("%s %s: %v", single.method, single.path, err)})
			fmt.Fprintf(stdout, "%s: FAILED %v\n", single.label, err)
			continue
		}
		fmt.Fprintf(stdout, "%s: %s\n", single.label, result)
	}
	return failures, nil
}

type failedStep struct {
	label  string
	reason string
}

// outcome maps an HTTP answer to the word the Python prints, tolerating an "already exists" 409 where
// the step is idempotent.
func outcome(method string, status int, text string, tolerateExists bool) (string, error) {
	if status == 200 || status == 201 {
		if method == "POST" {
			return "created", nil
		}
		return "applied", nil
	}
	if status == 409 && tolerateExists && strings.Contains(strings.ToLower(text), "already exists") {
		return "exists", nil
	}
	if len(text) > 300 {
		text = text[:300]
	}
	return "", &clientcmd.CodedError{Message: fmt.Sprintf("HTTP %d %s", status, text), Code: ExitRemote}
}

// usageError is a caller error: it exits 2 with one message naming what is wrong.
func usageError(message string) error {
	return &clientcmd.CodedError{Message: message, Code: ExitUsage}
}
