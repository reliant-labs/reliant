// Package servicecatalog defines Forge's browse-only, declared RPC/code inventory.
// It contains no endpoint values, credentials, invocation handles or runtime health.
package servicecatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const (
	Schema        = "forge.dev/service-catalog/v1"
	GeneratedPath = "gen/forge_service_catalog.json"
	BundlePath    = "forge/service-catalog.json"
)

// Catalog is generated from the same typed composition model and wire descriptor
// as the application. Missing evidence is explicit; none of these edges proves a call.
type Catalog struct {
	Schema      string       `json:"schema"`
	Module      string       `json:"module"`
	Processes   []Process    `json:"processes"`
	Services    []Service    `json:"services"`
	Components  []Component  `json:"components"`
	Bindings    []Binding    `json:"bindings"`
	Clients     []Client     `json:"clients"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// Source locates a declaration, in one of two forms.
//
// The committed gen/ catalog names it: Path plus Symbol, and no line. A line
// number moves whenever unrelated code above it does, so a committed catalog
// that recorded one went stale on every merge that touched a handler file.
//
// The bundle copy carries Path plus Line: Locate resolves each Symbol against
// the source being built, and the bundle's readers have always required a
// line with a path. The zero Source means the location is unknown.
type Source struct {
	Path string `json:"path"` // module-relative, never absolute
	// Symbol is the declaration's name in Path: "NewOrders" (a func), "Orders"
	// (a type), or "Orders.Get" (a method, or a method of an interface type).
	// Only the committed form carries it.
	Symbol string `json:"symbol,omitempty"`
	Line   int    `json:"line"`
}

// MarshalJSON writes the committed form ({path, symbol}) when s is symbolic,
// and the located form ({path, line}) otherwise.
func (s Source) MarshalJSON() ([]byte, error) {
	if s.Symbol != "" {
		return json.Marshal(struct {
			Path   string `json:"path"`
			Symbol string `json:"symbol"`
		}{s.Path, s.Symbol})
	}
	return json.Marshal(struct {
		Path string `json:"path"`
		Line int    `json:"line"`
	}{s.Path, s.Line})
}

type Process struct {
	ID         string   `json:"id"`
	Root       string   `json:"root"`
	Command    []string `json:"command"`
	Components []string `json:"components"`
	Serves     []Serve  `json:"serves"`
	Clients    []string `json:"clients"` // registrations reachable by this root: can-call only
	Source     Source   `json:"source"`
}

type Serve struct {
	Component  string `json:"component"` // handler component; method names join to Component.Methods
	Service    string `json:"service"`   // fully-qualified protobuf service
	RouteSet   string `json:"route_set"`
	Audience   string `json:"audience"`
	Resolution string `json:"resolution"` // declared_route_set; not runtime observation
}

type Service struct {
	Name      string   `json:"name"`
	ProtoFile string   `json:"proto_file"`
	Methods   []Method `json:"methods"`
	// Descriptor is the existing forge descriptor (gen/forge_descriptor/) ServiceDef, unchanged:
	// Schemas, SchemaFiles, Enums and field validate/validate_options are the
	// authoritative schema graph. Do not invent a second validation dialect.
	Descriptor json.RawMessage `json:"descriptor"`
}

type Method struct {
	ID              string `json:"id"` // full proto service + "/" + method
	Name            string `json:"name"`
	Input           string `json:"input"`
	Output          string `json:"output"`
	ClientStreaming bool   `json:"client_streaming"`
	ServerStreaming bool   `json:"server_streaming"`
	Idempotency     string `json:"idempotency"` // UNKNOWN, IDEMPOTENT, NO_SIDE_EFFECTS
	AuthRequired    bool   `json:"auth_required"`
}

type Component struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Kind    string       `json:"kind"`
	Type    string       `json:"type"`
	Source  Source       `json:"source"`
	Methods []CodeMethod `json:"methods"`
	// ImplementationCandidates are assignable concrete types in the component's
	// package. They are not proof of which implementation a constructor returns.
	ImplementationCandidates []Implementation `json:"implementation_candidates"`
}

type Implementation struct {
	Type    string       `json:"type"`
	Source  Source       `json:"source"`
	Methods []CodeMethod `json:"methods"`
}

type CodeMethod struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Signature  string `json:"signature"`   // Go types qualified by import path
	Scope      string `json:"scope"`       // legacy instrumentation scope
	LegacyName string `json:"legacy_name"` // legacy pkg.Method span name
	Source     Source `json:"source"`
}

type Binding struct {
	Consumer   string       `json:"consumer"`
	Field      string       `json:"field"`
	Interface  string       `json:"interface"`
	Methods    []CodeMethod `json:"methods"`
	Candidates []string     `json:"candidates"` // assignable producers, NOT selected bindings
	Providers  []string     `json:"providers"`  // accessors in this field's composition expression
	Resolution string       `json:"resolution"` // composition_expression or unresolved
}

type Client struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	Service     string `json:"service"`
	ConfigField string `json:"config_field"` // existing fully-qualified proto config field, NOT its value
	Constructor string `json:"constructor"`
	Source      Source `json:"source"`
	Resolution  string `json:"resolution"` // declared; no target URL inference
}

type Diagnostic struct {
	Code      string `json:"code"`
	Component string `json:"component,omitempty"`
	Source    Source `json:"source"`
}

// MethodID is a stable low-cardinality symbol identity. Signature, source line,
// build, instance and arguments deliberately do not enter the identity.
func MethodID(scope, owner, method string) string {
	h := sha256.Sum256([]byte("forge.method.v1\x00" + scope + "\x00" + owner + "\x00" + method))
	return "method1:" + hex.EncodeToString(h[:])
}
