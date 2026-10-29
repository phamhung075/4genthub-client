//go:build !windows

package seatwait

import (
	"os"
	"path/filepath"
	"syscall"
)

// slotFree answers whether one `slot*` file in dir can be locked now. The lock is taken and released at once:
// the build-slot script (`flock -n` on each file) holds a slot for the whole build, so a lock that succeeds is
// a slot nobody holds.
func slotFree(dir string) bool {
	files, _ := filepath.Glob(filepath.Join(dir, "slot*"))
	for _, path := range files {
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			continue
		}
		fd := int(file.Fd())
		if syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			_ = syscall.Flock(fd, syscall.LOCK_UN)
			_ = file.Close()
			return true
		}
		_ = file.Close()
	}
	return false
}
