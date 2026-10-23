//go:build e2e

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestE2EGeneratedStoreSeam is the acceptance gate for the generated store:
// internal/db/store_gen.go gives the ORM's free functions an INTERFACE shape
// (db.<Entity>Store per entity, plus an aggregate db.Store), so a domain
// package can name persistence as a Deps field.
//
// ── Why this is an e2e test and not three unit tests ──────────────────────
//
// The feature is only worth anything if the WHOLE path holds, and each unit
// of it passed while the path was broken:
//
//   - The generator emitted the type, and a unit test proved it. Writing a
//     service against it then showed every method still demanded an
//     orm.Context, so a consumer needed a SECOND dependency to call the
//     first and still hand-wrote the passthrough the store exists to delete.
//   - Binding the handle fixed that, and the file still compiled. But
//     composition had no provider for the type, so `forge generate` told the
//     author to add an Infra field and construct "your implementation" —
//     the same hand-written adapter, reached by a different route.
//
// Both gaps were invisible to a test that stopped at "the generated code
// compiles". So this test walks the path a user walks — project new →
// entity → scaffold → declare the dep → generate → build → boot → call —
// and ends on a real row read back over HTTP. Two measured forge-one-shot
// runs hand-wrote 723 and 464 lines of that adapter layer; this is the gate
// that keeps the third from having to.
func TestE2EGeneratedStoreSeam(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "storeapp", "--mod", "example.com/storeapp", "--service", "widgets")
	projectDir := filepath.Join(dir, "storeapp")
	addCorpusForgePkgReplace(t, projectDir)

	// One entity is enough: the seam is per-entity, and a second would only
	// re-test the loop.
	protoPath := filepath.Join(projectDir, "proto", "services", "widgets", "v1", "widgets.proto")
	proto := readFileE2E(t, protoPath)
	proto += `
// forge:entity
message Widget {
  string id = 1;
  string name = 2;
}
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author widgets proto: %v", err)
	}

	runCmd(t, projectDir, forgeBin, "scaffold")

	// ── 1. The store exists, and its methods take NO handle ──────────────
	//
	// The no-handle property is the one that makes it usable as a Deps
	// field. Asserting the type exists would have passed the broken cut.
	storeGen := readFileE2E(t, filepath.Join(projectDir, "internal", "db", "store_gen.go"))
	if !strings.Contains(storeGen, "type WidgetStore interface") {
		t.Fatalf("expected a per-entity WidgetStore interface; store_gen.go:\n%s", storeGen)
	}
	ifaceStart := strings.Index(storeGen, "type WidgetStore interface {")
	iface := storeGen[ifaceStart : ifaceStart+strings.Index(storeGen[ifaceStart:], "\n}")]
	if strings.Contains(iface, "db orm.Context") {
		t.Errorf("no store METHOD may take an orm.Context — a consumer would then need the handle as a "+
			"second dep and would write the very passthrough this seam removes:\n%s", iface)
	}
	if !strings.Contains(iface, "WithTx(tx orm.Context) WidgetStore") {
		t.Errorf("binding the handle must not cost the transaction seam; WidgetStore needs WithTx:\n%s", iface)
	}
	if !strings.Contains(storeGen, "Widgets() WidgetStore") {
		t.Errorf("the aggregate Store must expose a per-entity accessor; store_gen.go:\n%s", storeGen)
	}

	// ── 2. A package DECLARING the dep resolves with no hand-wiring ──────
	//
	// This is the half that composition owns. The package is born by the
	// real command, and the only edit is the Deps field a user would add.
	runCmd(t, projectDir, forgeBin, "scaffold", "package", "pricing")

	svcPath := filepath.Join(projectDir, "internal", "pricing", "service.go")
	svc := readFileE2E(t, svcPath)
	svc = strings.Replace(svc, "type Deps struct {",
		"type Deps struct {\n\t// The generated store: declared, never constructed.\n\tWidgets db.WidgetStore\n\tAll     db.Store\n", 1)
	svc = strings.Replace(svc, "import (", "import (\n\t\"example.com/storeapp/internal/db\"", 1)
	if err := os.WriteFile(svcPath, []byte(svc), 0o644); err != nil {
		t.Fatalf("declare store deps on internal/pricing: %v", err)
	}

	// generate MUST NOT report "has no provider" here. That error is the
	// exact failure this seam removes, and it is a hard generate failure,
	// so runCmd's non-zero exit is the assertion.
	runCmd(t, projectDir, forgeBin, "generate")

	compose := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "compose.go"))
	for _, want := range []string{
		"db.NewStore(g.ORM()).Widgets()",
		"db.NewStore(g.ORM())",
		`"example.com/storeapp/internal/db"`,
	} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose.go must wire the store from the graph's ORM node, missing %q:\n%s", want, compose)
		}
	}

	// ── 3. It all builds ─────────────────────────────────────────────────
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")

	// ── 4. And a real row round-trips over HTTP ──────────────────────────
	bootStoreSeamRoundTrip(t, projectDir)
}

// bootStoreSeamRoundTrip boots the built server against a real postgres with
// the project's own migrations applied, then drives CreateWidget and
// ListWidgets over real HTTP with a valid bearer token.
//
// The read-back is the point. A 200 from Create proves the write path
// answered; only reading the row out again proves it was PERSISTED through
// the generated ORM the store wraps — which is the claim the whole seam
// rests on.
func bootStoreSeamRoundTrip(t *testing.T, projectDir string) {
	t.Helper()
	port := freePortE2E(t)

	serverBin := filepath.Join(projectDir, "store-seam-server")
	buildCorpusServer(t, projectDir, serverBin)

	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision store-seam postgres: %v", err)
	}
	defer cleanup()
	applyProjectMigrationsPostgres(t, projectDir, dsn)

	pubPEM, bearer := mintDevJWT(t)

	cmd := exec.Command(serverBin, "server")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		"DATABASE_URL="+dsn,
		"ENVIRONMENT=development",
		// Real JWT validation — the scaffolded CRUD RPCs are auth-gated, so
		// the calls below carry a real token rather than relying on a bypass.
		"JWT_SECRET="+pubPEM,
	)
	var serverOut strings.Builder
	cmd.Stdout = &serverOut
	cmd.Stderr = &serverOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start store-seam server: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
		}
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitForServing(t, base, 30*time.Second) {
		t.Fatalf("store-seam server did not become ready\nserver output:\n%s", serverOut.String())
	}

	post := func(procedure, body string) []byte {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, base+"/services.widgets.v1.WidgetsService/"+procedure, strings.NewReader(body))
		if err != nil {
			t.Fatalf("build %s request: %v", procedure, err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", bearer) // mintDevJWT already carries the "Bearer " prefix
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", procedure, err)
		}
		defer resp.Body.Close()
		out, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			t.Fatalf("read %s response: %v", procedure, rerr)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s = HTTP %d, want 200\nresponse: %s\nserver output:\n%s",
				procedure, resp.StatusCode, out, serverOut.String())
		}
		return out
	}

	created := post("CreateWidget", `{"name":"store-seam-proof"}`)
	var createResp struct {
		Widget struct {
			Id   string `json:"id"`
			Name string `json:"name"`
		} `json:"widget"`
	}
	if err := json.Unmarshal(created, &createResp); err != nil {
		t.Fatalf("parse CreateWidget response %q: %v", created, err)
	}
	if createResp.Widget.Id == "" || createResp.Widget.Name != "store-seam-proof" {
		t.Fatalf("CreateWidget did not return the created row: %s", created)
	}

	// Read it back. This is what a 200 alone cannot tell you.
	listed := post("ListWidgets", `{"pageSize":10}`)
	if !strings.Contains(string(listed), createResp.Widget.Id) {
		t.Errorf("the row created above did not come back from ListWidgets — it was answered but not persisted.\n"+
			"created id: %s\nlist response: %s\nserver output:\n%s",
			createResp.Widget.Id, listed, serverOut.String())
	}
	if !strings.Contains(string(listed), "store-seam-proof") {
		t.Errorf("ListWidgets did not return the created row's data: %s", listed)
	}
}
