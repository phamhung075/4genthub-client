// The per-runtime rows of `doctor`: one row for each runtime this client launches on this machine, saying where it
// was found and what its owner-wide config says about the seat guard.
//
// WHY THE ROW EXISTS. The guard has two halves, and until this row only one of them was visible: `hooks` reads the
// git hooks in the checkout, and the runtime entries - what covers a runtime whose only hook surface is its
// owner-wide file - were written by nothing and reported by nothing. A runtime a seat picks read its own file, and
// a missing entry there was invisible in every report.
//
// A ROW THAT CANNOT SAY ANYTHING SAYS SO. Every other row in this report carries its unreadable input in Row.Note
// (clonerow.go, hooksrow.go), and this one did not: a home that could not be resolved printed a bare `running` row
// with no line under it. Now the reason travels in the row's note AND in its lines, so a reader of either surface
// learns why the row decides nothing.
//
// A MISSING ENTRY IS A NOTE, NOT A FAILURE, for the same reason the hooks row treats an uninstalled guard as a
// note: it is `guard install` that writes it, and a machine where install has not run yet must not make `doctor`
// refuse. The row is also the MACHINE's - no runtime entry belongs to a room - so it carries no room.
package clientlifecycle

import (
	"path/filepath"

	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientguard"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// runtimeRow is the row plus the runtime state that produced it.
type runtimeRow struct {
	Row  clientservices.Health
	Hook clientguard.RuntimeHook
	// Home is the home the runtime's config was read under, so the line can name the file absolutely: this client
	// writes one path and reports another when it prints the home-relative one, which the gate called ambiguous.
	Home string
	// Reason is why this row decides nothing - an unreadable home, an unreadable file - and it is printed as a line
	// so the human report and `--json` (which carries the same lines as reasons) both show it.
	Reason string
}

// runtimeLines is one runtime row as the report prints it: where the runtime was found, then what its config says
// about the guard, then the reason this row read nothing. Every line carries the row's own safeguard name, so a
// reader can match a line to the row it explains.
func runtimeLines(row runtimeRow) []string {
	name := row.Row.Safeguard
	var lines []string
	if row.Hook.Found() {
		found := name + ": found"
		switch {
		case row.Hook.Bin != "":
			found += " at " + row.Hook.Bin
		case row.Hook.Dir != "":
			found += " by its config directory " + row.Hook.Dir
		}
		lines = append(lines, found)
		switch {
		case row.Hook.Config == "":
			lines = append(lines, name+": no entry is placed - there is no hook surface for this runtime")
		case row.Hook.CoveredBySeat():
			lines = append(lines, name+": no owner-wide entry is written - this runtime's guard is the seat's own "+
				"<seat dir>/"+row.Hook.SeatFile+", which the runtime writes and passes with --settings")
			if row.Hook.EntryPresent {
				lines = append(lines, name+": a stale owner-wide entry is in "+row.ConfigPath()+
					"; `4genteam guard uninstall` removes it")
			}
		case row.Hook.EntryPresent:
			lines = append(lines, name+": the guard's entry is in "+row.ConfigPath())
		default:
			lines = append(lines, name+": no seat-guard entry in "+row.ConfigPath()+
				"; `4genteam guard install` writes it")
		}
	}
	if row.Hook.ReadError != "" {
		lines = append(lines, name+": "+row.Hook.ReadError)
	}
	if len(lines) == 0 && row.Reason != "" {
		lines = append(lines, name+": "+row.Reason)
	}
	return lines
}

// ConfigPath is the runtime's config file as an absolute path, or "" when the runtime has no surface.
func (r runtimeRow) ConfigPath() string {
	if r.Hook.Config == "" {
		return ""
	}
	return filepath.Join(r.Home, r.Hook.Config)
}

// runtimeRowFor is the runtime rows as a seam, beside hooksRowFor: a case drives detected, undetected, placed and
// missing without a PATH or a home directory on the machine.
var runtimeRowFor = runtimeRowsFor

// runtimeHome is the home the runtime configs are read under, and runtimeHooks the read itself, both as seams for
// the same reason.
var (
	runtimeHome  = clientenv.RealHome
	runtimeHooks = clientguard.RuntimeHooks
)

// runtimeRowsFor is the rows the report covers. The argument is the room the caller named and is unused: the
// owner-wide runtime configs belong to the machine, not to a room.
func runtimeRowsFor(string) []runtimeRow {
	home, err := runtimeHome()
	if err != nil {
		// The rows cannot be read at all: ONE row carries the reason, and its line is what the human report and
		// `--json` both show - a bare `running` row with no line under it says nothing about why.
		return []runtimeRow{{
			Row: clientservices.Health{
				Safeguard: runtimeSafeguard(""),
				State:     clientservices.Running,
				AgeS:      clientservices.UnknownAge,
			},
			Reason: "the runtime configs were not observed: " + err.Error(),
		}}
	}
	hooks := runtimeHooks(home)
	out := make([]runtimeRow, 0, len(hooks))
	for _, hook := range hooks {
		out = append(out, runtimeRow{
			Row: clientservices.Health{
				Safeguard: runtimeSafeguard(hook.Runtime),
				State:     clientservices.Running,
				AgeS:      clientservices.UnknownAge,
			},
			Hook: hook,
			Home: home,
		})
	}
	return out
}

// runtimeSafeguard is the row's name: `guard_agy`, `guard_claude`, `guard_codex`. It is prefixed rather than the
// runtime's own name because the row is about the GUARD's presence in that runtime, and a report line reading
// `claude` alone would look like a statement about the binary.
func runtimeSafeguard(runtime string) string {
	if runtime == "" {
		return "guard_runtime"
	}
	return "guard_" + runtime
}
