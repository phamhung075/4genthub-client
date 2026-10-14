package clientoffpeak

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// everyBoundary is the schedule's edge set, with the answer the SHELL gate this package replaces printed
// for each instant. MEASURED 2026-10-10 by running the shell off-peak gate with OFFPEAK_UTC
// pinned to that instant and reading its own --json line and human line; `holiday` marks the one row that
// also had a holiday file. The port is only right if it prints the SAME BYTES, so both forms are asserted
// verbatim rather than compared field by field: a field test would pass while the gate's output drifted.
//
// TWO ROWS ARE NOT THE SHELL'S BYTES, on purpose: the Friday instants after the last peak (10:00 and
// 23:30) carry `01:00 UTC Monday (peak starts)` where the script printed "01:00 UTC tomorrow", because
// tomorrow is a Saturday and a weekend is off-peak in full. The divergence is commented at the branch in
// schedule.go and in the CHANGELOG; 23:30 is the row whose absence let the script's line stand unnoticed.
var everyBoundary = []struct {
	pin     string
	peak    bool
	holiday int
	json    string
	human   string
}{
	{
		pin:  "2026-10-09 00:59",
		json: `{"state":"OFF-PEAK","utc":"2026-10-09T00:59Z","weekday":5,"holiday":0,"reason":"off-peak: outside the Mon-Fri peak blocks","next":"01:00 UTC (peak starts)"}`,
		human: "OFF-PEAK  00:59 UTC (Fri)  off-peak: outside the Mon-Fri peak blocks\n" +
			"  next: 01:00 UTC (peak starts)\n",
	},
	{
		pin:  "2026-10-09 01:00",
		peak: true,
		json: `{"state":"PEAK","utc":"2026-10-09T01:00Z","weekday":5,"holiday":0,"reason":"PEAK: 01:00-04:00 UTC (Mon-Fri)","next":"04:00 UTC (off-peak resumes)"}`,
		human: "PEAK  01:00 UTC (Fri)  PEAK: 01:00-04:00 UTC (Mon-Fri)\n" +
			"  next: 04:00 UTC (off-peak resumes)\n",
	},
	{
		pin:  "2026-10-09 03:59",
		peak: true,
		json: `{"state":"PEAK","utc":"2026-10-09T03:59Z","weekday":5,"holiday":0,"reason":"PEAK: 01:00-04:00 UTC (Mon-Fri)","next":"04:00 UTC (off-peak resumes)"}`,
		human: "PEAK  03:59 UTC (Fri)  PEAK: 01:00-04:00 UTC (Mon-Fri)\n" +
			"  next: 04:00 UTC (off-peak resumes)\n",
	},
	{
		pin:  "2026-10-09 04:00",
		json: `{"state":"OFF-PEAK","utc":"2026-10-09T04:00Z","weekday":5,"holiday":0,"reason":"off-peak: outside the Mon-Fri peak blocks","next":"06:00 UTC (peak starts)"}`,
		human: "OFF-PEAK  04:00 UTC (Fri)  off-peak: outside the Mon-Fri peak blocks\n" +
			"  next: 06:00 UTC (peak starts)\n",
	},
	{
		pin:  "2026-10-09 05:59",
		json: `{"state":"OFF-PEAK","utc":"2026-10-09T05:59Z","weekday":5,"holiday":0,"reason":"off-peak: outside the Mon-Fri peak blocks","next":"06:00 UTC (peak starts)"}`,
		human: "OFF-PEAK  05:59 UTC (Fri)  off-peak: outside the Mon-Fri peak blocks\n" +
			"  next: 06:00 UTC (peak starts)\n",
	},
	{
		pin:  "2026-10-09 06:00",
		peak: true,
		json: `{"state":"PEAK","utc":"2026-10-09T06:00Z","weekday":5,"holiday":0,"reason":"PEAK: 06:00-10:00 UTC (Mon-Fri)","next":"10:00 UTC (off-peak resumes)"}`,
		human: "PEAK  06:00 UTC (Fri)  PEAK: 06:00-10:00 UTC (Mon-Fri)\n" +
			"  next: 10:00 UTC (off-peak resumes)\n",
	},
	{
		pin:  "2026-10-09 09:59",
		peak: true,
		json: `{"state":"PEAK","utc":"2026-10-09T09:59Z","weekday":5,"holiday":0,"reason":"PEAK: 06:00-10:00 UTC (Mon-Fri)","next":"10:00 UTC (off-peak resumes)"}`,
		human: "PEAK  09:59 UTC (Fri)  PEAK: 06:00-10:00 UTC (Mon-Fri)\n" +
			"  next: 10:00 UTC (off-peak resumes)\n",
	},
	{
		pin:  "2026-10-09 10:00",
		json: `{"state":"OFF-PEAK","utc":"2026-10-09T10:00Z","weekday":5,"holiday":0,"reason":"off-peak: outside the Mon-Fri peak blocks","next":"01:00 UTC Monday (peak starts)"}`,
		human: "OFF-PEAK  10:00 UTC (Fri)  off-peak: outside the Mon-Fri peak blocks\n" +
			"  next: 01:00 UTC Monday (peak starts)\n",
	},
	{
		pin: "2026-10-09 23:30",
		// The case that caught the "tomorrow" line: on a Friday the shell gate printed
		// "01:00 UTC tomorrow (peak starts)", but tomorrow is Saturday and a weekend is off-peak in full,
		// so the next peak is Monday's 01:00. See the divergence note above everyBoundary.
		json: `{"state":"OFF-PEAK","utc":"2026-10-09T23:30Z","weekday":5,"holiday":0,"reason":"off-peak: outside the Mon-Fri peak blocks","next":"01:00 UTC Monday (peak starts)"}`,
		human: "OFF-PEAK  23:30 UTC (Fri)  off-peak: outside the Mon-Fri peak blocks\n" +
			"  next: 01:00 UTC Monday (peak starts)\n",
	},
	{
		pin:   "2026-10-10 12:00",
		json:  `{"state":"OFF-PEAK","utc":"2026-10-10T12:00Z","weekday":6,"holiday":0,"reason":"off-peak: weekend","next":""}`,
		human: "OFF-PEAK  12:00 UTC (Sat)  off-peak: weekend\n",
	},
	{
		pin:   "2026-10-11 07:00",
		json:  `{"state":"OFF-PEAK","utc":"2026-10-11T07:00Z","weekday":7,"holiday":0,"reason":"off-peak: weekend","next":""}`,
		human: "OFF-PEAK  07:00 UTC (Sun)  off-peak: weekend\n",
	},
	{
		pin:  "2026-10-12 05:00",
		json: `{"state":"OFF-PEAK","utc":"2026-10-12T05:00Z","weekday":1,"holiday":0,"reason":"off-peak: outside the Mon-Fri peak blocks","next":"06:00 UTC (peak starts)"}`,
		human: "OFF-PEAK  05:00 UTC (Mon)  off-peak: outside the Mon-Fri peak blocks\n" +
			"  next: 06:00 UTC (peak starts)\n",
	},
	{
		pin:     "2026-10-09 02:00",
		holiday: 1,
		json:    `{"state":"OFF-PEAK","utc":"2026-10-09T02:00Z","weekday":5,"holiday":1,"reason":"off-peak: Chinese public holiday","next":""}`,
		human:   "OFF-PEAK  02:00 UTC (Fri)  off-peak: Chinese public holiday\n",
	},
}

// TestTheBoundariesMatchTheShellGate drives the VERB with the clock pinned, which is the path the order
// asks to be verifiable: the same instant in, the same bytes and the same exit code out. 0 means off-peak
// and safe to run, 1 means peak - the contract the shell gate held and the one callers gate on.
func TestTheBoundariesMatchTheShellGate(t *testing.T) {
	holidays := filepath.Join(t.TempDir(), "cn-holidays.txt")
	if err := os.WriteFile(holidays, []byte("2026-10-09\n2026-05-01\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, boundary := range everyBoundary {
		t.Run(boundary.pin, func(t *testing.T) {
			t.Setenv(PinEnv, boundary.pin)
			if boundary.holiday == 1 {
				t.Setenv(HolidaysEnv, holidays)
			} else {
				t.Setenv(HolidaysEnv, "")
			}
			want := offPeakExit
			if boundary.peak {
				want = ExitPeak
			}

			var jsonOut, humanOut, quietOut, errOut bytes.Buffer
			if code := runReport(false, true, &jsonOut, &errOut); code != want {
				t.Fatalf("--json exit %d, want %d (stderr %q)", code, want, errOut.String())
			}
			if got := jsonOut.String(); got != boundary.json+"\n" {
				t.Errorf("--json printed\n  %q\nwant the shell gate's line\n  %q", got, boundary.json+"\n")
			}
			if code := runReport(false, false, &humanOut, &errOut); code != want {
				t.Fatalf("human exit %d, want %d", code, want)
			}
			if got := humanOut.String(); got != boundary.human {
				t.Errorf("human line = %q, want %q", got, boundary.human)
			}
			if code := runReport(true, false, &quietOut, &errOut); code != want {
				t.Errorf("--quiet exit %d, want %d", code, want)
			}
			if quietOut.Len() != 0 {
				t.Errorf("--quiet printed %q, want nothing", quietOut.String())
			}
		})
	}
}

const offPeakExit = 0

// TestQuietAndJSONTogetherPrintJSON is the shell gate's branch order: --json is checked first, so asking
// for both is not an error and the machine-readable answer wins.
func TestQuietAndJSONTogetherPrintJSON(t *testing.T) {
	t.Setenv(PinEnv, "2026-10-10 12:00")
	var out, errOut bytes.Buffer
	if code := runReport(true, true, &out, &errOut); code != offPeakExit {
		t.Fatalf("exit %d, want 0", code)
	}
	if !strings.HasPrefix(out.String(), `{"state":"OFF-PEAK"`) {
		t.Errorf("--quiet --json printed %q, want the JSON line", out.String())
	}
}

// TestAHolidayFileIsReadOnlyForTheDayItNames: the file is optional, its absence is not an error, and only
// the WHOLE line counts - the shell gate used `grep -qx`, so a trailing space or a comment on the same
// line names nothing.
func TestAHolidayFileIsReadOnlyForTheDayItNames(t *testing.T) {
	dir := t.TempDir()
	exact := filepath.Join(dir, "exact.txt")
	if err := os.WriteFile(exact, []byte("2026-10-09\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	padded := filepath.Join(dir, "padded.txt")
	if err := os.WriteFile(padded, []byte("2026-10-09 \n2026-10-09 # a comment\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		path    string
		holiday bool
	}{
		"the day itself":           {exact, true},
		"a trailing space":         {padded, false},
		"a file that is not there": {filepath.Join(dir, "nope.txt"), false},
		"a directory":              {dir, false},
		"nothing named":            {"", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			verdict, err := NowFrom(func(key string) string {
				if key == HolidaysEnv {
					return c.path
				}
				return "2026-10-09 02:00"
			})
			if err != nil {
				t.Fatal(err)
			}
			if verdict.Holiday != c.holiday {
				t.Errorf("holiday = %v, want %v", verdict.Holiday, c.holiday)
			}
			if verdict.Peak == c.holiday {
				t.Errorf("peak = %v for a day whose holiday flag is %v: the two must be opposites here",
					verdict.Peak, c.holiday)
			}
		})
	}
}

// TestAPinThatIsNotAnInstantIsAnError: an operator who pinned the clock wants the gate to use the pinned
// instant, so a malformed one must not be answered from the real clock. The message names the variable and
// both shapes, and the verb exits 2 rather than reporting a state nobody asked about.
func TestAPinThatIsNotAnInstantIsAnError(t *testing.T) {
	t.Setenv(PinEnv, "yesterday")
	_, err := Now()
	if err == nil {
		t.Fatal("a malformed pin was accepted")
	}
	for _, want := range []string{PinEnv, "yesterday", "2006-01-02 15:04"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}

	var out, errOut bytes.Buffer
	if code := runReport(false, true, &out, &errOut); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q while the clock was unusable", out.String())
	}
	if !strings.Contains(errOut.String(), PinEnv) {
		t.Errorf("stderr %q does not name the variable at fault", errOut.String())
	}
}

// TestWithoutAPinTheRealClockIsUsed: the pin is for tests and for a caller that needs a fixed instant; a
// machine with no pin gets its own clock, read once.
func TestWithoutAPinTheRealClockIsUsed(t *testing.T) {
	verdict, err := NowFrom(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if delta := time.Since(verdict.At); delta > time.Minute || delta < -time.Minute {
		t.Errorf("the verdict is for %s, %s from now, want the real clock", verdict.At, delta)
	}
	if verdict.Weekday != isoWeekday(verdict.At) {
		t.Errorf("weekday = %d, want %d", verdict.Weekday, isoWeekday(verdict.At))
	}
}

// TestAtIsPureAndPinnedToUTC: the schedule does not consult a clock, a file or the environment, and an
// instant handed in with an offset is answered for its UTC moment.
func TestAtIsPureAndPinnedToUTC(t *testing.T) {
	offset := time.FixedZone("UTC+2", 2*60*60)
	// 02:00 in a +2 zone is 00:00 UTC, which is OFF-PEAK; read as a local clock it would be PEAK.
	verdict := At(time.Date(2026, 10, 9, 2, 0, 0, 0, offset), nil)
	if verdict.Peak {
		t.Errorf("state %s for an instant that is 00:00 UTC, want off-peak", verdict.State())
	}
	if got, want := verdict.Stamp(), "2026-10-09T00:00Z"; got != want {
		t.Errorf("stamp = %q, want %q", got, want)
	}
	if got, want := verdict.Day(), "Fri"; got != want {
		t.Errorf("day = %q, want %q", got, want)
	}
}
