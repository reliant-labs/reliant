// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// The result outbox on disk.
//
// resultOutbox (gateway_session.go) keeps every tool result until a gateway
// acknowledges it, so a result survives a dropped stream. Held only in memory,
// it did not survive the daemon: a restart — planned or not — turned every
// finished-but-unacknowledged result, and every call still running, into "the
// machine no longer knows the request". This file is the part that outlives
// the process.
//
//   - A finished call's result is written to <dir>/<request id>.result before
//     it is sent anywhere: write to a temp file, fsync, rename, fsync the
//     directory. The file is removed when a gateway acknowledges the result,
//     when it ages past resultHoldTTL, and when the outbox is over its byte
//     budget (oldest first) — the same three exits the in-memory entry has.
//   - A call that has started and not finished leaves <dir>/<request id>.running.
//     It carries no content; its name is the whole record. A call cannot be
//     resumed in another process, so when the daemon finds one at startup it
//     turns it into a result of its own: ErrorCodeDaemonRestarted, "interrupted
//     by daemon restart". The worker's tool.status then says "completed", the
//     result is sent like any other, and the model is told to verify the
//     call's effects — instead of a vaguer "unknown request" later.
//   - Results can hold file contents and command output. The directory is
//     0700, every file 0600, and nothing here logs more than a request id and
//     a size.
//   - A file that cannot be read back (truncated by a full disk, failed its
//     checksum, names a different request) is removed and counted in the load
//     report. It never stops the daemon from starting.
//
// There is no persistence when the daemon has no data directory (an embedded
// runtime): the outbox then behaves exactly as it did, in memory.

const (
	resultFileExt  = ".result"
	runningFileExt = ".running"
	tempInfix      = ".tmp-"

	// outboxDirName is the outbox's directory inside the daemon's data
	// directory. It is per instance: two daemons on one machine have two
	// data directories, and one must never answer for the other's requests.
	outboxDirName = "outbox"

	// outboxMagic opens every result file; the trailing digit is the format
	// version.
	outboxMagic = "RLNTOBX1"
	// outboxHeaderLen is magic + held-at (int64) + payload length (uint64) +
	// sha256 of the payload.
	outboxHeaderLen = len(outboxMagic) + 8 + 8 + sha256.Size
)

// requestIDFile is what a request id must look like to become a file name.
// The id arrives from a gateway: a '/', '..' or NUL in it must never address
// another path.
var requestIDFile = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// outboxStore is the on-disk half of a resultOutbox. A nil *outboxStore does
// nothing, so an outbox with no directory is the in-memory outbox it was.
type outboxStore struct {
	dir string
}

// openOutboxStore prepares dir for use: created 0700, and tightened to 0700
// if an earlier version of something left it wider.
func openOutboxStore(dir string) (*outboxStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating tool result outbox %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("securing tool result outbox %s: %w", dir, err)
	}
	return &outboxStore{dir: dir}, nil
}

func (s *outboxStore) path(requestID, ext string) string {
	return filepath.Join(s.dir, requestID+ext)
}

// storable reports whether requestID can be a file name.
func (s *outboxStore) storable(requestID string) bool {
	return s != nil && requestIDFile.MatchString(requestID)
}

// markRunning records that a call began executing. Best effort and not
// synced: a marker lost to a power failure only degrades the restart answer
// from "interrupted by daemon restart" to "unknown request".
func (s *outboxStore) markRunning(requestID string) {
	if !s.storable(requestID) {
		return
	}
	f, err := os.OpenFile(s.path(requestID, runningFileExt), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		logging.Warn(logPrefix+" Could not record a running tool call; a restart will not know it was interrupted",
			"request_id", requestID, "error", err)
		return
	}
	_ = f.Close()
}

// clearRunning forgets a running marker.
func (s *outboxStore) clearRunning(requestID string) {
	if s.storable(requestID) {
		_ = os.Remove(s.path(requestID, runningFileExt))
	}
}

// putResult durably records a finished call's result. A failure is logged and
// returned; the in-memory outbox still holds the result.
func (s *outboxStore) putResult(requestID string, msg *reliantv1.DaemonMessage, heldAt time.Time) error {
	if !s.storable(requestID) {
		return nil
	}
	data, err := encodeResultFile(msg, heldAt)
	if err != nil {
		logging.Warn(logPrefix+" Could not encode a tool result for the outbox", "request_id", requestID, "error", err)
		return err
	}
	if err := writeFileDurably(s.dir, requestID+resultFileExt, data); err != nil {
		logging.Warn(logPrefix+" Could not persist a tool result; it will not survive a daemon restart",
			"request_id", requestID, "error", err)
		return err
	}
	return nil
}

// removeResult deletes a result file, once it is acknowledged or expired.
func (s *outboxStore) removeResult(requestID string) {
	if s.storable(requestID) {
		_ = os.Remove(s.path(requestID, resultFileExt))
	}
}

// outboxLoadReport says what reloading the outbox found.
type outboxLoadReport struct {
	// Restored is the number of unacknowledged results put back in the outbox.
	Restored int
	// Interrupted is the number of calls that were running when the previous
	// process died; each now holds a DAEMON_RESTARTED result.
	Interrupted int
	// Corrupt names the files that could not be read back (never their
	// contents). They were removed.
	Corrupt []string
	// Expired counts results and markers older than the hold window.
	Expired int
	// OverBudget counts results dropped, oldest first, to fit the byte budget.
	OverBudget int
}

// load reads the directory back at startup. It returns every result the
// outbox should hold, including one standing in for each call that was
// running, and a report. It removes what it cannot use.
func (s *outboxStore) load(now time.Time, ttl time.Duration, budget int) ([]*heldResult, outboxLoadReport) {
	var report outboxLoadReport
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		logging.Warn(logPrefix+" Could not read the tool result outbox; starting with it empty", "error", err)
		return nil, report
	}

	results := make(map[string]*heldResult)
	var running []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		full := filepath.Join(s.dir, name)
		switch {
		case strings.Contains(name, tempInfix):
			// A write that never reached its rename.
			_ = os.Remove(full)
		case strings.HasSuffix(name, resultFileExt):
			id := strings.TrimSuffix(name, resultFileExt)
			if !requestIDFile.MatchString(id) {
				continue
			}
			h, err := readResultFile(full, id)
			if err != nil {
				report.Corrupt = append(report.Corrupt, name)
				_ = os.Remove(full)
				continue
			}
			if now.Sub(h.heldAt) > ttl {
				report.Expired++
				_ = os.Remove(full)
				continue
			}
			results[id] = h
		case strings.HasSuffix(name, runningFileExt):
			id := strings.TrimSuffix(name, runningFileExt)
			if !requestIDFile.MatchString(id) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if now.Sub(info.ModTime()) > ttl {
				report.Expired++
				_ = os.Remove(full)
				continue
			}
			running = append(running, id)
		}
	}

	// A call that finished and was then interrupted before its marker went
	// is finished: the result wins.
	for _, id := range running {
		if _, done := results[id]; done {
			s.clearRunning(id)
			continue
		}
		msg := interruptedByRestartResponse(id)
		h := &heldResult{requestID: id, msg: msg, heldAt: now, size: messageSize(msg)}
		if err := s.putResult(id, msg, now); err != nil {
			// Not persisted, still answered: this process holds it.
			logging.Warn(logPrefix+" Interrupted tool call answered from memory only", "request_id", id)
		}
		s.clearRunning(id)
		results[id] = h
		report.Interrupted++
	}

	// Oldest first out, until the rest fits.
	ordered := make([]*heldResult, 0, len(results))
	total := 0
	for _, h := range results {
		ordered = append(ordered, h)
		total += h.size
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].heldAt.Before(ordered[j].heldAt) })
	for total > budget && len(ordered) > 0 {
		oldest := ordered[0]
		ordered = ordered[1:]
		total -= oldest.size
		s.removeResult(oldest.requestID)
		report.OverBudget++
	}
	report.Restored = len(ordered) - report.Interrupted
	if report.Restored < 0 {
		report.Restored = 0
	}

	switch {
	case len(report.Corrupt) > 0:
		logging.Warn(logPrefix+" Skipped unreadable files in the tool result outbox",
			"count", len(report.Corrupt), "files", strings.Join(report.Corrupt, ","))
	}
	if report.Restored+report.Interrupted+report.Expired+report.OverBudget > 0 {
		logging.Info(logPrefix+" Reloaded the tool result outbox",
			"restored", report.Restored, "interrupted_by_restart", report.Interrupted,
			"expired", report.Expired, "over_budget", report.OverBudget)
	}
	return ordered, report
}

// interruptedByRestartResponse is the answer for a call the previous daemon
// process was running when it died.
func interruptedByRestartResponse(requestID string) *reliantv1.DaemonMessage {
	msg := fmt.Sprintf("Tool request %q was interrupted by a daemon restart: the machine's daemon stopped while "+
		"it was running, and a running call cannot be resumed, so its outcome is unknown. "+
		"Verify its effects before running it again.", requestID)
	return &reliantv1.DaemonMessage{
		Message: &reliantv1.DaemonMessage_ToolResponse{ToolResponse: &reliantv1.ToolResponse{
			RequestId:    requestID,
			Success:      false,
			IsError:      true,
			Content:      msg,
			ErrorMessage: msg,
			ErrorCode:    toolexec.ErrorCodeDaemonRestarted,
		}},
	}
}

func encodeResultFile(msg *reliantv1.DaemonMessage, heldAt time.Time) ([]byte, error) {
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	buf := make([]byte, 0, outboxHeaderLen+len(payload))
	buf = append(buf, outboxMagic...)
	buf = binary.BigEndian.AppendUint64(buf, uint64(heldAt.UnixNano()))
	buf = binary.BigEndian.AppendUint64(buf, uint64(len(payload)))
	buf = append(buf, sum[:]...)
	return append(buf, payload...), nil
}

var errCorruptResultFile = errors.New("unreadable tool result file")

// readResultFile decodes one result file, checking that it is whole and is
// the result of requestID.
func readResultFile(path, requestID string) (*heldResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < outboxHeaderLen || !bytes.HasPrefix(data, []byte(outboxMagic)) {
		return nil, errCorruptResultFile
	}
	rest := data[len(outboxMagic):]
	heldAt := time.Unix(0, int64(binary.BigEndian.Uint64(rest[:8])))
	length := binary.BigEndian.Uint64(rest[8:16])
	var want [sha256.Size]byte
	copy(want[:], rest[16:16+sha256.Size])
	payload := rest[16+sha256.Size:]
	if uint64(len(payload)) != length || sha256.Sum256(payload) != want {
		return nil, errCorruptResultFile
	}
	msg := &reliantv1.DaemonMessage{}
	if err := proto.Unmarshal(payload, msg); err != nil {
		return nil, errCorruptResultFile
	}
	if msg.GetToolResponse().GetRequestId() != requestID {
		return nil, errCorruptResultFile
	}
	return &heldResult{requestID: requestID, msg: msg, heldAt: heldAt, size: messageSize(msg)}, nil
}

// writeFileDurably writes data to dir/name so a reader sees all of it or
// none: a 0600 temp file in the same directory, synced, renamed over name,
// and the directory synced so the rename itself survives a power cut.
func writeFileDurably(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, name+tempInfix+"*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir flushes a directory entry change. Best effort: not every platform
// can open a directory to sync it.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, fs.ErrInvalid) {
		return
	}
}
