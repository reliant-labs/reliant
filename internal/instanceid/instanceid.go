// Copyright (c) 2025 Reliant Labs

// Package instanceid owns Reliant's stable per-machine identity.
//
// The hostname is not a stable key. macOS in particular reports a different
// name depending on how the machine is currently reachable — mDNS
// (`Seans-MacBook-Pro-2.local`) versus DHCP (`seans-mbp-2.lan`) — so the same
// physical machine reports two names within one session, with no process
// restart in between.
//
// That is not cosmetic. Temporal's LastWorkerIdentity defaults to
// `<pid>@<hostname>`, and during a real incident the identity changed from
// `46628@Seans-MacBook-Pro-2.local` to `20143@seans-mbp-2.lan`. That was read
// as "the worker moved to a different machine" — it had not; the hostname had
// simply flipped. A restart signal that fires on a network event is worse than
// no signal, because it is trusted.
//
// This package supplies a UUID generated once and persisted under
// `~/.reliant/instance.json`, so it is stable across restarts, network changes,
// and hostname changes. The hostname is kept ALONGSIDE it for display — the
// goal is a stable KEY, not the loss of a readable name.
//
// # Scope
//
// The id identifies a machine's Reliant state directory, not a process and not
// a user. Every Reliant process on the box (daemon, api-server, temporal
// worker) reads the same file and reports the same id. A container with an
// ephemeral home directory regenerates on each start, which is the correct
// reading: a fresh container IS a fresh instance. Deployments that want a
// pinned value can set RELIANT_INSTANCE_ID.
//
// # What this is NOT
//
// This id is not a registration key, an auth token, or a database identifier.
// The daemon's authoritative identity remains the server-assigned daemon_id
// persisted per-origin in `~/.reliant/daemon.json`; the `daemons.hostname`
// column and the registration message keep carrying the real hostname because
// the server's daemon-row reuse path matches on it. Nothing here is a lookup
// key, so nothing here can desynchronize one.
package instanceid

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	// FileName is the identity record's basename inside the Reliant state dir.
	FileName = "instance.json"

	// EnvOverride pins the instance id explicitly. Intended for container and
	// Kubernetes deployments where the home directory is ephemeral but the
	// workload identity is not: without it, every pod restart would look like
	// a new machine.
	EnvOverride = "RELIANT_INSTANCE_ID"

	// lockFileName serializes first-run generation across processes.
	lockFileName = "instance.lock"

	// unknownHost is reported when the OS will not tell us a hostname. It is a
	// display value only — never a key.
	unknownHost = "unknown-host"
)

// record is the on-disk identity file.
//
// CreatedOnHostname is deliberately informational and is NEVER read back as
// identity. It exists so a human opening the file can tell which machine
// minted the id; if it were consulted, the hostname instability this package
// exists to remove would leak straight back in.
type record struct {
	InstanceID        string    `json:"instance_id"`
	CreatedAt         time.Time `json:"created_at"`
	CreatedOnHostname string    `json:"created_on_hostname,omitempty"`
}

var (
	once   sync.Once
	cached string
)

// ID returns the stable instance UUID, generating and persisting it on first
// use. It never fails: if the state directory cannot be read or written, it
// degrades to a process-lifetime UUID so identity is still internally
// consistent for this run rather than crashing a daemon or worker boot.
//
// The result is memoized, so the disk is touched at most once per process.
func ID() string {
	once.Do(func() { cached = resolve() })
	return cached
}

// Short returns the first 8 hex digits of ID. That is the segment a human
// actually compares in a log line, and 32 bits is ample to distinguish the
// machines one operator looks at while remaining short enough to keep a
// Temporal identity readable.
func Short() string {
	id := ID()
	if len(id) < 8 {
		return id
	}
	return id[:8]
}

// Hostname returns the machine's current hostname for display. It is
// deliberately not stable — that is the entire premise of this package — and
// must never be used as a key.
func Hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return unknownHost
	}
	if name = strings.TrimSpace(name); name == "" {
		return unknownHost
	}
	return name
}

// Label returns "<short-id>@<hostname>": a stable key with a readable name
// attached. Use it wherever a human-facing identity string is wanted.
func Label() string {
	return Short() + "@" + Hostname()
}

// WorkerIdentity returns the identity string for a Temporal worker or client,
// formatted "<pid>.<short-id>@<hostname>".
//
// The shape extends Temporal's own "<pid>@<hostname>" default rather than
// replacing it, so existing greps and dashboards keep working. Each segment
// answers a different question, and the incident that motivated this needed
// all three: the pid says whether the PROCESS restarted, the short id says
// whether it is the same MACHINE (which the hostname alone cannot), and the
// hostname keeps the value readable at a glance.
func WorkerIdentity() string {
	return fmt.Sprintf("%d.%s@%s", os.Getpid(), Short(), Hostname())
}

// Dir returns the per-user Reliant state directory `~/.reliant` — the same
// location the daemon credentials store already uses.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".reliant"), nil
}

// Path returns the identity file's location.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

// resolve applies the override, then the on-disk record, then a last-resort
// ephemeral id.
func resolve() string {
	if pinned := strings.TrimSpace(os.Getenv(EnvOverride)); pinned != "" {
		return pinned
	}

	dir, err := Dir()
	if err != nil {
		id := uuid.NewString()
		logging.Warn("instanceid: no home directory; using a process-lifetime instance id",
			"error", err, "instance_id", id)
		return id
	}

	id, err := resolveIn(dir)
	if err != nil {
		id = uuid.NewString()
		logging.Warn("instanceid: could not persist instance id; using a process-lifetime id",
			"error", err, "dir", dir, "instance_id", id)
		return id
	}
	return id
}

// resolveIn reads — or mints and persists — the instance id in dir.
//
// The lock is what makes concurrent first runs converge. Two daemons starting
// at the same moment must agree on ONE id; without serialization both would
// generate their own and the second write would silently win, leaving the
// first process reporting an id that no longer matches the file it will read
// after the next restart.
func resolveIn(dir string) (string, error) {
	// Fast path: an existing valid record needs no lock at all, which is every
	// run after the first.
	if id, ok := readValid(dir); ok {
		return id, nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating reliant state dir: %w", err)
	}

	release, err := acquireLock(dir)
	if err != nil {
		// A lock we cannot take must not block a boot. Fall through unlocked:
		// the worst case is that two simultaneous first runs disagree for one
		// process lifetime, which is strictly better than refusing to start.
		logging.Warn("instanceid: proceeding without the generation lock", "error", err, "dir", dir)
		if id, ok := readValid(dir); ok {
			return id, nil
		}
		id := uuid.NewString()
		if writeErr := writeRecord(dir, id); writeErr != nil {
			return "", writeErr
		}
		return id, nil
	}
	defer release()

	// Re-read under the lock: another process may have written a valid record
	// between the fast-path read and the moment we took the lock. Whoever got
	// here first wins, and everyone else adopts their id.
	if id, ok := readValid(dir); ok {
		return id, nil
	}

	id := uuid.NewString()
	if err := writeRecord(dir, id); err != nil {
		return "", err
	}
	return id, nil
}

// readValid returns the persisted id when the record exists and holds a
// well-formed UUID.
//
// Missing, empty, truncated, malformed-JSON, and non-UUID records all report
// false and are treated identically: regenerate. A machine identity is not
// worth failing a boot over, and a corrupt file has no recoverable meaning —
// the only thing a caller could do with a parse error is mint a new id anyway.
func readValid(dir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		return "", false
	}
	var rec record
	if err := json.Unmarshal(data, &rec); err != nil {
		return "", false
	}
	id := strings.TrimSpace(rec.InstanceID)
	if id == "" {
		return "", false
	}
	if _, err := uuid.Parse(id); err != nil {
		return "", false
	}
	return id, true
}

// writeRecord persists the identity atomically: a fully-written temp file is
// renamed over the target, so a crash mid-write can leave a stray temp file
// but never a half-parsed identity record.
func writeRecord(dir, id string) error {
	data, err := json.MarshalIndent(record{
		InstanceID:        id,
		CreatedAt:         time.Now().UTC(),
		CreatedOnHostname: Hostname(),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding instance record: %w", err)
	}

	tmp, err := os.CreateTemp(dir, FileName+".*")
	if err != nil {
		return fmt.Errorf("creating temp instance record: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("writing temp instance record: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("closing temp instance record: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp instance record: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, FileName)); err != nil {
		cleanup()
		return fmt.Errorf("publishing instance record: %w", err)
	}
	return nil
}

// lockAcquireTimeout bounds how long a first run waits for a peer's generation
// to land. Generation is a UUID plus one small atomic write, so anything past
// this is a stuck or dead holder rather than contention.
const lockAcquireTimeout = 2 * time.Second

// lockPollInterval is the retry gap while waiting for the lock.
const lockPollInterval = 5 * time.Millisecond

// acquireLock takes an exclusive advisory lock on <dir>/instance.lock and
// returns a release function.
//
// The lock lives on a dedicated file rather than on instance.json, so the
// record itself is only ever created by rename and never held open. The OS
// drops an advisory lock when its holder dies for any reason, so this claim
// cannot go stale.
//
// Retries are non-blocking with a deadline rather than a blocking flock: a
// wedged holder should cost a bounded wait and a warning, not a hung daemon.
func acquireLock(dir string) (func(), error) {
	path := filepath.Join(dir, lockFileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening instance lock %s: %w", path, err)
	}

	deadline := time.Now().Add(lockAcquireTimeout)
	for {
		held, lockErr := tryLockExclusiveFile(file)
		if lockErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("locking %s: %w", path, lockErr)
		}
		if held {
			return func() {
				_ = unlockFile(file)
				_ = file.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("timed out waiting for instance lock %s", path)
		}
		time.Sleep(lockPollInterval)
	}
}
