package storage

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A trim can never see which cache entry a running build has resolved and not
// yet opened, but it can bound what that build might hold, from cmd/go's own
// contract (cmd/go/internal/cache): a lookup restamps an entry's mtime to now
// when the mtime is already more than mtimeInterval (1h) old. So an entry a
// process looked up at time t, at or after its start S, has an mtime of at
// least S-1h: either it was restamped to t, or it was already within the hour
// before t. An entry with an mtime older than S-1h was therefore never looked
// up by that process, and deleting it cannot fail it.
//
// The fixed 2h floor is that argument for a process younger than about an hour.
// A build older than that (a `go test -race ./...` gate runs 40-70 minutes)
// can hold an entry the floor no longer protects, so the cutoff is also held
// back to liveBuildMargin before the start of the oldest live toolchain
// process. That is a lease derived from the processes themselves, not a
// larger number: a build of any length is covered for as long as it runs.
//
// A process older than goCacheLongLived is a long-lived server (`go run` of a
// dev server, a watcher), not a build: it resolved its entries at start, and
// letting it pin the cache would make the budget unreachable for as long as it
// lives, so it is not counted. 3h is three times the longest run timeout the
// gates here declare (60m).
//
// The lease also covers a `go run` executable, which lives in the cache as an
// executable entry: `go run` waits on its child, so the parent `go` counts as a
// build for as long as the program runs, up to goCacheLongLived. A program
// that must outlive that runs from an installed binary, not from the cache.

// liveBuildMargin is added to cmd/go's mtimeInterval (1h): a process's clock
// reading and the filesystem's mtime resolution are not exact.
const liveBuildMargin = 1*time.Hour + 5*time.Minute

// goCacheLongLived is the age past which a toolchain process is a server, not
// a build.
const goCacheLongLived = 3 * time.Hour

// goToolchainProcs are the commands that open entries in the Go build cache.
// gopls and other language servers keep their own cache and are not listed.
var goToolchainProcs = map[string]bool{
	"go": true, "golangci-lint": true, "staticcheck": true, "govulncheck": true,
	"compile": true, "link": true, "vet": true, "asm": true, "cgo": true,
}

// liveBuildBound returns the latest mtime the trim may delete given the
// toolchain processes now running, and the age of the oldest one that
// counted. ok is false when no process counts: the floor alone is then the
// guard. A process table that cannot be read counts as a build goCacheLongLived
// old.
func (r Runner) liveBuildBound(ctx context.Context, now time.Time) (bound time.Time, oldest time.Duration, ok bool) {
	list := r.LiveToolchainAges
	if list == nil {
		if testing.Testing() {
			list = func(context.Context) ([]time.Duration, error) { return nil, nil }
		} else {
			list = psToolchainAges
		}
	}
	ages, err := list(ctx)
	if err != nil {
		// Fail closed: with no view of the builds, assume one as old as any
		// that counts, so the cutoff falls back to the age rule's reach.
		r.print("go build cache: cannot list running builds (%v); assuming one %s old\n", err, goCacheLongLived)
		ages = []time.Duration{goCacheLongLived}
	}
	for _, age := range ages {
		if age > goCacheLongLived {
			continue
		}
		oldest = max(oldest, age)
	}
	if oldest == 0 {
		return time.Time{}, 0, false
	}
	return now.Add(-oldest - liveBuildMargin), oldest, true
}

// psToolchainAges is how long each running Go toolchain process has lived.
func psToolchainAges(ctx context.Context) ([]time.Duration, error) {
	if runtime.GOOS == "windows" {
		return nil, fmt.Errorf("process listing is not supported on windows")
	}
	out, err := exec.CommandContext(ctx, "ps", "-axo", "etime=,comm=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return parsePsToolchainAges(string(out)), nil
}

// parsePsToolchainAges reads `ps -axo etime=,comm=` output, keeping the
// processes of goToolchainProcs. comm may be a path on some systems.
func parsePsToolchainAges(out string) []time.Duration {
	var ages []time.Duration
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		name := f[1]
		if i := strings.LastIndexByte(name, '/'); i >= 0 {
			name = name[i+1:]
		}
		if !goToolchainProcs[name] {
			continue
		}
		if d, ok := parsePsElapsed(f[0]); ok {
			ages = append(ages, d)
		}
	}
	return ages
}

// parsePsElapsed parses ps's etime: [[dd-]hh:]mm:ss.
func parsePsElapsed(s string) (time.Duration, bool) {
	var days int64
	if d, rest, found := strings.Cut(s, "-"); found {
		n, err := strconv.ParseInt(d, 10, 64)
		if err != nil {
			return 0, false
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var secs int64
	for _, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil || n < 0 {
			return 0, false
		}
		secs = secs*60 + n
	}
	return time.Duration(days*86400+secs) * time.Second, true
}
