package doctor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/storage"
)

// diskTestNow is a fixed clock. Every age in a message or in the evidence is
// derived from it, so no assertion here is a function of when the suite runs.
var diskTestNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// fakeDiskProbe is a probe whose every fact is stated by the test.
//
// statfs is injected rather than read, and that is the point of the whole
// seam: a table that asserted FAIL from the real disk would pass or fail
// according to how full the developer's laptop happened to be that morning,
// which is the one dependency a status-rule test may not have.
func fakeDiskProbe(free uint64, opts ...func(*diskProbe)) diskProbe {
	p := diskProbe{
		space: func(_ context.Context, path string) (hostSpace, error) {
			return hostSpace{Path: path, Free: free, Total: 1000 * storage.GiB}, nil
		},
		dockerRaw: func() string { return "" },
		usage: func(context.Context, string) (dockerRawUsage, bool, error) {
			return dockerRawUsage{}, false, nil
		},
		policyPath: func() (string, error) { return "/fake/config/forge/storage.json", nil },
		policy: func(string) (storage.Policy, error) {
			return storage.DefaultPolicy(), nil
		},
		schedule: func() (bool, string) { return true, "/fake/LaunchAgents/com.reliant.forge-storage.plist" },
		fullGC: func(string) (storage.GCResult, bool) {
			return storage.GCResult{At: diskTestNow.Add(-time.Hour), OK: true}, true
		},
		now: func() time.Time { return diskTestNow },
	}
	for _, o := range opts {
		o(&p)
	}
	return p
}

// withRegistries registers n local registries for cleanup.
func withRegistries(n int) func(*diskProbe) {
	return func(p *diskProbe) {
		base, _ := p.policy("")
		base.Registries = nil
		for i := 0; i < n; i++ {
			base.Registries = append(base.Registries, storage.Registry{
				Container:    "k3d-test-registry",
				Repositories: []string{"app"},
				Aliases:      []string{"k3d-test-registry:5000"},
				Contexts:     []string{"k3d-test"},
			})
		}
		p.policy = func(string) (storage.Policy, error) { return base, nil }
	}
}

// withoutSchedule is the state that guarantees registered caches grow
// forever: the policy names them and nothing is installed to reclaim them.
func withoutSchedule(p *diskProbe) {
	p.schedule = func() (bool, string) { return false, "/fake/LaunchAgents/com.reliant.forge-storage.plist" }
}

func TestCheckDisk_StatusRules(t *testing.T) {
	t.Parallel()

	// DefaultPolicy's host reserve is 20 GiB and the warning floor is 50.
	reserve := storage.DefaultPolicy().HostReserveGiB
	if reserve != 20 {
		t.Fatalf("test fixtures assume a 20 GiB default host reserve, got %d", reserve)
	}

	tests := []struct {
		name       string
		probe      diskProbe
		wantStatus Status
		wantInMsg  []string
		wantFix    string
	}{
		{
			// Below the policy's hard reserve: forge's own build admission
			// check refuses builds here, so this is the one arm that must
			// not be a warning.
			name:       "fail below host reserve",
			probe:      fakeDiskProbe(12 * storage.GiB),
			wantStatus: StatusFail,
			wantInMsg:  []string{"12.0 GiB free", "20 GiB host reserve", "REFUSE"},
			wantFix:    diskFixGC,
		},
		{
			// The window the machine crossed in silence: well above the
			// reserve, far enough down that reclaiming is still a chore
			// rather than an emergency.
			name:       "warn below the free-space floor",
			probe:      fakeDiskProbe(31 * storage.GiB),
			wantStatus: StatusWarn,
			wantInMsg:  []string{"31.0 GiB free", "50 GiB floor"},
			wantFix:    diskFixGC,
		},
		{
			// Plenty of space TODAY, and nothing will ever reclaim the
			// registries the policy names. This warns on its own because
			// the accumulation is certain; waiting for the space warning
			// is waiting for the failure.
			name:       "warn when registries are registered with no schedule",
			probe:      fakeDiskProbe(400*storage.GiB, withRegistries(2), withoutSchedule),
			wantStatus: StatusWarn,
			wantInMsg:  []string{"2 local registry", "no GC schedule is installed"},
			wantFix:    diskFixInstall,
		},
		{
			// Low space AND no schedule: the space finding wins the status,
			// but the missing schedule must still ride on the one line the
			// reader sees, and the fix must name both commands.
			name:       "low space carries the missing schedule and both fixes",
			probe:      fakeDiskProbe(31*storage.GiB, withRegistries(1), withoutSchedule),
			wantStatus: StatusWarn,
			wantInMsg:  []string{"31.0 GiB free", "no GC schedule is installed to reclaim it"},
			wantFix:    diskFixGC + ", then " + diskFixInstall,
		},
		{
			name:       "pass with space and a schedule",
			probe:      fakeDiskProbe(400*storage.GiB, withRegistries(1)),
			wantStatus: StatusPass,
			wantInMsg:  []string{"400.0 GiB free", "GC scheduled"},
		},
		{
			// No registries registered means nothing is expected to be
			// reclaimed, so an absent schedule is not a finding. Warning
			// here would make the check permanently yellow on every
			// project that never built a local image.
			name:       "pass with no registries and no schedule",
			probe:      fakeDiskProbe(400*storage.GiB, withoutSchedule),
			wantStatus: StatusPass,
			wantInMsg:  []string{"no local registries registered"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := checkDisk(context.Background(), &Environment{ProjectDir: t.TempDir()}, tc.probe)
			if res.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q\nmessage: %s", res.Status, tc.wantStatus, res.Message)
			}
			for _, want := range tc.wantInMsg {
				if !strings.Contains(res.Message, want) {
					t.Errorf("message missing %q:\n%s", want, res.Message)
				}
			}
			if tc.wantFix == "" {
				return
			}
			// The contract: a non-PASS line ENDS with the literal command.
			// `forge doctor` prints only the message without -v, so a
			// diagnosis that does not hand over the next command leaves the
			// reader exactly where the silent machine left them.
			if !strings.HasSuffix(res.Message, "run: "+tc.wantFix) {
				t.Errorf("message does not end with the fix command %q:\n%s", tc.wantFix, res.Message)
			}
		})
	}
}

// TestCheckDisk_TightestFilesystemDecides pins that the status comes from
// the filesystem that will fail FIRST. The project directory and $TMPDIR are
// often different mounts, and reporting only the roomier one is how a full
// scratch volume gets rendered as healthy.
func TestCheckDisk_TightestFilesystemDecides(t *testing.T) {
	t.Parallel()

	probe := fakeDiskProbe(0)
	probe.space = func(_ context.Context, path string) (hostSpace, error) {
		free := 800 * storage.GiB
		if path == os.TempDir() {
			free = 3 * storage.GiB
		}
		return hostSpace{Path: path, Free: free, Total: 1000 * storage.GiB}, nil
	}

	res := checkDisk(context.Background(), &Environment{ProjectDir: t.TempDir()}, probe)
	if res.Status != StatusFail {
		t.Fatalf("status = %q, want fail — the roomy project dir must not mask a full TMPDIR\n%s", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, os.TempDir()) {
		t.Errorf("message names the wrong filesystem:\n%s", res.Message)
	}
	if !strings.Contains(res.Evidence, "800.0 GiB free") {
		t.Errorf("evidence should still report every filesystem read:\n%s", res.Evidence)
	}
}

// TestCheckDisk_NoFilesystemAnswered pins that an unreadable disk is
// UNDETERMINED, not a pass. The number this check exists to report is the
// one it does not have.
func TestCheckDisk_NoFilesystemAnswered(t *testing.T) {
	t.Parallel()

	probe := fakeDiskProbe(0)
	probe.space = func(_ context.Context, path string) (hostSpace, error) {
		return hostSpace{}, os.ErrPermission
	}

	res := checkDisk(context.Background(), &Environment{ProjectDir: t.TempDir()}, probe)
	if res.Status != StatusUnknown {
		t.Fatalf("status = %q, want unknown: %s", res.Status, res.Message)
	}
	if !strings.HasSuffix(res.Message, "run: "+diskFixGC) {
		t.Errorf("message does not end with the fix command:\n%s", res.Message)
	}
	if !strings.Contains(res.Evidence, "permission denied") {
		t.Errorf("evidence should carry the hole:\n%s", res.Evidence)
	}
}

// TestDockerRawStat_SparseFileReportsAllocatedNotApparent is the measurement
// the whole check turns on.
//
// A 1 GiB Truncate produces a file with a 1 GiB APPARENT size and almost no
// blocks committed — the same shape as Docker Desktop's Docker.raw, whose
// apparent size is a fixed 1 TiB on the day it holds 90 GiB. Reading
// fi.Size() here instead of st_blocks is the single mistake that would make
// this check report a full disk on every Mac forever, so it is pinned
// directly rather than inferred from a status.
func TestDockerRawStat_SparseFileReportsAllocatedNotApparent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "Docker.raw")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const apparent = int64(1 << 30) // 1 GiB
	if err := f.Truncate(apparent); err != nil {
		_ = f.Close()
		t.Fatalf("truncate: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	u, ok, err := dockerRawStat(context.Background(), path)
	if err != nil {
		t.Fatalf("dockerRawStat: %v", err)
	}
	if runtime.GOOS == "windows" {
		// Windows has no allocated-size read, by design (disk_alloc_windows.go:
		// Docker.raw is a macOS artefact), and its contract there is NO number
		// rather than the apparent size masquerading as one.
		if ok {
			t.Fatalf("dockerRawStat reported %+v on Windows; it must report no usage there", u)
		}
		return
	}
	if !ok {
		t.Fatal("dockerRawStat reported no usage for a file that exists")
	}
	if u.Apparent != uint64(apparent) {
		t.Errorf("apparent = %d, want %d", u.Apparent, apparent)
	}
	// "~0" with generous headroom: a filesystem may commit metadata blocks
	// or refuse to sparsify at all. 64 MiB is far below the 1 GiB apparent
	// size, so the assertion still fails loudly if fi.Size() is ever
	// substituted for st_blocks.
	if u.Allocated > 64<<20 {
		t.Errorf("allocated = %d bytes for a sparse 1 GiB file; want ~0 (st_blocks*512, not fi.Size())", u.Allocated)
	}
}

// TestCheckDisk_DockerRawAbsentIsSilent pins the OrbStack / colima / Linux
// case. There is no such file on those machines, so its absence is neither a
// finding nor a hole — the check simply stops reporting a number that does
// not exist, and says nothing about Docker.raw at all.
func TestCheckDisk_DockerRawAbsentIsSilent(t *testing.T) {
	t.Parallel()

	probe := fakeDiskProbe(400 * storage.GiB)
	probe.dockerRaw = func() string { return filepath.Join(t.TempDir(), "Docker.raw") }
	probe.usage = dockerRawStat // the real reader, against a path with no file

	res := checkDisk(context.Background(), &Environment{ProjectDir: t.TempDir()}, probe)
	if res.Status != StatusPass {
		t.Fatalf("status = %q, want pass — an absent Docker.raw is not a finding: %s", res.Status, res.Message)
	}
	if strings.Contains(res.Message, "Docker.raw") || strings.Contains(res.Evidence, "Docker.raw") {
		t.Errorf("an absent Docker.raw must not be mentioned at all:\nmessage: %s\nevidence: %s", res.Message, res.Evidence)
	}
	if strings.Contains(res.Evidence, "Could not obtain") {
		t.Errorf("an absent Docker.raw must not be reported as a hole:\n%s", res.Evidence)
	}
}

// TestCheckDisk_DockerRawPresentShowsBothNumbers pins that the evidence
// carries the allocated AND apparent figures side by side. The pair is what
// stops the next reader quoting `ls` at someone: 1 GiB apparent next to ~0
// allocated explains the trap without a paragraph about it.
func TestCheckDisk_DockerRawPresentShowsBothNumbers(t *testing.T) {
	t.Parallel()

	probe := fakeDiskProbe(400 * storage.GiB)
	probe.dockerRaw = func() string { return "/fake/Docker.raw" }
	probe.usage = func(context.Context, string) (dockerRawUsage, bool, error) {
		return dockerRawUsage{
			Path:      "/fake/Docker.raw",
			Apparent:  1024 * storage.GiB,
			Allocated: 90 * storage.GiB,
		}, true, nil
	}

	res := checkDisk(context.Background(), &Environment{ProjectDir: t.TempDir()}, probe)
	if res.Status != StatusPass {
		t.Fatalf("status = %q, want pass: %s", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "Docker.raw holds 90.0 GiB") {
		t.Errorf("pass line should state the allocated figure:\n%s", res.Message)
	}
	for _, want := range []string{"allocated", "90.0 GiB", "apparent", "1024.0 GiB"} {
		if !strings.Contains(res.Evidence, want) {
			t.Errorf("evidence missing %q:\n%s", want, res.Evidence)
		}
	}
}

// TestCheckDisk_LastGCAgeReported pins that a recorded pass shows its age,
// and that a stale one is a finding. A schedule that is installed but not
// firing looks identical to a working one on every other signal, so a 5-day-
// old pass against a daily schedule WARNs — it used to PASS while its own
// evidence said "the daily pass is not firing".
func TestCheckDisk_LastGCAgeReported(t *testing.T) {
	t.Parallel()

	probe := fakeDiskProbe(400*storage.GiB, withRegistries(1), fullGC(diskTestNow.Add(-5*24*time.Hour), true))
	res := checkDisk(context.Background(), &Environment{ProjectDir: t.TempDir()}, probe)
	if res.Status != StatusWarn {
		t.Fatalf("status = %q, want warn: %s", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "5d ago") {
		t.Errorf("the line should state the GC age:\n%s", res.Message)
	}
	if !strings.Contains(res.Evidence, "the daily pass is not firing") {
		t.Errorf("a 5-day-old pass against a daily schedule should be called out:\n%s", res.Evidence)
	}
}

// TestCheckDisk_RealMachine is not an assertion — it is the sample of this
// check against the machine it runs on, with the real probes, so the reading
// can be read in the test log rather than inferred from fakes.
func TestCheckDisk_RealMachine(t *testing.T) {
	t.Parallel()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	res := CheckDisk(context.Background(), &Environment{ProjectDir: wd})
	t.Logf("status:  %s", res.Status)
	t.Logf("message: %s", res.Message)
	t.Logf("evidence:\n%s", res.Evidence)
}

// noFullGC is a machine on which no full GC has ever completed.
func noFullGC(p *diskProbe) {
	p.fullGC = func(string) (storage.GCResult, bool) { return storage.GCResult{}, false }
}

// fullGC states the last full storage GC's record.
func fullGC(at time.Time, ok bool, failed ...string) func(*diskProbe) {
	return func(p *diskProbe) {
		p.fullGC = func(string) (storage.GCResult, bool) {
			return storage.GCResult{At: at, OK: ok, FailedLayers: failed}, true
		}
	}
}

// TestCheckDisk_WarnsWhenRegistryRetentionIsNotRunning is the review's
// "protection reported as on while off". The disk check stayed PASS — "GC
// scheduled, last ran 2h ago" — when every full pass had failed (registry GC
// refusing on a stale context), because the opportunistic pass stamped the
// same record on failure and never runs the registry layer at all.
func TestCheckDisk_WarnsWhenRegistryRetentionIsNotRunning(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		opts []func(*diskProbe)
		want string
	}{
		{"last full GC failed", []func(*diskProbe){fullGC(diskTestNow.Add(-2*time.Hour), false, "registry k3d-cp-registry")}, "failed in registry k3d-cp-registry"},
		{"last full GC is stale", []func(*diskProbe){fullGC(diskTestNow.Add(-72*time.Hour), true)}, "3d ago"},
		{"no full GC ever completed", []func(*diskProbe){noFullGC}, "no full storage GC has ever completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := append([]func(*diskProbe){withRegistries(1)}, tc.opts...)
			probe := fakeDiskProbe(400*storage.GiB, opts...)
			res := checkDisk(context.Background(), &Environment{ProjectDir: t.TempDir()}, probe)
			if res.Status != StatusWarn {
				t.Fatalf("status = %q, want warn: %s", res.Status, res.Message)
			}
			if !strings.Contains(res.Message, tc.want) {
				t.Errorf("message does not say why:\n%s", res.Message)
			}
			if !strings.HasSuffix(res.Message, "run: "+diskFixFullGC) {
				t.Errorf("message does not end with the command that shows the failure:\n%s", res.Message)
			}
		})
	}
}

// TestCheckDisk_PassesWithARecentSuccessfulFullGC is the control.
func TestCheckDisk_PassesWithARecentSuccessfulFullGC(t *testing.T) {
	t.Parallel()
	probe := fakeDiskProbe(400*storage.GiB, withRegistries(1), fullGC(diskTestNow.Add(-3*time.Hour), true))
	res := checkDisk(context.Background(), &Environment{ProjectDir: t.TempDir()}, probe)
	if res.Status != StatusPass || !strings.Contains(res.Message, "last full GC 3h ago") {
		t.Fatalf("status = %q: %s", res.Status, res.Message)
	}
}
