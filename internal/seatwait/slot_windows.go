//go:build windows

package seatwait

// slotFree cannot read a flock on Windows, so a slot wait there ends on a message or its expiry.
func slotFree(string) bool { return false }
