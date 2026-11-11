// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// Run-step tombstones on disk (result_once.go).
//
// A tombstone is <request id>.seen, and its expiry is in its contents:
// seenMagic followed by the expiry in Unix nanoseconds, big-endian. It is
// written to a temp file, synced, renamed into place, and the directory is
// synced, all before the command runs; any of those failing fails the call
// closed. The expiry rides in the bytes the file sync covers, not in metadata
// set afterwards, so a crash can lose the whole tombstone only before the
// command could have started, and never its expiry alone.
//
// Tombstones written before the expiry moved into the contents are empty
// files whose modification time was set to the expiry after they were synced,
// a change a crash could lose. They are still honoured: the expiry is taken as
// the modification time plus runStepSeenFloor. If the time was set, that keeps
// the tombstone a floor longer than it needed; if a crash reset it to the
// file's creation, that is still the floor every tombstone lasts. A file whose
// contents cannot be read as either is honoured the same way: a tombstone is
// only ever kept too long, never forgotten too soon.

// seenMagic opens a tombstone's contents; the trailing digit is the format.
const seenMagic = "RLNTSEN1"

// seenFileLen is a tombstone's size: the magic and the expiry.
const seenFileLen = len(seenMagic) + 8

func encodeSeen(expires time.Time) []byte {
	return binary.BigEndian.AppendUint64([]byte(seenMagic), uint64(expires.UnixNano()))
}

// decodeSeen is a tombstone's expiry from its contents and modification time:
// the expiry it carries, or for a legacy (empty) or unreadable one,
// modTime + runStepSeenFloor. ok is false for the latter, so a caller can say
// which it was.
func decodeSeen(data []byte, modTime time.Time) (expires time.Time, ok bool) {
	if len(data) == seenFileLen && bytes.HasPrefix(data, []byte(seenMagic)) {
		return time.Unix(0, int64(binary.BigEndian.Uint64(data[len(seenMagic):]))), true
	}
	return modTime.Add(runStepSeenFloor), false
}

// markSeen durably records that a run step's request id began here, until
// expires (above). The error says the tombstone is not durable, and the caller
// does not run the call (markRunStep). No store, or an id that cannot be a
// file name, is nil: there is nothing that could be made durable.
func (s *outboxStore) markSeen(requestID string, expires time.Time) error {
	if !s.storable(requestID) {
		return nil
	}
	dirSync := s.dirSync
	if dirSync == nil {
		dirSync = syncDirStrict
	}
	if err := writeFileThenSyncDir(s.dir, requestID+seenFileExt, encodeSeen(expires), dirSync); err != nil {
		return fmt.Errorf("persist run step tombstone: %w", err)
	}
	return nil
}

// removeSeen deletes a run step's tombstone, once it expires.
func (s *outboxStore) removeSeen(requestID string) {
	if s.storable(requestID) {
		_ = os.Remove(s.path(requestID, seenFileExt))
	}
}

// loadSeen reads back the tombstones still in force at now, each with its
// expiry, and removes the expired ones, counting them.
func (s *outboxStore) loadSeen(now time.Time) (seen map[string]time.Time, expired int) {
	seen = make(map[string]time.Time)
	if s == nil {
		return seen, 0
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return seen, 0
	}
	legacy := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.Contains(name, tempInfix) || !strings.HasSuffix(name, seenFileExt) {
			continue
		}
		id := strings.TrimSuffix(name, seenFileExt)
		if !requestIDFile.MatchString(id) {
			continue
		}
		full := filepath.Join(s.dir, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			data = nil // unreadable: honoured by its modification time
		}
		expires, current := decodeSeen(data, info.ModTime())
		if !current {
			legacy++
		}
		if !now.Before(expires) {
			expired++
			_ = os.Remove(full)
			continue
		}
		seen[id] = expires
	}
	if legacy > 0 {
		logging.Info(logPrefix+" Reloaded run-step tombstones without a recorded expiry; each kept a floor past its modification time",
			"count", legacy, "floor", runStepSeenFloor)
	}
	return seen, expired
}

// writeFileThenSyncDir is writeFileDurably whose directory sync is dirSync,
// and whose failure is the write's.
func writeFileThenSyncDir(dir, name string, data []byte, dirSync func(dir string) error) error {
	if err := writeFileSynced(dir, name, data); err != nil {
		return err
	}
	if err := dirSync(dir); err != nil {
		return fmt.Errorf("sync directory %s: %w", dir, err)
	}
	return nil
}

// syncDirStrict flushes a directory entry change and says whether it could.
// Unlike syncDir, a failure is an error. The one exception is a filesystem
// that has no directory sync to offer — Windows, where a directory cannot be
// opened for it, or one answering EINVAL or "unsupported" — where the file's
// own sync and the rename are all the durability there is.
func syncDirStrict(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		if errors.Is(err, syscall.EINVAL) || errors.Is(err, errors.ErrUnsupported) {
			return nil
		}
		return err
	}
	return nil
}
