//go:build !windows

package seatwait

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSlotFreeReadsTheBuildSlotLocks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slot0")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !slotFree(dir) {
		t.Fatal("an unlocked slot reads busy")
	}
	held, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if slotFree(dir) {
		t.Error("a held slot reads free")
	}
	if slotFree(t.TempDir()) {
		t.Error("a directory with no slot files reads free")
	}
}
