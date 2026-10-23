package codegen

import (
	"strings"
	"testing"
)

func familyField(name, prefix, suffix, valueType string) ConfigField {
	return ConfigField{Name: name, ProtoType: "message", EnvPrefix: prefix, EnvSuffix: suffix, MapValueType: valueType}
}

// reliant's shape: the OAuth client pair and a feature-flag family on the
// api server, beside plain fields under the same prefix.
func familyMessages() []ConfigMessage {
	return []ConfigMessage{
		{Name: "ReliantConfig", Binary: "reliant", Fields: []ConfigField{
			{Name: "log_level", ProtoType: "string", EnvVar: "RELIANT_LOG_LEVEL"},
		}},
		{Name: "ApiServerConfig", Binary: "reliant", Command: []string{"server", "api"}, Fields: []ConfigField{
			familyField("oauth_client_ids", "RELIANT_OAUTH_", "_CLIENT_ID", "string"),
			familyField("oauth_client_secrets", "RELIANT_OAUTH_", "_CLIENT_SECRET", "string"),
			familyField("features", "RELIANT_FEATURE_", "", "bool"),
			{Name: "oauth_issuer", ProtoType: "string", EnvVar: "RELIANT_OAUTH_ISSUER"},
		}},
	}
}

func TestValidateConfigEnvFamilies_AcceptsDistinctFamilies(t *testing.T) {
	if err := ValidateConfigEnvFamilies(familyMessages()); err != nil {
		t.Fatalf("a valid family set was refused: %v", err)
	}
}

func TestValidateConfigEnvFamilies_Refusals(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func([]ConfigMessage) []ConfigMessage
		want   string
	}{
		{"suffix without prefix", func(ms []ConfigMessage) []ConfigMessage {
			ms[1].Fields = append(ms[1].Fields, familyField("x", "", "_X", "string"))
			return ms
		}, "ApiServerConfig.x: env_suffix without env_prefix"},
		{"not a map of scalars", func(ms []ConfigMessage) []ConfigMessage {
			ms[1].Fields = append(ms[1].Fields, familyField("x", "RELIANT_X_", "", "message"))
			return ms
		}, "ApiServerConfig.x: env_prefix needs a map<string, V> field with a scalar V"},
		{"not a map at all", func(ms []ConfigMessage) []ConfigMessage {
			ms[1].Fields = append(ms[1].Fields, familyField("x", "RELIANT_X_", "", ""))
			return ms
		}, "ApiServerConfig.x: env_prefix needs a map<string, V> field"},
		{"also a flag and env_var", func(ms []ConfigMessage) []ConfigMessage {
			f := familyField("x", "RELIANT_X_", "", "string")
			f.EnvVar, f.Flag = "RELIANT_X", "x"
			ms[1].Fields = append(ms[1].Fields, f)
			return ms
		}, "an env_prefix family takes no env_var or flag"},
		{"bad prefix", func(ms []ConfigMessage) []ConfigMessage {
			ms[1].Fields = append(ms[1].Fields, familyField("x", "9X-", "", "string"))
			return ms
		}, `env_prefix "9X-" does not begin an environment variable name`},
		{"two families claim one name", func(ms []ConfigMessage) []ConfigMessage {
			ms[1].Fields = append(ms[1].Fields, familyField("ids", "RELIANT_OAUTH_", "_ID", "string"))
			return ms
		}, "The `reliant server api` process reads two env families that can claim the same variable: RELIANT_OAUTH_<KEY>_CLIENT_ID (ApiServerConfig.oauth_client_ids) and RELIANT_OAUTH_<KEY>_ID"},
		{"a family claims another config's env_var in the same process", func(ms []ConfigMessage) []ConfigMessage {
			ms[0].Fields = append(ms[0].Fields, ConfigField{Name: "beta", ProtoType: "bool", EnvVar: "RELIANT_FEATURE_BETA"})
			return ms
		}, "reads RELIANT_FEATURE_BETA as a name of ReliantConfig.beta, and the env family RELIANT_FEATURE_<KEY> (ApiServerConfig.features) claims it too"},
		{"on a frontend config", func(ms []ConfigMessage) []ConfigMessage {
			return append(ms, ConfigMessage{Name: "WebConfig", Frontend: "web", Fields: []ConfigField{familyField("flags", "WEB_FLAG_", "", "bool")}})
		}, `a frontend config field takes no env_prefix — frontend "web"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateConfigEnvFamilies(c.mutate(familyMessages()))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want a refusal containing %q, got: %v", c.want, err)
			}
		})
	}
}

// A family is not projected into KCL: an open-ended set of variables has no
// schema shape, so it is set on a workload's env directly.
func TestEnvFamiliesAreNotProjectedIntoKCL(t *testing.T) {
	fields := FlattenFieldsWithBlocks(familyMessages()[1].Fields, familyMessages())
	for _, f := range fields {
		if f.EnvPrefix != "" {
			t.Errorf("family %s reached the KCL field list", f.Name)
		}
	}
	if len(fields) != 1 || fields[0].Name != "oauth_issuer" {
		t.Errorf("KCL fields = %v, want only the plain field", fields)
	}
}
