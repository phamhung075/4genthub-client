package clientoffpeak

import (
	"strings"
	"testing"
	"time"
)

func envOf(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

// TestParseModeRefusesAValueThatIsNotASwitch: the ONE mistake this setting exists to prevent is a typo
// reading as "run at the peak rate", so a value that is neither is an error and never a default.
func TestParseModeRefusesAValueThatIsNotASwitch(t *testing.T) {
	for _, value := range []string{"1", "on", "ON", "true", "yes", " on "} {
		mode, err := ParseMode(value)
		if err != nil || mode != OffpeakOnly {
			t.Errorf("ParseMode(%q) = %v, %v; want offpeak-only", value, mode, err)
		}
	}
	for _, value := range []string{"0", "off", "OFF", "false", "no"} {
		mode, err := ParseMode(value)
		if err != nil || mode != AllowPeak {
			t.Errorf("ParseMode(%q) = %v, %v; want allow-peak", value, mode, err)
		}
	}
	for _, value := range []string{"ture", "maybe", "2", "-"} {
		if _, err := ParseMode(value); err == nil {
			t.Errorf("ParseMode(%q) was accepted; a typo here decides the money question", value)
		}
	}
}

// TestTheFlagBeatsTheEnvironmentWhichBeatsTheDefault is the precedence the order asked for, asserted on
// all three states: the flag, the variable, and the default that applies when neither is given.
func TestTheFlagBeatsTheEnvironmentWhichBeatsTheDefault(t *testing.T) {
	cases := []struct {
		name       string
		flag       bool
		env        string
		want       Mode
		wantSource string
	}{
		{"nothing set", false, "", OffpeakOnly, "the default (" + ModeEnv + " is not set)"},
		{"the environment turns it off", false, "0", AllowPeak, ModeEnv + "=0 in the environment or the client's env file"},
		{"the environment says on in words", false, "on", OffpeakOnly, ModeEnv + "=on in the environment or the client's env file"},
		{FlagAllowPeak + " beats an env that says on", true, "1", AllowPeak, "the " + FlagAllowPeak + " flag"},
		{FlagAllowPeak + " is the same answer an env that says off gives", true, "0", AllowPeak, "the " + FlagAllowPeak + " flag"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, source, err := ResolveMode(c.flag, envOf(map[string]string{ModeEnv: c.env}))
			if err != nil {
				t.Fatal(err)
			}
			if mode != c.want {
				t.Errorf("mode = %v, want %v", mode, c.want)
			}
			if source != c.wantSource {
				t.Errorf("source = %q, want %q", source, c.wantSource)
			}
		})
	}
}

func TestAnUnreadableSettingIsAnErrorNamingTheVariable(t *testing.T) {
	_, _, err := ResolveMode(false, envOf(map[string]string{ModeEnv: "nah"}))
	if err == nil {
		t.Fatal("OFFPEAK_ONLY=nah was accepted")
	}
	for _, want := range []string{ModeEnv, `"nah"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}
}

// TestModeFromFlagsTakesTheFlagAnywhereAndLeavesTheRest: the flag may sit before or after the verb's own
// positional (the Python's argparse allowed either), and the arguments this package does not own are
// handed back untouched. Naming it twice is the same request twice, not an error.
func TestModeFromFlagsTakesTheFlagAnywhereAndLeavesTheRest(t *testing.T) {
	allowPeak, rest := ModeFromFlags([]string{"my-room", FlagAllowPeak})
	if !allowPeak {
		t.Error("the flag after the positional was not seen")
	}
	if len(rest) != 1 || rest[0] != "my-room" {
		t.Errorf("rest = %v, want the positional alone", rest)
	}

	allowPeak, rest = ModeFromFlags([]string{FlagAllowPeak, "my-room", "--after-seconds", "5"})
	if !allowPeak {
		t.Error("the flag before the positional was not seen")
	}
	if strings.Join(rest, " ") != "my-room --after-seconds 5" {
		t.Errorf("rest = %v, want the flags this package does not own left alone", rest)
	}

	if allowPeak, rest = ModeFromFlags([]string{FlagAllowPeak, FlagAllowPeak}); !allowPeak || len(rest) != 0 {
		t.Errorf("allowPeak = %v, rest = %v; twice is the same request twice", allowPeak, rest)
	}

	if allowPeak, rest = ModeFromFlags([]string{"my-room"}); allowPeak || len(rest) != 1 {
		t.Errorf("allowPeak = %v, rest = %v; with no flag the setting decides", allowPeak, rest)
	}
}

// TestTheOldFlagNameIsNotAFlag: `--any-time` was this package's own invention and the order fixed the
// name `--allow-peak`, so the old spelling must not be swallowed - it is handed to the verb, which
// refuses it as an unknown argument rather than reading it as a rig name.
func TestTheOldFlagNameIsNotAFlag(t *testing.T) {
	allowPeak, rest := ModeFromFlags([]string{"--any-time", "my-room"})
	if allowPeak {
		t.Error("--any-time is still recognised as the mode flag")
	}
	if strings.Join(rest, " ") != "--any-time my-room" {
		t.Errorf("rest = %v, want the old flag handed back to the verb", rest)
	}
	if allowPeak, rest = ModeFromFlags([]string{FlagAllowPeak}); !allowPeak || len(rest) != 0 {
		t.Errorf("allowPeak = %v, rest = %v for %s", allowPeak, rest, FlagAllowPeak)
	}
}

// TestSeatsFromNodesReadsTheNameAndTheModel: one node reading feeds the gate, and a node that names
// neither a logical id nor a session is skipped rather than judged as an empty seat.
func TestSeatsFromNodesReadsTheNameAndTheModel(t *testing.T) {
	nodes := []map[string]any{
		{"logicalId": "room.a", "model": "deepseek/deepseek-flash"},
		{"canonicalSessionName": "room-b-lead@room-b", "model": "claude-code/sonnet"},
		{"logicalId": "room.c", "model": "DEEPSEEK/deepseek-chat"},
		{"model": "deepseek/nameless"},
		{"logicalId": "room.d"},
	}
	seats := SeatsFromNodes(nodes)
	if len(seats) != 4 {
		t.Fatalf("seats = %v, want four (the nameless node skipped)", seats)
	}
	deepseek := DeepSeekSeats(seats)
	if len(deepseek) != 2 || deepseek[0].Name != "room.a" || deepseek[1].Name != "room.c" {
		t.Errorf("deepseek seats = %v, want room.a and room.c (the provider prefix is case-insensitive)", deepseek)
	}
}

func peakFriday() Verdict {
	return At(time.Date(2026, 10, 9, 2, 0, 0, 0, time.UTC), nil)
}

func offPeakFriday() Verdict {
	return At(time.Date(2026, 10, 9, 5, 0, 0, 0, time.UTC), nil)
}

// TestCheckRefusesOnlyWhenTheSettingIsOnTheRoomIsDeepSeekAndItIsPeak is the gate's whole truth table. A
// setting that is off means no check at all, a room with no DeepSeek seat is nobody's business here, and
// off-peak passes silently - only the intersection refuses.
func TestCheckRefusesOnlyWhenTheSettingIsOnTheRoomIsDeepSeekAndItIsPeak(t *testing.T) {
	deepseekNode := map[string]any{"logicalId": "room.alpha", "model": "deepseek/deepseek-flash"}
	localNode := map[string]any{"logicalId": "room.beta", "model": "claude-code/sonnet"}

	cases := []struct {
		name      string
		allowPeak bool
		env       map[string]string
		nodes     []map[string]any
		refused   bool
	}{
		{"peak, deepseek, the default (on)", false, nil, []map[string]any{deepseekNode}, true},
		{"peak, deepseek, the setting says on", false, map[string]string{ModeEnv: "1"}, []map[string]any{deepseekNode}, true},
		{"peak, deepseek, " + FlagAllowPeak, true, nil, []map[string]any{deepseekNode}, false},
		{"peak, deepseek, the setting says off", false, map[string]string{ModeEnv: "0"}, []map[string]any{deepseekNode}, false},
		{"off-peak, deepseek, the default (on)", false, map[string]string{PinEnv: "2026-10-09 05:00"}, []map[string]any{deepseekNode}, false},
		{"peak, no deepseek seat", false, nil, []map[string]any{localNode}, false},
		{"peak, no seats at all", false, nil, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := c.env
			if env == nil {
				env = map[string]string{PinEnv: "2026-10-09 02:00"}
			}
			if _, ok := env[PinEnv]; !ok {
				env[PinEnv] = "2026-10-09 02:00"
			}
			proceed, message, err := Check(c.allowPeak, c.nodes, envOf(env))
			if err != nil {
				t.Fatal(err)
			}
			if refused := !proceed; refused != c.refused {
				t.Fatalf("refused = %v, want %v (message %q)", refused, c.refused, message)
			}
			if c.refused {
				for _, want := range []string{"deepseek/deepseek-flash", "room.alpha", "PEAK", "04:00 UTC", FlagAllowPeak, ModeEnv + "=" + ValueOff} {
					if !strings.Contains(message, want) {
						t.Errorf("refusal %q does not name %q", message, want)
					}
				}
			} else if message != "" {
				t.Errorf("a passing gate still had something to say: %q", message)
			}
		})
	}
}

// TestACheckThatIsOffDoesNotReadTheClock: --allow-peak means no check, so a pinned instant the check
// would never use cannot fail the run - the same reason the verbs resolve the mode before consulting the
// panel.
func TestACheckThatIsOffDoesNotReadTheClock(t *testing.T) {
	proceed, _, err := Check(true, []map[string]any{{"logicalId": "room.a", "model": "deepseek/x"}},
		envOf(map[string]string{PinEnv: "not an instant"}))
	if err != nil || !proceed {
		t.Errorf("proceed = %v, err = %v; a check that is off must not read the clock", proceed, err)
	}
}

func TestAPinnedClockThatCannotBeReadIsAnError(t *testing.T) {
	if _, _, err := Check(false, nil, envOf(map[string]string{PinEnv: "whenever"})); err == nil {
		t.Error("the gate accepted an unreadable pin")
	}
}

// TestDecideRefusesOnThePeakBlockItSitsIn: the refusal names the window it is IN and when that window
// ends, which is the sentence an operator needs to know when to retry.
func TestDecideRefusesOnThePeakBlockItSitsIn(t *testing.T) {
	seats := []Seat{{Name: "room.alpha", Model: "deepseek/deepseek-flash"}}
	first := Decide(OffpeakOnly, seats, peakFriday())
	second := Decide(OffpeakOnly, seats, At(time.Date(2026, 10, 9, 7, 30, 0, 0, time.UTC), nil))
	if !first.Refuse || !second.Refuse {
		t.Fatal("one of the two peak blocks was not refused")
	}
	if !strings.Contains(first.Message, "01:00-04:00") || !strings.Contains(first.Message, "04:00 UTC") {
		t.Errorf("first block refusal = %q", first.Message)
	}
	if !strings.Contains(second.Message, "06:00-10:00") || !strings.Contains(second.Message, "10:00 UTC") {
		t.Errorf("second block refusal = %q", second.Message)
	}
	if len(first.Seats) != 1 || first.Seats[0].Name != "room.alpha" {
		t.Errorf("the refusal must carry the seats that caused it: %v", first.Seats)
	}
	if off := Decide(OffpeakOnly, seats, offPeakFriday()); off.Refuse || off.Message != "" {
		t.Errorf("off-peak decided %+v, want a silent pass", off)
	}
}
