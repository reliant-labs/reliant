// Copyright (c) 2025 Reliant Labs

// Package inputoptionstest is an httptest fake of the parts of GitHub's REST
// API the shipped `github` types list through, plus the pieces that put the
// real option service in front of it: the shipped catalog's types with the
// github manifest pointed at the fake, the real declarative runner trusting
// its certificate, and a per-user credential source. A test built on it
// exercises the real manifest, real CEL projections and real action runs;
// only GitHub is a stand-in.
//
// forge:exclude-contract: test support
package inputoptionstest

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/inputoptions"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/netguard"
)

// Repo is a repository an installation reaches.
type Repo struct {
	FullName string
	Private  bool
	Push     bool
	Archived bool
}

// Installation is an account the App is installed on, with the repositories
// the user it is listed for can see through it.
type Installation struct {
	ID      int
	Account string
	Repos   []Repo
}

// Account is what the fake knows about one user's GitHub access.
type Account struct {
	Installations []Installation
	// Labels by "owner/name".
	Labels map[string][]string
	// Down makes every request answer 502.
	Down bool
}

// Fake is the GitHub stand-in. Users are told apart by the bearer token,
// "tok-<userID>", so a test sees whose credential made a request.
type Fake struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	accounts map[string]*Account // user -> access
	people   map[int]string      // id -> login
	reqs     []string
	tokens   []string
}

// NewFake starts the fake. accounts maps a user id to what that user can see.
func NewFake(t *testing.T, accounts map[string]*Account) *Fake {
	t.Helper()
	f := &Fake{t: t, accounts: accounts, people: map[int]string{583231: "octocat", 42: "hubot"}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// Requests are the "METHOD path?query" of every request seen, in order.
func (f *Fake) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

// Tokens are the bearer tokens of the requests seen, in order.
func (f *Fake) Tokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.tokens...)
}

// Reset forgets the requests seen.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs, f.tokens = nil, nil
}

// SetDown makes user's requests answer 502 (or stops doing so).
func (f *Fake) SetDown(user string, down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accounts[user].Down = down
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	f.reqs = append(f.reqs, r.Method+" "+r.URL.RequestURI())
	f.tokens = append(f.tokens, bearer)
	acct := f.accounts[strings.TrimPrefix(bearer, "tok-")]
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	reply := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case acct == nil:
		reply(401, map[string]any{"message": "Bad credentials"})
	case acct.Down:
		reply(502, map[string]any{"message": "bad gateway"})
	case r.URL.Path == "/user/installations":
		var out []any
		for _, in := range acct.Installations {
			out = append(out, map[string]any{"id": in.ID, "account": map[string]any{"login": in.Account, "type": "Organization"},
				"target_type": "Organization", "repository_selection": "selected"})
		}
		reply(200, map[string]any{"total_count": len(out), "installations": out})
	case strings.HasPrefix(r.URL.Path, "/user/installations/") && strings.HasSuffix(r.URL.Path, "/repositories"):
		id, _ := strconv.Atoi(strings.Split(strings.TrimPrefix(r.URL.Path, "/user/installations/"), "/")[0])
		for _, in := range acct.Installations {
			if in.ID != id {
				continue
			}
			var repos []any
			for i, rp := range in.Repos {
				owner, name, _ := strings.Cut(rp.FullName, "/")
				repos = append(repos, map[string]any{"id": id*100 + i, "name": name, "full_name": rp.FullName, "private": rp.Private,
					"archived": rp.Archived, "owner": map[string]any{"login": owner}, "html_url": "https://github.com/" + rp.FullName,
					"permissions": map[string]any{"push": rp.Push, "pull": true}})
			}
			reply(200, map[string]any{"total_count": len(repos), "repositories": repos})
			return
		}
		reply(404, map[string]any{"message": "Not Found"})
	case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/labels"):
		repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/labels")
		names, ok := acct.Labels[repo]
		if !ok {
			reply(404, map[string]any{"message": "Not Found"})
			return
		}
		out := []any{}
		for _, n := range names {
			out = append(out, map[string]any{"name": n, "color": "ededed"})
		}
		reply(200, out)
	case r.URL.Path == "/search/users":
		q := strings.ToLower(r.URL.Query().Get("q"))
		items := []any{}
		for id, login := range f.people {
			if strings.Contains(login, q) {
				items = append(items, map[string]any{"id": id, "login": login, "type": "User"})
			}
		}
		reply(200, map[string]any{"total_count": len(items), "items": items})
	case strings.HasPrefix(r.URL.Path, "/user/"):
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/user/"))
		if login, ok := f.people[id]; ok {
			reply(200, map[string]any{"id": id, "login": login, "type": "User"})
			return
		}
		reply(404, map[string]any{"message": "Not Found"})
	default:
		reply(404, map[string]any{"message": "Not Found"})
	}
}

// Types is the shipped catalog's types with the github manifest pointed at the
// fake.
func (f *Fake) Types() inputoptions.TypeSource {
	idx, err := catalogindex.Builtin()
	if err != nil {
		f.t.Fatalf("catalog: %v", err)
	}
	return pointedAt{idx: idx, url: f.srv.URL}
}

type pointedAt struct {
	idx *catalogindex.Index
	url string
}

func (p pointedAt) Lookup(ref string) (*reliantv1.IntegrationManifest, *reliantv1.TypeSpec, bool) {
	m, t, ok := p.idx.Lookup(ref)
	if !ok {
		return nil, nil, false
	}
	m = proto.Clone(m).(*reliantv1.IntegrationManifest)
	if m.GetId() == "github" {
		m.Connection.BaseUrl = p.url
	}
	t, _ = manifest.Type(m, t.GetId())
	return m, t, true
}

// Runner is the real declarative runner, trusting the fake's certificate and
// allowed to dial loopback.
func (f *Fake) Runner() *httpaction.Runner {
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	g := netguard.New()
	g.AllowLoopback = true
	return httpaction.NewRunner(g).WithRootCAs(pool)
}

// Credentials gives each user their own bearer token. A user in without has no
// GitHub connection; a named connection must be "conn-<userID>".
func Credentials(without ...string) inputoptions.UserCredentials {
	m := map[string]bool{}
	for _, u := range without {
		m[u] = true
	}
	return users(m)
}

type users map[string]bool

func (u users) ForUser(userID string) httpaction.CredentialSource {
	return userSource{id: userID, none: u[userID]}
}

type userSource struct {
	id   string
	none bool
}

func (s userSource) Credential(_ context.Context, req httpaction.CredentialRequest) (httpaction.Credential, error) {
	if s.none || (req.ConnectionID != "" && req.ConnectionID != "conn-"+s.id) {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "no github connection"}
	}
	return bearer{user: s.id}, nil
}

type bearer struct{ user string }

func (b bearer) ConnectionID() string      { return "conn-" + b.user }
func (b bearer) Params() map[string]string { return nil }
func (b bearer) Scrub(s string) string     { return strings.ReplaceAll(s, "tok-"+b.user, "[redacted]") }
func (b bearer) Apply(r *http.Request) error {
	r.Header.Set("Authorization", "Bearer tok-"+b.user)
	return nil
}

// Service is the real option service over the fake.
func (f *Fake) Service(without ...string) inputoptions.Service {
	svc, err := inputoptions.NewInstrumented(inputoptions.Deps{Types: f.Types(), Runner: f.Runner(), Credentials: Credentials(without...)})
	if err != nil {
		f.t.Fatal(err)
	}
	return svc
}
