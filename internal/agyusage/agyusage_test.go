package agyusage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// measured is the answer of `agy -p /usage --output-format json` on agy 1.4.9, Google AI Pro, 2026-10-28.
const measured = `{"conversation_id":"","status":"SUCCESS","response":"...","duration_seconds":0,"num_turns":0,` +
	`"usage":{"input_tokens":0,"output_tokens":0,"thinking_tokens":0,"cache_read_tokens":0,"total_tokens":0},` +
	`"command":{"name":"usage","data":{"description":"Within each group, models share a weekly limit and a 5-hour limit.",` +
	`"groups":[{"name":"Gemini Models","description":"Models within this group: Gemini Flash, Gemini Pro","buckets":[` +
	`{"id":"gemini-weekly","name":"Weekly Limit Remaining","window":"weekly","remaining_fraction":0.16056880354881287,"reset_time":"2026-11-01T15:55:13Z"},` +
	`{"id":"gemini-5h","name":"Five Hour Limit Remaining","window":"5h","remaining_fraction":0.9107968807220459,"reset_time":"2026-10-28T09:39:53Z"}]},` +
	`{"name":"Claude and GPT models","description":"Models within this group: Claude Opus, Claude Sonnet, GPT-OSS","buckets":[` +
	`{"id":"3p-weekly","name":"Weekly Limit Remaining","window":"weekly","remaining_fraction":1,"reset_time":"2026-11-04T08:06:25Z"},` +
	`{"id":"3p-5h","name":"Five Hour Limit Remaining","window":"5h","remaining_fraction":1,"reset_time":"2026-10-28T13:06:25Z"}]},` +
	`{"name":"Gemini 4 Argon","description":"Argon quota is separate.","buckets":[` +
	`{"id":"gemini-argon-wtu-weekly","name":"Gemini 4 Argon Weekly Limit","window":"weekly","remaining_fraction":1,"reset_time":"2026-11-04T08:06:25Z"}]}]}}}`

var probeTime = time.Date(2026, 10, 28, 8, 6, 30, 0, time.UTC)

func TestTheMeasuredAnswerShowsBothWindowsOfTheSeatsGroup(t *testing.T) {
	groups, err := Parse([]byte(measured))
	if err != nil {
		t.Fatal(err)
	}
	r := Reading{FetchedAt: probeTime, Groups: groups}
	got := Line(r, "gemini-3.8-flash-low", probeTime)
	if want := "5h 91% left ↻1h33m · week 16% left ↻4d7h"; got != want {
		t.Errorf("gemini seat = %q, want %q", got, want)
	}
	if got := Line(r, "claude-sonnet-4.6", probeTime); !strings.HasPrefix(got, "5h 100% left ↻4h59m · week 100% left") {
		t.Errorf("claude seat = %q", got)
	}
}

// Argon carries only a weekly window: the 5h one is unknown, not invented.
func TestAWindowTheAnswerDoesNotCarryIsUnknown(t *testing.T) {
	groups, _ := Parse([]byte(measured))
	got := Line(Reading{FetchedAt: probeTime, Groups: groups}, "gemini-4-argon", probeTime)
	if want := "5h: unknown · week 100% left ↻6d23h"; got != want {
		t.Errorf("argon = %q, want %q", got, want)
	}
}

func TestAWindowPastItsResetIsUnknown(t *testing.T) {
	groups, _ := Parse([]byte(measured))
	later := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	got := Line(Reading{FetchedAt: probeTime, Groups: groups}, "gemini-3.8-flash-low", later)
	if !strings.HasPrefix(got, "5h: unknown · week 16% left") {
		t.Errorf("after the 5h reset = %q", got)
	}
}

func TestNoReadingAndAFailedReadingSayUnknown(t *testing.T) {
	if got := Line(Reading{}, "gemini-3.8-flash-low", probeTime); !strings.HasPrefix(got, "5h: unknown · week: unknown") {
		t.Errorf("no reading = %q", got)
	}
	got := Line(Reading{FetchedAt: probeTime, Error: "agy -p /usage failed: exit status 1"}, "gemini-3.8-flash-low", probeTime)
	if !strings.Contains(got, "unknown") || !strings.Contains(got, "exit status 1") {
		t.Errorf("failed reading = %q", got)
	}
}

func TestParseRefusesAnAnswerWithoutGroups(t *testing.T) {
	for _, raw := range []string{"not json", `{"status":"SUCCESS","command":{"name":"help"}}`, `{"command":{"name":"usage","data":{"groups":[]}}}`} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("Parse(%q) accepted it", raw)
		}
	}
}

func newTestWatcher(t *testing.T, now *time.Time, probe func(context.Context) ([]byte, error)) *Watcher {
	t.Helper()
	return &Watcher{Path: CachePath(t.TempDir()), Every: 5 * time.Minute, Probe: probe, Now: func() time.Time { return *now }}
}

// Panes ask every second; the probe runs at most once per interval, and a failure backs off like a success.
func TestTheWatcherProbesAtMostOncePerInterval(t *testing.T) {
	now := probeTime
	var calls atomic.Int32
	fail := false
	w := newTestWatcher(t, &now, func(context.Context) ([]byte, error) {
		calls.Add(1)
		if fail {
			return nil, errors.New("boom")
		}
		return []byte(measured), nil
	})
	if r := w.Reading(); !r.FetchedAt.IsZero() {
		t.Fatalf("first reading should be empty while the probe runs: %+v", r)
	}
	w.wait()
	for range 30 {
		now = now.Add(time.Second)
		w.Reading()
		w.wait()
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("probes in the first interval = %d, want 1", got)
	}
	if got := Line(w.Reading(), "gemini-3.8-flash-low", now); !strings.HasPrefix(got, "5h 91% left") {
		t.Errorf("cached line = %q", got)
	}

	fail = true
	now = now.Add(5 * time.Minute)
	w.Reading()
	w.wait()
	for range 30 {
		now = now.Add(time.Second)
		w.Reading()
		w.wait()
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("probes after a failure = %d, want 2", got)
	}
	r := w.Reading()
	if r.Error == "" || len(r.Groups) == 0 {
		t.Errorf("a failed probe should keep the last groups and name the error: %+v", r)
	}
}

// A second process finds the lock held and does not probe; it reads the other's answer from the file.
func TestALockHeldByAnotherPaneSkipsTheProbe(t *testing.T) {
	now := probeTime
	var calls atomic.Int32
	w := newTestWatcher(t, &now, func(context.Context) ([]byte, error) { calls.Add(1); return []byte(measured), nil })
	if err := os.MkdirAll(filepath.Dir(w.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	lock := w.Path + ".lock"
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	w.Reading()
	w.wait()
	if calls.Load() != 0 {
		t.Fatal("probed while another pane held the lock")
	}
	// A lock left by a probe that died is taken once it is stale.
	old := now.Add(-time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	w.Reading()
	w.wait()
	if calls.Load() != 1 {
		t.Fatalf("a stale lock was not taken: %d probes", calls.Load())
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Error("the lock was not released")
	}
}

func TestTheIntervalHasAFloor(t *testing.T) {
	if w := NewWatcher(t.TempDir(), time.Second); w.Every != MinEvery {
		t.Errorf("Every = %v, want %v", w.Every, MinEvery)
	}
	if w := NewWatcher(t.TempDir(), 0); w.Every != DefaultEvery {
		t.Errorf("Every = %v, want %v", w.Every, DefaultEvery)
	}
}
