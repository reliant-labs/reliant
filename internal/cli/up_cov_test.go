package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/doctor"
	"github.com/reliant-labs/forge/internal/envutil"
	"github.com/reliant-labs/forge/internal/hostlaunch"
	"github.com/reliant-labs/forge/internal/projectstore"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

type covCMeta struct{ name string }

func (m covCMeta) Meta() projectstore.ProjectMeta { return projectstore.ProjectMeta{Name: m.name} }

func TestCovC_UpDeployNamespace(t *testing.T) {
	cluster := func(ns string) WorkloadEntity {
		return WorkloadEntity{Name: "w", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Namespace: ns}}}
	}
	tests := []struct {
		name  string
		e     *KCLEntities
		store metaReader
		env   string
		want  string
	}{
		{"env-wide cluster target wins over everything", &KCLEntities{
			ClusterTarget: &ClusterTargetEntity{Namespace: "target-ns"}, ManifestNamespace: "manifest-ns",
			Workloads: []WorkloadEntity{cluster("svc-ns")},
		}, covCMeta{"proj"}, "dev", "target-ns"},
		{"first cluster workload's namespace", &KCLEntities{
			ManifestNamespace: "manifest-ns", Workloads: []WorkloadEntity{cluster(""), cluster("svc-ns")},
		}, covCMeta{"proj"}, "dev", "svc-ns"},
		{"manifests-only fallback", &KCLEntities{ManifestNamespace: "manifest-ns"}, covCMeta{"proj"}, "dev", "manifest-ns"},
		{"project-env convention", &KCLEntities{}, covCMeta{"proj"}, "dev", "proj-dev"},
		{"nil entities still use the convention", nil, covCMeta{"proj"}, "e2e", "proj-e2e"},
		{"no env and nothing declared", &KCLEntities{}, covCMeta{"proj"}, "", ""},
		{"no store", nil, nil, "dev", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := upDeployNamespace(tc.e, tc.store, tc.env); got != tc.want {
				t.Fatalf("namespace = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestCovC_ProxyPreflightTimeoutOutlastsAWindowsRefusal(t *testing.T) {
	if got := proxyPreflightTimeoutFor("windows"); got != 5*time.Second {
		t.Errorf("windows = %s; a closed loopback port takes ~2s to refuse there, so the budget must exceed it", got)
	}
	if got := proxyPreflightTimeoutFor("linux"); got != 2*time.Second {
		t.Errorf("linux = %s", got)
	}
}

func TestCovC_GoModuleDirs(t *testing.T) {
	root := t.TempDir()
	mod := func(rel string) string {
		d := filepath.Join(root, rel)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "go.mod"), []byte("module x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return d
	}
	project := mod(".")
	sibling := mod("sibling")
	hostWD := mod("hostwd")
	if err := os.MkdirAll(filepath.Join(root, "npmonly"), 0o755); err != nil {
		t.Fatal(err)
	}
	host := func(dir string) WorkloadEntity {
		return WorkloadEntity{Name: "h", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{WorkingDir: dir}}}
	}
	e := &KCLEntities{Workloads: []WorkloadEntity{host("hostwd"), host("sibling"), host("npmonly"), host("sibling"), host("")}}
	got := goModuleDirs(e, root)
	want := []string{project, hostWD, sibling}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("dirs = %v; want the project plus each distinct Go module a host workload runs from: %v", got, want)
	}
	if got := goModuleDirs(nil, root); !slices.Equal(got, []string{project}) {
		t.Errorf("nil entities: %v", got)
	}
	if got := goModuleDirs(nil, filepath.Join(root, "npmonly")); len(got) != 0 {
		t.Errorf("a directory with no go.mod is not a Go module: %v", got)
	}
}

func TestCovC_StaleModuleDiff(t *testing.T) {
	for name, tc := range map[string]struct {
		out  string
		want bool
	}{
		"empty":                       {"", false},
		"whitespace":                  {" \n", false},
		"a real diff":                 {"diff current/go.mod tidy/go.mod\n--- current/go.mod\n", true},
		"proxy failure":               {"go: finding module for package x: 404 Not Found\n", false},
		"diff of something else":      {"diff a/foo b/foo\n", false},
		"mentions go.mod but no diff": {"go: updates to go.mod needed\n", false},
	} {
		if got := staleModuleDiff([]byte(tc.out)); got != tc.want {
			t.Errorf("%s: = %v; want %v", name, got, tc.want)
		}
	}
}

func TestCovC_PreflightGoModulesTidy(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t.Run("a stale module graph is reported with the fix and without rewriting go.mod", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"go": `echo "diff current/go.mod tidy/go.mod"; exit 1`})
		err := preflightGoModulesTidy(ctx, nil, root)
		if err == nil || !strings.Contains(err.Error(), "go module graph is stale in "+root) || !strings.Contains(err.Error(), "go mod tidy") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a tidy module passes", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"go": `exit 0`})
		if err := preflightGoModulesTidy(ctx, nil, root); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a probe that cannot run is not evidence of staleness", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"go": `echo "go: 404 from proxy"; exit 1`})
		if err := preflightGoModulesTidy(ctx, nil, root); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a probe that outlives its budget is skipped, not failed", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"go": `exec sleep 30`})
		orig := goModTidyTimeout
		t.Cleanup(func() { goModTidyTimeout = orig })
		goModTidyTimeout = 100 * time.Millisecond
		var err error
		out := captureStdout(t, func() { err = preflightGoModulesTidy(ctx, nil, root) })
		if err != nil || !strings.Contains(out, "go module check skipped for "+root) {
			t.Fatalf("err %v out %q", err, out)
		}
	})
}

func TestCovC_PortHolderError(t *testing.T) {
	owned := []portConflict{{"api", 8080}}
	foreign := []portConflict{{"web", 3000}, {"admin", 3001}}
	t.Run("owned only points at env down", func(t *testing.T) {
		msg := portHolderError("dev", owned, nil).Error()
		for _, want := range []string{"forge-managed stack for env=dev still holds these ports", "api            :8080", "forge env down dev"} {
			if !strings.Contains(msg, want) {
				t.Errorf("lacks %q:\n%s", want, msg)
			}
		}
		if strings.Contains(msg, "doesn't manage") {
			t.Errorf("foreign section printed with no foreign holders:\n%s", msg)
		}
	})
	t.Run("foreign only says to free the ports or retarget", func(t *testing.T) {
		msg := portHolderError("dev", nil, foreign).Error()
		for _, want := range []string{"held by a process forge doesn't manage (env=dev)", ":3000", ":3001", "--target"} {
			if !strings.Contains(msg, want) {
				t.Errorf("lacks %q:\n%s", want, msg)
			}
		}
		if strings.HasPrefix(msg, "\n") {
			t.Errorf("a leading blank line:\n%q", msg)
		}
	})
	t.Run("both are separated into two sections", func(t *testing.T) {
		msg := portHolderError("dev", owned, foreign).Error()
		if i, j := strings.Index(msg, "forge-managed"), strings.Index(msg, "doesn't manage"); i < 0 || j < i || !strings.Contains(msg[i:j], "\n") {
			t.Errorf("sections out of order:\n%s", msg)
		}
	})
}

func TestCovC_PortAlsoDeclaredBy(t *testing.T) {
	host := func(name string, port int) WorkloadEntity {
		ports := []int{port}
		return WorkloadEntity{Name: name, Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{ListenPorts: &ports}}}
	}
	e := &KCLEntities{
		Workloads: []WorkloadEntity{host("api", 8080), host("worker", 8080), host("other", 9090)},
		Frontends: []FrontendEntity{{Name: "web", Port: 8080}, {Name: "docs", Port: 4000}},
	}
	got := portAlsoDeclaredBy(e, 8080, "api")
	slices.Sort(got)
	if !slices.Equal(got, []string{`frontend "web"`, `service "worker"`}) {
		t.Fatalf("declared by %v; want everything but the named service", got)
	}
	if got := portAlsoDeclaredBy(e, 4000, "docs"); len(got) != 0 {
		t.Errorf("the service itself is excluded: %v", got)
	}
}

func TestCovC_ReportEnvConflicts(t *testing.T) {
	out := captureStdout(t, func() {
		reportEnvConflicts("api", []envutil.EnvConflict{
			{Key: "DATABASE_URL", ShellWon: true},
			{Key: "PORT"},
		})
	})
	for _, want := range []string{
		"api: DATABASE_URL taken from your shell (" + hostlaunch.ShellWinsEnvVar + "=DATABASE_URL), overriding the declared value",
		"api: PORT uses the DECLARED value, ignoring a different one in your shell (export " + hostlaunch.ShellWinsEnvVar + "=PORT to prefer the shell)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if captured := captureStdout(t, func() { reportEnvConflicts("api", nil) }); captured != "" {
		t.Errorf("no conflicts must print nothing: %q", captured)
	}
}

func TestCovC_ReportAirBuildFlags(t *testing.T) {
	dir := t.TempDir()
	writeAir := func(t *testing.T, cmd string) string {
		t.Helper()
		p := filepath.Join(dir, ".air.toml")
		if err := os.WriteFile(p, []byte("[build]\ncmd = \""+cmd+"\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	flags := []string{"-trimpath", "-race"}
	t.Run("not air: silent", func(t *testing.T) {
		out := captureStdout(t, func() { reportAirBuildFlags("api", hostlaunch.RunnerSpec{Runner: "go-run", GoBuildFlags: flags}, nil) })
		if out != "" {
			t.Errorf("out = %q", out)
		}
	})
	t.Run("air with no flags: silent", func(t *testing.T) {
		out := captureStdout(t, func() { reportAirBuildFlags("api", hostlaunch.RunnerSpec{Runner: "air"}, nil) })
		if out != "" {
			t.Errorf("out = %q", out)
		}
	})
	t.Run("dropped flags are named with the reason", func(t *testing.T) {
		cfg := writeAir(t, "go build $"+hostlaunch.GoBuildFlagsEnv+" -o ./tmp/main .")
		out := captureStdout(t, func() {
			reportAirBuildFlags("api", hostlaunch.RunnerSpec{Runner: "air", AirConfig: cfg, GoBuildFlags: flags, ProjectDir: dir}, []string{"-ldflags=-X a=b c"})
		})
		if !strings.Contains(out, `host api: air does not get ["-ldflags=-X a=b c"]`) || !strings.Contains(out, "split on spaces") {
			t.Errorf("out = %q", out)
		}
		if strings.Contains(out, "does not build with") {
			t.Errorf("a config that expands the variable must not be told to add it: %q", out)
		}
	})
	t.Run("a config that ignores the variable names what it is missing", func(t *testing.T) {
		cfg := writeAir(t, "go build -trimpath -o ./tmp/main .")
		out := captureStdout(t, func() {
			reportAirBuildFlags("api", hostlaunch.RunnerSpec{Runner: "air", AirConfig: cfg, GoBuildFlags: flags, ProjectDir: dir}, nil)
		})
		if !strings.Contains(out, "does not build with $"+hostlaunch.GoBuildFlagsEnv) || !strings.Contains(out, "[-race]") || strings.Contains(out, "[-trimpath") {
			t.Errorf("only the flag the config really lacks is reported (its own -trimpath is fine): %q", out)
		}
	})
	t.Run("a config that already covers every flag is silent", func(t *testing.T) {
		cfg := writeAir(t, "go build -trimpath -o ./tmp/main .")
		out := captureStdout(t, func() {
			reportAirBuildFlags("api", hostlaunch.RunnerSpec{Runner: "air", AirConfig: cfg, GoBuildFlags: []string{"-trimpath"}, ProjectDir: dir}, nil)
		})
		if out != "" {
			t.Errorf("out = %q", out)
		}
	})
}

func TestCovC_PrintUpSummary(t *testing.T) {
	e := &KCLEntities{
		Workloads: []WorkloadEntity{
			{Name: "api", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{ListenPorts: &[]int{18090}}}},
			{Name: "worker", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{}}},
		},
	}
	reg := newProcRegistry("projC", t.TempDir(), "dev")
	reg.processes = []*managedProcess{{name: "worker", done: make(chan struct{})}}
	var rows []upServiceRow
	out := captureStdout(t, func() {
		rows = printUpSummary(e, "dev", true, nil, false, nil, reg)
	})
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	byName := map[string]upServiceRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	if byName["api"].URL != "http://localhost:18090" {
		t.Errorf("api row = %+v", byName["api"])
	}
	if got := byName["worker"].ProcessStatus; got != "running (process only)" {
		t.Errorf("a portless service with a live process is process-only, got %q", got)
	}
	for _, want := range []string{"forge env up · dev", "Host services", "Detached — stop with `forge env down dev`", "forge env status dev"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
	t.Run("no rows and no cluster prints nothing", func(t *testing.T) {
		if out := captureStdout(t, func() { printUpSummary(&KCLEntities{}, "dev", false, nil, false, nil) }); out != "" {
			t.Errorf("out = %q", out)
		}
	})
	t.Run("foreground gets the Ctrl-C hint", func(t *testing.T) {
		out := captureStdout(t, func() { printUpSummary(e, "dev", false, nil, false, nil) })
		if !strings.Contains(out, "Ctrl-C to stop.") || strings.Contains(out, "Detached") {
			t.Errorf("out = %s", out)
		}
	})
	t.Run("a portless service with no process is not running", func(t *testing.T) {
		empty := newProcRegistry("projC", t.TempDir(), "dev")
		var r []upServiceRow
		captureStdout(t, func() { r = printUpSummary(e, "dev", false, nil, false, nil, empty) })
		for _, row := range r {
			if row.Name == "worker" && row.ProcessStatus != "not running" {
				t.Errorf("worker = %+v", row)
			}
		}
	})
}

func TestCovC_AuthParityTrailer(t *testing.T) {
	dir := t.TempDir()
	if got := authParityTrailerIn(dir, "dev"); got != "" {
		t.Errorf("a project that declares no issuer on either side is closed and bootable, not a warning: %q", got)
	}
	t.Chdir(dir)
	if got := authParityTrailer("dev"); got != "" {
		t.Errorf("trailer in an empty project = %q", got)
	}
	// The warning text is the doctor's, framed by forge env up.
	res := doctor.CheckResult{Status: doctor.StatusWarn, Message: "backend issuer https://a differs from frontend issuer https://b"}
	got := "⚠ Auth: " + res.Message + "\n│   Sign-in will succeed and RPCs will still 401. Details: forge doctor"
	if !strings.Contains(got, "RPCs will still 401") {
		t.Fatal("unreachable")
	}
}

func TestCovC_RenderUpSummaryTable(t *testing.T) {
	rows := []upServiceRow{
		{Name: "api", Kind: "host", URL: "http://localhost:8080", Port: 8080, Log: ".forge/logs/dev/api.log", Listening: true},
		{Name: "worker", Kind: "host", Log: ".forge/logs/dev/worker.log"},
		{Name: "web", Kind: "frontend", URL: "http://localhost:3000", Port: 3000, Log: ".forge/logs/dev/frontend_web.log"},
		{Name: "pg", Kind: "compose", URL: "localhost:5432", Port: 5432, Log: ".forge/logs/dev/pg.log"},
	}
	var buf bytes.Buffer
	renderUpSummary(&buf, "dev", rows, "starting", false, []string{"extra trailer"},
		&clusterWorkloadSummary{status: doctor.StatusFail, message: "daemon-gateway: CrashLoopBackOff"})
	out := buf.String()
	for _, want := range []string{
		"CLUSTER WORKLOADS NOT RUNNING", "Host services", "Frontends", "Compose services",
		"↳ .forge/logs/dev/api.log", "starting", "extra trailer", "env exec dev --", "daemon-gateway: CrashLoopBackOff",
		"forge cluster urls",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "Host services") > strings.Index(out, "Frontends") || strings.Index(out, "Frontends") > strings.Index(out, "Compose services") {
		t.Errorf("groups must print host, frontend, compose in that order:\n%s", out)
	}
	// Rows only for kinds that cannot be `env exec`'d offer no exec hint.
	buf.Reset()
	renderUpSummary(&buf, "dev", rows[2:3], "down", false, nil, nil)
	if strings.Contains(buf.String(), "env exec") {
		t.Errorf("a frontend-only table must not offer env exec:\n%s", buf.String())
	}
	buf.Reset()
	renderUpSummary(&buf, "dev", nil, "down", false, nil, nil)
	if buf.Len() != 0 {
		t.Errorf("nothing to show must print nothing: %q", buf.String())
	}
}

func TestCovC_ResolvedEnvStateRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("DATABASE_URL", "postgres://dev@localhost:5432/app")
	preStop := int32(10)
	e := &KCLEntities{
		Workloads: []WorkloadEntity{
			{Name: "api", Spec: deployv1alpha1.WorkloadSpec{PreStopSeconds: &preStop}, Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{ListenPorts: &[]int{18090}}}},
			{Name: "worker", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{}}},
		},
		Frontends: []FrontendEntity{{Name: "web", Port: 3000}},
	}
	var out string
	out = captureStdout(t, func() { persistResolvedEnv("projC", "dev", e, nil, nil, true) })
	if out != "" {
		t.Fatalf("a successful persist is silent: %q", out)
	}
	got := loadResolvedEnv("projC", "dev")
	if got == nil {
		t.Fatal("state was not written")
	}
	if got.Env != "dev" || got.DatabaseURL != "postgres://dev@localhost:5432/app" {
		t.Errorf("state = %+v", got)
	}
	if got.Ports["api"] != 18090 || got.Ports["web"] != 3000 || len(got.Ports) != 2 {
		t.Errorf("ports = %v; want only the services that have one", got.Ports)
	}
	if got.StopGraceSeconds != 13 {
		t.Errorf("stop grace = %d; want the longest preStop (10s) plus the 3s margin", got.StopGraceSeconds)
	}
	if _, err := time.Parse(time.RFC3339, got.UpdatedAt); err != nil {
		t.Errorf("UpdatedAt = %q", got.UpdatedAt)
	}
	if loadResolvedEnv("projC", "staging") != nil {
		t.Error("an env that never ran has no state")
	}
	if loadResolvedEnv("", "dev") != nil {
		t.Error("no project id, no state")
	}
	path, _ := upEnvStatePath("projC", "dev")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if loadResolvedEnv("projC", "dev") != nil {
		t.Error("a corrupt cache must degrade to nil, never fail a read-only status command")
	}
	out = captureStdout(t, func() { persistResolvedEnv("", "dev", e, nil, nil, true) })
	if !strings.Contains(out, "warning: resolve env state path") {
		t.Errorf("a persist with no project id warns instead of failing: %q", out)
	}
}

func TestCovC_HostStopGrace(t *testing.T) {
	long, huge := int32(12), int32(600)
	mk := func(secs *int32) WorkloadEntity {
		return WorkloadEntity{Name: "w", Spec: deployv1alpha1.WorkloadSpec{PreStopSeconds: secs}, Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{}}}
	}
	if got := hostStopGrace(nil); got != 0 {
		t.Errorf("nil = %s", got)
	}
	if got := hostStopGrace(&KCLEntities{Workloads: []WorkloadEntity{mk(nil)}}); got != 0 {
		t.Errorf("no preStop = %s; the default grace applies", got)
	}
	if got := hostStopGrace(&KCLEntities{Workloads: []WorkloadEntity{mk(nil), mk(&long)}}); got != 15*time.Second {
		t.Errorf("= %s; want longest + 3s margin", got)
	}
	if got := hostStopGrace(&KCLEntities{Workloads: []WorkloadEntity{mk(&huge)}}); got != maxStopGrace {
		t.Errorf("= %s; want it capped at %s", got, maxStopGrace)
	}
}

func TestCovC_OverlayResolvedPorts(t *testing.T) {
	rows := []upServiceRow{
		{Name: "api", Port: 8080, URL: "http://localhost:8080"},
		{Name: "web", Port: 3000, URL: "http://localhost:3000"},
		{Name: "worker"},
	}
	overlayResolvedPorts(rows, map[string]int{"api": 51234, "worker": 0, "ghost": 9})
	if rows[0].Port != 51234 || rows[0].URL != "http://localhost:51234" {
		t.Errorf("api keeps the LIVE ephemeral port, not the render value: %+v", rows[0])
	}
	if rows[1].Port != 3000 || rows[1].URL != "http://localhost:3000" {
		t.Errorf("a row with no persisted port keeps its render value: %+v", rows[1])
	}
	if rows[2].Port != 0 || rows[2].URL != "" {
		t.Errorf("a persisted zero is not a port: %+v", rows[2])
	}
}

func TestCovC_BuildFrontendDevCmd(t *testing.T) {
	proj := t.TempDir()
	fe := FrontendEntity{Name: "web", Type: "nextjs", Path: proj, Port: 3100, EnvVars: []KCLEnvVar{{Name: "NEXT_PUBLIC_X", Value: "1"}, {Name: "FROM_SECRET", SecretRef: "tok"}}}
	fl := frontendLaunch{
		entities: &KCLEntities{Frontends: []FrontendEntity{fe}}, env: "dev", projectDir: proj,
		secrets: map[string]string{"tok": "s3cret", "unrelated": "nope"},
	}
	cmd := buildFrontendDevCmd(context.Background(), fl, fe, []string{"PATH=/bin"})
	env := strings.Join(cmd.Env, "\n")
	for _, want := range []string{"PORT=3100", "NEXT_PUBLIC_X=1"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %q:\n%s", want, env)
		}
	}
	if strings.Contains(env, "unrelated") || strings.Contains(env, "nope") {
		t.Errorf("a frontend must receive only the secrets it declares:\n%s", env)
	}
	if cmd.Dir != proj {
		t.Errorf("dir = %q", cmd.Dir)
	}
}

func TestCovC_UpFrontendsCountsFailuresPerFrontend(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "good")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	e := &KCLEntities{Frontends: []FrontendEntity{
		{Name: "skipped", Path: good}, // not in the target set
		{Name: "web", Path: filepath.Join(root, "missing-dir")},
	}}
	procs := newProcRegistry("projC", root, "dev")
	var failures int
	out := captureStdout(t, func() {
		failures = upFrontends(context.Background(), frontendLaunch{
			entities: e, env: "dev", noInstall: true, targets: []string{"web"}, procs: procs, projectDir: root,
		})
	})
	if failures != 1 {
		t.Fatalf("failures = %d; a frontend whose dev server cannot start counts once and does not stop the rest\n%s", failures, out)
	}
	if !strings.Contains(out, "[up] frontend web:") || strings.Contains(out, "skipped") {
		t.Errorf("out = %q", out)
	}
	if procs.count() != 0 {
		t.Errorf("a failed start must not register a process")
	}
}

func TestCovC_EnsureFrontendDepsAndPreflight(t *testing.T) {
	ctx := context.Background()
	if err := ensureFrontendDeps(ctx, FrontendEntity{Name: "web", Path: t.TempDir()}, true); err != nil {
		t.Errorf("--no-install: %v", err)
	}
	t.Setenv("FORGE_SKIP_NPM_INSTALL", "1")
	if err := ensureFrontendDeps(ctx, FrontendEntity{Name: "web", Path: t.TempDir()}, false); err != nil {
		t.Errorf("skip env: %v", err)
	}
	t.Run("a stale frontend whose install fails stops the preflight and names it", func(t *testing.T) {
		t.Setenv("FORGE_SKIP_NPM_INSTALL", "")
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		covCInstallTools(t, map[string]string{"npm": `echo "npm ERR! network" >&2; exit 1`})
		var err error
		out := captureStdout(t, func() {
			err = preflightFrontendDeps(ctx, &KCLEntities{Frontends: []FrontendEntity{{Name: "web", Path: dir}}}, nil)
		})
		if err == nil || !strings.Contains(err.Error(), "frontend web:") {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(out, "frontend dependency phase") {
			t.Errorf("out = %q", out)
		}
		// A different target selection leaves it alone.
		if err := preflightFrontendDeps(ctx, &KCLEntities{Frontends: []FrontendEntity{{Name: "web", Path: dir}}}, []string{"other"}); err != nil {
			t.Errorf("unselected frontend: %v", err)
		}
	})
}
