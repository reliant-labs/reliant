package templates

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// The scaffolded login broker's password routes are exercised end to end
// against a real identity provider (internal/cli, TestE2ENativeSignInPassword
// PolicyAndResetAgainstZitadel), which only runs in `task test:e2e`. This is the
// part of that contract a `-short` run can hold: the rendered file is valid Go,
// serves the routes the scaffolded frontend is written against, and does not
// bring back the sentence that named no requirement.
func TestLoginBrokerScaffoldServesThePasswordRoutes(t *testing.T) {
	for _, tc := range []struct {
		name string
		data map[string]any
	}{
		{"current middleware", map[string]any{"Module": "example.com/app", "Name": "app", "MiddlewareDeclaresSessionCookie": true}},
		{"legacy middleware", map[string]any{"Module": "example.com/app", "Name": "app", "MiddlewareDeclaresSessionCookie": false}},
		{"command config", map[string]any{"Module": "example.com/app", "Name": "app", "MiddlewareDeclaresSessionCookie": true, "ConfigType": "ServerConfig"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ProjectTemplates().Render("app-login-broker.go.tmpl", tc.data)
			if err != nil {
				t.Fatal(err)
			}
			src := string(out)
			if _, err := parser.ParseFile(token.NewFileSet(), "login_broker.go", src, parser.AllErrors); err != nil {
				t.Fatalf("the rendered broker does not parse: %v", err)
			}

			// The routes the frontend's native-login.ts posts to.
			for _, route := range []string{
				`LoginPath          = "POST /auth/login"`,
				`PasswordPolicyPath = "GET /auth/password-policy"`,
				`ForgotPath         = "POST /auth/forgot"`,
				`ResetPath          = "POST /auth/reset"`,
			} {
				if !strings.Contains(src, route) {
					t.Errorf("broker does not declare %s", route)
				}
			}
			for _, mount := range []string{"PasswordPolicyPath", "RegisterPath", "ForgotPath", "ResetPath"} {
				if !strings.Contains(src, "mux.Handle("+mount+",") {
					t.Errorf("broker never mounts %s", mount)
				}
			}

			// The sentence the owner hit. A rejected password names its rules.
			if strings.Contains(src, "does not meet the requirements") {
				t.Error("the broker answers a rejected password with the generic sentence again")
			}
			if !strings.Contains(src, `"password_policy"`) || !strings.Contains(src, "writePasswordRejection") {
				t.Error("a rejected password no longer reaches writePasswordRejection / code password_policy")
			}

			// The reset hazard: the broker's credential can set any password with no code.
			for _, guard := range []string{
				`code == ""`,          // an empty code is refused here, before the issuer
				"resetByUser.reserve", // guesses are capped per account
				"forgotByIP.reserve",  // and requests per client
				"forgotByAddress.reserve",
			} {
				if !strings.Contains(src, guard) {
					t.Errorf("the reset flow lost %q", guard)
				}
			}
			// The reset code is a credential: it is logged in development and never answered.
			if strings.Contains(src, `"code": code`) || strings.Contains(src, "map[string]string{\"code\"") {
				t.Error("a handler appears to return a reset code in a response")
			}

			// Honest about delivery, and about how sign-up is turned off.
			for _, truth := range []string{"forge.ManagedIdentity.smtp", "PUBLIC BY DEFAULT", "INVITE-ONLY"} {
				if !strings.Contains(strings.ToUpper(src), strings.ToUpper(truth)) {
					t.Errorf("the broker's guidance no longer mentions %q", truth)
				}
			}
			if strings.Contains(src, "platform relay") {
				t.Error("the broker repeats the platform-relay claim: no instance sends mail unless it declares smtp")
			}
		})
	}
}
