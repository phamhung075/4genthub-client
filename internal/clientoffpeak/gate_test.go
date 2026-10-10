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
		if err != nil || mode != AnyTime {
			t.Errorf("ParseMode(%q) = %v, %v; want any-time", value, mode, err)
		}
	}
	for _, value := range []string{"ture", "maybe", "2", "-"} {
		if _, err := ParseMode(value); err == nil {
			t.Errorf("ParseMode(%q) was accepted; a typo here decides the money question", value)
		}
	}
}

// TestTheFlagBeatsTheEnvironmentWhichBeatsTheDefault is the precedence the order asked for, asserted on
// all four combinations plus the default.
func TestTheFlagBeatsTheEnvironmentWhichBeatsTheDefault(t *testing.T) {
	anyTime, offpeak := AnyTime, OffpeakOnly
	cases := []struct {
		name       string
		flag       *Mode
		env        string
		want       Mode
		wantSource string
	}{
		{"nothing set", nil, "", OffpeakOnly, "the default (" + ModeEnv + " is not set)"},
		{"the environment turns it off", nil, "0", AnyTime, ModeEnv + "=0 in the environment or the client's env file"},
		{"the environment says on in words", nil, "on", OffpeakOnly, ModeEnv + "=on in the environment or the client's env file"},
		{"--any-time beats an env that says on", &anyTime, "1", AnyTime, "the " + FlagAnyTime + " flag"},
		{"--offpeak-only beats an env that says off", &offpeak, "0", OffpeakOnly, "the " + FlagOffpeakOnly + " flag"},
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
	_, _, err := ResolveMode(nil, envOf(map[string]string{ModeEnv: "nah"}))
	if err == nil {
		t.Fatal("OFFPEAK_ONLY=nah was accepted")
	}
	for _, want := range []string{ModeEnv, `"nah"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}
}

// TestModeFromFlagsTakesThePairAnywhereAndRefusesBoth: the flags may sit before or after the verb's own
// positional (the Python's argparse allowed either), and a caller who wrote BOTH meant neither.
func TestModeFromFlagsTakesThePairAnywhereAndRefusesBoth(t *testing.T) {
	mode, rest, err := ModeFromFlags([]string{"my-room", FlagAnyTime})
	if err != nil || mode == nil || *mode != AnyTime {
		t.Fatalf("mode = %v, rest = %v, err = %v", mode, rest, err)
	}
	if len(rest) != 1 || rest[0] != "my-room" {
		t.Errorf("rest = %v, want the positional alone", rest)
	}

	mode, rest, err = ModeFromFlags([]string{FlagOffpeakOnly, "my-room", "--after-seconds", "5"})
	if err != nil || mode == nil || *mode != OffpeakOnly {
		t.Fatalf("mode = %v, err = %v", mode, err)
	}
	if strings.Join(rest, " ") != "my-room --after-seconds 5" {
		t.Errorf("rest = %v, want the flags this package does not own left alone", rest)
	}

	if _, _, err := ModeFromFlags([]string{FlagOffpeakOnly, FlagAnyTime}); err == nil {
		t.Error("both flags were accepted; the pair must refuse rather than pick one")
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
	anyTime, offpeak := AnyTime, OffpeakOnly

	cases := []struct {
		name    string
		mode    *Mode
		env     map[string]string
		nodes   []map[string]any
		refused bool
	}{
		{"peak, deepseek, setting on", &offpeak, nil, []map[string]any{deepseekNode}, true},
		{"peak, deepseek, setting off", &anyTime, nil, []map[string]any{deepseekNode}, false},
		{"peak, deepseek, env off", nil, map[string]string{ModeEnv: "0"}, []map[string]any{deepseekNode}, false},
		{"peak, deepseek, default (on)", nil, nil, []map[string]any{deepseekNode}, true},
		{"off-peak, deepseek, setting on", &offpeak, map[string]string{PinEnv: "2026-10-09 05:00"}, []map[string]any{deepseekNode}, false},
		{"peak, no deepseek seat", &offpeak, nil, []map[string]any{localNode}, false},
		{"peak, no seats at all", &offpeak, nil, nil, false},
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
			proceed, message, err := Check(c.mode, c.nodes, envOf(env))
			if err != nil {
				t.Fatal(err)
			}
			if refused := !proceed; refused != c.refused {
				t.Fatalf("refused = %v, want %v (message %q)", refused, c.refused, message)
			}
			if c.refused {
				for _, want := range []string{"deepseek/deepseek-flash", "room.alpha", "PEAK", "04:00 UTC", FlagAnyTime, ModeEnv + "=" + ValueOff} {
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

// TestACheckThatIsOffDoesNotReadTheClock: --any-time means no check, so a pinned instant the check would
// never use cannot fail the run - the same reason the verbs resolve the mode before consulting the panel.
func TestACheckThatIsOffDoesNotReadTheClock(t *testing.T) {
	anyTime := AnyTime
	proceed, _, err := Check(&anyTime, []map[string]any{{"logicalId": "room.a", "model": "deepseek/x"}},
		envOf(map[string]string{PinEnv: "not an instant"}))
	if err != nil || !proceed {
		t.Errorf("proceed = %v, err = %v; a check that is off must not read the clock", proceed, err)
	}
}

func TestAPinnedClockThatCannotBeReadIsAnError(t *testing.T) {
	if _, _, err := Check(nil, nil, envOf(map[string]string{PinEnv: "whenever"})); err == nil {
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
