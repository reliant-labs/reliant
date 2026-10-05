// Package manifest loads and validates integration manifests: YAML authored,
// proto typed (reliantv1.IntegrationManifest), decoded strictly so an unknown
// field is a load error.
package manifest

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/tmpl"
	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"
)

// Trust says who authored a manifest. Only curated manifests (embedded in the
// binary) may claim server or any placement; see TOOL_PLACEMENT.md §2.3.
type Trust int

const (
	TrustUser Trust = iota
	TrustCurated
)

const (
	PlacementServer = "server"
	PlacementAny    = "any"
	PlacementDaemon = "daemon"

	ConnectionNone = "none"
)

var (
	idPattern     = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	actionPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	toolPattern   = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	methods       = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}
)

// Parse decodes and validates one manifest document.
func Parse(data []byte, trust Trust) (*reliantv1.IntegrationManifest, error) {
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("manifest yaml: %w", err)
	}
	asJSON, err := json.Marshal(normalize(doc))
	if err != nil {
		return nil, fmt.Errorf("manifest yaml: %w", err)
	}
	m := &reliantv1.IntegrationManifest{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(asJSON, m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if err := Validate(m, trust); err != nil {
		return nil, fmt.Errorf("manifest %q: %w", m.GetId(), err)
	}
	return m, nil
}

// normalize turns yaml.v3's map[string]any / map[any]any into JSON-encodable maps.
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalize(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = normalize(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalize(val)
		}
		return out
	default:
		return v
	}
}

// LoadFS loads every <dir>/manifest.yaml under fsys. Manifests are returned
// sorted by id then version, and an id@version pair may appear only once.
func LoadFS(fsys fs.FS, trust Trust) ([]*reliantv1.IntegrationManifest, error) {
	files, err := fs.Glob(fsys, "*/manifest*.yaml")
	if err != nil {
		return nil, err
	}
	seen := map[string]string{}
	var out []*reliantv1.IntegrationManifest
	for _, file := range files {
		data, err := fs.ReadFile(fsys, file)
		if err != nil {
			return nil, err
		}
		m, err := Parse(data, trust)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if dir := path.Dir(file); dir != m.GetId() {
			return nil, fmt.Errorf("%s: directory %q must match manifest id %q", file, dir, m.GetId())
		}
		key := fmt.Sprintf("%s@%d", m.GetId(), m.GetVersion())
		if prev, dup := seen[key]; dup {
			return nil, fmt.Errorf("%s: %s is already defined by %s", file, key, prev)
		}
		seen[key] = file
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GetId() != out[j].GetId() {
			return out[i].GetId() < out[j].GetId()
		}
		return out[i].GetVersion() < out[j].GetVersion()
	})
	return out, nil
}

// ToolName is the agent tool name for an action.
func ToolName(m *reliantv1.IntegrationManifest, a *reliantv1.ActionSpec) string {
	if n := a.GetTool().GetName(); n != "" {
		return n
	}
	return m.GetId() + "__" + strings.ReplaceAll(a.GetId(), ".", "_")
}

// Validate applies the load-time rules.
func Validate(m *reliantv1.IntegrationManifest, trust Trust) error {
	if !idPattern.MatchString(m.GetId()) {
		return fmt.Errorf("id %q must match %s", m.GetId(), idPattern)
	}
	if m.GetVersion() < 1 {
		return fmt.Errorf("version must be >= 1 (the manifest major version)")
	}
	if len(m.GetTriggers()) > 0 {
		return fmt.Errorf("triggers are reserved and not yet supported")
	}
	if err := validateKeywords("", m.GetKeywords()); err != nil {
		return err
	}
	conn := m.GetConnection()
	allowed, _, err := validateConnection(conn, trust)
	if err != nil {
		return err
	}
	if len(m.GetActions()) == 0 {
		return fmt.Errorf("at least one action is required")
	}
	ids := map[string]bool{}
	tools := map[string]bool{}
	for _, a := range m.GetActions() {
		if ids[a.GetId()] {
			return fmt.Errorf("duplicate action id %q", a.GetId())
		}
		ids[a.GetId()] = true
		if err := validateAction(m, a, conn, allowed, trust); err != nil {
			return fmt.Errorf("action %q: %w", a.GetId(), err)
		}
		if a.GetTool().GetExpose() {
			name := ToolName(m, a)
			if tools[name] {
				return fmt.Errorf("action %q: duplicate tool name %q", a.GetId(), name)
			}
			tools[name] = true
		}
	}
	return nil
}

func parseHTTPS(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("%q must be an https URL", raw)
	}
	if u.Hostname() == "" || u.User != nil {
		return nil, fmt.Errorf("%q must have a host and no userinfo", raw)
	}
	return u, nil
}

func validateAction(m *reliantv1.IntegrationManifest, a *reliantv1.ActionSpec, conn *reliantv1.ConnectionSpec, allowed map[string]bool, trust Trust) error {
	if !actionPattern.MatchString(a.GetId()) {
		return fmt.Errorf("id must match %s", actionPattern)
	}
	switch a.GetKind() {
	case "", "call":
	default:
		return fmt.Errorf("kind %q is not supported (only call)", a.GetKind())
	}
	switch a.GetPlacement() {
	case PlacementServer, PlacementAny:
		if trust != TrustCurated {
			return fmt.Errorf("placement %q may only be claimed by curated integrations", a.GetPlacement())
		}
	case PlacementDaemon:
	case "":
		return fmt.Errorf("placement is required (server, any or daemon)")
	default:
		return fmt.Errorf("unknown placement %q", a.GetPlacement())
	}
	if a.GetTool().GetName() != "" && !toolPattern.MatchString(a.GetTool().GetName()) {
		return fmt.Errorf("tool.name %q must match %s", a.GetTool().GetName(), toolPattern)
	}
	if strings.ContainsAny(a.GetSummary(), "\r\n") || len(a.GetSummary()) > 200 {
		return fmt.Errorf("summary must be one line of at most 200 characters")
	}
	if err := validateKeywords("", a.GetKeywords()); err != nil {
		return err
	}
	if a.GetParams() != nil {
		if t, _ := a.GetParams().AsMap()["type"].(string); t != "object" {
			return fmt.Errorf("params must be a JSON Schema with type: object")
		}
		if err := validateSchema("params", a.GetParams().AsMap()); err != nil {
			return err
		}
	} else if a.GetTool().GetExpose() {
		return fmt.Errorf("params are required when tool.expose is set")
	}
	if s := a.GetOutput().GetSchema(); s != nil {
		if err := validateSchema("output.schema", s.AsMap()); err != nil {
			return err
		}
	}
	if a.GetExecutor() != "" {
		if _, ok := ExecutorName(a); !ok {
			return fmt.Errorf("executor %q must be go:<name> (%s)", a.GetExecutor(), executorPattern)
		}
		if a.GetRequest() != nil {
			return fmt.Errorf("executor and request are mutually exclusive")
		}
		if a.GetParams() == nil {
			return fmt.Errorf("params are required with an executor: the schema is the executor's contract")
		}
		if a.GetPlacement() == PlacementDaemon {
			return fmt.Errorf("an executor runs on the server; placement must be server or any")
		}
		if sel := a.GetOutput().GetSelect(); sel != "" {
			return fmt.Errorf("output.select applies to an HTTP response; an executor returns its data directly")
		}
		return nil
	}
	req := a.GetRequest()
	if req == nil {
		return fmt.Errorf("request is required (or an executor)")
	}
	if err := validateRequest(req); err != nil {
		return err
	}
	if a.GetPlacement() != PlacementDaemon {
		if err := checkHost(req, conn, allowed); err != nil {
			return err
		}
	}
	return validateOutput(a.GetOutput(), req.GetPagination())
}

func validateRequest(req *reliantv1.HttpRequestSpec) error {
	if (req.GetUrl() == "") == (req.GetPath() == "") {
		return fmt.Errorf("request needs exactly one of url or path")
	}
	method := req.GetMethod()
	if tmpl.HasExpr(method) {
		if err := tmpl.Validate(method); err != nil {
			return fmt.Errorf("request.method: %w", err)
		}
	} else if !methods[method] {
		return fmt.Errorf("request.method %q must be one of GET, POST, PUT, PATCH, DELETE", method)
	}
	if req.GetTimeoutSeconds() < 0 || req.GetTimeoutSeconds() > 120 {
		return fmt.Errorf("request.timeout_seconds must be 0..120")
	}
	if req.GetMaxResponseBytes() < 0 || req.GetMaxResponseBytes() > 10<<20 {
		return fmt.Errorf("request.max_response_bytes must be 0..10485760")
	}
	for _, s := range []string{req.GetUrl(), req.GetPath()} {
		if err := tmpl.Validate(s); err != nil {
			return fmt.Errorf("request: %w", err)
		}
	}
	for k, v := range req.GetQuery() {
		if err := tmpl.Validate(v); err != nil {
			return fmt.Errorf("request.query.%s: %w", k, err)
		}
	}
	for k, v := range req.GetHeaders() {
		if err := tmpl.Validate(v); err != nil {
			return fmt.Errorf("request.headers.%s: %w", k, err)
		}
	}
	for name, expr := range map[string]string{"headers_expr": req.GetHeadersExpr(), "query_expr": req.GetQueryExpr(), "body_expr": req.GetBodyExpr()} {
		if expr == "" {
			continue
		}
		if err := tmpl.ValidateExpr(expr); err != nil {
			return fmt.Errorf("request.%s: %w", name, err)
		}
	}
	if req.GetBody() != nil && req.GetBodyExpr() != "" {
		return fmt.Errorf("request.body and request.body_expr are mutually exclusive")
	}
	if req.GetBody() != nil {
		if err := validateBodyTemplates(req.GetBody().AsInterface()); err != nil {
			return fmt.Errorf("request.body: %w", err)
		}
	}
	for i, rule := range req.GetErrors() {
		if rule.GetStatus() == 0 && rule.GetStatusMin() == 0 {
			return fmt.Errorf("request.errors[%d] needs status or status_min/status_max", i)
		}
		if err := tmpl.Validate(rule.GetMessage()); err != nil {
			return fmt.Errorf("request.errors[%d].message: %w", i, err)
		}
		if when := rule.GetWhen(); when != "" {
			if err := tmpl.ValidateExpr(when); err != nil {
				return fmt.Errorf("request.errors[%d].when: %w", i, err)
			}
		}
	}
	if p := req.GetPagination(); p != nil {
		if err := validatePagination(p); err != nil {
			return err
		}
	}
	return nil
}

func validateBodyTemplates(v any) error {
	switch t := v.(type) {
	case string:
		return tmpl.Validate(t)
	case []any:
		for _, e := range t {
			if err := validateBodyTemplates(e); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, e := range t {
			if err := validateBodyTemplates(e); err != nil {
				return err
			}
		}
	}
	return nil
}

func validatePagination(p *reliantv1.PaginationSpec) error {
	if p.GetMaxPages() < 0 || p.GetMaxPages() > 100 {
		return fmt.Errorf("pagination.max_pages must be 0..100")
	}
	switch p.GetStyle() {
	case "link_header":
	case "cursor":
		if p.GetCursorParam() == "" || p.GetNextCursor() == "" {
			return fmt.Errorf("cursor pagination needs cursor_param and next_cursor")
		}
		if err := tmpl.ValidateExpr(p.GetNextCursor()); err != nil {
			return fmt.Errorf("pagination.next_cursor: %w", err)
		}
	case "page":
		if p.GetPageParam() == "" {
			return fmt.Errorf("page pagination needs page_param")
		}
	default:
		return fmt.Errorf("pagination.style %q must be link_header, cursor or page", p.GetStyle())
	}
	return nil
}

func validateOutput(o *reliantv1.OutputSpec, p *reliantv1.PaginationSpec) error {
	sel := o.GetSelect()
	if sel != "" && sel != "$" && sel != "$raw" {
		if err := tmpl.ValidateExpr(sel); err != nil {
			return fmt.Errorf("output.select: %w", err)
		}
	}
	if p != nil && (sel == "$" || sel == "$raw") && sel != "" {
		return fmt.Errorf("a paginated action needs an output.select expression that yields a list")
	}
	return nil
}

// checkHost enforces the catalog-level SSRF rule: a server/any action may only
// target the manifest's base_url host or an allowed_hosts entry. The runtime
// re-checks the rendered URL and refuses private addresses after DNS.
func checkHost(req *reliantv1.HttpRequestSpec, conn *reliantv1.ConnectionSpec, allowed map[string]bool) error {
	if conn.GetAllowAnyPublicHost() {
		if req.GetUrl() == "" {
			return fmt.Errorf("allow_any_public_host actions take a url, not a path")
		}
		return nil
	}
	if req.GetPath() != "" {
		if conn.GetBaseUrl() == "" {
			return fmt.Errorf("request.path needs connection.base_url")
		}
		if err := checkPath(req.GetPath()); err != nil {
			return fmt.Errorf("request.path: %w", err)
		}
		return nil
	}
	raw := req.GetUrl()
	prefixEnd := strings.Index(raw, "://")
	if prefixEnd < 0 {
		return fmt.Errorf("request.url %q must be an absolute https URL", raw)
	}
	rest := raw[prefixEnd+3:]
	hostEnd := strings.IndexAny(rest, "/?#")
	if hostEnd < 0 {
		hostEnd = len(rest)
	}
	authority := rest[:hostEnd]
	if tmpl.HasExpr(authority) || tmpl.HasExpr(raw[:prefixEnd]) {
		return fmt.Errorf("request.url host must be a literal in base_url or allowed_hosts, not templated")
	}
	u, err := parseHTTPS(raw)
	if err != nil {
		return fmt.Errorf("request.url: %w", err)
	}
	if !allowed[strings.ToLower(u.Host)] {
		return fmt.Errorf("request.url host %q is outside base_url and allowed_hosts", u.Host)
	}
	return nil
}
