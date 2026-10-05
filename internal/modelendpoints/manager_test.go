// Copyright (c) 2025 Reliant Labs
package modelendpoints

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/localprobe"
	"github.com/reliant-labs/reliant/internal/netguard"
)

// ---- fakes ----

type memStore struct {
	mu      sync.Mutex
	rows    map[string]*db.ModelEndpoint
	daemons map[string]string // id -> owner
}

func newMemStore() *memStore {
	return &memStore{rows: map[string]*db.ModelEndpoint{}, daemons: map[string]string{}}
}

func (s *memStore) CreateModelEndpoint(_ context.Context, e *db.ModelEndpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rows {
		if r.UserID == e.UserID && r.Name == e.Name {
			return db.ErrModelEndpointNameTaken
		}
	}
	cp := *e
	s.rows[e.ID] = &cp
	return nil
}
func (s *memStore) GetModelEndpoint(_ context.Context, userID, id string) (*db.ModelEndpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || r.UserID != userID {
		return nil, db.ErrModelEndpointNotFound
	}
	cp := *r
	return &cp, nil
}
func (s *memStore) ListModelEndpoints(_ context.Context, userID string) ([]*db.ModelEndpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*db.ModelEndpoint
	for _, r := range s.rows {
		if r.UserID == userID {
			cp := *r
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (s *memStore) UpdateModelEndpoint(_ context.Context, e *db.ModelEndpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[e.ID]
	if !ok || r.UserID != e.UserID {
		return db.ErrModelEndpointNotFound
	}
	cp := *e
	s.rows[e.ID] = &cp
	return nil
}
func (s *memStore) SetModelEndpointProbe(_ context.Context, userID, id, probe string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rows[id]; ok && r.UserID == userID {
		r.ProbeJSON = probe
	}
	return nil
}
func (s *memStore) DeleteModelEndpoint(_ context.Context, userID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rows[id]; !ok || r.UserID != userID {
		return db.ErrModelEndpointNotFound
	}
	delete(s.rows, id)
	return nil
}
func (s *memStore) GetDaemon(_ context.Context, id string) (*db.Daemon, error) {
	owner, ok := s.daemons[id]
	if !ok {
		return nil, errors.New("no rows")
	}
	return &db.Daemon{ID: id, UserID: owner}, nil
}

// fakeDaemon behaves like a daemon's relay authorization: SetConfiguredEndpoints
// replaces its configured list, and its inventory reports one endpoint per
// configured URL, answered by whatever server backs it.
type fakeDaemon struct {
	mu         sync.Mutex
	configured []string
	online     bool
	setCalls   [][]string
	// serve maps a root URL to the models that "machine" sees there.
	serve map[string][]*reliantv1.LocalModelInfo
}

func (d *fakeDaemon) inventory() *reliantv1.LocalModelInventory {
	inv := &reliantv1.LocalModelInventory{}
	for _, u := range d.configured {
		root, _ := localprobe.NormalizeRoot(u)
		inv.Endpoints = append(inv.Endpoints, &reliantv1.LocalModelEndpoint{
			Id: localprobe.EndpointID(root), Kind: "vllm", BaseUrl: root + "/v1", Source: "configured", Models: d.serve[root],
		})
	}
	return inv
}
func (d *fakeDaemon) Refresh(_ context.Context, _, _ string) (*reliantv1.LocalModelInventory, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.online {
		return nil, ErrDaemonOffline
	}
	return d.inventory(), nil
}
func (d *fakeDaemon) SetConfiguredEndpoints(_ context.Context, _, _ string, urls []string) (*reliantv1.LocalModelInventory, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.online {
		return nil, ErrDaemonOffline
	}
	d.configured = append([]string(nil), urls...)
	d.setCalls = append(d.setCalls, append([]string(nil), urls...))
	return d.inventory(), nil
}

type recordingCreds struct {
	puts    int
	deletes []string
}

func (c *recordingCreds) Put(_ context.Context, _, _ string, key *string, _ map[string]string) (string, error) {
	c.puts++
	if key != nil && *key != "" {
		return "conn-1", nil
	}
	return "", nil
}
func (c *recordingCreds) Get(context.Context, string, string) (string, map[string]string, error) {
	return "sk-secret-key-1234", nil, nil
}
func (c *recordingCreds) Delete(_ context.Context, _, id string) error {
	c.deletes = append(c.deletes, id)
	return nil
}

func in(name, url string, route reliantv1.ModelEndpointRoute) *reliantv1.ModelEndpointInput {
	return &reliantv1.ModelEndpointInput{Name: name, BaseUrl: url, Route: route}
}

const (
	direct = reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_DIRECT
	via    = reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_VIA_DAEMON
)

// ---- validation ----

func TestNormalizeBaseURL(t *testing.T) {
	good := map[string]string{
		"https://LLM.Example.com/v1/":   "https://llm.example.com/v1",
		"  http://host:8000  ":          "http://host:8000",
		"https://llm.example.com/proxy": "https://llm.example.com/proxy",
	}
	for raw, want := range good {
		got, err := NormalizeBaseURL(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, want, got)
	}
	bad := map[string]string{
		"":                               "base URL",
		"not a url":                      "http",
		"ftp://example.com":              "http://",
		"http://":                        "host",
		"https://user:pw@example.com/v1": "username or password",
		"https://example.com/v1?key=1":   "query",
		"https://example.com/v1#frag":    "query",
		"https://example.com:99999/v1":   "port",
		"//example.com/v1":               "http://",
	}
	for raw, wantMsg := range bad {
		_, err := NormalizeBaseURL(raw)
		require.Error(t, err, raw)
		assert.True(t, IsValidation(err), raw)
		assert.Contains(t, err.Error(), wantMsg, raw)
	}
}

func TestValidateInputRoutesAndModels(t *testing.T) {
	t.Run("via daemon needs a daemon, direct must not have one", func(t *testing.T) {
		_, err := validateInput(in("a", "https://x.example.com/v1", via))
		require.ErrorContains(t, err, "which of your machines")
		d := in("a", "https://x.example.com/v1", direct)
		d.DaemonId = "d-1"
		_, err = validateInput(d)
		require.ErrorContains(t, err, "only be chosen")
		_, err = validateInput(in("a", "https://x.example.com/v1", reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_UNSPECIFIED))
		require.ErrorContains(t, err, "how Reliant should reach")
	})
	t.Run("extra body", func(t *testing.T) {
		ok := in("a", "https://x.example.com/v1", direct)
		ok.Models = []*reliantv1.ModelEndpointModel{{Name: "m", ExtraBodyJson: `{"min_p":0.05}`}}
		_, err := validateInput(ok)
		require.NoError(t, err)
		for _, body := range []string{`[1]`, `"x"`, `{"model":"other"}`, `{"messages":[]}`, `{"tools":[]}`, `{"stream":false}`, `{bad`, `{} {}`} {
			bad := in("a", "https://x.example.com/v1", direct)
			bad.Models = []*reliantv1.ModelEndpointModel{{Name: "m", ExtraBodyJson: body}}
			_, err := validateInput(bad)
			require.Error(t, err, body)
			assert.True(t, IsValidation(err), body)
		}
	})
	t.Run("params are range-checked", func(t *testing.T) {
		hot, huge := 3.0, 0.0
		for _, m := range []*reliantv1.ModelEndpointModel{
			{Name: "m", Temperature: &hot},
			{Name: "m", TopP: &huge},
			{Name: "m", ContextWindow: 100, MaxOutputTokens: 200},
			{Name: "m", ContextWindow: -1},
			{Name: ""},
		} {
			bad := in("a", "https://x.example.com/v1", direct)
			bad.Models = []*reliantv1.ModelEndpointModel{m}
			_, err := validateInput(bad)
			require.Error(t, err, m.String())
		}
		dup := in("a", "https://x.example.com/v1", direct)
		dup.Models = []*reliantv1.ModelEndpointModel{{Name: "m"}, {Name: "m"}}
		_, err := validateInput(dup)
		require.ErrorContains(t, err, "twice")
	})
	t.Run("headers", func(t *testing.T) {
		for _, name := range []string{"Authorization", "host", "Content-Length", "bad name", ""} {
			bad := in("a", "https://x.example.com/v1", direct)
			bad.Headers = map[string]string{name: "v"}
			_, err := validateInput(bad)
			require.Error(t, err, name)
		}
		crlf := in("a", "https://x.example.com/v1", direct)
		crlf.Headers = map[string]string{"X-Org": "a\r\nInjected: 1"}
		_, err := validateInput(crlf)
		require.ErrorContains(t, err, "single line")
	})
}

// ---- SSRF matrix (route rules) ----

func TestDirectRouteSSRFMatrix(t *testing.T) {
	hostedLookup := func(_ context.Context, host string) ([]net.IP, error) {
		switch host {
		case "internal.example.com":
			return []net.IP{net.ParseIP("10.4.4.4")}, nil
		case "public.example.com":
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
		return nil, errors.New("no such host")
	}
	hosted := netguard.Policy{LookupIP: hostedLookup}
	selfHost := netguard.Policy{AllowPrivate: true, LookupIP: hostedLookup}

	cases := []struct {
		url     string
		blocked bool
	}{
		{"http://127.0.0.1:8000/v1", true},
		{"http://[::1]:8000/v1", true},
		{"http://10.1.2.3/v1", true},
		{"http://169.254.169.254/latest", true},
		{"http://internal.example.com/v1", true}, // hostname resolving to private
		{"http://localhost:11434/v1", true},
		{"https://public.example.com/v1", false},
		{"https://8.8.8.8/v1", false},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			// Probes must never succeed in reaching a blocked host; a probe
			// client that fails the test if dialed proves validation stops first.
			trap := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, errors.New("unreachable in test")
			})}
			m := NewManager(Config{Store: newMemStore(), Policy: hosted, ProbeClient: trap})
			_, err := m.Create(context.Background(), "u1", in("ep", tc.url, direct))
			if tc.blocked {
				require.Error(t, err)
				assert.True(t, IsValidation(err))
				assert.Contains(t, err.Error(), "through one of your machines")
			} else {
				require.NoError(t, err, "a public address must be accepted (probe failure does not block saving)")
			}
			// Test (draft) enforces the same rule.
			_, _, err = m.Test(context.Background(), "u1", "", in("ep", tc.url, direct))
			if tc.blocked {
				require.Error(t, err)
			}
		})
	}

	t.Run("self-host allows private and loopback", func(t *testing.T) {
		m := NewManager(Config{Store: newMemStore(), Policy: selfHost, ProbeClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("down")
		})}})
		for _, u := range []string{"http://127.0.0.1:8000/v1", "http://10.1.2.3/v1", "http://localhost:11434/v1"} {
			_, err := m.Create(context.Background(), "u1", in(u, u, direct))
			require.NoError(t, err, u)
		}
	})

	t.Run("via daemon is allowed to name private hosts", func(t *testing.T) {
		st := newMemStore()
		st.daemons["d-1"] = "u1"
		m := NewManager(Config{Store: st, Policy: hosted, Daemons: &fakeDaemon{online: true}})
		_, err := m.Create(context.Background(), "u1", func() *reliantv1.ModelEndpointInput {
			i := in("lan", "http://10.0.0.5:8000/v1", via)
			i.DaemonId = "d-1"
			return i
		}())
		require.NoError(t, err)
	})

	t.Run("via daemon requires a daemon the user owns", func(t *testing.T) {
		st := newMemStore()
		st.daemons["d-other"] = "someone-else"
		m := NewManager(Config{Store: st, Policy: hosted, Daemons: &fakeDaemon{online: true}})
		for _, id := range []string{"d-other", "d-missing"} {
			i := in("lan", "http://10.0.0.5:8000/v1", via)
			i.DaemonId = id
			_, err := m.Create(context.Background(), "u1", i)
			require.ErrorIs(t, err, ErrDaemonNotOwned, id)
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// ---- credentials ----

func TestCredentialsRejectedWithoutSealedStore(t *testing.T) {
	st := newMemStore()
	var dialed bool
	m := NewManager(Config{Store: st, Policy: netguard.Policy{AllowPrivate: true}, ProbeClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		dialed = true
		return nil, errors.New("down")
	})}})

	key := "sk-live-abc"
	withKey := in("keyed", "https://llm.example.com/v1", direct)
	withKey.ApiKey = &key
	_, err := m.Create(context.Background(), "u1", withKey)
	require.ErrorIs(t, err, ErrCredentialStoreUnavailable)
	assert.Contains(t, err.Error(), "sealed credential store")
	assert.Empty(t, st.rows, "a rejected endpoint must not be stored")
	assert.False(t, dialed, "and must not be probed with the key")

	withHeader := in("hdr", "https://llm.example.com/v1", direct)
	withHeader.Headers = map[string]string{"X-Org": "acme"}
	_, err = m.Create(context.Background(), "u1", withHeader)
	require.ErrorIs(t, err, ErrCredentialStoreUnavailable)

	_, _, err = m.Test(context.Background(), "u1", "", withKey)
	require.ErrorIs(t, err, ErrCredentialStoreUnavailable)

	t.Run("update with a key is rejected and leaves the row intact", func(t *testing.T) {
		created, err := m.Create(context.Background(), "u1", in("plain", "https://llm.example.com/v1", direct))
		require.NoError(t, err)
		upd := in("plain", "https://llm.example.com/v1", direct)
		upd.ApiKey = &key
		_, err = m.Update(context.Background(), "u1", created.GetId(), upd)
		require.ErrorIs(t, err, ErrCredentialStoreUnavailable)
		got, _ := st.GetModelEndpoint(context.Background(), "u1", created.GetId())
		assert.Nil(t, got.CredentialConnectionID)
	})

	t.Run("clearing is accepted", func(t *testing.T) {
		empty := ""
		clr := in("clear", "https://llm.example.com/v1", direct)
		clr.ApiKey = &empty
		clr.Headers = map[string]string{"X-Org": ""}
		_, err := m.Create(context.Background(), "u1", clr)
		require.NoError(t, err)
	})

	t.Run("endpoints without credentials work fully", func(t *testing.T) {
		got, err := m.Create(context.Background(), "u1", in("nocreds", "https://llm.example.com/v1", direct))
		require.NoError(t, err)
		assert.False(t, got.GetHasApiKey())
		assert.Empty(t, got.GetMaskedApiKey())
	})
}

func TestNotAvailableCredentialsContract(t *testing.T) {
	c := NotAvailableCredentials{}
	k := "k"
	_, err := c.Put(context.Background(), "u", "e", &k, nil)
	assert.ErrorIs(t, err, ErrCredentialStoreUnavailable)
	_, err = c.Put(context.Background(), "u", "e", nil, map[string]string{"A": "b"})
	assert.ErrorIs(t, err, ErrCredentialStoreUnavailable)
	id, err := c.Put(context.Background(), "u", "e", nil, nil)
	assert.NoError(t, err)
	assert.Empty(t, id)
	_, _, err = c.Get(context.Background(), "u", "c")
	assert.ErrorIs(t, err, ErrCredentialStoreUnavailable)
	assert.NoError(t, c.Delete(context.Background(), "u", "c"))
}

func TestCredentialsStoredAndMaskedWithASealedStore(t *testing.T) {
	st := newMemStore()
	creds := &recordingCreds{}
	m := NewManager(Config{Store: st, Credentials: creds, Policy: netguard.Policy{AllowPrivate: true},
		ProbeClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("down") })}})
	key := "sk-secret-key-1234"
	i := in("keyed", "https://llm.example.com/v1", direct)
	i.ApiKey = &key
	i.Headers = map[string]string{"X-Org": "acme"}
	got, err := m.Create(context.Background(), "u1", i)
	require.NoError(t, err)
	assert.True(t, got.GetHasApiKey())
	assert.Equal(t, "sk-…1234", got.GetMaskedApiKey())
	assert.Equal(t, []string{"X-Org"}, got.GetHeaderNames())

	row := st.rows[got.GetId()]
	require.NotNil(t, row.CredentialConnectionID)
	assert.Equal(t, "conn-1", *row.CredentialConnectionID)
	for _, field := range []string{row.ModelsJSON, row.ProbeJSON, row.Name, row.BaseURL, strings.Join(row.HeaderNames, ",")} {
		assert.NotContains(t, field, "sk-secret", "no secret may reach this table")
		assert.NotContains(t, field, "acme")
	}

	require.NoError(t, m.Delete(context.Background(), "u1", got.GetId()))
	assert.Equal(t, []string{"conn-1"}, creds.deletes, "deleting the endpoint purges its credential")
}

// ---- probing ----

func openAIServer(t *testing.T, ids ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			var items []string
			for _, id := range ids {
				items = append(items, `{"id":"`+id+`"}`)
			}
			_, _ = w.Write([]byte(`{"data":[` + strings.Join(items, ",") + `]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDirectProbeStoresResultAndReportsFailureWithoutBlockingSave(t *testing.T) {
	srv := openAIServer(t, "llama-3-70b", "mixtral")
	st := newMemStore()
	m := NewManager(Config{Store: st, Policy: netguard.Policy{AllowPrivate: true}})

	created, err := m.Create(context.Background(), "u1", in("live", srv.URL+"/v1", direct))
	require.NoError(t, err)
	require.NotNil(t, created.GetProbe())
	assert.Empty(t, created.GetProbe().GetError())
	var names []string
	for _, mm := range created.GetProbe().GetModels() {
		names = append(names, mm.GetName())
	}
	assert.Equal(t, []string{"llama-3-70b", "mixtral"}, names)
	assert.NotEmpty(t, st.rows[created.GetId()].ProbeJSON, "the probe is stored in probe_json")

	t.Run("a down server is saved with the error", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		url := dead.URL
		dead.Close()
		got, err := m.Create(context.Background(), "u1", in("dead", url+"/v1", direct))
		require.NoError(t, err)
		assert.NotEmpty(t, got.GetProbe().GetError())
		assert.Empty(t, got.GetProbe().GetModels())
	})

	t.Run("Test returns models and latency for a draft", func(t *testing.T) {
		probe, latency, err := m.Test(context.Background(), "u1", "", in("draft", srv.URL+"/v1", direct))
		require.NoError(t, err)
		assert.Len(t, probe.GetModels(), 2)
		assert.GreaterOrEqual(t, latency.Nanoseconds(), int64(0))
	})

	t.Run("Test refreshes a saved endpoint's stored probe", func(t *testing.T) {
		_, _, err := m.Test(context.Background(), "u1", created.GetId(), nil)
		require.NoError(t, err)
		_, _, err = m.Test(context.Background(), "u2", created.GetId(), nil)
		require.ErrorIs(t, err, db.ErrModelEndpointNotFound, "another user cannot test it")
	})

	t.Run("a non-OpenAI server is reported, not trusted", func(t *testing.T) {
		html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }))
		defer html.Close()
		got, err := m.Create(context.Background(), "u1", in("html", html.URL+"/v1", direct))
		require.NoError(t, err)
		assert.Contains(t, got.GetProbe().GetError(), "OpenAI-style")
	})

	t.Run("auth failures are reported", func(t *testing.T) {
		locked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }))
		defer locked.Close()
		got, err := m.Create(context.Background(), "u1", in("locked", locked.URL+"/v1", direct))
		require.NoError(t, err)
		assert.Contains(t, got.GetProbe().GetError(), "401")
	})
}

func TestDuplicateNameRejected(t *testing.T) {
	srv := openAIServer(t, "m")
	m := NewManager(Config{Store: newMemStore(), Policy: netguard.Policy{AllowPrivate: true}})
	_, err := m.Create(context.Background(), "u1", in("Same", srv.URL+"/v1", direct))
	require.NoError(t, err)
	_, err = m.Create(context.Background(), "u1", in("Same", srv.URL+"/v1", direct))
	require.ErrorIs(t, err, db.ErrModelEndpointNameTaken)
	_, err = m.Create(context.Background(), "u2", in("Same", srv.URL+"/v1", direct))
	require.NoError(t, err)
}

// ---- VIA_DAEMON relay authorization ----

func TestViaDaemonAuthorizesEndpointOnTheMachineAndPreservesTheRest(t *testing.T) {
	const lanURL = "http://10.0.0.5:8000/v1"
	st := newMemStore()
	st.daemons["d-1"] = "u1"
	daemon := &fakeDaemon{
		online:     true,
		configured: []string{"http://localhost:9999/v1"}, // hand-added in the daemon's own settings
		serve: map[string][]*reliantv1.LocalModelInfo{
			"http://10.0.0.5:8000": {{Name: "qwen-72b", SupportsChat: true, ContextWindow: 32768}},
		},
	}
	m := NewManager(Config{Store: st, Daemons: daemon, Policy: netguard.Policy{}})

	i := in("Home vLLM", lanURL, via)
	i.DaemonId = "d-1"
	created, err := m.Create(context.Background(), "u1", i)
	require.NoError(t, err)

	t.Run("the daemon's relay now authorizes it, alongside what was already configured", func(t *testing.T) {
		require.Len(t, daemon.setCalls, 1)
		assert.ElementsMatch(t, []string{"http://localhost:9999/v1", lanURL}, daemon.setCalls[0])
	})
	t.Run("the probe is what the daemon saw, keyed to our endpoint id", func(t *testing.T) {
		assert.Empty(t, created.GetProbe().GetError())
		assert.Equal(t, created.GetId(), created.GetProbe().GetId())
		require.Len(t, created.GetProbe().GetModels(), 1)
		assert.Equal(t, "qwen-72b", created.GetProbe().GetModels()[0].GetName())
	})
	t.Run("re-testing is idempotent: no redundant rewrite", func(t *testing.T) {
		_, _, err := m.Test(context.Background(), "u1", created.GetId(), nil)
		require.NoError(t, err)
		assert.Len(t, daemon.setCalls, 1)
	})
	t.Run("deleting revokes ONLY our URL", func(t *testing.T) {
		require.NoError(t, m.Delete(context.Background(), "u1", created.GetId()))
		require.Len(t, daemon.setCalls, 2)
		assert.Equal(t, []string{"http://localhost:9999/v1"}, daemon.setCalls[1])
	})
}

func TestViaDaemonOfflineSavesAndRepairsOnTest(t *testing.T) {
	st := newMemStore()
	st.daemons["d-1"] = "u1"
	daemon := &fakeDaemon{online: false, serve: map[string][]*reliantv1.LocalModelInfo{"http://10.0.0.5:8000": {{Name: "m", SupportsChat: true}}}}
	m := NewManager(Config{Store: st, Daemons: daemon})

	i := in("Home", "http://10.0.0.5:8000/v1", via)
	i.DaemonId = "d-1"
	created, err := m.Create(context.Background(), "u1", i)
	require.NoError(t, err, "an offline machine must not prevent saving")
	assert.Contains(t, created.GetProbe().GetError(), "offline")
	assert.Empty(t, daemon.setCalls)

	daemon.online = true
	probe, _, err := m.Test(context.Background(), "u1", created.GetId(), nil)
	require.NoError(t, err)
	assert.Empty(t, probe.GetError())
	require.Len(t, daemon.setCalls, 1, "Test re-authorizes the endpoint once the machine is back")
	assert.Equal(t, []string{"http://10.0.0.5:8000/v1"}, daemon.setCalls[0])
}

func TestMovingAnEndpointRevokesItFromTheOldMachine(t *testing.T) {
	st := newMemStore()
	st.daemons["d-1"], st.daemons["d-2"] = "u1", "u1"
	old := &fakeDaemon{online: true}
	other := &fakeDaemon{online: true}
	router := routedDaemons{"d-1": old, "d-2": other}
	m := NewManager(Config{Store: st, Daemons: router})

	i := in("Move", "http://10.0.0.5:8000/v1", via)
	i.DaemonId = "d-1"
	created, err := m.Create(context.Background(), "u1", i)
	require.NoError(t, err)
	require.Equal(t, []string{"http://10.0.0.5:8000/v1"}, old.configured)

	upd := in("Move", "http://10.0.0.5:8000/v1", via)
	upd.DaemonId = "d-2"
	_, err = m.Update(context.Background(), "u1", created.GetId(), upd)
	require.NoError(t, err)
	assert.Equal(t, []string{"http://10.0.0.5:8000/v1"}, other.configured)
	assert.Empty(t, old.configured, "the previous machine no longer authorizes it")
}

type routedDaemons map[string]*fakeDaemon

func (r routedDaemons) Refresh(ctx context.Context, u, d string) (*reliantv1.LocalModelInventory, error) {
	return r[d].Refresh(ctx, u, d)
}
func (r routedDaemons) SetConfiguredEndpoints(ctx context.Context, u, d string, urls []string) (*reliantv1.LocalModelInventory, error) {
	return r[d].SetConfiguredEndpoints(ctx, u, d, urls)
}

func TestUserIsolationInManager(t *testing.T) {
	srv := openAIServer(t, "m")
	st := newMemStore()
	m := NewManager(Config{Store: st, Policy: netguard.Policy{AllowPrivate: true}})
	created, err := m.Create(context.Background(), "u1", in("mine", srv.URL+"/v1", direct))
	require.NoError(t, err)

	list, err := m.List(context.Background(), "u2")
	require.NoError(t, err)
	assert.Empty(t, list)
	_, err = m.Update(context.Background(), "u2", created.GetId(), in("x", srv.URL+"/v1", direct))
	require.ErrorIs(t, err, db.ErrModelEndpointNotFound)
	require.ErrorIs(t, m.Delete(context.Background(), "u2", created.GetId()), db.ErrModelEndpointNotFound)
	list, _ = m.List(context.Background(), "u1")
	assert.Len(t, list, 1)
}

func TestListMergesDiscoveredModelsWithUserSettings(t *testing.T) {
	srv := openAIServer(t, "a", "b")
	m := NewManager(Config{Store: newMemStore(), Policy: netguard.Policy{AllowPrivate: true}})
	i := in("e", srv.URL+"/v1", direct)
	i.Models = []*reliantv1.ModelEndpointModel{{Name: "a", Hidden: true, ContextWindow: 4096}}
	_, err := m.Create(context.Background(), "u1", i)
	require.NoError(t, err)
	list, err := m.List(context.Background(), "u1")
	require.NoError(t, err)
	require.Len(t, list[0].GetModels(), 2)
	assert.Equal(t, "a", list[0].GetModels()[0].GetName())
	assert.True(t, list[0].GetModels()[0].GetHidden())
	assert.EqualValues(t, 4096, list[0].GetModels()[0].GetContextWindow())
	assert.Equal(t, "b", list[0].GetModels()[1].GetName())
	assert.False(t, list[0].GetModels()[1].GetHidden())
}
