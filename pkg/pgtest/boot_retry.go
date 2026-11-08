package pgtest

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

const embeddedBootAttempts = 4

// portBootError preserves the last collision while reporting the bounded set
// of candidates. Internal: callers need no new public API to recover from it.
type portBootError struct {
	ports []uint32
	err   error
}

func (e *portBootError) Error() string {
	return fmt.Sprintf("port collisions exhausted %d embedded postgres boot attempts (ports %v); check concurrent listeners or set %s to an existing test server: %v", len(e.ports), e.ports, EnvBaseURL, e.err)
}
func (e *portBootError) Unwrap() error { return e.err }

// startEmbeddedOnFreshPort retries only a confirmed bind collision. Dependencies
// are per-call so deterministic tests never replace process-global functions.
// Config is copied unchanged except for the port and its runtime directory.
//
// On success the runtime directory's boot claim is returned STILL HELD: the
// server has neither an owner lock nor a pool record yet, so the claim is all
// that keeps another process's reaper (portsInUse) off it. The caller
// releases it once the server's lasting ownership is established.
func startEmbeddedOnFreshPort(cfg embeddedpostgres.Config, reserve func() (uint32, error), dirFor func(uint32) string, start func(embeddedpostgres.Config) (*Embedded, error)) (*Embedded, uint32, *bootClaim, error) {
	var ports []uint32
	var last error
	for range embeddedBootAttempts {
		port, err := reserve()
		if err != nil {
			return nil, 0, nil, fmt.Errorf("pgtest: reserve port: %w", err)
		}
		ports = append(ports, port)
		dir := dirFor(port)
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return nil, 0, nil, fmt.Errorf("pgtest: create runtime parent: %w", err)
		}
		// The dependency unconditionally RemoveAlls RuntimePath before
		// extraction, so a claim INSIDE dir would be deleted mid-boot. The
		// claim is a sibling lock file held until start returns; a held
		// claim or an existing dir (somebody else's, even if the port
		// currently looks free) means the candidate is skipped.
		claim, err := claimBootDir(dir)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("pgtest: claim runtime directory: %w", err)
		}
		if claim == nil {
			last = fmt.Errorf("runtime directory %s already claimed", dir)
			continue
		}
		ep, err := start(cfg.Port(port).RuntimePath(dir))
		if err == nil {
			return ep, port, claim, nil
		}
		// No Stop/stopSharedByPort here: the listener that won the race is
		// NOT ours. StartEmbedded owns its failed-start logs. Remove only an
		// empty dir; partial extraction/data is left intact, never swept.
		_ = os.Remove(dir)
		claim.release()
		if !embeddedBindCollision(err, port, dir) {
			return nil, 0, nil, err
		}
		last = err
	}
	return nil, 0, nil, &portBootError{ports: ports, err: last}
}

// bootClaim is an exclusive kernel lock on bootClaimPath(dir), held while
// StartEmbedded populates dir and until the booted server's owner (a pool
// record with a live lease, or an owner lock) is in place. Kernel locks belong to the open file, so the
// claim means the same thing across pid namespaces, and a holder that dies
// releases it.
type bootClaim struct {
	f    *os.File
	path string
}

func bootClaimPath(dir string) string { return dir + ".boot" }

// claimBootDir takes dir's boot claim. It returns (nil, nil) when another
// process holds the claim or dir already exists: the candidate is not ours.
func claimBootDir(dir string) (*bootClaim, error) {
	claim, err := lockBootDir(dir)
	if err != nil || claim == nil {
		return claim, err
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		claim.release()
		return nil, nil
	}
	return claim, nil
}

// lockBootDir serializes boot/handoff with reaper ownership checks and removal.
// The sibling inode persists: unlinking permits two independently locked inodes.
// There is one zero-byte reusable file per TCP port per version root (at most
// 65535), not one per attempt, process or invocation.
func lockBootDir(dir string) (*bootClaim, error) {
	path := bootClaimPath(dir)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	held, err := tryLockFileExclusive(f)
	if err != nil || !held {
		_ = f.Close()
		return nil, err
	}
	// A previous holder unlinks the file before unlocking (release). If it
	// did so between our open and our lock, we locked an orphaned inode
	// that no contender can see: treat the candidate as taken.
	ours, statErr := f.Stat()
	onDisk, pathErr := os.Stat(path)
	if statErr != nil || pathErr != nil || !os.SameFile(ours, onDisk) {
		_ = f.Close()
		return nil, nil
	}
	return &bootClaim{f: f, path: path}, nil
}

// release leaves the stable sibling inode for future boots and reapers.
// A nil claim is a no-op, so error paths can release unconditionally.
func (c *bootClaim) release() {
	if c == nil {
		return
	}
	_ = c.f.Close()
}

// bootClaimHeld reports whether a boot into dir is in progress. A claim that
// cannot be tested counts as held: a dir is never reaped on doubt.
func bootClaimHeld(dir string) bool {
	f, err := os.Open(bootClaimPath(dir))
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	held, err := tryLockFileExclusive(f)
	return err != nil || !held
}

// addrInUse reports whether err is a bind refused because something still
// holds the address, in this OS's spelling (addrInUseErrnos).
func addrInUse(err error) bool {
	for _, errno := range addrInUseErrnos {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}

func embeddedBindCollision(err error, port uint32, dir string) bool {
	// Do not restart after an uncertain partial boot. Even a pid file whose
	// process is invisible here could belong to a still-running postmaster.
	if _, statErr := os.Stat(filepath.Join(dir, "data", "postmaster.pid")); !errors.Is(statErr, os.ErrNotExist) {
		return false
	}
	if err.Error() == fmt.Sprintf("process already listening on port %d", port) {
		// Upstream discards Listen's cause, so its wording alone also covers
		// permission/socket exhaustion errors. Confirm EADDRINUSE, not merely
		// the presence of an arbitrary startup error or an answering port.
		ln, listenErr := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
		if listenErr == nil {
			_ = ln.Close()
		}
		return addrInUse(listenErr)
	}
	// The second race is after upstream's precheck, during pg_ctl start.
	// Require both PostgreSQL's bind diagnostic and its terminal TCP failure,
	// not a generic "address" substring (download/initdb errors aren't binds).
	text := err.Error()
	if !strings.HasPrefix(text, "could not start postgres using ") || !strings.Contains(text, "FATAL:  could not create any TCP/IP sockets") {
		return false
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "could not bind IPv") && (strings.Contains(line, "Address already in use") || strings.Contains(line, "Only one usage of each socket address")) {
			return true
		}
	}
	return false
}
