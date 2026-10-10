package clientoffpeak

import (
	"fmt"
	"strings"
)

// Mode is the client's peak policy for work that STARTS OR RESUMES a DeepSeek seat. It is a setting
// rather than a constant because the same client runs on machines that pay for their own tokens and on
// machines that do not, and the operator has to be able to switch without a rebuild.
type Mode int

const (
	// OffpeakOnly is the default: DeepSeek seats start or resume off-peak only, and a peak start is
	// refused with the time off-peak resumes.
	OffpeakOnly Mode = iota
	// AllowPeak runs at any hour and skips the check entirely - no reading, no message, no refusal.
	AllowPeak
)

func (m Mode) String() string {
	if m == AllowPeak {
		return "allow-peak"
	}
	return "offpeak-only"
}

// ModeEnv is the ONE variable that carries the setting: in the process environment, or in the client's
// one env file, which clientenv loads into that environment at startup - so a value written there
// arrives here as this variable and nothing else has to read a file.
const ModeEnv = "OFFPEAK_ONLY"

// DefaultMode is what applies when the variable is unset. On, because the expensive default has to be
// the one nobody had to remember.
const DefaultMode = OffpeakOnly

// The values this client WRITES for the setting, and the ones it accepts.
const (
	ValueOn  = "1"
	ValueOff = "0"
)

// FlagAllowPeak is the ONE argument that turns the gate off for a single run, and the name the order
// fixed: a gated verb refuses at peak "unless --allow-peak is passed". It exists beside the variable so
// a one-off start does not need an edit to a file the operator shares with everything else.
const FlagAllowPeak = "--allow-peak"

// ModeFlagUsage is how every gated verb's usage names it.
const ModeFlagUsage = "[" + FlagAllowPeak + "]"

// ParseMode reads the setting's value: the words an operator writes in a file, and the ones the flags
// use. A value that is neither is an error, never a silent default - a typo must not be able to read as
// "run at the peak rate", which is the one mistake this setting exists to prevent.
func ParseMode(value string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "on", "true", "yes":
		return OffpeakOnly, nil
	case "0", "off", "false", "no":
		return AllowPeak, nil
	}
	return DefaultMode, fmt.Errorf("%q is not on or off: use %s/%s or %s/%s",
		value, ValueOn, "on", ValueOff, "off")
}

// ResolveMode decides which policy applies: the flag when it was given, else ModeEnv, else the default.
// It also answers WHERE the value came from, because a refusal has to say which knob turned the gate on
// and `offpeak status` prints the same words.
func ResolveMode(allowPeak bool, getenv func(string) string) (Mode, string, error) {
	if allowPeak {
		return AllowPeak, "the " + FlagAllowPeak + " flag", nil
	}
	value := strings.TrimSpace(getenv(ModeEnv))
	if value == "" {
		return DefaultMode, "the default (" + ModeEnv + " is not set)", nil
	}
	mode, err := ParseMode(value)
	if err != nil {
		return DefaultMode, "", fmt.Errorf("%s: %w", ModeEnv, err)
	}
	return mode, fmt.Sprintf("%s=%s in the environment or the client's env file", ModeEnv, value), nil
}

// VerdictFor is what a verb that is about to start or resume seats needs from this package: the mode,
// and - ONLY when the mode asks for a check - the schedule for now. It exists so the gated verbs cannot
// drift on the two rules that matter: the flag beats the environment, and a check that is OFF never
// reads the clock, so a malformed OFFPEAK_UTC cannot break a run that does not consult it.
func VerdictFor(allowPeak bool, getenv func(string) string) (Mode, Verdict, error) {
	mode, _, err := ResolveMode(allowPeak, getenv)
	if err != nil {
		return mode, Verdict{}, err
	}
	if mode == AllowPeak {
		return mode, Verdict{}, nil
	}
	verdict, err := NowFrom(getenv)
	if err != nil {
		return mode, Verdict{}, err
	}
	return mode, verdict, nil
}

// Check is the gate as a verb uses it: it resolves the mode and - only when the mode asks - the
// schedule, then decides over the seats in ONE node reading. `proceed` false means the verb must refuse
// and `message` is the body it prints after its own name; `err` is a caller error (a malformed
// OFFPEAK_UTC, or an OFFPEAK_ONLY value that is neither on nor off) that the verb reports and exits 2 on.
//
// It takes the reading rather than making it, so each verb keeps its own rig seam and none of them
// learns to read `rig ps` a second time.
func Check(allowPeak bool, nodes []map[string]any, getenv func(string) string) (proceed bool, message string, err error) {
	resolved, verdict, err := VerdictFor(allowPeak, getenv)
	if err != nil {
		return false, "", err
	}
	if resolved == AllowPeak {
		return true, "", nil
	}
	if decision := Decide(resolved, SeatsFromNodes(nodes), verdict); decision.Refuse {
		return false, decision.Message, nil
	}
	return true, "", nil
}

// ModeFromFlags takes that flag out of an argument list and returns the rest, so a gated verb accepts it
// wherever it sits - before or after its own positional, as the Python's argparse did. It cannot fail:
// there is one flag with one meaning, so naming it twice is the same request twice. A flag this package
// does NOT own is handed back with the rest, for the verb to refuse or to read for itself.
func ModeFromFlags(args []string) (allowPeak bool, rest []string) {
	rest = make([]string, 0, len(args))
	for _, arg := range args {
		if arg == FlagAllowPeak {
			allowPeak = true
			continue
		}
		rest = append(rest, arg)
	}
	return allowPeak, rest
}

// Seat is one seat a caller is about to start or resume.
type Seat struct {
	Name  string
	Model string
}

// DeepSeekSeats are the seats whose model is DeepSeek's. The test is the provider prefix `deepseek/`,
// case-insensitively, because that prefix - not the model behind it - is what says whose rate applies.
func DeepSeekSeats(seats []Seat) []Seat {
	var deepseek []Seat
	for _, seat := range seats {
		if strings.HasPrefix(strings.ToLower(seat.Model), "deepseek/") {
			deepseek = append(deepseek, seat)
		}
	}
	return deepseek
}

// SeatsFromNodes reads the seat name and the model out of ONE `rig ps --json --nodes --rig ROOM`
// reading, which is the reading every gated verb already takes for its own reasons: the models are in
// the node records OpenRig reports, so the gate costs no extra call and no cloud round trip.
//
// The name is the logical id ("room.seat") when the node carries one, else the canonical session name.
// A node with neither is skipped rather than reported as an empty seat.
func SeatsFromNodes(nodes []map[string]any) []Seat {
	seats := make([]Seat, 0, len(nodes))
	for _, node := range nodes {
		name, _ := node["logicalId"].(string)
		if name == "" {
			name, _ = node["canonicalSessionName"].(string)
		}
		if name == "" {
			continue
		}
		model, _ := node["model"].(string)
		seats = append(seats, Seat{Name: name, Model: model})
	}
	return seats
}

// Decision is the gate's answer.
type Decision struct {
	Refuse  bool
	Message string // the sentence the caller prints after its own verb name; empty when it does not refuse
	Seats   []Seat // the DeepSeek seats that triggered the refusal
}

// Decide refuses ONLY when the mode is off-peak-only, the room holds a DeepSeek seat AND the schedule
// says PEAK. Every other combination passes in silence: a room with no DeepSeek seat is not this gate's
// business, and an operator who turned the setting off asked for no check at all.
//
// It does not know or care whether the seats are running. The verbs that call it either restore a room
// or launch a seat, so "this room holds a DeepSeek seat" is already the whole question they ask.
func Decide(mode Mode, seats []Seat, verdict Verdict) Decision {
	if mode == AllowPeak {
		return Decision{}
	}
	deepseek := DeepSeekSeats(seats)
	if len(deepseek) == 0 || !verdict.Peak {
		return Decision{}
	}
	named := make([]string, 0, len(deepseek))
	for _, seat := range deepseek {
		named = append(named, seat.Name+": "+seat.Model)
	}
	next := verdict.Next
	if next == "" {
		next = "the end of the peak block"
	}
	return Decision{
		Refuse: true,
		Seats:  deepseek,
		Message: fmt.Sprintf(
			"%d seat(s) run a DeepSeek model (%s) and DeepSeek bills at PEAK right now - %s, next: %s. "+
				"Refusing to start or resume them at the peak rate: pass %s (or set %s=%s) to run now, "+
				"or wait and the gate opens by itself",
			len(deepseek), strings.Join(named, ", "), verdict.Reason, next,
			FlagAllowPeak, ModeEnv, ValueOff),
	}
}
