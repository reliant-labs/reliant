//go:build e2e

package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/reliant-labs/forge/internal/devidentity"
	"github.com/reliant-labs/forge/internal/hostinfra"
	"github.com/reliant-labs/forge/internal/secrets"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/pkg/devidp"
	"github.com/reliant-labs/forge/pkg/identity"
	"github.com/reliant-labs/forge/pkg/release"
)

// TestE2EIdentityImportPreservesIdAndPassword is the gate for user import
// (forge G5) against the REAL pinned Zitadel, booted the way `forge env up`
// boots it and converged with a GitHub provider (dummy OAuth credentials: no
// sign-in goes through GitHub here; the link only has to land).
//
// It proves what a migration from another identity provider depends on:
//
//   - an account imported with a caller-chosen id and a bcrypt hash of a known
//     password signs in through the broker with the ORIGINAL password, and the
//     token it gets carries `sub` = the imported id;
//   - the verified address and the GitHub link (by numeric id) land;
//   - --dry-run writes nothing, and re-running the file is a no-op;
//   - Zitadel's own refusals map to the right row outcome: a taken link, a
//     taken address, an unverifiable scheme, a bcrypt cost under the instance's
//     floor;
//   - --update-passwords replaces the hash, and the new password signs in.
func TestE2EIdentityImportPreservesIdAndPassword(t *testing.T) {
	t.Parallel() // its own IdP, Postgres and temp dir
	dir := t.TempDir()
	steps, err := templates.ProjectTemplates().Render("idp-steps.yaml.tmpl", struct{ ProjectName string }{"importapp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "idp-steps.yaml"), steps, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	pgSpec := hostinfra.Spec{Name: "postgres", Engine: hostinfra.EnginePostgres, Port: freePortE2E(t), Database: "importapp", User: "postgres", Password: "postgres"}
	idpSpec := hostinfra.Spec{
		Name: "idp", Engine: hostinfra.EngineZitadel, Port: freePortE2E(t),
		IDPDatabase: "zitadel", IDPDatabasePort: pgSpec.Port, IDPMasterKey: "MasterkeyNeedsToHave32Characters",
		IDPStepsFile: "idp-steps.yaml", IDPPATPath: ".forge/idp/pat.txt",
		Database: "postgres", User: "postgres", Password: "postgres",
	}
	t.Cleanup(func() {
		_ = hostinfra.Stop(dir, idpSpec)
		_ = hostinfra.Stop(dir, pgSpec)
	})
	if err := hostinfra.Start(ctx, dir, pgSpec); err != nil {
		t.Skipf("host postgres is unavailable here: %v", err)
	}
	if err := hostinfra.Start(ctx, dir, idpSpec); err != nil {
		t.Skipf("host Zitadel is unavailable here: %v", err)
	}

	idpBase := fmt.Sprintf("http://localhost:%d", idpSpec.Port)
	store := filepath.Join(dir, "secrets", "dev.yaml")
	in := devidentity.Input{
		Project: "importapp",
		Identity: &release.ShapeIdentity{
			Name:         "identity",
			Runtime:      release.IdentityRuntimeHost,
			Login:        release.ShapeIdentityLogin{Mode: release.IdentityLoginNative},
			Methods:      []string{release.IdentityMethodPassword},
			Roles:        []release.ShapeIdentityRole{{Key: "member", MembersFrom: release.IdentityMembersConsole}},
			Applications: []release.ShapeIdentityApplication{{Name: "dev:api", Workload: "api", Kind: release.IdentityAppNativeBroker}},
			Social: []release.ShapeIdentitySocial{{Type: release.IdentitySocialGitHub, ClientID: "dummy-github-client",
				ClientSecretSecret: "GITHUB_CLIENT_SECRET", Scopes: []string{"read:user", "user:email"}}},
		},
		BaseURL:        idpBase,
		PATPath:        idpSpec.PATPath(dir),
		FrontendOrigin: "http://localhost:3000",
		Secret: func(n string) (string, bool) {
			if n == "GITHUB_CLIENT_SECRET" {
				return "dummy-github-secret", true
			}
			return "", false
		},
		Refs: []devidentity.Ref{
			{EnvName: "OIDC_CLIENT_ID", SecretName: "forge-identity-identity", SecretKey: "DEV_API_CLIENT_ID"},
			{EnvName: "OIDC_REDIRECT_URI", SecretName: "forge-identity-identity", SecretKey: "DEV_API_REDIRECT_URI"},
			{EnvName: "IDP_BROKER_TOKEN", SecretName: "forge-identity-identity", SecretKey: "DEV_API_BROKER_TOKEN"},
		},
		StorePath:   store,
		OutputsPath: filepath.Join(dir, ".forge", "identity", "identity.json"),
		KeysDir:     filepath.Join(dir, ".forge", "identity", "identity.keys"),
	}
	if _, err := devidentity.Converge(ctx, in); err != nil {
		t.Fatalf("converge the identity: %v", err)
	}
	vals, err := secrets.ReadSecretFile(store)
	if err != nil {
		t.Fatal(err)
	}
	var out identity.Output
	raw, err := os.ReadFile(in.OutputsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	pat, err := os.ReadFile(in.PATPath)
	if err != nil {
		t.Fatal(err)
	}
	admin := &devidp.Client{Base: idpBase, Token: strings.TrimSpace(string(pat))}
	broker := &devidp.Client{Base: idpBase, Token: vals["IDP_BROKER_TOKEN"]}
	flow := devidp.FlowConfig{ClientID: vals["OIDC_CLIENT_ID"], RedirectURI: vals["OIDC_REDIRECT_URI"], Scopes: out.Scopes}

	// ── The file: what a project exports from its old provider. ──
	const password = "Imported-Original-1!"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 10) // the instance's MinCost
	if err != nil {
		t.Fatal(err)
	}
	userID := uuid(t)
	ada := map[string]any{
		"v": 1, "id": userID, "email": "ada@import.example", "email_verified": true,
		"given_name": "Ada", "family_name": "Lovelace", "display_name": "Ada L",
		"password_hash": string(hash),
		"links":         []any{map[string]any{"provider": "github", "provider_user_id": "583231", "provider_user_name": "ada"}},
		"metadata":      map[string]any{"legacy_plan": "pro"},
		"roles":         []any{"member"},
	}
	file := writeJSONL(t, dir, "users.jsonl", ada)
	host := &hostIdentityImporter{in: in, env: "dev"}
	run := func(file string, opts identity.ImportOptions) ([]identityImportRow, error) {
		t.Helper()
		report := filepath.Join(t.TempDir(), "report.jsonl")
		var sb strings.Builder
		err := runIdentityImport(ctx, &sb, identityImportOptions{file: file, report: report, batch: maxIdentityImportBatch, opts: opts},
			func(context.Context) (identityImporter, error) { return host, nil })
		t.Logf("import %s %+v:\n%s", filepath.Base(file), opts, sb.String())
		if strings.Contains(sb.String(), string(hash)) {
			t.Fatal("the summary printed a password hash")
		}
		return readReport(t, report), err
	}

	// ── Dry run: every row resolved, nothing written. ──
	rows, err := run(file, identity.ImportOptions{DryRun: true})
	if err != nil || len(rows) != 1 || rows[0].Outcome != identity.ImportCreated {
		t.Fatalf("dry run = %+v, %v", rows, err)
	}
	if _, err := admin.FindUserByID(ctx, userID); err == nil {
		t.Fatal("a dry run created the account")
	}

	// ── The import. ──
	rows, err = run(file, identity.ImportOptions{})
	if err != nil || rows[0].Outcome != identity.ImportCreated {
		t.Fatalf("import = %+v, %v", rows, err)
	}

	// The ORIGINAL password signs in, and the token's subject is the id the
	// app already knows.
	toks := signInEventually(ctx, t, broker, flow, "ada@import.example", password)
	for name, tok := range map[string]string{"access": toks.AccessToken, "id": toks.IDToken} {
		if tok == "" {
			continue
		}
		if sub := jwtSubject(t, tok); sub != userID {
			t.Errorf("%s token sub = %q, want the imported id %q", name, sub, userID)
		}
	}
	if _, err := broker.SignIn(ctx, flow, devidp.Credentials{LoginName: "ada@import.example", Password: "Not-The-Password-1!"}); err == nil {
		t.Error("a wrong password signed in")
	}
	u, err := admin.FindUserByID(ctx, userID)
	if err != nil || !u.EmailVerified {
		t.Errorf("imported account = %+v, %v (want the address verified)", u, err)
	}
	links, err := admin.ListIDPLinks(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].UserID != "583231" || links[0].UserName != "ada" || links[0].IdPID == "" {
		t.Errorf("links = %+v, want the GitHub link by numeric id", links)
	}
	if v, ok, err := admin.Metadata(ctx, userID, "legacy_plan"); err != nil || !ok || v != "pro" {
		t.Errorf("metadata legacy_plan = %q %v %v", v, ok, err)
	}

	// ── Re-running the file is a no-op. ──
	for _, opts := range []identity.ImportOptions{{}, {UpdateExisting: true}} {
		rows, err = run(file, opts)
		if err != nil || rows[0].Outcome != identity.ImportUnchanged || rows[0].Reason != "" {
			t.Errorf("re-run %+v = %+v, %v", opts, rows, err)
		}
	}

	// ── Zitadel's own refusals, row by row. ──
	weak, err := bcrypt.GenerateFromPassword([]byte(password), 4) // under the instance's MinCost (10)
	if err != nil {
		t.Fatal(err)
	}
	row := func(id, email string, mod func(map[string]any)) map[string]any {
		r := map[string]any{"v": 1, "id": id, "email": email, "email_verified": true, "password_hash": string(hash)}
		mod(r)
		return r
	}
	bad := writeJSONL(t, dir, "bad.jsonl",
		row("thief", "thief@import.example", func(r map[string]any) {
			r["links"] = []any{map[string]any{"provider": "github", "provider_user_id": "583231"}}
		}),
		row("twin", "ADA@import.example", func(map[string]any) {}),
		row("argon", "argon@import.example", func(r map[string]any) {
			r["password_hash"] = "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g"
		}),
		row("weak", "weak@import.example", func(r map[string]any) { r["password_hash"] = string(weak) }),
		row("fine", "fine@import.example", func(map[string]any) {}),
	)
	rows, err = run(bad, identity.ImportOptions{})
	if exitCodeForError(err) != exitConflict {
		t.Errorf("a file with conflicts exited %d (%v), want %d", exitCodeForError(err), err, exitConflict)
	}
	want := map[string][2]string{
		"thief": {"conflict", identity.ReasonLinkTaken},
		"twin":  {"conflict", identity.ReasonEmailTaken},
		"argon": {"invalid", identity.ReasonUnsupportedHash},
		"weak":  {"invalid", identity.ReasonInvalidHash},
		"fine":  {"created", ""},
	}
	for _, r := range rows {
		if w := want[r.ID]; string(r.Outcome) != w[0] || r.Reason != w[1] {
			t.Errorf("row %s = %s %q, want %s %q", r.ID, r.Outcome, r.Reason, w[0], w[1])
		}
	}

	// ── --update-passwords replaces the hash; the new password signs in. ──
	const next = "Rotated-Password-2!"
	nextHash, err := bcrypt.GenerateFromPassword([]byte(next), 10)
	if err != nil {
		t.Fatal(err)
	}
	ada["password_hash"] = string(nextHash)
	rotated := writeJSONL(t, dir, "rotated.jsonl", ada)
	if rows, err = run(rotated, identity.ImportOptions{}); err != nil || rows[0].Outcome != identity.ImportUnchanged {
		t.Errorf("without --update-passwords = %+v, %v", rows, err)
	}
	if rows, err = run(rotated, identity.ImportOptions{UpdatePasswords: true}); err != nil || rows[0].Outcome != identity.ImportUpdated || rows[0].Reason != "password" {
		t.Fatalf("--update-passwords = %+v, %v", rows, err)
	}
	toks = signInEventually(ctx, t, broker, flow, "ada@import.example", next)
	if sub := jwtSubject(t, toks.AccessToken); sub != userID {
		t.Errorf("after the rotation, sub = %q, want %q", sub, userID)
	}
}

func uuid(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func writeJSONL(t *testing.T, dir, name string, rows ...map[string]any) string {
	t.Helper()
	var b strings.Builder
	for _, r := range rows {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func readReport(t *testing.T, path string) []identityImportRow {
	t.Helper()
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	var rows []identityImportRow
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		var r identityImportRow
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r)
	}
	return rows
}

// signInEventually signs in, waiting out the issuer's projection lag after
// the import wrote the account.
func signInEventually(ctx context.Context, t *testing.T, c *devidp.Client, flow devidp.FlowConfig, login, password string) devidp.Tokens {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for {
		toks, err := c.SignIn(ctx, flow, devidp.Credentials{LoginName: login, Password: password})
		if err == nil {
			return toks
		}
		if time.Now().After(deadline) {
			t.Fatalf("sign in as %s with the imported password: %v", login, err)
		}
		time.Sleep(time.Second)
	}
}

func jwtSubject(t *testing.T, tok string) string {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %.20s…", tok)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	return claims.Sub
}
