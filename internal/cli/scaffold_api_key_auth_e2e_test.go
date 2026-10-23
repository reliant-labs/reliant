//go:build e2e

package cli

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/apikey"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestE2EScaffoldAPIKeyAuth is the acceptance gate for issue #746: following
// the auth/api-keys skill in a scaffolded project gives a server that
// accepts "X-API-Key: fk_…" (and "Authorization: Bearer fk_…") checked
// against the project's OWN table, still accepts a bearer JWT, and rejects
// everything else on an auth_required rpc.
//
// Before the fix, three things stood in the way, and each one alone made
// the feature unreachable:
//
//   - the generated credentialFrom read only "Authorization: Bearer" and the
//     session cookie, so an X-API-Key header reached no validator;
//   - SetupAuth returned a bare func(token string), with no place for a key
//     validator, while the skill said to pass `Provider: "both"`;
//   - serve.go called SetupAuth ~110 lines before OpenInfra, so a key
//     validator had no database handle — the project that hit this opened a
//     second sql.Open pool inside SetupAuth.
//
// So the test walks the user's path — project new → entity → scaffold → own
// an api_keys table → fill in the APIKeyValidator seam over the serving pool →
// generate → build → boot → call — and ends on real HTTP. Nothing in it
// reaches past the seams the skill names.
func TestE2EScaffoldAPIKeyAuth(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "keyapp", "--mod", "example.com/keyapp", "--service", "widgets")
	projectDir := filepath.Join(dir, "keyapp")
	addCorpusForgePkgReplace(t, projectDir)

	// One entity, for an auth_required rpc to call.
	protoPath := filepath.Join(projectDir, "proto", "services", "widgets", "v1", "widgets.proto")
	proto := readFileE2E(t, protoPath) + `
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

	// ── 1. The scaffold carries the wiring ──────────────────────────────
	mw := readFileE2E(t, filepath.Join(projectDir, "pkg", "middleware", "middleware.go"))
	if !strings.Contains(mw, `const APIKeyHeader = "X-API-Key"`) || !strings.Contains(mw, "h.Get(APIKeyHeader)") {
		t.Fatalf("pkg/middleware/middleware.go must declare APIKeyHeader and read it in credentialFrom:\n%s", mw)
	}
	providers := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "providers.go"))
	if !strings.Contains(providers, "authenticator.UseKeyValidator(APIKeyValidator(d.DB))") {
		t.Fatalf("providers.go's UserAuth must install APIKeyValidator over the serving pool on the authenticator:\n%s", providers)
	}

	// The project's own policy tests: credential precedence, key + bearer
	// through the real interceptor.
	runCmd(t, projectDir, "go", "test", "-count=1", "./pkg/middleware/")

	// ── 2. Own the table and fill in the seam, as the skill says ────────
	runCmd(t, projectDir, forgeBin, "db", "migration", "new", "add_api_keys")
	migs, err := filepath.Glob(filepath.Join(projectDir, "db", "migrations", "*_add_api_keys.up.sql"))
	if err != nil || len(migs) != 1 {
		t.Fatalf("forge db migration new add_api_keys created %v (err %v), want one file", migs, err)
	}
	if err := os.WriteFile(migs[0], []byte(`CREATE TABLE api_keys (
    prefix     TEXT PRIMARY KEY,
    key_hash   TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    revoked_at TIMESTAMPTZ
);
`), 0o644); err != nil {
		t.Fatalf("write api_keys migration: %v", err)
	}

	authPath := filepath.Join(projectDir, "internal", "app", "auth.go")
	authGo := readFileE2E(t, authPath)
	const seam = "func APIKeyValidator(_ *DB) auth.KeyValidator {\n\treturn nil\n}"
	if !strings.Contains(authGo, seam) {
		t.Fatalf("internal/app/auth.go must scaffold the APIKeyValidator seam returning nil (keys disabled):\n%s", authGo)
	}
	// The validator the skill describes: one indexed read by lookup prefix
	// on the handlers' own pool, then a constant-time hash compare.
	authGo = strings.Replace(authGo, seam, `func APIKeyValidator(db *DB) auth.KeyValidator {
	return auth.KeyValidatorFunc(func(ctx context.Context, key string) (*auth.Claims, error) {
		prefix, err := apikey.LookupPrefix(key)
		if err != nil {
			return nil, err
		}
		var hash, userID string
		err = db.QueryRowContext(ctx,
			"SELECT key_hash, user_id FROM api_keys WHERE prefix = $1 AND revoked_at IS NULL",
			prefix).Scan(&hash, &userID)
		if err != nil {
			return nil, err
		}
		if !apikey.Verify(key, hash) {
			return nil, errors.New("key does not match")
		}
		return &auth.Claims{UserID: userID}, nil
	})
}`, 1)
	authGo = strings.Replace(authGo, "\t\"context\"\n", "\t\"context\"\n\t\"errors\"\n", 1)
	authGo = strings.Replace(authGo, "\t\"github.com/reliant-labs/forge/pkg/auth\"\n",
		"\t\"github.com/reliant-labs/forge/pkg/apikey\"\n\t\"github.com/reliant-labs/forge/pkg/auth\"\n", 1)
	if err := os.WriteFile(authPath, []byte(authGo), 0o644); err != nil {
		t.Fatalf("fill in APIKeyValidator: %v", err)
	}

	// A non-entity table is ordinary schema: generate must take it in stride.
	runCmd(t, projectDir, forgeBin, "generate")
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")

	// ── 3. Boot it and drive the accept/reject matrix over HTTP ─────────
	bootAPIKeyAuthMatrix(t, projectDir)
}

// apiKeyServerLog collects the server's output; the test reads it while the
// process may still be writing.
type apiKeyServerLog struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *apiKeyServerLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *apiKeyServerLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func bootAPIKeyAuthMatrix(t *testing.T, projectDir string) {
	t.Helper()
	port := freePortE2E(t)

	serverBin := filepath.Join(projectDir, "api-key-server")
	buildCorpusServer(t, projectDir, serverBin)

	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision api-key postgres: %v", err)
	}
	defer cleanup()
	applyProjectMigrationsPostgres(t, projectDir, dsn)

	// Issue two keys the way the skill says to: store the lookup prefix and
	// the hash, never the key. One is then revoked.
	const owner = "key-owner-746"
	key := issueAPIKeyE2E(t, dsn, owner, false)
	revoked := issueAPIKeyE2E(t, dsn, "revoked-owner", true)

	pubPEM, bearer := mintDevJWT(t)

	cmd := exec.Command(serverBin, "server")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		"DATABASE_URL="+dsn,
		"ENVIRONMENT=development",
		"JWT_SECRET="+pubPEM,
	)
	serverOut := &apiKeyServerLog{}
	cmd.Stdout = serverOut
	cmd.Stderr = serverOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start api-key server: %v", err)
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
		t.Fatalf("api-key server did not become ready\nserver output:\n%s", serverOut.String())
	}

	call := func(procedure, body string, header ...string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, base+"/services.widgets.v1.WidgetsService/"+procedure, strings.NewReader(body))
		if err != nil {
			t.Fatalf("build %s request: %v", procedure, err)
		}
		req.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", procedure, err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(out)
	}

	// Same lookup prefix, different secret: the row is found, the hash is not.
	wrongKey := key[:len(key)-1] + "A"
	if key[len(key)-1] == 'A' {
		wrongKey = key[:len(key)-1] + "B"
	}
	cases := []struct {
		name   string
		header []string
		want   int
	}{
		{"api key on X-API-Key", []string{"X-API-Key", key}, http.StatusOK},
		{"api key as a bearer token", []string{"Authorization", "Bearer " + key}, http.StatusOK},
		{"jwt as a bearer token", []string{"Authorization", bearer}, http.StatusOK},
		{"right prefix, wrong secret", []string{"X-API-Key", wrongKey}, http.StatusUnauthorized},
		{"revoked key", []string{"X-API-Key", revoked}, http.StatusUnauthorized},
		{"malformed key", []string{"X-API-Key", apikey.KeyPrefix + "short"}, http.StatusUnauthorized},
		{"not a key at all", []string{"X-API-Key", "hunter2"}, http.StatusUnauthorized},
		{"no credential", nil, http.StatusUnauthorized},
		// One credential per request: the Bearer token is the one checked,
		// so a valid key beside an invalid token cannot rescue it.
		{"bad bearer beside a good key", []string{"Authorization", "Bearer forged", "X-API-Key", key}, http.StatusUnauthorized},
		{"malformed bearer beside a good key", []string{"Authorization", "Bearer", "X-API-Key", key}, http.StatusUnauthorized},
		{"empty authorization beside a good key", []string{"Authorization", "", "X-API-Key", key}, http.StatusUnauthorized},
		{"empty key beside a good cookie", []string{"X-API-Key", "", "Cookie", "keyapp_session=" + strings.TrimPrefix(bearer, "Bearer ")}, http.StatusUnauthorized},
		{"whitespace key beside a good cookie", []string{"X-API-Key", "   ", "Cookie", "keyapp_session=" + strings.TrimPrefix(bearer, "Bearer ")}, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		status, body := call("ListWidgets", `{"pageSize":10}`, tc.header...)
		if status != tc.want {
			t.Errorf("%s: ListWidgets = HTTP %d, want %d\nresponse: %s\nserver output:\n%s",
				tc.name, status, tc.want, body, serverOut.String())
		}
	}

	// The wrong key's own reason stays server-side: the caller reads
	// "invalid API key", the operator's log carries the cause.
	if status, body := call("ListWidgets", `{}`, "X-API-Key", wrongKey); status != http.StatusUnauthorized ||
		!strings.Contains(body, "invalid API key") || strings.Contains(body, "does not match") {
		t.Errorf("a wrong key must answer 401 \"invalid API key\" without the validator's reason, got %d %s", status, body)
	}

	// The principal comes from the key's ROW: a write made with the key is
	// audited as that row's user_id.
	if status, body := call("CreateWidget", `{"name":"made-with-a-key"}`, "X-API-Key", key); status != http.StatusOK {
		t.Fatalf("CreateWidget with a valid API key = HTTP %d: %s\nserver output:\n%s", status, body, serverOut.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(serverOut.String(), owner) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	logs := serverOut.String()
	if !strings.Contains(logs, owner) {
		t.Errorf("the request made with the key must run as its row's user_id %q; server output:\n%s", owner, logs)
	}
	if !strings.Contains(logs, "key does not match") {
		t.Errorf("the key validator's reason must reach the server log as the failure's cause; server output:\n%s", logs)
	}
}

// issueAPIKeyE2E generates a key and stores what the skill says to store —
// the lookup prefix and the SHA-256 hash, never the key — returning the
// plaintext, as an issuing endpoint would exactly once.
func issueAPIKeyE2E(t *testing.T, dsn, userID string, revoke bool) string {
	t.Helper()
	key, err := apikey.Generate()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}
	prefix, err := apikey.LookupPrefix(key)
	if err != nil {
		t.Fatalf("lookup prefix: %v", err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer db.Close()
	revokedAt := sql.NullTime{Time: time.Now(), Valid: revoke}
	if _, err := db.Exec(`INSERT INTO api_keys (prefix, key_hash, user_id, revoked_at) VALUES ($1, $2, $3, $4)`,
		prefix, apikey.Hash(key), userID, revokedAt); err != nil {
		t.Fatalf("insert api key: %v", err)
	}
	return key
}
