package hostinfra

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Host infra logs to the one directory every process of the env logs to,
// named for the workload — not beside its data, where the local collector
// never looked.
func TestLogPath_IsTheEnvLogDirectory(t *testing.T) {
	for _, tc := range []struct{ env, name, want string }{
		{"dev", "postgres", ".forge/logs/dev/postgres.log"},
		{"dev", "idp", ".forge/logs/dev/idp.log"},
		{"e2e", "events", ".forge/logs/e2e/events.log"},
		{"dev", "frontend:web", ".forge/logs/dev/frontend_web.log"},
		{"dev", "a/b", ".forge/logs/dev/a_b.log"},
	} {
		got := filepath.ToSlash(LogPath("", tc.env, tc.name))
		if got != tc.want {
			t.Errorf("LogPath(%q, %q) = %q, want %q", tc.env, tc.name, got, tc.want)
		}
	}
	if got, want := LogPath("/p", "dev", "nats"), filepath.Join("/p", ".forge", "logs", "dev", "nats.log"); got != want {
		t.Errorf("LogPath resolves against the project root: got %q, want %q", got, want)
	}
}

// The point of the move: the local ClickStack collector's own include glob
// and path regex (the scaffolded otel-collector.yaml) pick the host-infra log
// up, with service.name = the workload and deployment.environment.name = the
// env. Read from the template itself, so a change to either side fails here.
func TestLogPath_IsIngestedByTheLocalCollector(t *testing.T) {
	tmpl, err := os.ReadFile(filepath.Join("..", "templates", "project", "otel-collector.yaml.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`regex: '(\^/var/log/forge/[^']+)'`).FindSubmatch(tmpl)
	if m == nil {
		t.Fatal("otel-collector.yaml.tmpl no longer carries the filelog path regex this test reads")
	}
	pathRE := regexp.MustCompile(string(m[1]))
	if !strings.Contains(string(tmpl), "- /var/log/forge/*/*.log") {
		t.Fatal("otel-collector.yaml.tmpl no longer includes /var/log/forge/*/*.log")
	}
	compose, err := os.ReadFile(filepath.Join("..", "templates", "project", "docker-compose.yml.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "./.forge/logs:/var/log/forge") {
		t.Fatal("docker-compose.yml.tmpl no longer mounts .forge/logs at /var/log/forge")
	}

	project := t.TempDir()
	logs := filepath.Join(project, ".forge", "logs")
	for _, name := range []string{"postgres", "idp", "temporal", "nats", "mail"} {
		rel, err := filepath.Rel(logs, LogPath(project, "dev", name))
		if err != nil {
			t.Fatal(err)
		}
		inContainer := "/var/log/forge/" + filepath.ToSlash(rel)
		if ok, _ := filepath.Match("/var/log/forge/*/*.log", inContainer); !ok {
			t.Errorf("%s: %s is outside the collector's include glob", name, inContainer)
		}
		sub := pathRE.FindStringSubmatch(inContainer)
		if sub == nil {
			t.Errorf("%s: the collector's path regex does not parse %s", name, inContainer)
			continue
		}
		if got := sub[pathRE.SubexpIndex("service")]; got != name {
			t.Errorf("%s: service.name would be %q", name, got)
		}
		if got := sub[pathRE.SubexpIndex("env")]; got != "dev" {
			t.Errorf("%s: deployment.environment.name would be %q", name, got)
		}
	}
}

// symlinksWork skips a test on a host that cannot make symlinks (Windows
// without the privilege), where the legacy path is best-effort by design.
func symlinksWork(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink("x", filepath.Join(dir, "probe")); err != nil {
		t.Skipf("this host cannot create symlinks (%v); the legacy log path is best-effort there", err)
	}
}

func writeAndClose(t *testing.T, f *os.File, s string) {
	t.Helper()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func TestOpenLog_WritesTheEnvLogAndKeepsTheLegacyPathReadable(t *testing.T) {
	symlinksWork(t)
	project := t.TempDir()
	spec := Spec{Name: "events", Env: "dev", Engine: EngineNATS}
	if err := os.MkdirAll(spec.dataDir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	f, p, err := spec.openLog(project, "nats.log", os.O_TRUNC)
	if err != nil {
		t.Fatal(err)
	}
	if want := LogPath(project, "dev", "events"); p != want {
		t.Fatalf("log path = %s, want %s", p, want)
	}
	writeAndClose(t, f, "server is ready\n")

	legacy := filepath.Join(project, ".forge", "hostinfra", "events", "nats.log")
	if got := readFile(t, legacy); got != "server is ready\n" {
		t.Errorf("the legacy path reads %q, want the live log", got)
	}
	target, err := os.Readlink(legacy)
	if err != nil {
		t.Fatalf("the legacy path should be a symlink: %v", err)
	}
	if filepath.IsAbs(target) {
		t.Errorf("the legacy symlink is absolute (%s); relative survives moving the checkout", target)
	}

	// A second start keeps the link and truncates the log.
	f, _, err = spec.openLog(project, "nats.log", os.O_TRUNC)
	if err != nil {
		t.Fatal(err)
	}
	writeAndClose(t, f, "second run\n")
	if got := readFile(t, legacy); got != "second run\n" {
		t.Errorf("after a restart the legacy path reads %q", got)
	}
}

// A project an earlier forge ran has a regular postgres.log beside the data.
// Its history moves to the new path (postgres appends: the initdb transcript
// is written once), and the old path becomes the link.
func TestOpenLog_AdoptsTheLogAnEarlierForgeWrote(t *testing.T) {
	symlinksWork(t)
	project := t.TempDir()
	spec := Spec{Name: "postgres", Env: "dev", Engine: EnginePostgres}
	legacy := filepath.Join(spec.dataDir(project), "postgres.log")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("initdb transcript\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, p, err := spec.openLog(project, "postgres.log", os.O_APPEND)
	if err != nil {
		t.Fatal(err)
	}
	writeAndClose(t, f, "ready to accept connections\n")
	want := "initdb transcript\nready to accept connections\n"
	if got := readFile(t, p); got != want {
		t.Errorf("new log = %q, want the earlier history kept: %q", got, want)
	}
	if info, err := os.Lstat(legacy); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the legacy path should now be a symlink: %v, %v", info, err)
	}
	if got := readFile(t, legacy); got != want {
		t.Errorf("legacy path reads %q, want %q", got, want)
	}
}

// When both exist (an older forge ran after this one), the legacy regular file
// is the stale one: it is replaced by the link, never left to read as live.
func TestOpenLog_ReplacesAStaleLegacyFile(t *testing.T) {
	symlinksWork(t)
	project := t.TempDir()
	spec := Spec{Name: "idp", Env: "dev", Engine: EngineZitadel}
	legacy := filepath.Join(spec.dataDir(project), "zitadel.log")
	logPath := LogPath(project, "dev", "idp")
	for _, p := range []string{legacy, logPath} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("old "+filepath.Base(p)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f, _, err := spec.openLog(project, "zitadel.log", os.O_TRUNC)
	if err != nil {
		t.Fatal(err)
	}
	writeAndClose(t, f, "fresh\n")
	if got := readFile(t, legacy); got != "fresh\n" {
		t.Errorf("legacy path reads %q, want the live log", got)
	}
}

// A Spec with no environment (built by hand) keeps the log where it always was.
func TestOpenLog_WithoutAnEnvLogsBesideTheData(t *testing.T) {
	project := t.TempDir()
	spec := Spec{Name: "nats", Engine: EngineNATS}
	if err := os.MkdirAll(spec.dataDir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	f, p, err := spec.openLog(project, "nats.log", os.O_TRUNC)
	if err != nil {
		t.Fatal(err)
	}
	writeAndClose(t, f, "x\n")
	if want := filepath.Join(project, ".forge", "hostinfra", "nats", "nats.log"); p != want {
		t.Errorf("log path = %s, want %s", p, want)
	}
	if _, err := os.Stat(filepath.Join(project, ".forge", "logs")); !os.IsNotExist(err) {
		t.Errorf("no env, so nothing belongs under .forge/logs: %v", err)
	}
}

// The running postgres logs to the instance's file itself. Before, its stderr
// was the library's temp file, unlinked once the server was up.
func TestWithServerLog_PointsTheLoggingCollectorAtTheLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".forge", "logs", "dev", "postgres.log")
	params := withServerLog(postgresStartParameters(runtime.GOOS), p)
	for k, want := range map[string]string{
		"logging_collector": "on",
		"log_directory":     filepath.ToSlash(filepath.Dir(p)),
		"log_filename":      "postgres.log",
		"log_file_mode":     "0644",
		"log_rotation_age":  "0",
		"log_rotation_size": "0",
	} {
		if got := params[k]; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if strings.ContainsRune(params["log_filename"], '%') {
		t.Error("log_filename must be fixed: a strftime escape would rename the file the collector tails")
	}
	if got := withServerLog(map[string]string{}, ""); len(got) != 0 {
		t.Errorf("no log path adds nothing, got %v", got)
	}
}
