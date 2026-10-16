// RestrictUmask is a no-op on Windows, which has no umask: there, who may read a file is decided by ACLs, not
// by mode bits, so this call cannot state the same guarantee. The measurement behind the Unix half
// (2026-10-16) was taken on this box, and Windows ACLs for the state root are NOT claimed by it. It exists so
// the package still builds for windows.
//
//go:build windows

package clientenv

func RestrictUmask() {}
