package clientllmproxy

import (
	"os"
	"path/filepath"
	"testing"
)

// noListener is the probe of a machine with nothing up: every port is unanswered.
func noListener(int) (string, bool) { return "", false }

// sharedListener answers 7391 as either route on ANTHROPIC_BASE_URL and nothing else, which is the shape
// of one listener serving the shared variable.
func sharedListener(route string) func(port int) (string, bool) {
	return func(port int) (string, bool) {
		if port == 7391 {
			return route, true
		}
		return "", false
	}
}

// TestTheReportShapes pins the three states doctor renders, and the count is of ROUTES: one listener on
// the shared variable makes TWO routes proxied, and codex never does - its entry is inert until a seat
// runs under the profile, so it reads unproxied with that qualifier.
func TestTheReportShapes(t *testing.T) {
	cases := []struct {
		name   string
		state  string
		listen func(port int) (string, bool)
		codex  bool
		want   string
	}{
		{
			name:   "nothing listening",
			listen: noListener,
			want:   "llm: 0 proxied routes, 5 unproxied (claude-code, omp-anthropic, omp-deepseek, codex, agy), 0 withheld today",
		},
		{
			name:   "one listener serving the shared variable",
			listen: sharedListener("claude-code"),
			want:   "llm: 2 proxied routes, 3 unproxied (omp-deepseek, codex, agy), 0 withheld today",
		},
		{
			name:   "the same listener answers for the other route on the variable",
			listen: sharedListener("omp-anthropic"),
			want:   "llm: 2 proxied routes, 3 unproxied (omp-deepseek, codex, agy), 0 withheld today",
		},
		{
			name:   "a listener on another surface is not this route's proxy",
			listen: sharedListener("codex"),
			want:   "llm: 0 proxied routes, 5 unproxied (claude-code, omp-anthropic, omp-deepseek, codex, agy), 0 withheld today",
		},
		{
			name:   "codex entry present but not selected",
			listen: sharedListener("claude-code"),
			codex:  true,
			want:   "llm: 2 proxied routes, 3 unproxied (omp-deepseek, codex-unselected, agy), 0 withheld today",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := report(t.TempDir(), c.listen, c.codex); got != c.want {
				t.Errorf("report = %q\nwant      %q", got, c.want)
			}
		})
	}
}

// TestTheReportCountsWithheldRecords pins the last field: every refusal record under
// <state-root>/llm-withheld/<route>/ counts, and a state root with none counts zero.
func TestTheReportCountsWithheldRecords(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"claude-code/2026-10-21T12-00-00Z.json", "claude-code/2026-10-21T12-00-01Z.json",
		"omp-anthropic/2026-10-21T12-00-02Z.json"} {
		path := filepath.Join(root, recordDirName, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("create the record directory: %v", err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write the record: %v", err)
		}
	}
	want := "llm: 2 proxied routes, 3 unproxied (omp-deepseek, codex, agy), 3 withheld today"
	if got := report(root, sharedListener("claude-code"), false); got != want {
		t.Errorf("report = %q\nwant      %q", got, want)
	}
}

// TestReportRendersTheDetailLinePrefix keeps the exported entry point honest against a real environment:
// its label and withheld figure are the contract doctor prints, without this test asserting what this
// machine happens to be listening to.
func TestReportRendersTheDetailLinePrefix(t *testing.T) {
	got := Report("")
	if len(got) < len("llm: ") || got[:len("llm: ")] != "llm: " {
		t.Errorf("Report = %q, want it to start with the llm: label", got)
	}
	if got[len(got)-len(", 0 withheld today"):] != ", 0 withheld today" {
		t.Errorf("Report = %q, want it to end with the withheld figure", got)
	}
}
