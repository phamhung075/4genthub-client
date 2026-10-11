// Installing the guard into a checkout. The hook files are the two templates in hooks/, embedded in the
// client binary so an installed client can write them anywhere it is asked to, without needing a checkout of
// its own source.
//
// A HOOK WE DID NOT WRITE IS NEVER LOST. The checkout in this room already runs a pre-commit written by the
// pre-commit framework, and other hooks beside it; `core.hooksPath` is NOT set here, so git runs whatever is
// in its own hooks folder. Installing the guard therefore MOVES an existing hook aside to
// `<name>.pre-4genteam` and writes a guard hook that runs it, exactly as git would have. A backup that
// already exists is never overwritten: two tools claiming the same backup name would lose one of them.
package clientguard

import (
	"embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

//go:embed hooks/pre-commit hooks/pre-push
var hookTemplates embed.FS

// BackupSuffix is what an existing hook is moved to before the guard takes its name.
const BackupSuffix = ".pre-4genteam"

const (
	guardBinPlaceholder = "__GUARD_BIN__"
	prevPlaceholder     = "__PREV__"
)

// InstalledHook is one hook the guard wrote: the file, and the hook it replaced (empty when there was none).
type InstalledHook struct {
	Name     string
	Path     string
	Replaced string
}

// Install writes the guard's hooks into the hooks folder git resolves for the checkout at dir, so that
// every git command in that checkout - and in every linked worktree of it, because the hooks folder is the
// repository's - runs the guard. clientBinary is the absolute path of the client the hooks will run; the
// caller passes os.Executable, so the hook runs the client that installed it rather than whatever happens to
// be on PATH.
//
// It reports what it wrote. Nothing is removed and nothing is overwritten except a hook the guard itself
// wrote before, which is why re-running it is safe.
func Install(dir, clientBinary string, out io.Writer) ([]InstalledHook, error) {
	if !filepath.IsAbs(clientBinary) {
		absolute, err := filepath.Abs(clientBinary)
		if err != nil {
			return nil, fmt.Errorf("the client's own path could not be resolved: %v", err)
		}
		clientBinary = absolute
	}
	folder, err := HooksFolder(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return nil, fmt.Errorf("cannot create the hooks folder %s: %v", folder, err)
	}

	var installed []InstalledHook
	for _, name := range []string{HookPreCommit, HookPrePush} {
		body, err := hookTemplates.ReadFile("hooks/" + name)
		if err != nil {
			return installed, fmt.Errorf("the guard's %s template is missing from this client: %v", name, err)
		}
		path := filepath.Join(folder, name)
		previous, err := previousHook(path)
		if err != nil {
			return installed, err
		}
		script := strings.ReplaceAll(string(body), guardBinPlaceholder, clientBinary)
		script = strings.ReplaceAll(script, prevPlaceholder, previous)
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			return installed, fmt.Errorf("cannot write the guard's %s hook to %s: %v", name, path, err)
		}
		installed = append(installed, InstalledHook{Name: name, Path: path, Replaced: previous})
		fmt.Fprintf(out, "installed %s\n", path)
		if previous != "" {
			fmt.Fprintf(out, "  it now runs the hook it replaced: %s\n", previous)
		}
	}
	return installed, nil
}

// UninstalledHook is one hook the guard took back out: the file, and the path the hook it had replaced was
// put back at (empty when there was nothing before the guard, so the file is simply gone).
type UninstalledHook struct {
	Name     string
	Path     string
	Restored string
}

// Uninstall takes the guard's hooks back out of the hooks folder git resolves for dir, and puts back whatever
// the installer moved aside, so a checkout that was carrying its own hooks is exactly as it was.
//
// A hook that is NOT the guard's is left alone and reported: uninstalling must never remove a hook this
// client did not write. Installing the guard is a change to every linked worktree of a repository (the hooks
// folder is the repository's), so an install that could not be undone would be one nobody should make.
func Uninstall(dir string, out io.Writer) ([]UninstalledHook, error) {
	folder, err := HooksFolder(dir)
	if err != nil {
		return nil, err
	}
	var removed []UninstalledHook
	for _, name := range []string{HookPreCommit, HookPrePush} {
		path := filepath.Join(folder, name)
		body, err := os.ReadFile(path)
		switch {
		case err != nil && !os.IsNotExist(err):
			return removed, fmt.Errorf("cannot read the hook %s: %v", path, err)
		case os.IsNotExist(err):
			fmt.Fprintf(out, "%s is not there: the guard was not installed for it\n", path)
			continue
		case !strings.Contains(string(body), clientservices.GuardMarker):
			fmt.Fprintf(out, "%s is not the guard's hook: left alone\n", path)
			continue
		}
		entry := UninstalledHook{Name: name, Path: path}
		backup := path + BackupSuffix
		// ONE atomic step when the repository's own hook was moved aside: os.Rename replaces the guard's file in
		// place, so there is no window in which a crash loses the hook the guard replaced. A stat error that is
		// not "it is not there" REFUSES, exactly as the same question is asked fifteen lines above: reading an
		// unreadable backup as "there was no hook before the guard" would remove the guard's hook, leave the
		// repository's own behind as a file nothing runs, and report success.
		switch _, err := os.Stat(backup); {
		case err == nil:
			if err := os.Rename(backup, path); err != nil {
				return removed, fmt.Errorf("cannot put the hook the guard replaced back at %s: %v", path, err)
			}
			entry.Restored = path
			fmt.Fprintf(out, "removed %s and put back the hook it replaced\n", path)
		case os.IsNotExist(err):
			if err := os.Remove(path); err != nil {
				return removed, fmt.Errorf("cannot remove the guard's hook %s: %v", path, err)
			}
			fmt.Fprintf(out, "removed %s (there was no hook there before the guard)\n", path)
		default:
			return removed, fmt.Errorf("cannot read %s, the hook the guard replaced, so nothing was removed: %v",
				backup, err)
		}
		removed = append(removed, entry)
	}
	return removed, nil
}

// previousHook answers the hook the guard's own file should run after the guard allows: the hook that was
// just moved aside, a backup an earlier install left, or "" when there is none.
//
// A hook the GUARD wrote is replaced, not backed up (the marker is how it is recognised, and re-installing
// must not fill the folder with copies of our own file). A hook we did NOT write is moved aside, and a
// backup that already exists is never overwritten: two tools claiming one backup name would lose one of them.
func previousHook(path string) (string, error) {
	backup := path + BackupSuffix
	body, err := os.ReadFile(path)
	switch {
	case err != nil && !os.IsNotExist(err):
		return "", fmt.Errorf("cannot read the hook %s: %v", path, err)
	case err == nil && !strings.Contains(string(body), clientservices.GuardMarker):
		if _, err := os.Stat(backup); err == nil {
			return "", fmt.Errorf("the hook %s already has a backup at %s, so installing would overwrite one "+
				"of two hooks: move the backup away first", path, backup)
		}
		if err := os.Rename(path, backup); err != nil {
			return "", fmt.Errorf("cannot move the existing hook %s aside: %v", path, err)
		}
		return backup, nil
	}
	if _, err := os.Stat(backup); err == nil {
		return backup, nil
	}
	return "", nil
}
