// The seat's own policy document: the hub's render of it, the client's non-policy keys, and the one place a
// start or a restart asks for it.
//
// IT LIVES HERE AND NOT IN clientpolicy BECAUSE THE HUB'S RENDER IS READ HERE. The `policy` verb prints and
// checks this document (clientpolicy), the launch path hands it to the runtime (clientlifecycle,
// clientroom, clientseat), and every one of them needs the SAME bytes - so there is one definition of them,
// next to the fetch, and clientpolicy imports it rather than composing a second one.
//
// THIS IS THE ONLY FILE HALF THE CLIENT STILL READS. The render used to carry `agent.yaml`, `startup.files`,
// a skills list and `guidance/role.md` (retired: a seat's skills come from the client tree's share, room and
// seat tiers, and its role is the seat tier's `<seat>-role` skill). What writes a seat's other files is the
// RUNTIME, not the client and not the render: `install_seat_skills` copies the resolved skill tree into
// `<seat dir>/skills/` (rust/agenthub-runtime/src/supervisor/mod.rs:400) and `start_seat` writes the seat's
// tool surface, `<seat dir>/mcp.json`, at every start (same file, :704). So a reader looking for who
// produced a file in a seat's launch directory starts there, and this package is only the policy document.
package clientsync

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// SeatRenderPath is the hub's name for a seat's omp config inside a resolved seat's file list
// (`seatrenderer.ompConfigPath`). The hub renders it, and the client installs it as the seat's `config.yml`;
// the hub's own comment on the constant states that destination, which is what this reader serves.
const SeatRenderPath = "runtime/omp-config.yml"

// keepRecentTokens is the tokens of the newest conversation kept verbatim at a compaction. omp's default is
// 20000, which holds the post-compaction floor near 42k; 8000 brings it near 30k. It is a CLIENT key rather
// than a policy decision - the render says what a seat may DO, not how much of its conversation it keeps -
// and it is the one key this client adds to the document, in InstallDocument.
const keepRecentTokens = 8000

// SeatRender is the hub's rendered `runtime/omp-config.yml` for one seat: the document the runtime installs
// as `<state root>/rooms/<room>/seats/<seat>/config.yml` before the seat's runner starts.
//
// IT IS FETCHED, NEVER RE-DERIVED. The hub folds the seat's policy modules into this document, and the
// seat's own limits text is rendered from the SAME fold, so a client that composed a second document from a
// table of its own could tell the seat one thing and enforce another - which is exactly the divergence this
// reader replaced. The bool is false when the hub knows the seat and its render carries no such entry: a seat
// with no policy module has NO document, and vending an empty one would install an empty policy.
func SeatRender(ctx context.Context, room, seat string) (string, bool, error) {
	baseURL, err := RequireEnv("AGENTHUB_URL")
	if err != nil {
		return "", false, err
	}
	token, err := RequireEnv("AGENTHUB_TOKEN")
	if err != nil {
		return "", false, err
	}
	resolved, err := FetchSeat(ctx, baseURL, token, room, seat)
	if err != nil {
		return "", false, err
	}
	entries, err := ExtractFiles(resolved)
	if err != nil {
		return "", false, err
	}
	for _, entry := range entries {
		if strings.Join(entry.Parts, "/") == SeatRenderPath {
			return entry.Content, true, nil
		}
	}
	return "", false, nil
}

// InstallDocument is the text installed as a seat's config.yml: the hub's render, with this client's own
// non-policy key added.
//
// WHY NOT VERBATIM: the render supersedes this client's old document only where it SPEAKS. The compaction
// setting is not a policy - the hub cannot render it, and dropping it would put every seat back on omp's own
// 20000-token default, a behaviour change beyond the deny the install is for. The key is APPENDED rather than
// merged, so the render's own bytes survive untouched and a reader can diff the file against the render and
// see the one added block; it is skipped when the render already carries a top-level `compaction:`, because a
// second one of those is a duplicate key rather than a default.
func InstallDocument(render string) string {
	if render == "" || hasTopLevelKey(render, "compaction") {
		return render
	}
	if !strings.HasSuffix(render, "\n") {
		render += "\n"
	}
	return render + "compaction:\n  keepRecentTokens: " + strconv.Itoa(keepRecentTokens) + "\n"
}

// hasTopLevelKey reports whether a YAML document already states a key at the top level. Column zero is what
// makes it top level: an indented `compaction:` belongs to whatever block holds it.
func hasTopLevelKey(document, key string) bool {
	for _, line := range strings.Split(document, "\n") {
		if strings.HasPrefix(line, key+":") {
			return true
		}
	}
	return false
}

// SeatDocumentFor is the document to hand the runtime for one seat that is about to be started, or nil when
// there is none: the hub's render for that seat, with this client's own non-policy key, ready for the seat
// spec (or for a restart frame).
//
// A RENDER THAT CANNOT BE READ DOES NOT STOP A ROOM. The room's definition is local - a room starts with no
// cloud round trip - and a scratch room's seats are not on the hub at all, so a fetch that fails is SAID on
// `said` and the start goes ahead from the room's own record. The alternative, refusing the start, would make
// `up` and every reseat depend on the hub being reachable, which is a promise this client has never made; and
// the case is not silent, because the line names the room, the seat and the hub's own reason. A seat whose
// render carries no document is a different thing and says nothing: no policy module means no file, the same
// absence rule the seat dir already follows for mcp.json.
func SeatDocumentFor(ctx context.Context, room, seat string, said io.Writer) *string {
	render, found, err := SeatRender(ctx, room, seat)
	if err != nil {
		fmt.Fprintf(said, "4genteam: %s/%s: the hub's render could not be read, so this start installs the "+
			"room's own record: %v\n", room, seat, err)
		return nil
	}
	if !found {
		return nil
	}
	document := InstallDocument(render)
	return &document
}

// ServesRuntime reports whether a seat's runtime takes this document, which is the same gate the runtime
// applies before it writes the file: only an omp seat has a `config.yml` in its own agent directory. It
// exists so a caller can skip a hub round trip for a seat that could not use the answer - and a seat that
// names NO runtime is not served, because the runtime would treat it as a custom command, not as omp.
func ServesRuntime(runtime string) bool {
	return strings.EqualFold(strings.TrimSpace(runtime), "omp")
}

// SeatConfigPath is a seat's own config, under the machine-level state root: the file the runtime installs,
// and the one the `policy` verb's drift check reads. The path is the seat's PI_CODING_AGENT_DIR, which is
// where omp looks for it.
func SeatConfigPath(stateRoot, room, seat string) string {
	return filepath.Join(clientruntime.SeatDir(stateRoot, room, seat), "config.yml")
}
