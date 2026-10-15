package commpolicy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// AuditPath is one seat's audit file: the seat's own directory of the store, so every line the seat's
// decisions produced is read in one place.
func AuditPath(store, rig, seat string) string {
	return filepath.Join(store, rig, seat, "audit.jsonl")
}

// EnsureAuditDir makes the directory the audit file lives in, 0700 because the lines record who may send
// to whom. The client owns the store, so a seat whose directory is missing is created rather than refused.
func EnsureAuditDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0o700)
}

// AppendAudit appends one JSON line and syncs it to disk, so a line written before delivery
// survives a crash. The file must not be readable or writable by others.
//
// MOVED HERE 2026-10-15, body unchanged, from cmd/seatcheck's appendAudit, which was the only writer: the
// client's own send path now decides and audits its deliveries too (the architect's wiring ruling), and two
// writers would have been two definitions of one audit line.
func AppendAudit(path string, record AuditRecord) (err error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s has mode %v, want 0600", path, info.Mode().Perm())
	}
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(line, '\n')); err != nil {
		return err
	}
	return f.Sync()
}
