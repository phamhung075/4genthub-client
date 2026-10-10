package clientseat

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// deliverSkills makes an omp seat's own skill directory exactly the skills `sync rig` built for it.
//
// An omp seat reads skills from <stateRoot>/<seat session>/agent/skills (its PI_CODING_AGENT_DIR), and
// nothing else copies `<store>/<rig>/rig/agents/<seat>/skills` there: measured 2026-10-11, a restarted
// fe-dev answered "No such skill" for a skill present in the store. The directory is replaced, not merged,
// so a skill the client tree no longer carries disappears from the seat too.
//
// A seat with no agent directory under the state root is not an omp seat and is left alone.
func deliverSkills(store, stateRoot, rig, seat string) error {
	agentDir := filepath.Join(stateRoot, rig+"-"+seat+"@"+rig, "agent")
	if info, err := os.Stat(agentDir); err != nil || !info.IsDir() {
		return nil
	}
	source := filepath.Join(store, rig, "rig", "agents", seat, "skills")
	if info, err := os.Stat(source); err != nil || !info.IsDir() {
		return &clientcmd.CodedError{
			Message: fmt.Sprintf("seat %s has no skills to deliver: %s is missing; run `4genteam sync rig %s` first", seat, source, rig),
			Code:    ExitFailed,
		}
	}
	target := filepath.Join(agentDir, "skills")
	if err := os.RemoveAll(target); err != nil {
		return &clientcmd.CodedError{Message: fmt.Sprintf("clearing %s: %v", target, err), Code: ExitFailed}
	}
	if err := os.CopyFS(target, os.DirFS(source)); err != nil {
		return &clientcmd.CodedError{Message: fmt.Sprintf("copying skills into %s: %v", target, err), Code: ExitFailed}
	}
	return nil
}

// deliverSkillsDefault resolves the real store and state root.
func deliverSkillsDefault(rig, seat string) error {
	store, err := clientenv.ResolveSeatStore("")
	if err != nil {
		return err
	}
	stateRoot, err := clientenv.ResolveStateRoot("")
	if err != nil {
		return err
	}
	return deliverSkills(store, stateRoot, rig, seat)
}
