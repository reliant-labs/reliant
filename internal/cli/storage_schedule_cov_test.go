package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/storage"
)

// covAFakeTools puts stub executables for the named tools first on PATH. Each
// appends its argv to calls.log in the returned directory and exits with
// failing[name] (default 0). docker answers `context inspect` / `context show`
// the way a local engine does.
func covAFakeTools(t *testing.T, failing map[string]int, names ...string) (logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs; the schedule installer under test is unix-only")
	}
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	for _, name := range names {
		body := "#!/bin/sh\necho \"" + name + " $*\" >> '" + logPath + "'\n"
		if name == "docker" {
			body += `case "$*" in
*"context inspect"*) echo '[{"Endpoints":{"docker":{"Host":"unix:///var/run/docker.sock"}}}]' ;;
*"context show"*) echo desktop-linux ;;
esac
`
		}
		if code := failing[name]; code != 0 {
			body += "echo " + name + " exploded >&2\nexit " + string(rune('0'+code)) + "\n"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func covAReadLog(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestStorageScheduleInstalledFollowsTheUnitFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	var unit string
	switch runtime.GOOS {
	case "darwin":
		unit = filepath.Join(home, "Library", "LaunchAgents", "com.reliant.forge-storage.plist")
	case "linux":
		unit = filepath.Join(home, ".config", "systemd", "user", "forge-storage.timer")
	case "windows":
		var err error
		if unit, err = windowsTaskMarkerPath(); err != nil {
			t.Fatal(err)
		}
	default:
		t.Skip("forge does not schedule on " + runtime.GOOS + ": the notice is always suppressed there")
	}
	if storageScheduleInstalled() {
		t.Fatal("no unit file yet, so the schedule is not installed and the notice must show")
	}
	if err := os.MkdirAll(filepath.Dir(unit), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unit, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !storageScheduleInstalled() {
		t.Fatal("the unit file is there, so the schedule is installed")
	}
}

func TestInstallSystemdTimerWritesUnitsAndEnablesTheTimer(t *testing.T) {
	home := t.TempDir()
	calls := covAFakeTools(t, nil, "systemctl")
	t.Setenv("PATH", os.Getenv("PATH")) // keep the stub dir first

	// A path with a space, a quote and a literal % must survive both systemd's
	// own %-specifier expansion and its quoting rules.
	args := []string{"/opt/my forge/forge", "storage", "gc", "--policy", `/p/with "quote"/100%.json`}
	if err := installSystemdTimer(context.Background(), args, home, dailySystemdSchedule); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	svc, err := os.ReadFile(filepath.Join(dir, "forge-storage.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Description=Daily Forge storage maintenance",
		"Type=oneshot",
		`ExecStart="/opt/my forge/forge" "storage" "gc" "--policy" "/p/with \"quote\"/100%%.json"`,
		`Environment="PATH=`,
	} {
		if !strings.Contains(string(svc), want) {
			t.Errorf("service unit lacks %q:\n%s", want, svc)
		}
	}
	timer, err := os.ReadFile(filepath.Join(dir, "forge-storage.timer"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"OnCalendar=*-*-* 03:30:00", "Persistent=true", "WantedBy=timers.target"} {
		if !strings.Contains(string(timer), want) {
			t.Errorf("timer unit lacks %q:\n%s", want, timer)
		}
	}
	got := covAReadLog(t, calls)
	want := []string{"systemctl --user daemon-reload", "systemctl --user enable --now forge-storage.timer"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("systemctl calls = %q, want %q", got, want)
	}
}

func TestInstallSystemdTimerHourlyUsesItsOwnUnit(t *testing.T) {
	home := t.TempDir()
	calls := covAFakeTools(t, nil, "systemctl")
	if err := installSystemdTimer(context.Background(), []string{"/f", "storage", "auto-gc"}, home, hourlySystemdSchedule); err != nil {
		t.Fatal(err)
	}
	timer, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", "forge-storage-hourly.timer"))
	if err != nil || !strings.Contains(string(timer), "OnCalendar=hourly") {
		t.Fatalf("timer = %q err %v", timer, err)
	}
	if got := covAReadLog(t, calls); len(got) != 2 || !strings.HasSuffix(got[1], "forge-storage-hourly.timer") {
		t.Errorf("calls = %q", got)
	}
}

func TestInstallSystemdTimerReportsSystemctlFailures(t *testing.T) {
	t.Run("unwritable unit directory", func(t *testing.T) {
		home := t.TempDir()
		covAFakeTools(t, nil, "systemctl")
		if err := os.WriteFile(filepath.Join(home, ".config"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := installSystemdTimer(context.Background(), []string{"/f"}, home, dailySystemdSchedule); err == nil {
			t.Fatal("a file where the config dir should be must fail")
		}
	})
	t.Run("daemon-reload fails", func(t *testing.T) {
		calls := covAFakeTools(t, map[string]int{"systemctl": 1}, "systemctl")
		err := installSystemdTimer(context.Background(), []string{"/f"}, t.TempDir(), dailySystemdSchedule)
		if err == nil || !strings.Contains(err.Error(), "systemctl exploded") {
			t.Fatalf("err = %v", err)
		}
		if got := covAReadLog(t, calls); len(got) != 1 {
			t.Errorf("a failed daemon-reload must stop before enabling: %q", got)
		}
	})
}

func TestInstallLaunchAgentWritesEscapedPlistAndBootstraps(t *testing.T) {
	home := t.TempDir()
	calls := covAFakeTools(t, nil, "launchctl")
	args := []string{"/Applications/R&D/forge", "storage", "gc", "--policy", "/p/<x>.json"}
	if err := installLaunchAgent(context.Background(), args, home, dailyLaunchdSchedule); err != nil {
		t.Fatal(err)
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.reliant.forge-storage.plist")
	b, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatal(err)
	}
	plist := string(b)
	for _, want := range []string{
		"<string>com.reliant.forge-storage</string>",
		"<string>/Applications/R&amp;D/forge</string>",
		"<string>/p/&lt;x&gt;.json</string>",
		"<key>Hour</key><integer>3</integer><key>Minute</key><integer>30</integer>",
		filepath.Join(home, "Library", "Logs", "forge-storage", "com.reliant.forge-storage.log"),
		filepath.Join(home, "Library", "Logs", "forge-storage", "com.reliant.forge-storage.err.log"),
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist lacks %q:\n%s", want, plist)
		}
	}
	if st, err := os.Stat(plistPath); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("plist mode = %v err %v, want 0600", st.Mode(), err)
	}
	got := covAReadLog(t, calls)
	if len(got) != 2 || !strings.HasPrefix(got[0], "launchctl bootout gui/") || !strings.HasSuffix(got[0], "/com.reliant.forge-storage") ||
		!strings.HasPrefix(got[1], "launchctl bootstrap gui/") || !strings.HasSuffix(got[1], plistPath) {
		t.Errorf("launchctl calls = %q: bootout the old agent, then bootstrap the new plist", got)
	}
}

func TestInstallLaunchAgentIgnoresABootoutFailureButNotABootstrapFailure(t *testing.T) {
	calls := covAFakeTools(t, map[string]int{"launchctl": 5}, "launchctl")
	err := installLaunchAgent(context.Background(), []string{"/f"}, t.TempDir(), hourlyLaunchdSchedule)
	if err == nil || !strings.Contains(err.Error(), "launchctl exploded") {
		t.Fatalf("err = %v", err)
	}
	if got := covAReadLog(t, calls); len(got) != 2 {
		t.Errorf("bootout failing (agent not loaded yet) must not stop bootstrap: %q", got)
	}
}

func TestInstallLaunchAgentRefusesAnUnwritableHome(t *testing.T) {
	home := t.TempDir()
	covAFakeTools(t, nil, "launchctl")
	if err := os.WriteFile(filepath.Join(home, "Library"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installLaunchAgent(context.Background(), []string{"/f"}, home, dailyLaunchdSchedule); err == nil {
		t.Fatal("a file where Library should be must fail")
	}
}

func TestDailyStorageArgsAreScheduledAndApplied(t *testing.T) {
	got := dailyStorageArgs("/x/forge-storage", []string{"forge"}, "/p.json")
	want := "/x/forge-storage forge storage gc --policy /p.json --apply --scheduled"
	if strings.Join(got, " ") != want {
		t.Errorf("args = %q, want %q", strings.Join(got, " "), want)
	}
}

// installStorageSchedule copies the running binary next to the policy, so the
// test (whose binary is a few hundred MB) runs in the full tier only.
func TestInstallStorageScheduleEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("copies the test binary (hundreds of MB); runs in task test")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("shell stubs for the service manager are unix-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	manager := "systemctl"
	if runtime.GOOS == "darwin" {
		manager = "launchctl"
	}
	calls := covAFakeTools(t, nil, "docker", manager)
	policyPath := filepath.Join(t.TempDir(), "forge", "storage.json")
	t.Setenv("FORGE_STORAGE_POLICY", policyPath)

	var out strings.Builder
	if err := installStorageSchedule(context.Background(), &out, policyPath, storage.DefaultPolicy()); err != nil {
		t.Fatal(err)
	}
	saved, err := storage.Load(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.DockerContext != "desktop-linux" {
		t.Errorf("an unset docker context must be pinned to the one in use now, got %q", saved.DockerContext)
	}
	exe := filepath.Join(filepath.Dir(policyPath), "bin", "forge-storage")
	if st, err := os.Stat(exe); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("the pinned binary must be private and executable: %v %v", st, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(exe))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".forge-") {
			t.Errorf("temp copy %s left behind", e.Name())
		}
	}
	if !strings.Contains(out.String(), "Installed daily storage maintenance at 03:30 and an hourly bounded cache pass using "+exe) {
		t.Errorf("output = %q", out.String())
	}
	log := strings.Join(covAReadLog(t, calls), "\n")
	if !strings.Contains(log, manager) {
		t.Errorf("the service manager was never called:\n%s", log)
	}
	if runtime.GOOS == "linux" {
		svc, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", "forge-storage-hourly.service"))
		if err != nil || !strings.Contains(string(svc), `"storage" "auto-gc" "--policy" "`+policyPath+`"`) {
			t.Errorf("hourly unit = %q err %v", svc, err)
		}
	}
}

func TestInstallStorageScheduleStopsWhenDockerIsNotLocal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs")
	}
	covAFakeTools(t, nil, "docker")
	t.Setenv("DOCKER_HOST", "tcp://build-farm:2375")
	t.Setenv("DOCKER_CONTEXT", "")
	policyPath := filepath.Join(t.TempDir(), "storage.json")
	t.Setenv("FORGE_STORAGE_POLICY", policyPath)
	err := installStorageSchedule(context.Background(), &strings.Builder{}, policyPath, storage.DefaultPolicy())
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		if err == nil || !strings.Contains(err.Error(), "schedule 'forge storage daemon") {
			t.Fatalf("err = %v", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), "refuses nonlocal DOCKER_HOST") {
		t.Fatalf("a remote engine must never be scheduled for cleanup: %v", err)
	}
	if _, statErr := os.Stat(policyPath); statErr == nil {
		t.Error("no policy may be written when the engine is refused")
	}
}

func TestInstallStorageScheduleReportsAnUnusableDockerContext(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("scheduling is unix-only here")
	}
	covAFakeTools(t, map[string]int{"docker": 1}, "docker")
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	policyPath := filepath.Join(t.TempDir(), "storage.json")
	t.Setenv("FORGE_STORAGE_POLICY", policyPath)
	err := installStorageSchedule(context.Background(), &strings.Builder{}, policyPath, storage.DefaultPolicy())
	if err == nil || !strings.Contains(err.Error(), "docker exploded") {
		t.Fatalf("err = %v", err)
	}
}
