// Package clientoffpeak answers one question - is DeepSeek billing at off-peak right now - and uses the
// answer to keep this client from STARTING OR RESUMING DeepSeek seats at the peak rate.
//
// The schedule is UTC, never local time: DeepSeek defines it in UTC (its peak blocks are the provider's
// working hours), so a gate written in local time drifts by an hour twice a year. France is UTC+2 until
// 2026-10-25 and UTC+1 after, which would move a local-time gate without anyone noticing.
//
//	PEAK  01:00-04:00 and 06:00-10:00 UTC, Monday-Friday
//	OFF   every other hour, and all of Saturday, Sunday and the days a holiday file names
//	Off-peak bills at exactly half the peak rate.
//
// Ported from the shell gate this client replaces, which named the widely-quoted single window
// "16:30-00:30 UTC" as OUTDATED.
//
// Chinese public holidays are NOT encoded: they move every year, and a wrong hardcoded list is worse
// than none. A file of YYYY-MM-DD lines named by DEEPSEEK_HOLIDAYS is read instead, and its days are
// off-peak for the whole day.
package clientoffpeak

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	// DateLayout is the day shape a holiday file carries and the schedule reports.
	DateLayout = "2006-01-02"
	// PinLayouts are the shapes OFFPEAK_UTC accepts. The first is the one the shell gate documented
	// ("YYYY-MM-DD HH:MM"); the second is the same instant written the way a machine usually sends it.
	PinLayouts = "2006-01-02 15:04, 2006-01-02T15:04"
	// HolidaysEnv names the holiday file, absolute or relative to the working directory. Unset is the
	// normal case and means "weekends only".
	HolidaysEnv = "DEEPSEEK_HOLIDAYS"
	// PinEnv pins the clock, which is what makes a gate on WHEN work may run verifiable at its
	// boundaries rather than only at whatever time the operator happens to run it.
	PinEnv = "OFFPEAK_UTC"
)

// peakBlocks are the two Mon-Fri windows, in minutes from UTC midnight, each [start, end).
var peakBlocks = [2][2]int{{60, 240}, {360, 600}}

// Verdict is the schedule's answer for ONE instant. Every field the shell gate printed is here, so the
// verb can render either form without re-deriving anything.
type Verdict struct {
	Peak    bool
	At      time.Time // the instant, always UTC
	Weekday int       // ISO 8601: Monday 1 .. Sunday 7
	Holiday bool
	Reason  string
	Next    string // the next boundary, or "" when there is none to name (a weekend or a holiday)
}

// State is the word the shell gate printed first.
func (v Verdict) State() string {
	if v.Peak {
		return "PEAK"
	}
	return "OFF-PEAK"
}

// Stamp is the JSON form of the instant: minutes precision and a literal Z, which is what the shell
// gate emitted ("%sT%sZ"). NOT time.RFC3339: that would add seconds the gate never printed.
func (v Verdict) Stamp() string { return v.At.Format("2006-01-02T15:04") + "Z" }

// Clock is HH:MM UTC.
func (v Verdict) Clock() string { return v.At.Format("15:04") }

// Day is the weekday's short English name, the shell gate's `date +%a`.
func (v Verdict) Day() string { return v.At.Format("Mon") }

// Holidays is the set of days a holiday file named. A nil *Holidays is the empty set, which is what a
// machine with no file has.
type Holidays struct{ days map[string]bool }

// Has reports whether a YYYY-MM-DD day is in the file. The test is the whole line and nothing else, so a
// trailing space or a comment on the same line does not match - the shell gate used `grep -qx`.
func (h *Holidays) Has(day string) bool { return h != nil && h.days[day] }

// LoadHolidays reads the file DEEPSEEK_HOLIDAYS names. An empty name, a missing file, a directory or an
// unreadable one is the EMPTY set and no error: every one of those meant "no holiday" to the shell gate,
// and a schedule that refused to answer because a file it was told to consult is absent would be worse
// than one that answers.
func LoadHolidays(path string) *Holidays {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	days := map[string]bool{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if line := scanner.Text(); line != "" {
			days[line] = true
		}
	}
	return &Holidays{days: days}
}

// At is the schedule for one instant: the pure function the boundary tests pin. It reads no clock, no
// file and no environment.
func At(now time.Time, holidays *Holidays) Verdict {
	utc := now.UTC()
	weekday := isoWeekday(utc)
	holiday := holidays.Has(utc.Format(DateLayout))
	minutes := utc.Hour()*60 + utc.Minute()

	v := Verdict{At: utc, Weekday: weekday, Holiday: holiday, Reason: "off-peak: outside the Mon-Fri peak blocks"}
	if weekday <= 5 && !holiday {
		if inBlock(minutes, peakBlocks[0]) {
			v.Peak, v.Reason = true, "PEAK: 01:00-04:00 UTC (Mon-Fri)"
		} else if inBlock(minutes, peakBlocks[1]) {
			v.Peak, v.Reason = true, "PEAK: 06:00-10:00 UTC (Mon-Fri)"
		}
	}
	// The reason a weekend or a holiday is off-peak replaces the generic one, in the shell gate's order:
	// a holiday first, then a weekend, so a Saturday that is also a holiday reads as the weekend.
	if holiday {
		v.Reason = "off-peak: Chinese public holiday"
	}
	if weekday >= 6 {
		v.Reason = "off-peak: weekend"
	}

	// The next boundary, so a caller can schedule rather than poll. A weekend or a holiday has none the
	// gate can name from the schedule alone, and the shell gate printed nothing there.
	switch {
	case v.Peak:
		if inBlock(minutes, peakBlocks[0]) {
			v.Next = "04:00 UTC (off-peak resumes)"
		} else {
			v.Next = "10:00 UTC (off-peak resumes)"
		}
	case weekday <= 5 && !holiday:
		switch {
		case minutes < peakBlocks[0][0]:
			v.Next = "01:00 UTC (peak starts)"
		case minutes < peakBlocks[1][0]:
			v.Next = "06:00 UTC (peak starts)"
		default:
			// "Tomorrow" is a SATURDAY when today is a Friday, and a weekend is off-peak in full, so the
			// next peak is Monday's 01:00 and not tomorrow's. DELIBERATE DIVERGENCE from the shell gate,
			// which printed "tomorrow" here and so named a Saturday morning that is off-peak: this line
			// exists so a caller can SCHEDULE on it, and a caller that scheduled on the shell's word ran
			// at an off-peak hour it did not need to wait for. Every other weekday's tomorrow does open
			// a peak at 01:00, so only weekday 5 is special-cased.
			if weekday == 5 {
				v.Next = "01:00 UTC Monday (peak starts)"
			} else {
				v.Next = "01:00 UTC tomorrow (peak starts)"
			}
		}
	}
	return v
}

// Now is At for the real clock, honouring the OFFPEAK_UTC pin out of the process environment.
func Now() (Verdict, error) { return NowFrom(os.Getenv) }

// NowFrom is Now with the environment as a parameter, so the pin and the holiday file are testable
// without a process-wide variable. A pin that is PRESENT and unparseable is an error naming the variable
// and the accepted shapes: a caller who pinned the clock wants the gate to use the pinned instant, so
// falling back to the real clock would answer a different question than the one asked.
func NowFrom(getenv func(string) string) (Verdict, error) {
	holidays := LoadHolidays(getenv(HolidaysEnv))
	pin := strings.TrimSpace(getenv(PinEnv))
	if pin == "" {
		return At(time.Now(), holidays), nil
	}
	for _, layout := range strings.Split(PinLayouts, ", ") {
		if parsed, err := time.ParseInLocation(layout, pin, time.UTC); err == nil {
			return At(parsed, holidays), nil
		}
	}
	return Verdict{}, fmt.Errorf("%s is not a UTC instant: %q (accepted: %s)", PinEnv, pin, PinLayouts)
}

// isoWeekday is the shell's `date +%u`: Monday 1 .. Sunday 7, while Go numbers Sunday 0.
func isoWeekday(t time.Time) int {
	if weekday := int(t.Weekday()); weekday != 0 {
		return weekday
	}
	return 7
}

// inBlock reports whether a minute of the day is inside one window.
func inBlock(minutes int, block [2]int) bool { return minutes >= block[0] && minutes < block[1] }
