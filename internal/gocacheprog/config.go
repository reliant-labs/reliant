package gocacheprog

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// DefaultTrustedDir is the machine's trusted tier when $FORGE_GO_TRUSTED_CACHE
// is unset. It is used only when it exists: a machine that was never seeded
// runs every build on its overlay.
const DefaultTrustedDir = "/var/cache/workspace/go-build-trusted"

// TrustedEnv names the trusted tier as the current context sees it (a runner
// or a gate container mounts the host's trusted dir at its own path).
const TrustedEnv = "FORGE_GO_TRUSTED_CACHE"

// StatsEnv names a file the helper appends one JSON line of counters to when
// it exits (Stats).
const StatsEnv = "FORGE_CACHE_PROG_STATS"

// Config is where the helper reads and writes.
type Config struct {
	// Trusted is the read-only tier. Empty after Resolve means overlay-only.
	Trusted string
	// Overlay is the tier the helper writes; cmd/go's GOCACHE by default.
	Overlay string
	// StatsPath, when set, is the file the counters are appended to at exit.
	StatsPath string
	// TrustedNote says why Trusted is empty after Resolve (for the stats
	// line), or "" when it is in use.
	TrustedNote string
}

// Resolve fills cfg's unset fields from getenv and the defaults, and checks
// them. It fails only when the helper cannot work at all: no overlay, or an
// overlay that is the trusted tier itself (every put would then write the
// trusted tier, which is exactly what the split forbids). A trusted tier that
// is missing or unreadable is dropped, with the reason in TrustedNote.
func Resolve(cfg Config, getenv func(string) string) (Config, error) {
	if cfg.Overlay == "" {
		cfg.Overlay = getenv("GOCACHE")
	}
	if cfg.Overlay == "" || cfg.Overlay == "off" {
		return cfg, errors.New("no overlay cache: set GOCACHE or pass --overlay")
	}
	if !filepath.IsAbs(cfg.Overlay) {
		return cfg, fmt.Errorf("overlay cache %q is not an absolute path", cfg.Overlay)
	}
	cfg.Overlay = filepath.Clean(cfg.Overlay)
	if err := os.MkdirAll(cfg.Overlay, 0o777); err != nil {
		return cfg, fmt.Errorf("create overlay cache: %w", err)
	}

	if cfg.StatsPath == "" {
		cfg.StatsPath = getenv(StatsEnv)
	}

	explicit := cfg.Trusted != ""
	if !explicit {
		cfg.Trusted = getenv(TrustedEnv)
		explicit = cfg.Trusted != ""
	}
	if !explicit {
		cfg.Trusted = DefaultTrustedDir
	}
	cfg.Trusted = filepath.Clean(cfg.Trusted)
	if !filepath.IsAbs(cfg.Trusted) {
		cfg.TrustedNote = fmt.Sprintf("trusted cache %q is not an absolute path", cfg.Trusted)
		cfg.Trusted = ""
		return cfg, nil
	}
	if same, err := sameDir(cfg.Overlay, cfg.Trusted); err == nil && same {
		return cfg, fmt.Errorf("the overlay cache %s is the trusted cache %s: untrusted builds must write somewhere else (point GOCACHE at an overlay)", cfg.Overlay, cfg.Trusted)
	}
	f, err := os.Open(cfg.Trusted)
	if err != nil {
		cfg.TrustedNote = "trusted cache unavailable: " + err.Error()
		cfg.Trusted = ""
		return cfg, nil
	}
	_, err = f.Readdirnames(1)
	_ = f.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		cfg.TrustedNote = "trusted cache unreadable: " + err.Error()
		cfg.Trusted = ""
	}
	return cfg, nil
}

// sameDir reports whether a and b name one directory, through symlinks and
// bind mounts alike. A b that does not exist is not a.
func sameDir(a, b string) (bool, error) {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false, err
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false, err
	}
	if filepath.Clean(ra) == filepath.Clean(rb) {
		return true, nil
	}
	ia, err := os.Stat(ra)
	if err != nil {
		return false, err
	}
	ib, err := os.Stat(rb)
	if err != nil {
		return false, err
	}
	return os.SameFile(ia, ib), nil
}

// TrustedDirForSeed names the trusted tier a trusted build writes: dir, else
// $FORGE_GO_TRUSTED_CACHE, else DefaultTrustedDir — whether or not it exists
// yet.
func TrustedDirForSeed(dir string, getenv func(string) string) string {
	if dir == "" {
		dir = getenv(TrustedEnv)
	}
	if dir == "" {
		dir = DefaultTrustedDir
	}
	return filepath.Clean(dir)
}

// CheckTrustedOwnership refuses a trusted tier anyone but this user could
// have written: a directory that is group- or world-writable, or owned by
// another uid, may already hold a planted entry, so seeding it would bless
// it. Windows has no such mode bits; there the check passes.
func CheckTrustedOwnership(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("trusted cache %s is not a directory", dir)
	}
	return checkOwner(dir, info)
}
