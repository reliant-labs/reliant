//go:build e2e

package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestE2EUnmountedServiceIsFailClosed pins what happens to a service whose
// proto generates and whose handler dir exists, but which a given process
// does not mount — asked of the composition graph a new project composes
// with:
//
//	A. Its typed mount IS generated (MountOptions.Mount<Svc>) and its
//	   audience's route set (UserRoutes) takes it as a Deps field — being
//	   unmounted is a composition choice, never a gap in what forge emitted.
//	B. Inventory lists it, so `forge project map` / `forge project audit`
//	   still see it. Introspection is data-only and independent of mounting.
//	C. COMPLETE BY CONSTRUCTION: the default `server` root's API listener
//	   serves UserRoutes, which mounts every user-audience service — there
//	   is no list to forget a service from. (A service reachable from no
//	   root at all is `forge lint --graph`'s finding, composition graph PR 5;
//	   the boot-time RequireComplete gate belonged to the retired ServeSpec.)
//	D. A deliberate subset — an owned value expression in compose.go that
//	   hands the API listener a route set of its own — boots, serves ONLY
//	   the chosen service, and never constructs the other one: a node no
//	   root reaches is not built.
func TestE2EUnmountedServiceIsFailClosed(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin,
		"project", "new", "tonly",
		"--mod", "github.com/test/tonly",
		"--service", "api",
	)
	projectDir := filepath.Join(dir, "tonly")
	assertPathExistsE2E(t, filepath.Join(projectDir, "forge.yaml"))

	// The scaffolded api service has no RPCs, and a zero-RPC service mounts
	// no routes at all — which would make "not mounted" and "mounted but
	// empty" indistinguishable over HTTP in phase D. Give it one RPC so the
	// served/not-served probe has something real to hit.
	apiProtoPath := filepath.Join(projectDir, "proto", "services", "api", "v1", "api.proto")
	apiProto := readFileE2E(t, apiProtoPath)
	const rpcTODO = "  // TODO: Add your RPC methods here.\n"
	if !strings.Contains(apiProto, rpcTODO) {
		t.Fatalf("scaffolded api.proto no longer carries the RPC TODO anchor:\n%s", apiProto)
	}
	// NO_SIDE_EFFECTS: Ping reads, and an rpc that is neither a read verb
	// nor marked so must declare an activity event (forgeconv-activity-declared).
	apiProto = strings.Replace(apiProto, rpcTODO, "  rpc Ping(PingRequest) returns (PingResponse) {\n    option idempotency_level = NO_SIDE_EFFECTS;\n  }\n", 1)
	apiProto += "\nmessage PingRequest {}\n\nmessage PingResponse {\n  string ok = 1;\n}\n"
	if err := os.WriteFile(apiProtoPath, []byte(apiProto), 0o644); err != nil {
		t.Fatalf("write api.proto: %v", err)
	}

	// Declare a SECOND service. Its canonical implementation lives in a
	// sibling binary (the control-plane shape); this project generates its
	// types and handler dir but a carved binary may well not serve it.
	protoDir := filepath.Join(projectDir, "proto", "services", "project", "v1")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatalf("mkdir proto dir: %v", err)
	}
	const projectProto = `syntax = "proto3";

package services.project.v1;

option go_package = "github.com/test/tonly/gen/services/project/v1;projectv1";

// ProjectService is canonically served by a sibling binary; this project
// generates its types and its typed mount, and composition decides whether a
// given binary actually mounts it.
service ProjectService {
  rpc GetProject(GetProjectRequest) returns (GetProjectResponse) {}
}

message GetProjectRequest {
  string id = 1;
}

message GetProjectResponse {
  string id = 1;
  string name = 2;
}
`
	if err := os.WriteFile(filepath.Join(protoDir, "project.proto"), []byte(projectProto), 0o644); err != nil {
		t.Fatalf("write project.proto: %v", err)
	}

	// Wire the unpublished forge/pkg to local sources, same as the
	// fixture-corpus harness (serverkit's completeness gate — the thing
	// under test — is newer than any published snapshot).
	addCorpusForgePkgReplace(t, projectDir)

	runCmd(t, projectDir, forgeBin, "generate")

	// ── A. The typed mount surface is generated for BOTH services ───────
	//
	// Not being mounted is a composition decision. Forge still emits
	// everything needed to mount it, so turning it on is a one-line change
	// and never a regeneration.
	assertPathExistsE2E(t, filepath.Join(projectDir, "gen", "services", "project", "v1"))
	assertPathExistsE2E(t, filepath.Join(projectDir, "internal", "handlers", "project"))

	mounts := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "routes_gen.go"))
	for _, method := range []string{
		"func (o MountOptions) MountProject(",
		"func (o MountOptions) MountAPI(",
		"func (r *UserRoutes) Register(",
	} {
		if !strings.Contains(mounts, method) {
			t.Errorf("routes_gen.go must generate %q:\n%s", method, mounts)
		}
	}
	if !regexp.MustCompile(`(?s)type UserRoutesDeps struct \{[^}]*Project +\*project\.Service`).MatchString(mounts) {
		t.Errorf("UserRoutes must take the project service as a Deps field:\n%s", mounts)
	}

	// ── B. Inventory lists it — introspection is independent of mounting ─
	//
	// Inventory is DATA-ONLY (`forge project map` / `audit` / the services
	// listing read it; the run path never does), so a service that no binary
	// mounts is still fully discoverable.
	for _, row := range []string{
		`Name:        "project"`,
		"ConnectPath: projectv1connect.ProjectServiceName",
	} {
		if !strings.Contains(mounts, row) {
			t.Errorf("Inventory must carry the project row %q:\n%s", row, mounts)
		}
	}
	assertAuditSeesService(t, projectDir, forgeBin, "project", "GetProject")

	runCmd(t, projectDir, "go", "build", "./...")

	// One postgres for both boots below: the scaffolded config declares
	// database_url REQUIRED and the DB provider pings it, so "no database" is not
	// a bootable configuration and neither phase could run without it.
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision boot postgres: %v", err)
	}
	defer cleanup()

	// ── C. The default root mounts every user-audience service ──────────
	fullBin := filepath.Join(projectDir, "tonly-full")
	runCmd(t, projectDir, "go", "build", "-o", fullBin, "./cmd/tonly")
	assertServesRoutes(t, fullBin, projectDir, dsn,
		[]string{"/services.api.v1.APIService/Ping", "/services.project.v1.ProjectService/GetProject"}, nil)

	// ── D. A deliberate subset, as owned value expressions ──────────────
	//
	// The API listener takes any serverkit.Routes, and compose.go hands it
	// one that mounts only the api service. forge keeps both edits (they
	// are the author's), and the project service — reached by nothing now —
	// is never constructed.
	mustReplaceInFile(t, filepath.Join(projectDir, "internal", "app", "providers.go"),
		"\tRoutes    *UserRoutes\n", "\tRoutes    serverkit.Routes\n")
	composePath := filepath.Join(projectDir, "internal", "app", "compose.go")
	compose := readFileE2E(t, composePath)
	routesKey := regexp.MustCompile(`Routes: +g\.UserRoutes\(\),`)
	if !routesKey.MatchString(compose) {
		t.Fatalf("compose.go's newAPIListener no longer hands the listener g.UserRoutes():\n%s", compose)
	}
	compose = routesKey.ReplaceAllString(compose, `Routes: serverkit.RoutesFunc(func(mux *http.ServeMux, opts ...connect.HandlerOption) []string {
			return MountOptions{}.MountAPI(mux, g.API(), g.Log, opts...)
		}),`)
	for _, imp := range []string{`"net/http"`, `"connectrpc.com/connect"`, `"github.com/reliant-labs/forge/pkg/serverkit"`} {
		if !strings.Contains(compose, imp) {
			compose = strings.Replace(compose, "import (\n", "import (\n\t"+imp+"\n", 1)
		}
	}
	if err := os.WriteFile(composePath, []byte(compose), 0o644); err != nil {
		t.Fatalf("write carved compose.go: %v", err)
	}
	runCmd(t, projectDir, forgeBin, "generate")
	if got := readFileE2E(t, composePath); !strings.Contains(got, "MountOptions{}.MountAPI(mux, g.API(), g.Log, opts...)") {
		t.Fatalf("generate rewrote the owned value expression:\n%s", got)
	}
	subsetBin := filepath.Join(projectDir, "tonly-subset")
	runCmd(t, projectDir, "go", "build", "-o", subsetBin, "./cmd/tonly")
	assertServesRoutes(t, subsetBin, projectDir, dsn,
		[]string{"/services.api.v1.APIService/Ping"},
		[]string{"/services.project.v1.ProjectService/GetProject"})
}

// assertAuditSeesService asserts that `forge project audit --json` reports the
// named service and one of its RPCs under the shape category. Mounting is a
// composition decision made in Go source; the audit reads the proto surface
// and the data-only Inventory, so a service no binary mounts must still be
// fully visible to introspection.
func assertAuditSeesService(t *testing.T, projectDir, forgeBin, serviceName, rpcName string) {
	t.Helper()
	out := runCmdStdout(t, projectDir, forgeBin, "project", "audit", "--json")
	var report struct {
		Categories map[string]struct {
			Details struct {
				Services []struct {
					Name string `json:"name"`
					RPCs []struct {
						Name string `json:"name"`
					} `json:"rpcs"`
				} `json:"services"`
			} `json:"details"`
		} `json:"categories"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("parse audit JSON: %v\n%s", err, out)
	}

	for _, svc := range report.Categories["shape"].Details.Services {
		if svc.Name != serviceName {
			continue
		}
		for _, rpc := range svc.RPCs {
			if rpc.Name == rpcName {
				return
			}
		}
		t.Fatalf("audit shape service %q is missing rpc %q: %+v", serviceName, rpcName, svc)
	}
	t.Fatalf("audit shape must keep the unmounted service %q discoverable:\n%s", serviceName, out)
}

// assertServesRoutes boots serverBin's `server`, then asserts it is
// healthy, that every served path is registered, and that every notServed
// path is not.
//
// The status code is the discriminator, and the distinction is sharp: a
// registered Connect route runs the auth interceptor and rejects the
// unauthenticated probe with 401, while an unregistered one never reaches a
// handler and the mux answers 404. Asserting 401 (not 2xx) also keeps the
// test honest about auth — nothing here disables authentication.
func assertServesRoutes(t *testing.T, serverBin, projectDir, dsn string, served, notServed []string) {
	t.Helper()
	port := freePortE2E(t)

	cmd := exec.Command(serverBin, "server")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		"DATABASE_URL="+dsn,
		"ENVIRONMENT=development",
	)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	killed := false
	defer func() {
		if !killed {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitForServing(t, base, 30*time.Second) {
		t.Fatalf("server never became ready\noutput:\n%s", out.String())
	}
	for _, p := range served {
		if code := postStatus(t, base+p); code != http.StatusUnauthorized {
			t.Errorf("route %s = %d, want 401 (route registered, auth interceptor rejects)\noutput:\n%s", p, code, out.String())
		}
	}
	for _, p := range notServed {
		if code := postStatus(t, base+p); code != http.StatusNotFound {
			t.Errorf("route %s = %d, want 404 (never registered on the mux)\noutput:\n%s", p, code, out.String())
		}
	}

	// The process must still shut down cleanly.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		killed = true
		if err != nil {
			t.Fatalf("server did not shut down cleanly on SIGTERM: %v\noutput:\n%s", err, out.String())
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		killed = true
		t.Fatalf("server did not exit within 30s of SIGTERM\noutput:\n%s", out.String())
	}
}

// postStatus POSTs an empty Connect/JSON body and returns the status code.
func postStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
