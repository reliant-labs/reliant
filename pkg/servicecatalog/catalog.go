// Package servicecatalog defines Forge's browse-only, declared RPC/code inventory.
// It contains no endpoint values, credentials, invocation handles or runtime health.
package servicecatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Schema is the catalog document's schema id. GeneratedPath is where forge
// generate writes the committed catalog, relative to the module root, and
// BundlePath is the copy's path inside a release bundle.
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

// Process is one composition root of the module, with the command that runs
// it: the components it reaches, the services its route sets serve, and the
// clients it can call. One executable's `server api` and `server worker` are
// two processes.
type Process struct {
	ID         string   `json:"id"`
	Root       string   `json:"root"`
	Command    []string `json:"command"`
	Components []string `json:"components"`
	Serves     []Serve  `json:"serves"`
	Clients    []string `json:"clients"` // registrations reachable by this root: can-call only
	Source     Source   `json:"source"`
}

// Serve is a protobuf service a process's route set declares it serves. It is
// a declared capability, not proof a live listener mounted it.
type Serve struct {
	Component  string `json:"component"` // handler component; method names join to Component.Methods
	Service    string `json:"service"`   // fully-qualified protobuf service
	RouteSet   string `json:"route_set"`
	Audience   string `json:"audience"`
	Resolution string `json:"resolution"` // declared_route_set; not runtime observation
}

// Service is one protobuf service and its methods.
type Service struct {
	Name      string   `json:"name"`
	ProtoFile string   `json:"proto_file"`
	Methods   []Method `json:"methods"`
	// Descriptor is the existing forge descriptor (gen/forge_descriptor/) ServiceDef, unchanged:
	// Schemas, SchemaFiles, Enums and field validate/validate_options are the
	// authoritative schema graph. Do not invent a second validation dialect.
	Descriptor json.RawMessage `json:"descriptor"`
}

// Method is one rpc of a [Service], as its proto declares it.
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

// Component is one node of the typed composition graph: a component, a
// provider, a route set or the operators.
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

// Implementation is a concrete type in a component's package that satisfies
// the component's interface type: a candidate, not proof a constructor returns
// it.
type Implementation struct {
	Type    string       `json:"type"`
	Source  Source       `json:"source"`
	Methods []CodeMethod `json:"methods"`
}

// CodeMethod is one Go method of a component, implementation or binding. The
// catalog records none today (the bundle_method_sets_omitted diagnostic), so
// every methods list is empty.
type CodeMethod struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Signature  string `json:"signature"`   // Go types qualified by import path
	Scope      string `json:"scope"`       // legacy instrumentation scope
	LegacyName string `json:"legacy_name"` // legacy pkg.Method span name
	Source     Source `json:"source"`
}

// Binding is one interface-typed Deps field of a component: the interface it
// consumes, the producers assignable to it, and the providers its compose.go
// expression reads.
type Binding struct {
	Consumer   string       `json:"consumer"`
	Field      string       `json:"field"`
	Interface  string       `json:"interface"`
	Methods    []CodeMethod `json:"methods"`
	Candidates []string     `json:"candidates"` // assignable producers, NOT selected bindings
	Providers  []string     `json:"providers"`  // accessors in this field's composition expression
	Resolution string       `json:"resolution"` // composition_expression or unresolved
}

// Client is one typed client registration in a provider: the protobuf service
// it can call and the config field naming its target. Reach means can-call,
// never that any method was called.
type Client struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	Service     string `json:"service"`
	ConfigField string `json:"config_field"` // existing fully-qualified proto config field, NOT its value
	Constructor string `json:"constructor"`
	Source      Source `json:"source"`
	Resolution  string `json:"resolution"` // declared; no target URL inference
}

// Diagnostic is one gap in the catalog's evidence: Code names it (such as
// type_unresolved or binding_unresolved), Component the component it concerns.
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
