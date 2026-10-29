package seatwait

import (
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
)

// MainRef is the branch a merge wait watches.
const MainRef = "main"

// LiveProbes are the probes wired to the machine: git, the slot files and the off-peak schedule.
func LiveProbes() Probes {
	return Probes{MergedAs: gitMergedAs, SlotFree: slotFree, OffPeak: offPeakAt}
}

func git(repo string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// gitMergedAs answers the first merge commit on main that carries sha, or main's tip when sha arrived by a
// fast-forward or a cherry-pick of the same commit.
func gitMergedAs(repo, sha string) (string, bool) {
	if _, err := git(repo, "merge-base", "--is-ancestor", sha, MainRef); err != nil {
		return "", false
	}
	if merges, err := git(repo, "rev-list", "--ancestry-path", "--merges", "--reverse", sha+".."+MainRef); err == nil && merges != "" {
		first, _, _ := strings.Cut(merges, "\n")
		return shortSHA(first), true
	}
	tip, err := git(repo, "rev-parse", MainRef)
	if err != nil {
		return "", false
	}
	return shortSHA(tip), true
}

// ResolveRepo is the git top level of dir, or "" when dir is not in a repo.
func ResolveRepo(dir string) string {
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return top
}

// ResolveSHA is the full commit a merge target names: branch@sha's sha, else the branch's tip.
func ResolveSHA(repo, target string) (string, error) {
	ref := target
	if _, sha, found := strings.Cut(target, "@"); found && sha != "" {
		ref = sha
	}
	return git(repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func offPeakAt(now time.Time) bool {
	holidays := clientoffpeak.LoadHolidays(os.Getenv(clientoffpeak.HolidaysEnv))
	return !clientoffpeak.At(now, holidays).Peak
}

// PeakEnd is when the current peak window ends, and false when it is off-peak now.
func PeakEnd(now time.Time) (time.Time, bool) {
	holidays := clientoffpeak.LoadHolidays(os.Getenv(clientoffpeak.HolidaysEnv))
	if !clientoffpeak.At(now, holidays).Peak {
		return time.Time{}, false
	}
	next := clientoffpeak.NextChange(now, holidays)
	return next, !next.IsZero()
}
