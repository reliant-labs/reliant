package testdocker

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The labels a smoke test puts on every docker network and container it
// creates. OwnerLabel names the suite that made it ("openbao"), CreatedLabel
// when (RFC 3339 with nanoseconds, UTC; resources labelled before that carry
// unix seconds, which still parse), and HostLabel and PIDLabel the test
// process that made it, so a reap on the same host can ask whether that
// process is still alive.
//
// WHY. A test removes what it made in t.Cleanup, which never runs when the test
// binary is killed (a -timeout panic, ^C, a process-group kill), and `docker
// network rm` fails while a container is still attached. The leftovers piled
// up on shared machines as unlabelled `bao-smoke-*` networks that nothing could
// tell apart from another agent's (forge#2283). With these labels ReapStale
// removes exactly a suite's own leftovers and nothing else.
const (
	OwnerLabel   = "forge.test"
	CreatedLabel = "forge.test.created"
	HostLabel    = "forge.test.host"
	PIDLabel     = "forge.test.pid"
)

// StaleMargin is how far past a suite's longest run ReapStale waits before it
// treats an owner's resource as left behind. The grace must be strictly longer
// than any run: a grace equal to the longest run reaps a run that is still
// going at that mark.
const StaleMargin = 30 * time.Minute

// origin is the test process that creates a resource.
type origin struct {
	host string
	pid  int
}

func self() origin {
	host, _ := os.Hostname()
	return origin{host: host, pid: os.Getpid()}
}

// Labels is the `docker run` / `docker network create` flags that mark a
// resource as owner's, created now by this process.
func Labels(owner string) []string {
	return labelArgs(owner, time.Now(), self())
}

func labelArgs(owner string, now time.Time, o origin) []string {
	args := []string{
		"--label", OwnerLabel + "=" + owner,
		"--label", CreatedLabel + "=" + now.UTC().Format(time.RFC3339Nano),
	}
	// A host with no name (or one that would break the tab-separated listing
	// ReapStale reads) gets no origin labels: its resources fall back to age.
	if o.host != "" && o.pid > 0 && !strings.ContainsAny(o.host, "\t\n") {
		args = append(args,
			"--label", HostLabel+"="+o.host,
			"--label", PIDLabel+"="+strconv.Itoa(o.pid))
	}
	return args
}

// Network creates the docker network name, labelled as owner's (Labels), with
// extraArgs (such as "--subnet", cidr) passed to `docker network create`. Its
// cleanup removes the containers on it that carry owner's label, then the
// network; a cleanup failure is logged, not failed, because ReapStale gets
// whatever is left on a later run.
//
// It returns the create error rather than failing, so the caller decides
// whether a network docker refused (a subnet already in use) is a failure or a
// skip.
func Network(t testing.TB, owner, name string, extraArgs ...string) error {
	t.Helper()
	mustOwner(t, owner)
	if out, err := exec.Command("docker", networkCreateArgs(owner, name, time.Now(), self(), extraArgs)...).CombinedOutput(); err != nil {
		return fmt.Errorf("docker network create %s: %w\n%s", name, err, out)
	}
	t.Cleanup(func() {
		out, err := exec.Command("docker", "ps", "-aq", "--filter", "network="+name, "--filter", ownerFilter(owner)).Output()
		if err != nil {
			t.Logf("testdocker: listing the containers on network %s: %v", name, err)
		}
		if ids := strings.Fields(string(out)); len(ids) > 0 {
			if out, err := exec.Command("docker", append([]string{"rm", "-fv"}, ids...)...).CombinedOutput(); err != nil {
				t.Logf("testdocker: removing the containers on network %s: %v\n%s", name, err, out)
			}
		}
		if out, err := exec.Command("docker", "network", "rm", name).CombinedOutput(); err != nil {
			t.Logf("testdocker: removing network %s (ReapStale removes it on a later run): %v\n%s", name, err, out)
		}
	})
	return nil
}

func networkCreateArgs(owner, name string, now time.Time, o origin, extraArgs []string) []string {
	args := append([]string{"network", "create"}, labelArgs(owner, now, o)...)
	args = append(args, extraArgs...)
	return append(args, name)
}

// ReapStale removes what an earlier run of owner's suite left behind when it
// was killed before its cleanup ran: owner's containers and networks whose
// creating process is gone. Containers go first, so their networks are free to
// remove. A resource without owner's label, or without a readable
// CreatedLabel, is never touched: the docker daemon is shared with other
// agents and their tests. Failures are logged, not failed; reaping is
// housekeeping, not the test.
//
// A resource made on this host by a process that is still alive is never
// reaped, however old. Any other resource (made on another host sharing the
// daemon, by a process that has exited, or labelled before the origin labels
// existed) is reaped once it is older than staleAfter: maxRun, the longest a
// run of the suite can take, or this test binary's -timeout if that is longer,
// plus StaleMargin.
func ReapStale(t testing.TB, owner string, maxRun time.Duration) {
	t.Helper()
	mustOwner(t, owner)
	format := `{{.ID}}	{{.Label "` + OwnerLabel + `"}}	{{.Label "` + CreatedLabel + `"}}	{{.Label "` +
		HostLabel + `"}}	{{.Label "` + PIDLabel + `"}}`
	after := staleAfter(maxRun, testTimeout())
	here := self()
	now := time.Now()
	for _, kind := range []struct{ list, rm []string }{
		{[]string{"ps", "-a"}, []string{"rm", "-fv"}},
		{[]string{"network", "ls"}, []string{"network", "rm"}},
	} {
		out, err := exec.Command("docker", append(kind.list, "--filter", ownerFilter(owner), "--format", format)...).Output()
		if err != nil {
			t.Logf("testdocker: docker %s: %v", strings.Join(kind.list, " "), err)
			continue
		}
		ids := staleIDs(string(out), owner, now, after, here, processAlive)
		if len(ids) == 0 {
			continue
		}
		if out, err := exec.Command("docker", append(kind.rm, ids...)...).CombinedOutput(); err != nil {
			t.Logf("testdocker: reaping %s's stale resources (docker %s): %v\n%s", owner, strings.Join(kind.rm, " "), err, out)
			continue
		}
		t.Logf("testdocker: reaped %d stale %s resource(s) older than %s: %s", len(ids), owner, after, strings.Join(ids, " "))
	}
}

// staleAfter is the age past which a resource whose owner cannot be shown
// alive is reaped: the longer of maxRun and the binary's timeout (0 when it
// has none), plus StaleMargin, so it always exceeds the longest run.
func staleAfter(maxRun, timeout time.Duration) time.Duration {
	return max(maxRun, timeout) + StaleMargin
}

// testTimeout is this test binary's -timeout, or 0 when it has none (or is
// not a test binary).
func testTimeout() time.Duration {
	f := flag.Lookup("test.timeout")
	if f == nil {
		return 0
	}
	if g, ok := f.Value.(flag.Getter); ok {
		if d, ok := g.Get().(time.Duration); ok && d > 0 {
			return d
		}
	}
	return 0
}

// staleIDs reads `ID<TAB>owner<TAB>created<TAB>host<TAB>pid` lines (host and
// pid empty on resources labelled before they existed) and returns the IDs
// labelled as owner's that are left behind: not made by a live process on
// here's host, and created more than after before now. The docker filter
// already selected owner's resources; the owner column is checked again so a
// filter that matched more than it should cannot widen the reap.
func staleIDs(out, owner string, now time.Time, after time.Duration, here origin, alive func(pid int) bool) []string {
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		// Trim only the line ending: empty trailing columns are part of the row.
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 3 || f[0] == "" || f[1] != owner {
			continue
		}
		created, ok := parseCreated(f[2])
		if !ok {
			continue
		}
		if len(f) >= 5 && here.host != "" && f[3] == here.host {
			if pid, err := strconv.Atoi(strings.TrimSpace(f[4])); err == nil && pid > 0 && alive(pid) {
				continue // its run is still going on this host
			}
		}
		if now.Sub(created) > after {
			ids = append(ids, f[0])
		}
	}
	return ids
}

// parseCreated reads a CreatedLabel value: RFC 3339 (with or without
// fractional seconds), or the unix seconds earlier labels carry.
func parseCreated(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return ts, true
	}
	if secs, err := strconv.ParseInt(s, 10, 64); err == nil && secs > 0 {
		return time.Unix(secs, 0), true
	}
	return time.Time{}, false
}

func ownerFilter(owner string) string { return "label=" + OwnerLabel + "=" + owner }

// mustOwner refuses an empty owner: it would label nothing as anyone's, and a
// reap by it must not be guessed at.
func mustOwner(t testing.TB, owner string) {
	t.Helper()
	if strings.TrimSpace(owner) == "" || strings.ContainsAny(owner, "=\t\n") {
		t.Fatalf("testdocker: owner %q must be a non-empty label value without '=', tabs or newlines", owner)
	}
}
