// Copyright (c) 2025 Reliant Labs
package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/localprobe"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/netguard"
)

// A configured endpoint is a user-declared OpenAI-compatible server
// (model_endpoints). It reuses the local model machinery — "<model>@local" ids,
// the local driver, the daemon relay — but its identity is the endpoint, not a
// daemon: provider "endpoint:<endpointID>" can never collide with
// "local:<daemonID>".

const (
	// EndpointProviderPrefix begins a ModelSelector provider that names a
	// configured endpoint: "endpoint:<endpointID>".
	EndpointProviderPrefix = "endpoint:"

	// DirectPlaceholderHost is the host the driver is pointed at when a
	// transport reaches the real server. DirectTransport rewrites it.
	directPlaceholderHost = "local-model.invalid"

	maxExtraBodyBytes = 16 << 10
)

// ReservedBodyKeys are request-body keys the driver owns; extra_body_json may
// not set them.
var ReservedBodyKeys = []string{"model", "messages", "tools", "stream"}

// ModelParams are the per-model request settings a user configured.
type ModelParams struct {
	// Temperature and TopP are DEFAULTS: they apply only when the request does
	// not already carry the key (a chat-level override always wins).
	Temperature *float64
	TopP        *float64
	// ExtraBody is merged into the request body over everything except the
	// reserved keys. It is an escape hatch and deliberately wins.
	ExtraBody map[string]json.RawMessage
}

// CustomEndpoint is what a Model needs to remember about the configured
// endpoint that serves it.
type CustomEndpoint struct {
	ID                     string
	Name                   string
	BaseURL                string
	Route                  string // db.ModelEndpointRoute*
	CredentialConnectionID string
	Params                 ModelParams
}

// EndpointConfig is one stored endpoint, decoded, with liveness resolved.
type EndpointConfig struct {
	ID                     string
	Name                   string
	BaseURL                string
	Route                  string
	DaemonID               string
	Machine                string // daemon hostname for VIA_DAEMON
	Online                 bool
	CredentialConnectionID string
	Probe                  *reliantv1.LocalModelEndpoint
	Models                 []*reliantv1.ModelEndpointModel
}

// EndpointLister is optionally implemented by a Directory that can also list
// the user's configured endpoints.
type EndpointLister interface {
	ConfiguredEndpoints(ctx context.Context, userID string) ([]EndpointConfig, error)
}

// EndpointSource is the slice of db.Repository the endpoint listing reads,
// beyond Source.
type EndpointSource interface {
	ListModelEndpoints(ctx context.Context, userID string) ([]*db.ModelEndpoint, error)
}

// DecodeEndpointModels reads models_json (a JSON array of ModelEndpointModel).
func DecodeEndpointModels(raw string) ([]*reliantv1.ModelEndpointModel, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("models_json is not a JSON array: %w", err)
	}
	out := make([]*reliantv1.ModelEndpointModel, 0, len(items))
	for _, item := range items {
		m := &reliantv1.ModelEndpointModel{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(item, m); err != nil {
			return nil, fmt.Errorf("decoding endpoint model: %w", err)
		}
		out = append(out, m)
	}
	return out, nil
}

// EncodeEndpointModels is DecodeEndpointModels' inverse.
func EncodeEndpointModels(ms []*reliantv1.ModelEndpointModel) (string, error) {
	items := make([]json.RawMessage, 0, len(ms))
	for _, m := range ms {
		b, err := protojson.Marshal(m)
		if err != nil {
			return "", err
		}
		items = append(items, b)
	}
	b, err := json.Marshal(items)
	return string(b), err
}

// DecodeProbe reads probe_json; an empty or unreadable value is "never probed".
func DecodeProbe(raw string) *reliantv1.LocalModelEndpoint {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	ep := &reliantv1.LocalModelEndpoint{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(raw), ep); err != nil {
		logging.Warn("[ModelEndpoints] Unreadable stored probe", "error", err)
		return nil
	}
	return ep
}

// EncodeProbe is DecodeProbe's inverse.
func EncodeProbe(ep *reliantv1.LocalModelEndpoint) string {
	if ep == nil {
		return ""
	}
	b, err := protojson.Marshal(ep)
	if err != nil {
		return ""
	}
	return string(b)
}

// ValidateExtraBody checks extra_body_json: empty, or a JSON object without
// reserved keys. It returns the decoded object.
func ValidateExtraBody(raw string) (map[string]json.RawMessage, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxExtraBodyBytes {
		return nil, fmt.Errorf("extra body JSON is too large (max %d KB)", maxExtraBodyBytes>>10)
	}
	var obj map[string]json.RawMessage
	dec := json.NewDecoder(strings.NewReader(raw))
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return nil, errors.New("extra body must be a JSON object, e.g. {\"min_p\": 0.05}")
	}
	if dec.More() {
		return nil, errors.New("extra body must be a single JSON object")
	}
	for _, key := range ReservedBodyKeys {
		if _, ok := obj[key]; ok {
			return nil, fmt.Errorf("extra body may not set %q: Reliant controls it", key)
		}
	}
	return obj, nil
}

// SynthesizeEndpoint builds the catalog models for one configured endpoint.
// Hidden models are skipped. A model the user configured but the server did
// not list is still offered (not every server lists everything).
func SynthesizeEndpoint(cfg EndpointConfig) []Model {
	kind := "openai_compatible"
	probed := map[string]*reliantv1.LocalModelInfo{}
	var order []string
	if cfg.Probe != nil {
		if cfg.Probe.GetKind() != "" {
			kind = cfg.Probe.GetKind()
		}
		for _, info := range cfg.Probe.GetModels() {
			if info.GetSupportsChat() && strings.TrimSpace(info.GetName()) != "" {
				probed[info.GetName()] = info
				order = append(order, info.GetName())
			}
		}
	}
	settings := map[string]*reliantv1.ModelEndpointModel{}
	for _, s := range cfg.Models {
		name := strings.TrimSpace(s.GetName())
		if name == "" {
			continue
		}
		settings[name] = s
		if _, ok := probed[name]; !ok {
			order = append(order, name)
		}
	}

	machine := cfg.Name
	online := cfg.Online
	if cfg.Route == db.ModelEndpointRouteDirect {
		// Nothing can be known about a remote server's liveness short of
		// calling it; a failed request says so precisely.
		online = true
	}
	var out []Model
	for _, name := range order {
		set := settings[name]
		if set == nil {
			set = &reliantv1.ModelEndpointModel{}
		}
		if set.GetHidden() {
			continue
		}
		info := &reliantv1.LocalModelInfo{Name: name, SupportsChat: true}
		if p := probed[name]; p != nil {
			info = &reliantv1.LocalModelInfo{
				Name: name, ContextWindow: p.GetContextWindow(), SupportsChat: true,
				SupportsTools: p.GetSupportsTools(), SupportsVision: p.GetSupportsVision(),
				SupportsThinking: p.GetSupportsThinking(), SizeBytes: p.GetSizeBytes(), ParameterSize: p.GetParameterSize(),
			}
		}
		if set.GetContextWindow() > 0 {
			info.ContextWindow = set.GetContextWindow()
		}
		if set.SupportsTools != nil {
			info.SupportsTools = set.GetSupportsTools()
		}
		if set.SupportsVision != nil {
			info.SupportsVision = set.GetSupportsVision()
		}
		if set.SupportsThinking != nil {
			info.SupportsThinking = set.GetSupportsThinking()
		}
		def := definitionFor(kind, info)
		if set.GetMaxOutputTokens() > 0 {
			def.Capabilities.MaxOutputTokens = int(set.GetMaxOutputTokens())
		}

		params := ModelParams{}
		if set.Temperature != nil {
			v := set.GetTemperature()
			params.Temperature = &v
		}
		if set.TopP != nil {
			v := set.GetTopP()
			params.TopP = &v
		}
		if extra, err := ValidateExtraBody(set.GetExtraBodyJson()); err != nil {
			logging.Warn("[ModelEndpoints] Ignoring invalid stored extra_body_json", "endpoint", cfg.ID, "model", name, "error", err)
		} else {
			params.ExtraBody = extra
		}
		relayID := ""
		if cfg.Route == db.ModelEndpointRouteViaDaemon {
			relayID = RelayEndpointID(cfg.BaseURL)
		}
		out = append(out, Model{
			Definition: def, DaemonID: cfg.DaemonID, Machine: machine, Online: online,
			EndpointID: relayID, EndpointKind: kind, Info: info,
			Custom: &CustomEndpoint{
				ID: cfg.ID, Name: cfg.Name, BaseURL: cfg.BaseURL, Route: cfg.Route,
				CredentialConnectionID: cfg.CredentialConnectionID, Params: params,
			},
		})
	}
	return out
}

// RelayEndpointID is the id a daemon gives a configured endpoint at baseURL,
// the only name its relay will accept: "cfg-" + the first 8 hex of the
// sha256 of the normalized root (base URL minus a trailing /v1 and slash).
func RelayEndpointID(baseURL string) string {
	root, err := localprobe.NormalizeRoot(baseURL)
	if err != nil {
		root = strings.TrimSpace(baseURL)
	}
	return localprobe.EndpointID(root)
}

// ---- directory ----

// ConfiguredEndpoints implements EndpointLister for a RepoDirectory whose
// source also reads model_endpoints.
func (r *RepoDirectory) ConfiguredEndpoints(ctx context.Context, userID string) ([]EndpointConfig, error) {
	if r == nil || r.src == nil {
		return nil, errors.New("local model directory has no data source")
	}
	es, ok := r.src.(EndpointSource)
	if !ok {
		return nil, nil
	}
	rows, err := es.ListModelEndpoints(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("listing model endpoints: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	online := map[string]bool{}
	hostnames := map[string]string{}
	needDaemons := false
	for _, row := range rows {
		needDaemons = needDaemons || row.Route == db.ModelEndpointRouteViaDaemon
	}
	if needDaemons {
		attachments, err := r.src.ListFreshDaemonAttachmentsForUser(ctx, userID, onlineStaleThreshold)
		if err != nil {
			return nil, fmt.Errorf("listing daemon attachments: %w", err)
		}
		for _, a := range attachments {
			online[a.DaemonID] = true
		}
		daemons, err := r.src.ListDaemonsByUserID(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("listing daemons: %w", err)
		}
		for _, d := range daemons {
			if d.Hostname != nil {
				hostnames[d.ID] = *d.Hostname
			}
		}
	}

	out := make([]EndpointConfig, 0, len(rows))
	for _, row := range rows {
		ms, err := DecodeEndpointModels(row.ModelsJSON)
		if err != nil {
			logging.Warn("[ModelEndpoints] Skipping endpoint with unreadable models", "endpoint", row.ID, "error", err)
			continue
		}
		cfg := EndpointConfig{
			ID: row.ID, Name: row.Name, BaseURL: row.BaseURL, Route: row.Route,
			Probe: DecodeProbe(row.ProbeJSON), Models: ms,
		}
		if row.CredentialConnectionID != nil {
			cfg.CredentialConnectionID = *row.CredentialConnectionID
		}
		if row.DaemonID != nil {
			cfg.DaemonID = *row.DaemonID
			cfg.Online = online[cfg.DaemonID]
			cfg.Machine = hostnames[cfg.DaemonID]
		}
		out = append(out, cfg)
	}
	return out, nil
}

// ---- transports ----

// CredentialSource yields an endpoint's stored credentials. Declared here, at
// its consumer; modelendpoints.EndpointCredentials satisfies it.
type CredentialSource interface {
	Get(ctx context.Context, userID, connectionID string) (apiKey string, headers map[string]string, err error)
}

// CustomRoutes carries what reaching a configured endpoint needs.
type CustomRoutes struct {
	// Relay builds the daemon relay transport for VIA_DAEMON endpoints.
	Relay TransportFactory
	// Policy governs DIRECT endpoints: hosted refuses private destinations.
	Policy netguard.Policy
	// Credentials is optional; endpoints without a stored credential never
	// consult it.
	Credentials CredentialSource
	// Dial, when set, replaces the guarded network transport for DIRECT
	// endpoints. Tests only.
	Dial http.RoundTripper
}

// TransportFor returns the RoundTripper a resolved model's driver must send
// all HTTP through. Detected models get the plain relay; configured endpoints
// additionally get their per-model request shaping.
func TransportFor(ctx context.Context, userID string, m *Model, relay TransportFactory, custom *CustomRoutes) (http.RoundTripper, error) {
	if m.Custom == nil {
		if relay == nil {
			return nil, &UnavailableError{Model: m.Definition.ID, Machine: m.Machine, Reason: "local models are not reachable from this server"}
		}
		return relay(userID, m.DaemonID, m.EndpointID), nil
	}
	if custom == nil {
		return nil, &UnavailableError{Model: m.Definition.ID, Machine: m.Machine, Reason: "custom endpoints are not reachable from this server"}
	}

	var apiKey string
	var headers map[string]string
	if m.Custom.CredentialConnectionID != "" {
		if custom.Credentials == nil {
			return nil, &UnavailableError{Model: m.Definition.ID, Machine: m.Machine, Reason: "its credentials cannot be read on this server"}
		}
		var err error
		if apiKey, headers, err = custom.Credentials.Get(ctx, userID, m.Custom.CredentialConnectionID); err != nil {
			return nil, &UnavailableError{Model: m.Definition.ID, Machine: m.Machine, Reason: "its stored credentials could not be read"}
		}
	}

	var next http.RoundTripper
	switch m.Custom.Route {
	case "via_daemon":
		if custom.Relay == nil {
			return nil, &UnavailableError{Model: m.Definition.ID, Machine: m.Machine, Reason: "no machine relay is available on this server"}
		}
		next = custom.Relay(userID, m.DaemonID, m.EndpointID)
	default:
		inner := custom.Dial
		if inner == nil {
			inner = custom.Policy.Transport()
		}
		next = &directTransport{base: m.Custom.BaseURL, apiKey: apiKey, headers: headers, policy: custom.Policy, next: inner}
	}
	return &shapingTransport{params: m.Custom.Params, next: next}, nil
}

// directTransport sends the driver's placeholder-host requests to the real
// endpoint. Anything not addressed to the placeholder (a redirect hop) passes
// through untouched, so the dial guard sees — and can refuse — every hop.
type directTransport struct {
	base    string
	apiKey  string
	headers map[string]string
	policy  netguard.Policy
	next    http.RoundTripper
}

func (t *directTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() != directPlaceholderHost {
		return t.next.RoundTrip(req)
	}
	target, err := url.Parse(t.base)
	if err != nil {
		return nil, fmt.Errorf("custom endpoint has an invalid base URL: %w", err)
	}
	// Fail early and readably; the dial guard is the part that cannot be
	// raced (DNS rebinding), this is the part that explains itself.
	if err := t.policy.CheckHost(req.Context(), target.Hostname()); err != nil {
		return nil, err
	}

	out := req.Clone(req.Context())
	// The driver's base URL is placeholder + "/v1"; the endpoint's own base
	// URL already carries whatever prefix it needs.
	rest := strings.TrimPrefix(req.URL.Path, "/v1")
	u := *target
	u.Path = strings.TrimRight(target.Path, "/") + rest
	u.RawPath = ""
	u.RawQuery = req.URL.RawQuery
	out.URL = &u
	out.Host = u.Host
	for k, v := range t.headers {
		out.Header.Set(k, v)
	}
	if t.apiKey != "" {
		out.Header.Set("Authorization", "Bearer "+t.apiKey)
	}
	return t.next.RoundTrip(out)
}

// shapingTransport applies a model's ModelParams to chat-completion request
// bodies, on whichever route carries them.
type shapingTransport struct {
	params ModelParams
	next   http.RoundTripper
}

func (t *shapingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || req.Body == nil || !strings.HasSuffix(req.URL.Path, "/chat/completions") ||
		(t.params.Temperature == nil && t.params.TopP == nil && len(t.params.ExtraBody) == 0) {
		return t.next.RoundTrip(req)
	}
	raw, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("reading request body: %w", err)
	}
	shaped, err := ShapeChatBody(raw, t.params)
	if err != nil {
		return nil, err
	}
	out := req.Clone(req.Context())
	out.Body = io.NopCloser(bytes.NewReader(shaped))
	out.ContentLength = int64(len(shaped))
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(shaped)), nil }
	return t.next.RoundTrip(out)
}

// ShapeChatBody applies p to a chat-completions JSON body: temperature and
// top_p fill in only when absent, extra body keys then override (reserved keys
// never do).
func ShapeChatBody(body []byte, p ModelParams) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("request body is not a JSON object: %v", err)
	}
	setDefault := func(key string, v *float64) error {
		if v == nil {
			return nil
		}
		if _, set := obj[key]; set {
			return nil
		}
		b, err := json.Marshal(*v)
		if err != nil {
			return err
		}
		obj[key] = b
		return nil
	}
	if err := setDefault("temperature", p.Temperature); err != nil {
		return nil, err
	}
	if err := setDefault("top_p", p.TopP); err != nil {
		return nil, err
	}
	reserved := map[string]bool{}
	for _, k := range ReservedBodyKeys {
		reserved[k] = true
	}
	for k, v := range p.ExtraBody {
		if !reserved[k] {
			obj[k] = v
		}
	}
	return json.Marshal(obj)
}
