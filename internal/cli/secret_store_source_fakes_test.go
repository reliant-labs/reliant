package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

// canary is a value no output may ever contain, raw, encoded or digested.
const canary = "sk_live_CANARY_0123456789abcdefABCDEF"

// fakeKube is an in-memory kubeAccess.
type fakeKube struct {
	mu       sync.Mutex
	secrets  map[string]kubeSecret // "ns/name"
	patched  []string              // "ns/name -annotation"
	tokens   []string              // "ns/sa aud=..."
	tokenErr error

	configMaps map[string]string // "ns/name/key"
	forwardTo  string            // host:port a ForwardService tunnel ends at
	forwards   []string          // "ns/service:port"
	forwardErr error
	closed     int
}

func newFakeKube() *fakeKube { return &fakeKube{secrets: map[string]kubeSecret{}} }

func (f *fakeKube) put(ns, name string, s kubeSecret) { f.secrets[ns+"/"+name] = s }

func (f *fakeKube) GetSecret(_ context.Context, ns, name string) (kubeSecret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.secrets[ns+"/"+name]
	if !ok {
		return kubeSecret{}, fmt.Errorf("%s/%s: %w", ns, name, errKubeSecretNotFound)
	}
	return s, nil
}

func (f *fakeKube) RemoveAnnotations(_ context.Context, ns, name string, annotations []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.secrets[ns+"/"+name]
	if !ok {
		return errKubeSecretNotFound
	}
	ann := map[string]string{}
	for k, v := range s.Annotations {
		ann[k] = v
	}
	for _, a := range annotations {
		delete(ann, a)
		f.patched = append(f.patched, ns+"/"+name+" -"+a)
	}
	s.Annotations = ann
	f.secrets[ns+"/"+name] = s
	return nil
}

func (f *fakeKube) MintToken(_ context.Context, ns, sa, aud string, ttl time.Duration) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tokenErr != nil {
		return "", f.tokenErr
	}
	if ttl > 10*time.Minute {
		return "", fmt.Errorf("ttl %s exceeds the ten minute writer token", ttl)
	}
	f.tokens = append(f.tokens, fmt.Sprintf("%s/%s aud=%s", ns, sa, aud))
	return "sa-jwt:" + ns + ":" + sa + ":" + aud, nil
}

func (f *fakeKube) ConfigMapValue(_ context.Context, ns, name, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.configMaps[ns+"/"+name+"/"+key]
	if !ok {
		return "", fmt.Errorf("configmap %s/%s: %w", ns, name, errKubeConfigMapNotFound)
	}
	return v, nil
}

func (f *fakeKube) ForwardService(_ context.Context, ns, svc string, port int) (podForward, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forwards = append(f.forwards, fmt.Sprintf("%s/%s:%d", ns, svc, port))
	if f.forwardErr != nil {
		return nil, f.forwardErr
	}
	return &fakeForward{addr: f.forwardTo, onClose: func() { f.mu.Lock(); f.closed++; f.mu.Unlock() }}, nil
}

type fakeForward struct {
	addr    string
	onClose func()
}

func (f *fakeForward) Addr() string { return f.addr }
func (f *fakeForward) Close() error { f.onClose(); return nil }

// writerPolicy is the policy the OpenBao component's converge Job gives the
// forge-secret-writer role (pkg/deploy/openbao BuildAccess), for the fixture's
// (org, env): create and update on values, read and list on metadata.
var writerPolicy = []struct {
	path string
	caps []string
}{
	{"secret/data/tenants/acme/prod/*", []string{"create", "update"}},
	{"secret/metadata/tenants/acme/prod/*", []string{"list", "read"}},
	{"secret/metadata/tenants/acme/prod", []string{"list"}},
}

// fakeBao is an in-memory OpenBao that enforces the writer policy above. It is
// honest on purpose: a request is allowed only if a rule grants the capability
// on the path.
type fakeBao struct {
	t     *testing.T
	mu    sync.Mutex
	rules []struct {
		path string
		caps []string
	}
	login  baoWriterLogin
	saJWT  string
	kv     map[string][]string // path -> versions (values)
	calls  []string
	echoOn bool // echo the request body in an error response
	failKV int  // answer this HTTP status to KV writes (0 = normal)
}

func newFakeBao(t *testing.T) *fakeBao {
	t.Helper()
	bao := storeEnv().OpenBao
	login := baoWriterLogin{
		LoginPath: "auth/" + bao.AuthMount + "/login", Role: writerRole, Audience: bao.Audience,
		Namespace: bao.Namespace, ServiceAccount: writerServiceAccount,
	}
	return &fakeBao{
		t: t, rules: writerPolicy, login: login, kv: map[string][]string{},
		saJWT: "sa-jwt:" + login.Namespace + ":" + login.ServiceAccount + ":" + login.Audience,
	}
}

const fakeBaoToken = "b.fake-writer-token"

func (b *fakeBao) allowed(capability, path string) bool {
	for _, r := range b.rules {
		match := r.path == path || (strings.HasSuffix(r.path, "*") && strings.HasPrefix(path, strings.TrimSuffix(r.path, "*")))
		if !match {
			continue
		}
		for _, c := range r.caps {
			if c == capability {
				return true
			}
		}
	}
	return false
}

func (b *fakeBao) do(_ context.Context, method, path string, query url.Values, token string, payload any) (int, map[string]any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, method+" "+path)

	if path == b.login.LoginPath {
		p, _ := payload.(map[string]any)
		if p["role"] != b.login.Role || p["jwt"] != b.saJWT {
			return http.StatusBadRequest, map[string]any{"errors": []any{"role or token rejected"}}, nil
		}
		return 200, map[string]any{"auth": map[string]any{"client_token": fakeBaoToken, "lease_duration": float64(600)}}, nil
	}
	if token != fakeBaoToken {
		return http.StatusForbidden, map[string]any{"errors": []any{"permission denied"}}, nil
	}

	cap := map[string]string{http.MethodGet: "read", http.MethodPut: "create", http.MethodDelete: "delete"}[method]
	if query.Get("list") == "true" {
		cap = "list"
	}
	switch {
	case strings.Contains(path, "/data/") && method == http.MethodPut:
		if b.failKV != 0 {
			errs := []any{"internal error"}
			if b.echoOn {
				errs = []any{fmt.Sprintf("bad request body: %v", payload)}
			}
			return b.failKV, map[string]any{"errors": errs}, nil
		}
		_, exists := b.kv[path]
		if exists {
			cap = "update"
		}
		if !b.allowed(cap, path) {
			return http.StatusForbidden, map[string]any{"errors": []any{"permission denied"}}, nil
		}
		p := payload.(map[string]any)
		data := p["data"].(map[string]any)
		if opts, ok := p["options"].(map[string]any); ok {
			if cas, ok := opts["cas"]; ok && cas != len(b.kv[path]) && cas != float64(len(b.kv[path])) {
				return http.StatusBadRequest, map[string]any{"errors": []any{"check-and-set parameter did not match the current version"}}, nil
			}
		}
		if _, isString := data["value"].(string); !isString {
			b.t.Errorf("the value was sent as %T, not a JSON string (OpenBao audit-logs numbers in the clear)", data["value"])
		}
		b.kv[path] = append(b.kv[path], data["value"].(string))
		return 200, map[string]any{"data": map[string]any{"version": float64(len(b.kv[path]))}}, nil

	case strings.Contains(path, "/metadata/") && method == http.MethodGet:
		if !b.allowed(cap, path) {
			return http.StatusForbidden, map[string]any{"errors": []any{"permission denied"}}, nil
		}
		dataPath := strings.Replace(path, "/metadata/", "/data/", 1)
		vs, ok := b.kv[dataPath]
		if !ok {
			return http.StatusNotFound, nil, nil
		}
		versions := map[string]any{}
		for i := range vs {
			versions[fmt.Sprint(i+1)] = map[string]any{"deletion_time": "", "destroyed": false}
		}
		return 200, map[string]any{"data": map[string]any{"current_version": float64(len(vs)), "versions": versions}}, nil
	}
	if !b.allowed(cap, path) {
		return http.StatusForbidden, map[string]any{"errors": []any{"permission denied"}}, nil
	}
	return http.StatusNotFound, nil, nil
}

// fakeGSM is an in-memory gsmAPI.
type fakeGSM struct {
	mu       sync.Mutex
	secrets  map[string][]string // "project/id" -> versions
	disabled map[string]bool
	created  []string
	fail     error
	// accessed counts payload reads, which a metadata-only caller never makes.
	accessed int
	// disabledVersions are the single versions taken out of service.
	disabledVersions map[string]map[uint32]bool
}

func newFakeGSM() *fakeGSM {
	return &fakeGSM{secrets: map[string][]string{}, disabled: map[string]bool{}}
}

func (g *fakeGSM) Latest(_ context.Context, project, id string) (uint32, bool, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fail != nil {
		return 0, false, false, g.fail
	}
	vs, ok := g.secrets[project+"/"+id]
	if !ok {
		return 0, false, false, nil
	}
	return uint32(len(vs)), len(vs) > 0 && !g.disabled[project+"/"+id], true, nil
}

func (g *fakeGSM) AddVersion(_ context.Context, project, id string, value []byte, create bool) (uint32, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fail != nil {
		return 0, g.fail
	}
	k := project + "/" + id
	if _, ok := g.secrets[k]; !ok {
		if !create {
			return 0, errors.New("secret manager add version: HTTP 404 NOT_FOUND: no such secret")
		}
		g.secrets[k] = nil
		g.created = append(g.created, k)
	}
	g.secrets[k] = append(g.secrets[k], string(value))
	return uint32(len(g.secrets[k])), nil
}

func (g *fakeGSM) Access(_ context.Context, project, id string) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.accessed++
	vs := g.secrets[project+"/"+id]
	if len(vs) == 0 {
		return nil, errors.New("no version")
	}
	return []byte(vs[len(vs)-1]), nil
}

func (g *fakeGSM) Disable(_ context.Context, project, id string, version uint32) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	k := project + "/" + id
	if version < 1 || int(version) > len(g.secrets[k]) {
		return errors.New("secret manager disable version: HTTP 404 NOT_FOUND: no such version")
	}
	if g.disabledVersions == nil {
		g.disabledVersions = map[string]map[uint32]bool{}
	}
	if g.disabledVersions[k] == nil {
		g.disabledVersions[k] = map[uint32]bool{}
	}
	g.disabledVersions[k][version] = true
	return nil
}

func (g *fakeGSM) List(_ context.Context, project string) ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for k := range g.secrets {
		if p, id, _ := strings.Cut(k, "/"); p == project {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

// storeEnv returns the rendered env the store-source tests run against: an
// OpenBaoStore-backed Secret and a HostedStore-backed one in `app`, and a
// self-run OpenBao on the hub.
func storeEnv() *KCLEntities {
	return &KCLEntities{
		Project:       "acme",
		Env:           "prod",
		ClusterTarget: &ClusterTargetEntity{Cluster: "ctx-hub", Namespace: "app"},
		// The rendered `openbao` block (kcl/lib/openbao.k), as the SF6 render
		// emits it for env "prod" of project "acme" on the hub.
		OpenBao: &OpenBaoEntity{
			Name: "openbao", Namespace: "openbao", Cluster: "ctx-hub", ConnectedCluster: "hub",
			ServerURL: "https://openbao.openbao.svc:8200", CAConfigMap: "openbao-ca", KVMount: "secret",
			SelfCluster: "hub", AuthMount: "jwt-hub", Audience: "https://openbao.openbao.svc:8200/c/hub",
			ServiceAccount: "reliant-secrets", Org: "acme", EnvDir: "prod",
		},
		BootstrapSource: &BootstrapSourceEntity{Type: "gcpsm", Project: "acme-prod"},
		RequiredSecrets: []ExternalSecretEntity{
			{
				Name: "app-secrets", Namespace: "app", Keys: []string{"api-key", "dsn", "pw"},
				Source: &StoreSourceEntity{
					RenderedStoreSource: release.RenderedStoreSource{
						Store: release.RenderedStore{Type: release.StoreOpenBao},
						Data:  map[string]string{"api-key": "API_KEY", "pw": "DB_PASSWORD"},
					},
					Templates: map[string]string{"dsn": "postgres://u:{{ .pw }}@db/app"},
				},
			},
			{
				Name: "billing", Namespace: "app", Keys: []string{"stripe"},
				Source: &StoreSourceEntity{RenderedStoreSource: release.RenderedStoreSource{
					Store: release.RenderedStore{Type: release.StoreHosted},
					Data:  map[string]string{"stripe": "STRIPE_SECRET_KEY"},
				}},
			},
			{
				Name: "tls", Namespace: "app", Keys: []string{"cert"},
				Source: &StoreSourceEntity{RenderedStoreSource: release.RenderedStoreSource{
					Store: release.RenderedStore{Type: release.StoreGCPSecretManager, Project: "acme-prod"},
					Data:  map[string]string{"cert": "tls-cert-prod"},
				}},
			},
		},
	}
}

// installStoreSeams routes every store-source command at the fakes.
type storeFixture struct {
	t      *testing.T
	env    *KCLEntities
	kube   *fakeKube
	bao    *fakeBao
	gsm    *fakeGSM
	hosted *fakeHostedWriter
}

func installStoreFixture(t *testing.T, e *KCLEntities) *storeFixture {
	t.Helper()
	f := &storeFixture{t: t, env: e, kube: newFakeKube(), gsm: newFakeGSM(), hosted: &fakeHostedWriter{stored: map[string]uint32{}}}
	f.bao = newFakeBao(t)

	prevRender, prevKube, prevGSM, prevHosted := renderEntitiesForSecrets, newKubeAccess, newGSMAPI, newHostedSecretWriter
	renderEntitiesForSecrets = func(context.Context, string) (*KCLEntities, error) { return f.env, nil }
	newKubeAccess = func(kctx string) (kubeAccess, error) {
		if kctx == "" {
			return nil, errors.New("no kube context")
		}
		return f.kube, nil
	}
	newGSMAPI = func(context.Context) (gsmAPI, error) { return f.gsm, nil }
	newHostedSecretWriter = func(context.Context, string, *KCLEntities, bool) (hostedSecretWriter, cloud.Endpoint, error) {
		return f.hosted, cloud.Endpoint{URL: "https://cp.example.com"}, nil
	}
	prevOpen := openStoreSource
	openStoreSource = func(ctx context.Context, envName string, e *KCLEntities, ref storeRef, o storeOptions, writing bool) (storeSource, error) {
		if ref.Kind == release.StoreOpenBao {
			return openBaoStoreSourceWith(envName, e, o, func(kctx string) (kubeAccess, baoTransport, error) {
				if kctx == "" {
					return nil, nil, errors.New("no kube context")
				}
				return f.kube, f.bao, nil
			})
		}
		return prevOpen(ctx, envName, e, ref, o, writing)
	}
	t.Cleanup(func() {
		renderEntitiesForSecrets, newKubeAccess, newGSMAPI, newHostedSecretWriter = prevRender, prevKube, prevGSM, prevHosted
		openStoreSource = prevOpen
	})
	return f
}

// fakeHostedWriter is an in-memory hostedSecretWriter that keeps no value.
type fakeHostedWriter struct {
	mu      sync.Mutex
	stored  map[string]uint32
	sets    []string // "name cas=<bool>"
	failErr error
}

func (h *fakeHostedWriter) Set(_ context.Context, name, _ string) (uint32, error) {
	return h.write(name, false)
}
func (h *fakeHostedWriter) Create(_ context.Context, name, _ string) (uint32, error) {
	return h.write(name, true)
}
func (h *fakeHostedWriter) write(name string, createOnly bool) (uint32, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failErr != nil {
		return 0, h.failErr
	}
	h.sets = append(h.sets, fmt.Sprintf("%s cas=%t", name, createOnly))
	if createOnly && h.stored[name] > 0 {
		return 0, &cloud.Error{HTTPStatus: 409, Code: "aborted", Message: "secret version conflict: re-read and retry"}
	}
	h.stored[name]++
	return h.stored[name], nil
}
func (h *fakeHostedWriter) List(context.Context) ([]hostedSecretSummary, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []hostedSecretSummary
	for n, v := range h.stored {
		out = append(out, hostedSecretSummary{Name: n, CurrentVersion: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (h *fakeHostedWriter) Delete(context.Context, string) error { return nil }
