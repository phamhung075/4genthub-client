// Package agyusage reads the usage limits of the Antigravity CLI (agy) account the seats of a room run on, for
// the watch grid's pane title.
//
// WHERE THE NUMBERS COME FROM, measured 2026-10-28 on agy 1.4.9 with a Google AI Pro sign-in: agy keeps its quota
// in memory (quota_manager.go logs "doRefreshQuota: starting reload" and no value), writes none of it under
// ~/.gemini/antigravity-cli (no cache file, nothing in a conversation transcript), and its TUI status line shows
// no quota. The one reader is the `/usage` slash command run in print mode:
//
//	agy -p /usage --output-format json
//
// which answers in about 4 s with num_turns 0 and every token count 0 (no model turn, no quota spent) and creates
// no conversation. Its `command.data.groups[]` are the model groups ("Gemini Models", "Claude and GPT models",
// "Gemini 4 Argon"), each with buckets whose `window` is "5h" or "weekly", a `remaining_fraction` and a
// `reset_time` in UTC. A group may carry only one window (Argon has only a weekly one), so a window the answer
// does not carry is shown as unknown, never filled in.
//
// The quota belongs to the agy SIGN-IN, not to a seat: every agy seat on this machine shares it. So the reading
// is one cache file under the state root, refreshed by at most one probe per interval machine-wide (a lock file
// keeps two panes from probing at once), and a failed probe is cached too, so an error backs off for the same
// interval rather than retrying every second.
package agyusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Bucket is one window of one group: "5h" or "weekly".
type Bucket struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Window            string   `json:"window"`
	RemainingFraction *float64 `json:"remaining_fraction"`
	ResetTime         string   `json:"reset_time"`
}

// Group is the set of models that share one pair of windows.
type Group struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Buckets     []Bucket `json:"buckets"`
}

// Reading is what the cache file holds: the groups of the last probe, when it ran, and its error if it failed.
type Reading struct {
	FetchedAt time.Time `json:"fetched_at"`
	Groups    []Group   `json:"groups,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// Parse reads the print-mode JSON answer of `agy -p /usage --output-format json`.
func Parse(raw []byte) ([]Group, error) {
	var answer struct {
		Status  string `json:"status"`
		Command struct {
			Name string `json:"name"`
			Data struct {
				Groups []Group `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, fmt.Errorf("agy /usage answered something that is not JSON: %w", err)
	}
	if answer.Command.Name != "usage" {
		return nil, fmt.Errorf("agy /usage answered no usage command (status %q)", answer.Status)
	}
	if len(answer.Command.Data.Groups) == 0 {
		return nil, errors.New("agy /usage answered no quota groups")
	}
	return answer.Command.Data.Groups, nil
}

// GroupFor is the group whose limits a model draws on: Claude and GPT models share one group, Argon has its own,
// and every other Gemini model is in "Gemini Models". nil when the answer has no group for the model.
func GroupFor(groups []Group, model string) *Group {
	m := strings.ToLower(model)
	match := func(want func(name string) bool) *Group {
		for index := range groups {
			if want(strings.ToLower(groups[index].Name)) {
				return &groups[index]
			}
		}
		return nil
	}
	switch {
	case strings.Contains(m, "claude") || strings.Contains(m, "gpt"):
		return match(func(n string) bool { return strings.Contains(n, "claude") || strings.Contains(n, "gpt") })
	case strings.Contains(m, "argon"):
		return match(func(n string) bool { return strings.Contains(n, "argon") })
	}
	if exact := match(func(n string) bool { return n == "gemini models" }); exact != nil {
		return exact
	}
	return match(func(n string) bool { return strings.HasPrefix(n, "gemini") && !strings.Contains(n, "argon") })
}

// Line is the usage text for a pane title: the 5h and weekly windows of the model's group, as remaining percent
// and the time to their reset - "5h 91% left ↻1h33m · week 16% left ↻4d7h". A window the answer does not carry,
// or whose reset time has already passed (the reading is older than the window), is "unknown".
func Line(r Reading, model string, now time.Time) string {
	if r.FetchedAt.IsZero() {
		return "5h: unknown · week: unknown (agy usage not read yet)"
	}
	if r.Error != "" && len(r.Groups) == 0 {
		return "5h: unknown · week: unknown (agy usage: " + r.Error + ")"
	}
	group := GroupFor(r.Groups, model)
	if group == nil {
		return fmt.Sprintf("5h: unknown · week: unknown (agy usage has no group for %s)", model)
	}
	return window(group, "5h", "5h", now) + " · " + window(group, "weekly", "week", now)
}

func window(group *Group, name, label string, now time.Time) string {
	for _, bucket := range group.Buckets {
		if bucket.Window != name || bucket.RemainingFraction == nil {
			continue
		}
		reset, err := time.Parse(time.RFC3339, bucket.ResetTime)
		if err != nil {
			return fmt.Sprintf("%s %d%% left", label, percent(*bucket.RemainingFraction))
		}
		left := reset.Sub(now)
		if left <= 0 {
			return label + ": unknown"
		}
		return fmt.Sprintf("%s %d%% left ↻%s", label, percent(*bucket.RemainingFraction), countdown(left))
	}
	return label + ": unknown"
}

func percent(fraction float64) int {
	value := int(math.Floor(fraction*100 + 1e-9))
	return max(0, min(100, value))
}

// countdown is the statusline's form: 4d7h, 1h33m, 12m.
func countdown(d time.Duration) string {
	minutes := int(d.Minutes())
	days, hours, mins := minutes/(24*60), minutes%(24*60)/60, minutes%60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}

// Probe runs the one reader agy has. It spends no model turn.
func Probe(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "agy", "-p", "/usage", "--output-format", "json")
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("agy -p /usage failed: %w", err)
	}
	return out, nil
}

const (
	// DefaultEvery is how old a reading may get before a pane asks for a new one.
	DefaultEvery = 5 * time.Minute
	// MinEvery is the floor on the interval, whatever the environment says: the probe is cheap, but not free.
	MinEvery     = time.Minute
	probeTimeout = 30 * time.Second
	// staleLock is how old a lock file may get before it is taken as left behind by a probe that died.
	staleLock = 2 * probeTimeout
)

// Watcher hands a pane the cached reading and starts a background refresh when it is older than Every. It never
// blocks the caller on a probe.
type Watcher struct {
	Path  string
	Every time.Duration
	Probe func(context.Context) ([]byte, error)
	Now   func() time.Time

	mu       sync.Mutex
	inflight bool
	cached   Reading
	modTime  time.Time
	done     chan struct{}
	// attempted is when this process last started a refresh, so a cache it cannot write never turns into a
	// probe every second: the interval holds in memory as well as on disk.
	attempted time.Time
}

// NewWatcher is a watcher on <stateRoot>/cache/agy-usage.json that refreshes every `every` (DefaultEvery when 0,
// at least MinEvery) with the real probe.
func NewWatcher(stateRoot string, every time.Duration) *Watcher {
	if every <= 0 {
		every = DefaultEvery
	}
	return &Watcher{Path: CachePath(stateRoot), Every: max(every, MinEvery), Probe: Probe, Now: time.Now}
}

// CachePath is where the reading lives: one file per machine state root, shared by every pane and room.
func CachePath(stateRoot string) string {
	return filepath.Join(stateRoot, "cache", "agy-usage.json")
}

// Reading is the last reading on disk. When it is older than Every and no refresh is running, it starts one in
// the background and answers with what it has.
func (w *Watcher) Reading() Reading {
	w.mu.Lock()
	defer w.mu.Unlock()
	if info, err := os.Stat(w.Path); err == nil && !info.ModTime().Equal(w.modTime) {
		if r, err := readCache(w.Path); err == nil {
			w.cached, w.modTime = r, info.ModTime()
		}
	}
	now := w.Now()
	if !w.inflight && now.Sub(w.cached.FetchedAt) >= w.Every && now.Sub(w.attempted) >= w.Every {
		w.inflight = true
		w.attempted = now
		w.done = make(chan struct{})
		go w.refresh(w.done)
	}
	return w.cached
}

// wait is for tests: it returns once the refresh that is running, if any, has finished.
func (w *Watcher) wait() {
	w.mu.Lock()
	done := w.done
	w.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (w *Watcher) refresh(done chan struct{}) {
	defer func() {
		w.mu.Lock()
		w.inflight = false
		w.mu.Unlock()
		close(done)
	}()
	if err := os.MkdirAll(filepath.Dir(w.Path), 0o700); err != nil {
		return
	}
	lock := w.Path + ".lock"
	if !takeLock(lock, w.Now()) {
		return // another pane is probing; its answer reaches this one through the file
	}
	defer os.Remove(lock)
	// Another process may have refreshed while this one waited for the lock.
	if r, err := readCache(w.Path); err == nil && w.Now().Sub(r.FetchedAt) < w.Every {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	reading := Reading{FetchedAt: w.Now()}
	raw, err := w.Probe(ctx)
	if err == nil {
		reading.Groups, err = Parse(raw)
	}
	if err != nil {
		reading.Error = shortError(err)
		// Keep the last good groups so a failed probe does not blank the title; the error still backs off.
		if previous, readErr := readCache(w.Path); readErr == nil {
			reading.Groups = previous.Groups
		}
	}
	_ = writeCache(w.Path, reading)
	// Hand this process its own answer at once: the file's mtime has a coarse clock, and two writes inside one
	// tick would otherwise look unchanged. The zero modTime makes the next call read the file again anyway.
	w.mu.Lock()
	w.cached, w.modTime = reading, time.Time{}
	w.mu.Unlock()
}

func takeLock(path string, now time.Time) bool {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		file.Close()
		return true
	}
	if info, statErr := os.Stat(path); statErr == nil && now.Sub(info.ModTime()) > staleLock {
		_ = os.Remove(path)
		file, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			file.Close()
			return true
		}
	}
	return false
}

func shortError(err error) string {
	text := strings.TrimSpace(err.Error())
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	if len(text) > 60 {
		text = text[:60] + "…"
	}
	return text
}

func readCache(path string) (Reading, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Reading{}, err
	}
	var r Reading
	if err := json.Unmarshal(raw, &r); err != nil {
		return Reading{}, err
	}
	return r, nil
}

func writeCache(path string, r Reading) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agy-usage-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
