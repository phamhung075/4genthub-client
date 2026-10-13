package clientruntime

import (
	"fmt"
	"strings"
)

// FlagSpec is one option a shim verb accepts.
//
// WHY THIS EXISTS. Go's flag package stops parsing at the first positional argument, so an option
// written AFTER the message — which is exactly how the OpenRig CLI documents its own verbs, e.g.
// `rig send <session> 'message' --verify` (the daemon's own `whoami` prints that form as
// `commands.sendExamples`) — became message text: the verb read rig=<session>, seat="text",
// message="--verify" and delivered nothing to the named seat. The CLI's parser accepts an option
// anywhere, so a shim that claims to be a drop-in for it must too.
type FlagSpec struct {
	Name  string // without the leading dashes
	Value bool   // true when the option takes a value (--reason "why")
}

// SplitFlags pulls the named options out of args WHEREVER they appear — before, between or after the
// positionals — and returns them with the positionals in their original order.
//
// An option this verb does not declare is an ERROR, never a positional: a flag silently consumed as
// message text is the defect this helper exists to prevent, and a caller is better told than obeyed.
// `--` ends option parsing (everything after it is positional), and a lone `-` is a positional.
func SplitFlags(args []string, specs []FlagSpec) (map[string]string, []string, error) {
	known := make(map[string]FlagSpec, len(specs))
	for _, spec := range specs {
		known[spec.Name] = spec
	}

	opts := make(map[string]string, len(specs))
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(arg) < 2 || !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}

		name := strings.TrimLeft(arg, "-")
		value := ""
		hasValue := false
		if eq := strings.Index(name, "="); eq >= 0 {
			value, hasValue = name[eq+1:], true
			name = name[:eq]
		}

		spec, ok := known[name]
		if !ok {
			return nil, nil, fmt.Errorf("unknown flag --%s for this verb", name)
		}
		if !spec.Value {
			if hasValue {
				return nil, nil, fmt.Errorf("--%s does not take a value", name)
			}
			opts[name] = "true"
			continue
		}
		if !hasValue {
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("--%s needs a value", name)
			}
			i++
			value = args[i]
		}
		opts[name] = value
	}
	return opts, positional, nil
}
