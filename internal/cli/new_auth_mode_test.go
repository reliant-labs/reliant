package cli

import (
	"strings"
	"testing"
)

func TestResolveNewAuthMode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		changed   bool
		value     string
		kind      string
		frontends []string
		want      string
		wantErr   string
	}{
		// Not passed: "" keeps today's default whatever else is set.
		{name: "unset", want: ""},
		{name: "unset with a frontend", frontends: []string{"web"}, want: ""},
		{name: "none", changed: true, value: "none", frontends: []string{"web"}, want: authModeNone},
		{name: "native", changed: true, value: "native", frontends: []string{"web"}, want: authModeNative},
		{name: "case and space", changed: true, value: " None ", frontends: []string{"web"}, want: authModeNone},
		{name: "explicit service kind", changed: true, value: "none", kind: "service", frontends: []string{"web"}, want: authModeNone},
		// none with no frontend is what such a project gets anyway.
		{name: "none without a frontend", changed: true, value: "none", want: authModeNone},
		{name: "unknown value", changed: true, value: "jwt", frontends: []string{"web"}, wantErr: `invalid --auth-mode "jwt"`},
		{name: "empty value", changed: true, value: "", frontends: []string{"web"}, wantErr: "invalid --auth-mode"},
		{name: "site kind", changed: true, value: "none", kind: "site", wantErr: "--kind site has none to pick"},
		{name: "cli kind", changed: true, value: "none", kind: "cli", wantErr: "--kind cli has none to pick"},
		{name: "native needs a frontend", changed: true, value: "native", wantErr: "--auth-mode native needs a --frontend"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveNewAuthMode(tc.changed, tc.value, tc.kind, tc.frontends)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("mode = %q, want %q", got, tc.want)
			}
		})
	}
}

// The --auth-mode flag parses on the real command, so `forge start app` and
// the skills can advertise it.
func TestProjectNewHasAuthModeFlag(t *testing.T) {
	t.Parallel()
	f := newNewCmd().Flags().Lookup("auth-mode")
	if f == nil {
		t.Fatal("`forge project new` has no --auth-mode flag")
	}
	if f.DefValue != "" {
		t.Errorf("--auth-mode defaults to %q: it must stay optional, with today's behaviour as the default", f.DefValue)
	}
}

// project new always says which sign-in system it scaffolded, and names the
// flag that picks the other.
func TestNewAuthSummary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode        string
		hasFrontend bool
		want        []string
	}{
		{"", true, []string{"native sign-in", "--auth-mode none"}},
		{authModeNative, true, []string{"native sign-in", "--auth-mode none"}},
		{authModeNone, true, []string{"Auth: none", "auth_required: false", "401"}},
		{"", false, []string{"no sign-in system", "auth_required: false"}},
		{authModeNone, false, []string{"no sign-in system"}},
	} {
		got := newAuthSummary(tc.mode, tc.hasFrontend)
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("newAuthSummary(%q, %v) = %q, want it to mention %q", tc.mode, tc.hasFrontend, got, w)
			}
		}
	}
}
