package codegen

import (
	"strings"
	"testing"
)

// aliasMessages is reliant's shape: a binary root config composing a
// control-plane block whose url has two aliases, and a `server api` command
// config. Valid as given; each case below breaks one thing.
func aliasMessages() []ConfigMessage {
	return []ConfigMessage{
		{Name: "ReliantConfig", Binary: "reliant", Fields: []ConfigField{
			{Name: "environment", ProtoType: "string", EnvVar: "RELIANT_ENV",
				EnvVarAliases: []EnvVarAlias{{Name: "NODE_ENV"}}, ProtoFile: "proto/config/v1/config.proto"},
			{Name: "control_plane", ProtoType: "message", MessageType: "ControlPlaneConfig"},
		}},
		{Name: "ControlPlaneConfig", Fields: []ConfigField{
			{Name: "url", ProtoType: "string", EnvVar: "RELIANT_CONTROL_PLANE_URL", ProtoFile: "proto/config/v1/control_plane.proto",
				EnvVarAliases: []EnvVarAlias{{Name: "CONTROL_PLANE_API_URL"}, {Name: "CONTROL_PLANE_BASE_URL", Deprecated: true}}},
		}},
		{Name: "ApiServerConfig", Binary: "reliant", Command: []string{"server", "api"}, Fields: []ConfigField{
			{Name: "grpc_port", ProtoType: "int32", EnvVar: "GRPC_PORT", ProtoFile: "proto/config/v1/api_server_config.proto"},
		}},
	}
}

func TestValidateConfigEnvAliases_AcceptsDistinctNames(t *testing.T) {
	if err := ValidateConfigEnvAliases(aliasMessages()); err != nil {
		t.Fatalf("a valid alias set was refused: %v", err)
	}
}

func TestValidateConfigEnvAliases_Refusals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(ms []ConfigMessage) []ConfigMessage
		want   []string
	}{
		{
			name: "alias is another field's env_var in the same process",
			mutate: func(ms []ConfigMessage) []ConfigMessage {
				ms[2].Fields = append(ms[2].Fields, ConfigField{Name: "api_url", ProtoType: "string", EnvVar: "CONTROL_PLANE_API_URL"})
				return ms
			},
			want: []string{"The `reliant server api` process reads CONTROL_PLANE_API_URL as the alias of ReliantConfig.control_plane.url", "env_var of ApiServerConfig.api_url"},
		},
		{
			name: "alias is another field's alias",
			mutate: func(ms []ConfigMessage) []ConfigMessage {
				ms[0].Fields = append(ms[0].Fields, ConfigField{Name: "mode", ProtoType: "string", EnvVar: "RELIANT_MODE",
					EnvVarAliases: []EnvVarAlias{{Name: "NODE_ENV"}}})
				return ms
			},
			want: []string{"The `reliant` process reads NODE_ENV", "alias of ReliantConfig.environment", "alias of ReliantConfig.mode"},
		},
		{
			name: "alias is the field's own env_var",
			mutate: func(ms []ConfigMessage) []ConfigMessage {
				ms[0].Fields[0].EnvVarAliases = append(ms[0].Fields[0].EnvVarAliases, EnvVarAlias{Name: "RELIANT_ENV"})
				return ms
			},
			want: []string{"ReliantConfig.environment (proto/config/v1/config.proto): alias RELIANT_ENV is the field's own env_var"},
		},
		{
			name: "alias listed twice",
			mutate: func(ms []ConfigMessage) []ConfigMessage {
				ms[0].Fields[0].EnvVarAliases = append(ms[0].Fields[0].EnvVarAliases, EnvVarAlias{Name: "NODE_ENV", Deprecated: true})
				return ms
			},
			want: []string{"alias NODE_ENV is listed twice"},
		},
		{
			name: "alias without env_var",
			mutate: func(ms []ConfigMessage) []ConfigMessage {
				ms[0].Fields[0].EnvVar = ""
				return ms
			},
			want: []string{"env_var_aliases without an env_var"},
		},
		{
			name: "alias that is not a variable name",
			mutate: func(ms []ConfigMessage) []ConfigMessage {
				ms[0].Fields[0].EnvVarAliases = []EnvVarAlias{{Name: "NODE ENV"}}
				return ms
			},
			want: []string{`alias "NODE ENV" is not an environment variable name`},
		},
		{
			name: "alias on a frontend config field",
			mutate: func(ms []ConfigMessage) []ConfigMessage {
				return append(ms, ConfigMessage{Name: "WebConfig", Frontend: "web", Fields: []ConfigField{
					{Name: "api_url", ProtoType: "string", EnvVar: "API_URL", EnvVarAliases: []EnvVarAlias{{Name: "VITE_API_URL"}}},
				}})
			},
			want: []string{`WebConfig.api_url: a frontend config field takes no env_var_aliases — frontend "web"`},
		},
		{
			name: "alias collides in the project-global config",
			mutate: func(ms []ConfigMessage) []ConfigMessage {
				return append(ms, ConfigMessage{Name: "AppConfig", Fields: []ConfigField{
					{Name: "port", ProtoType: "int32", EnvVar: "PORT"},
					{Name: "listen_port", ProtoType: "int32", EnvVar: "LISTEN_PORT", EnvVarAliases: []EnvVarAlias{{Name: "PORT", Deprecated: true}}},
				}})
			},
			want: []string{"The project config reads PORT as the env_var of AppConfig.port, and as the alias of AppConfig.listen_port"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConfigEnvAliases(tc.mutate(aliasMessages()))
			if err == nil {
				t.Fatal("want a refusal, got nil")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not contain %q:\n%v", want, err)
				}
			}
		})
	}
}

// TestValidateConfigEnvAliases_ProcessesAreSeparate: two processes may each
// read the same alias — the gateway and the api are separate environments.
// The same alias on a root and a command config of one binary is one
// process, and refused.
func TestValidateConfigEnvAliases_ProcessesAreSeparate(t *testing.T) {
	ms := append(aliasMessages(),
		ConfigMessage{Name: "GatewayConfig", Binary: "gateway", Fields: []ConfigField{
			{Name: "environment", ProtoType: "string", EnvVar: "GATEWAY_ENV", EnvVarAliases: []EnvVarAlias{{Name: "NODE_ENV"}}},
		}})
	if err := ValidateConfigEnvAliases(ms); err != nil {
		t.Fatalf("two binaries reading one alias were refused: %v", err)
	}

	ms = aliasMessages()
	ms[2].Fields = append(ms[2].Fields, ConfigField{Name: "env", ProtoType: "string", EnvVar: "API_ENV",
		EnvVarAliases: []EnvVarAlias{{Name: "NODE_ENV"}}})
	if err := ValidateConfigEnvAliases(ms); err == nil {
		t.Fatal("a command config re-reading its root config's alias must be refused: the process loads both")
	}
}

// TestValidateConfigEnvAliases_ReportsEveryProblem: like the binding checks,
// every offender is listed in one run rather than one per generate.
func TestValidateConfigEnvAliases_ReportsEveryProblem(t *testing.T) {
	ms := aliasMessages()
	ms[0].Fields[0].EnvVarAliases = append(ms[0].Fields[0].EnvVarAliases, EnvVarAlias{Name: "RELIANT_ENV"})
	ms[1].Fields[0].EnvVarAliases = append(ms[1].Fields[0].EnvVarAliases, EnvVarAlias{Name: "9LIVES"})
	err := ValidateConfigEnvAliases(ms)
	if err == nil || !strings.HasPrefix(err.Error(), "2 (forge.v1.config).env_var_aliases problem(s)") {
		t.Fatalf("want both problems in one error, got %v", err)
	}
}
