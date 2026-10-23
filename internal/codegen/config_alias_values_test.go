package codegen

import (
	"strings"
	"testing"
)

// A value-mapped alias's map is checked at generate time against the field it
// sets, so a mapping the loader would refuse never reaches a process start.
func TestValidateConfigEnvAliases_ValueMaps(t *testing.T) {
	level := func(aliases ...EnvVarAlias) []ConfigMessage {
		return []ConfigMessage{{Name: "LogConfig", Binary: "reliant", Fields: []ConfigField{{
			Name: "level", ProtoType: "string", EnvVar: "RELIANT_LOG_LEVEL",
			AllowedValues: []string{"debug", "info"}, EnvVarAliases: aliases,
		}}}}
	}
	if err := ValidateConfigEnvAliases(level(EnvVarAlias{Name: "DEBUG", Values: map[string]string{"true": "debug", "1": "debug"}})); err != nil {
		t.Fatalf("a valid value map was refused: %v", err)
	}
	for _, c := range []struct {
		name string
		msgs []ConfigMessage
		want string
	}{
		{"outside allowed_values", level(EnvVarAlias{Name: "DEBUG", Values: map[string]string{"true": "trace"}}),
			`alias DEBUG maps "true" to "trace", which is not one of the field's allowed_values [debug info]`},
		{"empty key", level(EnvVarAlias{Name: "DEBUG", Values: map[string]string{"": "debug"}}),
			"alias DEBUG maps the empty value, which never matches"},
		{"not the field's kind", []ConfigMessage{{Name: "C", Binary: "reliant", Fields: []ConfigField{{
			Name: "verbose", ProtoType: "bool", EnvVar: "APP_VERBOSE",
			EnvVarAliases: []EnvVarAlias{{Name: "CI", Values: map[string]string{"true": "yes"}}},
		}}}}, `alias CI maps "true" to "yes", which is not a valid bool`},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateConfigEnvAliases(c.msgs)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want a refusal containing %q, got: %v", c.want, err)
			}
		})
	}
}
