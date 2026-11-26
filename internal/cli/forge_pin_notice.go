package cli

// How often the pin notices print.
//
// The hand-off notice ("forge: running forge_version X, which forge.yaml
// pins …") printed on EVERY command, `lint --quiet` included, in front of the
// verdict (Ranger build, friction item #49). It is news once: after that it
// says only that forge did what the project asked. And it read like a
// refusal, while the command ran.
//
// So the notices that confirm a forge someone chose — the hand-off to the
// pinned forge, and the one-liner under --allow-version-skew — print at most
// once a day per (project, pin, this forge), and never under --quiet, which
// promises only what failed. The day is kept in a stamp file under the user
// cache dir (pinNoticeDir); with no such dir every notice prints.
// FORGE_PIN_NOTICE=always prints it on every run, and `forge version --pinned`
// answers which forge the project runs at any time.
//
// What stays loud on every run, --quiet included: the full ⚠️ skew warning
// (a forge nobody chose runs the project's command, so its output may be
// wrong), the "could not be obtained — running this forge instead" fallback,
// and every refusal (handOffFailure, pinSkewRefusal). Those report a problem;
// throttling one is how the 2026-10-07 stale-forge week went unnoticed
// (forge_pin_skew.go's header).

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/pflag"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// pinNoticeEnv set to "always" prints the throttled pin notices on every run
// (still not under --quiet).
const pinNoticeEnv = "FORGE_PIN_NOTICE"

// quietFlag is the flag that promises only failures (`forge lint --quiet`).
const quietFlag = "quiet"

// Seams for the notice throttle: the clock, and the directory its stamps are
// kept in ("" keeps none, so every notice prints — main_test.go's hermetic
// default).
var (
	pinNoticeNow = time.Now
	pinNoticeDir = func() (string, error) {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "forge", "notices"), nil
	}
)

// pinNoticeDue reports whether the notice of kind, for the forge want in
// projectRoot run by this forge binary, prints now — and, when it does,
// records that it printed today. quiet is the invocation's --quiet.
//
// It fails open: an unreadable or unwritable stamp prints the notice.
func pinNoticeDue(kind, projectRoot, want, binary string, quiet bool) bool {
	if quiet {
		return false
	}
	if strings.TrimSpace(os.Getenv(pinNoticeEnv)) == "always" {
		return true
	}
	dir, err := pinNoticeDir()
	if err != nil || dir == "" {
		return true
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{kind, projectRoot, want, binary}, "\x00")))
	stamp := filepath.Join(dir, kind+"-"+hex.EncodeToString(sum[:8]))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return true
	}
	// Claim the entire read/decide/write operation across processes. A busy
	// lock means another invocation is handling the notice; don't delay the
	// command just to print it. Keep the lock file: unlinking it can let two
	// processes lock different inodes for the same stamp.
	lock := flock.New(stamp + ".lock")
	locked, err := lock.TryLock()
	if err != nil {
		return true
	}
	if !locked {
		return false
	}
	defer func() { _ = lock.Unlock() }()

	today := pinNoticeNow().Format(time.DateOnly)
	if data, err := os.ReadFile(stamp); err == nil && strings.TrimSpace(string(data)) == today {
		return false
	}
	_ = atomicfile.WriteFile(stamp, []byte(today+"\n"), 0o644)
	return true
}

// quietRun reports whether a run asked for only its failures: the command's
// own --quiet (and its shorthand, -q on `lint`) when flags defines it, else
// the long spelling in args — a command or flag the pinned forge has and this
// one lacks. A shorthand is trusted only where this forge knows it means
// quiet: -q could be anything to a command it cannot parse.
func quietRun(flags *pflag.FlagSet, args []string) bool {
	if flags != nil {
		if f := flags.Lookup(quietFlag); f != nil && f.Value.Type() == "bool" {
			on, _ := flags.GetBool(quietFlag)
			return on
		}
	}
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--"+quietFlag {
			return true
		}
		if v, ok := strings.CutPrefix(a, "--"+quietFlag+"="); ok {
			on, err := strconv.ParseBool(v)
			return err == nil && on
		}
	}
	return false
}
