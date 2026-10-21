// The per-runtime rows of `doctor`: one row for each runtime CLI this machine has, saying where it was found and
// whether the owner-wide config carries the seat guard's entry.
//
// WHY THE ROW EXISTS. The guard has two halves, and until this row only one of them was visible: `hooks` reads the
// git hooks in the checkout, and the runtime entries - what covers a tool call in a claude or agy window before
// any seat exists - were written by nothing and reported by nothing. A seat picked its runtime, the runtime read
// its owner-wide file, and a missing entry there was invisible in every report.
//
// A MISSING ENTRY IS A NOTE, NOT A FAILURE, for the same reason the hooks row treats an uninstalled guard as a
// note: it is item 2's install that writes it, and a machine where install has not run yet must not make
// `doctor` refuse. The row is also the MACHINE's - no runtime entry belongs to a room - so it carries no room.
//
// AN INPUT THAT COULD NOT BE READ IS A LINE, never a failure.
package clientlifecycle

import (
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientguard"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// runtimeRow is the row plus the runtime state that produced it.
type runtimeRow struct {
	Row  clientservices.Health
	Hook clientguard.RuntimeHook
}

// runtimeLines is one runtime row as the report prints it: where the runtime was found, then what its config says
// about the guard, then a file this client could not read. Every line carries the row's own safeguard name, so a
// reader can match a line to the row it explains.
func runtimeLines(row runtimeRow) []string {
	if !row.Hook.Found() {
		return nil
	}
	name := row.Row.Safeguard
	found := name + ": found"
	switch {
	case row.Hook.Bin != "":
		found += " at " + row.Hook.Bin
	case row.Hook.Dir != "":
		found += " by its config directory " + row.Hook.Dir
	}
	lines := []string{found}
	switch {
	case row.Hook.Config == "":
		lines = append(lines, name+": no seat-guard entry is placed - the guard has no "+row.Hook.Runtime+" mode")
	case row.Hook.EntryPresent:
		lines = append(lines, name+": the guard's entry is in "+row.Hook.Config)
	default:
		lines = append(lines, name+": no seat-guard entry in "+row.Hook.Config+
			"; `4genteam guard install` writes it, and neither the owner's own window nor a seat's tool calls are "+
			"read by the guard until it does")
	}
	if row.Hook.ReadError != "" {
		lines = append(lines, name+": "+row.Hook.ReadError)
	}
	return lines
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
		return []runtimeRow{{Row: clientservices.Health{
			Safeguard: runtimeSafeguard(""),
			State:     clientservices.Running,
			AgeS:      clientservices.UnknownAge,
		}, Hook: clientguard.RuntimeHook{ReadError: "not observed: " + err.Error()}}}
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
		})
	}
	return out
}

// runtimeSafeguard is the row's name: `guard_claude`, `guard_agy`, `guard_gemini`. It is prefixed rather than the
// runtime's own name because the row is about the GUARD's presence in that runtime, and a report line reading
// `claude` alone would look like a statement about the binary.
func runtimeSafeguard(runtime string) string {
	if runtime == "" {
		return "guard_runtime"
	}
	return "guard_" + runtime
}
