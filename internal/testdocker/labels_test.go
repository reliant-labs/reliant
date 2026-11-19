package testdocker

import (
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// A fixed clock: every age below is measured from it.
var labelNow = time.Date(2026, 11, 14, 19, 30, 0, 123456789, time.UTC)

var here = origin{host: "box-a", pid: 4242}

func TestLabelArgs(t *testing.T) {
	want := []string{
		"--label", "forge.test=openbao",
		"--label", "forge.test.created=2026-11-14T19:30:00.123456789Z",
		"--label", "forge.test.host=box-a",
		"--label", "forge.test.pid=4242",
	}
	if got := labelArgs("openbao", labelNow, here); !reflect.DeepEqual(got, want) {
		t.Errorf("labelArgs = %q; want %q", got, want)
	}
}

func TestLabelArgsWithoutAnOriginCarryNoOriginLabels(t *testing.T) {
	want := []string{"--label", "forge.test=openbao", "--label", "forge.test.created=2026-11-14T19:30:00.123456789Z"}
	for _, o := range []origin{{}, {host: "box-a"}, {pid: 4242}, {host: "bad\thost", pid: 4242}} {
		if got := labelArgs("openbao", labelNow, o); !reflect.DeepEqual(got, want) {
			t.Errorf("labelArgs(%+v) = %q; want %q", o, got, want)
		}
	}
}

func TestNetworkCreateArgs(t *testing.T) {
	got := networkCreateArgs("openbao", "bao-public-1", labelNow, here, []string{"--subnet", "10.20.30.0/24"})
	want := []string{"network", "create",
		"--label", "forge.test=openbao", "--label", "forge.test.created=2026-11-14T19:30:00.123456789Z",
		"--label", "forge.test.host=box-a", "--label", "forge.test.pid=4242",
		"--subnet", "10.20.30.0/24", "bao-public-1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("networkCreateArgs = %q; want %q", got, want)
	}
}

func TestStaleAfterExceedsTheLongestRun(t *testing.T) {
	for _, c := range []struct{ maxRun, timeout, want time.Duration }{
		{time.Hour, 0, time.Hour + StaleMargin},
		{time.Hour, 10 * time.Minute, time.Hour + StaleMargin},
		{time.Hour, 2 * time.Hour, 2*time.Hour + StaleMargin}, // a longer -timeout wins
	} {
		got := staleAfter(c.maxRun, c.timeout)
		if got != c.want {
			t.Errorf("staleAfter(%s, %s) = %s; want %s", c.maxRun, c.timeout, got, c.want)
		}
		if got <= c.maxRun || got <= c.timeout {
			t.Errorf("staleAfter(%s, %s) = %s; must be strictly longer than both", c.maxRun, c.timeout, got)
		}
	}
}

// ago is a CreatedLabel value d before labelNow.
func ago(d time.Duration) string { return labelNow.Add(-d).Format(time.RFC3339Nano) }

func noneAlive(int) bool { return false }

func TestStaleIDsNeverReapsARunWithinItsMaxRun(t *testing.T) {
	const maxRun = time.Hour
	after := staleAfter(maxRun, 0)
	// Made on another host, so only age decides.
	out := "" +
		"a\topenbao\t" + ago(maxRun-time.Second) + "\tbox-b\t1\n" + // a run near its end: kept
		"b\topenbao\t" + ago(maxRun) + "\tbox-b\t1\n" + // a run at its very end: kept
		"c\topenbao\t" + ago(after) + "\tbox-b\t1\n" + // exactly at the threshold: kept
		"d\topenbao\t" + ago(after+time.Nanosecond) + "\tbox-b\t1\n" // past it: reaped
	if got, want := staleIDs(out, "openbao", labelNow, after, here, noneAlive), []string{"d"}; !reflect.DeepEqual(got, want) {
		t.Errorf("staleIDs = %q; want %q", got, want)
	}
}

func TestStaleIDsReadsOldUnixSecondLabels(t *testing.T) {
	after := staleAfter(time.Hour, 0)
	old := labelNow.Add(-after - time.Second).Unix()
	fresh := labelNow.Add(-time.Hour).Unix()
	out := "" +
		"old\topenbao\t" + itoa(old) + "\t\t\n" + // labelled before origin labels: reaped by age
		"fresh\topenbao\t" + itoa(fresh) + "\n" // the three-column form: kept
	if got, want := staleIDs(out, "openbao", labelNow, after, here, noneAlive), []string{"old"}; !reflect.DeepEqual(got, want) {
		t.Errorf("staleIDs = %q; want %q", got, want)
	}
}

func TestStaleIDsKeepsResourcesOfALiveProcessOnThisHost(t *testing.T) {
	after := staleAfter(time.Hour, 0)
	old := ago(24 * time.Hour)
	alive := func(pid int) bool { return pid == 4242 || pid == 7 }
	out := "" +
		"live\topenbao\t" + old + "\tbox-a\t4242\n" + // ours, its process alive: kept
		"dead\topenbao\t" + old + "\tbox-a\t9999\n" + // ours, its process gone: reaped
		"other\topenbao\t" + old + "\tbox-b\t7\n" + // another host's pid says nothing here: reaped
		"nopid\topenbao\t" + old + "\tbox-a\t\n" // no pid: reaped by age
	if got, want := staleIDs(out, "openbao", labelNow, after, here, alive), []string{"dead", "other", "nopid"}; !reflect.DeepEqual(got, want) {
		t.Errorf("staleIDs = %q; want %q", got, want)
	}
}

func TestStaleIDsReapsOnlyTheOwnersOldResources(t *testing.T) {
	old := ago(10 * time.Hour)
	fresh := ago(10 * time.Minute)
	out := "" +
		"aaa\topenbao\t" + old + "\t\t\n" + // ours and stale: reaped
		"bbb\topenbao\t" + fresh + "\t\t\n" + // ours but live: kept
		"ccc\tclickhouse\t" + old + "\t\t\n" + // another suite's: kept
		"ddd\t\t" + old + "\t\t\n" + // unlabelled owner: kept
		"eee\topenbao\t\t\t\n" + // no created stamp: kept
		"fff\topenbao\tyesterday\t\t\n" + // unreadable stamp: kept
		"ggg\topenbao\n" + // malformed line: kept
		"\n" +
		"hhh\topenbao\t" + old + "\t\t\n" // ours and stale: reaped
	after := staleAfter(time.Hour, 0)
	if got, want := staleIDs(out, "openbao", labelNow, after, here, noneAlive), []string{"aaa", "hhh"}; !reflect.DeepEqual(got, want) {
		t.Errorf("staleIDs = %q; want %q", got, want)
	}
	if got := staleIDs(out, "openbao", labelNow, 24*time.Hour, here, noneAlive); len(got) != 0 {
		t.Errorf("staleIDs with a day's grace = %q; want none", got)
	}
}

func TestParseCreated(t *testing.T) {
	for _, c := range []struct {
		in   string
		want time.Time
		ok   bool
	}{
		{"2026-11-14T19:30:00.123456789Z", labelNow, true},
		{"2026-11-14T19:30:00Z", labelNow.Truncate(time.Second), true},
		{"1700000000", time.Unix(1_700_000_000, 0), true},
		{" 1700000000 ", time.Unix(1_700_000_000, 0), true},
		{"", time.Time{}, false},
		{"0", time.Time{}, false},
		{"-5", time.Time{}, false},
		{"yesterday", time.Time{}, false},
	} {
		got, ok := parseCreated(c.in)
		if ok != c.ok || !got.Equal(c.want) {
			t.Errorf("parseCreated(%q) = %s, %v; want %s, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("processAlive(own pid) = false; want true")
	}
	for _, pid := range []int{0, -1} {
		if processAlive(pid) {
			t.Errorf("processAlive(%d) = true; want false", pid)
		}
	}
}

func TestOwnerFilter(t *testing.T) {
	if got, want := ownerFilter("openbao"), "label=forge.test=openbao"; got != want {
		t.Errorf("ownerFilter = %q; want %q", got, want)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
