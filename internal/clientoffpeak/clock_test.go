package clientoffpeak

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func pinClock(t *testing.T, now time.Time, net time.Time, reached bool) {
	t.Helper()
	prevNow, prevNet := clockNow, internetTime
	clockNow = func() time.Time { return now }
	internetTime = func(context.Context) (time.Time, string, bool) {
		if !reached {
			return time.Time{}, "", false
		}
		return net, "https://www.google.com", true
	}
	t.Cleanup(func() { clockNow, internetTime = prevNow, prevNet })
}

func runClock(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := clockCommand{}.Run(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// The stamp a seat writes is the owner's zone, with the zone's abbreviation, and needs no network.
func TestClockShortIsTheOwnersStamp(t *testing.T) {
	t.Setenv(OwnerTZEnv, "Europe/Paris")
	pinClock(t, time.Date(2026, 10, 28, 5, 25, 0, 0, time.UTC), time.Time{}, false)
	code, out, _ := runClock(t, "--short")
	if code != 0 || strings.TrimSpace(out) != "2026-10-28 06:25 CET" {
		t.Fatalf("exit %d, out %q; want 2026-10-28 06:25 CET", code, out)
	}
}

// Wednesday 05:25 UTC is off-peak, and the next change is the 06:00 UTC peak block.
func TestClockNamesThePriceWindowAndItsNextChange(t *testing.T) {
	t.Setenv(OwnerTZEnv, "Europe/Paris")
	now := time.Date(2026, 10, 28, 5, 25, 0, 0, time.UTC)
	pinClock(t, now, now.Add(-2*time.Second), true)
	code, out, _ := runClock(t)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{
		"local  2026-10-28 06:25:00 CET (Wed)  [Europe/Paris]",
		"UTC    2026-10-28 05:25:00",
		"machine clock matches internet time (drift 2s",
		"price  DeepSeek OFF-PEAK now; PEAK from Wed 07:00 CET (2026-10-28T06:00Z, in 35 min)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// A Friday after the last block flips on Monday 01:00 UTC, not on the off-peak Saturday.
func TestNextChangeSkipsTheWeekend(t *testing.T) {
	friday := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	if got := NextChange(friday, nil); !got.Equal(time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("next change %v, want Monday 01:00 UTC", got)
	}
	inPeak := time.Date(2026, 10, 28, 7, 10, 0, 0, time.UTC)
	if got := NextChange(inPeak, nil); !got.Equal(time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("next change %v, want 10:00 UTC", got)
	}
}

// A drifted clock is a warning and exit 1; an unreachable internet is "unverified", never a guess.
func TestClockWarnsOnDriftAndSaysUnverifiedOffline(t *testing.T) {
	t.Setenv(OwnerTZEnv, "UTC")
	now := time.Date(2026, 10, 28, 5, 25, 0, 0, time.UTC)
	pinClock(t, now, now.Add(-40*time.Second), true)
	code, out, _ := runClock(t)
	if code != 1 || !strings.Contains(out, "WARNING machine clock is off by 40s") {
		t.Errorf("drift: exit %d\n%s", code, out)
	}
	pinClock(t, now, time.Time{}, false)
	code, out, _ = runClock(t, "--json")
	var report ClockReport
	if err := json.Unmarshal([]byte(out), &report); err != nil || code != 0 {
		t.Fatalf("json: exit %d, %v\n%s", code, err, out)
	}
	if report.Check != "unverified" || report.DriftSecs != nil || report.Price != "OFF-PEAK" || report.NextChange != "2026-10-28T06:00Z" {
		t.Errorf("offline report: %+v", report)
	}
}

// A zone name the database does not know is refused, naming the variable.
func TestClockRefusesAnUnknownZone(t *testing.T) {
	t.Setenv(OwnerTZEnv, "Mars/Olympus")
	code, _, errOut := runClock(t, "--short")
	if code == 0 || !strings.Contains(errOut, OwnerTZEnv) {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
}
