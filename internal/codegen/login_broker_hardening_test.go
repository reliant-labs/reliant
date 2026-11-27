package codegen

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// renderHardeningBroker scaffolds the login broker into a fixture project and
// returns its parsed source.
func renderHardeningBroker(t *testing.T) (*token.FileSet, *ast.File, []byte) {
	t.Helper()
	brokerFields := []ConfigField{
		{Name: "idp_base", GoName: "IdpBase", GoType: "string", ProtoType: "string", EnvVar: "IDP_BASE"},
		{Name: "idp_host", GoName: "IdpHost", GoType: "string", ProtoType: "string", EnvVar: "IDP_HOST"},
		{Name: "idp_broker_token", GoName: "IdpBrokerToken", GoType: "string", ProtoType: "string", EnvVar: "IDP_BROKER_TOKEN", Sensitive: true},
		{Name: "idp_broker_token_file", GoName: "IdpBrokerTokenFile", GoType: "string", ProtoType: "string", EnvVar: "IDP_BROKER_TOKEN_FILE"},
		{Name: "oidc_client_id", GoName: "OidcClientId", GoType: "string", ProtoType: "string", EnvVar: "OIDC_CLIENT_ID"},
		{Name: "oidc_redirect_uri", GoName: "OidcRedirectUri", GoType: "string", ProtoType: "string", EnvVar: "OIDC_REDIRECT_URI"},
		{Name: "idp_login_uri", GoName: "IdpLoginUri", GoType: "string", ProtoType: "string", EnvVar: "IDP_LOGIN_URI"},
		{Name: "environment", GoName: "Environment", GoType: "string", ProtoType: "string", EnvVar: "ENVIRONMENT", Role: "CONFIG_FIELD_ROLE_MODE"},
	}
	dir := t.TempDir()
	writeBrokerFixture(t, dir, []ConfigMessage{{Name: "AppConfig", Fields: brokerFields}})
	if wrote, err := GenerateLoginBroker("example.com/shop", "shop", dir); err != nil || !wrote {
		t.Fatalf("GenerateLoginBroker = %v, %v", wrote, err)
	}
	src, err := os.ReadFile(filepath.Join(dir, "internal", "app", "login_broker.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "login_broker.go", src, 0)
	if err != nil {
		t.Fatalf("the scaffolded broker does not parse: %v", err)
	}
	return fset, f, src
}

// funcSource is the source of the function or method name in f ("" if none).
func funcSource(t *testing.T, fset *token.FileSet, f *ast.File, name string) string {
	t.Helper()
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == name {
			var b bytes.Buffer
			if err := format.Node(&b, fset, fn); err != nil {
				t.Fatal(err)
			}
			return b.String()
		}
	}
	return ""
}

// Two scaffolded-broker defects a review of reliant's owned copy found
// (forge report 684983c7):
//
//   - with server-side sessions, a cookie that is not a session id (a token
//     replayed into it) was presented by /auth/session as signed in, with
//     whatever claims the unverified token carried;
//   - /auth/reset charged caller-chosen user ids with no per-client
//     admission, and a full limiter table refused every new key, so a client
//     inventing ids could disable password resets for everyone for an hour.
func TestGenerateLoginBroker_SessionCookieAndResetAdmission(t *testing.T) {
	fset, f, src := renderHardeningBroker(t)

	access := funcSource(t, fset, f, "accessToken")
	if !strings.Contains(access, "!session.IsID(cookie.Value)") || !strings.Contains(access, `return "", session.ErrNoSession`) {
		t.Errorf("with sessions on, a cookie that is not a session id must be no session:\n%s", access)
	}

	reset := funcSource(t, fset, f, "handleReset")
	byIP, byUser := strings.Index(reset, "b.resetByIP.reserve("), strings.Index(reset, "b.resetByUser.reserve(")
	if byIP < 0 || byUser < 0 || byIP > byUser {
		t.Errorf("handleReset must admit per client (resetByIP) before charging a user id (resetByUser):\n%s", reset)
	}
	if !strings.Contains(string(src), "resetByIP:       newAttemptLimiter(") {
		t.Error("the broker never constructs resetByIP")
	}

	reserve := funcSource(t, fset, f, "reserve")
	if !strings.Contains(reserve, "l.evict()") {
		t.Errorf("a full limiter must make room, never refuse an unseen key:\n%s", reserve)
	}
}

// The limiter's eviction, run: a table full of spent windows still admits a
// key it has not seen, and an open window is dropped before a spent one.
func TestGenerateLoginBroker_AttemptLimiterAdmitsUnseenKeysWhenFull(t *testing.T) {
	testkit.Slow(t, "runs go test over the scaffolded limiter")
	fset, f, _ := renderHardeningBroker(t)

	var b bytes.Buffer
	b.WriteString("package limiter\n\nimport (\n\t\"sync\"\n\t\"time\"\n)\n\n")
	for _, d := range f.Decls {
		keep := false
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Name.Name == "newAttemptLimiter" {
				keep = true
			}
			if d.Recv != nil && len(d.Recv.List) == 1 {
				if star, ok := d.Recv.List[0].Type.(*ast.StarExpr); ok {
					if id, ok := star.X.(*ast.Ident); ok && id.Name == "attemptLimiter" {
						keep = true
					}
				}
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					keep = keep || s.Name.Name == "attemptLimiter" || s.Name.Name == "attemptEntry"
				case *ast.ValueSpec:
					for _, n := range s.Names {
						keep = keep || n.Name == "attemptLimiterMaxKeys"
					}
				}
			}
		}
		if keep {
			if err := format.Node(&b, fset, d); err != nil {
				t.Fatal(err)
			}
			b.WriteString("\n\n")
		}
	}
	const behaviour = `package limiter

import (
	"strconv"
	"testing"
	"time"
)

func TestFullTableAdmitsAnUnseenKey(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := newAttemptLimiter(1, time.Hour)
	l.now = func() time.Time { return now }
	for i := 0; i < attemptLimiterMaxKeys; i++ {
		if !l.reserve("invented-" + strconv.Itoa(i)) {
			t.Fatalf("key %d refused while filling", i)
		}
	}
	if !l.reserve("real-user") {
		t.Fatal("a table full of spent windows refused a key it has not seen")
	}
	if len(l.entries) > attemptLimiterMaxKeys {
		t.Fatalf("table grew past its bound: %d", len(l.entries))
	}
}

func TestEvictDropsAnOpenWindowBeforeASpentOne(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := newAttemptLimiter(2, time.Hour)
	l.now = func() time.Time { return now }
	for i := 0; i < attemptLimiterMaxKeys; i++ {
		key := "k" + strconv.Itoa(i)
		l.reserve(key)
		if i > 0 {
			l.reserve(key) // spent
		}
		now = now.Add(time.Millisecond)
	}
	l.reserve("new")
	if _, ok := l.entries["k0"]; ok {
		t.Error("the only open window should have been evicted")
	}
	if !l.spent("k1") {
		t.Error("a spent window was evicted while an open one remained")
	}
}
`
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":          "module example.com/limiter\n\ngo 1.22\n",
		"limiter.go":      b.String(),
		"limiter_test.go": behaviour,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "test", "-count=1", "./...")
	cmd.Dir = dir
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOWORK=off", "GOFLAGS="))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the scaffolded limiter misbehaves: %v\n%s\n--- limiter.go ---\n%s", err, out, b.String())
	}
}
