package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/secretaccess"
)

// The self-run OpenBaoStore writer (design decision N2, option a).
//
// `forge secret set` against an env that runs its own OpenBao needs a
// credential that can write the env's values and nothing else, from a person
// who holds only a kubeconfig. The path:
//
//  1. mint a ten-minute token for the forge-rendered `forge-secret-writer`
//     service account with the TokenRequest API, using the operator's kube
//     context, for the audience the render's `openbao` block names;
//  2. log in to the store's own JWT mount (the block's auth_mount) as the
//     writer role. The OpenBao component's converge Job creates that role
//     (pkg/deploy/openbao, RoleWriter): bound to the account and the audience,
//     with a policy that can create and update, never read, under the env's
//     directory and read its metadata (versions, never values). The CLI
//     derives none of that: it logs in to what the render says exists;
//  3. write with the batch token that login returns. A create-only write
//     (import) sends check-and-set 0.
//
// Nothing is stored: the kubeconfig is the trust root, the token lives for the
// command, and OpenBao's audit device records every write.
//
// THE STORE IS REACHED OVER A PORT-FORWARD, WITH ITS CERTIFICATE VERIFIED.
// The API server's service proxy would not do: its transport to the backend is
// built with InsecureSkipVerify (kube-apiserver's CreateProxyTransport: "proxying
// to pods and services is IP-based... don't expect to be able to verify the
// hostname"), and that is the API server dialing, so the client cannot supply a
// CA. A request that carries a write-capable token across an unverified hop is
// the exposure a verified one closes. A port-forward is a raw byte stream
// between the CLI and the pod, so the TLS handshake is the CLI's own, end to
// end, and it verifies against the CA the component publishes (the ConfigMap
// named by the render's `openbao.ca_configmap`, in the store's namespace) for
// the name the certificate answers to (the host of the render's server_url).
// There is no fallback to an unverified connection: a CA that is absent or does
// not match is an error that names the fix, before any token leaves the CLI.
//
// The operator needs `get` on configmaps, services and pods, and `create` on
// pods/portforward, in the store's namespace.

// writerRole is the login role of the forge-secret-writer service account, the
// name pkg/deploy/openbao creates on the store's own mount (openbao.RoleWriter).
// It is restated here, not imported: the CLI logs in to a role the component's
// converge Job made, and a test pins the two names together.
const writerRole = "forge-secret-writer"

// writerServiceAccount is the account the component renders for the writer
// (forge.OpenBaoSAWriter's default). The render's `openbao` block does not
// carry it, so a non-default account is a known gap (see deviations).
const writerServiceAccount = "forge-secret-writer"

// OpenBaoEntity is the rendered `openbao` block (kcl/lib/openbao.k): where the
// store is and how a client logs in to it. Declarations only; no value.
type OpenBaoEntity struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Image     string `json:"image,omitempty"`
	// Cluster is the kube context the store runs in; ConnectedCluster is the
	// name `forge cluster connect` registered.
	Cluster          string `json:"cluster,omitempty"`
	ConnectedCluster string `json:"connected_cluster,omitempty"`
	// ServerURL is the in-cluster address (https://<name>.<ns>.svc:<port>).
	ServerURL   string `json:"server_url,omitempty"`
	CAConfigMap string `json:"ca_configmap,omitempty"`
	KVMount     string `json:"kv_mount,omitempty"`
	// SelfCluster is the store's own cluster id: its auth mount is jwt-<id>.
	SelfCluster string `json:"self_cluster,omitempty"`
	AuthMount   string `json:"auth_mount,omitempty"`
	// Audience is what the cluster's tokens must carry to log in.
	Audience       string `json:"audience,omitempty"`
	ServiceAccount string `json:"service_account,omitempty"`
	Org            string `json:"org,omitempty"`
	EnvDir         string `json:"env,omitempty"`
	// BootstrapNames are the BootstrapSecret names the component declares that
	// a person may import (the seal key, the Postgres password and the recovery
	// escrow).
	BootstrapNames []string `json:"bootstrap_names,omitempty"`
	// BootstrapGenerate are the BootstrapSecrets (by secret id) the deploy
	// mints when the bootstrap source has none: only under a
	// GCPSecretManager source, and only the seal key and the Postgres
	// password.
	BootstrapGenerate map[string]release.RenderedGeneratedSecret `json:"bootstrap_generate,omitempty"`
	// Bootstrap is the delivery the component renders under a
	// GCPSecretManager source: nil under any other.
	Bootstrap *OpenBaoBootstrapEntity `json:"bootstrap,omitempty"`
}

// OpenBaoBootstrapEntity is the resolved Secret Manager delivery: the Google
// identities it runs as, and which secrets it moves. Declarations only.
type OpenBaoBootstrapEntity struct {
	Project string `json:"project"`
	// Namespace is the store's, where both service accounts live.
	Namespace            string `json:"namespace"`
	ReaderServiceAccount string `json:"reader_service_account"`
	ReaderGSA            string `json:"reader_gsa"`
	EscrowServiceAccount string `json:"escrow_service_account"`
	EscrowGSA            string `json:"escrow_gsa"`
	ESONamespace         string `json:"eso_namespace"`
	SealSecret           string `json:"seal_secret"`
	PasswordSecret       string `json:"password_secret,omitempty"`
	EscrowSecret         string `json:"escrow_secret"`
}

// BootstrapSourceEntity is the rendered Bundle.bootstrap_source.
type BootstrapSourceEntity struct {
	Type    string `json:"type"`
	Project string `json:"project,omitempty"`
}

// apiPort is the port of ServerURL (https://<name>.<ns>.svc:<port>).
func (o *OpenBaoEntity) apiPort() int {
	if u, err := url.Parse(o.ServerURL); err == nil {
		if p, err := strconv.Atoi(u.Port()); err == nil && p > 0 {
			return p
		}
	}
	return 8200
}

// baoTransport sends one OpenBao API request. The payload and the response are
// JSON objects; the response may be nil (204).
type baoTransport interface {
	do(ctx context.Context, method, path string, query url.Values, token string, payload any) (status int, body map[string]any, err error)
}

// caKey is the ConfigMap key the OpenBao component publishes the CA under.
const caKey = "ca.crt"

// requestTimeout bounds one store request, including the TLS handshake.
const requestTimeout = 30 * time.Second

// maxStoreResponse bounds how much of a response is read.
const maxStoreResponse = 8 << 20

// tunnelTransport reaches the store through a port-forward to one of its pods,
// speaking TLS to it end to end. The tunnel opens on the first request and
// lives until Close, so a command's requests share it.
type tunnelTransport struct {
	kube        kubeAccess
	namespace   string
	service     string
	port        int
	host        string // the name the certificate answers to
	caConfigMap string

	mu     sync.Mutex
	client *http.Client
	fwd    podForward
	closed bool
}

func newTunnelTransport(kube kubeAccess, bao *OpenBaoEntity) *tunnelTransport {
	host := bao.Name + "." + bao.Namespace + ".svc"
	if u, err := url.Parse(bao.ServerURL); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	return &tunnelTransport{
		kube: kube, namespace: bao.Namespace, service: bao.Name, port: bao.apiPort(),
		host: host, caConfigMap: bao.CAConfigMap,
	}
}

// connect reads the CA, then opens the tunnel. The CA comes first: with none,
// no connection is made at all.
func (t *tunnelTransport) connect(ctx context.Context) (*http.Client, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, errors.New("the OpenBao connection is closed")
	}
	if t.client != nil {
		return t.client, nil
	}
	if t.caConfigMap == "" {
		return nil, errors.New("the env's render names no CA for the OpenBao component (openbao.ca_configmap)\n" +
			"fix: upgrade forge so the render describes the store, then re-run; the CLI will not talk to the store without verifying its certificate")
	}
	pemCA, err := t.kube.ConfigMapValue(ctx, t.namespace, t.caConfigMap, caKey)
	if err != nil {
		if errors.Is(err, errKubeConfigMapNotFound) {
			return nil, fmt.Errorf("%w\nfix: the OpenBao component publishes its CA to this ConfigMap when it converges; run `forge env deploy` for this env first. The CLI will not talk to the store without verifying its certificate", err)
		}
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(pemCA)) {
		return nil, fmt.Errorf("configmap %s/%s holds no PEM certificate under %q", t.namespace, t.caConfigMap, caKey)
	}
	fwd, err := t.kube.ForwardService(ctx, t.namespace, t.service, t.port)
	if err != nil {
		return nil, err
	}
	addr := fwd.Addr()
	t.fwd = fwd
	t.client = &http.Client{
		Timeout: requestTimeout,
		// A redirect would carry the token to wherever it points.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
			},
			TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: t.host, MinVersion: tls.VersionTLS12},
		},
	}
	return t.client, nil
}

// Close ends the tunnel. Safe to call more than once.
func (t *tunnelTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	if t.client != nil {
		t.client.CloseIdleConnections()
		t.client = nil
	}
	if t.fwd != nil {
		err := t.fwd.Close()
		t.fwd = nil
		return err
	}
	return nil
}

func (t *tunnelTransport) do(ctx context.Context, method, path string, query url.Values, token string, payload any) (int, map[string]any, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		body = strings.NewReader(string(b))
	}
	client, err := t.connect(ctx)
	if err != nil {
		return 0, nil, err
	}
	u := "https://" + net.JoinHostPort(t.host, strconv.Itoa(t.port)) + "/v1/" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, t.explain(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxStoreResponse))
	if err != nil {
		return 0, nil, fmt.Errorf("read the OpenBao response: %w", err)
	}
	var out map[string]any
	if len(strings.TrimSpace(string(raw))) > 0 {
		// A non-JSON body (an error page) leaves out nil; the status carries the answer.
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out, nil
}

// explain names the fix when the failure is the certificate not verifying.
func (t *tunnelTransport) explain(err error) error {
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	switch {
	case errors.As(err, &unknown), errors.As(err, &host), errors.As(err, &invalid):
		return fmt.Errorf("the OpenBao certificate did not verify against the CA in configmap %s/%s for %q: %w\n"+
			"fix: the CA must be the one that signed the store's current certificate; run `forge env deploy` for this env to re-publish it. Nothing was sent to the store",
			t.namespace, t.caConfigMap, t.host, err)
	}
	return fmt.Errorf("reach OpenBao %s/%s over a port-forward: %w", t.namespace, t.service, err)
}

// baoStoreSource is the self-run store, signed in as the writer.
type baoStoreSource struct {
	tr    baoTransport
	scope secretaccess.Scope
	where string

	login baoWriterLogin
	kube  kubeAccess
	token string
}

// baoWriterLogin is everything a client needs to sign in as the writer.
type baoWriterLogin struct {
	LoginPath      string // auth/<auth_mount>/login
	Role           string
	Audience       string
	Namespace      string // of the writer's service account (the store's)
	ServiceAccount string
}

func (s *baoStoreSource) describe() string { return s.where }

// Close ends the connection to the store, if the transport holds one.
func (s *baoStoreSource) Close() error {
	if c, ok := s.tr.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// signIn exchanges a freshly minted service-account token for a batch token.
func (s *baoStoreSource) signIn(ctx context.Context) error {
	if s.token != "" {
		return nil
	}
	sa, err := s.kube.MintToken(ctx, s.login.Namespace, s.login.ServiceAccount, s.login.Audience, tokenTTL)
	if err != nil {
		if isNotFound(err) {
			return fmt.Errorf("%w\nfix: the writer service account is rendered with the OpenBao component; run `forge env deploy` for this env first", err)
		}
		return err
	}
	status, body, err := s.tr.do(ctx, http.MethodPut, s.login.LoginPath, nil, "", map[string]any{"role": s.login.Role, "jwt": sa})
	if err != nil {
		return redactValue(err, sa)
	}
	if status != http.StatusOK {
		return fmt.Errorf("sign in to OpenBao as the writer role %q failed (HTTP %d%s)\n"+
			"fix: the role is created by the OpenBao component's converge Job; check it ran for this env, and that this cluster is registered (forge cluster connect)",
			s.login.Role, status, baoErrors(body))
	}
	tok := dig(body, "auth", "client_token")
	if tok == "" {
		return errors.New("OpenBao answered the writer login without a token")
	}
	s.token = tok
	return nil
}

func (s *baoStoreSource) put(ctx context.Context, name, value string, createOnly bool) (uint32, error) {
	if err := s.signIn(ctx); err != nil {
		return 0, err
	}
	// The value is a JSON string under "value": OpenBao's audit log HMACs
	// strings and passes numbers through in the clear, so it is never coerced
	// to anything else (the same rule the hosted store enforces).
	payload := map[string]any{"data": map[string]any{"value": value}}
	if createOnly {
		payload["options"] = map[string]any{"cas": 0}
	}
	status, body, err := s.tr.do(ctx, http.MethodPut, s.scope.SecretPath(name), nil, s.token, payload)
	if err != nil {
		return 0, redactValue(err, value)
	}
	switch {
	case status == http.StatusOK:
		return uint32(digNum(body, "data", "version")), nil
	case createOnly && status == http.StatusBadRequest && strings.Contains(strings.Join(baoErrorList(body), " "), "check-and-set"):
		return 0, errStoreValueExists
	case status == http.StatusForbidden:
		return 0, redactValue(fmt.Errorf("OpenBao refused the write (HTTP 403): the writer policy does not cover this path%s", baoErrors(body)), value)
	}
	return 0, redactValue(fmt.Errorf("OpenBao write failed (HTTP %d%s)", status, baoErrors(body)), value)
}

func (s *baoStoreSource) presence(ctx context.Context, names []string) (map[string]uint32, error) {
	if err := s.signIn(ctx); err != nil {
		return nil, err
	}
	versions := map[string]uint32{}
	for _, n := range names {
		status, body, err := s.tr.do(ctx, http.MethodGet, s.metadataPath(n), nil, s.token, nil)
		if err != nil {
			return nil, err
		}
		switch status {
		case http.StatusOK:
			if v, ok := currentUsableVersion(body); ok {
				versions[n] = v
			}
		case http.StatusNotFound:
		default:
			return nil, fmt.Errorf("read metadata of %s: OpenBao answered HTTP %d%s", n, status, baoErrors(body))
		}
	}
	return versions, nil
}

// metadataPath is the KV-v2 metadata path of a name. Metadata carries version
// numbers and timestamps, never a value.
func (s *baoStoreSource) metadataPath(name string) string {
	return strings.Replace(s.scope.SecretPath(name), "/data/", "/metadata/", 1)
}

// currentUsableVersion reads a metadata document: the current version, unless
// it was deleted or destroyed.
func currentUsableVersion(body map[string]any) (uint32, bool) {
	data, _ := body["data"].(map[string]any)
	cur := uint32(digNum(body, "data", "current_version"))
	if cur == 0 {
		return 0, false
	}
	versions, _ := data["versions"].(map[string]any)
	if v, ok := versions[fmt.Sprint(cur)].(map[string]any); ok {
		if d, _ := v["deletion_time"].(string); d != "" {
			return 0, false
		}
		if destroyed, _ := v["destroyed"].(bool); destroyed {
			return 0, false
		}
	}
	return cur, true
}

// openBaoStoreSource builds the self-run store for the env: the OpenBao the
// bundle declares, reached through the kube context it runs in.
func openBaoStoreSource(ctx context.Context, envName string, e *KCLEntities, o storeOptions) (storeSource, error) {
	return openBaoStoreSourceWith(envName, e, o, func(kctx string) (kubeAccess, baoTransport, error) {
		kube, err := newKubeAccess(kctx)
		if err != nil {
			return nil, nil, err
		}
		return kube, newTunnelTransport(kube, e.OpenBao), nil
	})
}

// openBaoStoreSourceWith is the construction with the transport injected: the
// checks before any cluster is touched are production's, whoever supplies it.
func openBaoStoreSourceWith(envName string, e *KCLEntities, o storeOptions, connect func(kubeContext string) (kubeAccess, baoTransport, error)) (storeSource, error) {
	bao := e.OpenBao
	if bao == nil {
		return nil, fmt.Errorf("env %q reads from an OpenBaoStore but declares no Bundle.openbao\nfix: declare `openbao = forge.OpenBao {}` in its KCL", envName)
	}
	kctx := o.KubeContext
	if kctx == "" {
		kctx = bao.Cluster
	}
	if kctx == "" && e.ClusterTarget != nil {
		kctx = e.ClusterTarget.Cluster
	}
	kube, tr, err := connect(kctx)
	if err != nil {
		return nil, err
	}
	return newOpenBaoSource(e, bao, kube, tr, kctx)
}

// newOpenBaoSource resolves where the env's values live and how to sign in,
// from the render's `openbao` block. The directory is the block's (org, env),
// not the CLI's --env: the component wrote its policy for those.
func newOpenBaoSource(e *KCLEntities, bao *OpenBaoEntity, kube kubeAccess, tr baoTransport, kubeContext string) (*baoStoreSource, error) {
	org, env := bao.Org, bao.EnvDir
	if org == "" {
		org = e.Project
	}
	if org == "" || env == "" || bao.AuthMount == "" || bao.Audience == "" {
		return nil, errors.New("the env's render carries an incomplete `openbao` block (org, env, auth_mount and audience are required to sign in)\n" +
			"fix: upgrade forge so the render describes the store, then re-run")
	}
	scope := secretaccess.Scope{Mount: bao.KVMount, Org: org, Env: env}
	if err := scope.Validate(); err != nil {
		return nil, fmt.Errorf("the OpenBao directory in the render is not usable: %w", err)
	}
	return &baoStoreSource{
		tr: tr, scope: scope, kube: kube,
		login: baoWriterLogin{
			LoginPath: "auth/" + bao.AuthMount + "/login", Role: writerRole, Audience: bao.Audience,
			Namespace: bao.Namespace, ServiceAccount: writerServiceAccount,
		},
		where: fmt.Sprintf("self-run OpenBao %s/%s (%s)", bao.Namespace, bao.Name, kubeContext),
	}, nil
}

// tokenTTL is the lifetime of the writer's service-account token and of the
// batch token it buys (both ten minutes, the component's role maximum).
const tokenTTL = 10 * time.Minute

func isNotFound(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "NotFound"))
}

// dig reads a nested string from a decoded JSON object.
func dig(m map[string]any, path ...string) string {
	var cur any = m
	for _, p := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = obj[p]
	}
	s, _ := cur.(string)
	return s
}

func digNum(m map[string]any, path ...string) float64 {
	var cur any = m
	for _, p := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return 0
		}
		cur = obj[p]
	}
	f, _ := cur.(float64)
	return f
}

func baoErrorList(body map[string]any) []string {
	raw, _ := body["errors"].([]any)
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if str, ok := r.(string); ok {
			out = append(out, str)
		}
	}
	return out
}

// baoErrors renders OpenBao's error list for a message. These strings name
// paths and policies, never request bodies.
func baoErrors(body map[string]any) string {
	if l := baoErrorList(body); len(l) > 0 {
		return ": " + strings.Join(l, "; ")
	}
	return ""
}
