// detach puts a started child in its own session, so the supervisor and the bridge outlive the verb
// that started them. The Python did the same with start_new_session=True.
//
//go:build !windows

package clientlifecycle

import (
	"os/exec"
	"syscall"
)

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
