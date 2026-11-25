package servicecatalog

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestForBundleKeepsWhatReadersUse(t *testing.T) {
	method := CodeMethod{ID: MethodID("example.com/shop/internal/orders", "Service", "Get"), Name: "Get", Signature: "func() error", Scope: "example.com/shop/internal/orders", LegacyName: "orders.Get", Source: Source{Path: "internal/orders/contract.go", Symbol: "Service.Get"}}
	c := Catalog{Schema: Schema, Module: "example.com/shop",
		Services: []Service{{Name: "shop.v1.OrdersService", ProtoFile: "shop/v1/orders.proto",
			Methods:    []Method{{ID: "shop.v1.OrdersService/GetOrder", Name: "GetOrder", Idempotency: "IDEMPOTENCY_UNKNOWN"}, {ID: "shop.v1.OrdersService/ListOrders", Name: "ListOrders", Idempotency: "NO_SIDE_EFFECTS"}},
			Descriptor: json.RawMessage(`{"Name":"OrdersService","Methods":[{"Name":"GetOrder"}],"Messages":{"X":[]},"SchemaFiles":{"shop.v1.GetOrderRequest":"shop/v1/orders.proto"},"Schemas":{"shop.v1.GetOrderRequest":[{"name":"id","kind":"string"}]}}`)}},
		Components: []Component{{ID: "Orders", Name: "Orders", Kind: "component", Source: Source{Path: "internal/orders/service.go", Symbol: "New"},
			Methods: []CodeMethod{method}, ImplementationCandidates: []Implementation{{Type: "*example.com/shop/internal/orders.impl", Methods: []CodeMethod{method}}}}},
		Bindings:    []Binding{{Consumer: "API", Field: "Orders", Interface: "example.com/shop/internal/orders.Service", Methods: []CodeMethod{method}, Candidates: []string{"Orders"}, Providers: []string{"Orders"}, Resolution: "composition_expression"}},
		Diagnostics: []Diagnostic{{Code: "coverage_generated_routes_and_registered_clients_only"}, {Code: "type_unresolved", Component: "X"}},
	}
	ForBundle(&c)
	if got := string(c.Services[0].Descriptor); got != `{"Schemas":{"shop.v1.GetOrderRequest":[{"name":"id","kind":"string"}]}}` {
		t.Fatalf("descriptor: %s", got)
	}
	if c.Services[0].Methods[0].Idempotency != "UNKNOWN" || c.Services[0].Methods[1].Idempotency != "NO_SIDE_EFFECTS" {
		t.Fatalf("idempotency: %+v", c.Services[0].Methods)
	}
	if len(c.Components[0].Methods) != 0 || len(c.Components[0].ImplementationCandidates[0].Methods) != 0 || len(c.Bindings[0].Methods) != 0 {
		t.Fatal("method sets kept")
	}
	if c.Components[0].Source.Symbol != "New" || c.Bindings[0].Providers[0] != "Orders" || len(c.Components[0].ImplementationCandidates) != 1 {
		t.Fatal("topology lost")
	}
	codes := []string{}
	for _, d := range c.Diagnostics {
		codes = append(codes, d.Code)
	}
	if got := strings.Join(codes, ","); got != "bundle_method_sets_omitted,coverage_generated_routes_and_registered_clients_only,type_unresolved" {
		t.Fatalf("diagnostics: %s", got)
	}
	again := c
	ForBundle(&again)
	a, _ := json.Marshal(c)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Fatal("ForBundle is not idempotent")
	}
	// Empty method lists marshal as [] (readers require arrays), and the
	// copy still decodes.
	if !strings.Contains(string(a), `"methods":[]`) {
		t.Fatalf("method lists: %s", a)
	}
	if _, err := Decode(a); err != nil {
		t.Fatal(err)
	}
}

func TestForBundleSaysNothingWhenNothingIsOmitted(t *testing.T) {
	c := Catalog{Schema: Schema, Module: "example.com/shop", Components: []Component{{ID: "A", Methods: []CodeMethod{}}}}
	ForBundle(&c)
	if len(c.Diagnostics) != 0 {
		t.Fatalf("diagnostics: %+v", c.Diagnostics)
	}
}

// A catalog grows with the code graph; the bundle copy grows with the RPC
// surface. Shaped like reliant's (2.1 MB compact): 36 services, 120
// components and 114 bindings over ~3,000 code methods.
func TestForBundleIsBoundedByTheRPCSurface(t *testing.T) {
	c := Catalog{Schema: Schema, Module: "github.com/acme/big"}
	methods := func(owner string, n int) []CodeMethod {
		out := make([]CodeMethod, n)
		for i := range out {
			name := fmt.Sprintf("Method%d", i)
			scope := "github.com/acme/big/internal/" + strings.ToLower(owner)
			out[i] = CodeMethod{ID: MethodID(scope, owner, name), Name: name, Scope: scope, LegacyName: strings.ToLower(owner) + "." + name,
				Signature: "func(ctx context.Context, id string, opts *github.com/acme/big/internal/db.ListOptions) (*github.com/acme/big/internal/db.Row, error)",
				Source:    Source{Path: "internal/" + strings.ToLower(owner) + "/service.go", Symbol: owner + "." + name}}
		}
		return out
	}
	for i := 0; i < 120; i++ {
		owner := fmt.Sprintf("Component%d", i)
		c.Components = append(c.Components, Component{ID: owner, Name: owner, Kind: "component", Type: "*github.com/acme/big/internal/app." + owner, Methods: methods(owner, 25)})
	}
	for i := 0; i < 114; i++ {
		owner := fmt.Sprintf("Port%d", i)
		c.Bindings = append(c.Bindings, Binding{Consumer: fmt.Sprintf("Component%d", i), Field: owner, Interface: "github.com/acme/big/internal/app." + owner, Methods: methods(owner, 8), Candidates: []string{}, Providers: []string{}, Resolution: "unresolved"})
	}
	for i := 0; i < 36; i++ {
		var rpcs []Method
		schemas := map[string][]map[string]string{}
		for j := 0; j < 9; j++ {
			in := fmt.Sprintf("acme.v1.Rpc%dRequest", j)
			rpcs = append(rpcs, Method{ID: fmt.Sprintf("acme.v1.Service%d/Rpc%d", i, j), Name: fmt.Sprintf("Rpc%d", j), Input: in, Idempotency: "IDEMPOTENCY_UNKNOWN"})
			schemas[in] = []map[string]string{{"name": "chat_id", "kind": "string"}, {"name": "workflow_id", "kind": "string"}}
		}
		generatorView := strings.Repeat(`{"Name":"Rpc","InputType":"RpcRequest","OutputType":"RpcResponse","InputProtoFile":"acme/v1/service.proto","Activity":{"Mutating":true}},`, 150)
		desc, _ := json.Marshal(map[string]any{"Schemas": schemas, "Methods": json.RawMessage("[" + strings.TrimSuffix(generatorView, ",") + "]")})
		c.Services = append(c.Services, Service{Name: fmt.Sprintf("acme.v1.Service%d", i), ProtoFile: "acme/v1/service.proto", Methods: rpcs, Descriptor: desc})
	}
	full, _ := json.Marshal(c)
	ForBundle(&c)
	reduced, _ := json.Marshal(c)
	if len(full) < 2<<20 {
		t.Fatalf("fixture is %d bytes, not reliant-sized", len(full))
	}
	if len(reduced) > 256<<10 {
		t.Fatalf("bundle copy is %d bytes of %d: the code graph is still in it", len(reduced), len(full))
	}
}
