// detach is a no-op on Windows, where the client can reach a room only through the room orchestrator inside WSL2 and
// has no local process to keep alive. It exists so the package still builds for windows.
//
//go:build windows

package clientlifecycle

import "os/exec"

func detach(*exec.Cmd) {}
