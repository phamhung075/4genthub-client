//go:build !windows

package clientenv

import "syscall"

// RestrictUmask takes the group's and the world's bits out of everything this process creates, once, before
// anything creates anything.
//
// WHY THE PROCESS AND NOT EACH WRITE: the state root holds a seat's own words - the seat's session transcript
// is written by the agent, not by this client - and the client's half is written from a dozen call sites whose
// modes were chosen site by site (0755 for a directory here, 0644 for a heartbeat there). The umask is
// inherited by every process the client starts, so one call clamps all of them, including the modes a site
// asked for that no longer make sense: `os.MkdirAll(dir, 0o755)` under `0077` is `0700`.
//
// Measured 2026-10-16: the daemon and every seat ran with `0022`, so the heartbeat files, the safeguard logs
// and 138 of 163 seat transcripts under the state root were `0644` over `0755` directories, and 49 of those
// files held a credential-shaped value. The runtime sets the same bits for the files it and the agents write
// (`agenthub_runtime::restrict_umask`); the two calls are the whole tree, because every other writer is a
// child of one of these processes.
func RestrictUmask() {
	syscall.Umask(0o077)
}
