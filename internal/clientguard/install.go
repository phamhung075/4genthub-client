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
