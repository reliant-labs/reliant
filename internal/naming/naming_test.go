package naming

import "testing"

func TestToPascalCase(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"api-gateway", "APIGateway"},
		{"db-service", "DBService"},
		{"http_client", "HTTPClient"},
		{"database_url", "DatabaseURL"},
		{"simple", "Simple"},
		{"", ""},
		{"a", "A"},
		{"id", "ID"},
		{"user_id", "UserID"},
		{"uuid", "UUID"},
		{"mcp-server", "MCPServer"},
		{"sql_db", "SQLDB"},
		// LLM-port additions: these acronyms surfaced as scaffolder bugs
		// where service.go referenced "LlmGatewayService" but proto-emitted
		// Go correctly produced "LLMGatewayService".
		{"llm-gateway", "LLMGateway"},
		{"jwt-auth", "JWTAuth"},
		{"io-bound", "IOBound"},
		// An initialism is replaced only as a whole word. Each of these
		// used to be mangled by a prefix match ("identity" -> "IDentity").
		{"identity", "Identity"},
		{"slack_channel", "SlackChannel"},
		{"uint_value", "UintValue"},
		{"ramp", "Ramp"},
		{"idle", "Idle"},
		{"ion", "Ion"},
		{"ipsum", "Ipsum"},
		{"api_key", "APIKey"},
		{"http_url", "HTTPURL"},
		{"https_proxy", "HTTPSProxy"},
		{"uid_map", "UIDMap"},
		{"vm2", "VM2"},
		{"user_id_2", "UserID2"},
		// Plurals are not initialisms: "Ids" matches no word, as in
		// staticcheck ST1003 and protoc-gen-go.
		{"ids", "Ids"},
		{"user_ids", "UserIds"},
		{"urls", "Urls"},
		{"apis", "Apis"},
		// Already-PascalCase input is word-bounded by its capitals.
		{"IdentityService", "IdentityService"},
		{"ApiKeyService", "APIKeyService"},
		{"UserIdService", "UserIDService"},
	}
	for _, tt := range tests {
		got := ToPascalCase(tt.input)
		if got != tt.want {
			t.Errorf("ToPascalCase(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestToExportedFieldName(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"api", "API"},
		{"db", "DB"},
		{"mcp", "MCP"},
		{"orders", "Orders"},
		{"notifications", "Notifications"},
		{"", ""},
		{"id", "ID"},
		{"uuid", "UUID"},
	}
	for _, tt := range tests {
		got := ToExportedFieldName(tt.input)
		if got != tt.want {
			t.Errorf("ToExportedFieldName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestToSnakeCase(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"firstName", "first_name"},
		{"LastName", "last_name"},
		{"already_snake", "already_snake"},
		{"HTTPStatus", "http_status"},
		{"userID", "user_id"},
		{"UPPER_CASE", "upper_case"},
		{"simple", "simple"},
		{"", ""},
		{"a", "a"},
		{"A", "a"},
		{"createdAt", "created_at"},
		{"XMLParser", "xml_parser"},
		// Multi-byte runes must not confuse prev/next lookups (byte vs rune index bug).
		{"café", "café"},
		{"CaféName", "café_name"},
		{"αBeta", "α_beta"},
	}
	for _, tt := range tests {
		got := ToSnakeCase(tt.input)
		if got != tt.want {
			t.Errorf("ToSnakeCase(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestToKebabCase(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		// Plain PascalCase
		{"UserService", "user-service"},
		{"TaskItem", "task-item"},
		{"Tasks", "tasks"},
		{"A", "a"},
		{"", ""},
		// camelCase input
		{"taskItem", "task-item"},
		// snake_case input → kebab
		{"user_service", "user-service"},
		// already-kebab input is preserved
		{"user-service", "user-service"},
		// LLM/JWT/HTTP/URL/JSON acronyms must stay glued
		{"LLMGateway", "llm-gateway"},
		{"LLMGatewayService", "llm-gateway-service"},
		{"JWTAuth", "jwt-auth"},
		{"HTTPClient", "http-client"},
		{"URLBuilder", "url-builder"},
		{"JSONParser", "json-parser"},
		{"XMLParser", "xml-parser"},
		// Trailing acronym
		{"UserID", "user-id"},
		{"TaskUUID", "task-uuid"},
		// Lone acronym
		{"LLM", "llm"},
		{"API", "api"},
		// HTTPS prefix-vs-HTTP — longer wins
		{"HTTPSConnection", "https-connection"},
		// Doubled separators normalise
		{"foo--bar", "foo-bar"},
		{"foo__bar", "foo-bar"},
	}
	for _, tt := range tests {
		got := ToKebabCase(tt.input)
		if got != tt.want {
			t.Errorf("ToKebabCase(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestServicePackage covers the kebab + snake -> snake-lowercase rule
// shared by every site that derives a Go-package identifier from a
// CLI/forge.yaml service/binary/worker/frontend name. Hyphens convert
// to underscores; PascalCase boundaries split on underscores; existing
// snake_case passes through. This matches protoc-gen-go's convention
// for multi-word proto packages (proto package
// `services.admin_server.v1` -> directory `services/admin_server/v1`)
// and the universal on-disk dir convention forge projects use.
//
// History: pre-2026-06-08 this function emitted compact form
// (separators stripped). The compact convention silently collided with
// every project's snake-case handler dirs — see the docstring on
// ServicePackage for the full repro.
func TestServicePackage(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain lowercase passes through", "api", "api"},
		{"single hyphen becomes underscore", "admin-server", "admin_server"},
		{"snake_case passes through", "calibrator_refit", "calibrator_refit"},
		{"mixed hyphen and underscore normalize to snake", "calibrator_refit-worker", "calibrator_refit_worker"},
		{"PascalCase splits on word boundaries", "AdminServer", "admin_server"},
		{"repeated separators collapse to single", "a--b__c", "a_b_c"},
		{"empty stays empty", "", ""},
		// PascalCase + "Service" suffix branch (proto service names).
		{"proto Service suffix trimmed", "EchoService", "echo"},
		{"proto multi-word Service suffix produces snake", "AdminServerService", "admin_server"},
		{"proto Go initialisms produce snake", "LLMGatewayService", "llm_gateway"},
		{"proto initialism only", "APIService", "api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ServicePackage(tt.in)
			if got != tt.want {
				t.Errorf("ServicePackage(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestGoPackage pins down the GoPackage helper (no Service-suffix
// trimming). Like ServicePackage, snake_case is the canonical output
// form: hyphens convert to underscores, PascalCase splits on word
// boundaries, snake_case passes through.
func TestGoPackage(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain lowercase passes through", "api", "api"},
		{"hyphen to underscore", "admin-server", "admin_server"},
		{"snake_case preserved", "calibrator_refit", "calibrator_refit"},
		{"PascalCase to snake", "AdminServer", "admin_server"},
		{"initialism handling", "HTTPClient", "http_client"},
		{"empty stays empty", "", ""},
		// Service suffix is NOT trimmed (that's what distinguishes GoPackage
		// from ServicePackage).
		{"Service suffix preserved", "EchoService", "echo_service"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GoPackage(tt.in)
			if got != tt.want {
				t.Errorf("GoPackage(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestApplyGoInitialisms_LongerFirst(t *testing.T) {
	// "Uuid" should become "UUID", not "UuID" (which would happen if "Id" was processed first)
	got := applyGoInitialisms("Uuid")
	if got != "UUID" {
		t.Errorf("applyGoInitialisms(%q) = %q, want %q", "Uuid", got, "UUID")
	}

	// "Xsrf" should become "XSRF", not get partially mangled
	got = applyGoInitialisms("Xsrf")
	if got != "XSRF" {
		t.Errorf("applyGoInitialisms(%q) = %q, want %q", "Xsrf", got, "XSRF")
	}
}

func TestProtoPackageVersion(t *testing.T) {
	tests := []struct{ in, want string }{
		{"billing.v1", "v1"},
		{"acme.billing.v1", "v1"},
		{"shop.v2beta1", "v2beta1"},
		{"users.v1alpha1", "v1alpha1"},
		{"billing", ""},
		{"", ""},
		{"v1", "v1"},
		{"acme.beta", ""},    // "beta" is not a version segment
		{"acme.v", ""},       // bare "v" is not a version
		{"acme.v1alpha", ""}, // channel without a number is malformed
		{"acme.version", ""}, // "version" must not match
	}
	for _, tt := range tests {
		if got := ProtoPackageVersion(tt.in); got != tt.want {
			t.Errorf("ProtoPackageVersion(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestProtoPackageBase(t *testing.T) {
	tests := []struct{ in, want string }{
		{"billing.v1", "billing"},
		{"acme.billing.v1", "acme.billing"},
		{"billing", "billing"},
		{"shop.v2beta1", "shop"},
		{"acme.version", "acme.version"}, // not a version, unchanged
		{"", ""},
	}
	for _, tt := range tests {
		if got := ProtoPackageBase(tt.in); got != tt.want {
			t.Errorf("ProtoPackageBase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHumanize(t *testing.T) {
	for in, want := range map[string]string{
		"RetainedDatabases": "Retained Databases",
		"Task":              "Task",
		"HTTPServer":        "HTTP Server",
		"module_config":     "module config",
	} {
		if got := Humanize(in); got != want {
			t.Errorf("Humanize(%q) = %q, want %q", in, got, want)
		}
	}
	if got := HumanizeLower("RetainedDatabases"); got != "retained databases" {
		t.Errorf("HumanizeLower = %q", got)
	}
}

func TestTripsReviveVarNaming(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"CreateApiKey", true},
		{"CreateAPIKey", false},
		{"ListOrders", false},
		{"GetHTTPStatus", false},
		{"GetHttpStatus", true},
	}
	for _, c := range cases {
		if got := TripsReviveVarNaming(c.in); got != c.want {
			t.Errorf("TripsReviveVarNaming(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
