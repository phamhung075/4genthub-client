// MainRoot's three sources, in the order a measured fault fixed: `--main`, then the checkout variable,
// then the working directory.
//
// THE ORDER IS THE TEST'S POINT. A smoke run of `4genteam worktree list 4genthub-min` from this seat's
// own client worktree (which is a worktree of the CLIENT repository) reported every derived path as
// existing-but-not-a-worktree, because `git rev-parse --show-toplevel` there names the client repository
// and the seats' worktrees belong to the main checkout.
package clientworktree

import (
	"path/filepath"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

func TestMainRootIsTheFlagThenTheCheckoutThenTheWorkingDirectory(t *testing.T) {
	if root, err := MainRoot("/somewhere/else"); err != nil || root != "/somewhere/else" {
		t.Errorf("MainRoot(flag) = %q, %v; want the flag's value", root, err)
	}

	t.Setenv(clientservices.SkillsRootEnv, "/the/checkout")
	root, err := MainRoot("")
	if err != nil {
		t.Fatalf("MainRoot with %s set: %v", clientservices.SkillsRootEnv, err)
	}
	if want := "/the/checkout"; root != want {
		t.Errorf("MainRoot = %q, want %q", root, want)
	}
}

// With neither source, the working directory's repository answers: this test runs inside the client
// repository, so its toplevel is the repository this checkout belongs to.
func TestMainRootReadsTheWorkingDirectorysRepositoryLast(t *testing.T) {
	t.Setenv(clientservices.SkillsRootEnv, "")
	root, err := MainRoot("")
	if err != nil {
		t.Fatalf("MainRoot: %v", err)
	}
	toplevel, err := runGit(root, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatalf("giving the answer back to git: %v", err)
	}
	if toplevel == "" {
		t.Fatalf("MainRoot = %q, which is no repository", root)
	}
	if cleaned := filepath.Clean(toplevel); root != cleaned {
		t.Errorf("MainRoot = %q, want the working directory's toplevel %q", root, cleaned)
	}
}
