// Copyright (c) 2025 Reliant Labs

package daemonstate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/daemoninstance"
)

// Instance is one enumerated instance directory: which instance it is, whether
// a daemon is alive in it, and what its runtime record says.
//
// Locked and Record answer different questions and neither substitutes for the
// other. Locked is the kernel's answer to "is a process alive in this
// directory" and cannot go stale. Record is a file the daemon writes, so it can
// outlive its writer or be absent while a daemon runs. Reporting both is what
// lets a human see the disagreement — a held lock with no record is a daemon
// that died before publishing; a record with no lock is a corpse.
type Instance struct {
	// Slug is the "<origin>/<sub>/<workspace>" instance key.
	Slug string
	// DataDir is the absolute directory the instance owns.
	DataDir string
	// Locked reports whether a live process held daemon.lock at the instant it
	// was probed. Not a durable claim: a daemon may start immediately after.
	Locked bool
	// Record is the runtime record, valid only when HasRecord is true.
	Record    State
	HasRecord bool
	// RecordErr is set when a record exists but could not be read or parsed.
	// A corrupt record is reported, never silently treated as absent.
	RecordErr error
}

// instancesRoot resolves ~/.reliant/instances. It is a variable so tests can
// enumerate a temp directory instead of the developer's real one; nothing in
// production replaces it.
var instancesRoot = daemoninstance.InstancesDir

// List enumerates every instance directory under root — exactly three levels
// deep, matching daemoninstance's projection — and reports each one's liveness
// and runtime record.
//
// A root that does not exist yields no instances and no error: nobody has run a
// daemon on this machine yet, which is a legitimate state rather than a fault.
func List(root string) ([]Instance, error) {
	dirs, err := instanceDirs(root)
	if err != nil {
		return nil, err
	}
	instances := make([]Instance, 0, len(dirs))
	for _, dir := range dirs {
		instances = append(instances, inspect(root, dir))
	}
	return instances, nil
}

// ListDefault enumerates the real ~/.reliant/instances.
func ListDefault() ([]Instance, error) {
	root, err := instancesRoot()
	if err != nil {
		return nil, err
	}
	return List(root)
}

// DefaultRoot reports the instances root List would enumerate by default.
func DefaultRoot() (string, error) { return instancesRoot() }

// inspect probes one instance directory. Ordering matters: the lock probe runs
// before the record read so a daemon that is starting cannot be reported as
// "locked but with a record from its predecessor".
func inspect(root, dir string) Instance {
	instance := Instance{
		Slug:    slugRelativeTo(root, dir),
		DataDir: dir,
	}
	instance.Locked, _ = ProbeLocked(dir)

	state, err := Read(dir)
	switch {
	case err == nil:
		instance.Record = state
		instance.HasRecord = true
	case os.IsNotExist(err):
		// No record. Locked tells the real story either way.
	default:
		instance.RecordErr = err
	}
	return instance
}

// ProbeLocked reports whether a live daemon holds dataDir's lock right now.
//
// It takes the same non-blocking exclusive flock a starting daemon would and
// releases it immediately: refused means a daemon holds it, granted means none
// did at that instant. The kernel drops an advisory lock when its holder dies,
// so this is authoritative in a way that scanning the process table is not — it
// does not care what the binary is called or where it lives, which matters
// because a `go run` daemon's executable is an unrecognizable temp path.
//
// The window the probe itself holds the lock is as short as the syscalls allow,
// and a granted probe is NOT a claim on the directory: by the time the caller
// reads the result a daemon may already be starting. Callers must treat false
// as "none at this instant", never as "safe to assume idle".
//
// It never creates, truncates, or unlinks anything. An absent lock file means
// no daemon has ever claimed this directory, which is simply not-locked —
// creating one to find that out would leave litter behind a read-only command.
func ProbeLocked(dataDir string) (bool, error) {
	if dataDir == "" {
		return false, nil
	}
	path := filepath.Join(dataDir, LockFileName)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("opening daemon lock %s: %w", path, err)
	}
	defer file.Close()

	acquired, err := tryLockExclusive(file)
	if err != nil {
		return false, fmt.Errorf("probing daemon lock %s: %w", path, err)
	}
	if !acquired {
		return true, nil
	}
	// Release at once. Holding a probe lock any longer than the check itself
	// would make `ls` refuse a daemon that was starting concurrently, turning a
	// read-only command into an outage.
	if err := unlock(file); err != nil {
		return false, fmt.Errorf("releasing daemon lock probe %s: %w", path, err)
	}
	return false, nil
}

// instanceDirs returns the absolute path of every directory exactly three
// levels below root, sorted, so `ls` output is stable between runs.
func instanceDirs(root string) ([]string, error) {
	origins, err := subdirs(root)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, origin := range origins {
		subs, err := subdirs(origin)
		if err != nil {
			return nil, err
		}
		for _, sub := range subs {
			workspaces, err := subdirs(sub)
			if err != nil {
				return nil, err
			}
			dirs = append(dirs, workspaces...)
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

func subdirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			out = append(out, filepath.Join(dir, entry.Name()))
		}
	}
	return out, nil
}

// slugRelativeTo renders dir as the "<origin>/<sub>/<workspace>" key. dir came
// from walking root, so the relative path is three segments by construction;
// the fallback is for a caller that passed something else.
func slugRelativeTo(root, dir string) string {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return dir
	}
	return strings.ReplaceAll(rel, string(filepath.Separator), "/")
}
