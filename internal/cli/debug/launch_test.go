package debug

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/factory"
)

// TestResolveShortFuncName pins the handler package path: handlers live
// under <module>/internal/handlers/<svc>, and a short name must resolve to
// that import path or Delve finds no such function.
func TestResolveShortFuncName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "internal", "handlers", "tasks", "rpc_create_task.go"),
		"package tasks\n\nfunc (s *Service) CreateTask(ctx context.Context) error { return nil }\n")
	withWD(t, dir, func() {
		got := resolveShortFuncName("example.com/app", "CreateTask")
		if want := "example.com/app/internal/handlers/tasks.(*Service).CreateTask"; got != want {
			t.Fatalf("resolveShortFuncName = %q, want %q", got, want)
		}
		if got := resolveShortFuncName("example.com/app", "Missing"); got != "" {
			t.Fatalf("unknown name resolved to %q", got)
		}
	})
}

// TestParseLineSpec pins the spellings a file:line breakpoint is tried
// under: absolute (a binary built without -trimpath), module-relative (with
// -trimpath, as air builds under GOFLAGS), then the spec as typed for
// Delve's suffix match.
func TestParseLineSpec(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "proj")
	cases := []struct {
		spec      string
		wantFiles []string
		wantLoc   string
		wantLine  int
	}{
		{
			spec:      "internal/handlers/tasks/rpc_create.go:42",
			wantFiles: []string{filepath.Join(root, "internal", "handlers", "tasks", "rpc_create.go"), "example.com/app/internal/handlers/tasks/rpc_create.go"},
			wantLoc:   "internal/handlers/tasks/rpc_create.go",
			wantLine:  42,
		},
		{
			// Outside the project: no module-relative spelling.
			spec:      filepath.Join(string(filepath.Separator), "elsewhere", "x.go") + ":7",
			wantFiles: []string{filepath.Join(string(filepath.Separator), "elsewhere", "x.go")},
			wantLoc:   filepath.ToSlash(filepath.Join(string(filepath.Separator), "elsewhere", "x.go")),
			wantLine:  7,
		},
	}
	for _, tc := range cases {
		got, err := parseLineSpec(tc.spec, root, "example.com/app")
		if err != nil {
			t.Fatalf("parseLineSpec(%q): %v", tc.spec, err)
		}
		if !slices.Equal(got.files, tc.wantFiles) || got.locExpr != tc.wantLoc || got.line != tc.wantLine {
			t.Fatalf("parseLineSpec(%q) = %+v, want files=%v loc=%q line=%d", tc.spec, got, tc.wantFiles, tc.wantLoc, tc.wantLine)
		}
	}
	if _, err := parseLineSpec("handler.go:abc", root, ""); err == nil {
		t.Fatal("non-numeric line accepted")
	}
}

func TestDefaultWorkload(t *testing.T) {
	names := []string{"api", "Worker", "web"}
	cases := []struct {
		service string
		isPath  bool
		want    string
	}{
		{"worker", false, "Worker"}, // a workload named after the service
		{"tasks", false, "api"},     // a proto service runs in the api server
		{"worker", true, "api"},     // a path only takes the api server's config
	}
	for _, tc := range cases {
		if got := defaultWorkload(names, tc.service, tc.isPath); got != tc.want {
			t.Errorf("defaultWorkload(%q, path=%v) = %q, want %q", tc.service, tc.isPath, got, tc.want)
		}
	}
	if got := defaultWorkload([]string{"web"}, "tasks", false); got != "" {
		t.Errorf("no candidate should give \"\", got %q", got)
	}
}

func TestLayerEnv(t *testing.T) {
	base := []string{"DATABASE_URL=postgres://x", "PORT=8080", "OTEL_SERVICE_NAME=declared"}
	got := layerEnv(base, []string{"OTEL_SERVICE_NAME=api", "PORT=18080"})
	if v := envValue(got, "PORT"); v != "18080" {
		t.Fatalf("--app-port must override the configured PORT, got %q", v)
	}
	if v := envValue(got, "OTEL_SERVICE_NAME"); v != "declared" {
		t.Fatalf("a declared OTEL_SERVICE_NAME must be kept, got %q", v)
	}
	if v := envValue(got, "DATABASE_URL"); v != "postgres://x" {
		t.Fatalf("resolved config lost: DATABASE_URL=%q", v)
	}
	if v := envValue(layerEnv([]string{"A=1"}, []string{"OTEL_SERVICE_NAME=api"}), "OTEL_SERVICE_NAME"); v != "api" {
		t.Fatalf("OTEL_SERVICE_NAME should fill a gap, got %q", v)
	}
}

// TestLaunchSpecPort pins which PORT a launched service binds. --app-port
// sets it for any binary, a plain main package included. Without it, --port
// still sets a cobra service's PORT, as it did before --app-port existed;
// with neither, the configured PORT stands.
func TestLaunchSpecPort(t *testing.T) {
	dir := t.TempDir()
	writeMain(t, filepath.Join(dir, "cmd", "app", "main.go"))
	writeFile(t, filepath.Join(dir, "cmd", "app", "cmd", "server.go"), "package cmd\n\nvar c = cobra.Command{\n\tUse: \"server\",\n}\n")
	writeMain(t, filepath.Join(dir, "cmd", "standalone", "main.go"))
	withWD(t, dir, func() {
		cases := []struct {
			name, buildPath string
			o               startOptions
			want            string
		}{
			{"neither", "./cmd/app", startOptions{}, ""},
			{"app-port", "./cmd/app", startOptions{port: 2345, appPort: 18080}, "18080"},
			{"legacy --port", "./cmd/app", startOptions{port: 2345}, "2345"},
			{"app-port on a plain main", "./cmd/standalone", startOptions{appPort: 18080}, "18080"},
			{"--port on a plain main", "./cmd/standalone", startOptions{port: 2345}, ""},
		}
		for _, tc := range cases {
			_, env := launchSpec("api", tc.buildPath, tc.o)
			if got := envValue(env, "PORT"); got != tc.want {
				t.Errorf("%s: PORT = %q (env %v), want %q", tc.name, got, env, tc.want)
			}
		}
	})
}

func TestCheckAppPortFree(t *testing.T) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	defer l.Close()
	busy := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	err = checkAppPortFree([]string{"PORT=" + busy}, false)
	if err == nil || !strings.Contains(err.Error(), "--app-port") {
		t.Fatalf("busy configured PORT: err = %v, want advice naming --app-port", err)
	}
	if err := checkAppPortFree([]string{"HOME=/x"}, false); err != nil {
		t.Fatalf("no PORT must pass: %v", err)
	}
}

func TestNormalizeAddr(t *testing.T) {
	for in, want := range map[string]string{
		":2345":          "127.0.0.1:2345",
		"2345":           "127.0.0.1:2345",
		"127.0.0.1:2345": "127.0.0.1:2345",
		"[::1]:2345":     "[::1]:2345",
	} {
		if got := normalizeAddr(in); got != want {
			t.Errorf("normalizeAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestResolveLaunchEnv pins defect 4: a service launched by `start` gets
// its workload's configuration — the workload named after it, else api —
// and a failure to resolve it is an error naming --no-env, not a process
// that dies on boot.
func TestResolveLaunchEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "deploy", "kcl", "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	var gotWorkload string
	api := factory.DebugAPI{
		WorkloadNames: func(context.Context, string, string) ([]string, error) { return []string{"api", "web"}, nil },
		WorkloadEnv: func(_ context.Context, _, env, workload string, base []string) ([]string, error) {
			gotWorkload = workload
			if workload == "broken" {
				return nil, errors.New("secret db_password missing")
			}
			return append(base, "DATABASE_URL=postgres://"+env), nil
		},
	}
	withWD(t, dir, func() {
		env, workload, err := resolveLaunchEnv(context.Background(), api, startOptions{env: "dev"}, "tasks", false)
		if err != nil {
			t.Fatalf("resolveLaunchEnv: %v", err)
		}
		if workload != "api" || gotWorkload != "api" || envValue(env, "DATABASE_URL") != "postgres://dev" {
			t.Fatalf("got workload %q (asked %q), DATABASE_URL=%q", workload, gotWorkload, envValue(env, "DATABASE_URL"))
		}

		_, _, err = resolveLaunchEnv(context.Background(), api, startOptions{env: "dev", workload: "broken"}, "tasks", false)
		if err == nil || !strings.Contains(err.Error(), "--no-env") || !strings.Contains(err.Error(), "secret db_password missing") {
			t.Fatalf("resolution failure = %v, want the cause and --no-env", err)
		}

		if env, _, err := resolveLaunchEnv(context.Background(), api, startOptions{env: "dev", noEnv: true}, "tasks", false); err != nil || env != nil {
			t.Fatalf("--no-env = (%v, %v), want the shell environment", env, err)
		}

		// No such env: the default degrades to the shell env; an explicit
		// --env is an error.
		if env, _, err := resolveLaunchEnv(context.Background(), api, startOptions{env: "staging"}, "tasks", false); err != nil || env != nil {
			t.Fatalf("defaulted missing env = (%v, %v), want shell env", env, err)
		}
		if _, _, err := resolveLaunchEnv(context.Background(), api, startOptions{env: "staging", envSet: true}, "tasks", false); err == nil {
			t.Fatal("explicit --env with no deploy/kcl/<env> accepted")
		}
	})
}
