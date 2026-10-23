package pgtest

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// A scratch name round-trips the owner record it was written with, and fits
// postgres's 63-byte identifier limit for the largest pid either OS hands out.
func TestScratchName_RoundTripsTheOwner(t *testing.T) {
	for _, prefix := range []string{scratchTestPrefix, scratchShadowPrefix} {
		name := scratchName(prefix)
		rec, ok := parseScratchName(name)
		if !ok {
			t.Fatalf("parseScratchName(%q) failed on a name scratchName wrote", name)
		}
		if rec.owner != selfOwner() {
			t.Errorf("%s: owner = %+v, want this process %+v", name, rec.owner, selfOwner())
		}
		if d := time.Since(rec.created); d < 0 || d > time.Minute {
			t.Errorf("%s: created %v, want about now", name, rec.created)
		}
	}

	// linux pid_max tops out at 2^22 (7 digits); a counter of a billion
	// databases in one process is far past anything real.
	widest := fmt.Sprintf("%s%s_%d_%s_%d_%d", scratchShadowPrefix,
		strings.Repeat("f", 12), 4194304, strings.Repeat("f", 8), time.Now().Unix(), 999999999)
	if len(widest) > 63 {
		t.Errorf("widest scratch name is %d bytes, over postgres's 63: %s", len(widest), widest)
	}
}

func TestParseScratchName_RejectsWhatItDidNotWrite(t *testing.T) {
	for _, name := range []string{
		"forge_test_123_4",   // the pid_counter names earlier versions wrote
		"forge_shadow_123_4", // likewise
		"forge_tmpl_abc",     // a template
		"postgres",           // anything else on the server
		"forge_test_abcdefabcdef_12_abcdef01_1700000000", // a field short
		"forge_test_abcdefabcdef_12_abcdef01_1700000000_1_2",
		"forge_test_ABCDEFABCDEF_12_abcdef01_1700000000_1", // not lowercase hex
		"forge_test_abcdef_12_abcdef01_1700000000_1",       // space hash too short
		"forge_test_abcdefabcdef_x_abcdef01_1700000000_1",  // pid not a number
		"forge_test_abcdefabcdef_0_abcdef01_1700000000_1",  // pid zero
		"forge_test_abcdefabcdef_12_abcdef01_soon_1",       // created not a number
		"forge_test_abcdefabcdef_12_abcdef01_1700000000_n", // counter not a number
	} {
		if rec, ok := parseScratchName(name); ok {
			t.Errorf("parseScratchName(%q) = %+v; it must not claim a name it did not write", name, rec)
		}
	}
	if _, ok := parseScratchName("forge_test_0_12_0_1700000000_1"); !ok {
		t.Error("a name with unreadable owner fields must still parse, so the sweep can decline it")
	}
}

// The reaping decision, case by case. Every case that cannot PROVE the owner
// gone keeps the database.
func TestAbandoned(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	me := owner{space: "aaaaaaaaaaaa", pid: 100, start: "11111111"}
	old := now.Add(-time.Hour)
	const grace = 10 * time.Minute

	dead := func(int) (bool, string, bool) { return false, "", false }
	liveAs := func(start string) pidProbe {
		return func(int) (bool, string, bool) { return true, start, true }
	}
	liveUnreadable := func(int) (bool, string, bool) { return true, "", false }

	cases := []struct {
		name  string
		rec   scratchRecord
		me    owner
		probe pidProbe
		want  bool
	}{
		{"owner dead, old: reap", scratchRecord{owner{"aaaaaaaaaaaa", 200, "22222222"}, old}, me, dead, true},
		{"owner pid recycled: reap", scratchRecord{owner{"aaaaaaaaaaaa", 200, "22222222"}, old}, me, liveAs("33333333"), true},
		{"owner alive: keep", scratchRecord{owner{"aaaaaaaaaaaa", 200, "22222222"}, old}, me, liveAs("22222222"), false},
		{"owner alive, start unreadable: keep", scratchRecord{owner{"aaaaaaaaaaaa", 200, "22222222"}, old}, me, liveUnreadable, false},
		{"owner dead but young: keep", scratchRecord{owner{"aaaaaaaaaaaa", 200, "22222222"}, now.Add(-time.Minute)}, me, dead, false},
		{"another pid space: keep", scratchRecord{owner{"bbbbbbbbbbbb", 200, "22222222"}, old}, me, dead, false},
		{"owner start unrecorded: keep", scratchRecord{owner{"aaaaaaaaaaaa", 200, scratchUnknown}, old}, me, dead, false},
		{"owner space unrecorded: keep", scratchRecord{owner{scratchUnknown, 200, "22222222"}, old}, me, dead, false},
		{"my own pid space unreadable: keep", scratchRecord{owner{scratchUnknown, 200, "22222222"}, old}, owner{scratchUnknown, 100, scratchUnknown}, dead, false},
		{"mine: keep", scratchRecord{me, old}, me, dead, false},
	}
	for _, c := range cases {
		if got := abandoned(c.rec, c.me, c.probe, now, grace); got != c.want {
			t.Errorf("%s: abandoned = %v, want %v", c.name, got, c.want)
		}
	}
}

// The probe a sweep uses recognizes this process as the owner its own names
// record — otherwise every sweeper would see every live owner as recycled.
func TestProbePid_RecognizesALiveOwner(t *testing.T) {
	me := selfOwner()
	if me.start == scratchUnknown {
		t.Skip("process start time is unreadable on this platform; such names are never reaped")
	}
	alive, start, ok := probePid(os.Getpid())
	if !alive || !ok || start != me.start {
		t.Fatalf("probePid(self) = (%v, %q, %v), want (true, %q, true)", alive, start, ok, me.start)
	}
	rec := scratchRecord{owner: me, created: time.Now().Add(-time.Hour)}
	other := owner{space: me.space, pid: me.pid + 1, start: me.start}
	if abandoned(rec, other, probePid, time.Now(), 0) {
		t.Fatal("a sweeper judged a live owner's database abandoned")
	}
}

func TestShadowScratchLike_MatchesOnlyThisProcess(t *testing.T) {
	pattern := ShadowScratchLike()
	if !strings.HasSuffix(pattern, "%") || strings.Count(pattern, "%") != 1 {
		t.Fatalf("pattern %q: want a literal prefix and one trailing %%", pattern)
	}
	prefix := strings.ReplaceAll(strings.TrimSuffix(pattern, "%"), `\_`, "_")
	if strings.Contains(prefix, `\`) || strings.Contains(strings.ReplaceAll(strings.TrimSuffix(pattern, "%"), `\_`, ""), "_") {
		t.Fatalf("pattern %q leaves an underscore unescaped: LIKE reads it as a wildcard", pattern)
	}
	if mine := scratchName(scratchShadowPrefix); !strings.HasPrefix(mine, prefix) {
		t.Errorf("own shadow %q does not match %q", mine, pattern)
	}
	if strings.HasPrefix(scratchName(scratchTestPrefix), prefix) {
		t.Errorf("pattern %q matches a forge_test_ database", pattern)
	}
	o := selfOwner()
	sibling := fmt.Sprintf("%s%s_%d_%s_%d_%d", scratchShadowPrefix, o.space, o.pid+1, o.start, time.Now().Unix(), 1)
	if strings.HasPrefix(sibling, prefix) {
		t.Errorf("pattern %q matches another process's %q", pattern, sibling)
	}
}
