package pgtest

// Scratch databases name their owner, so a crashed run's leftovers are reaped.
//
// Every per-caller database pgtest creates — New, NewClone, NewURL and
// NewFromTemplate (forge_test_…) and NewAtURL / NewCloneAtURL (forge_shadow_…)
// — is dropped by its cleanup. A process that dies first (SIGKILL, a test
// -timeout panic, a closed laptop lid) never runs it. On the shared embedded
// server that used to be harmless only when the server was torn down soon
// after; on a long-lived one — an external FORGE_TEST_POSTGRES_URL server, or
// an embedded server a busy machine's fan-outs keep alive for days — every
// crash left its databases there for good.
//
// So the name records who owns the database:
//
//	forge_test_<space>_<pid>_<start>_<created>_<n>
//
//	space    12 hex digits of a hash of the owner's pid space (pidSpaceID):
//	         the boot, plus the pid namespace on linux. Only a process in the
//	         same pid space can probe the owner's pid, so only it may reap.
//	pid      the owner's pid
//	start    8 hex digits of a hash of the owner's start token
//	         (processStartToken), so a pid the kernel has since recycled is
//	         not mistaken for the owner
//	created  when the database was created, unix seconds
//	n        a per-process counter
//
// The name is the record because CREATE DATABASE is the only atomic step: an
// owner written by a second statement (COMMENT ON DATABASE) is missing exactly
// when a crash lands between the two. A field this process could not read is
// written as "0", and a database carrying one is never reaped by liveness.
//
// The first time a process reaches a server it sweeps it (reapAbandonedScratch)
// and drops every scratch database that is provably abandoned: same pid space,
// owner pid dead or recycled, and older than scratchGrace. Everything else is
// left alone — another machine's or container's databases, a live owner's, one
// whose owner cannot be read, a young one — because dropping a database a live
// test is using is far worse than leaking one. The DROP is not forced, so a
// database something is still connected to survives the sweep too.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Scratch database name prefixes: per-caller databases on pgtest's shared
// server, and the generate pipeline's on a caller-resolved one.
const (
	scratchTestPrefix   = "forge_test_"
	scratchShadowPrefix = "forge_shadow_"
)

// scratchUnknown is written for an owner field this process could not read.
const scratchUnknown = "0"

// scratchGrace is how old an abandoned scratch database must be before a sweep
// drops it. The owner check is exact, so this is defence in depth rather than
// the mechanism: the owner check is never the only thing standing between a
// just-created database and a DROP. A var so tests can sweep at once.
var scratchGrace = 10 * time.Minute

// owner is the identity a scratch database's name records.
type owner struct {
	space string // scratchUnknown when unreadable
	pid   int
	start string // scratchUnknown when unreadable
}

// selfOwner is this process's identity, read once.
var selfOwner = sync.OnceValue(func() owner {
	o := owner{space: scratchUnknown, pid: os.Getpid(), start: scratchUnknown}
	if id, ok := pidSpaceID(); ok {
		o.space = shortHash(id, 12)
	}
	if tok, ok := processStartToken(o.pid); ok {
		o.start = shortHash(tok, 8)
	}
	return o
})

func shortHash(s string, n int) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:n]
}

// scratchName names a new scratch database this process owns. At most 63
// bytes, postgres's identifier limit, for any pid and counter.
func scratchName(prefix string) string {
	o := selfOwner()
	return fmt.Sprintf("%s%s_%d_%s_%d_%d", prefix, o.space, o.pid, o.start, time.Now().Unix(), dbCounter.Add(1))
}

// ShadowScratchLike is a LIKE pattern (backslash escapes) matching exactly the
// scratch databases this process has created with NewAtURL and NewCloneAtURL,
// for a test that asserts its code dropped every one it made:
//
//	SELECT count(*) FROM pg_database WHERE datname LIKE $1  -- ShadowScratchLike()
func ShadowScratchLike() string {
	o := selfOwner()
	own := fmt.Sprintf("%s%s_%d_%s_", scratchShadowPrefix, o.space, o.pid, o.start)
	return strings.ReplaceAll(own, "_", `\_`) + "%"
}

// scratchRecord is what a scratch database's name says about it.
type scratchRecord struct {
	owner   owner
	created time.Time
}

// parseScratchName reads the owner record from a scratch database name. A name
// in any other shape — including the pid_counter names earlier versions wrote,
// which record no owner — is not ours to judge.
func parseScratchName(name string) (scratchRecord, bool) {
	rest, ok := strings.CutPrefix(name, scratchTestPrefix)
	if !ok {
		if rest, ok = strings.CutPrefix(name, scratchShadowPrefix); !ok {
			return scratchRecord{}, false
		}
	}
	f := strings.Split(rest, "_")
	if len(f) != 5 {
		return scratchRecord{}, false
	}
	if !ownerField(f[0], 12) || !ownerField(f[2], 8) {
		return scratchRecord{}, false
	}
	pid, err := strconv.Atoi(f[1])
	if err != nil || pid <= 0 {
		return scratchRecord{}, false
	}
	created, err := strconv.ParseInt(f[3], 10, 64)
	if err != nil || created <= 0 {
		return scratchRecord{}, false
	}
	if _, err := strconv.ParseUint(f[4], 10, 64); err != nil {
		return scratchRecord{}, false
	}
	return scratchRecord{owner: owner{space: f[0], pid: pid, start: f[2]}, created: time.Unix(created, 0)}, true
}

// ownerField reports whether s is scratchUnknown or n lowercase hex digits.
func ownerField(s string, n int) bool {
	if s == scratchUnknown {
		return true
	}
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// pidProbe reports whether pid is a live process and, when it can be read, the
// hashed start token of the process now holding it.
type pidProbe func(pid int) (alive bool, start string, startOK bool)

// probePid is the real pidProbe.
func probePid(pid int) (bool, string, bool) {
	if !processAlive(pid) {
		return false, "", false
	}
	tok, ok := processStartToken(pid)
	if !ok {
		return true, "", false
	}
	return true, shortHash(tok, 8), true
}

// abandoned reports whether the scratch database rec describes may be dropped
// by me at now: its owner shares my pid space and is provably gone, and it is
// at least grace old. Anything that cannot be proven says no.
func abandoned(rec scratchRecord, me owner, probe pidProbe, now time.Time, grace time.Duration) bool {
	o := rec.owner
	if me.space == scratchUnknown || o.space != me.space || o.start == scratchUnknown {
		return false
	}
	if now.Sub(rec.created) < grace {
		return false
	}
	if o.pid == me.pid && o.start == me.start {
		return false
	}
	alive, start, ok := probe(o.pid)
	if !alive {
		return true
	}
	if !ok {
		return false // a live process whose start cannot be read may be the owner
	}
	return start != o.start // the pid was recycled: the owner is gone
}

var (
	scratchMu    sync.Mutex
	scratchSwept = map[string]bool{} // servers (by maintenance URL) this process has swept
)

// sweepScratchOnce runs reapAbandonedScratch on baseURL's server the first
// time this process reaches it. Best effort: it never fails the caller.
func sweepScratchOnce(base *sql.DB, baseURL string) {
	scratchMu.Lock()
	if scratchSwept[baseURL] {
		scratchMu.Unlock()
		return
	}
	scratchSwept[baseURL] = true
	scratchMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	reapAbandonedScratch(ctx, base, selfOwner(), probePid, time.Now(), scratchGrace)
}

// reapAbandonedScratch drops every scratch database on base's server that
// abandoned says may go, and returns the names it dropped. Many processes may
// sweep one server at once: a database two of them drop is dropped once (IF
// EXISTS), and a failure — a database another role owns, one still in use —
// leaves it for a later sweep.
func reapAbandonedScratch(ctx context.Context, base *sql.DB, me owner, probe pidProbe, now time.Time, grace time.Duration) []string {
	rows, err := base.QueryContext(ctx, `
		SELECT datname FROM pg_database
		WHERE starts_with(datname, $1) OR starts_with(datname, $2)`, scratchTestPrefix, scratchShadowPrefix)
	if err != nil {
		return nil
	}
	var names []string
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			names = append(names, name)
		}
	}
	_ = rows.Close()

	var dropped []string
	for _, name := range names {
		rec, ok := parseScratchName(name)
		if !ok || !abandoned(rec, me, probe, now, grace) {
			continue
		}
		// Not WITH (FORCE): a session still connected — an orphaned child of
		// the dead owner, say — makes the DROP fail and the database waits for
		// a later sweep, rather than this one killing a connection.
		if _, err := base.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quoteIdent(name)); err == nil {
			dropped = append(dropped, name)
		}
	}
	return dropped
}
