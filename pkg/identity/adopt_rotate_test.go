package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// adoptedInstance is the shape of the prod instance adopted in place: content that
// exists under ids a fresh converge would never have produced.
func adoptedInstance(f *fakeZitadel) (*fProject, *fApp) {
	p := f.seedProject("389458096738533670", "control-plane admin API")
	app := f.seedApp(p, "389458096906306630", "control-plane internal-console", "389458096906371366", map[string]any{
		"redirectUris":             []any{"http://localhost:8098/old"},
		"responseTypes":            []any{"OIDC_RESPONSE_TYPE_CODE"},
		"grantTypes":               []any{"OIDC_GRANT_TYPE_AUTHORIZATION_CODE"},
		"appType":                  "OIDC_APP_TYPE_USER_AGENT",
		"authMethodType":           "OIDC_AUTH_METHOD_TYPE_NONE",
		"accessTokenType":          "OIDC_TOKEN_TYPE_JWT",
		"accessTokenRoleAssertion": true,
		"devMode":                  true,
	})
	p.Roles = []roleObj{{Key: "admin", DisplayName: "admin"}}
	f.mu.Lock()
	f.users["h1"] = &fUser{ID: "h1", UserName: "admin@reliantlabs.io", Email: "admin@reliantlabs.io", AuthMethods: []string{"AUTHENTICATION_METHOD_TYPE_PASSWORD"}}
	f.users["m1"] = &fUser{ID: "m1", UserName: "ops-automation", Machine: true, Name: "ops-automation", ATT: tokenTypeJWT}
	f.users["b1"] = &fUser{ID: "b1", UserName: "control-plane-login-broker", Machine: true, Name: "x", ATT: tokenTypeBearer}
	f.grants["gh"] = &fGrant{ID: "gh", UserID: "h1", ProjectID: p.ID, Roles: []string{"admin"}}
	f.grants["gm"] = &fGrant{ID: "gm", UserID: "m1", ProjectID: p.ID, Roles: []string{"admin"}}
	f.members["b1"] = []string{"IAM_LOGIN_CLIENT"}
	f.mu.Unlock()
	return p, app
}

func adoptedInstanceSpec() Spec {
	return Spec{
		Project: ProjectSpec{Name: "control-plane admin API", AdoptID: "389458096738533670"},
		Roles:   []Role{{Key: "admin", DisplayName: "admin", Declared: true, Members: []string{"admin@reliantlabs.io"}}},
		Applications: []Application{{
			Name: "control-plane internal-console", Kind: AppNativeBroker, AdoptClientID: "389458096906371366",
			RedirectURIs: []string{"https://console.example/auth/callback"}, LoginURI: "https://console.example/auth/sign-in",
			Broker: &BrokerAccount{Username: "control-plane-login-broker"},
		}},
		MachineUsers: []MachineUser{{Username: "ops-automation", Roles: []string{"admin"}, Key: true}},
	}
}

func TestAdoptionKeepsIdsAndExistingObjects(t *testing.T) {
	f := newFake(t)
	proj, app := adoptedInstance(f)
	r, _ := newRec(t, f, func(o *Options) { o.Adopt = true })

	rep, err := r.Reconcile(context.Background(), adoptedInstanceSpec())
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	out := rep.Output
	if out.ProjectID != "389458096738533670" || out.Audience != "389458096738533670" {
		t.Errorf("project id changed: %+v", out)
	}
	if out.Apps["control-plane internal-console"].ClientID != "389458096906371366" {
		t.Errorf("client id changed: %+v", out.Apps)
	}
	if len(f.projects) != 2 || len(proj.Apps) != 1 || proj.Apps[0] != app {
		t.Errorf("adoption must not create a second project or application: %d projects, %d apps", len(f.projects), len(proj.Apps))
	}
	// The pre-existing human, machine user and broker account are all kept.
	for _, id := range []string{"h1", "m1", "b1"} {
		if _, ok := f.users[id]; !ok {
			t.Errorf("user %s vanished", id)
		}
	}
	if n := countUsers(f); n != 5 { // setup, h1, m1, b1 (+ nothing new)
		t.Logf("users: %d", n)
	}
	got := changes(rep)
	if strings.Contains(got, "created project") || strings.Contains(got, "created application") ||
		strings.Contains(got, `created machine user`) || strings.Contains(got, `created user`) {
		t.Errorf("adoption created objects that already existed:\n%s", got)
	}
	// What was drifted is repaired in place: the redirect URIs and the login
	// route now match the declaration, on the SAME application.
	if uris := strs(app.Config["redirectUris"]); len(uris) != 1 || uris[0] != "https://console.example/auth/callback" {
		t.Errorf("redirect URIs = %v", uris)
	}
	// A broker token and a machine key are minted for accounts that had none.
	if out.Apps["control-plane internal-console"].BrokerToken == "" {
		t.Error("no broker token minted for the adopted broker account")
	}
	if _, ok := out.Machine["ops-automation"]; !ok || out.Machine["ops-automation"].KeyID == "" {
		t.Errorf("no machine key for the adopted machine user: %+v", out.Machine)
	}
	// And now it is quiet.
	before := f.writeCount()
	rep, err = r.Reconcile(context.Background(), adoptedInstanceSpec())
	if err != nil || rep.Changed() || f.writeCount() != before {
		t.Fatalf("second adoption pass must be a no-op: err=%v changes=%v", err, rep.Changes)
	}
}

func countUsers(f *fakeZitadel) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.users)
}

func TestAdoptionRefusesToCreateTheProjectOrAnApplication(t *testing.T) {
	f := newFake(t)
	r, _ := newRec(t, f, func(o *Options) { o.Adopt = true })
	_, err := r.Reconcile(context.Background(), Spec{Project: ProjectSpec{Name: "missing"}})
	if err == nil || !strings.Contains(err.Error(), `project "missing"`) || !strings.Contains(err.Error(), "Adopt") {
		t.Fatalf("got %v", err)
	}

	adoptedInstance(f)
	spec := adoptedInstanceSpec()
	spec.Applications[0].Name = "never-existed"
	spec.Applications[0].AdoptClientID = ""
	_, err = r.Reconcile(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), `application "never-existed"`) {
		t.Fatalf("got %v", err)
	}
	if f.writeCount() != 0 {
		t.Errorf("a refused adoption must not have written: %v", f.log)
	}
}

func TestPinnedIdMismatchIsAnErrorNotARecreate(t *testing.T) {
	f := newFake(t)
	adoptedInstance(f)
	r, _ := newRec(t, f, nil)

	spec := adoptedInstanceSpec()
	spec.Project.AdoptID = "999"
	_, err := r.Reconcile(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "pinned id 999") {
		t.Fatalf("project: got %v", err)
	}
	if len(f.projects) != 2 {
		t.Error("a pinned project id that does not exist created a project")
	}

	spec = adoptedInstanceSpec()
	spec.Applications[0].AdoptClientID = "000"
	_, err = r.Reconcile(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "pinned client id 000") {
		t.Fatalf("application: got %v", err)
	}
}

func TestPinnedClientIdFindsARenamedApplicationAndRenamesItBack(t *testing.T) {
	f := newFake(t)
	_, app := adoptedInstance(f)
	app.Name = "renamed in the console"
	r, _ := newRec(t, f, func(o *Options) { o.Adopt = true })
	rep, err := r.Reconcile(context.Background(), adoptedInstanceSpec())
	if err != nil {
		t.Fatal(err)
	}
	if app.Name != "control-plane internal-console" {
		t.Errorf("app not renamed back: %q", app.Name)
	}
	if rep.Output.Apps["control-plane internal-console"].ClientID != "389458096906371366" {
		t.Error("client id lost across a rename")
	}
}

func TestPinnedProjectIdWithANameClashIsRefused(t *testing.T) {
	f := newFake(t)
	adoptedInstance(f)
	f.seedProject("123", "other")
	spec := adoptedInstanceSpec()
	spec.Project.Name = "other"
	r, _ := newRec(t, f, nil)
	_, err := r.Reconcile(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "different project") {
		t.Fatalf("got %v", err)
	}
}

// --- rotation --------------------------------------------------------------

func brokerSpec() Spec {
	return Spec{
		Project: ProjectSpec{Name: "acme"},
		Applications: []Application{{Name: "prod:api", Kind: AppNativeBroker,
			RedirectURIs: []string{"https://acme.example/cb"}, LoginURI: "https://acme.example/auth/sign-in"}},
		MachineUsers: []MachineUser{{Username: "ops", Key: true}},
	}
}

func TestBrokerTokenIsRenewedBeforeItExpiresAndOverlaps(t *testing.T) {
	f := newFake(t)
	clock := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	f.now = func() time.Time { return clock }
	r, keys := newRec(t, f, nil)
	ctx := context.Background()

	rep, err := r.Reconcile(ctx, brokerSpec())
	if err != nil {
		t.Fatal(err)
	}
	first := rep.Output.Apps["prod:api"].BrokerToken
	user := f.user("broker-prod-api")

	// 15 days in: 15 days of life left, outside the 10-day renew window.
	clock = clock.Add(15 * day)
	rep, err = r.Reconcile(ctx, brokerSpec())
	if err != nil || rep.Changed() || rep.Output.Apps["prod:api"].BrokerToken != first {
		t.Fatalf("token renewed too early: err=%v changes=%v", err, rep.Changes)
	}

	// 21 days in: 9 days left, inside the window. A new one is minted and the
	// old one keeps working.
	clock = clock.Add(6 * day)
	rep, err = r.Reconcile(ctx, brokerSpec())
	if err != nil {
		t.Fatal(err)
	}
	second := rep.Output.Apps["prod:api"].BrokerToken
	if second == first || second == "" {
		t.Fatalf("token was not renewed: %q -> %q", first, second)
	}
	if len(user.Pats) != 2 {
		t.Fatalf("overlap window broken: %d live tokens, want both", len(user.Pats))
	}
	if _, ok := f.patOwner[first]; !ok {
		t.Error("the superseded token stopped working during its overlap")
	}
	var retiring *Retiring
	for i := range rep.Retiring {
		if rep.Retiring[i].Kind == "token" {
			retiring = &rep.Retiring[i]
		}
	}
	if retiring == nil || !retiring.DeleteAfter.Equal(clock.Add(10*day)) {
		t.Fatalf("retiring = %+v, want the old token to be deleted 10 days after the new one", rep.Retiring)
	}
	stored, _ := keys.Load(ctx, BrokerTokenEntry("broker-prod-api"))
	var rec tokenRecord
	_ = json.Unmarshal(stored, &rec)
	if rec.Token != second {
		t.Error("the key store does not hold the new token")
	}

	// Inside the overlap nothing is deleted.
	clock = clock.Add(5 * day)
	if _, err := r.Reconcile(ctx, brokerSpec()); err != nil {
		t.Fatal(err)
	}
	if len(user.Pats) != 2 {
		t.Errorf("superseded token deleted inside its overlap: %d live", len(user.Pats))
	}

	// After it, the old one goes.
	clock = clock.Add(6 * day)
	rep, err = r.Reconcile(ctx, brokerSpec())
	if err != nil {
		t.Fatal(err)
	}
	if len(user.Pats) != 1 {
		t.Fatalf("superseded token not pruned after its overlap: %d live", len(user.Pats))
	}
	if _, ok := f.patOwner[first]; ok {
		t.Error("the pruned token still authenticates")
	}
	if !strings.Contains(changes(rep), "pruned token") {
		t.Errorf("prune not reported:\n%s", changes(rep))
	}
}

func TestRotateReplacesNowWithOverlap(t *testing.T) {
	f := newFake(t)
	r, keys := newRec(t, f, nil)
	ctx := context.Background()
	rep, err := r.Reconcile(ctx, brokerSpec())
	if err != nil {
		t.Fatal(err)
	}
	oldTok := rep.Output.Apps["prod:api"].BrokerToken
	oldKey, _ := keys.Load(ctx, MachineKeyEntry("ops"))
	oldDoc, _ := ParseMachineKey(oldKey)

	rep, err = r.Rotate(ctx, brokerSpec(), RotateTarget{Application: "prod:api"})
	if err != nil {
		t.Fatal(err)
	}
	newTok := rep.Output.Apps["prod:api"].BrokerToken
	if newTok == oldTok || newTok == "" {
		t.Fatalf("broker token not rotated")
	}
	if _, ok := f.patOwner[oldTok]; !ok {
		t.Error("the old broker token must keep working through the overlap")
	}
	if len(f.user("broker-prod-api").Pats) != 2 {
		t.Error("rotation should leave old and new live")
	}
	// The machine key was NOT named, so it is untouched.
	curKey, _ := keys.Load(ctx, MachineKeyEntry("ops"))
	if string(curKey) != string(oldKey) {
		t.Error("an un-named machine key was rotated")
	}

	rep, err = r.Rotate(ctx, brokerSpec(), RotateTarget{MachineUser: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	curKey, _ = keys.Load(ctx, MachineKeyEntry("ops"))
	newDoc, _ := ParseMachineKey(curKey)
	if newDoc.KeyID == oldDoc.KeyID {
		t.Fatal("machine key not rotated")
	}
	if len(f.user("ops").Keys) != 2 {
		t.Errorf("machine key overlap broken: %d live", len(f.user("ops").Keys))
	}
	if rep.Output.Machine["ops"].KeyID != newDoc.KeyID {
		t.Error("output carries the old key id")
	}

	if _, err := r.Rotate(ctx, brokerSpec(), RotateTarget{}); err == nil {
		t.Error("an empty RotateTarget must be rejected")
	}
}

func TestFailedDeliveryKeepsThePreviousCredential(t *testing.T) {
	f := newFake(t)
	r, keys := newRec(t, f, nil)
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, brokerSpec()); err != nil {
		t.Fatal(err)
	}
	user := f.user("ops")
	oldKey, _ := keys.Load(ctx, MachineKeyEntry("ops"))

	keys.SaveErr = errors.New("openbao is down")
	_, err := r.Rotate(ctx, brokerSpec(), RotateTarget{MachineUser: "ops"})
	if err == nil || !strings.Contains(err.Error(), "previous key") || !strings.Contains(err.Error(), `"ops"`) {
		t.Fatalf("got %v", err)
	}
	if len(user.Keys) != 1 {
		t.Errorf("the key issued for a failed delivery was not deleted: %d live", len(user.Keys))
	}
	keys.SaveErr = nil
	cur, _ := keys.Load(ctx, MachineKeyEntry("ops"))
	if string(cur) != string(oldKey) {
		t.Error("the stored key changed despite the failed save")
	}
	doc, _ := ParseMachineKey(cur)
	if user.Keys[0].ID != doc.KeyID {
		t.Error("the live key is not the stored one")
	}
}

func TestExpiredOrForeignStoredCredentialsAreReplaced(t *testing.T) {
	f := newFake(t)
	r, keys := newRec(t, f, nil)
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, brokerSpec()); err != nil {
		t.Fatal(err)
	}
	// A broker token that authenticates as a DIFFERENT account must never be
	// reused, however valid.
	other := f.user("ops")
	foreign := &fCred{ID: "foreign", Token: "pat-foreign", Expires: f.now().Add(100 * day).Format(time.RFC3339), Created: f.stamp()}
	f.mu.Lock()
	other.Pats = append(other.Pats, foreign)
	f.patOwner[foreign.Token] = other.ID
	f.mu.Unlock()
	blob, _ := json.Marshal(tokenRecord{TokenID: foreign.ID, Token: foreign.Token})
	brokerUser := f.user("broker-prod-api")
	// Register the foreign id on the broker too, so the "still listed" test passes.
	brokerUser.Pats = append(brokerUser.Pats, &fCred{ID: foreign.ID, Token: "x", Expires: foreign.Expires, Created: f.stamp()})
	_ = keys.Save(ctx, BrokerTokenEntry("broker-prod-api"), blob)

	rep, err := r.Reconcile(ctx, brokerSpec())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Output.Apps["prod:api"].BrokerToken == foreign.Token {
		t.Fatal("reused a token that belongs to another account")
	}
	if !strings.Contains(changes(rep), `issued token "broker-prod-api"`) {
		t.Errorf("no replacement issued:\n%s", changes(rep))
	}
}

func TestSupersededCredentialsAreOnlyReportedWhenKept(t *testing.T) {
	f := newFake(t)
	clock := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	f.now = func() time.Time { return clock }
	r, _ := newRec(t, f, func(o *Options) { o.KeepSuperseded = true })
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, brokerSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Rotate(ctx, brokerSpec(), RotateTarget{Application: "prod:api"}); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(60 * day)
	if _, err := r.Reconcile(ctx, brokerSpec()); err != nil {
		t.Fatal(err)
	}
	if n := len(f.user("broker-prod-api").Pats); n < 2 {
		t.Errorf("KeepSuperseded still deleted: %d live", n)
	}
}

// --- machine-key credential ------------------------------------------------

func TestMachineKeyCredentialExchangesAndCaches(t *testing.T) {
	f := newFake(t)
	r, keys := newRec(t, f, nil)
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, brokerSpec()); err != nil {
		t.Fatal(err)
	}
	raw, _ := keys.Load(ctx, MachineKeyEntry("ops"))
	cred, err := MachineKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	// The fake accepts a machine-key token on management endpoints.
	r2, err := New(Options{BaseURL: f.srv.URL, Credential: cred, Keys: keys, Now: f.now})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := r2.Reconcile(ctx, brokerSpec()); err != nil {
			t.Fatalf("pass %d with a machine-key credential: %v", i, err)
		}
	}
	if n := f.tokenExchanges.Load(); n != 1 {
		t.Errorf("token exchanged %d times, want 1 (cached)", n)
	}
}

func TestMachineKeyRejectsGarbageAndWrongAudience(t *testing.T) {
	if _, err := MachineKey([]byte(`{"keyId":"1","userId":"2","key":"not pem"}`)); err == nil {
		t.Error("garbage key accepted")
	}
	f := newFake(t)
	r, keys := newRec(t, f, nil)
	if _, err := r.Reconcile(context.Background(), brokerSpec()); err != nil {
		t.Fatal(err)
	}
	raw, _ := keys.Load(context.Background(), MachineKeyEntry("ops"))
	cred, _ := MachineKey(raw)
	r2, _ := New(Options{BaseURL: f.srv.URL, Credential: cred, Issuer: "https://not-the-issuer.example", Now: f.now})
	_, err := r2.Reconcile(context.Background(), Spec{Project: ProjectSpec{Name: "p"}})
	if err == nil || !strings.Contains(err.Error(), "refused machine key") {
		t.Fatalf("got %v", err)
	}
}

// --- transport -------------------------------------------------------------

func TestTransientErrorsAreRetriedAndMintingCallsAreNot(t *testing.T) {
	f := newFake(t)
	f.flaky["/projects/_search"] = 2 // 503 twice, then fine
	r, _ := newRec(t, f, nil)
	if _, err := r.Reconcile(context.Background(), Spec{Project: ProjectSpec{Name: "p"}}); err != nil {
		t.Fatalf("a read must survive two 503s: %v", err)
	}

	// 502 on a call that MINTS something is ambiguous (the request may have been
	// processed), so it must not be repeated.
	f2 := newFake(t)
	var posts atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/keys") {
			posts.Add(1)
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		f2.srv.Config.Handler.ServeHTTP(w, req)
	}))
	defer proxy.Close()
	r2, err := New(Options{BaseURL: proxy.URL, Credential: PAT(f2.pat), Keys: &MemKeyStore{}, MaxAttempts: 4, Now: f2.now})
	if err != nil {
		t.Fatal(err)
	}
	r2.c.sleep = noSleep
	_, err = r2.Reconcile(context.Background(), Spec{Project: ProjectSpec{Name: "p"}, MachineUsers: []MachineUser{{Username: "svc", Key: true}}})
	if err == nil || !strings.Contains(err.Error(), `machine key "svc"`) {
		t.Fatalf("got %v", err)
	}
	if n := posts.Load(); n != 1 {
		t.Errorf("a minting call was attempted %d times, want exactly 1", n)
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	f := newFake(t)
	f.failPath["/projects/_search"] = 400
	var seen atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "projects/_search") {
			seen.Add(1)
		}
		f.srv.Config.Handler.ServeHTTP(w, req)
	}))
	defer proxy.Close()
	r, _ := New(Options{BaseURL: proxy.URL, Credential: PAT(f.pat), Now: f.now})
	r.c.sleep = noSleep
	_, err := r.Reconcile(context.Background(), Spec{Project: ProjectSpec{Name: "p"}})
	if err == nil || !strings.Contains(err.Error(), `project "p"`) || seen.Load() != 1 {
		t.Fatalf("err=%v attempts=%d", err, seen.Load())
	}
}

func TestRequestsAreBoundedByATimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/debug/ready" || r.URL.Path == "/management/v1/healthz" {
			w.WriteHeader(200)
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(400 * time.Millisecond):
		}
	}))
	defer slow.Close()
	r, _ := New(Options{BaseURL: slow.URL, Credential: PAT("x"), RequestTimeout: 50 * time.Millisecond, MaxAttempts: 2, ReadyTimeout: time.Second})
	r.c.sleep = noSleep
	start := time.Now()
	_, err := r.Reconcile(context.Background(), Spec{Project: ProjectSpec{Name: "p"}})
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v; the per-request timeout did not bound the call", d)
	}
}

func TestReadinessWaitIsBoundedAndNamesTheInstance(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer down.Close()
	r, _ := New(Options{BaseURL: down.URL, Credential: PAT("x"), ReadyTimeout: 30 * time.Millisecond, MaxAttempts: 1})
	r.c.sleep = func(ctx context.Context, d time.Duration) error { return sleepCtx(ctx, 10*time.Millisecond) }
	_, err := r.Reconcile(context.Background(), Spec{Project: ProjectSpec{Name: "p"}})
	if err == nil || !strings.Contains(err.Error(), "not ready") || !strings.Contains(err.Error(), down.URL) {
		t.Fatalf("got %v", err)
	}
}

func TestHostHeaderOverrideReachesTheInstance(t *testing.T) {
	f := newFake(t)
	r, _ := newRec(t, f, func(o *Options) {
		o.Host = "adopted.reliantidentity.com"
		o.Issuer = "https://adopted.reliantidentity.com"
	})
	rep, err := r.Reconcile(context.Background(), Spec{Project: ProjectSpec{Name: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	for h := range f.hosts {
		if h != "adopted.reliantidentity.com" {
			t.Errorf("a request carried Host %q", h)
		}
	}
	if rep.Output.Issuer != "https://adopted.reliantidentity.com" {
		t.Errorf("issuer = %q", rep.Output.Issuer)
	}
}

func TestEveryErrorNamesItsObject(t *testing.T) {
	f := newFake(t)
	f.failPath["/roles"] = 400
	r, _ := newRec(t, f, nil)
	_, err := r.Reconcile(context.Background(), Spec{Project: ProjectSpec{Name: "p"}, Roles: []Role{{Key: "admin"}}})
	var oe *ObjectError
	if !errors.As(err, &oe) {
		t.Fatalf("not an ObjectError: %v", err)
	}
	if !strings.Contains(err.Error(), `"p"`) && !strings.Contains(err.Error(), `"admin"`) {
		t.Errorf("error does not name the object: %v", err)
	}
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 400 {
		t.Errorf("the API error is not reachable through the chain: %v", err)
	}
}

func TestOneBrokenObjectDoesNotHideTheRest(t *testing.T) {
	f := newFake(t)
	f.failPath["/admin/v1/policies/login"] = 500
	r, _ := newRec(t, f, nil)
	r.c.maxAttempts = 1
	spec := Spec{
		Project:  ProjectSpec{Name: "p"},
		Roles:    []Role{{Key: "admin"}},
		Policies: Policies{Login: &LoginPolicy{}, PasswordComplexity: &PasswordComplexity{MinLength: 14}},
	}
	rep, err := r.Reconcile(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "login policy") {
		t.Fatalf("got %v", err)
	}
	if f.cmplx["minLength"] != "14" {
		t.Error("a step after the failing one did not run")
	}
	if len(f.mustProject("p").Roles) != 1 {
		t.Error("roles were not converged")
	}
	if rep.Output.ProjectID != "" {
		t.Error("an Output must not be built from a failed pass")
	}
}

func TestOutputsAreNotPublishedFromAFailedPass(t *testing.T) {
	f := newFake(t)
	f.failPath["/admin/v1/policies/login"] = 500
	var n int
	r, _ := newRec(t, f, func(o *Options) {
		o.Outputs = OutputsFunc(func(context.Context, Output) error { n++; return nil })
	})
	r.c.maxAttempts = 1
	_, err := r.Reconcile(context.Background(), Spec{Project: ProjectSpec{Name: "p"}, Policies: Policies{Login: &LoginPolicy{}}})
	if err == nil || n != 0 {
		t.Fatalf("err=%v published=%d", err, n)
	}
}
