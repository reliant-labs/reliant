package ledgerfile

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	dirMode  = 0o700 // the ledger is one user's; it holds no secrets, but it IS authority
	fileMode = 0o600
)

// DefaultHomeEnv names the environment variable that relocates the ledger.
const DefaultHomeEnv = "FORGE_LEDGER_HOME"

// Store is one project's machine ledger. Returned as a concrete type —
// accept interfaces, return structs — so a caller that needs only promotions
// takes only the methods it uses, and the consumer declares the narrow
// interface (internal/cli/binding_store.go does exactly that).
//
// A Store holds no open file and no cached history: every operation opens,
// locks, reads, decides, writes and closes. That costs a few syscalls and
// buys the thing a cache cannot have — a decision made against the history
// that is on disk right now, under a lock, rather than one read earlier.
type Store struct {
	dir string
}

// Open prepares the ledger directory for a project and returns a Store
// bound to it.
//
// home is passed in rather than read from the environment HERE so that tests
// are hermetic without t.Setenv (which cannot be used in a parallel test):
// the caller resolves the home once with Home and hands it over. That is also
// why Open takes the already-derived project id.
func Open(home, projectID string) (*Store, error) {
	if strings.TrimSpace(home) == "" {
		return nil, fmt.Errorf("ledger home is empty")
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("ledger project id is empty")
	}
	dir := filepath.Join(home, projectID)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("create ledger %s: %w", dir, err)
	}
	// Checked AFTER the mkdir, because statfs needs the directory to
	// exist, and checked on the project dir rather than the home so a
	// symlinked or separately-mounted project directory is judged on its
	// own filesystem.
	if err := checkLockableFS(dir); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Home resolves the ledger home: $FORGE_LEDGER_HOME, else ~/.forge/ledger.
//
// The environment variable is read in exactly one place, and it is not read
// by Open, so every other entry point into this package can be driven from a
// temp dir in a test.
func Home() (string, error) {
	if h := strings.TrimSpace(os.Getenv(DefaultHomeEnv)); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate the ledger home: %w (set %s to choose one)", err, DefaultHomeEnv)
	}
	return filepath.Join(home, ".forge", "ledger"), nil
}

// Dir is the ledger's directory, for human-facing output. Opaque: PRINT it.
func (s *Store) Dir() string { return s.dir }

// ─── The lock ────────────────────────────────────────────────────────────────

// withLock runs fn holding the ledger's exclusive lock.
//
// EVERY read-decide-append goes through here, and so do the plain reads. A
// read under the lock cannot observe a half-written file, which is what makes
// "the newest line is current" true rather than usually-true.
//
// The lock is one file for the whole project rather than one per env. A
// per-env lock would allow two envs to promote concurrently, but promotions
// are rare, human-initiated and sub-millisecond to write, so the contention
// saved is zero and the invariant bought is large: a reader that needs two
// files (an apply joined to its promotion) sees one consistent instant.
func (s *Store) withLock(fn func() error) error {
	path := filepath.Join(s.dir, "lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, fileMode) //nolint:gosec // path is the store's own directory
	if err != nil {
		return fmt.Errorf("open ledger lock %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	unlock, err := lockExclusive(f)
	if err != nil {
		return fmt.Errorf("lock ledger %s: %w", path, err)
	}
	defer unlock()
	return fn()
}

// ─── jsonl primitives ────────────────────────────────────────────────────────

// path resolves a file inside the ledger. Each segment is flattened, so an
// env name can never escape the ledger directory.
func (s *Store) path(parts ...string) string {
	safe := make([]string, 0, len(parts))
	for _, p := range parts {
		safe = append(safe, safeSegment(p))
	}
	return filepath.Join(append([]string{s.dir}, safe...)...)
}

// envPath is the per-env file in a subdirectory (promotions, gates, applies).
func (s *Store) envPath(kind, env string) string {
	return s.path(kind, safeSegment(env)+".jsonl")
}

// readLines returns a file's non-blank lines. A missing file is an empty
// list: "nothing recorded yet" is a normal state, not an error.
func readLines(path string) ([][]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // every path comes from Store.path, which flattens each segment
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var out [][]byte
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		if line := bytes.TrimSpace(sc.Bytes()); len(line) > 0 {
			out = append(out, append([]byte(nil), line...))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}

// decodeAll reads a jsonl file into []T, validating every line.
//
// A line that does not decode or does not validate is an ERROR, never a
// skipped line. A ledger whose newest line nobody can read is not a ledger
// whose newest line can be trusted as "current", and silently dropping it
// would answer "never promoted" for an env that was promoted — the failure
// mode this whole design exists to prevent.
func decodeAll[T any](path string, validate func(T) error) ([]T, error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(lines))
	for i, raw := range lines {
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, i+1, err)
		}
		if validate != nil {
			if err := validate(v); err != nil {
				return nil, fmt.Errorf("%s line %d: %w", path, i+1, err)
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// appendRecord writes v as ONE line in ONE write under O_APPEND.
//
// The single write matters even with the lock held: a reader takes the lock
// too, but an external reader (a human with `tail`, a future tool) never
// sees a torn line.
//
// It encodes with the SAME encoder every other backend uses — plain
// encoding/json over the pkg/release types, no indentation, no HTML
// escaping difference — so a line here is byte-identical to what the hosted
// ledger's ImportLedger accepts. That property is pinned by a test.
func appendRecord(path string, v any) error {
	line, err := canonicalJSON(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, fileMode) //nolint:gosec // path comes from Store.path
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("append to %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

// canonicalJSON encodes one record as the single line that represents it.
//
// SetEscapeHTML(false) is the one deviation from json.Marshal's default, and
// it is what makes the encoding canonical rather than Go-specific: Marshal
// escapes <, > and & to \u003c-style sequences, which protojson does not, so
// a promotion note containing "&" would serialize differently here than on
// the wire and the "a file line IS what ImportLedger accepts" property would
// hold only for records that happen to avoid three bytes.
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encode ledger record: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// rewriteLines replaces a file's contents atomically (write a temp file in
// the same directory, then rename). Used by sessions.jsonl (compacted) and
// the hosted-ledger mirror (hostedcache.go, a cache); every ledger file is append-only.
func rewriteLines(path string, records []any) error {
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	var buf bytes.Buffer
	for _, r := range records {
		line, err := canonicalJSON(r)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp beside %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(fileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	return os.Rename(tmpName, path)
}
