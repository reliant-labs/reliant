package identity

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeZitadel is a stateful in-memory model of the slice of Zitadel's API the
// library uses. Its response shapes were checked against a real v4.19.4
// instance: 64-bit integers arrive as strings, zero values are omitted from
// policy reads, and an update that changes nothing is refused with the
// COMMAND-* / INSTANCE-* "not changed" ids. The docker smoke test is what
// proves the model; this is what makes the logic testable in milliseconds.
type fakeZitadel struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex

	now   func() time.Time
	seq   int
	orgID string
	pat   string

	projects map[string]*fProject
	users    map[string]*fUser
	grants   map[string]*fGrant
	members  map[string][]string
	login    map[string]any
	cmplx    map[string]any
	lockout  map[string]any
	smtp     []*fSMTP
	idps     []*fIdP
	// loginIDPs and secondFactors are the login policy's lists, which have
	// their own endpoints and are not part of the policy's PUT.
	loginIDPs     []string
	secondFactors []string
	generators    map[string]map[string]any
	// hideSigningKey makes the email provider list omit the HTTP signing key.
	hideSigningKey bool
	invites        map[string]int // user id -> invite codes sent

	keyOwner  map[string]string // key id -> user id
	patOwner  map[string]string // token -> user id
	signKey   *rsa.PrivateKey
	issuerURL string

	writes   int
	log      []string
	hosts    map[string]bool
	flaky    map[string]int // path substring -> remaining 503s
	failPath map[string]int // path substring -> HTTP status to return always
	smallPg  bool
	// projectionLag makes the v2 user reads of a human created through the
	// fake answer 404 "User could not be found", as real Zitadel does while
	// its projections trail the write.
	projectionLag bool
	unprojected   map[string]bool // user id -> created, not yet readable

	tokenExchanges atomic.Int32
}

type fProject struct {
	ID, Name                       string
	Assert, MustHoldRole, HasCheck bool
	Roles                          []roleObj
	Apps                           []*fApp
}

type fApp struct {
	ID, Name, ClientID, Secret string
	Config                     map[string]any
}

type fUser struct {
	ID, UserName    string
	Machine         bool
	Name, Desc, ATT string
	Email           string
	Keys, Pats      []*fCred
	AuthMethods     []string
	PasswordSetBy   string
	Given, Family   string
	// Unverified is the email's isVerified, negated: an account a test
	// seeds is verified unless it says otherwise.
	Unverified bool
	// ResetCode and EmailCode are the outstanding password-reset and
	// email-verification codes.
	ResetCode, EmailCode string
	Metadata             map[string]string
}

type fCred struct {
	ID, Token string
	Expires   string
	Created   string
	Pub       *rsa.PublicKey
}

type fGrant struct {
	ID, UserID, ProjectID string
	Roles                 []string
}

type fSMTP struct {
	ID, Sender, SenderName, Host, User, Desc, ReplyTo, Password, State string
	TLS                                                                bool
	// HTTP marks an HTTP email provider (Endpoint, SigningKey).
	HTTP                 bool
	Endpoint, SigningKey string
}

type fIdP struct {
	ID, Name, Type, ClientID, Secret string
	Scopes                           []string
	Options                          map[string]any
}

var (
	testKeyOnce sync.Once
	testKey     *rsa.PrivateKey
)

func sharedKey(t testing.TB) *rsa.PrivateKey {
	testKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 1024)
		if err != nil {
			t.Fatal(err)
		}
		testKey = k
	})
	return testKey
}

func newFake(t *testing.T) *fakeZitadel {
	t.Helper()
	f := &fakeZitadel{
		t: t, now: time.Now, orgID: "1000", pat: "owner-pat",
		projects: map[string]*fProject{}, users: map[string]*fUser{}, grants: map[string]*fGrant{},
		members: map[string][]string{}, keyOwner: map[string]string{}, patOwner: map[string]string{},
		hosts: map[string]bool{}, flaky: map[string]int{}, failPath: map[string]int{},
		login: map[string]any{"allowUsernamePassword": true, "allowRegister": true, "allowExternalIdp": true,
			"passwordlessType": "PASSWORDLESS_TYPE_ALLOWED", "mfaInitSkipLifetime": "2592000s", "passwordCheckLifetime": "864000s"},
		cmplx:   map[string]any{"minLength": "8", "hasUppercase": true, "hasLowercase": true, "hasNumber": true, "hasSymbol": true},
		lockout: map[string]any{},
		signKey: sharedKey(t),
		// A fresh Zitadel instance enables these second factors at setup.
		secondFactors: []string{"SECOND_FACTOR_TYPE_OTP", "SECOND_FACTOR_TYPE_U2F", "SECOND_FACTOR_TYPE_OTP_EMAIL"},
		generators: map[string]map[string]any{
			"SECRET_GENERATOR_TYPE_OTP_EMAIL": {"length": float64(8), "expiry": "300s", "includeDigits": true},
		},
		invites: map[string]int{},
	}
	// The ZITADEL system project every instance has.
	f.projects["sys"] = &fProject{ID: "sys", Name: "ZITADEL"}
	f.users["setup"] = &fUser{ID: "setup", UserName: "forge-setup", Machine: true}
	f.members["setup"] = []string{"IAM_OWNER"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	f.issuerURL = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeZitadel) id() string { f.seq++; return strconv.Itoa(2000 + f.seq) }

func (f *fakeZitadel) stamp() string { return f.now().UTC().Format(time.RFC3339Nano) }

// mutate records a write.
func (f *fakeZitadel) mutate(what string) { f.writes++; f.log = append(f.log, what) }

func (f *fakeZitadel) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

func (f *fakeZitadel) mustProject(name string) *fProject {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.projects {
		if p.Name == name {
			return p
		}
	}
	f.t.Fatalf("fake has no project %q", name)
	return nil
}

func (f *fakeZitadel) user(name string) *fUser {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.UserName == name {
			return u
		}
	}
	return nil
}

// seedProject installs a pre-existing project (adoption tests).
func (f *fakeZitadel) seedProject(id, name string) *fProject {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := &fProject{ID: id, Name: name, Assert: true}
	f.projects[id] = p
	return p
}

func (f *fakeZitadel) seedApp(p *fProject, id, name, clientID string, cfg map[string]any) *fApp {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := &fApp{ID: id, Name: name, ClientID: clientID, Config: cfg}
	p.Apps = append(p.Apps, a)
	return a
}

// ---------------------------------------------------------------------------

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func zerr(w http.ResponseWriter, status, code int, id, msg string) {
	reply(w, status, map[string]any{
		"code": code, "message": msg + " (" + id + ")",
		"details": []any{map[string]any{"@type": "type.googleapis.com/zitadel.v1.ErrorDetail", "id": id, "message": msg}},
	})
}

func notChanged(w http.ResponseWriter, id string) { zerr(w, 400, 9, id, "not changed") }

func decode(r *http.Request) map[string]any {
	m := map[string]any{}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&m)
	}
	return m
}

func details() map[string]any {
	return map[string]any{"sequence": "1", "resourceOwner": "1000"}
}

// pageOf honours query.offset/query.limit as Zitadel does.
func (f *fakeZitadel) pageOf(body map[string]any, n int) (lo, hi int) {
	q, _ := body["query"].(map[string]any)
	off, _ := q["offset"].(float64)
	lim, _ := q["limit"].(float64)
	if lim == 0 {
		lim = 100
	}
	lo = int(off)
	if lo > n {
		lo = n
	}
	hi = lo + int(lim)
	if hi > n {
		hi = n
	}
	return lo, hi
}

func (f *fakeZitadel) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hosts[r.Host] = true
	f.log = append(f.log, r.Method+" "+r.URL.Path)

	for sub, left := range f.flaky {
		if left > 0 && strings.Contains(r.URL.Path, sub) {
			f.flaky[sub]--
			zerr(w, 503, 14, "SVC-1", "unavailable")
			return
		}
	}
	for sub, status := range f.failPath {
		if strings.Contains(r.URL.Path, sub) {
			zerr(w, status, 13, "SVC-2", "forced failure")
			return
		}
	}

	switch r.URL.Path {
	case "/debug/ready":
		reply(w, 200, "ok")
		return
	case "/.well-known/openid-configuration":
		reply(w, 200, map[string]any{"issuer": f.issuerURL})
		return
	case "/oauth/v2/keys":
		reply(w, 200, map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256", "n": "AQAB", "e": "AQAB"}}})
		return
	case "/oauth/v2/token":
		f.token(w, r)
		return
	case "/auth/v1/users/me":
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		uid, ok := f.patOwner[tok]
		if !ok && tok != f.pat {
			zerr(w, 401, 16, "AUTH-1", "invalid token")
			return
		}
		if tok == f.pat {
			uid = "setup"
		}
		reply(w, 200, map[string]any{"user": map[string]any{"id": uid}})
		return
	}

	auth := r.Header.Get("Authorization")
	if auth != "Bearer "+f.pat && !strings.HasPrefix(auth, "Bearer mk-token-") {
		zerr(w, 401, 16, "AUTH-1", "invalid token")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/management/") && r.URL.Path != "/management/v1/orgs/me" && r.Header.Get("x-zitadel-orgid") != f.orgID {
		zerr(w, 400, 3, "AUTH-2", "Resource Owner Organisation missing")
		return
	}

	switch {
	case r.URL.Path == "/management/v1/orgs/me" || r.URL.Path == "/v2/organizations/_search":
		f.orgs(w, r)
	case strings.HasPrefix(r.URL.Path, "/management/v1/projects"):
		f.projectAPI(w, r)
	case strings.HasPrefix(r.URL.Path, "/management/v1/users/grants/"):
		f.grantSearch(w, r)
	case strings.HasPrefix(r.URL.Path, "/management/v1/users"):
		f.userAPI(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin/v1/members"):
		f.memberAPI(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin/v1/policies"):
		f.policyAPI(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin/v1/smtp"):
		f.smtpAPI(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin/v1/email"):
		f.emailAPI(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin/v1/idps"):
		f.idpAPI(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin/v1/secretgenerators/"):
		f.generatorAPI(w, r)
	case strings.HasPrefix(r.URL.Path, "/v2/users"):
		f.v2Users(w, r)
	default:
		f.t.Errorf("fake: unhandled %s %s", r.Method, r.URL.Path)
		zerr(w, 404, 5, "FAKE-1", "unhandled")
	}
}

func (f *fakeZitadel) orgs(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/management/v1/orgs/me" {
		reply(w, 200, map[string]any{"org": map[string]any{"id": f.orgID, "name": "org"}})
		return
	}
	body := decode(r)
	res := []any{map[string]any{"id": f.orgID, "name": "org"}}
	if qs, _ := body["queries"].([]any); len(qs) > 0 {
		res = nil // name query that matches nothing unless it is "org"
		q := qs[0].(map[string]any)["nameQuery"].(map[string]any)
		if q["name"] == "org" {
			res = []any{map[string]any{"id": f.orgID, "name": "org"}}
		}
	}
	reply(w, 200, map[string]any{"result": res})
}

func (f *fakeZitadel) token(w http.ResponseWriter, r *http.Request) {
	f.tokenExchanges.Add(1)
	_ = r.ParseForm()
	parts := strings.Split(r.PostForm.Get("assertion"), ".")
	if len(parts) != 3 {
		reply(w, 400, map[string]any{"error": "invalid_request", "error_description": "no assertion"})
		return
	}
	var hdr struct{ Kid string }
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	_ = json.Unmarshal(hb, &hdr)
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Iss, Sub string
		Aud      []string
	}
	_ = json.Unmarshal(cb, &claims)
	owner, ok := f.keyOwner[hdr.Kid]
	var pub *rsa.PublicKey
	if ok {
		for _, k := range f.users[owner].Keys {
			if k.ID == hdr.Kid {
				pub = k.Pub
			}
		}
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	switch {
	case pub == nil:
		reply(w, 400, map[string]any{"error": "invalid_grant", "error_description": "key not found"})
	case rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig) != nil:
		reply(w, 400, map[string]any{"error": "invalid_grant", "error_description": "bad signature"})
	case claims.Iss != owner || claims.Sub != owner:
		reply(w, 400, map[string]any{"error": "invalid_grant", "error_description": "iss/sub mismatch"})
	case len(claims.Aud) != 1 || claims.Aud[0] != f.issuerURL:
		reply(w, 400, map[string]any{"error": "invalid_grant", "error_description": "wrong audience"})
	default:
		reply(w, 200, map[string]any{"access_token": "mk-token-" + owner, "expires_in": 3600})
	}
}

// --- projects, roles, apps -------------------------------------------------

func (f *fakeZitadel) projectAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/management/v1/projects")
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if path == "" || path == "/" {
		segs = nil
	}
	switch {
	case len(segs) == 0 && r.Method == "POST":
		body := decode(r)
		name, _ := body["name"].(string)
		for _, p := range f.projects {
			if p.Name == name {
				zerr(w, 409, 6, "V3-DKcYh", "Project already exists on organization")
				return
			}
		}
		p := &fProject{ID: f.id(), Name: name}
		p.Assert, _ = body["projectRoleAssertion"].(bool)
		p.MustHoldRole, _ = body["projectRoleCheck"].(bool)
		p.HasCheck, _ = body["hasProjectCheck"].(bool)
		f.projects[p.ID] = p
		f.mutate("create project " + name)
		reply(w, 200, map[string]any{"id": p.ID, "details": details()})
	case len(segs) == 1 && segs[0] == "_search":
		body := decode(r)
		var all []any
		var ids []string
		for id := range f.projects {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			p := f.projects[id]
			if qs, _ := body["queries"].([]any); len(qs) > 0 {
				if q, ok := qs[0].(map[string]any)["nameQuery"].(map[string]any); ok && q["name"] != p.Name {
					continue
				}
			}
			all = append(all, projectJSON(p))
		}
		lo, hi := f.pageOf(body, len(all))
		reply(w, 200, map[string]any{"result": all[lo:hi]})
	case len(segs) == 1 && r.Method == "GET":
		p, ok := f.projects[segs[0]]
		if !ok {
			zerr(w, 404, 5, "QUERY-1", "Project not found")
			return
		}
		reply(w, 200, map[string]any{"project": projectJSON(p)})
	case len(segs) == 1 && r.Method == "PUT":
		p, ok := f.projects[segs[0]]
		if !ok {
			zerr(w, 404, 5, "QUERY-1", "Project not found")
			return
		}
		body := decode(r)
		n, _ := body["name"].(string)
		a, _ := body["projectRoleAssertion"].(bool)
		rc, _ := body["projectRoleCheck"].(bool)
		hc, _ := body["hasProjectCheck"].(bool)
		if n == p.Name && a == p.Assert && rc == p.MustHoldRole && hc == p.HasCheck {
			notChanged(w, "COMMAND-1m88i")
			return
		}
		p.Name, p.Assert, p.MustHoldRole, p.HasCheck = n, a, rc, hc
		f.mutate("update project " + n)
		reply(w, 200, map[string]any{"details": details()})
	default:
		f.projectChild(w, r, segs)
	}
}

func projectJSON(p *fProject) map[string]any {
	m := map[string]any{"id": p.ID, "name": p.Name, "state": "PROJECT_STATE_ACTIVE"}
	if p.Assert {
		m["projectRoleAssertion"] = true
	}
	if p.MustHoldRole {
		m["projectRoleCheck"] = true
	}
	if p.HasCheck {
		m["hasProjectCheck"] = true
	}
	return m
}

func (f *fakeZitadel) projectChild(w http.ResponseWriter, r *http.Request, segs []string) {
	p, ok := f.projects[segs[0]]
	if !ok {
		zerr(w, 404, 5, "QUERY-1", "Project not found")
		return
	}
	rest := strings.Join(segs[1:], "/")
	body := decode(r)
	switch {
	case rest == "roles/_search":
		var all []any
		for _, ro := range p.Roles {
			m := map[string]any{"key": ro.Key}
			if ro.DisplayName != "" {
				m["displayName"] = ro.DisplayName
			}
			if ro.Group != "" {
				m["group"] = ro.Group
			}
			all = append(all, m)
		}
		lo, hi := f.pageOf(body, len(all))
		reply(w, 200, map[string]any{"result": all[lo:hi]})
	case rest == "roles" && r.Method == "POST":
		key, _ := body["roleKey"].(string)
		for _, ro := range p.Roles {
			if ro.Key == key {
				zerr(w, 409, 6, "V3-DKcYh", "Role already exists")
				return
			}
		}
		d, _ := body["displayName"].(string)
		g, _ := body["group"].(string)
		p.Roles = append(p.Roles, roleObj{Key: key, DisplayName: d, Group: g})
		f.mutate("create role " + key)
		reply(w, 200, map[string]any{"details": details()})
	case strings.HasPrefix(rest, "roles/") && r.Method == "PUT":
		key := strings.TrimPrefix(rest, "roles/")
		for i := range p.Roles {
			if p.Roles[i].Key == key {
				d, _ := body["displayName"].(string)
				g, _ := body["group"].(string)
				if p.Roles[i].DisplayName == d && p.Roles[i].Group == g {
					notChanged(w, "COMMAND-1m88i")
					return
				}
				p.Roles[i].DisplayName, p.Roles[i].Group = d, g
				f.mutate("update role " + key)
				reply(w, 200, map[string]any{"details": details()})
				return
			}
		}
		zerr(w, 404, 5, "COMMAND-2", "Role not found")
	case rest == "apps/_search":
		var all []any
		for _, a := range p.Apps {
			oc := map[string]any{}
			for k, v := range a.Config {
				oc[k] = v
			}
			oc["clientId"] = a.ClientID
			// The gateway omits an enum's zero value.
			if oc["appType"] == "OIDC_APP_TYPE_WEB" {
				delete(oc, "appType")
			}
			if oc["authMethodType"] == "OIDC_AUTH_METHOD_TYPE_BASIC" {
				delete(oc, "authMethodType")
			}
			all = append(all, map[string]any{"id": a.ID, "name": a.Name, "state": "APP_STATE_ACTIVE", "oidcConfig": oc})
		}
		lo, hi := f.pageOf(body, len(all))
		reply(w, 200, map[string]any{"result": all[lo:hi]})
	case rest == "apps/oidc" && r.Method == "POST":
		name, _ := body["name"].(string)
		for _, a := range p.Apps {
			if a.Name == name {
				zerr(w, 409, 6, "V3-DKcYh", "Application already exists")
				return
			}
		}
		cfg := map[string]any{}
		for k, v := range body {
			if k != "name" {
				cfg[k] = v
			}
		}
		a := &fApp{ID: f.id(), Name: name, ClientID: f.id(), Config: cfg}
		out := map[string]any{"appId": a.ID, "clientId": a.ClientID, "details": details()}
		if cfg["authMethodType"] == "OIDC_AUTH_METHOD_TYPE_BASIC" {
			a.Secret = "secret-" + a.ID
			out["clientSecret"] = a.Secret
		}
		p.Apps = append(p.Apps, a)
		f.mutate("create app " + name)
		reply(w, 200, out)
	case strings.HasPrefix(rest, "apps/"):
		f.appUpdate(w, r, p, strings.Split(strings.TrimPrefix(rest, "apps/"), "/"), body)
	default:
		f.t.Errorf("fake: unhandled project path %s %s", r.Method, r.URL.Path)
		zerr(w, 404, 5, "FAKE-1", "unhandled")
	}
}

func (f *fakeZitadel) appUpdate(w http.ResponseWriter, r *http.Request, p *fProject, segs []string, body map[string]any) {
	var app *fApp
	for _, a := range p.Apps {
		if a.ID == segs[0] {
			app = a
		}
	}
	if app == nil {
		zerr(w, 404, 5, "COMMAND-3", "App not found")
		return
	}
	switch {
	case len(segs) == 1 && r.Method == "PUT":
		n, _ := body["name"].(string)
		if n == app.Name {
			notChanged(w, "COMMAND-1m88i")
			return
		}
		app.Name = n
		f.mutate("rename app " + n)
		reply(w, 200, map[string]any{"details": details()})
	case len(segs) == 2 && segs[1] == "oidc_config":
		if reflect.DeepEqual(normalize(body), normalize(app.Config)) {
			notChanged(w, "COMMAND-1m88i")
			return
		}
		app.Config = body
		f.mutate("update app config " + app.Name)
		reply(w, 200, map[string]any{"details": details()})
	case len(segs) == 3 && segs[2] == "_generate_client_secret":
		app.Secret = "secret2-" + app.ID + "-" + strconv.Itoa(f.seq)
		f.seq++
		f.mutate("regenerate secret " + app.Name)
		reply(w, 200, map[string]any{"clientSecret": app.Secret, "details": details()})
	default:
		zerr(w, 404, 5, "FAKE-1", "unhandled")
	}
}

// normalize round-trips through JSON so []string and []any compare equal.
func normalize(m map[string]any) map[string]any {
	raw, _ := json.Marshal(m)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

// --- users, keys, pats, grants ---------------------------------------------

func (f *fakeZitadel) userAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/management/v1/users")
	segs := strings.Split(strings.Trim(path, "/"), "/")
	body := decode(r)
	switch {
	case segs[0] == "_search":
		var all []any
		var ids []string
		for id := range f.users {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			u := f.users[id]
			if qs, _ := body["queries"].([]any); len(qs) > 0 {
				if q, ok := qs[0].(map[string]any)["userNameQuery"].(map[string]any); ok && q["userName"] != u.UserName {
					continue
				}
			}
			m := map[string]any{"id": u.ID, "userName": u.UserName}
			if u.Machine {
				mm := map[string]any{"name": u.Name, "description": u.Desc, "accessTokenType": u.ATT}
				if u.ATT == tokenTypeBearer {
					delete(mm, "accessTokenType") // zero value, omitted by the gateway
				}
				m["machine"] = mm
			} else {
				m["human"] = map[string]any{}
			}
			all = append(all, m)
		}
		lo, hi := f.pageOf(body, len(all))
		reply(w, 200, map[string]any{"result": all[lo:hi]})
	case segs[0] == "machine" && r.Method == "POST":
		name, _ := body["userName"].(string)
		for _, u := range f.users {
			if u.UserName == name {
				zerr(w, 409, 6, "V3-DKcYh", "User already exists")
				return
			}
		}
		u := &fUser{ID: f.id(), UserName: name, Machine: true}
		u.Name, _ = body["name"].(string)
		u.Desc, _ = body["description"].(string)
		u.ATT, _ = body["accessTokenType"].(string)
		f.users[u.ID] = u
		f.mutate("create machine user " + name)
		reply(w, 200, map[string]any{"userId": u.ID, "details": details()})
	default:
		u, ok := f.users[segs[0]]
		if !ok {
			zerr(w, 404, 5, "QUERY-2", "User not found")
			return
		}
		f.userChild(w, r, u, segs[1:], body)
	}
}

func (f *fakeZitadel) userChild(w http.ResponseWriter, r *http.Request, u *fUser, segs []string, body map[string]any) {
	switch {
	case len(segs) == 1 && segs[0] == "machine" && r.Method == "PUT":
		n, _ := body["name"].(string)
		d, _ := body["description"].(string)
		a, _ := body["accessTokenType"].(string)
		if n == u.Name && d == u.Desc && a == u.ATT {
			notChanged(w, "COMMAND-2n8vs")
			return
		}
		u.Name, u.Desc, u.ATT = n, d, a
		f.mutate("update machine user " + u.UserName)
		reply(w, 200, map[string]any{"details": details()})
	case segs[0] == "keys" || segs[0] == "pats":
		f.credAPI(w, r, u, segs, body)
	case segs[0] == "grants" && r.Method == "POST" && len(segs) == 1:
		pid, _ := body["projectId"].(string)
		for _, g := range f.grants {
			if g.UserID == u.ID && g.ProjectID == pid {
				zerr(w, 409, 6, "COMMAND-3", "User grant already exists")
				return
			}
		}
		g := &fGrant{ID: f.id(), UserID: u.ID, ProjectID: pid, Roles: strs(body["roleKeys"])}
		f.grants[g.ID] = g
		f.mutate("grant " + u.UserName)
		reply(w, 200, map[string]any{"userGrantId": g.ID, "details": details()})
	case segs[0] == "grants" && len(segs) == 2:
		g, ok := f.grants[segs[1]]
		if !ok {
			zerr(w, 404, 5, "COMMAND-4", "grant not found")
			return
		}
		if r.Method == "DELETE" {
			delete(f.grants, g.ID)
			f.mutate("revoke grant " + u.UserName)
			reply(w, 200, map[string]any{"details": details()})
			return
		}
		keys := strs(body["roleKeys"])
		if reflect.DeepEqual(sorted(keys), sorted(g.Roles)) {
			notChanged(w, "COMMAND-Rs8fy")
			return
		}
		g.Roles = keys
		f.mutate("update grant " + u.UserName)
		reply(w, 200, map[string]any{"details": details()})
	default:
		f.t.Errorf("fake: unhandled user path %s %s", r.Method, r.URL.Path)
		zerr(w, 404, 5, "FAKE-1", "unhandled")
	}
}

func (f *fakeZitadel) credAPI(w http.ResponseWriter, r *http.Request, u *fUser, segs []string, body map[string]any) {
	kind := segs[0]
	list := &u.Keys
	if kind == "pats" {
		list = &u.Pats
	}
	switch {
	case len(segs) == 2 && segs[1] == "_search":
		var all []any
		for _, c := range *list {
			all = append(all, map[string]any{"id": c.ID, "expirationDate": c.Expires,
				"details": map[string]any{"creationDate": c.Created}})
		}
		lo, hi := f.pageOf(body, len(all))
		reply(w, 200, map[string]any{"result": all[lo:hi]})
	case len(segs) == 1 && r.Method == "POST":
		c := &fCred{ID: f.id(), Created: f.stamp()}
		c.Expires, _ = body["expirationDate"].(string)
		out := map[string]any{"details": details()}
		if kind == "keys" {
			c.Pub = &f.signKey.PublicKey
			f.keyOwner[c.ID] = u.ID
			doc, _ := json.Marshal(MachineKeyDoc{Type: "serviceaccount", KeyID: c.ID, UserID: u.ID, Key: pemOf(f.signKey)})
			out["keyId"] = c.ID
			out["keyDetails"] = base64.StdEncoding.EncodeToString(doc)
		} else {
			c.Token = "pat-" + c.ID
			f.patOwner[c.Token] = u.ID
			out["tokenId"] = c.ID
			out["token"] = c.Token
		}
		*list = append(*list, c)
		f.mutate("issue " + kind + " " + u.UserName)
		reply(w, 200, out)
	case len(segs) == 2 && r.Method == "DELETE":
		for i, c := range *list {
			if c.ID == segs[1] {
				*list = append((*list)[:i], (*list)[i+1:]...)
				delete(f.keyOwner, c.ID)
				delete(f.patOwner, c.Token)
				f.mutate("delete " + kind + " " + u.UserName)
				reply(w, 200, map[string]any{"details": details()})
				return
			}
		}
		zerr(w, 404, 5, "COMMAND-5", "not found")
	default:
		zerr(w, 404, 5, "FAKE-1", "unhandled")
	}
}

func pemOf(k *rsa.PrivateKey) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
}

func strs(v any) []string {
	var out []string
	if arr, ok := v.([]any); ok {
		for _, x := range arr {
			out = append(out, fmt.Sprint(x))
		}
	}
	return out
}

func sorted(s []string) []string {
	c := append([]string{}, s...)
	sort.Strings(c)
	return c
}

func (f *fakeZitadel) grantSearch(w http.ResponseWriter, r *http.Request) {
	body := decode(r)
	pid := ""
	if qs, _ := body["queries"].([]any); len(qs) > 0 {
		if q, ok := qs[0].(map[string]any)["projectIdQuery"].(map[string]any); ok {
			pid, _ = q["projectId"].(string)
		}
	}
	var ids []string
	for id, g := range f.grants {
		if g.ProjectID == pid {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var all []any
	for _, id := range ids {
		g := f.grants[id]
		u := f.users[g.UserID]
		typ := "TYPE_HUMAN"
		if u.Machine {
			typ = "TYPE_MACHINE"
		}
		all = append(all, map[string]any{"id": g.ID, "userId": g.UserID, "userName": u.UserName,
			"email": u.Email, "userType": typ, "roleKeys": g.Roles})
	}
	lo, hi := f.pageOf(body, len(all))
	reply(w, 200, map[string]any{"result": all[lo:hi]})
}

// --- instance members, policies, smtp --------------------------------------

func (f *fakeZitadel) memberAPI(w http.ResponseWriter, r *http.Request) {
	body := decode(r)
	switch {
	case r.URL.Path == "/admin/v1/members/_search":
		var ids []string
		for id := range f.members {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		var all []any
		for _, id := range ids {
			all = append(all, map[string]any{"userId": id, "roles": f.members[id]})
		}
		lo, hi := f.pageOf(body, len(all))
		reply(w, 200, map[string]any{"result": all[lo:hi]})
	case r.Method == "POST":
		uid, _ := body["userId"].(string)
		if _, ok := f.members[uid]; ok {
			zerr(w, 409, 6, "INSTANCE-1", "member already exists")
			return
		}
		f.members[uid] = strs(body["roles"])
		f.mutate("add member " + uid)
		reply(w, 200, map[string]any{"details": details()})
	case r.Method == "PUT":
		uid := strings.TrimPrefix(r.URL.Path, "/admin/v1/members/")
		if _, ok := f.members[uid]; !ok {
			zerr(w, 404, 5, "INSTANCE-D8JxR", "Errors.NotFound")
			return
		}
		f.members[uid] = strs(body["roles"])
		f.mutate("update member " + uid)
		reply(w, 200, map[string]any{"details": details()})
	}
}

// omitZero mimics the gateway, which omits zero values from policy reads.
func omitZero(m map[string]any) map[string]any {
	out := map[string]any{"details": details(), "isDefault": true}
	for k, v := range m {
		switch x := v.(type) {
		case bool:
			if !x {
				continue
			}
		case string:
			if x == "" || x == "0" || x == "0s" {
				continue
			}
		case float64:
			if x == 0 {
				continue
			}
		}
		out[k] = v
	}
	return out
}

func (f *fakeZitadel) policyAPI(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.Path, "/policies/login/") {
		f.loginListAPI(w, r)
		return
	}
	var target *map[string]any
	notChangedID := ""
	switch {
	case strings.HasSuffix(r.URL.Path, "/policies/login"):
		target, notChangedID = &f.login, "COMMAND-LOGIN"
	case strings.HasSuffix(r.URL.Path, "/policies/password/complexity"):
		target, notChangedID = &f.cmplx, "INSTANCE-9jlsf"
	case strings.HasSuffix(r.URL.Path, "/policies/lockout") && r.Method == "GET":
		target = &f.lockout
	case strings.HasSuffix(r.URL.Path, "/policies/password/lockout") && r.Method == "PUT":
		target, notChangedID = &f.lockout, "INSTANCE-0psjF"
	default:
		f.t.Errorf("fake: unhandled policy path %s %s", r.Method, r.URL.Path)
		zerr(w, 404, 5, "FAKE-1", "unhandled")
		return
	}
	if r.Method == "GET" {
		pol := omitZero(*target)
		if target == &f.login {
			if len(f.secondFactors) > 0 {
				pol["secondFactors"] = append([]string{}, f.secondFactors...)
			}
			var links []any
			for _, id := range f.loginIDPs {
				for _, i := range f.idps {
					if i.ID == id {
						links = append(links, map[string]any{"idpId": i.ID, "idpName": i.Name, "idpType": "IDP_TYPE_OIDC"})
					}
				}
			}
			if links != nil {
				pol["idps"] = links
			}
		}
		reply(w, 200, map[string]any{"policy": pol})
		return
	}
	body := decode(r)
	if target == &f.login {
		for _, k := range []string{"idps", "secondFactors", "multiFactors"} {
			if _, ok := body[k]; ok {
				zerr(w, 400, 3, "GATEWAY-1", "unknown field "+k)
				return
			}
		}
	}
	// The gateway renders lockout numbers back as strings; normalize both sides.
	next := map[string]any{}
	for k, v := range body {
		switch x := v.(type) {
		case float64:
			next[k] = strconv.Itoa(int(x))
		default:
			next[k] = v
		}
	}
	if reflect.DeepEqual(omitZero(next), omitZero(*target)) {
		notChanged(w, notChangedID)
		return
	}
	*target = next
	f.mutate("update policy " + r.URL.Path)
	reply(w, 200, map[string]any{"details": details()})
}

func (f *fakeZitadel) smtpAPI(w http.ResponseWriter, r *http.Request) {
	body := decode(r)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/v1/smtp"), "/")
	switch {
	case rest == "_search":
		var all []any
		for _, s := range f.smtp {
			all = append(all, map[string]any{"id": s.ID, "senderAddress": s.Sender, "senderName": s.SenderName,
				"tls": s.TLS, "host": s.Host, "user": s.User, "state": s.State, "description": s.Desc, "replyToAddress": s.ReplyTo})
		}
		reply(w, 200, map[string]any{"result": all})
	case rest == "" && r.Method == "POST":
		s := &fSMTP{ID: f.id(), State: "SMTP_CONFIG_INACTIVE"}
		fillSMTP(s, body)
		s.Password, _ = body["password"].(string)
		f.smtp = append(f.smtp, s)
		f.mutate("create smtp")
		reply(w, 200, map[string]any{"id": s.ID, "details": details()})
	default:
		parts := strings.Split(rest, "/")
		var s *fSMTP
		for _, x := range f.smtp {
			if x.ID == parts[0] {
				s = x
			}
		}
		if s == nil {
			zerr(w, 404, 5, "SMTP-1", "smtp not found")
			return
		}
		switch {
		case len(parts) == 1 && r.Method == "PUT":
			before := *s
			fillSMTP(s, body)
			if *s == before {
				return200(w)
				return
			}
			f.mutate("update smtp")
			return200(w)
		case len(parts) == 2 && parts[1] == "password":
			s.Password, _ = body["password"].(string)
			f.mutate("set smtp password")
			return200(w)
		case len(parts) == 2 && parts[1] == "_activate":
			if s.State == "SMTP_CONFIG_ACTIVE" {
				zerr(w, 400, 9, "COMMAND-vUHBSmBzaw", "Errors.SMTPConfig.AlreadyActive")
				return
			}
			for _, x := range f.smtp {
				x.State = "SMTP_CONFIG_INACTIVE"
			}
			s.State = "SMTP_CONFIG_ACTIVE"
			f.mutate("activate smtp")
			return200(w)
		}
	}
}

func return200(w http.ResponseWriter) { reply(w, 200, map[string]any{"details": details()}) }

func fillSMTP(s *fSMTP, b map[string]any) {
	s.Sender, _ = b["senderAddress"].(string)
	s.SenderName, _ = b["senderName"].(string)
	s.Host, _ = b["host"].(string)
	s.User, _ = b["user"].(string)
	s.Desc, _ = b["description"].(string)
	s.ReplyTo, _ = b["replyToAddress"].(string)
	s.TLS, _ = b["tls"].(bool)
}

// --- v2 users --------------------------------------------------------------

func (f *fakeZitadel) v2Users(w http.ResponseWriter, r *http.Request) {
	body := decode(r)
	path := strings.TrimPrefix(r.URL.Path, "/v2/users")
	switch {
	case path == "" && r.Method == "POST":
		var hits []any
		if qs, _ := body["queries"].([]any); len(qs) > 0 {
			q := qs[0].(map[string]any)["emailQuery"].(map[string]any)
			want := strings.ToLower(fmt.Sprint(q["emailAddress"]))
			var ids []string
			for id := range f.users {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				if u := f.users[id]; !u.Machine && strings.ToLower(u.Email) == want {
					hits = append(hits, map[string]any{"userId": u.ID, "username": u.UserName,
						"human": map[string]any{"email": map[string]any{"email": u.Email, "isVerified": !u.Unverified}}})
				}
			}
		}
		lo, hi := f.pageOf(body, len(hits))
		reply(w, 200, map[string]any{"result": hits[lo:hi]})
	case path == "/human" && r.Method == "POST":
		email, _ := body["username"].(string)
		u := &fUser{ID: f.id(), UserName: email, Email: email}
		if em, ok := body["email"].(map[string]any); ok {
			verified, _ := em["isVerified"].(bool)
			u.Unverified = !verified
		}
		if prof, ok := body["profile"].(map[string]any); ok {
			u.Given, _ = prof["givenName"].(string)
			u.Family, _ = prof["familyName"].(string)
		}
		if pw, ok := body["password"].(map[string]any); ok {
			u.PasswordSetBy, _ = pw["password"].(string)
			u.AuthMethods = []string{"AUTHENTICATION_METHOD_TYPE_PASSWORD"}
		}
		f.users[u.ID] = u
		if f.projectionLag {
			if f.unprojected == nil {
				f.unprojected = map[string]bool{}
			}
			f.unprojected[u.ID] = true
		}
		f.mutate("create human " + email)
		reply(w, 200, map[string]any{"userId": u.ID, "details": details()})
	case strings.HasSuffix(path, "/authentication_methods"):
		id := strings.Trim(strings.TrimSuffix(path, "/authentication_methods"), "/")
		u := f.users[id]
		if u == nil || f.unprojected[id] {
			zerr(w, 404, 5, "QUERY-Dfbg2", "User could not be found")
			return
		}
		reply(w, 200, map[string]any{"authMethodTypes": u.AuthMethods})
	case strings.HasPrefix(path, "/human/") && r.Method == "PUT":
		u := f.users[strings.TrimPrefix(path, "/human/")]
		if u == nil {
			zerr(w, 404, 5, "QUERY-Dfbg2", "User could not be found")
			return
		}
		if name, _ := body["username"].(string); name != "" {
			u.UserName = name
		}
		f.mutate("rename user " + u.ID)
		return200(w)
	case strings.HasSuffix(path, "/email/resend") && r.Method == "POST":
		u := f.users[strings.Trim(strings.TrimSuffix(path, "/email/resend"), "/")]
		if u == nil {
			zerr(w, 404, 5, "QUERY-Dfbg2", "User could not be found")
			return
		}
		u.EmailCode = "EVR" + strconv.Itoa(f.seq)
		f.seq++
		f.mutate("resend email code " + u.ID)
		reply(w, 200, map[string]any{"verificationCode": u.EmailCode, "details": details()})
	case strings.HasSuffix(path, "/email/verify") && r.Method == "POST":
		u := f.users[strings.Trim(strings.TrimSuffix(path, "/email/verify"), "/")]
		code, _ := body["verificationCode"].(string)
		if u == nil || u.EmailCode == "" || code != u.EmailCode {
			zerr(w, 400, 3, "CODE-woT0xc", "Code is invalid")
			return
		}
		u.Unverified, u.EmailCode = false, ""
		f.mutate("verify email " + u.ID)
		return200(w)
	case strings.HasSuffix(path, "/email") && r.Method == "POST":
		u := f.users[strings.Trim(strings.TrimSuffix(path, "/email"), "/")]
		if u == nil {
			zerr(w, 404, 5, "QUERY-Dfbg2", "User could not be found")
			return
		}
		if e, _ := body["email"].(string); strings.EqualFold(e, u.Email) {
			zerr(w, 400, 9, "COMMAND-Uch5e", "Email not changed")
			return
		}
		u.Email, _ = body["email"].(string)
		u.Unverified = true
		f.mutate("change email " + u.ID)
		reply(w, 200, map[string]any{"verificationCode": "EV1", "details": details()})
	case strings.HasSuffix(path, "/metadata") && r.Method == "POST":
		u := f.users[strings.Trim(strings.TrimSuffix(path, "/metadata"), "/")]
		if u == nil {
			zerr(w, 404, 5, "QUERY-Dfbg2", "User could not be found")
			return
		}
		if u.Metadata == nil {
			u.Metadata = map[string]string{}
		}
		md, _ := body["metadata"].([]any)
		for _, e := range md {
			m, _ := e.(map[string]any)
			k, _ := m["key"].(string)
			v, _ := m["value"].(string)
			raw, _ := base64.StdEncoding.DecodeString(v)
			u.Metadata[k] = string(raw)
		}
		f.mutate("metadata " + u.ID)
		return200(w)
	case strings.HasSuffix(path, "/password_reset"):
		f.mutate("password reset")
		if u := f.users[strings.Trim(strings.TrimSuffix(path, "/password_reset"), "/")]; u != nil {
			u.ResetCode = "ABC123"
		}
		reply(w, 200, map[string]any{"verificationCode": "ABC123"})
	case strings.HasSuffix(path, "/password") && r.Method == "POST":
		// What the login broker's devidp.ResetPassword redeems.
		u := f.users[strings.Trim(strings.TrimSuffix(path, "/password"), "/")]
		code, _ := body["verificationCode"].(string)
		if u == nil || u.ResetCode == "" || code != u.ResetCode {
			zerr(w, 400, 3, "CODE-woT0xc", "Code is invalid")
			return
		}
		np, _ := body["newPassword"].(map[string]any)
		u.PasswordSetBy, _ = np["password"].(string)
		u.ResetCode = ""
		u.AuthMethods = []string{"AUTHENTICATION_METHOD_TYPE_PASSWORD"}
		f.mutate("set password " + u.ID)
		return200(w)
	case r.Method == "GET" && strings.Count(strings.Trim(path, "/"), "/") == 0 && path != "":
		// What devidp.FindUserByID reads.
		u := f.users[strings.Trim(path, "/")]
		if u == nil || u.Machine {
			zerr(w, 404, 5, "QUERY-Dfbg2", "User could not be found")
			return
		}
		reply(w, 200, map[string]any{"user": map[string]any{"userId": u.ID, "username": u.UserName, "preferredLoginName": u.UserName,
			"state": "USER_STATE_ACTIVE", "human": map[string]any{"email": map[string]any{"email": u.Email, "isVerified": !u.Unverified}}}})
	case strings.HasSuffix(path, "/invite_code") && r.Method == "POST":
		id := strings.Trim(strings.TrimSuffix(path, "/invite_code"), "/")
		u := f.users[id]
		if u == nil {
			zerr(w, 400, 9, "COMMAND-Wgvn4", "Errors.User.NotFound")
			return
		}
		if len(u.AuthMethods) > 0 {
			zerr(w, 400, 9, "COMMAND-EF34g", "Errors.User.AlreadyInitialised")
			return
		}
		send, _ := body["sendCode"].(map[string]any)
		if send == nil {
			f.t.Errorf("fake: invite_code without sendCode: %v", body)
		}
		f.invites[id]++
		f.mutate("invite code " + u.UserName)
		reply(w, 200, map[string]any{"details": details()})
	default:
		f.t.Errorf("fake: unhandled v2 path %s %s", r.Method, r.URL.Path)
		zerr(w, 404, 5, "FAKE-1", "unhandled")
	}
}

// --- email providers, IdPs, login policy lists, secret generators ----------

func (f *fakeZitadel) emailJSON(s *fSMTP) map[string]any {
	state := "EMAIL_PROVIDER_INACTIVE"
	if s.State == "SMTP_CONFIG_ACTIVE" {
		state = "EMAIL_PROVIDER_ACTIVE"
	}
	m := map[string]any{"id": s.ID, "state": state, "description": s.Desc}
	if s.HTTP {
		h := map[string]any{"endpoint": s.Endpoint}
		if !f.hideSigningKey {
			h["signingKey"] = s.SigningKey
		}
		m["http"] = h
	} else {
		m["smtp"] = map[string]any{"senderAddress": s.Sender, "senderName": s.SenderName, "tls": s.TLS,
			"host": s.Host, "user": s.User, "replyToAddress": s.ReplyTo}
	}
	return m
}

func (f *fakeZitadel) emailAPI(w http.ResponseWriter, r *http.Request) {
	body := decode(r)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/v1/email"), "/")
	parts := strings.Split(rest, "/")
	find := func(id string) *fSMTP {
		for _, x := range f.smtp {
			if x.ID == id {
				return x
			}
		}
		return nil
	}
	switch {
	case rest == "_search":
		var all []any
		for _, s := range f.smtp {
			all = append(all, f.emailJSON(s))
		}
		lo, hi := f.pageOf(body, len(all))
		reply(w, 200, map[string]any{"result": all[lo:hi]})
	case rest == "http" && r.Method == "POST":
		s := &fSMTP{ID: f.id(), State: "SMTP_CONFIG_INACTIVE", HTTP: true, SigningKey: "sk-" + strconv.Itoa(f.seq)}
		s.Endpoint, _ = body["endpoint"].(string)
		s.Desc, _ = body["description"].(string)
		f.smtp = append(f.smtp, s)
		f.mutate("create email http")
		reply(w, 200, map[string]any{"id": s.ID, "signingKey": s.SigningKey, "details": details()})
	case len(parts) == 2 && parts[0] == "http" && r.Method == "PUT":
		s := find(parts[1])
		if s == nil || !s.HTTP {
			zerr(w, 404, 5, "COMMAND-xIrdledqv4", "Errors.SMTPConfig.NotFound")
			return
		}
		ep, _ := body["endpoint"].(string)
		d, _ := body["description"].(string)
		out := map[string]any{"details": details()}
		changed := ep != s.Endpoint || d != s.Desc
		s.Endpoint, s.Desc = ep, d
		if body["expirationSigningKey"] != nil {
			f.seq++
			s.SigningKey = "sk-rotated-" + strconv.Itoa(f.seq)
			out["signingKey"] = s.SigningKey
			changed = true
		}
		if changed {
			f.mutate("update email http")
		}
		reply(w, 200, out)
	case len(parts) == 2 && (parts[1] == "_activate" || parts[1] == "_deactivate"):
		s := find(parts[0])
		if s == nil {
			zerr(w, 404, 5, "COMMAND-E9K20hxOS9", "Errors.SMTPConfig.NotFound")
			return
		}
		if parts[1] == "_deactivate" {
			if s.State != "SMTP_CONFIG_ACTIVE" {
				zerr(w, 400, 9, "COMMAND-km8g3", "Errors.SMTPConfig.AlreadyDeactivated")
				return
			}
			s.State = "SMTP_CONFIG_INACTIVE"
			f.mutate("deactivate email")
			return200(w)
			return
		}
		if s.State == "SMTP_CONFIG_ACTIVE" {
			zerr(w, 400, 9, "COMMAND-vUHBSmBzaw", "Errors.SMTPConfig.AlreadyActive")
			return
		}
		for _, x := range f.smtp {
			x.State = "SMTP_CONFIG_INACTIVE"
		}
		s.State = "SMTP_CONFIG_ACTIVE"
		f.mutate("activate email")
		return200(w)
	default:
		f.t.Errorf("fake: unhandled email path %s %s", r.Method, r.URL.Path)
		zerr(w, 404, 5, "FAKE-1", "unhandled")
	}
}

var fakeProviderType = map[string]string{"google": "PROVIDER_TYPE_GOOGLE", "github": "PROVIDER_TYPE_GITHUB"}

func (f *fakeZitadel) idp(name string) *fIdP {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, i := range f.idps {
		if i.Name == name {
			return i
		}
	}
	return nil
}

func (f *fakeZitadel) idpAPI(w http.ResponseWriter, r *http.Request) {
	body := decode(r)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/v1/idps"), "/")
	parts := strings.Split(rest, "/")
	switch {
	case rest == "templates/_search":
		var all []any
		for _, i := range f.idps {
			opts := map[string]any{}
			for k, v := range i.Options {
				if b, ok := v.(bool); ok && !b {
					continue // the gateway omits zero values
				}
				if v == "AUTO_LINKING_OPTION_UNSPECIFIED" {
					continue
				}
				opts[k] = v
			}
			all = append(all, map[string]any{"id": i.ID, "name": i.Name, "state": "IDP_STATE_ACTIVE",
				"type": fakeProviderType[i.Type], "config": map[string]any{"options": opts,
					i.Type: map[string]any{"clientId": i.ClientID, "scopes": i.Scopes}}})
		}
		lo, hi := f.pageOf(body, len(all))
		reply(w, 200, map[string]any{"result": all[lo:hi]})
	case len(parts) == 1 && r.Method == "POST" && fakeProviderType[parts[0]] != "":
		i := &fIdP{ID: f.id(), Type: parts[0]}
		i.Name, _ = body["name"].(string)
		i.ClientID, _ = body["clientId"].(string)
		i.Secret, _ = body["clientSecret"].(string)
		i.Scopes = strs(body["scopes"])
		i.Options, _ = body["providerOptions"].(map[string]any)
		if i.Secret == "" {
			zerr(w, 400, 3, "IDP-1", "client secret required")
			return
		}
		f.idps = append(f.idps, i)
		f.mutate("create idp " + i.Name)
		reply(w, 200, map[string]any{"id": i.ID, "details": details()})
	case len(parts) == 2 && r.Method == "PUT":
		var i *fIdP
		for _, x := range f.idps {
			if x.ID == parts[1] && x.Type == parts[0] {
				i = x
			}
		}
		if i == nil {
			zerr(w, 404, 5, "IDP-2", "not found")
			return
		}
		before := fmt.Sprint(i.Name, i.ClientID, i.Scopes, normalize(i.Options))
		i.Name, _ = body["name"].(string)
		i.ClientID, _ = body["clientId"].(string)
		i.Scopes = strs(body["scopes"])
		i.Options, _ = body["providerOptions"].(map[string]any)
		changed := fmt.Sprint(i.Name, i.ClientID, i.Scopes, normalize(i.Options)) != before
		if sec, _ := body["clientSecret"].(string); sec != "" {
			i.Secret, changed = sec, true // a re-applied secret is still a write
		}
		if changed {
			f.mutate("update idp " + i.Name)
		}
		reply(w, 200, map[string]any{"details": details()}) // an unchanged update answers 200
	default:
		f.t.Errorf("fake: unhandled idp path %s %s", r.Method, r.URL.Path)
		zerr(w, 404, 5, "FAKE-1", "unhandled")
	}
}

func (f *fakeZitadel) loginListAPI(w http.ResponseWriter, r *http.Request) {
	body := decode(r)
	rest := strings.TrimPrefix(r.URL.Path, "/admin/v1/policies/login/")
	switch {
	case rest == "idps" && r.Method == "POST":
		id, _ := body["idpId"].(string)
		if contains(f.loginIDPs, id) {
			zerr(w, 409, 6, "INSTANCE-2B0ps", "Errors.Instance.LoginPolicy.IDP.AlreadyExists")
			return
		}
		f.loginIDPs = append(f.loginIDPs, id)
		f.mutate("link idp " + id)
		return200(w)
	case strings.HasPrefix(rest, "idps/") && r.Method == "DELETE":
		id := strings.TrimPrefix(rest, "idps/")
		for i, x := range f.loginIDPs {
			if x == id {
				f.loginIDPs = append(f.loginIDPs[:i], f.loginIDPs[i+1:]...)
				f.mutate("unlink idp " + id)
				return200(w)
				return
			}
		}
		zerr(w, 404, 5, "INSTANCE-39fjs", "Errors.Instance.LoginPolicy.IDP.NotExisting")
	case rest == "second_factors" && r.Method == "POST":
		t, _ := body["type"].(string)
		if contains(f.secondFactors, t) {
			zerr(w, 409, 6, "INSTANCE-2B0ps", "Errors.Instance.MFA.AlreadyExists")
			return
		}
		f.secondFactors = append(f.secondFactors, t)
		f.mutate("add second factor " + t)
		return200(w)
	case strings.HasPrefix(rest, "second_factors/") && r.Method == "DELETE":
		t := strings.TrimPrefix(rest, "second_factors/")
		for i, x := range f.secondFactors {
			if x == t {
				f.secondFactors = append(f.secondFactors[:i], f.secondFactors[i+1:]...)
				f.mutate("remove second factor " + t)
				return200(w)
				return
			}
		}
		zerr(w, 404, 5, "INSTANCE-3M9od", "Errors.Instance.LoginPolicy.MFA.NotExisting")
	default:
		f.t.Errorf("fake: unhandled login policy path %s %s", r.Method, r.URL.Path)
		zerr(w, 404, 5, "FAKE-1", "unhandled")
	}
}

func (f *fakeZitadel) generatorAPI(w http.ResponseWriter, r *http.Request) {
	typ := strings.TrimPrefix(r.URL.Path, "/admin/v1/secretgenerators/")
	if r.Method == "GET" {
		g := map[string]any{"generatorType": typ}
		for k, v := range f.generators[typ] {
			if b, ok := v.(bool); ok && !b {
				continue
			}
			g[k] = v
		}
		reply(w, 200, map[string]any{"secretGenerator": g})
		return
	}
	body := decode(r)
	if reflect.DeepEqual(omitZero(body), omitZero(f.generators[typ])) {
		notChanged(w, "COMMAND-m0o3f")
		return
	}
	f.generators[typ] = body
	f.mutate("update secret generator " + typ)
	return200(w)
}
