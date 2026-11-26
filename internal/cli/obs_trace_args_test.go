package cli

import (
	"strings"
	"testing"
)

// Forge report 22553dc3: `obs trace prod <trace-id>` looked the id up as an
// env. Both orders work; the documented one keeps working; a pair forge cannot
// order is a usage error, never a lookup of the wrong thing.
func TestObsTraceArgs(t *testing.T) {
	const id = "890b36d25dabb673d68750c1cefdc8bc"
	const id64 = "890b36d25dabb673"
	for _, tc := range []struct {
		args            []string
		wantID, wantEnv string
		wantErr         string
	}{
		{args: []string{id}, wantID: id, wantEnv: "dev"},
		{args: []string{id, "prod"}, wantID: id, wantEnv: "prod"},
		{args: []string{"prod", id}, wantID: id, wantEnv: "prod"},
		{args: []string{"prod", id64}, wantID: id64, wantEnv: "prod"},
		{args: []string{"e2e", id}, wantID: id, wantEnv: "e2e"}, // an all-hex env name
		{args: []string{id, "e2e"}, wantID: id, wantEnv: "e2e"},
		{args: []string{"abc123", "prod"}, wantID: "abc123", wantEnv: "prod"}, // a short id, documented order
		{args: []string{"prod", "abc123"}, wantID: "abc123", wantEnv: "prod"},
		{args: []string{id, ""}, wantID: id, wantEnv: "dev"},
		{args: []string{"prod"}, wantErr: `"prod" is not a trace id`},
		{args: []string{"prod", "staging"}, wantErr: "neither"},
		{args: []string{id, id64}, wantErr: "both"},
	} {
		gotID, gotEnv, err := obsTraceArgs(tc.args)
		name := strings.Join(tc.args, " ")
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "obs trace <trace-id> [env]") {
				t.Errorf("obs trace %s: err = %v, want a usage error containing %q", name, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("obs trace %s: %v", name, err)
			continue
		}
		if gotID != tc.wantID || gotEnv != tc.wantEnv {
			t.Errorf("obs trace %s: (id, env) = (%q, %q), want (%q, %q)", name, gotID, gotEnv, tc.wantID, tc.wantEnv)
		}
	}
}
