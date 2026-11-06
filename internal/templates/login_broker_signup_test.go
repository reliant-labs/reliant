package templates

import (
	"go/format"
	"regexp"
	"strings"
	"testing"
)

// Public sign-up creates an account under an address nobody has verified and
// signs it straight in. An app that binds invitations or memberships by email
// is then open to takeover by whoever registers the address first, so the
// scaffolded broker serves POST /auth/register in development only, unless
// the deployed environment's config opts in with auth_self_signup.
func TestLoginBrokerScaffoldServesSignUpOnlyWhenOptedIn(t *testing.T) {
	for _, tc := range []struct {
		name       string
		data       map[string]any
		selfSignup string
	}{
		{
			name:       "config declares auth_self_signup",
			data:       map[string]any{"Module": "example.com/app", "Name": "app", "MiddlewareDeclaresSessionCookie": true, "SelfSignupField": true},
			selfSignup: `selfSignup: cfg.Environment == "development" || cfg.AuthSelfSignup,`,
		},
		{
			// An older config with no such field must still compile, and must
			// not serve sign-up anywhere but development.
			name:       "config predates auth_self_signup",
			data:       map[string]any{"Module": "example.com/app", "Name": "app", "MiddlewareDeclaresSessionCookie": true},
			selfSignup: `selfSignup: cfg.Environment == "development",`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ProjectTemplates().Render("app-login-broker.go.tmpl", tc.data)
			if err != nil {
				t.Fatal(err)
			}
			// The scaffold writes the file through gofmt, so formatting is
			// the bar, not byte equality.
			if _, err := format.Source(out); err != nil {
				t.Fatalf("the rendered broker does not format: %v", err)
			}
			src := string(out)

			if !strings.Contains(src, tc.selfSignup) {
				t.Errorf("broker does not decide sign-up as %s", tc.selfSignup)
			}
			if declared, _ := tc.data["SelfSignupField"].(bool); !declared && strings.Contains(src, "cfg.AuthSelfSignup") {
				t.Error("broker reads cfg.AuthSelfSignup from a config that does not declare it")
			}

			// The mount is conditional, and the condition is the switch.
			gated := regexp.MustCompile(`if broker\.selfSignup \{\s*mux\.Handle\(RegisterPath, http\.HandlerFunc\(broker\.handleRegister\)\)\s*\}`)
			if !gated.MatchString(src) {
				t.Error("RegisterPath is not mounted behind broker.selfSignup")
			}
			if n := strings.Count(src, "mux.Handle(RegisterPath,"); n != 1 {
				t.Errorf("RegisterPath is mounted %d times, want exactly once (behind the switch)", n)
			}

			// The sign-in screen reads the same switch, so it never offers a
			// button the server would answer with 404.
			if !strings.Contains(src, `OptionsPath        = "GET /auth/options"`) ||
				!strings.Contains(src, "mux.Handle(OptionsPath, http.HandlerFunc(broker.handleOptions))") {
				t.Error("broker does not serve GET /auth/options")
			}
			if !strings.Contains(src, "authOptions{SelfSignup: b.selfSignup}") {
				t.Error("GET /auth/options does not report the broker's own switch")
			}
		})
	}
}

// Both browser sign-in screens offer "Create an account" only when the server
// says it serves sign-up, and never enter sign-up mode otherwise.
func TestSignInScreensFollowTheServersSignUpSwitch(t *testing.T) {
	screens := map[string]string{
		"nextjs":   "src/app/auth/sign-in/page.tsx",
		"vite-spa": "src/routes.tsx",
	}
	for _, kind := range browserKinds {
		t.Run(kind, func(t *testing.T) {
			tree := authTreeFor(t, kind)
			if _, ok := tree["src/lib/auth/use-auth-options.ts"]; !ok {
				t.Fatalf("%s composed no src/lib/auth/use-auth-options.ts, which the sign-in screen imports", kind)
			}
			path := screens[kind]
			tmpl, ok := tree[path]
			if !ok {
				t.Fatalf("%s composed no %s", kind, path)
			}
			src := renderFrontend(t, tmpl, kind)
			for _, want := range []string{
				`import { useAuthOptions } from "@/lib/auth/use-auth-options";`,
				"const { selfSignup } = useAuthOptions();",
				`const signingUp = selfSignup && mode === "signUp";`,
				"{selfSignup ? (",
			} {
				if !strings.Contains(src, want) {
					t.Errorf("%s lacks %q", path, want)
				}
			}
			toggle := strings.Index(src, `"Create an account"`)
			gate := strings.Index(src, "{selfSignup ? (")
			if toggle < 0 || gate < 0 || gate > toggle {
				t.Errorf("the \"Create an account\" toggle in %s is not inside the selfSignup gate", path)
			}
		})
	}
}
