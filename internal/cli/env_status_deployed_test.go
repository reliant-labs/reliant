package cli

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
)

// `forge env status prod` in a clean release worktree reported the DEV stack's
// Vite (pid 20897, holding :3000) as prod's reliant-web, inside a
// `forge env up · prod` box. prod runs nothing on this machine and its
// reliant-web is Firebase Hosting. A cloud env's status must probe no local
// port, print no `env up` frame, and name the frontend's deployed URL.
func TestEnvStatus_CloudEnvProbesNoLocalPort(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	// Another env's dev server, holding the port prod's frontend declares.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	dir := t.TempDir()
	writeForgeYAML(t, dir, "name: demo\nmodule_path: github.com/example/demo\n")
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, fmt.Sprintf(`{
  "output": {
    "frontends": [
      {"name": "reliant-web", "path": "web", "port": %d,
       "runtime": {"type": "firebase", "project": "acme", "site": "acme-prod"}}
    ],
    "workloads": [
      {"name": "api", "kind": "service",
       "runtime": {"type": "cluster", "cluster": "gke_acme_us-central1_prod"},
       "spec": {"kind": "service"}}
    ]
  }
}`, port)))
	t.Chdir(dir)

	out := captureStdout(t, func() {
		if err := renderRuntimeStatus(context.Background(), "prod", "", false); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if strings.Contains(out, fmt.Sprintf("localhost:%d", port)) || strings.Contains(out, "forge env up ·") {
		t.Errorf("a cloud env's status probed localhost / printed an env-up frame:\n%s", out)
	}
	if !strings.Contains(out, "https://acme-prod.web.app") {
		t.Errorf("the shipped frontend is not named at its deployed URL:\n%s", out)
	}
}
