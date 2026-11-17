package deploystate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DirRel is where a Local store keeps its files, relative to the project
// root. The same directory forge's existing deploy-state files live in,
// covered by the existing `.forge/` .gitignore rule.
const DirRel = ".forge/state"

const (
	dirMode  os.FileMode = 0o755
	fileMode os.FileMode = 0o644
)

// Local implements [Store] over a directory. This is the default
// backend, and it is also the acceptance test for the interface: an
// OSS user with no server at all gets drift detection, five states, and
// policy out of it.
//
// # On-disk layout
//
//	.forge/state/reconcile-<env>-<provider>-<service>.json   one Record
//	.forge/state/policy-<env>.json                           one Policy
//
// A NEW filename prefix rather than reusing the existing
// `<provider>-<env>-<service>.json`, and that is deliberate. Those files
// are written and read by forge's non-cluster providers to remember the
// tag they last shipped (the next deploy's ${LAST_TAG}). Widening that
// file's schema in place would mean an older forge reading a newer file,
// and a provider that misparses it loses the only record of what it
// deployed.
//
// So the two coexist, and Local READS the legacy file when it has no
// record of its own (see [Local.Get]). A project that has been deploying
// for months does not report "never deployed" on the day it turns
// reconciliation on.
type Local struct {
	dir string
}

var _ Store = (*Local)(nil)

// NewLocal returns a Local store rooted at a project directory (the one
// holding forge.yaml). The state directory is created lazily on first
// write, so a project that never reconciles never grows the tree.
func NewLocal(projectDir string) *Local {
	return &Local{dir: filepath.Join(projectDir, DirRel)}
}

// safeSegment flattens one path segment to a filename-safe token:
// [A-Za-z0-9_-] passes through, everything else — path separators
// included — becomes '_'. Inputs are KCL-validated identifiers in
// practice, but a filename is composed from them, so separators are
// stripped defensively to keep every write inside the state directory.
// Empty input yields "_" so a segment is never empty.
func safeSegment(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "_"
	}
	return string(out)
}

const recordPrefix = "reconcile-"

func (l *Local) recordPath(k Key) string {
	name := recordPrefix + safeSegment(k.Env) + "-" + safeSegment(k.Provider) + "-" + safeSegment(k.Service) + ".json"
	return filepath.Join(l.dir, name)
}

func (l *Local) policyPath(env string) string {
	return filepath.Join(l.dir, "policy-"+safeSegment(env)+".json")
}

// PolicyPath is the file an engineer edits to change an environment's
// policy by hand. Exported because an error message that says "this
// environment is pinned" without saying WHERE to unpin it sends the
// reader to grep, and the whole value of an instant opt-out is lost if
// finding the switch takes longer than the incident.
func (l *Local) PolicyPath(env string) string { return l.policyPath(env) }

// legacyPath is the pre-existing per-(provider, env, service) deploy
// state file that forge's External / Compose / static-site providers
// already write. Read-only from here.
func (l *Local) legacyPath(k Key) string {
	name := safeSegment(k.Provider) + "-" + safeSegment(k.Env) + "-" + safeSegment(k.Service) + ".json"
	return filepath.Join(l.dir, name)
}

// shareRetryBudget bounds how long a read or a replacing rename waits out
// another handle on the same file (see isTransientShareErr). A state file is
// a few hundred bytes, so the other side holds it for microseconds; the
// budget only has to outlast scheduling noise, not real work.
const shareRetryBudget = 2 * time.Second

// retryShared runs op until it succeeds, fails with anything other than a
// transient sharing error, or shareRetryBudget runs out. Off Windows the
// first error is always final.
func retryShared(op func() error) error {
	deadline := time.Now().Add(shareRetryBudget)
	delay := time.Millisecond
	for {
		err := op()
		if err == nil || !isTransientShareErr(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(delay)
		if delay < 50*time.Millisecond {
			delay *= 2
		}
	}
}

// writeJSON persists v via a temp file and a rename.
//
// The rename is what makes a concurrent READER safe: a reader either sees
// the whole old file or the whole new one, never a half-written one. On
// POSIX that holds outright; on Windows the replace can also briefly refuse
// the reader's open, or be refused by it, and both sides retry that
// (retryShared) rather than report it. It does NOT make concurrent WRITERS
// safe — last writer still wins — which is the gap [Store] documents
// rather than hides.
func writeJSON(path, label string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return fmt.Errorf("create %s dir: %w", label, err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", label, err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp %s: %w", label, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", label, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", label, err)
	}
	if err := os.Chmod(tmpName, fileMode); err != nil {
		return fmt.Errorf("chmod %s: %w", label, err)
	}
	if err := retryShared(func() error { return os.Rename(tmpName, path) }); err != nil {
		return fmt.Errorf("rename %s %s: %w", label, path, err)
	}
	return nil
}

// readJSON decodes a state file. A missing file yields (false, nil).
func readJSON[T any](path, label string, out *T) (bool, error) {
	var data []byte
	err := retryShared(func() (err error) {
		data, err = os.ReadFile(path)
		return err
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read %s %s: %w", label, path, err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false, fmt.Errorf("parse %s %s: %w", label, path, err)
	}
	return true, nil
}

// legacyDeployState is the shape of the pre-existing deploy-state file.
// Declared here, in pkg/, as a deliberate copy of forge's internal
// deploytarget.DeployState: pkg/ is the library that ships into user
// binaries and does not reach into forge's internal tree, and the
// alternative — hoisting a three-field struct into a third shared
// package — costs more than the duplicate.
// The three json tags are the contract; they must not change.
type legacyDeployState struct {
	Image      string `json:"image"`
	Tag        string `json:"tag"`
	DeployedAt string `json:"deployed_at"`
}

// Get returns the record for one key.
//
// The legacy fallback is the reason this is not four lines. When no
// reconcile record exists, Local reads the older deploy-state file and
// presents it as a DESIRED-ONLY record: forge did ship this, here is
// what it shipped, and the observed half is explicitly unmeasured. The
// resulting record evaluates to [StateUnknown], which is the honest
// answer — nobody has looked yet — rather than the green a populated
// desired half might otherwise suggest.
func (l *Local) Get(ctx context.Context, key Key) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if !key.Valid() {
		return Record{}, fmt.Errorf("deploystate: incomplete key %q", key)
	}

	var rec Record
	found, err := readJSON(l.recordPath(key), "reconcile record", &rec)
	if err != nil {
		return Record{}, err
	}
	if found {
		rec.Key = key
		return rec, nil
	}

	var legacy legacyDeployState
	found, err = readJSON(l.legacyPath(key), "deploy state", &legacy)
	if err != nil {
		return Record{}, err
	}
	if !found {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}

	declared, _ := time.Parse(time.RFC3339, legacy.DeployedAt)
	return Record{
		Key: key,
		Desired: Desired{
			Image:      legacy.Image,
			Tag:        legacy.Tag,
			DeclaredAt: declared.UTC(),
		},
		Observed: Observation{
			Measured: false,
			Detail:   "recovered from a pre-reconcile deploy-state file; never observed",
		},
	}, nil
}

// Put writes a record. The key on the record is authoritative.
func (l *Local) Put(ctx context.Context, rec Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !rec.Key.Valid() {
		return fmt.Errorf("deploystate: incomplete key %q", rec.Key)
	}
	return writeJSON(l.recordPath(rec.Key), "reconcile record", rec)
}

// List returns every reconcile record for one environment.
//
// It reads only the reconcile-prefixed files — NOT the legacy ones. A
// list is a report of what reconciliation knows, and synthesizing
// never-observed entries from deploy history would fill it with rows
// whose every column is unknown. Get's fallback exists to answer a
// question about a specific target; List answering the same way would
// be noise.
func (l *Local) List(ctx context.Context, env string) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if env == "" {
		return nil, errors.New("deploystate: List requires an environment")
	}

	entries, err := os.ReadDir(l.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read state dir %s: %w", l.dir, err)
	}

	prefix := recordPrefix + safeSegment(env) + "-"
	var out []Record
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		var rec Record
		found, err := readJSON(filepath.Join(l.dir, name), "reconcile record", &rec)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key.Provider != out[j].Key.Provider {
			return out[i].Key.Provider < out[j].Key.Provider
		}
		return out[i].Key.Service < out[j].Key.Service
	})
	return out, nil
}

// Policy reads the per-environment policy from disk on EVERY call — no
// cache, no memo. That is the feature: an engineer who writes
// `{"policy":"pinned"}` into .forge/state/policy-prod.json mid-incident
// has changed forge's behaviour for the very next pass, with no deploy,
// no restart, and no artifact rebuild.
//
// A missing file is not an error. It returns PolicyObserve, which is
// also the zero value, so the absence of an opinion means "change
// nothing" through every path at once.
func (l *Local) Policy(ctx context.Context, env string) (Policy, error) {
	if err := ctx.Err(); err != nil {
		return PolicyObserve, err
	}
	if env == "" {
		return PolicyObserve, errors.New("deploystate: Policy requires an environment")
	}
	var pf policyFile
	found, err := readJSON(l.policyPath(env), "reconcile policy", &pf)
	if err != nil {
		return PolicyObserve, err
	}
	if !found {
		return PolicyObserve, nil
	}
	return pf.Policy, nil
}

// SetPolicy records the policy for one environment.
func (l *Local) SetPolicy(ctx context.Context, env string, p Policy) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if env == "" {
		return errors.New("deploystate: SetPolicy requires an environment")
	}
	return writeJSON(l.policyPath(env), "reconcile policy", policyFile{Policy: p})
}
