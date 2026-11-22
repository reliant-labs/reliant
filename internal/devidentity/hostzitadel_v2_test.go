package devidentity

// Identity v2 against the real thing: host postgres, the pinned Zitadel and
// the pinned Mailpit, started through hostinfra.Start as `forge env up`
// starts them, converged by Converge with owners, every sign-in method and a
// social provider. It proves what the fake cannot: that Zitadel v4.19.5
// accepts each call body, that the owner's invitation really arrives in
// Mailpit with a link to the app, that a second pass writes nothing, and that
// the HTTP email provider signs what it sends with the key the KeyStore holds.
//
// Skipped under -short: it boots three servers (the first run downloads the
// Zitadel and Mailpit releases into the user cache).

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/hostinfra"
	"github.com/reliant-labs/forge/internal/secrets"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/pkg/identity"
	"github.com/reliant-labs/forge/pkg/release"
)

func TestConverge_V2AgainstTheRealHostZitadelAndMailpit(t *testing.T) {
	if testing.Short() {
		t.Skip("boots host postgres, the pinned Zitadel and Mailpit; runs in task test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("host Zitadel is exercised on unix")
	}
	// Zitadel refuses to call a loopback HTTP endpoint by default (its
	// HTTPClient.DenyList); the HTTP-provider half below runs its receiver on
	// loopback, so this instance's list names only a documentation range.
	// The IdP inherits the test process's environment.
	t.Setenv("ZITADEL_HTTPCLIENT_DENYLIST", "192.0.2.0/24")

	dir := t.TempDir()
	steps, err := templates.ProjectTemplates().Render("idp-steps.yaml.tmpl", struct{ ProjectName string }{"acme"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "idp-steps.yaml"), steps, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pgSpec := hostinfra.Spec{Name: "postgres", Engine: hostinfra.EnginePostgres, Port: freeLoopbackPort(t), Database: "acme", User: "postgres", Password: "postgres"}
	idpSpec := hostinfra.Spec{
		Name: "idp", Engine: hostinfra.EngineZitadel, Port: freeLoopbackPort(t),
		IDPDatabase: "zitadel", IDPDatabasePort: pgSpec.Port, IDPMasterKey: "MasterkeyNeedsToHave32Characters",
		IDPStepsFile: "idp-steps.yaml", IDPPATPath: ".forge/idp/pat.txt",
		Database: "postgres", User: "postgres", Password: "postgres",
	}
	mailSpec := hostinfra.Spec{Name: "mail", Engine: hostinfra.EngineMailpit, Port: mailpitPort(t)}
	t.Cleanup(func() {
		_ = hostinfra.Stop(dir, mailSpec)
		_ = hostinfra.Stop(dir, idpSpec)
		_ = hostinfra.Stop(dir, pgSpec)
	})
	for _, s := range []hostinfra.Spec{pgSpec, idpSpec, mailSpec} {
		if err := hostinfra.Start(ctx, dir, s); err != nil {
			t.Skipf("host %s is unavailable here: %v", s.Engine, err)
		}
	}

	const frontend = "http://localhost:3000"
	sh := &release.ShapeIdentity{
		Name:    "identity",
		Runtime: release.IdentityRuntimeHost,
		Login:   release.ShapeIdentityLogin{Mode: release.IdentityLoginNative},
		Roles: []release.ShapeIdentityRole{
			{Key: "owner", MembersFrom: release.IdentityMembersConsole},
			{Key: "member", MembersFrom: release.IdentityMembersConsole},
		},
		Applications: []release.ShapeIdentityApplication{{Name: "dev:api", Workload: "api", Kind: release.IdentityAppNativeBroker}},
		Owners:       []string{"owner@acme.test"},
		OwnerRole:    "owner",
		Signup:       &release.ShapeIdentitySignup{Mode: release.IdentitySignupInvite},
		Methods:      []string{release.IdentityMethodMagicLink, release.IdentityMethodPasskey, release.IdentityMethodPassword},
		Social: []release.ShapeIdentitySocial{{Type: release.IdentitySocialGoogle, ClientID: "dev-client.apps.googleusercontent.com",
			ClientSecretSecret: "GOOGLE_CLIENT_SECRET", Scopes: []string{"email", "openid", "profile"}}},
		Email:   &release.ShapeIdentityEmail{Delivery: release.IdentityMailPlatform, FromName: "Acme"},
		DevMail: "mail",
	}
	store := filepath.Join(dir, "secrets", "dev.yaml")
	in := Input{
		Project:        "acme",
		Identity:       sh,
		BaseURL:        "http://localhost:" + strconv.Itoa(idpSpec.Port),
		PATPath:        idpSpec.PATPath(dir),
		FrontendOrigin: frontend,
		MailpitPort:    mailSpec.Port,
		Secret: func(n string) (string, bool) {
			if n == "GOOGLE_CLIENT_SECRET" {
				return "dev-google-secret", true
			}
			return "", false
		},
		Refs: []Ref{
			{EnvName: "IDP_BROKER_TOKEN", SecretName: "forge-identity-identity", SecretKey: "DEV_API_BROKER_TOKEN"},
			{EnvName: "IDP_LOGIN_POLICY", SecretName: "forge-identity-identity", SecretKey: "LOGIN_POLICY"},
		},
		StorePath:   store,
		OutputsPath: filepath.Join(dir, ".forge", "identity", "identity.json"),
		KeysDir:     filepath.Join(dir, ".forge", "identity", "identity.keys"),
	}

	first, err := Converge(ctx, in)
	if err != nil {
		t.Fatalf("first converge: %v", err)
	}
	if strings.Join(first.Report.OwnersInvited, ",") != "owner@acme.test" {
		t.Fatalf("owners invited = %v (changes %v)", first.Report.OwnersInvited, first.Report.Changes)
	}
	if first.SocialCallbackURL != in.BaseURL+"/idps/callback" {
		t.Errorf("social callback = %q", first.SocialCallbackURL)
	}

	// The invitation arrives in Mailpit, linking to the app's invite page.
	body := waitForMail(ctx, t, mailSpec, "owner@acme.test")
	if !strings.Contains(body, frontend+"/auth/invite?userID=") || !strings.Contains(body, "code=") {
		t.Errorf("the invitation does not link to the app's invite page:\n%s", body)
	}

	// The broker's LOGIN_POLICY document reached the store.
	vals, err := secrets.ReadSecretFile(store)
	if err != nil {
		t.Fatal(err)
	}
	var doc identity.LoginPolicyDoc
	if err := json.Unmarshal([]byte(vals["IDP_LOGIN_POLICY"]), &doc); err != nil {
		t.Fatalf("IDP_LOGIN_POLICY = %q: %v", vals["IDP_LOGIN_POLICY"], err)
	}
	if doc.Version != 1 || doc.Mail != identity.MailDev || !doc.RequireVerifiedEmail || doc.PasskeyRPID != "localhost" ||
		len(doc.Social) != 1 || doc.Social[0].IdPID == "" || doc.Password == nil || doc.Password.MinLength == 0 {
		t.Errorf("login policy document = %+v", doc)
	}

	// A second pass: nothing written, nobody re-invited.
	second, err := Converge(ctx, in)
	if err != nil {
		t.Fatalf("second converge: %v", err)
	}
	if second.Report.Changed() {
		t.Errorf("a converged instance was written to again: %v", second.Report.Changes)
	}
	if len(second.StoreKeys) != 0 || len(second.Report.OwnersInvited) != 0 {
		t.Errorf("second pass: store keys %v, invited %v", second.StoreKeys, second.Report.OwnersInvited)
	}

	// The HTTP email provider: the control plane's mail path, on the same
	// instance. Every notification is POSTed signed with the key the
	// KeyStore holds.
	got := make(chan *http.Request, 8)
	bodies := make(chan []byte, 8)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- r
		bodies <- b
	}))
	defer recv.Close()
	keys := &identity.MemKeyStore{}
	rec, err := identity.New(identity.Options{
		BaseURL: in.BaseURL, Issuer: in.BaseURL, Credential: identity.PATFile(in.PATPath), Keys: keys, ReadyTimeout: readyTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpSpec := identity.Spec{Project: identity.ProjectSpec{Name: "acme"}, EmailHTTP: &identity.HTTPEmail{Endpoint: recv.URL + "/v1/zitadel/test"}}
	if _, err := rec.Reconcile(ctx, httpSpec); err != nil {
		t.Fatalf("converge the HTTP provider: %v", err)
	}
	if rep, err := rec.Reconcile(ctx, httpSpec); err != nil || rep.Changed() {
		t.Errorf("HTTP provider second pass: %v %v", err, rep.Changes)
	}
	signingKey, _ := keys.Load(ctx, identity.KeyMailSigning)
	if len(signingKey) == 0 {
		t.Fatal("no signing key in the store")
	}
	ownerID := findUserID(ctx, t, in, "owner@acme.test")
	resetReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, in.BaseURL+"/v2/users/"+ownerID+"/password_reset", strings.NewReader(`{"sendLink":{}}`))
	resetReq.Header.Set("Authorization", "Bearer "+readPAT(t, in.PATPath))
	resetReq.Header.Set("Content-Type", "application/json")
	if resp, err := http.DefaultClient.Do(resetReq); err != nil || resp.StatusCode != 200 {
		t.Fatalf("request a password reset: %v %v", err, resp)
	}
	select {
	case r := <-got:
		payload := <-bodies
		if err := verifyZitadelSignature(payload, r.Header.Get("ZITADEL-Signature"), string(signingKey)); err != nil {
			t.Errorf("the notification's signature does not verify with the stored key: %v", err)
		}
		if !strings.Contains(string(payload), "owner@acme.test") {
			t.Errorf("the notification does not name its recipient: %s", payload)
		}
	case <-time.After(2 * time.Minute):
		t.Fatal("no notification reached the HTTP provider")
	}
}

// verifyZitadelSignature checks a ZITADEL-Signature header ("t=<unix>,v1=<hex
// hmac-sha256 of '<t>.<payload>'>"), Zitadel's own format (pkg/actions
// signing.go at v4.19.5).
func verifyZitadelSignature(payload []byte, header, key string) error {
	var ts string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	if ts == "" || len(sigs) == 0 {
		return fmt.Errorf("malformed header %q", header)
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(ts + "."))
	mac.Write(payload)
	want := hex.EncodeToString(mac.Sum(nil))
	for _, s := range sigs {
		if hmac.Equal([]byte(s), []byte(want)) {
			return nil
		}
	}
	return fmt.Errorf("no v1 signature matches")
}

func waitForMail(ctx context.Context, t *testing.T, mail hostinfra.Spec, to string) string {
	t.Helper()
	api := "http://127.0.0.1:" + strconv.Itoa(hostinfra.MailpitUIPort(mail)) + "/api/v1"
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		var list struct {
			Messages []struct {
				ID string `json:"ID"`
				To []struct {
					Address string `json:"Address"`
				} `json:"To"`
			} `json:"messages"`
		}
		if resp, err := http.Get(api + "/messages"); err == nil {
			_ = json.NewDecoder(resp.Body).Decode(&list)
			_ = resp.Body.Close()
		}
		for _, m := range list.Messages {
			for _, a := range m.To {
				if a.Address == to {
					resp, err := http.Get(api + "/message/" + m.ID)
					if err != nil {
						t.Fatal(err)
					}
					var msg struct{ Text, HTML string }
					_ = json.NewDecoder(resp.Body).Decode(&msg)
					_ = resp.Body.Close()
					return msg.Text + "\n" + msg.HTML
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	t.Fatalf("no mail to %s reached Mailpit", to)
	return ""
}

func readPAT(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func findUserID(ctx context.Context, t *testing.T, in Input, email string) string {
	t.Helper()
	body := `{"queries":[{"emailQuery":{"emailAddress":"` + email + `","method":"TEXT_QUERY_METHOD_EQUALS_IGNORE_CASE"}}]}`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, in.BaseURL+"/v2/users", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+readPAT(t, in.PATPath))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Result []struct {
			UserID string `json:"userId"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Result) == 0 {
		t.Fatalf("find %s: %v %+v", email, err, out)
	}
	return out.Result[0].UserID
}

// mailpitPort finds a free SMTP port whose UI port is free too. A host with no
// free pair in 50 tries is out of ports, so the test skips.
func mailpitPort(t *testing.T) int {
	t.Helper()
	p, found := 0, false
	for i := 0; i < 50 && !found; i++ {
		p = freeLoopbackPort(t)
		if p > 50000 {
			p -= 20000
		}
		found = freePort(p) && freePort(hostinfra.MailpitUIPort(hostinfra.Spec{Port: p}))
	}
	if !found {
		t.Skip("no free loopback port pair for mailpit (SMTP port and its UI port) in 50 tries")
	}
	return p
}

func freePort(p int) bool {
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(p))
	if err == nil {
		_ = resp.Body.Close()
		return false
	}
	return strings.Contains(err.Error(), "refused")
}
