package codegen

import (
	"strings"
	"testing"
)

// A repeated field name inside ONE KCL schema suite is not a KCL error. KCL
// accepts it and the LAST declaration wins, so the first field's default is
// discarded with no diagnostic:
//
//	schema T:
//	    app_url: str = "http://localhost:3000"
//	    app_url: str = ""
//	t = T {}          # -> app_url: ''
//
// That shipped: two config protos each declared `app_url`, both were
// flattened into AppConfig, the frontend proto's "" won, and three prod
// workloads rendered `APP_URL: ''`. The consumer gated a browser auth
// redirect on the value being non-empty, so the collision quietly turned
// that redirect into a bare 401 — invisible in review, because config_gen.k
// is generated and nobody reads it.
//
// So the generator must refuse rather than emit. These tests pin the
// refusal, and — just as importantly — pin that it does NOT fire on the
// per-binary case, where two schemas legitimately share a field name.

// shadowingField is a plain string config leaf, the shape the real collision
// had.
func shadowingField(name, protoFile, def string) ConfigField {
	return ConfigField{
		Name:         name,
		GoName:       name,
		ProtoType:    "string",
		GoType:       "string",
		EnvVar:       strings.ToUpper(name),
		DefaultValue: def,
		ProtoFile:    protoFile,
	}
}

func TestRenderConfigSchema_RefusesDuplicateFieldName(t *testing.T) {
	tests := []struct {
		name       string
		fields     []ConfigField
		schemaName string
		// wantIn are substrings the message must carry to be actionable.
		wantIn []string
	}{
		{
			name:       "two protos collide — the real app_url case",
			schemaName: "AppConfig",
			fields: []ConfigField{
				shadowingField("app_url", "proto/config/v1/config.proto", "http://localhost:3000"),
				shadowingField("log_level", "proto/config/v1/config.proto", "info"),
				shadowingField("app_url", "proto/config/v1/settings_web.proto", ""),
			},
			wantIn: []string{
				"config_gen.k:",
				`field "app_url" is declared twice`,
				"schema AppConfig",
				"proto/config/v1/config.proto",
				"proto/config/v1/settings_web.proto",
			},
		},
		{
			name:       "same proto declares the name twice",
			schemaName: "AppConfig",
			fields: []ConfigField{
				shadowingField("port", "proto/config/v1/config.proto", "8080"),
				shadowingField("port", "proto/config/v1/config.proto", "9090"),
			},
			wantIn: []string{
				`field "port" is declared twice`,
				"both declarations are in proto/config/v1/config.proto",
			},
		},
		{
			name:       "no provenance — older descriptor still names the field",
			schemaName: "AdminConfig",
			fields: []ConfigField{
				shadowingField("api_key", "", "a"),
				shadowingField("api_key", "", "b"),
			},
			wantIn: []string{
				`field "api_key" is declared twice`,
				"schema AdminConfig",
			},
		},
		{
			name:       "a sensitive field shadowing a plain one is still a collision",
			schemaName: "AppConfig",
			fields: []ConfigField{
				shadowingField("database_url", "proto/config/v1/config.proto", "postgres://localhost/dev"),
				{
					Name:      "database_url",
					ProtoType: "string",
					GoType:    "string",
					EnvVar:    "DATABASE_URL",
					Sensitive: true,
					ProtoFile: "proto/config/v1/secrets.proto",
				},
			},
			wantIn: []string{
				`field "database_url" is declared twice`,
				"proto/config/v1/secrets.proto",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := renderConfigSchemaNamed(tc.fields, "myproj", tc.schemaName, true)
			if err == nil {
				t.Fatalf("a duplicate field name must be refused, but the render succeeded:\n%s", got)
			}
			if got != "" {
				t.Errorf("a refused render must emit nothing, got:\n%s", got)
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error must name %q to be actionable, got:\n%s", want, err)
				}
			}
		})
	}
}

// The whole point of per-binary configs is that each binary gets its OWN
// schema suite. `port` on AdminConfig and `port` on GatewayConfig are two
// declarations in two suites — neither shadows the other, and refusing that
// would break the feature this check has to coexist with.
func TestPerBinaryConfig_SameFieldNameInDifferentSchemasIsFine(t *testing.T) {
	admin := []ConfigField{
		shadowingField("port", "proto/config/v1/admin.proto", "8080"),
		shadowingField("app_url", "proto/config/v1/admin.proto", "http://localhost:3000"),
	}
	gateway := []ConfigField{
		shadowingField("port", "proto/config/v1/gateway.proto", "9090"),
		shadowingField("app_url", "proto/config/v1/gateway.proto", "http://localhost:4000"),
	}

	got, err := GenerateConfigKCLPerBinary(nil, []BinaryConfigFields{
		{Binary: "admin", MessageName: "AdminConfig", Fields: admin},
		{Binary: "gateway", MessageName: "GatewayConfig", Fields: gateway},
	}, "myproj")
	if err != nil {
		t.Fatalf("the same field name in two different schemas must render: %v", err)
	}

	for _, want := range []string{
		"schema AdminConfig:",
		"schema GatewayConfig:",
		`port: str = "8080"`,
		`port: str = "9090"`,
		`app_url: str = "http://localhost:3000"`,
		`app_url: str = "http://localhost:4000"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("per-binary module missing %q\n%s", want, got)
		}
	}
}

// Two block-REFERENCE fields of the same name project to comments, not
// schema fields, so they cannot shadow. But a block reference colliding with
// a real scalar means two different proto fields are fighting over one name,
// and exactly one of them ends up declared — refuse that.
func TestRenderConfigSchema_BlockReferenceCollidingWithScalarIsRefused(t *testing.T) {
	fields := []ConfigField{
		shadowingField("trader", "proto/config/v1/config.proto", "x"),
		{
			Name:        "trader",
			ProtoType:   "message",
			MessageType: "TraderConfig",
			ProtoFile:   "proto/config/v1/trader.proto",
		},
	}
	if _, err := renderConfigSchemaNamed(fields, "myproj", "AppConfig", true); err == nil {
		t.Fatal("a block reference colliding with a scalar field must be refused")
	}
}

// The refusal must not disturb the shape of a clean render. This is the
// byte-for-byte guard: the representative field set (every type and default
// branch) renders exactly what it rendered before the check existed.
func TestRenderConfigSchema_HappyPathUnchangedByTheDuplicateCheck(t *testing.T) {
	fields := representativeConfigFields()

	got, err := renderConfigSchemaNamed(fields, "myproj", "AppConfig", true)
	if err != nil {
		t.Fatalf("the representative config field set must render: %v", err)
	}

	want := `schema ConfigSecretRef:
    name: str
    key: str

# forge:section schema AppConfig
schema AppConfig:
    # Log level (debug, info, warn, error)
    log_level: str = "info"
    # HTTP server port
    port: int = 8080
    cors_allow_credentials: bool = False
    # PostgreSQL connection string
    database_url: ConfigSecretRef = ConfigSecretRef { name = "myproj-secrets", key = "database_url" }
    # Max drain time during graceful shutdown
    shutdown_timeout: str = "30s"
    sample_rate: float = 0.0
` + representativeEmptyEnvChecks
	if got != want {
		t.Errorf("clean render changed\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// CheckDuplicateConfigFields is exported so the generate pipeline can gate
// on it directly. Empty and single-field sets are the boundary cases a gate
// gets called with constantly.
func TestCheckDuplicateConfigFields_Boundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields []ConfigField
	}{
		{"no fields", nil},
		{"one field", []ConfigField{shadowingField("port", "a.proto", "8080")}},
		{"distinct names", []ConfigField{
			shadowingField("port", "a.proto", "8080"),
			shadowingField("app_url", "b.proto", "u"),
			shadowingField("log_level", "b.proto", "info"),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckDuplicateConfigFields(tc.fields, ConfigSchemaModule, "AppConfig"); err != nil {
				t.Errorf("must not refuse: %v", err)
			}
		})
	}
}

// A frontend config schema is exposed to the same silent last-wins shadowing,
// and it lands in a DIFFERENT module — so the refusal must point at
// frontend_config_gen.k, not at config_gen.k. Naming the wrong file sends the
// author to a file that never carried the field.
func TestFrontendConfigSchema_RefusesDuplicateAndNamesItsOwnModule(t *testing.T) {
	fc := FrontendConfig{
		Frontend:    "web",
		MessageName: "WebConfig",
		Fields: []ConfigField{
			shadowingField("api_url", "proto/config/v1/web.proto", "http://localhost:8080"),
			shadowingField("api_url", "proto/config/v1/shared.proto", ""),
		},
	}

	_, err := GenerateFrontendConfigKCL([]FrontendConfig{fc}, "myproj")
	if err == nil {
		t.Fatal("a duplicate field in a frontend config schema must be refused")
	}
	for _, want := range []string{
		"frontend_config_gen.k:",
		`field "api_url" is declared twice`,
		"schema WebConfig",
		"proto/config/v1/web.proto",
		"proto/config/v1/shared.proto",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q, got:\n%s", want, err)
		}
	}
	// " config_gen.k:" with the leading space is the backend module's own
	// spelling; "frontend_config_gen.k:" merely ends with the same letters.
	if strings.Contains(err.Error(), " config_gen.k:") {
		t.Errorf("a frontend collision must not blame config_gen.k, got:\n%s", err)
	}
}
